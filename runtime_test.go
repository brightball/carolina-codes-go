package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPoolCapsConnsForOneCPU(t *testing.T) {
	prev := runtime.GOMAXPROCS(32)
	t.Cleanup(func() { runtime.GOMAXPROCS(prev) })

	pool, err := openPool(context.Background(), "postgres://postgres:postgres@192.0.2.1:1/none?sslmode=disable&pool_max_conns=32&pool_min_conns=3&pool_min_idle_conns=3&connect_timeout=30")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	cfg := pool.Config()
	if cfg.MinConns != 0 {
		t.Fatalf("MinConns = %d, want 0", cfg.MinConns)
	}
	if cfg.MinIdleConns != 0 {
		t.Fatalf("MinIdleConns = %d, want 0", cfg.MinIdleConns)
	}
	if cfg.MaxConns <= 0 || cfg.MaxConns > 4 {
		t.Fatalf("MaxConns = %d, want 1..4", cfg.MaxConns)
	}
	if cfg.ConnConfig.ConnectTimeout <= 0 || cfg.ConnConfig.ConnectTimeout > 5*time.Second {
		t.Fatalf("ConnectTimeout = %s, want >0 and <=5s", cfg.ConnConfig.ConnectTimeout)
	}
	time.Sleep(150 * time.Millisecond)
	st := pool.Stat()
	if st.NewConnsCount() != 0 || st.TotalConns() != 0 {
		t.Fatalf("openPool dialed: new=%d total=%d", st.NewConnsCount(), st.TotalConns())
	}
}

func TestServerTimeoutsAreNonZero(t *testing.T) {
	srv := newServer(listenAddr("0"), newHandler(nil))
	if srv.ReadHeaderTimeout <= 0 {
		t.Fatal("ReadHeaderTimeout is zero")
	}
	if srv.WriteTimeout <= 0 {
		t.Fatal("WriteTimeout is zero")
	}
	if srv.IdleTimeout <= 0 {
		t.Fatal("IdleTimeout is zero")
	}
	if srv.Addr != "[::]:0" {
		t.Fatalf("Addr = %s", srv.Addr)
	}
}

func TestBootDoesNotWaitOnPostgresOrRegistration(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	t.Setenv("CAROLINA_URL", "http://"+ln.Addr().String())
	t.Setenv("POLYGLOT_REGISTER_TOKEN", "dev")

	port := freePort(t)
	started := time.Now()
	srv, pool, err := boot("postgres://postgres:postgres@192.0.2.1:1/none?sslmode=disable", port)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("boot blocked for %s", elapsed)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		pool.Close()
	})
	if srv.Addr != "[::]:"+port {
		t.Fatalf("listen addr %s", srv.Addr)
	}
	if srv.ReadHeaderTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Fatalf("timeouts header=%s write=%s idle=%s", srv.ReadHeaderTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	time.Sleep(100 * time.Millisecond)
	st := pool.Stat()
	if st.NewConnsCount() != 0 || st.TotalConns() != 0 {
		t.Fatalf("boot dialed postgres: new=%d total=%d", st.NewConnsCount(), st.TotalConns())
	}

	lis, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()

	resetCounts()
	health := httpGet(t, "http://[::1]:"+port+"/health")
	if !strings.Contains(health, `"ok":true`) {
		t.Fatalf("health %s", health)
	}
	var healthDoc struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(health), &healthDoc); err != nil || !healthDoc.OK {
		t.Fatalf("health json %s", health)
	}
	assertIndexJSON(t, httpGet(t, "http://[::1]:"+port+"/"))
	if sqlCount.Load() != 0 {
		t.Fatalf("boot routes ran SQL: %d", sqlCount.Load())
	}
}

func TestMainUsesBootWithoutPing(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	mainBody := functionBody(t, body, "main")
	bootBody := functionBody(t, body, "boot")
	if !strings.Contains(mainBody, "boot(") || !strings.Contains(mainBody, "ListenAndServe(") {
		t.Fatal("main must boot the shared server and listen")
	}
	if strings.Contains(mainBody, "Ping(") || strings.Contains(mainBody, "pgxpool.New") {
		t.Fatal("main dials postgres itself")
	}
	if !strings.Contains(bootBody, "go register(") || !strings.Contains(bootBody, "openPool(") || !strings.Contains(bootBody, "newServer(") {
		t.Fatal("boot must open the shared pool, register in the background, and use newServer")
	}
	if strings.Contains(bootBody, "Ping(") {
		t.Fatal("boot pings postgres")
	}
}

func TestFlyRuntimeStaysScaleToZero(t *testing.T) {
	body := string(readRepo(t, "fly.toml"))
	if !strings.Contains(body, `auto_stop_machines = "stop"`) {
		t.Fatal("auto_stop_machines must stay stop")
	}
	if strings.Contains(body, `auto_stop_machines = "suspend"`) {
		t.Fatal("auto_stop_machines switched to suspend")
	}
	if !strings.Contains(body, "auto_start_machines = true") {
		t.Fatal("auto_start_machines must stay on")
	}
	if !strings.Contains(body, `memory = "256mb"`) || !strings.Contains(body, `cpu_kind = "shared"`) || !strings.Contains(body, "cpus = 1") {
		t.Fatal("vm must stay shared-cpu-1x / 256mb")
	}
	if !strings.Contains(body, "internal_port = 8080") || !strings.Contains(body, `path = "/health"`) || !strings.Contains(body, `method = "GET"`) {
		t.Fatal("HTTP check must be GET /health on port 8080")
	}
	if !strings.Contains(body, `GOMAXPROCS = "1"`) {
		t.Fatal("fly env must set GOMAXPROCS=1")
	}
	if !strings.Contains(body, `GOMEMLIMIT = "200MiB"`) {
		t.Fatal("fly env must set GOMEMLIMIT for the 256mb machine")
	}
	min := regexp.MustCompile(`(?m)^\s*min_machines_running\s*=\s*(\d+)\s*$`).FindStringSubmatch(body)
	if len(min) != 2 || min[1] != "0" {
		t.Fatalf("min_machines_running = %v, want 0", min)
	}
}

func TestDockerfileReleaseImage(t *testing.T) {
	body := string(readRepo(t, "Dockerfile"))
	if !strings.Contains(body, `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`) {
		t.Fatal("image build must be CGO_ENABLED=0 and stripped")
	}
	if !strings.Contains(body, "ca-certificates.crt") {
		t.Fatal("final image must include a CA bundle")
	}
	last := lastFrom(body)
	if !strings.Contains(last, "scratch") || strings.Contains(strings.ToLower(last), "golang") {
		t.Fatalf("final stage %q still carries the Go toolchain image", last)
	}
}

func TestReleaseBinaryColdStartsTwice(t *testing.T) {
	bin := buildReleaseBinary(t)
	assertStaticStripped(t, bin)
	for range 2 {
		launchReleaseBinary(t, bin)
	}
}

func buildReleaseBinary(t *testing.T) string {
	t.Helper()
	root := moduleRoot(t)
	bin := filepath.Join(t.TempDir(), "api")
	args := releaseBuildArgs(t, bin)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = envWithout([]string{"CGO_ENABLED", "GOFLAGS"}, map[string]string{"CGO_ENABLED": "0"})
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("release build: %v\n%s", err, out)
	}
	return bin
}

func launchReleaseBinary(t *testing.T, bin string) {
	t.Helper()
	port := freePort(t)
	cmd := exec.Command(bin)
	cmd.Env = envWithout(
		[]string{"CAROLINA_URL", "DATABASE_URL", "PORT", "POLYGLOT_REGISTER_TOKEN"},
		map[string]string{
			"PORT":         port,
			"DATABASE_URL": "postgres://postgres:postgres@192.0.2.1:1/none?sslmode=disable",
		},
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped || cmd.Process == nil {
			return
		}
		stopped = true
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	defer stop()

	base := "http://[::1]:" + port
	health, healthErr := pollGET(base + "/health")
	var root string
	var rootErr error
	if healthErr == nil {
		root, rootErr = pollGET(base + "/")
	}
	// Wait for the process and its log copy to finish before reading logs.
	stop()
	logText := logs.String()
	if healthErr != nil {
		t.Fatalf("%v\nlogs %s", healthErr, logText)
	}
	if !strings.Contains(health, `"ok":true`) {
		t.Fatalf("health %s\nlogs %s", health, logText)
	}
	if rootErr != nil {
		t.Fatalf("%v\nlogs %s", rootErr, logText)
	}
	assertIndexJSON(t, root)
	if !strings.Contains(logText, "[::]:"+port) {
		t.Fatalf("process did not log an IPv6 listen address\n%s", logText)
	}
}

func releaseBuildArgs(t *testing.T, output string) []string {
	t.Helper()
	body := string(readRepo(t, "Dockerfile"))
	var buildLine string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "RUN ") && strings.Contains(line, "go build") {
			buildLine = strings.TrimPrefix(line, "RUN ")
		}
	}
	if buildLine == "" || !strings.Contains(buildLine, "CGO_ENABLED=0") {
		t.Fatal("Dockerfile has no CGO_ENABLED=0 go build")
	}
	idx := strings.Index(buildLine, "go build")
	rest, err := splitArgs(strings.TrimSpace(buildLine[idx+len("go build"):]))
	if err != nil {
		t.Fatal(err)
	}
	out := []string{"build"}
	skipNext := false
	replaced := false
	for _, arg := range rest {
		if skipNext {
			out = append(out, output)
			skipNext = false
			replaced = true
			continue
		}
		if arg == "-o" {
			out = append(out, "-o")
			skipNext = true
			continue
		}
		out = append(out, arg)
	}
	if !replaced || skipNext {
		t.Fatal("Dockerfile go build has no -o target")
	}
	joined := strings.Join(out, " ")
	if !strings.Contains(joined, "-s") || !strings.Contains(joined, "-w") {
		t.Fatalf("release build does not strip: %s", joined)
	}
	return out
}

func assertStaticStripped(t *testing.T, path string) {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for _, prog := range f.Progs {
		if prog.Type == elf.PT_INTERP {
			t.Fatal("release binary is dynamically linked")
		}
	}
	for _, sec := range f.Sections {
		if sec.Name == ".symtab" || strings.HasPrefix(sec.Name, ".debug_") || strings.HasPrefix(sec.Name, ".zdebug_") {
			t.Fatalf("release binary is not stripped: has %s", sec.Name)
		}
	}
	if file, err := exec.LookPath("file"); err == nil {
		out, err := exec.Command(file, path).CombinedOutput()
		if err != nil {
			t.Fatalf("file: %v\n%s", err, out)
		}
		text := strings.ToLower(string(out))
		if !strings.Contains(text, "statically linked") || !strings.Contains(text, "stripped") {
			t.Fatalf("file: %s", out)
		}
	}
}

func assertIndexJSON(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, `"language":"Go"`) || !strings.Contains(body, `"framework":"net/http"`) {
		t.Fatalf("index body %s", body)
	}
	var doc struct {
		Language  string `json:"language"`
		Framework string `json:"framework"`
		Endpoints []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("index json: %v body %s", err, body)
	}
	if doc.Language != language || doc.Framework != framework {
		t.Fatalf("index language=%s framework=%s", doc.Language, doc.Framework)
	}
	have := map[string]bool{}
	for _, ep := range doc.Endpoints {
		if ep.Method != http.MethodGet {
			t.Fatalf("endpoint method %s", ep.Method)
		}
		have[ep.Path] = true
	}
	for _, path := range []string{
		"/",
		"/health",
		"/v1/years",
		"/v1/speakers",
		"/v1/speakers/:slug",
		"/v1/speakers/:year/:slug",
		"/v1/sponsors",
		"/v1/sponsors/:slug",
		"/v1/sponsors/:year/:slug",
	} {
		if !have[path] {
			t.Fatalf("index missing %s in %s", path, body)
		}
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status %d body %s", url, resp.StatusCode, body)
	}
	return string(body)
}

func pollGET(url string) (string, error) {
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			last = err
			time.Sleep(20 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s status %d body %s", url, resp.StatusCode, body)
		}
		return string(body), nil
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return "", fmt.Errorf("%s: %w", url, last)
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Fatalf("ipv6 listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func functionBody(t *testing.T, src, name string) string {
	t.Helper()
	needle := "func " + name + "("
	i := strings.Index(src, needle)
	if i < 0 {
		t.Fatalf("missing func %s", name)
	}
	rest := src[i:]
	if j := strings.Index(rest[len(needle):], "\nfunc "); j >= 0 {
		return rest[:len(needle)+j]
	}
	return rest
}

func readRepo(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func lastFrom(body string) string {
	var last string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "FROM ") {
			last = line
		}
	}
	return last
}

func splitArgs(s string) ([]string, error) {
	var args []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote && c == '"':
			inQuote = false
		case inQuote:
			b.WriteByte(c)
		case c == '"':
			inQuote = true
		case c == ' ' || c == '\t':
			if b.Len() > 0 {
				args = append(args, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if inQuote {
		return nil, os.ErrInvalid
	}
	if b.Len() > 0 {
		args = append(args, b.String())
	}
	return args, nil
}

func envWithout(drop []string, set map[string]string) []string {
	skip := map[string]bool{}
	for _, key := range drop {
		skip[key] = true
	}
	for key := range set {
		skip[key] = true
	}
	var out []string
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if skip[key] {
			continue
		}
		out = append(out, env)
	}
	for key, val := range set {
		out = append(out, key+"="+val)
	}
	return out
}
