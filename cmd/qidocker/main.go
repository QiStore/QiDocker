package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const home = "/var/packages/QiDocker"
const authCGI = "/usr/syno/synoman/webman/modules/authenticate.cgi"

var dsmPaths = []string{"/etc/environment", "/etc/proxy.conf"}

func main() {
	if strings.HasSuffix(os.Args[0], ".cgi") {
		if err := cgi.Serve(http.HandlerFunc(gateway)); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 4 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: qidocker serve STATE_DIR PACKAGE_DIR")
		os.Exit(2)
	}
	if err := serve(os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
		},
		DisableCompression:    true,
		ResponseHeaderTimeout: 45 * time.Second,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func gateway(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-QiDocker-Request") != "1" || (r.Method != "GET" && r.Method != "POST") {
		http.Error(w, "请求被拒绝", http.StatusForbidden)
		return
	}
	// Only the operation query is forwarded. DSM session data goes to the
	// local service as CGI environment values, never as command-line arguments.
	req, err := http.NewRequestWithContext(r.Context(), r.Method, "http://localhost/?"+r.URL.RawQuery, r.Body)
	if err != nil {
		http.Error(w, "无效请求", 400)
		return
	}
	req.ContentLength = r.ContentLength
	for _, name := range []string{"HTTP_COOKIE", "REMOTE_ADDR", "SERVER_ADDR", "SERVER_PORT", "HTTP_HOST", "HTTPS", "HTTP_X_SYNO_TOKEN"} {
		req.Header.Set("X-QD-"+name, os.Getenv(name))
	}
	res, err := unixClient(home + "/target/.control.sock").Do(req)
	if err != nil {
		http.Error(w, "QiDocker 服务无法连接", 503)
		return
	}
	defer res.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Disposition"} {
		if value := res.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(res.StatusCode)
	buffer := make([]byte, 32*1024)
	for {
		n, err := res.Body.Read(buffer)
		if n > 0 {
			if _, e := w.Write(buffer[:n]); e != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func checkAdmin(r *http.Request) bool {
	if r.Header.Get("X-QD-HTTP_COOKIE") == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, authCGI)
	cmd.Env = []string{"PATH=/usr/syno/bin:/usr/bin:/bin", "REQUEST_METHOD=GET"}
	for _, name := range []string{"HTTP_COOKIE", "REMOTE_ADDR", "SERVER_ADDR", "SERVER_PORT", "HTTP_HOST", "HTTPS", "HTTP_X_SYNO_TOKEN"} {
		cmd.Env = append(cmd.Env, name+"="+r.Header.Get("X-QD-"+name))
	}
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	user := strings.TrimSpace(string(out))
	if user == "" || strings.ContainsAny(user, "\x00\r\n") {
		return false
	}
	groups, err := exec.CommandContext(ctx, "/usr/bin/id", "-Gn", "--", user).Output()
	if err != nil {
		return false
	}
	for _, group := range strings.Fields(string(groups)) {
		if group == "administrators" {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(value)
}

func createSocket(path string, mode os.FileMode) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("控制 Socket 路径被占用")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, mode); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func drain(reader io.Reader) { _, _ = io.Copy(io.Discard, reader) }

func signalProcess(process *os.Process, signal syscall.Signal) {
	if process != nil {
		_ = process.Signal(signal)
	}
}

func packagePath(target, name string) string { return filepath.Join(target, "bin", name) }
