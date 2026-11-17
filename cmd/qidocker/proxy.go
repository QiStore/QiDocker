package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type proxySettings struct {
	Mode     string `json:"mode"`
	HTTP     string `json:"http"`
	HTTPS    string `json:"https"`
	NoProxy  string `json:"no_proxy"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func privateJSON(path string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}

func readSettings(path string) (proxySettings, error) {
	var p proxySettings
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return proxySettings{Mode: "dsm"}, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(data, &p)
	return p, err
}

// Read assignments as data. Never source configuration files as shell code.
func readAssignments(paths ...string) (map[string]string, error) {
	values := make(map[string]string)
	for _, path := range paths {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			line = strings.TrimPrefix(line, "export ")
			key, value, ok := strings.Cut(line, "=")
			if !ok || strings.HasPrefix(key, "#") {
				continue
			}
			key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
			if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[0] == value[len(value)-1] {
				value = value[1 : len(value)-1]
			}
			values[key] = value
		}
		err = scanner.Err()
		file.Close()
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

// DSM stores the proxy endpoint and optional credentials as separate keys.
func dsmProxySettings(values map[string]string) proxySettings {
	p := proxySettings{Mode: "dsm"}
	enabled, native := values["proxy_enabled"]
	if !native {
		p.HTTP, p.HTTPS, p.NoProxy = values["http_proxy"], values["https_proxy"], values["no_proxy"]
		p.Username, p.Password = values["proxy_username"], values["proxy_password"]
		return p
	}
	if enabled != "yes" {
		return p
	}
	endpoint := func(host, port string) string {
		if host == "" {
			return ""
		}
		host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		if port != "" {
			host = net.JoinHostPort(host, port)
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		return "http://" + host
	}
	p.HTTP = endpoint(values["http_host"], values["http_port"])
	p.HTTPS = endpoint(values["https_host"], values["https_port"])
	if p.HTTPS == "" {
		p.HTTPS = p.HTTP
	}
	p.NoProxy = values["no_proxy"]
	if values["auth_enabled"] == "yes" {
		p.Username, p.Password = values["proxy_user"], values["proxy_pwd"]
	}
	return p
}

func resolveProxy(p proxySettings, dsmPaths ...string) (proxySettings, error) {
	switch p.Mode {
	case "dsm":
		values, err := readAssignments(dsmPaths...)
		if err != nil {
			return p, err
		}
		p = dsmProxySettings(values)
	case "custom":
	case "direct":
		return proxySettings{Mode: "direct"}, nil
	default:
		return p, errors.New("无效的代理模式")
	}
	for _, address := range []*string{&p.HTTP, &p.HTTPS} {
		if *address == "" {
			continue
		}
		u, err := url.Parse(*address)
		if err != nil || u.Hostname() == "" || u.Opaque != "" ||
			(u.Scheme != "http" && u.Scheme != "https") ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return proxySettings{}, errors.New("代理地址必须为 HTTP 或 HTTPS URL")
		}
		if p.Username != "" {
			u.User = url.UserPassword(p.Username, p.Password)
		}
		*address = u.String()
	}
	return p, nil
}

func (p proxySettings) visible() proxySettings {
	p.Password = ""
	for _, address := range []*string{&p.HTTP, &p.HTTPS} {
		if u, err := url.Parse(*address); err == nil {
			u.User = nil
			*address = u.String()
		} else {
			*address = ""
		}
	}
	return p
}

type probeResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func proxyErrorDetail(err error) string {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var network net.Error
	switch {
	case errors.As(err, &unknown):
		return "证书签发方不受信任"
	case errors.As(err, &hostname):
		return "证书域名与连接地址不匹配"
	case errors.As(err, &invalid):
		return "证书无效或已过期"
	case errors.Is(err, syscall.ECONNRESET):
		return "连接被重置（TCP RST）"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "对端关闭连接"
	case errors.As(err, &network) && network.Timeout():
		return "连接超时"
	default:
		return "连接失败"
	}
}

// Probe the same host and port without proxy authentication. An explicitly
// configured HTTPS address is never rewritten and credentials are not probed.
// Use the official Docker daemon's configured TLS route as a read-only
// hint. Its credentials are never used for QiDocker.
func nativeDockerTLSProxy(ctx context.Context) string {
	const socket = "/var/run/docker.sock"
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return ""
	}
	client := unixClient(socket)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/info", nil)
	if err != nil {
		return ""
	}
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	var native struct {
		HTTPSProxy string
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&native) != nil {
		return ""
	}
	u, err := url.Parse(native.HTTPSProxy)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" ||
		(u.Path != "" && u.Path != "/") || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	u.User = nil
	return u.String()
}

func preferNativeTLSProxy(address, candidate string, lookup func(string) ([]net.IPAddr, error)) string {
	dsm, err := url.Parse(address)
	if err != nil || dsm.Scheme != "http" || dsm.Hostname() == "" {
		return address
	}
	tlsProxy, err := url.Parse(candidate)
	if err != nil || tlsProxy.Scheme != "https" || tlsProxy.Hostname() == "" {
		return address
	}
	if !strings.EqualFold(dsm.Hostname(), tlsProxy.Hostname()) {
		dsmIPs, dsmErr := lookup(dsm.Hostname())
		tlsIPs, tlsErr := lookup(tlsProxy.Hostname())
		if dsmErr != nil || tlsErr != nil || len(tlsIPs) == 0 {
			return address
		}
		known := make(map[string]bool, len(dsmIPs))
		for _, ip := range dsmIPs {
			known[ip.String()] = true
		}
		for _, ip := range tlsIPs {
			if !known[ip.String()] {
				return address
			}
		}
	}
	tlsProxy.User = dsm.User
	return tlsProxy.String()
}

func preferDSMProxyTLS(address string, detect func(string) bool) string {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" {
		return address
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if detect(net.JoinHostPort(u.Hostname(), port)) {
		u.Scheme = "https"
		// A shared listener keeps its original port, including implicit HTTP 80.
		u.Host = net.JoinHostPort(u.Hostname(), port)
		return u.String()
	}
	return address
}

func probeDSMProxyTLS(ctx context.Context, endpoint string, roots *x509.CertPool) bool {
	dialer := tls.Dialer{Config: &tls.Config{RootCAs: roots}}
	conn, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err == nil {
		conn.Close()
		return true
	}
	// A certificate failure proves TLS is present. Keep TLS and let the
	// existing diagnostics report why the certificate cannot be verified.
	var verification *tls.CertificateVerificationError
	var alert tls.AlertError
	return errors.As(err, &verification) || errors.As(err, &alert)
}

func proxyTrustRoots(caPaths ...string) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	for _, caPath := range caPaths {
		pem, err := os.ReadFile(caPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("无法加载受信任的 CA 证书")
		}
	}
	return roots, nil
}

func testProxy(ctx context.Context, p proxySettings, caPaths ...string) []probeResult {
	var result []probeResult
	add := func(name string, ok bool, detail string) {
		result = append(result, probeResult{name, ok, detail})
	}
	address := p.HTTPS
	if address == "" {
		address = p.HTTP
	}
	if p.Mode == "dsm" && address == "" {
		add("DSM 代理", false, "QiDocker 未读取到 DSM 代理地址，本次未执行连接测试")
		return result
	}
	proxy, _ := url.Parse(address)
	dialer := net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 7 * time.Second}
	defer transport.CloseIdleConnections()
	roots, err := proxyTrustRoots(caPaths...)
	if err != nil {
		add("CA 证书", false, err.Error())
		return result
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	if address != "" {
		_, err := net.DefaultResolver.LookupHost(ctx, proxy.Hostname())
		add("代理 DNS", err == nil, "代理主机解析")
		port := proxy.Port()
		if port == "" {
			if proxy.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		endpoint := net.JoinHostPort(proxy.Hostname(), port)
		conn, err := dialer.DialContext(ctx, "tcp", endpoint)
		if err != nil {
			add("代理 TCP", false, proxyErrorDetail(err))
			return result
		}
		conn.Close()
		add("代理 TCP", true, "代理端口连接")
		if proxy.Scheme == "https" {
			probeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
			tlsDialer := tls.Dialer{NetDialer: &dialer, Config: transport.TLSClientConfig}
			conn, err := tlsDialer.DialContext(probeCtx, "tcp", endpoint)
			cancel()
			if err != nil {
				add("代理 TLS", false, proxyErrorDetail(err))
				return result
			}
			conn.Close()
			add("代理 TLS", true, "代理证书和 TLS 握手通过")
		}
		transport.Proxy = http.ProxyURL(proxy)
		transport.OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, res *http.Response) error {
			ok := res.StatusCode == http.StatusOK
			add("HTTP CONNECT", ok, fmt.Sprintf("HTTP %d", res.StatusCode))
			if proxy.User != nil || res.StatusCode == http.StatusProxyAuthRequired {
				add("代理认证", ok, fmt.Sprintf("HTTP %d", res.StatusCode))
			}
			return nil
		}
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, check := range []struct {
		name, endpoint string
		expect         int
	}{
		{"Docker Registry", "https://registry-1.docker.io/v2/", 401},
		{"Docker Auth", "https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/hello-world:pull", 200},
	} {
		req, _ := http.NewRequestWithContext(ctx, "GET", check.endpoint, nil)
		res, err := client.Do(req)
		if err != nil {
			add(check.name, false, proxyErrorDetail(err))
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		add(check.name+" TLS", res.TLS != nil && res.TLS.Version >= tls.VersionTLS12, "HTTPS 握手")
		add(check.name, res.StatusCode == check.expect, fmt.Sprintf("HTTP %d", res.StatusCode))
	}
	return result
}
