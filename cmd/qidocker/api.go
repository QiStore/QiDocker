package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var dockerID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

type operation struct{ method, path string }

var operations = map[string]operation{
	"containers":     {"GET", "/containers/json?all=1"},
	"container":      {"GET", "/containers/%s/json"},
	"create":         {"POST", "/containers/create"},
	"start":          {"POST", "/containers/%s/start"},
	"stop":           {"POST", "/containers/%s/stop"},
	"restart":        {"POST", "/containers/%s/restart"},
	"pause":          {"POST", "/containers/%s/pause"},
	"unpause":        {"POST", "/containers/%s/unpause"},
	"remove":         {"DELETE", "/containers/%s"},
	"stats":          {"GET", "/containers/%s/stats?stream=0"},
	"logs":           {"GET", "/containers/%s/logs?stdout=1&stderr=1&tail=200&timestamps=1"},
	"images":         {"GET", "/images/json"},
	"image":          {"GET", "/images/%s/json"},
	"remove-image":   {"DELETE", "/images/%s"},
	"tag":            {"POST", "/images/%s/tag"},
	"search":         {"GET", "/images/search"},
	"pull":           {"POST", "/images/create"},
	"import":         {"POST", "/images/load"},
	"export":         {"GET", "/images/%s/get"},
	"networks":       {"GET", "/networks"},
	"network":        {"GET", "/networks/%s"},
	"create-network": {"POST", "/networks/create"},
	"remove-network": {"DELETE", "/networks/%s"},
}

type registryAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Server   string `json:"serveraddress"`
}

func (m *manager) registryFile() string { return filepath.Join(m.state, "registries.json") }

func (m *manager) registryList() map[string]registryAuth {
	store := make(map[string]registryAuth)
	if data, err := os.ReadFile(m.registryFile()); err == nil {
		_ = json.Unmarshal(data, &store)
	}
	if store == nil {
		store = make(map[string]registryAuth)
	}
	return store
}

func (m *manager) registry(w http.ResponseWriter, r *http.Request, action string) {
	switch action {
	case "registries":
		m.mu.Lock()
		defer m.mu.Unlock()
		rows := []map[string]string{{"server": "docker.io", "username": m.registryList()["docker.io"].Username}}
		for _, entry := range m.registryList() {
			if entry.Server != "docker.io" {
				rows = append(rows, map[string]string{"server": entry.Server, "username": entry.Username})
			}
		}
		writeJSON(w, rows)
	case "registry-login", "registry-logout":
		var creds registryAuth
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&creds) != nil {
			http.Error(w, "无效的认证配置", 400)
			return
		}
		host := strings.ToLower(strings.TrimSpace(creds.Server))
		if host == "" {
			host = "docker.io"
		}
		value, err := url.Parse("https://" + host)
		if err != nil || value.Hostname() == "" || value.Host != host || value.User != nil || strings.ContainsAny(host, "/\\?#@") {
			http.Error(w, "请输入域名和可选端口", 400)
			return
		}
		creds.Server = host
		if action == "registry-login" {
			login := creds
			if host == "docker.io" {
				login.Server = "https://index.docker.io/v1/"
			}
			res, err := m.request(r.Context(), "POST", "/auth", engineJSON(login), http.Header{"Content-Type": {"application/json"}})
			if err != nil {
				http.Error(w, "注册表认证请求失败", 502)
				return
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				http.Error(w, fmt.Sprintf("注册表认证失败（HTTP %d）", res.StatusCode), 422)
				return
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		store := m.registryList()
		if action == "registry-logout" {
			delete(store, host)
		} else {
			store[host] = creds
		}
		if err := privateJSON(m.registryFile(), store); err != nil {
			http.Error(w, "保存注册表配置失败", 500)
			return
		}
		writeJSON(w, map[string]bool{"saved": true})
	}
}

func registryHost(reference string) string {
	first, _, ok := strings.Cut(reference, "/")
	if ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return strings.ToLower(first)
	}
	return "docker.io"
}

func (m *manager) handle(w http.ResponseWriter, r *http.Request) {
	if !checkAdmin(r) {
		http.Error(w, "需要 DSM 管理员会话", 403)
		return
	}
	action := r.URL.Query().Get("action")
	switch action {
	case "status", "host", "registries", "engine-log":
		if r.Method != "GET" {
			http.Error(w, "请求方法错误", 405)
			return
		}
	case "proxy-save", "proxy-test", "proxy-apply", "registry-login", "registry-logout", "exec":
		if r.Method != "POST" {
			http.Error(w, "请求方法错误", 405)
			return
		}
	}
	switch action {
	case "status":
		m.status(w, r)
		return
	case "host":
		systemStats(w)
		return
	case "proxy-save", "proxy-test", "proxy-apply":
		m.proxyAction(w, r, action)
		return
	case "registries", "registry-login", "registry-logout":
		m.registry(w, r, action)
		return
	case "engine-log":
		m.engineLog(w)
		return
	case "exec":
		m.execute(w, r)
		return
	}
	op, ok := operations[action]
	if !ok {
		http.NotFound(w, r)
		return
	}
	method := op.method
	expected := method
	if method == "DELETE" {
		expected = "POST"
	}
	if r.Method != expected {
		http.Error(w, "请求方法错误", 405)
		return
	}
	q := r.URL.Query()
	if strings.Contains(op.path, "%s") {
		id := strings.TrimPrefix(q.Get("id"), "sha256:")
		if !dockerID.MatchString(id) {
			http.Error(w, "无效对象 ID", 400)
			return
		}
		op.path = fmt.Sprintf(op.path, id)
	}
	headers := make(http.Header)
	var body io.Reader
	if action == "import" {
		headers.Set("Content-Type", "application/x-tar")
		body = http.MaxBytesReader(w, r.Body, 10<<30)
	} else if method == "POST" {
		headers.Set("Content-Type", "application/json")
		body = http.MaxBytesReader(w, r.Body, 1<<20)
	}

	switch action {
	case "create":
		if name := q.Get("name"); name != "" {
			op.path += "?" + url.Values{"name": {name}}.Encode()
		}
	case "pull", "search", "tag":
		values := make(url.Values)
		switch action {
		case "pull":
			values.Set("fromImage", q.Get("image"))
		case "search":
			values.Set("term", q.Get("term"))
		case "tag":
			values.Set("repo", q.Get("repo"))
			values.Set("tag", q.Get("tag"))
		}
		op.path += "?" + values.Encode()
		if action != "tag" {
			ref := q.Get("image")
			if action == "search" {
				ref = q.Get("term")
			}
			m.mu.Lock()
			creds := m.registryList()[registryHost(ref)]
			m.mu.Unlock()
			if creds.Server == "docker.io" {
				creds.Server = "https://index.docker.io/v1/"
			}
			token, _ := json.Marshal(creds)
			headers.Set("X-Registry-Auth", base64.URLEncoding.EncodeToString(token))
		}
	}
	serialized := action == "start" || action == "restart" || action == "unpause"
	if serialized {
		m.mu.Lock()
		defer m.mu.Unlock()
	}
	res, err := m.request(r.Context(), method, op.path, body, headers)
	if err != nil {
		http.Error(w, "Docker Engine 无法连接", 502)
		return
	}
	defer res.Body.Close()
	w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
	if action == "export" {
		w.Header().Set("Content-Disposition", `attachment; filename="qidocker-image.tar"`)
	}
	if action == "logs" && res.StatusCode == 200 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		demultiplex(w, res.Body)
		return
	}
	w.WriteHeader(res.StatusCode)
	if action == "pull" || action == "import" {
		buf := make([]byte, 8<<10)
		for {
			n, e := res.Body.Read(buf)
			if n > 0 {
				if _, err = w.Write(buf[:n]); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
			if e != nil {
				return
			}
		}
	}
	_, _ = io.Copy(w, res.Body)
}

func demultiplex(w io.Writer, src io.Reader) {
	var header [8]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return
	}
	if (header[0] != 1 && header[0] != 2) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
		_, _ = w.Write(header[:])
		_, _ = io.Copy(w, src)
		return
	}
	for {
		if _, err := io.CopyN(w, src, int64(binary.BigEndian.Uint32(header[4:]))); err != nil {
			return
		}
		if _, err := io.ReadFull(src, header[:]); err != nil {
			return
		}
	}
}

func (m *manager) engineLog(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	file, err := os.Open(filepath.Join(m.state, "engine.log"))
	if err != nil {
		http.Error(w, "日志不可用", 404)
		return
	}
	defer file.Close()
	if end, err := file.Seek(0, io.SeekEnd); err == nil {
		_, _ = file.Seek(max(0, end-128<<10), io.SeekStart)
		_, _ = io.CopyN(w, file, 128<<10)
	}
}

func (m *manager) execute(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	var payload struct {
		Command string `json:"command"`
	}
	if !dockerID.MatchString(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&payload) != nil || payload.Command == "" {
		http.Error(w, "无效命令", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	headers := http.Header{"Content-Type": {"application/json"}}
	res, err := m.request(ctx, "POST", "/containers/"+id+"/exec", engineJSON(map[string]any{
		"AttachStdout": true, "AttachStderr": true, "Cmd": []string{"/bin/sh", "-lc", payload.Command},
	}), headers)
	if err != nil {
		http.Error(w, "无法创建执行会话", 502)
		return
	}
	defer res.Body.Close()
	var session struct{ ID string }
	if res.StatusCode != 201 || json.NewDecoder(res.Body).Decode(&session) != nil || !dockerID.MatchString(session.ID) {
		http.Error(w, "容器执行会话不可用", 422)
		return
	}
	run, err := m.request(ctx, "POST", "/exec/"+session.ID+"/start", engineJSON(map[string]bool{"Detach": false, "Tty": false}), headers)
	if err != nil {
		http.Error(w, "无法启动执行会话", 502)
		return
	}
	defer run.Body.Close()
	if run.StatusCode != 200 {
		http.Error(w, "执行会话启动失败", 422)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	demultiplex(w, io.LimitReader(run.Body, 1<<20))
}
