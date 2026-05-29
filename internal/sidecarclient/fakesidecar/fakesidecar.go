package fakesidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type Options struct {
	Version       string
	Commit        string
	StorageStatus int
	PutCASStatus  int
	LinkStatus    int
	StreamStatus  int
	Hang          bool
}

type Server struct {
	server *httptest.Server
	mu     sync.Mutex
	once   sync.Once

	version           string
	commit            string
	storageStatus     int
	putCASStatus      int
	linkStatus        int
	streamStatus      int
	hang              bool
	hangDone          chan struct{}
	lastAuthorization string
	putCASCount       int
	lastPutCAS        PutCASRequest
	lastLink          LinkRequest
	lastStream        StreamRequest
}

type PutCASRequest struct {
	FilePath      string
	ContentLength int64
	Body          []byte
}

type LinkRequest struct {
	StorageMount string
	RemotePath   string
	FullPath     string
}

type StreamRequest struct {
	Header http.Header
	Path   string
}

func New(t *testing.T, opts Options) *Server {
	t.Helper()
	s := &Server{
		version:       opts.Version,
		commit:        firstNonEmpty(opts.Commit, opts.Version),
		storageStatus: defaultStatus(opts.StorageStatus, http.StatusOK),
		putCASStatus:  defaultStatus(opts.PutCASStatus, http.StatusOK),
		linkStatus:    defaultStatus(opts.LinkStatus, http.StatusOK),
		streamStatus:  defaultStatus(opts.StreamStatus, 0),
		hang:          opts.Hang,
		hangDone:      make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/api/public/settings", s.handleSettings)
	mux.HandleFunc("/api/admin/storage/list", s.handleStorages)
	mux.HandleFunc("/api/fs/put", s.handlePutCAS)
	mux.HandleFunc("/api/fs/link", s.handleLink)
	mux.HandleFunc("/d/", s.handleStream)
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func defaultStatus(got, fallback int) int {
	if got == 0 {
		return fallback
	}
	return got
}

func (s *Server) URL() string {
	return s.server.URL
}

func (s *Server) Close() {
	s.once.Do(func() {
		close(s.hangDone)
		s.server.Close()
	})
}

func (s *Server) LastAuthorization() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastAuthorization
}

func (s *Server) LastPutCASRequest() PutCASRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPutCAS
}

func (s *Server) PutCASCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putCASCount
}

func (s *Server) LastLinkRequest() LinkRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastLink
}

func (s *Server) LastStreamRequest() StreamRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastStream
}

func (s *Server) recordAuth(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAuthorization = r.Header.Get("Authorization")
}

func (s *Server) maybeHang(r *http.Request) bool {
	if !s.hang {
		return false
	}
	select {
	case <-r.Context().Done():
	case <-s.hangDone:
	}
	return true
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.recordAuth(r)
	if s.maybeHang(r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": s.version,
		"commit":  s.commit,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.recordAuth(r)
	if s.maybeHang(r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]string{
			"version": s.version,
			"commit":  s.commit,
		},
	})
}

func (s *Server) handleStorages(w http.ResponseWriter, r *http.Request) {
	s.recordAuth(r)
	if s.maybeHang(r) {
		return
	}
	if s.storageStatus != http.StatusOK {
		writeJSON(w, s.storageStatus, map[string]string{"message": "storage error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": []map[string]string{
			{"id": "115-main", "provider": "115", "mount_path": "/115-main", "status": "ok"},
			{"id": "189-main", "provider": "189pc", "mount_path": "/189-main", "status": "ok"},
		},
	})
}

func (s *Server) handlePutCAS(w http.ResponseWriter, r *http.Request) {
	s.recordAuth(r)
	if s.maybeHang(r) {
		return
	}
	body, _ := io.ReadAll(r.Body)
	filePath := r.Header.Get("File-Path")
	contentLength := r.ContentLength
	if contentLength < 0 {
		contentLength, _ = strconv.ParseInt(r.Header.Get("Content-Length"), 10, 64)
	}
	s.mu.Lock()
	s.putCASCount++
	s.lastPutCAS = PutCASRequest{FilePath: filePath, ContentLength: contentLength, Body: body}
	s.mu.Unlock()

	if !strings.HasSuffix(filePath, ".cas") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "not a .cas file"})
		return
	}
	if s.putCASStatus != http.StatusOK {
		writeJSON(w, s.putCASStatus, map[string]string{"message": "put cas error"})
		return
	}

	cloudPath := strings.TrimSuffix(filePath, ".cas")
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"status":     "restored",
			"cloud_path": cloudPath,
			"size_bytes": int64(len(body)),
			"hashes": map[string]string{
				"sha1": "fake-sha1",
			},
		},
	})
}

func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	s.recordAuth(r)
	if s.maybeHang(r) {
		return
	}
	var payload map[string]string
	_ = json.NewDecoder(r.Body).Decode(&payload)
	storageMount := firstNonEmpty(payload["storage_mount"], payload["mount"])
	remotePath := firstNonEmpty(payload["remote_path"], payload["path"])
	fullPath := payload["path"]
	s.mu.Lock()
	s.lastLink = LinkRequest{StorageMount: storageMount, RemotePath: remotePath, FullPath: fullPath}
	s.mu.Unlock()

	if s.linkStatus != http.StatusOK {
		writeJSON(w, s.linkStatus, map[string]string{"message": "link error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"url":        "https://download.example" + remotePath,
			"headers":    map[string]string{"X-Download-Token": "fake-link-token"},
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		},
	})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	s.recordAuth(r)
	if s.maybeHang(r) {
		return
	}
	s.mu.Lock()
	s.lastStream = StreamRequest{Header: r.Header.Clone(), Path: r.URL.Path}
	s.mu.Unlock()

	if s.streamStatus != 0 {
		writeJSON(w, s.streamStatus, map[string]string{"message": "stream error"})
		return
	}

	body := "0123456789"
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("ETag", `"stream-etag"`)
	w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
	w.Header().Set("Content-Type", "application/octet-stream")

	if r.Header.Get("If-None-Match") == `"stream-etag"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		return
	}

	start, end, ok := parseSimpleRange(rangeHeader, len(body))
	if !ok {
		w.Header().Set("Content-Range", "bytes */"+strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	part := body[start : end+1]
	w.Header().Set("Content-Length", strconv.Itoa(len(part)))
	w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(end)+"/"+strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.WriteString(w, part)
}

func parseSimpleRange(value string, size int) (int, int, bool) {
	if !strings.HasPrefix(value, "bytes=") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	if start < 0 || end < start || start >= size {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func RemotePathFromDPath(p string) string {
	return "/" + strings.TrimPrefix(path.Clean(strings.TrimPrefix(p, "/d/")), "/")
}
