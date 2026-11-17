package tusuploadserver_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tus "github.com/Trysac/u921-tus-upload-server"
)

func newDirStore(t *testing.T) (tus.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "nested", "store")
	store, err := tus.NewDirStore(root)
	if err != nil {
		t.Fatalf("NewDirStore: %v", err)
	}
	return store, root
}

func storeErrCode(err error) string {
	var se *tus.Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestDirStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store, root := newDirStore(t)

	if err := store.Create(ctx, tus.Info{ID: "abc", Length: 5}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	for _, name := range []string{"abc.json", "abc.bin"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("expected %s under the store root: %v", name, err)
		}
	}
	if err := store.Create(ctx, tus.Info{ID: "abc", Length: 5}); storeErrCode(err) != tus.ErrCodeConflict {
		t.Fatalf("duplicate Create: want conflict, got %v", err)
	}

	n, err := store.WriteAt(ctx, "abc", 0, strings.NewReader("he"))
	if err != nil || n != 2 {
		t.Fatalf("WriteAt: n=%d err=%v", n, err)
	}
	if _, err := store.WriteAt(ctx, "abc", 0, strings.NewReader("x")); storeErrCode(err) != tus.ErrCodeConflict {
		t.Fatalf("WriteAt wrong offset: want conflict, got %v", err)
	}
	n, err = store.WriteAt(ctx, "abc", 2, strings.NewReader("llo!!"))
	if storeErrCode(err) != tus.ErrCodeTooLarge {
		t.Fatalf("WriteAt past length: want too_large, got n=%d err=%v", n, err)
	}

	info, err := store.Info(ctx, "abc")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.ID != "abc" || info.Length != 5 || info.Offset != 2 {
		t.Fatalf("Info after rejected write: %+v", info)
	}

	n, err = store.WriteAt(ctx, "abc", 2, strings.NewReader("llo"))
	if err != nil || n != 3 {
		t.Fatalf("WriteAt completion: n=%d err=%v", n, err)
	}
	rc, err := store.Read(ctx, "abc")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	body, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || string(body) != "hello" {
		t.Fatalf("Read body = %q, err=%v", body, err)
	}

	if err := store.Delete(ctx, "abc"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("store root not empty after Delete: %v", entries)
	}
}

func TestDirStoreMissingAndInvalidIDs(t *testing.T) {
	ctx := context.Background()
	store, root := newDirStore(t)
	outside := filepath.Join(filepath.Dir(root), "secret.json")
	if err := os.WriteFile(outside, []byte(`{"id":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"missing", "../secret", "..", "a/b", `a\b`, "", strings.Repeat("a", 65)} {
		if _, err := store.Info(ctx, id); storeErrCode(err) != tus.ErrCodeNotFound {
			t.Errorf("Info(%q): want not_found, got %v", id, err)
		}
		if _, err := store.WriteAt(ctx, id, 0, strings.NewReader("x")); storeErrCode(err) != tus.ErrCodeNotFound {
			t.Errorf("WriteAt(%q): want not_found, got %v", id, err)
		}
		if _, err := store.Read(ctx, id); storeErrCode(err) != tus.ErrCodeNotFound {
			t.Errorf("Read(%q): want not_found, got %v", id, err)
		}
		if err := store.Delete(ctx, id); storeErrCode(err) != tus.ErrCodeNotFound {
			t.Errorf("Delete(%q): want not_found, got %v", id, err)
		}
	}
	for _, id := range []string{"../secret", "a/b", ".."} {
		if err := store.Create(ctx, tus.Info{ID: id, Length: 1}); storeErrCode(err) != tus.ErrCodeNotFound {
			t.Errorf("Create(%q): want invalid id error, got %v", id, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("file outside the store root was touched: %v", err)
	}
}

func TestDirStoreHalfMissingEntry(t *testing.T) {
	ctx := context.Background()
	store, root := newDirStore(t)
	if err := store.Create(ctx, tus.Info{ID: "half", Length: 3}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "half.bin")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteAt(ctx, "half", 0, strings.NewReader("a")); storeErrCode(err) != tus.ErrCodeNotFound {
		t.Fatalf("WriteAt without data file: want not_found, got %v", err)
	}
	if _, err := store.Read(ctx, "half"); storeErrCode(err) != tus.ErrCodeNotFound {
		t.Fatalf("Read without data file: want not_found, got %v", err)
	}
	if err := store.Delete(ctx, "half"); storeErrCode(err) != tus.ErrCodeNotFound {
		t.Fatalf("Delete without data file: want not_found, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "half.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "half.json")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "half"); storeErrCode(err) != tus.ErrCodeNotFound {
		t.Fatalf("Delete without info file: want not_found, got %v", err)
	}
	if err := store.Create(ctx, tus.Info{ID: "half", Length: 3}); storeErrCode(err) != tus.ErrCodeConflict {
		t.Fatalf("Create over leftover data file: want conflict, got %v", err)
	}
}

func TestDirStoreCorruptInfo(t *testing.T) {
	ctx := context.Background()
	store, root := newDirStore(t)
	if err := store.Create(ctx, tus.Info{ID: "bad", Length: 3}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Info(ctx, "bad"); err == nil || storeErrCode(err) != "" {
		t.Fatalf("Info on corrupt metadata: want a non-domain error, got %v", err)
	}
	if _, err := store.WriteAt(ctx, "bad", 0, strings.NewReader("a")); err == nil || storeErrCode(err) != "" {
		t.Fatalf("WriteAt on corrupt metadata: want a non-domain error, got %v", err)
	}
}

// fakeStore returns a fixed error from every operation, to pin the HTTP mapping of store errors.
type fakeStore struct {
	infoErr   error
	deleteErr error
	writeErr  error
}

func (f *fakeStore) Create(context.Context, tus.Info) error { return nil }
func (f *fakeStore) Info(_ context.Context, id string) (tus.Info, error) {
	if f.infoErr != nil {
		return tus.Info{}, f.infoErr
	}
	return tus.Info{ID: id, Length: 10}, nil
}
func (f *fakeStore) WriteAt(context.Context, string, int64, io.Reader) (int64, error) {
	return 0, f.writeErr
}
func (f *fakeStore) Read(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}
func (f *fakeStore) Delete(context.Context, string) error { return f.deleteErr }

func TestStoreErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not found", &tus.Error{Code: tus.ErrCodeNotFound}, http.StatusNotFound},
		{"conflict", &tus.Error{Code: tus.ErrCodeConflict}, http.StatusConflict},
		{"locked", &tus.Error{Code: tus.ErrCodeLocked}, http.StatusLocked},
		{"too large", &tus.Error{Code: tus.ErrCodeTooLarge}, http.StatusRequestEntityTooLarge},
		{"checksum", &tus.Error{Code: tus.ErrCodeChecksumMismatch}, 460},
		{"unknown code", &tus.Error{Code: "other"}, http.StatusInternalServerError},
		{"plain error", errors.New("disk on fire"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, viaDelete := range []bool{false, true} {
				fs := &fakeStore{infoErr: tc.err}
				if viaDelete {
					fs = &fakeStore{deleteErr: tc.err}
				}
				h, err := tus.NewHandler(tus.Config{Store: fs})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodDelete, "/files/abc", nil)
				req.Header.Set("Tus-Resumable", "1.0.0")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != tc.want {
					t.Fatalf("viaDelete=%v: status = %d, want %d (body %q)", viaDelete, rec.Code, tc.want, rec.Body.String())
				}
				if tc.want == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "disk on fire") {
					t.Fatalf("internal error detail leaked to the client: %q", rec.Body.String())
				}
			}
		})
	}
}

func TestNewHandlerRequiresStore(t *testing.T) {
	if _, err := tus.NewHandler(tus.Config{}); err == nil {
		t.Fatal("NewHandler without a Store: want error")
	}
}
