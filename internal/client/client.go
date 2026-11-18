// Package client is a thin HTTP client over the Artifact API, used by the CLI.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// maxErrorBody caps how much of an error response is read before giving up on
// finding a JSON envelope in it.
const maxErrorBody = 64 << 10

// Client talks to one Artifact server with one token.
type Client struct {
	Endpoint string
	Token    string
	HTTP     *http.Client
}

// New returns a client for endpoint. A trailing slash is tolerated.
func New(endpoint, token string) *Client {
	return &Client{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Token:    token,
		HTTP:     &http.Client{Timeout: 10 * time.Minute},
	}
}

// APIError is a non-2xx response. Callers branch on Status for exit codes and
// on Code for behavior.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("server returned %d %s", e.Status, http.StatusText(e.Status))
	}

	return e.Message
}

// Artifact is the response body shared by upload, update, and list entries.
type Artifact struct {
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

// List is the GET /api/artifacts response.
type List struct {
	Artifacts  []Artifact `json:"artifacts"`
	NextCursor *string    `json:"next_cursor"`
}

// Version is one entry of an artifact's history.
type Version struct {
	N           int    `json:"n"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	CreatedAt   string `json:"created_at"`
	URL         string `json:"url"`
}

// Info is the GET /api/artifacts/{id} response.
type Info struct {
	ID            string    `json:"id"`
	Slug          *string   `json:"slug"`
	URL           string    `json:"url"`
	LatestVersion int       `json:"latest_version"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`
	Versions      []Version `json:"versions"`
}

// Upload publishes path as a new artifact. Both the parsed response and the
// raw response bytes are returned, so --json can echo the server verbatim.
func (c *Client) Upload(ctx context.Context, path, filename, slug string) (*Artifact, []byte, error) {
	query := url.Values{"filename": {filename}}
	if slug != "" {
		query.Set("slug", slug)
	}

	return sendFile[Artifact](ctx, c, http.MethodPost, "/api/upload?"+query.Encode(), path)
}

// Update replaces an artifact's contents, creating the next version.
func (c *Client) Update(ctx context.Context, id, path, filename string) (*Artifact, []byte, error) {
	query := url.Values{"filename": {filename}}
	target := "/api/artifacts/" + url.PathEscape(id) + "?" + query.Encode()

	return sendFile[Artifact](ctx, c, http.MethodPut, target, path)
}

// List returns recent artifacts, newest-updated first.
func (c *Client) List(ctx context.Context, limit int, cursor string) (*List, []byte, error) {
	query := url.Values{}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}

	if cursor != "" {
		query.Set("cursor", cursor)
	}

	target := "/api/artifacts"
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := c.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}

	return decode[List](c.do(req))
}

// Info returns metadata and the full version history for one artifact.
func (c *Client) Info(ctx context.Context, id string) (*Info, []byte, error) {
	req, err := c.request(ctx, http.MethodGet, "/api/artifacts/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, nil, err
	}

	return decode[Info](c.do(req))
}

// Delete removes an artifact, all its versions, and all its blobs.
func (c *Client) Delete(ctx context.Context, id string) error {
	req, err := c.request(ctx, http.MethodDelete, "/api/artifacts/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}

	_, err = c.do(req)

	return err
}

// Stdin is the path that names standard input rather than a file.
const Stdin = "-"

// sendFile streams a local file as a raw request body, the form the API
// prefers: no multipart framing, no buffering.
func sendFile[T any](ctx context.Context, c *Client, method, target, path string) (*T, []byte, error) {
	if path == Stdin {
		spool, cleanup, err := spoolStdin()
		if err != nil {
			return nil, nil, err
		}
		defer cleanup()

		info, err := spool.Stat()
		if err != nil {
			return nil, nil, fmt.Errorf("spool stdin: %w", err)
		}

		return sendBody[T](ctx, c, method, target, spool, info.Size())
	}

	// The *PathError from os already names the operation and the path.
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}

	if info.IsDir() {
		return nil, nil, fmt.Errorf("%s is a directory", path)
	}

	return sendBody[T](ctx, c, method, target, f, info.Size())
}

// spoolStdin copies standard input to a temporary file and returns it
// rewound. Sending input as it arrives would put the producing command's
// whole run under the client's timeout, cutting off a long build along with
// its log, and holding it in memory would cost several times its size.
//
// The file is unlinked before any input reaches it, so even an interrupted
// run leaves no copy of the log behind. Windows cannot unlink an open file;
// there, cleanup removes it after closing.
func spoolStdin() (*os.File, func(), error) {
	f, err := os.CreateTemp("", "artifact-stdin-*")
	if err != nil {
		return nil, nil, fmt.Errorf("spool stdin: %w", err)
	}

	// Anywhere else, a spool that cannot be unlinked is refused before any
	// input reaches it.
	windows := runtime.GOOS == "windows"
	if !windows {
		if err := os.Remove(f.Name()); err != nil {
			f.Close()

			return nil, nil, fmt.Errorf("spool stdin: %w", err)
		}
	}

	cleanup := func() {
		f.Close()

		if windows {
			os.Remove(f.Name())
		}
	}

	_, err = io.Copy(f, os.Stdin)
	if err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}

	if err != nil {
		cleanup()

		return nil, nil, fmt.Errorf("spool stdin: %w", err)
	}

	return f, cleanup, nil
}

func sendBody[T any](ctx context.Context, c *Client, method, target string, body io.Reader, size int64) (*T, []byte, error) {
	req, err := c.request(ctx, method, target, body)
	if err != nil {
		return nil, nil, err
	}

	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	return decode[T](c.do(req))
}

func (c *Client) request(ctx context.Context, method, target string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+target, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)

	return req, nil
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Redacted(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))

		return nil, apiError(resp.StatusCode, body)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return body, nil
}

func decode[T any](body []byte, err error) (*T, []byte, error) {
	if err != nil {
		return nil, nil, err
	}

	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}

	return &value, body, nil
}

func apiError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error.Code == "" {
		return &APIError{Status: status}
	}

	return &APIError{Status: status, Code: envelope.Error.Code, Message: envelope.Error.Message}
}
