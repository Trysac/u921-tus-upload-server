package tusuploadserver_test

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tus "github.com/Trysac/u921-tus-upload-server"
)

func setupTestServer(t *testing.T, opts ...func(*tus.Config)) (http.Handler, tus.Store, *time.Time) {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := tus.NewDirStore(tmpDir)
	if err != nil {
		t.Fatalf("NewDirStore error: %v", err)
	}

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := tus.Config{
		BasePath: "/files/",
		Store:    store,
		MaxSize:  1073741824,
		Clock: func() time.Time {
			return now
		},
		NewID: func() string {
			return "abc"
		},
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	h, err := tus.NewHandler(cfg)
	if err != nil {
		t.Fatalf("NewHandler error: %v", err)
	}
	return h, store, &now
}

func TestAcceptanceOPTIONS(t *testing.T) {
	h, _, _ := setupTestServer(t)
	req := httptest.NewRequest(http.MethodOptions, "/files/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Tus-Resumable"); got != "1.0.0" {
		t.Errorf("Tus-Resumable = %q, want 1.0.0", got)
	}
	if got := res.Header.Get("Tus-Version"); got != "1.0.0" {
		t.Errorf("Tus-Version = %q, want 1.0.0", got)
	}
	if got := res.Header.Get("Tus-Extension"); got != "creation,creation-with-upload,termination,expiration,checksum" {
		t.Errorf("Tus-Extension = %q", got)
	}
	if got := res.Header.Get("Tus-Max-Size"); got != "1073741824" {
		t.Errorf("Tus-Max-Size = %q, want 1073741824", got)
	}
	if got := res.Header.Get("Tus-Checksum-Algorithm"); got != "sha1" {
		t.Errorf("Tus-Checksum-Algorithm = %q, want sha1", got)
	}
}

func TestAcceptanceCreationAndHEAD(t *testing.T) {
	h, _, _ := setupTestServer(t)

	// POST /files/ with Upload-Length "11" and Upload-Metadata "filename aGVsbG8udHh0"
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "11")
	req.Header.Set("Upload-Metadata", "filename aGVsbG8udHh0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/files/abc" {
		t.Errorf("Location = %q, want /files/abc", got)
	}

	// HEAD /files/abc
	headReq := httptest.NewRequest(http.MethodHead, "/files/abc", nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headW := httptest.NewRecorder()
	h.ServeHTTP(headW, headReq)

	headRes := headW.Result()
	if headRes.StatusCode != http.StatusOK {
		t.Fatalf("HEAD expected 200, got %d", headRes.StatusCode)
	}
	if got := headRes.Header.Get("Upload-Offset"); got != "0" {
		t.Errorf("Upload-Offset = %q, want 0", got)
	}
	if got := headRes.Header.Get("Upload-Length"); got != "11" {
		t.Errorf("Upload-Length = %q, want 11", got)
	}
	if got := headRes.Header.Get("Upload-Metadata"); got != "filename aGVsbG8udHh0" {
		t.Errorf("Upload-Metadata = %q, want filename aGVsbG8udHh0", got)
	}
	if got := headRes.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

type failAfterReader struct {
	data []byte
	read int
	max  int
}

func (f *failAfterReader) Read(p []byte) (int, error) {
	if f.read >= f.max {
		return 0, errors.New("simulated network cut")
	}
	toRead := len(f.data) - f.read
	if toRead > f.max-f.read {
		toRead = f.max - f.read
	}
	if toRead > len(p) {
		toRead = len(p)
	}
	copy(p, f.data[f.read:f.read+toRead])
	f.read += toRead
	if f.read >= f.max {
		return toRead, errors.New("simulated network cut")
	}
	return toRead, nil
}

func TestAcceptancePartialPatchAndResume(t *testing.T) {
	h, store, _ := setupTestServer(t)

	// Create upload
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "11")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d", w.Code)
	}

	// PATCH at offset 0 with body "hello world" failing after 5 bytes
	failingBody := &failAfterReader{
		data: []byte("hello world"),
		max:  5,
	}
	patchReq1 := httptest.NewRequest(http.MethodPatch, "/files/abc", failingBody)
	patchReq1.Header.Set("Tus-Resumable", "1.0.0")
	patchReq1.Header.Set("Upload-Offset", "0")
	patchReq1.Header.Set("Content-Type", "application/offset+octet-stream")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, patchReq1)

	// Verify HEAD is at Upload-Offset "5"
	headReq := httptest.NewRequest(http.MethodHead, "/files/abc", nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headW := httptest.NewRecorder()
	h.ServeHTTP(headW, headReq)
	if got := headW.Header().Get("Upload-Offset"); got != "5" {
		t.Fatalf("after failure, Upload-Offset = %q, want 5", got)
	}

	// Resume: PATCH at offset "5" with " world"
	patchReq2 := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader(" world"))
	patchReq2.Header.Set("Tus-Resumable", "1.0.0")
	patchReq2.Header.Set("Upload-Offset", "5")
	patchReq2.Header.Set("Content-Type", "application/offset+octet-stream")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, patchReq2)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("patch 2 failed with %d: %s", w2.Code, w2.Body.String())
	}
	if got := w2.Header().Get("Upload-Offset"); got != "11" {
		t.Errorf("Upload-Offset = %q, want 11", got)
	}

	// Verify stored content equals "hello world"
	rc, err := store.Read(context.Background(), "abc")
	if err != nil {
		t.Fatalf("store.Read error: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("stored data = %q, want hello world", string(data))
	}
}

func TestAcceptanceConflictAndValidation(t *testing.T) {
	h, _, _ := setupTestServer(t)

	// Create upload length 11
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "11")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// PATCH with Upload-Offset "0" for 5 bytes
	p1 := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("hello"))
	p1.Header.Set("Tus-Resumable", "1.0.0")
	p1.Header.Set("Upload-Offset", "0")
	p1.Header.Set("Content-Type", "application/offset+octet-stream")
	w1 := httptest.NewRecorder()
	h.ServeHTTP(w1, p1)
	if w1.Code != http.StatusNoContent {
		t.Fatalf("p1 expected 204, got %d", w1.Code)
	}

	// PATCH with Upload-Offset "3" when offset is 5 returns 409
	pConflict := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("test"))
	pConflict.Header.Set("Tus-Resumable", "1.0.0")
	pConflict.Header.Set("Upload-Offset", "3")
	pConflict.Header.Set("Content-Type", "application/offset+octet-stream")
	wConflict := httptest.NewRecorder()
	h.ServeHTTP(wConflict, pConflict)
	if wConflict.Code != http.StatusConflict {
		t.Errorf("expected 409 Conflict, got %d", wConflict.Code)
	}

	// Content-Type "text/plain" returns 415
	pBadType := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("test"))
	pBadType.Header.Set("Tus-Resumable", "1.0.0")
	pBadType.Header.Set("Upload-Offset", "5")
	pBadType.Header.Set("Content-Type", "text/plain")
	wBadType := httptest.NewRecorder()
	h.ServeHTTP(wBadType, pBadType)
	if wBadType.Code != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415, got %d", wBadType.Code)
	}

	// Request without Tus-Resumable returns 412 with Tus-Version "1.0.0"
	pNoTus := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("test"))
	pNoTus.Header.Set("Upload-Offset", "5")
	pNoTus.Header.Set("Content-Type", "application/offset+octet-stream")
	wNoTus := httptest.NewRecorder()
	h.ServeHTTP(wNoTus, pNoTus)
	if wNoTus.Code != http.StatusPreconditionFailed {
		t.Errorf("expected 412, got %d", wNoTus.Code)
	}
	if got := wNoTus.Header().Get("Tus-Version"); got != "1.0.0" {
		t.Errorf("Tus-Version = %q, want 1.0.0", got)
	}

	// Body that would pass Upload-Length returns 413 and writes nothing
	// Current offset is 5, remaining length is 6. Send 7 bytes.
	pTooLarge := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("1234567"))
	pTooLarge.Header.Set("Tus-Resumable", "1.0.0")
	pTooLarge.Header.Set("Upload-Offset", "5")
	pTooLarge.Header.Set("Content-Type", "application/offset+octet-stream")
	wTooLarge := httptest.NewRecorder()
	h.ServeHTTP(wTooLarge, pTooLarge)
	if wTooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", wTooLarge.Code)
	}

	// Verify offset is still 5
	headReq := httptest.NewRequest(http.MethodHead, "/files/abc", nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headW := httptest.NewRecorder()
	h.ServeHTTP(headW, headReq)
	if got := headW.Header().Get("Upload-Offset"); got != "5" {
		t.Errorf("after 413, Upload-Offset = %q, want 5", got)
	}
}

func TestAcceptanceChecksum(t *testing.T) {
	h, _, _ := setupTestServer(t)

	// Create upload length 11
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "11")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	// Algorithm "md5" returns 400
	pMd5 := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("hello world"))
	pMd5.Header.Set("Tus-Resumable", "1.0.0")
	pMd5.Header.Set("Upload-Offset", "0")
	pMd5.Header.Set("Content-Type", "application/offset+octet-stream")
	pMd5.Header.Set("Upload-Checksum", "md5 1234")
	wMd5 := httptest.NewRecorder()
	h.ServeHTTP(wMd5, pMd5)
	if wMd5.Code != http.StatusBadRequest {
		t.Errorf("md5 expected 400, got %d", wMd5.Code)
	}

	// Wrong digest returns 460 and the offset stays unchanged
	pWrong := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("hello world"))
	pWrong.Header.Set("Tus-Resumable", "1.0.0")
	pWrong.Header.Set("Upload-Offset", "0")
	pWrong.Header.Set("Content-Type", "application/offset+octet-stream")
	pWrong.Header.Set("Upload-Checksum", "sha1 AAAA")
	wWrong := httptest.NewRecorder()
	h.ServeHTTP(wWrong, pWrong)
	if wWrong.Code != 460 {
		t.Errorf("wrong digest expected 460, got %d", wWrong.Code)
	}

	// Verify offset is still 0
	headReq := httptest.NewRequest(http.MethodHead, "/files/abc", nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headW := httptest.NewRecorder()
	h.ServeHTTP(headW, headReq)
	if got := headW.Header().Get("Upload-Offset"); got != "0" {
		t.Errorf("after 460, Upload-Offset = %q, want 0", got)
	}

	// sha1 for "hello world"
	hash := sha1.Sum([]byte("hello world"))
	b64Digest := base64.StdEncoding.EncodeToString(hash[:]) // Kq5sNclPz7QV2+lfQIuc6R7oRu0=
	if b64Digest != "Kq5sNclPz7QV2+lfQIuc6R7oRu0=" {
		t.Fatalf("unexpected sha1 digest: %s", b64Digest)
	}

	// Upload-Checksum "sha1 Kq5sNclPz7QV2+lfQIuc6R7oRu0=" with body "hello world" is accepted
	pValid := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("hello world"))
	pValid.Header.Set("Tus-Resumable", "1.0.0")
	pValid.Header.Set("Upload-Offset", "0")
	pValid.Header.Set("Content-Type", "application/offset+octet-stream")
	pValid.Header.Set("Upload-Checksum", "sha1 Kq5sNclPz7QV2+lfQIuc6R7oRu0=")
	wValid := httptest.NewRecorder()
	h.ServeHTTP(wValid, pValid)
	if wValid.Code != http.StatusNoContent {
		t.Errorf("valid checksum expected 204, got %d", wValid.Code)
	}
	if got := wValid.Header().Get("Upload-Offset"); got != "11" {
		t.Errorf("Upload-Offset = %q, want 11", got)
	}
}

func TestAcceptanceSizeExpirationDeletionAndIDEscaping(t *testing.T) {
	h, _, clock := setupTestServer(t, func(c *tus.Config) {
		c.Expiration = 24 * time.Hour
	})

	// Upload-Length "1073741825" returns 413
	reqTooLarge := httptest.NewRequest(http.MethodPost, "/files/", nil)
	reqTooLarge.Header.Set("Tus-Resumable", "1.0.0")
	reqTooLarge.Header.Set("Upload-Length", "1073741825")
	wTooLarge := httptest.NewRecorder()
	h.ServeHTTP(wTooLarge, reqTooLarge)
	if wTooLarge.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", wTooLarge.Code)
	}

	// Create valid upload
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "10")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d", w.Code)
	}

	// Advance clock 24h + 1s
	*clock = (*clock).Add(24*time.Hour + time.Second)

	// HEAD after expiration returns 410
	headReq := httptest.NewRequest(http.MethodHead, "/files/abc", nil)
	headReq.Header.Set("Tus-Resumable", "1.0.0")
	headW := httptest.NewRecorder()
	h.ServeHTTP(headW, headReq)
	if headW.Code != http.StatusGone {
		t.Errorf("expected 410 Gone, got %d", headW.Code)
	}

	// Create another upload for DELETE testing
	*clock = (*clock).Add(-24 * time.Hour) // reset clock
	req2 := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req2.Header.Set("Tus-Resumable", "1.0.0")
	req2.Header.Set("Upload-Length", "10")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("create 2 failed: %d", w2.Code)
	}

	// DELETE returns 204
	delReq := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
	delReq.Header.Set("Tus-Resumable", "1.0.0")
	delW := httptest.NewRecorder()
	h.ServeHTTP(delW, delReq)
	if delW.Code != http.StatusNoContent {
		t.Errorf("DELETE expected 204, got %d", delW.Code)
	}

	// Later HEAD returns 404
	headAfterDel := httptest.NewRequest(http.MethodHead, "/files/abc", nil)
	headAfterDel.Header.Set("Tus-Resumable", "1.0.0")
	headAfterDelW := httptest.NewRecorder()
	h.ServeHTTP(headAfterDelW, headAfterDel)
	if headAfterDelW.Code != http.StatusNotFound {
		t.Errorf("after delete HEAD expected 404, got %d", headAfterDelW.Code)
	}

	// The id "..%2Fx" returns 404
	escapeReq := httptest.NewRequest(http.MethodHead, "/files/..%2Fx", nil)
	escapeReq.Header.Set("Tus-Resumable", "1.0.0")
	escapeW := httptest.NewRecorder()
	h.ServeHTTP(escapeW, escapeReq)
	if escapeW.Code != http.StatusNotFound {
		t.Errorf("escape ID expected 404, got %d", escapeW.Code)
	}
}

func TestAcceptanceLockingConcurrencyAndEvents(t *testing.T) {
	var events []tus.Event
	var evMu sync.Mutex

	h, _, _ := setupTestServer(t, func(c *tus.Config) {
		c.OnEvent = func(e tus.Event) {
			evMu.Lock()
			events = append(events, e)
			evMu.Unlock()
		}
	})

	// Create upload with metadata
	req := httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "100")
	req.Header.Set("Upload-Metadata", "secretpassword dGVzdA==")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d", w.Code)
	}

	// Test locking: slow patch holding reader
	readyToRead := make(chan struct{})
	finishRead := make(chan struct{})
	slowReader := &blockingReader{
		data:  []byte("hello"),
		ready: readyToRead,
		done:  finishRead,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var slowW *httptest.ResponseRecorder
	go func() {
		defer wg.Done()
		slowReq := httptest.NewRequest(http.MethodPatch, "/files/abc", slowReader)
		slowReq.Header.Set("Tus-Resumable", "1.0.0")
		slowReq.Header.Set("Upload-Offset", "0")
		slowReq.Header.Set("Content-Type", "application/offset+octet-stream")
		slowW = httptest.NewRecorder()
		h.ServeHTTP(slowW, slowReq)
	}()

	<-readyToRead

	// Concurrent second PATCH on the same upload gets 423 Locked
	fastReq := httptest.NewRequest(http.MethodPatch, "/files/abc", strings.NewReader("world"))
	fastReq.Header.Set("Tus-Resumable", "1.0.0")
	fastReq.Header.Set("Upload-Offset", "0")
	fastReq.Header.Set("Content-Type", "application/offset+octet-stream")
	fastW := httptest.NewRecorder()
	h.ServeHTTP(fastW, fastReq)
	if fastW.Code != http.StatusLocked {
		t.Errorf("expected 423 Locked for concurrent PATCH, got %d", fastW.Code)
	}

	close(finishRead)
	wg.Wait()

	if slowW.Code != http.StatusNoContent {
		t.Errorf("slow PATCH expected 204, got %d", slowW.Code)
	}

	// Verify events
	evMu.Lock()
	defer evMu.Unlock()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	for _, ev := range events {
		if ev.ID != "abc" {
			t.Errorf("event ID = %q, want abc", ev.ID)
		}
		// Ensure no metadata values in event fields
		if strings.Contains(ev.Kind, "dGVzdA==") || strings.Contains(ev.ID, "dGVzdA==") {
			t.Errorf("event leaked metadata: %+v", ev)
		}
	}
}

type blockingReader struct {
	data  []byte
	ready chan struct{}
	done  chan struct{}
	once  sync.Once
}

func (b *blockingReader) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.ready)
		<-b.done
	})
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func TestConcurrentUploadsStress(t *testing.T) {
	// "go test -race passes with 10 concurrent uploads"
	tmpDir := t.TempDir()
	store, err := tus.NewDirStore(tmpDir)
	if err != nil {
		t.Fatalf("NewDirStore error: %v", err)
	}

	h, err := tus.NewHandler(tus.Config{
		BasePath: "/files/",
		Store:    store,
		MaxSize:  1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewHandler error: %v", err)
	}

	server := httptest.NewServer(h)
	defer server.Close()

	var wg sync.WaitGroup
	client := server.Client()

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bodyData := fmt.Sprintf("payload-number-%d-%s", idx, strings.Repeat("X", 1000))
			length := len(bodyData)

			// 1. Create
			createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/files/", nil)
			createReq.Header.Set("Tus-Resumable", "1.0.0")
			createReq.Header.Set("Upload-Length", fmt.Sprintf("%d", length))
			res, err := client.Do(createReq)
			if err != nil || res.StatusCode != http.StatusCreated {
				t.Errorf("worker %d create failed: %v", idx, err)
				return
			}
			loc := res.Header.Get("Location")
			res.Body.Close()

			uploadURL := server.URL + loc

			// 2. Patch part 1
			mid := length / 2
			p1Req, _ := http.NewRequest(http.MethodPatch, uploadURL, strings.NewReader(bodyData[:mid]))
			p1Req.Header.Set("Tus-Resumable", "1.0.0")
			p1Req.Header.Set("Upload-Offset", "0")
			p1Req.Header.Set("Content-Type", "application/offset+octet-stream")
			res1, err := client.Do(p1Req)
			if err != nil || res1.StatusCode != http.StatusNoContent {
				t.Errorf("worker %d patch 1 failed: %v", idx, err)
				return
			}
			res1.Body.Close()

			// 3. Patch part 2
			p2Req, _ := http.NewRequest(http.MethodPatch, uploadURL, strings.NewReader(bodyData[mid:]))
			p2Req.Header.Set("Tus-Resumable", "1.0.0")
			p2Req.Header.Set("Upload-Offset", fmt.Sprintf("%d", mid))
			p2Req.Header.Set("Content-Type", "application/offset+octet-stream")
			res2, err := client.Do(p2Req)
			if err != nil || res2.StatusCode != http.StatusNoContent {
				t.Errorf("worker %d patch 2 failed: %v", idx, err)
				return
			}
			res2.Body.Close()

			// 4. Verify HEAD offset
			headReq, _ := http.NewRequest(http.MethodHead, uploadURL, nil)
			headReq.Header.Set("Tus-Resumable", "1.0.0")
			resHead, err := client.Do(headReq)
			if err != nil || resHead.StatusCode != http.StatusOK {
				t.Errorf("worker %d head failed: %v", idx, err)
				return
			}
			off := resHead.Header.Get("Upload-Offset")
			resHead.Body.Close()
			if off != fmt.Sprintf("%d", length) {
				t.Errorf("worker %d offset %s != %d", idx, off, length)
			}
		}(i)
	}

	wg.Wait()
}

func TestCreationWithUpload(t *testing.T) {
	h, store, _ := setupTestServer(t)

	body := "initial payload"
	req := httptest.NewRequest(http.MethodPost, "/files/", strings.NewReader(body))
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", fmt.Sprintf("%d", len(body)))
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("creation-with-upload failed: %d (%s)", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Upload-Offset"); got != fmt.Sprintf("%d", len(body)) {
		t.Errorf("Upload-Offset = %q, want %d", got, len(body))
	}

	rc, err := store.Read(context.Background(), "abc")
	if err != nil {
		t.Fatalf("store.Read error: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != body {
		t.Errorf("data = %q, want %q", string(data), body)
	}
}

func TestLocalResumableClientSimulation(t *testing.T) {
	// Simulates the tus-js-client algorithm (HEAD then PATCH from Upload-Offset)
	h, store, _ := setupTestServer(t)
	server := httptest.NewServer(h)
	defer server.Close()

	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	totalSize := int64(len(payload))

	// Create
	createReq, _ := http.NewRequest(http.MethodPost, server.URL+"/files/", nil)
	createReq.Header.Set("Tus-Resumable", "1.0.0")
	createReq.Header.Set("Upload-Length", fmt.Sprintf("%d", totalSize))
	resp, err := server.Client().Do(createReq)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("create error: %v", err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	uploadURL := server.URL + loc

	chunkSize := int64(16384)
	var currentOffset int64

	for currentOffset < totalSize {
		// Query HEAD
		headReq, _ := http.NewRequest(http.MethodHead, uploadURL, nil)
		headReq.Header.Set("Tus-Resumable", "1.0.0")
		hResp, err := server.Client().Do(headReq)
		if err != nil || hResp.StatusCode != http.StatusOK {
			t.Fatalf("HEAD failed: %v", err)
		}
		headOffsetStr := hResp.Header.Get("Upload-Offset")
		hResp.Body.Close()
		fmt.Sscanf(headOffsetStr, "%d", &currentOffset)

		end := currentOffset + chunkSize
		if end > totalSize {
			end = totalSize
		}
		chunk := payload[currentOffset:end]

		patchReq, _ := http.NewRequest(http.MethodPatch, uploadURL, bytes.NewReader(chunk))
		patchReq.Header.Set("Tus-Resumable", "1.0.0")
		patchReq.Header.Set("Upload-Offset", fmt.Sprintf("%d", currentOffset))
		patchReq.Header.Set("Content-Type", "application/offset+octet-stream")
		pResp, err := server.Client().Do(patchReq)
		if err != nil || pResp.StatusCode != http.StatusNoContent {
			t.Fatalf("PATCH failed: %v", err)
		}
		pResp.Body.Close()
		currentOffset = end
	}

	rc, err := store.Read(context.Background(), "abc")
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	defer rc.Close()
	readData, _ := io.ReadAll(rc)
	if !bytes.Equal(readData, payload) {
		t.Fatalf("uploaded data does not match payload")
	}
}

func TestErrorHandlingAndEdgeCases(t *testing.T) {
	// Test nil store
	_, err := tus.NewHandler(tus.Config{Store: nil})
	if err == nil {
		t.Errorf("expected error for nil store")
	}

	h, _, _ := setupTestServer(t)

	// Unknown path
	req := httptest.NewRequest(http.MethodPost, "/other/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}

	// Root basePath with invalid method
	req = httptest.NewRequest(http.MethodPut, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}

	// Invalid method on item
	req = httptest.NewRequest(http.MethodPut, "/files/abc", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}

	// Missing Upload-Length on POST
	req = httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing length, got %d", w.Code)
	}

	// Duplicate metadata keys
	req = httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "10")
	req.Header.Set("Upload-Metadata", "k1 dGVzdA==, k1 dGVzdDI=")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate metadata key, got %d", w.Code)
	}

	// Metadata too long
	longMeta := "key " + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 4100))
	req = httptest.NewRequest(http.MethodPost, "/files/", nil)
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Upload-Length", "10")
	req.Header.Set("Upload-Metadata", longMeta)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for metadata too long, got %d", w.Code)
	}

	// Error.Error() coverage
	e1 := &tus.Error{Code: tus.ErrCodeNotFound}
	if e1.Error() != "tusuploadserver: not_found" {
		t.Errorf("e1.Error() = %s", e1.Error())
	}
	e2 := &tus.Error{Code: tus.ErrCodeNotFound, Message: "some message"}
	if e2.Error() != "tusuploadserver: not_found (some message)" {
		t.Errorf("e2.Error() = %s", e2.Error())
	}
}
