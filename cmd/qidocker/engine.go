package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type manager struct {
	state, target  string
	transportCache sync.Map
	client         *http.Client
	mu             sync.Mutex
	applied        proxySettings
	process        *os.Process
	restart        chan struct{}
}

var proxyUserinfo = regexp.MustCompile(`(?i)(https?://)[^/\s"@]+@`)

func (m *manager) start(proxies proxySettings) (*exec.Cmd, error) {
	if err := os.MkdirAll(filepath.Join(m.state, "run"), 0700); err != nil {
		return nil, err
	}
	config := map[string]any{
		"hosts":     []string{"unix://" + filepath.Join(m.state, "run", "docker.sock")},
		"data-root": filepath.Join(m.state, "data"), "exec-root": filepath.Join(m.state, "run", "engine"),
		"pidfile": filepath.Join(m.state, "run", "dockerd.pid"), "init-path": packagePath(m.target, "docker-init"),
		"group": "root", "bridge": "none", "iptables": false, "ip6tables": false,
		"ip-forward": false, "ip-masq": false, "userland-proxy-path": packagePath(m.target, "docker-proxy"),
		"log-driver": "local", "log-opts": map[string]string{"max-size": "20m", "max-file": "3"},
	}
	if err := privateJSON(filepath.Join(m.state, "engine.json"), config); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(m.state, "engine.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(packagePath(m.target, "dockerd"), "--config-file", filepath.Join(m.state, "engine.json"))
	cmd.Env = []string{
		"PATH=" + filepath.Join(m.target, "bin") + ":/usr/syno/sbin:/usr/syno/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + m.state, "HTTP_PROXY=" + proxies.HTTP, "HTTPS_PROXY=" + proxies.HTTPS, "NO_PROXY=" + proxies.NoProxy,
	}
	cmd.Env = append(cmd.Env, "SSL_CERT_DIR=/etc/ssl/certs:/etc/pki/tls/certs:"+filepath.Join(m.target, "certs")+":"+filepath.Join(m.state, "certs"))
	reader, writer, err := os.Pipe()
	if err != nil {
		log.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	err = cmd.Start()
	writer.Close()
	if err != nil {
		reader.Close()
		log.Close()
		return nil, err
	}
	go func() {
		defer reader.Close()
		defer log.Close()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := proxyUserinfo.ReplaceAllString(scanner.Text(), "${1}[redacted]@")
			for _, address := range []string{proxies.HTTP, proxies.HTTPS} {
				if parsed, err := url.Parse(address); err == nil && parsed.User != nil {
					if password, ok := parsed.User.Password(); ok && password != "" {
						line = strings.ReplaceAll(line, password, "[redacted]")
					}
					line = strings.ReplaceAll(line, parsed.User.String(), "[redacted]")
				}
			}
			fmt.Fprintln(log, line)
		}
		drain(reader)
	}()
	return cmd, nil
}

func (m *manager) desired() (proxySettings, error) {
	settings, err := readSettings(filepath.Join(m.state, "proxy.json"))
	if err != nil {
		return settings, err
	}
	resolved, err := resolveProxy(settings, dsmPaths...)
	if err == nil && settings.Mode == "dsm" {
		resolved.HTTP = applyDSMProxyAlias(resolved.HTTP, dsmTLSAliasSource, dsmTLSAliasDestination)
		resolved.HTTPS = applyDSMProxyAlias(resolved.HTTPS, dsmTLSAliasSource, dsmTLSAliasDestination)
		resolved.HTTP = preferDSMProxyTLS(resolved.HTTP, m.dsmProxyUsesTLS)
		resolved.HTTPS = preferDSMProxyTLS(resolved.HTTPS, m.dsmProxyUsesTLS)
		if strings.HasPrefix(resolved.HTTP, "http://") || strings.HasPrefix(resolved.HTTPS, "http://") {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if candidate := nativeDockerTLSProxy(ctx); candidate != "" {
				lookup := func(host string) ([]net.IPAddr, error) {
					return net.DefaultResolver.LookupIPAddr(ctx, host)
				}
				resolved.HTTP = preferNativeTLSProxy(resolved.HTTP, candidate, lookup)
				if resolved.HTTPS != resolved.HTTP {
					resolved.HTTPS = preferNativeTLSProxy(resolved.HTTPS, candidate, lookup)
				}
			}
			cancel()
		}
	}
	return resolved, err
}

type dsmProxyProbe struct {
	tls    bool
	expiry time.Time
}

func (m *manager) dsmProxyUsesTLS(endpoint string) bool {
	if cached, ok := m.transportCache.Load(endpoint); ok {
		probe := cached.(dsmProxyProbe)
		if probe.tls || time.Now().Before(probe.expiry) {
			return probe.tls
		}
	}
	roots, err := proxyTrustRoots(filepath.Join(m.target, "certs", "isrg-root-x1.pem"), filepath.Join(m.state, "certs", "proxy-ca.pem"))
	useTLS := true
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		useTLS = probeDSMProxyTLS(ctx, endpoint, roots)
		cancel()
	}
	m.transportCache.Store(endpoint, dsmProxyProbe{tls: useTLS, expiry: time.Now().Add(time.Minute)})
	return useTLS
}

func (m *manager) request(ctx context.Context, method, path string, body io.Reader, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header = headers
	return m.client.Do(req)
}

func (m *manager) running(ctx context.Context) (bool, error) {
	res, err := m.request(ctx, "GET", "/containers/json", nil, nil)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return false, errors.New("容器状态无法读取")
	}
	var containers []json.RawMessage
	err = json.NewDecoder(res.Body).Decode(&containers)
	return len(containers) > 0, err
}

func stop(cmd *exec.Cmd, done <-chan error) {
	signalProcess(cmd.Process, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(35 * time.Second):
		signalProcess(cmd.Process, syscall.SIGKILL)
		<-done
	}
}

func (m *manager) supervise(ctx context.Context) {
	moduleCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	loadBridgeModules(moduleCtx)
	cancel()
	for ctx.Err() == nil {
		m.mu.Lock()
		settings, err := m.desired()
		var cmd *exec.Cmd
		if err == nil {
			cmd, err = m.start(settings)
		}
		if err == nil {
			m.applied, m.process = settings, cmd.Process
		}
		m.mu.Unlock()
		if err != nil {
			fmt.Fprintln(os.Stderr, "QiDocker 引擎启动失败")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		tick := time.NewTicker(15 * time.Second)
		monitor := true
		for monitor {
			select {
			case <-ctx.Done():
				stop(cmd, done)
				monitor = false
			case <-done:
				monitor = false
			case <-m.restart:
				stop(cmd, done)
				monitor = false
			case <-tick.C:
				m.mu.Lock()
				check, cancel := context.WithTimeout(ctx, 3*time.Second)
				want, err := m.desired()
				if err == nil && want != m.applied {
					busy, err := m.running(check)
					if err == nil && !busy {
						stop(cmd, done)
						monitor = false
					}
				}
				cancel()
				m.mu.Unlock()
			}
		}
		tick.Stop()
		m.mu.Lock()
		m.process = nil
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func serve(state, target string) error {
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(state, "supervisor.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return err
	}
	controlPath := filepath.Join(target, ".control.sock")
	socket, err := createSocket(controlPath, 0600)
	if err != nil {
		return err
	}
	defer socket.Close()
	if group, e := user.LookupGroup("http"); e == nil {
		gid, e := strconv.Atoi(group.Gid)
		if e != nil {
			return e
		}
		if e = os.Chown(controlPath, -1, gid); e != nil {
			return e
		}
		if e = os.Chmod(controlPath, 0660); e != nil {
			return e
		}
	}
	m := &manager{state: state, target: target, client: unixClient(filepath.Join(state, "run", "docker.sock")), restart: make(chan struct{}, 1)}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	stopped := make(chan struct{})
	go func() { defer close(stopped); m.supervise(ctx) }()
	go m.watchBridges(ctx)
	server := &http.Server{Handler: http.HandlerFunc(m.handle), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() { <-ctx.Done(); server.Close() }()
	err = server.Serve(socket)
	cancel()
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (m *manager) status(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	settings, err := m.desired()
	active := false
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if res, e := m.request(ctx, "GET", "/_ping", nil, nil); e == nil {
		active = res.StatusCode == 200
		res.Body.Close()
	}
	writeJSON(w, map[string]any{"online": active, "proxy": settings.visible(), "proxy_pending": err == nil && settings != m.applied, "proxy_invalid": err != nil,
		"proxy_configured": err == nil && (settings.HTTP != "" || settings.HTTPS != "")})
}

func (m *manager) proxyAction(w http.ResponseWriter, r *http.Request, action string) {
	path := filepath.Join(m.state, "proxy.json")
	switch action {
	case "proxy-save":
		var settings proxySettings
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&settings) != nil {
			http.Error(w, "无效代理配置", 400)
			return
		}
		if settings.Mode == "dsm" {
			settings = proxySettings{Mode: "dsm"}
		} else if settings.Mode == "direct" {
			settings = proxySettings{Mode: "direct"}
		} else if _, err := resolveProxy(settings, dsmPaths...); err != nil {
			http.Error(w, "无效代理配置", 400)
			return
		}
		m.mu.Lock()
		previous, err := readSettings(path)
		if err == nil && settings.Mode == "custom" && previous.Mode == "custom" && settings.Username == previous.Username && settings.Username != "" && settings.Password == "" {
			settings.Password = previous.Password
		}
		if err == nil {
			err = privateJSON(path, settings)
		}
		m.mu.Unlock()
		if err != nil {
			http.Error(w, "代理配置保存失败", 500)
			return
		}
		writeJSON(w, map[string]bool{"saved": true})
	case "proxy-test":
		settings, err := m.desired()
		if err != nil {
			http.Error(w, "代理配置无法读取", 422)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 36*time.Second)
		defer cancel()
		writeJSON(w, testProxy(ctx, settings, filepath.Join(m.target, "certs", "isrg-root-x1.pem"), filepath.Join(m.state, "certs", "proxy-ca.pem")))
	case "proxy-apply":
		if _, err := m.desired(); err != nil {
			http.Error(w, "代理配置无法读取", 422)
			return
		}
		select {
		case m.restart <- struct{}{}:
		default:
		}
		writeJSON(w, map[string]bool{"scheduled": true})
	}
}

func engineJSON(value any) io.Reader {
	encoded, _ := json.Marshal(value)
	return bytes.NewReader(encoded)
}
