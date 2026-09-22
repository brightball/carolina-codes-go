package main

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPreparedEnvPackUnpack(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	gopath := filepath.Join(root, "gopath")
	modcache := filepath.Join(root, "mod")
	mustWrite(t, filepath.Join(ws, "go.mod"), "module example\n")
	mustWrite(t, filepath.Join(ws, "main.go"), "package main\n")
	mustWrite(t, filepath.Join(gopath, "bin", "gosec"), "#!/bin/sh\necho gosec\n")
	mustWrite(t, filepath.Join(modcache, "example.com/mod@v1/mod.go"), "package mod\n")
	if err := os.Chmod(filepath.Join(gopath, "bin", "gosec"), 0o755); err != nil {
		t.Fatal(err)
	}

	tarPath := filepath.Join(root, "prepared-env.tar.gz")
	runCIEnv(t, "pack", ws, gopath, modcache, tarPath, nil)
	if _, err := os.Stat(tarPath); err != nil {
		t.Fatalf("pack did not write tarball: %v", err)
	}

	ws2 := filepath.Join(root, "ws2")
	gopath2 := filepath.Join(root, "gopath2")
	modcache2 := filepath.Join(root, "mod2")
	runCIEnv(t, "unpack", ws2, gopath2, modcache2, tarPath, nil)

	assertFile(t, filepath.Join(ws2, "go.mod"), "module example\n")
	assertFile(t, filepath.Join(ws2, "main.go"), "package main\n")
	assertFile(t, filepath.Join(gopath2, "bin", "gosec"), "#!/bin/sh\necho gosec\n")
	assertFile(t, filepath.Join(modcache2, "example.com/mod@v1/mod.go"), "package mod\n")
	info, err := os.Stat(filepath.Join(gopath2, "bin", "gosec"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("restored gosec is not executable: %v", info.Mode())
	}
}

func TestPreparedEnvArtifactRoundTrip(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	gopath := filepath.Join(root, "gopath")
	modcache := filepath.Join(root, "mod")
	mustWrite(t, filepath.Join(ws, "README.md"), "carolina-codes-go\n")
	mustWrite(t, filepath.Join(gopath, "bin", "gitleaks"), "gitleaks-bin\n")
	mustWrite(t, filepath.Join(modcache, "cache.entry"), "mod\n")
	if err := os.Chmod(filepath.Join(gopath, "bin", "gitleaks"), 0o755); err != nil {
		t.Fatal(err)
	}

	store := newMockArtifactAPI()
	srv := httptest.NewServer(store)
	t.Cleanup(srv.Close)

	tarPath := filepath.Join(root, "prepared-env.tar.gz")
	env := []string{
		"ACTIONS_RUNTIME_URL=" + srv.URL + "/api/actions_pipeline/",
		"ACTIONS_RUNTIME_TOKEN=test-token",
		"GITHUB_RUN_ID=791",
		"GITHUB_SERVER_URL=" + srv.URL,
		"CI_ENV_SKIP_INSTALL=1",
	}
	runCIEnv(t, "prepare", ws, gopath, modcache, tarPath, env)

	ws2 := filepath.Join(root, "restored")
	gopath2 := filepath.Join(root, "gopath-restored")
	modcache2 := filepath.Join(root, "mod-restored")
	tar2 := filepath.Join(root, "downloaded.tar.gz")
	runCIEnv(t, "restore", ws2, gopath2, modcache2, tar2, env)

	assertFile(t, filepath.Join(ws2, "README.md"), "carolina-codes-go\n")
	assertFile(t, filepath.Join(gopath2, "bin", "gitleaks"), "gitleaks-bin\n")
	assertFile(t, filepath.Join(modcache2, "cache.entry"), "mod\n")
}

func runCIEnv(t *testing.T, cmd, ws, gopath, modcache, tarPath string, extra []string) {
	t.Helper()
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("bash", "scripts/ci-env.sh", cmd)
	c.Dir = "."
	c.Env = append(os.Environ(),
		"GITHUB_WORKSPACE="+ws,
		"GOPATH="+gopath,
		"GOMODCACHE="+modcache,
		"CI_ENV_TAR="+tarPath,
		"CI_ENV_ARTIFACT_NAME=prepared-env",
	)
	c.Env = append(c.Env, extra...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("ci-env.sh %s: %v\n%s", cmd, err, out)
	}
	t.Logf("ci-env.sh %s\n%s", cmd, out)
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s: got %q want %q", path, got, want)
	}
}

type mockArtifactAPI struct {
	mu        sync.Mutex
	nextID    int64
	pending   map[string][]byte // itemPath -> bytes
	confirmed map[string]storedArtifact
}

type storedArtifact struct {
	id       int64
	name     string
	path     string
	contents []byte
}

func newMockArtifactAPI() *mockArtifactAPI {
	return &mockArtifactAPI{
		nextID:    1,
		pending:   map[string][]byte{},
		confirmed: map[string]storedArtifact{},
	}
}

func (m *mockArtifactAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
		http.Error(w, "Bad authorization header", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/artifacts"):
		m.create(w, r)
	case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/upload"):
		m.upload(w, r)
	case r.Method == http.MethodPatch && strings.HasSuffix(strings.Split(r.URL.Path, "?")[0], "/artifacts"):
		m.confirm(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/artifacts"):
		m.list(w, r)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/download_url"):
		m.downloadURL(w, r)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/download"):
		m.download(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *mockArtifactAPI) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string
		Name string
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	hash := fmt.Sprintf("%x", md5.Sum([]byte(req.Name)))
	url := fmt.Sprintf("http://%s/api/actions_pipeline/_apis/pipelines/workflows/791/artifacts/%s/upload", r.Host, hash)
	_ = json.NewEncoder(w).Encode(map[string]string{"fileContainerResourceUrl": url})
}

func (m *mockArtifactAPI) upload(w http.ResponseWriter, r *http.Request) {
	itemPath := r.URL.Query().Get("itemPath")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := md5.Sum(body)
	want := base64.StdEncoding.EncodeToString(sum[:])
	if got := r.Header.Get("x-actions-results-md5"); got != want {
		http.Error(w, "md5 not match", http.StatusInternalServerError)
		return
	}
	m.mu.Lock()
	m.pending[itemPath] = body
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "success"})
}

func (m *mockArtifactAPI) confirm(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("artifactName")
	m.mu.Lock()
	defer m.mu.Unlock()
	for itemPath, body := range m.pending {
		if !strings.HasPrefix(itemPath, name+"/") {
			continue
		}
		id := m.nextID
		m.nextID++
		m.confirmed[fmt.Sprintf("%d", id)] = storedArtifact{
			id:       id,
			name:     name,
			path:     strings.TrimPrefix(itemPath, name+"/"),
			contents: body,
		}
		delete(m.pending, itemPath)
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "success"})
}

func (m *mockArtifactAPI) list(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type item struct {
		Name                     string `json:"name"`
		FileContainerResourceURL string `json:"fileContainerResourceUrl"`
	}
	seen := map[string]bool{}
	var items []item
	for _, art := range m.confirmed {
		if seen[art.name] {
			continue
		}
		seen[art.name] = true
		hash := fmt.Sprintf("%x", md5.Sum([]byte(art.name)))
		items = append(items, item{
			Name:                     art.name,
			FileContainerResourceURL: fmt.Sprintf("http://%s/api/actions_pipeline/_apis/pipelines/workflows/791/artifacts/%s/download_url", r.Host, hash),
		})
	}
	if len(items) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"count": len(items), "value": items})
}

func (m *mockArtifactAPI) downloadURL(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("itemPath")
	m.mu.Lock()
	defer m.mu.Unlock()
	type file struct {
		Path            string `json:"path"`
		ItemType        string `json:"itemType"`
		ContentLocation string `json:"contentLocation"`
	}
	var files []file
	for _, art := range m.confirmed {
		if art.name != name {
			continue
		}
		files = append(files, file{
			Path:            art.name + "/" + art.path,
			ItemType:        "file",
			ContentLocation: fmt.Sprintf("http://%s/api/actions_pipeline/_apis/pipelines/workflows/791/artifacts/%d/download", r.Host, art.id),
		})
	}
	if len(files) == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"value": files})
}

func (m *mockArtifactAPI) download(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var id string
	for i, p := range parts {
		if p == "artifacts" && i+1 < len(parts) {
			id = parts[i+1]
			break
		}
	}
	m.mu.Lock()
	art, ok := m.confirmed[id]
	m.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(art.contents)
}
