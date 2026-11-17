//go:build conformance

package tusuploadserver_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"

	tus "github.com/Trysac/u921-tus-upload-server"
)

// proxyListener drops the connection after every 3rd PATCH request
type droppingProxy struct {
	backendURL string
	patchCount int32
}

func (p *droppingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPatch {
		count := atomic.AddInt32(&p.patchCount, 1)
		if count%3 == 0 {
			// Hijack and close connection prematurely
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				if conn != nil {
					_ = conn.Close()
					return
				}
			}
		}
	}

	// Proxy to backend
	req, err := http.NewRequestWithContext(r.Context(), r.Method, p.backendURL+r.URL.Path, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for k, vv := range r.Header {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func TestConformanceTusJsClient(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found, skipping conformance test")
	}

	scriptPath := "testdata/conformance/upload.mjs"
	if _, err := os.Stat(scriptPath); err != nil {
		t.Skip("testdata/conformance/upload.mjs not found, skipping conformance test")
	}

	tmpDir := t.TempDir()
	store, err := tus.NewDirStore(tmpDir)
	if err != nil {
		t.Fatalf("NewDirStore error: %v", err)
	}

	h, err := tus.NewHandler(tus.Config{
		BasePath: "/files/",
		Store:    store,
		MaxSize:  20 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHandler error: %v", err)
	}

	backendServer := httptest.NewServer(h)
	defer backendServer.Close()

	proxy := &droppingProxy{backendURL: backendServer.URL}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Create 1 MiB test file
	fileSize := 1024 * 1024
	testData := make([]byte, fileSize)
	for i := range testData {
		testData[i] = byte(i % 251)
	}
	sum := sha256.Sum256(testData)
	expectedSHA := hex.EncodeToString(sum[:])

	testFilePath := tmpDir + "/source.bin"
	if err := os.WriteFile(testFilePath, testData, 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	cmd := exec.Command(nodePath, scriptPath, proxyServer.URL+"/files/", testFilePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("upload.mjs failed: %v", err)
	}

	// Find uploaded bin file in store
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var uploadedBinPath string
	for _, entry := range entries {
		if !entry.IsDir() && len(entry.Name()) > 4 && entry.Name()[len(entry.Name())-4:] == ".bin" && entry.Name() != "source.bin" {
			uploadedBinPath = tmpDir + "/" + entry.Name()
			break
		}
	}
	if uploadedBinPath == "" {
		t.Fatalf("no uploaded bin file found in %s", tmpDir)
	}

	uploadedData, err := os.ReadFile(uploadedBinPath)
	if err != nil {
		t.Fatalf("ReadFile uploaded file error: %v", err)
	}
	uploadedSum := sha256.Sum256(uploadedData)
	actualSHA := hex.EncodeToString(uploadedSum[:])

	if actualSHA != expectedSHA {
		t.Fatalf("SHA-256 mismatch: got %s, want %s", actualSHA, expectedSHA)
	}
}
