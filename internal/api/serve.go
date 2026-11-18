package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/10/artifact/internal/content"
	"github.com/10/artifact/internal/render"
	"github.com/10/artifact/internal/store"
)

// rawSegment is the reserved trailing path element that suppresses rendering.
const rawSegment = "raw"

// handleServe answers every public artifact route: /{ref}, /{ref}/{filename},
// /{ref}/raw, and the @n variants of all three.
func (s *server) handleServe(w http.ResponseWriter, r *http.Request) {
	reference, n, ok := splitRef(r.PathValue("ref"))
	if !ok {
		notFound(w)

		return
	}

	art, err := s.store.Get(r.Context(), reference)
	if err != nil {
		s.serveFailure(w, r, err)

		return
	}

	noteArtifact(r, art.ID)

	if n == 0 {
		n = art.LatestVersion

		// Without this a browser guesses freshness from Last-Modified and can
		// keep showing the old version for hours after an update.
		w.Header().Set("Cache-Control", "no-cache")
	}

	version, err := s.store.Version(r.Context(), art.ID, n)
	if err != nil {
		s.serveFailure(w, r, err)

		return
	}

	name := r.PathValue("name")
	raw := name == rawSegment

	// The filename form exists so a URL can end in .png or .html. It is not a
	// second name for the artifact, so anything else is simply not a URL here.
	if name != "" && !raw && name != version.Filename {
		notFound(w)

		return
	}

	s.writeArtifact(w, r, art.ID, *version, raw)
}

func (s *server) writeArtifact(w http.ResponseWriter, r *http.Request, id string, v store.Version, raw bool) {
	f, err := os.Open(s.store.BlobPath(id, v.N))
	if err != nil {
		s.log.Error("open blob", "error", err, "artifact_id", id, "version", v.N)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}
	defer f.Close()

	w.Header().Set("X-Content-Type-Options", "nosniff")

	// Public to whoever holds the link, not to whoever searches: a link that
	// gets indexed undoes the obstacle random IDs put in front of enumeration.
	w.Header().Set("X-Robots-Tag", "noindex")

	markdown := content.IsMarkdown(v.Filename)
	if markdown && !raw {
		s.writeMarkdown(w, v, f)

		return
	}

	contentType := v.ContentType
	if markdown {
		contentType = content.TypeText
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", content.ContentDisposition(content.Disposition(contentType), v.Filename))

	// Last-Modified has one-second resolution, so it cannot tell apart two
	// versions published in the same second. The digest alone is not enough
	// either: an update can keep the bytes and change the filename, and with
	// it the content type. A version never changes, so its number does it.
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(v.N)+"-"+v.SHA256))

	// ServeContent supplies Range, 206, 416, If-Range, and Last-Modified from
	// the standard library. Reimplementing that is what breaks video seeking.
	http.ServeContent(w, r, "", v.CreatedAt, f)
}

func (s *server) writeMarkdown(w http.ResponseWriter, v store.Version, f *os.File) {
	src, err := io.ReadAll(f)
	if err != nil {
		s.log.Error("read markdown blob", "error", err, "version", v.N)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	var page bytes.Buffer

	if err := render.Markdown(&page, v.Filename, src); err != nil {
		s.log.Error("render markdown", "error", err, "version", v.N)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", content.TypeHTML)
	w.Header().Set("Content-Disposition", content.ContentDisposition("inline", v.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(page.Len()))

	// A write failure here means the client is gone; the headers are already
	// sent, so there is nothing left to say on this connection.
	_, _ = w.Write(page.Bytes())
}

// splitRef separates "{id}@{n}" into its parts. A bare reference reports
// version 0, meaning "latest".
func splitRef(raw string) (string, int, bool) {
	base, version, found := strings.Cut(raw, "@")
	if !found {
		return raw, 0, raw != ""
	}

	n, err := strconv.Atoi(version)
	if err != nil || n < 1 || base == "" {
		return "", 0, false
	}

	return base, n, true
}

func (s *server) serveFailure(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)

		return
	}

	s.log.Error("serve artifact", "error", err, "path", r.URL.Path)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// notFound answers the public routes in plain text. The JSON error envelope is
// for /api; a browser is the audience here.
func notFound(w http.ResponseWriter) {
	http.Error(w, "404 artifact not found", http.StatusNotFound)
}
