// Package content resolves upload filenames and content types.
//
// A client-supplied Content-Type header is never consulted. The only inputs
// are the sanitized filename and, when the extension is unknown, the leading
// bytes of the file itself.
package content

import (
	"errors"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"
)

// MaxFilenameBytes caps a stored filename. Filenames never reach the
// filesystem, so this limit only keeps response headers sane.
const MaxFilenameBytes = 255

// SniffLen is how many leading bytes http.DetectContentType inspects.
const SniffLen = 512

// The three content types the code refers to by name.
const (
	TypeHTML   = "text/html; charset=utf-8"
	TypeText   = "text/plain; charset=utf-8"
	TypeBinary = "application/octet-stream"
)

// ErrBadFilename is returned by SanitizeFilename for a name that cannot be
// reduced to something storable.
var ErrBadFilename = errors.New("invalid filename")

// byExtension is the allowlist consulted before sniffing. Markdown resolves to
// HTML because it is rendered at request time; /raw overrides that per-request.
var byExtension = map[string]string{
	".md":       TypeHTML,
	".markdown": TypeHTML,

	".html": TypeHTML,
	".htm":  TypeHTML,

	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".svg":  "image/svg+xml",

	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",

	".mp3": "audio/mpeg",
	".wav": "audio/wav",
	".m4a": "audio/mp4",
	".caf": "audio/x-caf",

	".pdf": "application/pdf",

	".txt":  TypeText,
	".log":  TypeText,
	".json": TypeText,
	".yaml": TypeText,
	".yml":  TypeText,
	".toml": TypeText,
	".csv":  TypeText,
	".xml":  TypeText,
	".go":   TypeText,
	".py":   TypeText,
	".ts":   TypeText,
	".js":   TypeText,
	".sh":   TypeText,
	".sql":  TypeText,
	".rs":   TypeText,
	".c":    TypeText,
	".h":    TypeText,
}

// Resolve picks a content type for a sanitized filename: extension allowlist,
// then a sniff of head, then application/octet-stream as the floor.
func Resolve(filename string, head []byte) string {
	if t, ok := byExtension[ext(filename)]; ok {
		return t
	}

	if len(head) > 0 {
		if t := http.DetectContentType(head); t != "" {
			return t
		}
	}

	return TypeBinary
}

// IsMarkdown reports whether a filename should be rendered rather than served
// byte-for-byte.
func IsMarkdown(filename string) bool {
	switch ext(filename) {
	case ".md", ".markdown":
		return true
	}

	return false
}

// Disposition returns "inline" or "attachment" for a resolved content type.
// Only the octet-stream floor downloads; everything on the allowlist, and
// anything the sniffer recognized, is previewable.
func Disposition(contentType string) string {
	if mediaType(contentType) == TypeBinary {
		return "attachment"
	}

	return "inline"
}

// ContentDisposition builds an RFC 6266 header value. Non-ASCII names get an
// ASCII fallback plus a filename* parameter; a crafted name cannot escape the
// quoted string or inject a header.
func ContentDisposition(disposition, filename string) string {
	fallback := asciiFallback(filename)

	value := disposition + `; filename="` + fallback + `"`
	if fallback != filename {
		value += `; filename*=UTF-8''` + encodeRFC5987(filename)
	}

	return value
}

// SanitizeFilename reduces a client-supplied name to a bare, storable filename.
// Path separators, directory traversal, and control characters are removed
// rather than escaped, and the result is capped at MaxFilenameBytes.
func SanitizeFilename(name string) (string, error) {
	// Handle Windows separators too: path.Base only splits on '/'.
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(name)

	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}

		return r
	}, name)

	name = strings.TrimSpace(name)
	name = truncateUTF8(name, MaxFilenameBytes)
	name = strings.TrimSpace(name)

	switch name {
	case "", ".", "..", "/":
		return "", ErrBadFilename
	}

	return name, nil
}

func ext(filename string) string {
	return strings.ToLower(path.Ext(filename))
}

func mediaType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}

	return strings.ToLower(strings.TrimSpace(contentType))
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}

// asciiFallback replaces every byte that cannot appear unescaped inside a
// quoted-string, including all non-ASCII, with an underscore.
func asciiFallback(s string) string {
	var b strings.Builder

	for i := range len(s) {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			b.WriteByte('_')

			continue
		}

		b.WriteByte(c)
	}

	return b.String()
}

// encodeRFC5987 percent-encodes everything outside attr-char.
func encodeRFC5987(s string) string {
	const hex = "0123456789ABCDEF"

	var b strings.Builder

	for i := range len(s) {
		c := s[i]
		if isAttrChar(c) {
			b.WriteByte(c)

			continue
		}

		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}

	return b.String()
}

func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}

	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
