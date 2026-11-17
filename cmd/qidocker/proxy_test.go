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
