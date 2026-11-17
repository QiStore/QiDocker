package main

import (
	"bytes"
	"encoding/binary"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyCredentialEncodingAndRedaction(t *testing.T) {
	raw := proxySettings{Mode: "custom", HTTPS: "http://[2001:db8::1]:3128", Username: "user@example", Password: "a:p@ss#"}
	resolved, err := resolveProxy(raw)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(resolved.HTTPS)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.User.Username() != raw.Username || password != raw.Password {
		t.Fatal("proxy credentials were not encoded correctly")
	}
	visible := resolved.visible()
	if visible.Password != "" || strings.Contains(visible.HTTPS, "@") {
		t.Fatal("proxy credentials visible")
	}
}

func TestDSMProxyAssignmentsAreDataAndReread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.conf")
	content := "export HTTP_PROXY='http://proxy.example:8080'\nNO_PROXY=localhost,::1\nunused=$(exit 1)\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := resolveProxy(proxySettings{Mode: "dsm"}, path)
	if err != nil || settings.HTTP != "http://proxy.example:8080" || settings.NoProxy != "localhost,::1" {
		t.Fatal("DSM proxy configuration could not be read")
	}
	if err := os.WriteFile(path, []byte("HTTP_PROXY=http://proxy.example:8081\n"), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err = resolveProxy(proxySettings{Mode: "dsm"}, path)
	if err != nil || settings.HTTP != "http://proxy.example:8081" {
		t.Fatal("DSM proxy change was not observed")
	}
}

func TestPrivateWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.json")
	if err := privateJSON(path, proxySettings{Mode: "direct"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("configuration must have mode 0600")
	}
}

func TestDockerLogDemultiplex(t *testing.T) {
	var raw, decoded bytes.Buffer
	for i, line := range []string{"stdout\n", "stderr\n"} {
		var header [8]byte
		header[0] = byte(i + 1)
		binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
		raw.Write(header[:])
		raw.WriteString(line)
	}
	demultiplex(&decoded, &raw)
	if decoded.String() != "stdout\nstderr\n" {
		t.Fatalf("unexpected log %q", decoded.String())
	}
}

func TestDSMNativeProxySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.conf")
	contents := "proxy_enabled=yes\nhttp_host=proxy.example\nhttp_port=3128\nhttps_host=2001:db8::1\nhttps_port=8080\nauth_enabled=yes\nproxy_user=test\nproxy_pwd=a=p@ss\n"
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := resolveProxy(proxySettings{Mode: "dsm"}, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{settings.HTTP, settings.HTTPS} {
		u, err := url.Parse(address)
		if err != nil || u.User == nil || u.User.Username() != "test" {
			t.Fatal("expected DSM proxy credentials")
		}
		if pwd, ok := u.User.Password(); !ok || pwd != "a=p@ss" {
			t.Fatal("DSM proxy password was not preserved")
		}
	}
	if settings.visible().HTTP != "http://proxy.example:3128" || settings.visible().HTTPS != "http://[2001:db8::1]:8080" {
		t.Fatal("expected DSM proxy endpoints without credentials")
	}
}

func TestDSMNativeProxySwitches(t *testing.T) {
	values := map[string]string{"proxy_enabled": "no", "http_proxy": "http://fallback:8888", "http_host": "proxy.example", "http_port": "3128", "auth_enabled": "no", "proxy_user": "old", "proxy_pwd": "old"}
	if got := dsmProxySettings(values); got.HTTP != "" || got.HTTPS != "" {
		t.Fatal("disabled DSM proxy must override legacy environment values")
	}
	values["proxy_enabled"] = "yes"
	got := dsmProxySettings(values)
	if got.HTTPS != "http://proxy.example:3128" || got.HTTPS != got.HTTP || got.Username != "" || got.Password != "" {
		t.Fatal("expected shared endpoint with authentication disabled")
	}
}

func TestDSMProtocolOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.conf")
	content := "proxy_enabled=yes\nhttp_host=proxy.example\nhttp_port=17896\nauth_enabled=yes\nproxy_user=test\nproxy_pwd=secret\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ protocol, want string }{
		{"", "http://proxy.example:17896"},
		{"http", "http://proxy.example:17896"},
		{"https", "https://proxy.example:17896"},
	} {
		settings, err := resolveProxy(proxySettings{Mode: "dsm", DSMProtocol: tc.protocol}, path)
		if err != nil {
			t.Fatal(err)
		}
		visible := settings.visible()
		if visible.HTTP != tc.want || visible.HTTPS != tc.want || visible.DSMProtocol != tc.protocol || settings.Password != "secret" || visible.Password != "" {
			t.Fatal("protocol override must preserve endpoint and DSM authentication")
		}
	}
	if _, err := resolveProxy(proxySettings{Mode: "dsm", DSMProtocol: "ftp"}, path); err == nil {
		t.Fatal("unsupported protocol must be rejected")
	}
}
