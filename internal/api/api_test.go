package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/10/artifact/internal/api"
	"github.com/10/artifact/internal/store"
)

const (
	testToken = "artifact-test-token-0123456789-abcdef"
	baseURL   = "https://artifact.example.com"
)

type harness struct {
	t       *testing.T
	handler http.Handler
	server  *httptest.Server
	dataDir string
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	return newHarnessWithLimit(t, 10<<20)
}

func newHarnessWithLimit(t *testing.T, maxUpload int64) *harness {
	t.Helper()

	dataDir := t.TempDir()

	st, err := store.Open(dataDir, filepath.Join(dataDir, "artifact.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { st.Close() })

	handler := api.New(st, api.Config{
		Token:         testToken,
		BaseURL:       baseURL,
		MaxUploadSize: maxUpload,
		Logger:        slog.New(slog.DiscardHandler),
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &harness{t: t, handler: handler, server: server, dataDir: dataDir}
}

type artifactBody struct {
	ID          string  `json:"id"`
	Slug        *string `json:"slug"`
	URL         string  `json:"url"`
	RawURL      string  `json:"raw_url"`
	Version     int     `json:"version"`
	VersionURL  string  `json:"version_url"`
	Filename    string  `json:"filename"`
	ContentType string  `json:"content_type"`
	Size        int64   `json:"size"`
	SHA256      string  `json:"sha256"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (h *harness) request(method, target string, body io.Reader) *http.Request {
	h.t.Helper()

	req, err := http.NewRequest(method, h.server.URL+target, body)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}

	return req
}

func (h *harness) authed(method, target string, body io.Reader) *http.Request {
	h.t.Helper()

	req := h.request(method, target, body)
	req.Header.Set("Authorization", "Bearer "+testToken)

	return req
}

func (h *harness) send(req *http.Request) (*http.Response, []byte) {
	h.t.Helper()

	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read body: %v", err)
	}

	return resp, body
}

// upload publishes a raw body and requires it to succeed.
func (h *harness) upload(filename string, body []byte, extra url.Values) artifactBody {
	h.t.Helper()

	resp, raw := h.send(h.authed(http.MethodPost, "/api/upload?"+uploadQuery(filename, extra), bytes.NewReader(body)))
	if resp.StatusCode != http.StatusCreated {
		h.t.Fatalf("upload %q: status %d, body %s", filename, resp.StatusCode, raw)
	}

	return decodeArtifact(h.t, raw)
}

// get fetches a public URL returned by the API, rewritten onto the test server.
func (h *harness) get(publicURL string) (*http.Response, []byte) {
	h.t.Helper()

	return h.send(h.request(http.MethodGet, pathOf(h.t, publicURL), nil))
}

func uploadQuery(filename string, extra url.Values) string {
	query := url.Values{}
	if filename != "" {
		query.Set("filename", filename)
	}

	maps.Copy(query, extra)

	return query.Encode()
}

func decodeArtifact(t *testing.T, raw []byte) artifactBody {
	t.Helper()

	var art artifactBody
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatalf("decode artifact response: %v (body %s)", err, raw)
	}

	return art
}

func decodeError(t *testing.T, raw []byte) errorBody {
	t.Helper()

	var body errorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode error response: %v (body %s)", err, raw)
	}

	return body
}

func pathOf(t *testing.T, raw string) string {
	t.Helper()

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}

	return parsed.EscapedPath()
}

func (h *harness) blobCount() int {
	h.t.Helper()

	entries, err := os.ReadDir(filepath.Join(h.dataDir, "blobs"))
	if err != nil {
		h.t.Fatalf("read blobs: %v", err)
	}

	return len(entries)
}

// ---------------------------------------------------------------- auth

func TestAuthRejectsBadTokens(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cases := map[string]string{
		"missing header":     "",
		"bare token":         testToken,
		"wrong scheme":       "Basic " + testToken,
		"wrong value":        "Bearer " + strings.Repeat("x", len(testToken)),
		"shorter than token": "Bearer " + testToken[:8],
		"longer than token":  "Bearer " + testToken + "extra",
		"empty bearer":       "Bearer ",
		"prefix of token":    "Bearer " + testToken[:len(testToken)-1],
	}

	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			req := h.request(http.MethodPost, "/api/upload?filename=a.txt", strings.NewReader("payload"))
			if header != "" {
				req.Header.Set("Authorization", header)
			}

			resp, raw := h.send(req)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, raw)
			}

			if code := decodeError(t, raw).Error.Code; code != "unauthorized" {
				t.Errorf("code = %q, want unauthorized", code)
			}
		})
	}

	if n := h.blobCount(); n != 0 {
		t.Errorf("rejected uploads wrote %d blob directories, want 0", n)
	}
}

func TestAuthAcceptsCaseInsensitiveScheme(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	req := h.request(http.MethodPost, "/api/upload?filename=a.txt", strings.NewReader("payload"))
	req.Header.Set("Authorization", "bearer "+testToken)

	resp, raw := h.send(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, raw)
	}
}

// panicBody fails the test if anything reads it.
type panicBody struct{ t *testing.T }

func (p panicBody) Read([]byte) (int, error) {
	p.t.Fatal("a 401 must be returned before the request body is read")

	return 0, io.EOF
}

func (p panicBody) Close() error { return nil }

func TestUnauthorizedUploadNeverReadsBody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	req := httptest.NewRequest(http.MethodPost, "/api/upload?filename=a.txt", panicBody{t: t})
	rec := httptest.NewRecorder()

	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("notes.txt", []byte("hello"), nil)

	resp, body := h.get(art.URL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestNoCookieIsEverSet(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("notes.txt", []byte("hello"), nil)

	for _, req := range []*http.Request{
		h.authed(http.MethodGet, "/api/artifacts", nil),
		h.request(http.MethodGet, pathOf(t, art.URL), nil),
		h.request(http.MethodGet, "/healthz", nil),
		h.request(http.MethodPost, "/api/upload?filename=a.txt", strings.NewReader("x")),
	} {
		resp, _ := h.send(req)
		if got := resp.Header.Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("%s %s set cookies: %v", req.Method, req.URL.Path, got)
		}
	}
}

// ---------------------------------------------------------------- upload

func TestUploadRawAndMultipartAgree(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	payload := []byte("<h1>report</h1>")

	raw := h.upload("report.html", payload, nil)

	var form bytes.Buffer

	writer := multipart.NewWriter(&form)

	part, err := writer.CreateFormFile("file", "report.html")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}

	if _, err := part.Write(payload); err != nil {
		t.Fatalf("write part: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := h.authed(http.MethodPost, "/api/upload", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, body := h.send(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("multipart status = %d, want 201 (body %s)", resp.StatusCode, body)
	}

	multi := decodeArtifact(t, body)

	if multi.Filename != raw.Filename {
		t.Errorf("filename: multipart %q, raw %q", multi.Filename, raw.Filename)
	}

	if multi.ContentType != raw.ContentType {
		t.Errorf("content type: multipart %q, raw %q", multi.ContentType, raw.ContentType)
	}

	if multi.Size != raw.Size {
		t.Errorf("size: multipart %d, raw %d", multi.Size, raw.Size)
	}

	if multi.SHA256 != raw.SHA256 {
		t.Errorf("sha256: multipart %q, raw %q", multi.SHA256, raw.SHA256)
	}

	if multi.ID == raw.ID {
		t.Error("two uploads shared an ID")
	}
}

func TestUploadMultipartFilenameAndSlugFields(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var form bytes.Buffer

	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("filename", "override.md")
	_ = writer.WriteField("slug", "from-a-form")

	part, err := writer.CreateFormFile("file", "ignored.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}

	_, _ = part.Write([]byte("# hi"))
	_ = writer.Close()

	req := h.authed(http.MethodPost, "/api/upload", &form)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, body := h.send(req)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, body)
	}

	art := decodeArtifact(t, body)

	if art.Filename != "override.md" {
		t.Errorf("filename = %q, want override.md", art.Filename)
	}

	if art.Slug == nil || *art.Slug != "from-a-form" {
		t.Errorf("slug = %v, want from-a-form", art.Slug)
	}

	if art.URL != baseURL+"/from-a-form" {
		t.Errorf("url = %q, want %s/from-a-form", art.URL, baseURL)
	}
}

func TestUploadRequiresFilename(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	resp, raw := h.send(h.authed(http.MethodPost, "/api/upload", strings.NewReader("data")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, raw)
	}

	if code := decodeError(t, raw).Error.Code; code != "bad_request" {
		t.Errorf("code = %q, want bad_request", code)
	}
}

func TestUploadRejectsEmptyBody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	resp, raw := h.send(h.authed(http.MethodPost, "/api/upload?filename=a.txt", strings.NewReader("")))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, raw)
	}

	if n := h.blobCount(); n != 0 {
		t.Errorf("empty upload left %d blob directories", n)
	}
}

func TestUploadTooLarge(t *testing.T) {
	t.Parallel()

	t.Run("raw body", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithLimit(t, 64)

		resp, raw := h.send(h.authed(http.MethodPost, "/api/upload?filename=big.txt",
			bytes.NewReader(bytes.Repeat([]byte("x"), 4096))))

		assertTooLarge(t, h, resp, raw)
	})

	t.Run("multipart", func(t *testing.T) {
		t.Parallel()

		h := newHarnessWithLimit(t, 512)

		var form bytes.Buffer

		writer := multipart.NewWriter(&form)

		part, err := writer.CreateFormFile("file", "big.txt")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}

		_, _ = part.Write(bytes.Repeat([]byte("x"), 8192))
		_ = writer.Close()

		req := h.authed(http.MethodPost, "/api/upload", &form)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		resp, raw := h.send(req)

		assertTooLarge(t, h, resp, raw)
	})
}

func assertTooLarge(t *testing.T, h *harness, resp *http.Response, raw []byte) {
	t.Helper()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", resp.StatusCode, raw)
	}

	if code := decodeError(t, raw).Error.Code; code != "payload_too_large" {
		t.Errorf("code = %q, want payload_too_large", code)
	}

	if n := h.blobCount(); n != 0 {
		t.Errorf("oversized upload left %d blob directories", n)
	}
}

func TestUploadSlugConflict(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.upload("a.txt", []byte("a"), url.Values{"slug": {"taken-slug"}})

	resp, raw := h.send(h.authed(http.MethodPost,
		"/api/upload?"+uploadQuery("b.txt", url.Values{"slug": {"taken-slug"}}),
		strings.NewReader("b")))

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", resp.StatusCode, raw)
	}

	if code := decodeError(t, raw).Error.Code; code != "slug_taken" {
		t.Errorf("code = %q, want slug_taken", code)
	}
}

func TestUploadRejectsBadSlugs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cases := map[string]string{
		"exactly eight characters": "eightchr",
		"reserved api":             "api",
		"reserved healthz":         "healthz",
		"reserved raw":             "raw",
		"reserved static":          "static",
		"uppercase":                "My-Report",
		"underscore":               "my_report",
		"leading hyphen":           "-report",
		"trailing hyphen":          "report-",
		"too short":                "ab",
		"too long":                 strings.Repeat("a", 65),
	}

	for name, slug := range cases {
		t.Run(name, func(t *testing.T) {
			resp, raw := h.send(h.authed(http.MethodPost,
				"/api/upload?"+uploadQuery("a.txt", url.Values{"slug": {slug}}),
				strings.NewReader("data")))

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("slug %q: status = %d, want 400 (body %s)", slug, resp.StatusCode, raw)
			}

			if code := decodeError(t, raw).Error.Code; code != "bad_request" {
				t.Errorf("code = %q, want bad_request", code)
			}
		})
	}
}

func TestUploadSanitizesFilename(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cases := []struct {
		given string
		want  string
	}{
		{"../../etc/passwd", "passwd"},
		{`..\..\evil.exe`, "evil.exe"},
		{"re\x00port.html", "report.html"},
		{"café ☕.md", "café ☕.md"},
		{strings.Repeat("a", 300) + ".txt", strings.Repeat("a", 255)},
	}

	for _, tc := range cases {
		art := h.upload(tc.given, []byte("data"), nil)
		if art.Filename != tc.want {
			t.Errorf("filename for %q = %q, want %q", tc.given, art.Filename, tc.want)
		}
	}

	// Nothing escaped the blob tree: one directory per artifact, named by ID.
	entries, err := os.ReadDir(filepath.Join(h.dataDir, "blobs"))
	if err != nil {
		t.Fatalf("read blobs: %v", err)
	}

	for _, entry := range entries {
		if len(entry.Name()) != 8 {
			t.Errorf("unexpected blob directory %q", entry.Name())
		}
	}
}

func TestUploadRejectsUnsalvageableFilename(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	for _, name := range []string{"..", ".", "/"} {
		resp, raw := h.send(h.authed(http.MethodPost,
			"/api/upload?"+uploadQuery(name, nil), strings.NewReader("data")))

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("filename %q: status = %d, want 400 (body %s)", name, resp.StatusCode, raw)
		}
	}
}

// ---------------------------------------------------------------- update

func TestUpdateCreatesVersionAndKeepsURL(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.upload("plan.md", []byte("# v1"), nil)

	resp, raw := h.send(h.authed(http.MethodPut,
		"/api/artifacts/"+first.ID+"?filename=plan.md", strings.NewReader("# v2")))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}

	second := decodeArtifact(t, raw)

	if second.Version != 2 {
		t.Errorf("version = %d, want 2", second.Version)
	}

	if second.URL != first.URL {
		t.Errorf("url changed from %q to %q", first.URL, second.URL)
	}

	if second.ID != first.ID {
		t.Errorf("id changed from %q to %q", first.ID, second.ID)
	}

	// The frozen version URL still returns the original bytes.
	_, v1 := h.get(first.VersionURL + "/raw")
	if string(v1) != "# v1" {
		t.Errorf("version 1 bytes = %q, want %q", v1, "# v1")
	}

	_, latest := h.get(second.RawURL)
	if string(latest) != "# v2" {
		t.Errorf("latest bytes = %q, want %q", latest, "# v2")
	}
}

func TestUpdateMissingArtifact(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	resp, raw := h.send(h.authed(http.MethodPut,
		"/api/artifacts/nosuchid?filename=a.txt", strings.NewReader("data")))

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", resp.StatusCode, raw)
	}

	if code := decodeError(t, raw).Error.Code; code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
}

func TestUpdateIgnoresSlug(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.upload("a.txt", []byte("v1"), url.Values{"slug": {"original-slug"}})

	resp, raw := h.send(h.authed(http.MethodPut,
		"/api/artifacts/"+first.ID+"?filename=a.txt&slug=a-new-slug", strings.NewReader("v2")))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}

	second := decodeArtifact(t, raw)
	if second.Slug == nil || *second.Slug != "original-slug" {
		t.Errorf("slug = %v, want original-slug", second.Slug)
	}
}

func TestUpdateBySlug(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.upload("a.txt", []byte("v1"), url.Values{"slug": {"daily-report"}})

	resp, raw := h.send(h.authed(http.MethodPut,
		"/api/artifacts/daily-report?filename=a.txt", strings.NewReader("v2")))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}

	if second := decodeArtifact(t, raw); second.ID != first.ID {
		t.Errorf("id = %q, want %q", second.ID, first.ID)
	}
}

// ---------------------------------------------------------------- delete

func TestDeleteRemovesEverything(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("plan.md", []byte("# hi"), url.Values{"slug": {"gone-soon"}})

	resp, raw := h.send(h.authed(http.MethodDelete, "/api/artifacts/"+art.ID, nil))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", resp.StatusCode, raw)
	}

	if len(raw) != 0 {
		t.Errorf("204 response carried a body: %s", raw)
	}

	if _, err := os.Stat(filepath.Join(h.dataDir, "blobs", art.ID)); !os.IsNotExist(err) {
		t.Errorf("blob directory survived delete (err = %v)", err)
	}

	if resp, _ := h.get(art.URL); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", resp.StatusCode)
	}

	if resp, _ := h.get(baseURL + "/gone-soon"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET by slug after delete = %d, want 404", resp.StatusCode)
	}

	resp, raw = h.send(h.authed(http.MethodDelete, "/api/artifacts/"+art.ID, nil))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404 (body %s)", resp.StatusCode, raw)
	}

	if code := decodeError(t, raw).Error.Code; code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
}

// ---------------------------------------------------------------- serving

func TestServeContentTypeTable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	cases := []struct {
		filename    string
		body        []byte
		contentType string
		disposition string
	}{
		{"plan.md", []byte("# hi"), "text/html; charset=utf-8", "inline"},
		{"plan.markdown", []byte("# hi"), "text/html; charset=utf-8", "inline"},
		{"report.html", []byte("<h1>hi</h1>"), "text/html; charset=utf-8", "inline"},
		{"report.htm", []byte("<h1>hi</h1>"), "text/html; charset=utf-8", "inline"},
		{"shot.png", []byte("\x89PNG\r\n\x1a\n"), "image/png", "inline"},
		{"shot.jpg", []byte("\xff\xd8\xff"), "image/jpeg", "inline"},
		{"anim.gif", []byte("GIF89a"), "image/gif", "inline"},
		{"pic.webp", []byte("RIFF0000WEBP"), "image/webp", "inline"},
		{"pic.avif", []byte("avif"), "image/avif", "inline"},
		{"icon.svg", []byte("<svg/>"), "image/svg+xml", "inline"},
		{"clip.mp4", []byte("moov"), "video/mp4", "inline"},
		{"clip.webm", []byte("webm"), "video/webm", "inline"},
		{"clip.mov", []byte("moov"), "video/quicktime", "inline"},
		{"tune.mp3", []byte("ID3"), "audio/mpeg", "inline"},
		{"tune.wav", []byte("RIFF"), "audio/wav", "inline"},
		{"tune.m4a", []byte("ftyp"), "audio/mp4", "inline"},
		{"memo.caf", []byte("caff"), "audio/x-caf", "inline"},
		{"paper.pdf", []byte("%PDF-1.4"), "application/pdf", "inline"},
		{"build.log", []byte("line one\n"), "text/plain; charset=utf-8", "inline"},
		{"notes.txt", []byte("hello"), "text/plain; charset=utf-8", "inline"},
		{"data.json", []byte(`{"a":1}`), "text/plain; charset=utf-8", "inline"},
		{"main.go", []byte("package main"), "text/plain; charset=utf-8", "inline"},
		{"blob.bin", []byte{0x00, 0x01, 0x02, 0xff}, "application/octet-stream", "attachment"},
		{"unknown.xyzzy", []byte{0x00, 0x01, 0x02, 0xff}, "application/octet-stream", "attachment"},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			art := h.upload(tc.filename, tc.body, nil)

			if art.ContentType != tc.contentType {
				t.Errorf("stored content type = %q, want %q", art.ContentType, tc.contentType)
			}

			resp, _ := h.get(art.URL)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			if got := resp.Header.Get("Content-Type"); got != tc.contentType {
				t.Errorf("Content-Type = %q, want %q", got, tc.contentType)
			}

			disposition := resp.Header.Get("Content-Disposition")
			if !strings.HasPrefix(disposition, tc.disposition+";") {
				t.Errorf("Content-Disposition = %q, want %s", disposition, tc.disposition)
			}

			if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
			}
		})
	}
}

func TestServeMarkdownRenderedAndRaw(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	source := "# Title\n\n| a |\n| - |\n| 1 |\n"
	art := h.upload("plan.md", []byte(source), nil)

	resp, rendered := h.get(art.URL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	page := string(rendered)

	if !strings.HasPrefix(page, "<!doctype html>") {
		t.Errorf("rendered page does not start with a doctype: %.40q", page)
	}

	if !strings.Contains(page, "<h1>Title</h1>") || !strings.Contains(page, "<table>") {
		t.Error("markdown was not rendered")
	}

	resp, raw := h.get(art.RawURL)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw status = %d, want 200", resp.StatusCode)
	}

	if string(raw) != source {
		t.Errorf("raw body = %q, want the original source", raw)
	}

	if got := resp.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Errorf("raw Content-Type = %q, want text/plain; charset=utf-8", got)
	}

	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("raw X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestServeHTMLByteForByte(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	source := "<!doctype html><html><body><script>alert(1)</script></body></html>"
	art := h.upload("report.html", []byte(source), nil)

	_, body := h.get(art.URL)
	if string(body) != source {
		t.Errorf("body = %q, want the bytes as uploaded", body)
	}

	// /raw never renders, and HTML was never rendered to begin with.
	_, raw := h.get(art.RawURL)
	if string(raw) != source {
		t.Errorf("raw body = %q, want the bytes as uploaded", raw)
	}
}

func TestServeFilenameForm(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("shot.png", []byte("\x89PNG\r\n\x1a\n"), nil)

	resp, _ := h.get(art.URL + "/shot.png")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("matching filename = %d, want 200", resp.StatusCode)
	}

	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}

	resp, _ = h.get(art.URL + "/wrong.png")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("mismatched filename = %d, want 404", resp.StatusCode)
	}

	resp, _ = h.get(art.VersionURL + "/shot.png")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("versioned filename form = %d, want 200", resp.StatusCode)
	}
}

func TestServeRootDoesNotRedirect(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("shot.png", []byte("\x89PNG\r\n\x1a\n"), nil)

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := client.Get(h.server.URL + pathOf(t, art.URL))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 with the bytes served directly", resp.StatusCode)
	}
}

func TestServeUnknownReferences(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("a.txt", []byte("data"), nil)

	cases := map[string]string{
		"unknown id":       "/zzzzzzzz",
		"unknown slug":     "/no-such-slug",
		"missing version":  pathOf(t, art.URL) + "@9",
		"version zero":     pathOf(t, art.URL) + "@0",
		"negative version": pathOf(t, art.URL) + "@-1",
		"garbage version":  pathOf(t, art.URL) + "@abc",
		"empty base":       "/@1",
		"root":             "/",
		"deep path":        pathOf(t, art.URL) + "/a/b",
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			resp, _ := h.send(h.request(http.MethodGet, path, nil))
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
			}
		})
	}
}

func TestServeVersionIsFrozen(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.upload("notes.txt", []byte("one"), nil)

	for _, body := range []string{"two", "three"} {
		resp, raw := h.send(h.authed(http.MethodPut,
			"/api/artifacts/"+first.ID+"?filename=notes.txt", strings.NewReader(body)))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("update: status %d (body %s)", resp.StatusCode, raw)
		}
	}

	for n, want := range map[int]string{1: "one", 2: "two", 3: "three"} {
		_, body := h.get(first.URL + "@" + strconv.Itoa(n))
		if string(body) != want {
			t.Errorf("version %d = %q, want %q", n, body, want)
		}
	}

	_, latest := h.get(first.URL)
	if string(latest) != "three" {
		t.Errorf("latest = %q, want three", latest)
	}
}

func TestServeLatestRevalidatesAfterUpdate(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.upload("notes.txt", []byte("one"), nil)

	resp, _ := h.get(first.URL)

	if got := resp.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("latest Cache-Control = %q, want no-cache", got)
	}

	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("latest response has no ETag")
	}

	lastModified := resp.Header.Get("Last-Modified")

	resp, raw := h.send(h.authed(http.MethodPut,
		"/api/artifacts/"+first.ID+"?filename=notes.txt", strings.NewReader("two")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d (body %s)", resp.StatusCode, raw)
	}

	// A browser holding version 1 sends both validators. The two versions can
	// share a Last-Modified second, so only the ETag can tell them apart.
	req := h.request(http.MethodGet, pathOf(t, first.URL), nil)
	req.Header.Set("If-None-Match", etag)
	req.Header.Set("If-Modified-Since", lastModified)

	resp, body := h.send(req)
	if resp.StatusCode != http.StatusOK || string(body) != "two" {
		t.Fatalf("revalidating a stale copy = %d %q, want 200 \"two\"", resp.StatusCode, body)
	}

	etag = resp.Header.Get("ETag")

	req = h.request(http.MethodGet, pathOf(t, first.URL), nil)
	req.Header.Set("If-None-Match", etag)

	if resp, _ = h.send(req); resp.StatusCode != http.StatusNotModified {
		t.Errorf("revalidating a current copy = %d, want 304", resp.StatusCode)
	}

	// Same bytes under a new name is a new response: it now renders as HTML.
	resp, raw = h.send(h.authed(http.MethodPut,
		"/api/artifacts/"+first.ID+"?filename=notes.html", strings.NewReader("two")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rename update: status %d (body %s)", resp.StatusCode, raw)
	}

	req = h.request(http.MethodGet, pathOf(t, first.URL), nil)
	req.Header.Set("If-None-Match", etag)

	resp, _ = h.send(req)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("revalidating after a rename = %d %q, want 200 text/html",
			resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// A pinned version never changes, so ordinary caching is left alone.
	resp, _ = h.get(first.VersionURL)
	if got := resp.Header.Get("Cache-Control"); got != "" {
		t.Errorf("@1 Cache-Control = %q, want none", got)
	}
}

// ---------------------------------------------------------------- range

func TestRangeRequest(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	art := h.upload("clip.mp4", payload, nil)

	req := h.request(http.MethodGet, pathOf(t, art.URL), nil)
	req.Header.Set("Range", "bytes=100-199")

	resp, body := h.send(req)

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}

	if got := resp.Header.Get("Content-Range"); got != "bytes 100-199/4096" {
		t.Errorf("Content-Range = %q, want bytes 100-199/4096", got)
	}

	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q, want bytes", got)
	}

	if !bytes.Equal(body, payload[100:200]) {
		t.Errorf("body = %d bytes, want the 100 requested bytes", len(body))
	}
}

func TestRangeUnsatisfiable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	art := h.upload("clip.mp4", bytes.Repeat([]byte("x"), 100), nil)

	req := h.request(http.MethodGet, pathOf(t, art.URL), nil)
	req.Header.Set("Range", "bytes=5000-6000")

	resp, _ := h.send(req)

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", resp.StatusCode)
	}

	if got := resp.Header.Get("Content-Range"); got != "bytes */100" {
		t.Errorf("Content-Range = %q, want bytes */100", got)
	}
}

// ---------------------------------------------------------------- metadata

func TestInfoIncludesVersionHistory(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	first := h.upload("plan.md", []byte("# v1"), url.Values{"slug": {"my-plan"}})

	if resp, raw := h.send(h.authed(http.MethodPut,
		"/api/artifacts/"+first.ID+"?filename=plan.md", strings.NewReader("# v2 longer"))); resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d (body %s)", resp.StatusCode, raw)
	}

	resp, raw := h.send(h.authed(http.MethodGet, "/api/artifacts/"+first.ID, nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}

	var info struct {
		ID            string  `json:"id"`
		Slug          *string `json:"slug"`
		URL           string  `json:"url"`
		LatestVersion int     `json:"latest_version"`
		Versions      []struct {
			N        int    `json:"n"`
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
			URL      string `json:"url"`
		} `json:"versions"`
	}

	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if info.LatestVersion != 2 || len(info.Versions) != 2 {
		t.Fatalf("latest = %d with %d versions, want 2 and 2", info.LatestVersion, len(info.Versions))
	}

	if info.Versions[0].N != 1 || info.Versions[1].N != 2 {
		t.Errorf("versions are not in order: %d then %d", info.Versions[0].N, info.Versions[1].N)
	}

	if info.Versions[0].Size == info.Versions[1].Size {
		t.Error("both versions report the same size")
	}

	if info.URL != baseURL+"/my-plan" {
		t.Errorf("url = %q, want %s/my-plan", info.URL, baseURL)
	}

	if info.Versions[1].URL != baseURL+"/my-plan@2" {
		t.Errorf("version url = %q, want %s/my-plan@2", info.Versions[1].URL, baseURL)
	}
}

func TestInfoMissing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	resp, raw := h.send(h.authed(http.MethodGet, "/api/artifacts/zzzzzzzz", nil))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", resp.StatusCode, raw)
	}
}

func TestListPagesAndCaps(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	want := make(map[string]bool)

	for i := range 5 {
		art := h.upload("f.txt", bytes.Repeat([]byte("x"), i+1), nil)
		want[art.ID] = true
	}

	seen := make(map[string]bool)
	cursor := ""

	for range 10 {
		target := "/api/artifacts?limit=2"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}

		resp, raw := h.send(h.authed(http.MethodGet, target, nil))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
		}

		var page struct {
			Artifacts  []artifactBody `json:"artifacts"`
			NextCursor *string        `json:"next_cursor"`
		}

		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatalf("decode: %v", err)
		}

		if len(page.Artifacts) > 2 {
			t.Fatalf("page has %d artifacts, want at most 2", len(page.Artifacts))
		}

		for _, art := range page.Artifacts {
			if seen[art.ID] {
				t.Fatalf("artifact %q appeared twice", art.ID)
			}

			seen[art.ID] = true
		}

		if page.NextCursor == nil {
			break
		}

		cursor = *page.NextCursor
	}

	if len(seen) != len(want) {
		t.Fatalf("paged through %d artifacts, want %d", len(seen), len(want))
	}

	for id := range want {
		if !seen[id] {
			t.Errorf("artifact %q never appeared", id)
		}
	}
}

func TestListRejectsBadParameters(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	for _, target := range []string{
		"/api/artifacts?limit=abc",
		"/api/artifacts?limit=0",
		"/api/artifacts?limit=-5",
		"/api/artifacts?cursor=not-a-cursor!!",
	} {
		resp, raw := h.send(h.authed(http.MethodGet, target, nil))
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400 (body %s)", target, resp.StatusCode, raw)
		}
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	resp, raw := h.send(h.request(http.MethodGet, "/healthz", nil))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
	}

	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
}
