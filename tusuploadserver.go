package tusuploadserver

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Protocol constants
const (
	TusVersion            = "1.0.0"
	TusResumableHeader    = "Tus-Resumable"
	TusVersionHeader      = "Tus-Version"
	TusExtensionHeader    = "Tus-Extension"
	TusMaxSizeHeader      = "Tus-Max-Size"
	TusChecksumAlgoHeader = "Tus-Checksum-Algorithm"

	UploadLengthHeader   = "Upload-Length"
	UploadOffsetHeader   = "Upload-Offset"
	UploadMetadataHeader = "Upload-Metadata"
	UploadExpiresHeader  = "Upload-Expires"
	UploadChecksumHeader = "Upload-Checksum"

	ContentTypeOffsetOctetStream = "application/offset+octet-stream"
	DefaultMaxSize               = int64(1073741824) // 1 GiB
	DefaultBasePath              = "/files/"
	MaxMetadataLength            = 4096
)

// Error represents a store or domain error with a machine-readable code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("tusuploadserver: %s (%s)", e.Code, e.Message)
	}
	return fmt.Sprintf("tusuploadserver: %s", e.Code)
}

// Store error codes
const (
	ErrCodeNotFound         = "not_found"
	ErrCodeConflict         = "conflict"
	ErrCodeLocked           = "locked"
	ErrCodeTooLarge         = "too_large"
	ErrCodeChecksumMismatch = "checksum_mismatch"
)

// Info holds upload metadata and progress.
type Info struct {
	ID        string    `json:"id"`
	Length    int64     `json:"length"`
	Offset    int64     `json:"offset"`
	Metadata  string    `json:"metadata"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// Event records an upload state change.
type Event struct {
	Kind   string
	ID     string
	Offset int64
}

// Store defines the storage backend for tus uploads.
type Store interface {
	Create(ctx context.Context, info Info) error
	Info(ctx context.Context, id string) (Info, error)
	WriteAt(ctx context.Context, id string, offset int64, r io.Reader) (int64, error)
	Read(ctx context.Context, id string) (io.ReadCloser, error)
	Delete(ctx context.Context, id string) error
}

// Config configures the tus HTTP handler.
type Config struct {
	BasePath   string
	Store      Store
	MaxSize    int64
	Expiration time.Duration
	Clock      func() time.Time
	NewID      func() string
	OnEvent    func(Event)
}

// idRegex validates upload IDs to ensure they cannot traverse directories.
var idRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// handler implements http.Handler for tus uploads.
type handler struct {
	basePath   string
	store      Store
	maxSize    int64
	expiration time.Duration
	clock      func() time.Time
	newID      func() string
	onEvent    func(Event)

	lockMu sync.Mutex
	locks  map[string]struct{}
}

// NewHandler returns a new http.Handler implementing the tus 1.0.0 protocol.
func NewHandler(cfg Config) (http.Handler, error) {
	if cfg.Store == nil {
		return nil, errors.New("tusuploadserver: Store must not be nil")
	}
	basePath := cfg.BasePath
	if basePath == "" {
		basePath = DefaultBasePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	maxSize := cfg.MaxSize
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	newID := cfg.NewID
	if newID == nil {
		newID = defaultNewID
	}

	return &handler{
		basePath:   basePath,
		store:      cfg.Store,
		maxSize:    maxSize,
		expiration: cfg.Expiration,
		clock:      clock,
		newID:      newID,
		onEvent:    cfg.OnEvent,
		locks:      make(map[string]struct{}),
	}, nil
}

func defaultNewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (h *handler) lockID(id string) bool {
	h.lockMu.Lock()
	defer h.lockMu.Unlock()
	if _, exists := h.locks[id]; exists {
		return false
	}
	h.locks[id] = struct{}{}
	return true
}

func (h *handler) unlockID(id string) {
	h.lockMu.Lock()
	delete(h.locks, id)
	h.lockMu.Unlock()
}

func (h *handler) emit(kind, id string, offset int64) {
	if h.onEvent != nil {
		h.onEvent(Event{
			Kind:   kind,
			ID:     id,
			Offset: offset,
		})
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// OPTIONS handles capabilities and discovery without requiring Tus-Resumable
	if r.Method == http.MethodOptions {
		h.handleOptions(w, r)
		return
	}

	// Every request except OPTIONS must verify Tus-Resumable: 1.0.0
	if r.Header.Get(TusResumableHeader) != TusVersion {
		w.Header().Set(TusVersionHeader, TusVersion)
		http.Error(w, "Precondition Failed: unsupported tus version", http.StatusPreconditionFailed)
		return
	}

	// Every tus response carries Tus-Resumable: 1.0.0
	w.Header().Set(TusResumableHeader, TusVersion)

	cleanPath := r.URL.Path
	if !strings.HasPrefix(cleanPath, h.basePath) {
		http.NotFound(w, r)
		return
	}

	suffix := strings.TrimPrefix(cleanPath, h.basePath)
	if suffix == "" {
		if r.Method == http.MethodPost {
			h.handlePost(w, r)
			return
		}
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// Sub-path: must be a valid ID
	id := suffix
	if !idRegex.MatchString(id) {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodHead:
		h.handleHead(w, r, id)
	case http.MethodPatch:
		h.handlePatch(w, r, id)
	case http.MethodDelete:
		h.handleDelete(w, r, id)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (h *handler) handleOptions(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set(TusResumableHeader, TusVersion)
	w.Header().Set(TusVersionHeader, TusVersion)
	w.Header().Set(TusExtensionHeader, "creation,creation-with-upload,termination,expiration,checksum")
	w.Header().Set(TusMaxSizeHeader, strconv.FormatInt(h.maxSize, 10))
	w.Header().Set(TusChecksumAlgoHeader, "sha1")
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handlePost(w http.ResponseWriter, r *http.Request) {
	lengthHeader := r.Header.Get(UploadLengthHeader)
	if lengthHeader == "" {
		http.Error(w, "Upload-Length header is required", http.StatusBadRequest)
		return
	}
	length, err := strconv.ParseInt(lengthHeader, 10, 64)
	if err != nil || length < 0 {
		http.Error(w, "Invalid Upload-Length header", http.StatusBadRequest)
		return
	}
	if length > h.maxSize {
		http.Error(w, "Upload length exceeds maximum permitted size", http.StatusRequestEntityTooLarge)
		return
	}

	metadata := r.Header.Get(UploadMetadataHeader)
	if len(metadata) > MaxMetadataLength {
		http.Error(w, "Upload-Metadata exceeds limit", http.StatusBadRequest)
		return
	}
	if metadata != "" {
		if err := validateMetadata(metadata); err != nil {
			http.Error(w, fmt.Sprintf("Invalid Upload-Metadata: %v", err), http.StatusBadRequest)
			return
		}
	}

	id := h.newID()
	if !idRegex.MatchString(id) {
		http.Error(w, "Generated ID is invalid", http.StatusInternalServerError)
		return
	}

	var expiresAt time.Time
	if h.expiration > 0 {
		expiresAt = h.clock().Add(h.expiration)
	}

	info := Info{
		ID:        id,
		Length:    length,
		Offset:    0,
		Metadata:  metadata,
		ExpiresAt: expiresAt,
	}

	if err := h.store.Create(r.Context(), info); err != nil {
		http.Error(w, "Failed to create upload", http.StatusInternalServerError)
		return
	}
	h.emit("create", id, 0)

	// Check creation-with-upload
	contentType := r.Header.Get("Content-Type")
	if contentType == ContentTypeOffsetOctetStream && r.Body != nil {
		// First chunk provided in POST body
		if !h.lockID(id) {
			http.Error(w, "Locked", http.StatusLocked)
			return
		}
		defer h.unlockID(id)

		nw, status, err := h.writeChunk(r.Context(), id, info, 0, r.Header.Get(UploadChecksumHeader), r.Body)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		info.Offset = nw
		h.emit("write", id, info.Offset)
	}

	location := h.basePath + id
	w.Header().Set("Location", location)
	w.Header().Set(UploadOffsetHeader, strconv.FormatInt(info.Offset, 10))
	if !info.ExpiresAt.IsZero() {
		w.Header().Set(UploadExpiresHeader, info.ExpiresAt.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *handler) handleHead(w http.ResponseWriter, r *http.Request, id string) {
	info, err := h.store.Info(r.Context(), id)
	if err != nil {
		h.handleStoreError(w, err)
		return
	}

	if !info.ExpiresAt.IsZero() && h.clock().After(info.ExpiresAt) {
		_ = h.store.Delete(r.Context(), id)
		h.emit("delete", id, info.Offset)
		http.Error(w, "Upload expired", http.StatusGone)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set(UploadOffsetHeader, strconv.FormatInt(info.Offset, 10))
	w.Header().Set(UploadLengthHeader, strconv.FormatInt(info.Length, 10))
	if info.Metadata != "" {
		w.Header().Set(UploadMetadataHeader, info.Metadata)
	}
	if !info.ExpiresAt.IsZero() {
		w.Header().Set(UploadExpiresHeader, info.ExpiresAt.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusOK)
}

func (h *handler) handlePatch(w http.ResponseWriter, r *http.Request, id string) {
	contentType := r.Header.Get("Content-Type")
	if contentType != ContentTypeOffsetOctetStream {
		http.Error(w, "Unsupported Media Type: expected Content-Type application/offset+octet-stream", http.StatusUnsupportedMediaType)
		return
	}

	offsetHeader := r.Header.Get(UploadOffsetHeader)
	if offsetHeader == "" {
		http.Error(w, "Missing Upload-Offset header", http.StatusBadRequest)
		return
	}
	offset, err := strconv.ParseInt(offsetHeader, 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "Invalid Upload-Offset header", http.StatusBadRequest)
		return
	}

	if !h.lockID(id) {
		http.Error(w, "Upload is currently locked by another request", http.StatusLocked)
		return
	}
	defer h.unlockID(id)

	info, err := h.store.Info(r.Context(), id)
	if err != nil {
		h.handleStoreError(w, err)
		return
	}

	if !info.ExpiresAt.IsZero() && h.clock().After(info.ExpiresAt) {
		_ = h.store.Delete(r.Context(), id)
		h.emit("delete", id, info.Offset)
		http.Error(w, "Upload expired", http.StatusGone)
		return
	}

	if info.Offset != offset {
		http.Error(w, fmt.Sprintf("Upload-Offset conflict: current offset is %d, got %d", info.Offset, offset), http.StatusConflict)
		return
	}

	checksumHeader := r.Header.Get(UploadChecksumHeader)
	newOffset, status, err := h.writeChunk(r.Context(), id, info, offset, checksumHeader, r.Body)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}

	h.emit("write", id, newOffset)

	w.Header().Set(UploadOffsetHeader, strconv.FormatInt(newOffset, 10))
	if !info.ExpiresAt.IsZero() {
		w.Header().Set(UploadExpiresHeader, info.ExpiresAt.UTC().Format(http.TimeFormat))
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) writeChunk(ctx context.Context, id string, info Info, offset int64, checksumHeader string, body io.Reader) (int64, int, error) {
	if offset > info.Length {
		return offset, http.StatusRequestEntityTooLarge, errors.New("offset exceeds upload length")
	}

	// Check if incoming chunk has checksum extension
	if checksumHeader != "" {
		parts := strings.SplitN(strings.TrimSpace(checksumHeader), " ", 2)
		if len(parts) != 2 {
			return offset, http.StatusBadRequest, errors.New("invalid Upload-Checksum header format")
		}
		algo := parts[0]
		if algo != "sha1" {
			return offset, http.StatusBadRequest, fmtSprintfError("unsupported checksum algorithm: %s", algo)
		}
		expectedDigest, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return offset, http.StatusBadRequest, errors.New("invalid base64 in Upload-Checksum")
		}

		// Buffer the whole chunk to a temp file and compute sha1
		tmpFile, err := os.CreateTemp("", "tus-checksum-*")
		if err != nil {
			return offset, http.StatusInternalServerError, errors.New("failed to create temp file")
		}
		tmpPath := tmpFile.Name()
		defer func() {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}()

		hasher := sha1.New()
		// Limit chunk to avoid exceeding remaining length
		maxBytes := info.Length - offset
		limitedReader := io.LimitReader(body, maxBytes+1)
		mw := io.MultiWriter(tmpFile, hasher)
		nWritten, copyErr := io.Copy(mw, limitedReader)
		if nWritten > maxBytes {
			return offset, http.StatusRequestEntityTooLarge, errors.New("request body exceeds upload length")
		}
		if copyErr != nil {
			return offset, http.StatusInternalServerError, errors.New("failed reading request body")
		}

		if string(hasher.Sum(nil)) != string(expectedDigest) {
			// HTTP 460 Checksum Mismatch
			return offset, 460, errors.New("checksum mismatch")
		}

		// Checksum matched! Commit chunk to store
		if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
			return offset, http.StatusInternalServerError, errors.New("failed seeking temp file")
		}
		nCommitted, err := h.store.WriteAt(ctx, id, offset, tmpFile)
		if err != nil {
			return offset, http.StatusInternalServerError, err
		}
		return offset + nCommitted, http.StatusOK, nil
	}

	// No checksum: stream directly into store. If client drops or an error occurs,
	// any data received before read error or disconnect is kept and offset advanced.
	maxBytes := info.Length - offset
	limitedReader := io.LimitReader(body, maxBytes+1)

	// Pre-check Content-Length if provided
	chunkTracker := &countingReader{r: limitedReader, limit: maxBytes}
	nWritten, err := h.store.WriteAt(ctx, id, offset, chunkTracker)
	if chunkTracker.exceeded {
		return offset + nWritten, http.StatusRequestEntityTooLarge, errors.New("request body exceeds upload length")
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		var e *Error
		if errors.As(err, &e) && e.Code == ErrCodeTooLarge {
			return offset, http.StatusRequestEntityTooLarge, err
		}
	}
	return offset + nWritten, http.StatusOK, nil
}

type countingReader struct {
	r        io.Reader
	limit    int64
	read     int64
	exceeded bool
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	cr.read += int64(n)
	if cr.read > cr.limit {
		cr.exceeded = true
		return n, &Error{Code: ErrCodeTooLarge, Message: "write exceeds upload length"}
	}
	return n, err
}

func (h *handler) handleDelete(w http.ResponseWriter, r *http.Request, id string) {
	info, err := h.store.Info(r.Context(), id)
	if err != nil {
		h.handleStoreError(w, err)
		return
	}

	err = h.store.Delete(r.Context(), id)
	if err != nil {
		h.handleStoreError(w, err)
		return
	}
	h.emit("delete", id, info.Offset)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleStoreError(w http.ResponseWriter, err error) {
	var storeErr *Error
	if errors.As(err, &storeErr) {
		switch storeErr.Code {
		case ErrCodeNotFound:
			http.NotFound(w, nil)
			return
		case ErrCodeConflict:
			http.Error(w, storeErr.Error(), http.StatusConflict)
			return
		case ErrCodeLocked:
			http.Error(w, storeErr.Error(), http.StatusLocked)
			return
		case ErrCodeTooLarge:
			http.Error(w, storeErr.Error(), http.StatusRequestEntityTooLarge)
			return
		case ErrCodeChecksumMismatch:
			http.Error(w, storeErr.Error(), 460)
			return
		}
	}
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func validateMetadata(meta string) error {
	parts := strings.Split(meta, ",")
	seenKeys := make(map[string]struct{})
	for _, part := range parts {
		pair := strings.TrimSpace(part)
		if pair == "" {
			continue
		}
		fields := strings.Fields(pair)
		if len(fields) == 0 || len(fields) > 2 {
			return errors.New("invalid metadata format")
		}
		key := fields[0]
		if _, exists := seenKeys[key]; exists {
			return fmtSprintfError("duplicate metadata key: %s", key)
		}
		seenKeys[key] = struct{}{}
		if len(fields) == 2 {
			if _, err := base64.StdEncoding.DecodeString(fields[1]); err != nil {
				return fmtSprintfError("invalid base64 for key: %s", key)
			}
		}
	}
	return nil
}

func fmtSprintfError(format string, a ...any) error {
	return errors.New(fmt.Sprintf(format, a...))
}

// dirStore implements Store on the local filesystem.
type dirStore struct {
	rootPath string
	mu       sync.RWMutex
}

// NewDirStore creates a new filesystem-backed Store at root.
func NewDirStore(root string) (Store, error) {
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &dirStore{rootPath: abs}, nil
}

// sanitizeID verifies id contains only safe characters and returns its base name.
// The returned value is always the output of filepath.Base, so it can never carry a
// directory component: the store only ever touches files directly under rootPath.
func sanitizeID(id string) (string, error) {
	if !idRegex.MatchString(id) {
		return "", &Error{Code: ErrCodeNotFound, Message: "invalid id"}
	}
	base := filepath.Base(id)
	if base != id {
		return "", &Error{Code: ErrCodeNotFound, Message: "invalid id"}
	}
	return base, nil
}

// entryPath returns the path of the entry for id with extension ext, confined to rootPath.
func entryPath(root, id, ext string) (string, error) {
	safeID, err := sanitizeID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, safeID+ext), nil
}

// tmpEntryPath returns a fresh temporary path for id, confined to rootPath.
func tmpEntryPath(root, id string) (string, error) {
	safeID, err := sanitizeID(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, fmt.Sprintf("%s-%d.tmp", safeID, time.Now().UnixNano())), nil
}

// requireEntry reports ErrCodeNotFound when the entry at p does not exist.
func requireEntry(p string) error {
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return &Error{Code: ErrCodeNotFound, Message: "upload not found"}
		}
		return err
	}
	return nil
}

// requireNoEntry reports ErrCodeConflict when an entry already exists at p.
func requireNoEntry(p string) error {
	if _, err := os.Stat(p); err == nil {
		return &Error{Code: ErrCodeConflict, Message: "upload already exists"}
	}
	return nil
}

func (s *dirStore) Create(_ context.Context, info Info) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jsonPath, err := entryPath(s.rootPath, info.ID, ".json")
	if err != nil {
		return err
	}
	if err := requireNoEntry(jsonPath); err != nil {
		return err
	}
	binPath, err := entryPath(s.rootPath, info.ID, ".bin")
	if err != nil {
		return err
	}
	if err := requireNoEntry(binPath); err != nil {
		return err
	}

	binFile, err := os.OpenFile(binPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_ = binFile.Close()

	data, err := json.Marshal(info)
	if err != nil {
		return err
	}

	tmpPath, err := tmpEntryPath(s.rootPath, info.ID)
	if err != nil {
		return err
	}
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, jsonPath)
}

func (s *dirStore) writeInfoLocked(info Info) error {
	jsonPath, err := entryPath(s.rootPath, info.ID, ".json")
	if err != nil {
		return err
	}
	if err := requireEntry(jsonPath); err != nil {
		return err
	}

	data, err := json.Marshal(info)
	if err != nil {
		return err
	}

	tmpPath, err := tmpEntryPath(s.rootPath, info.ID)
	if err != nil {
		return err
	}
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	return os.Rename(tmpPath, jsonPath)
}

func (s *dirStore) Info(_ context.Context, id string) (Info, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	jsonPath, err := entryPath(s.rootPath, id, ".json")
	if err != nil {
		return Info{}, err
	}
	if err := requireEntry(jsonPath); err != nil {
		return Info{}, err
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Info{}, &Error{Code: ErrCodeNotFound, Message: "upload not found"}
		}
		return Info{}, err
	}

	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

func (s *dirStore) WriteAt(_ context.Context, id string, offset int64, r io.Reader) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jsonPath, err := entryPath(s.rootPath, id, ".json")
	if err != nil {
		return 0, err
	}
	if err := requireEntry(jsonPath); err != nil {
		return 0, err
	}
	binPath, err := entryPath(s.rootPath, id, ".bin")
	if err != nil {
		return 0, err
	}
	if err := requireEntry(binPath); err != nil {
		return 0, err
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, &Error{Code: ErrCodeNotFound, Message: "upload not found"}
		}
		return 0, err
	}

	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return 0, err
	}

	if info.Offset != offset {
		return 0, &Error{Code: ErrCodeConflict, Message: "offset mismatch"}
	}

	binFile, err := os.OpenFile(binPath, os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer binFile.Close()

	if _, err := binFile.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}

	buf := make([]byte, 32*1024)
	var totalWritten int64
	var writeErr error

	for {
		n, rErr := r.Read(buf)
		if n > 0 {
			if offset+totalWritten+int64(n) > info.Length {
				writeErr = &Error{Code: ErrCodeTooLarge, Message: "data exceeds upload length"}
				break
			}
			wn, wErr := binFile.Write(buf[:n])
			totalWritten += int64(wn)
			if wErr != nil {
				writeErr = wErr
				break
			}
		}
		if rErr != nil {
			if !errors.Is(rErr, io.EOF) {
				writeErr = rErr
			}
			break
		}
	}

	// fsync data file first
	_ = binFile.Sync()

	// update info
	info.Offset += totalWritten
	infoErr := s.writeInfoLocked(info)
	if infoErr != nil && writeErr == nil {
		writeErr = infoErr
	}

	return totalWritten, writeErr
}

func (s *dirStore) Read(_ context.Context, id string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	binPath, err := entryPath(s.rootPath, id, ".bin")
	if err != nil {
		return nil, err
	}
	if err := requireEntry(binPath); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(binPath, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &Error{Code: ErrCodeNotFound, Message: "upload not found"}
		}
		return nil, err
	}
	return f, nil
}

func (s *dirStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	binPath, err := entryPath(s.rootPath, id, ".bin")
	if err != nil {
		return err
	}
	if err := requireEntry(binPath); err != nil {
		return err
	}
	jsonPath, err := entryPath(s.rootPath, id, ".json")
	if err != nil {
		return err
	}
	if err := requireEntry(jsonPath); err != nil {
		return err
	}

	_ = os.Remove(binPath)
	_ = os.Remove(jsonPath)
	return nil
}
