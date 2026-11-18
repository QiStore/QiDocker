package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DSM 6.2 can ship kernel modules in a flat directory without a modprobe index.
func loadBridgeModules(ctx context.Context) {
	for _, module := range []string{"llc", "stp", "bridge", "veth"} {
		if _, err := os.Stat("/sys/module/" + module); err == nil {
			continue
		}
		if exec.CommandContext(ctx, "/sbin/modprobe", module).Run() == nil {
			continue
		}
		if _, err := os.Stat("/lib/modules/" + module + ".ko"); err == nil {
			if out, err := exec.CommandContext(ctx, "/sbin/insmod", "/lib/modules/"+module+".ko").CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "QiDocker kernel module %s: %s\n", module, strings.TrimSpace(string(out)))
			}
		}
	}
}

type engineNetwork struct {
	ID, Driver string
	Options    map[string]string
	IPAM       struct {
		Config []struct{ Subnet, Gateway string }
	}
}

// Restrict host changes to the default interface name of networks owned by
// this dockerd. An explicitly selected interface may belong to another service.
func (n engineNetwork) bridgeName() string {
	if n.Driver != "bridge" || len(n.ID) != 64 {
		return ""
	}
	for _, c := range n.ID {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	name := "br-" + n.ID[:12]
	if option := n.Options["com.docker.network.bridge.name"]; option != "" && option != name {
		return ""
	}
	return name
}

func (m *manager) reconcileBridges(ctx context.Context) error {
	res, err := m.request(ctx, http.MethodGet, "/networks", nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("list engine networks: %s", res.Status)
	}
	var networks []engineNetwork
	if err := json.NewDecoder(res.Body).Decode(&networks); err != nil {
		return err
	}
	var failures []string
	for _, network := range networks {
		name := network.bridgeName()
		if name == "" {
			continue
		}
		if _, err := os.Stat("/sys/class/net/" + name + "/bridge"); err != nil {
			continue
		}
		if err := repairBridge(ctx, name, network); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func repairBridge(ctx context.Context, name string, network engineNetwork) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return err
	}
	for _, config := range network.IPAM.Config {
		ip := net.ParseIP(config.Gateway)
		_, subnet, err := net.ParseCIDR(config.Subnet)
		if err != nil || ip.To4() == nil || !subnet.Contains(ip) {
			continue
		}
		bits, _ := subnet.Mask.Size()
		gateway := fmt.Sprintf("%s/%d", ip, bits)
		for _, addr := range addresses {
			if addr.String() != gateway {
				continue
			}
			out, err := exec.CommandContext(ctx, "/sbin/ip", "-4", "route", "show", "exact", subnet.String(), "dev", name).Output()
			if err == nil && len(out) != 0 {
				break
			}
			// Use add so an existing route on another interface is never replaced.
			if out, err := exec.CommandContext(ctx, "/sbin/ip", "-4", "route", "add", subnet.String(), "dev", name, "scope", "link", "src", ip.String()).CombinedOutput(); err != nil {
				return fmt.Errorf("%s route %s: %s (%w)", name, subnet, strings.TrimSpace(string(out)), err)
			}
			break
		}
	}
	flags, err := os.ReadFile("/sys/class/net/" + name + "/flags")
	if err != nil {
		return err
	}
	mask, err := strconv.ParseUint(strings.TrimSpace(string(flags)), 0, 32)
	if err != nil {
		return err
	}
	if mask&syscall.IFF_PROMISC == 0 {
		if out, err := exec.CommandContext(ctx, "/sbin/ip", "link", "set", "dev", name, "promisc", "on").CombinedOutput(); err != nil {
			return fmt.Errorf("%s promisc: %s (%w)", name, strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func (m *manager) watchBridges(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var lastError string
	for {
		check, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := m.reconcileBridges(check)
		cancel()
		if err == nil {
			lastError = ""
		} else if err.Error() != lastError && ctx.Err() == nil {
			lastError = err.Error()
			fmt.Fprintln(os.Stderr, "QiDocker bridge:", lastError)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
