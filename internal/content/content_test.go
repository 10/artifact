package content_test

import (
	"strings"
	"testing"

	"github.com/10/artifact/internal/content"
)

func TestResolveByExtension(t *testing.T) {
	t.Parallel()

	cases := []struct {
		filename string
		want     string
	}{
		{"plan.md", content.TypeHTML},
		{"plan.markdown", content.TypeHTML},
		{"report.html", content.TypeHTML},
		{"report.HTM", content.TypeHTML},
		{"shot.png", "image/png"},
		{"shot.jpg", "image/jpeg"},
		{"shot.jpeg", "image/jpeg"},
		{"anim.gif", "image/gif"},
		{"shot.webp", "image/webp"},
		{"shot.avif", "image/avif"},
		{"icon.svg", "image/svg+xml"},
		{"clip.mp4", "video/mp4"},
		{"clip.webm", "video/webm"},
		{"clip.mov", "video/quicktime"},
		{"tune.mp3", "audio/mpeg"},
		{"tune.wav", "audio/wav"},
		{"tune.m4a", "audio/mp4"},
		{"memo.caf", "audio/x-caf"},
		{"paper.pdf", "application/pdf"},
		{"notes.txt", content.TypeText},
		{"build.log", content.TypeText},
		{"data.json", content.TypeText},
		{"conf.yaml", content.TypeText},
		{"conf.yml", content.TypeText},
		{"conf.toml", content.TypeText},
		{"rows.csv", content.TypeText},
		{"doc.xml", content.TypeText},
		{"main.go", content.TypeText},
		{"main.py", content.TypeText},
		{"main.ts", content.TypeText},
		{"main.js", content.TypeText},
		{"run.sh", content.TypeText},
		{"query.sql", content.TypeText},
		{"main.rs", content.TypeText},
		{"main.c", content.TypeText},
		{"main.h", content.TypeText},
	}

	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			t.Parallel()

			// A body that would sniff differently proves the extension wins.
			if got := content.Resolve(tc.filename, []byte("<html>hello")); got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.filename, got, tc.want)
			}
		})
	}
}

func TestResolveFallsBackToSniffing(t *testing.T) {
	t.Parallel()

	if got := content.Resolve("mystery", []byte("plain text here")); got != content.TypeText {
		t.Errorf("text sniff = %q, want %q", got, content.TypeText)
	}

	if got := content.Resolve("mystery.unknown", []byte{0x00, 0x01, 0x02, 0x03}); got != content.TypeBinary {
		t.Errorf("binary sniff = %q, want %q", got, content.TypeBinary)
	}

	if got := content.Resolve("mystery", nil); got != content.TypeBinary {
		t.Errorf("empty head = %q, want %q", got, content.TypeBinary)
	}
}

func TestDisposition(t *testing.T) {
	t.Parallel()

	if got := content.Disposition(content.TypeBinary); got != "attachment" {
		t.Errorf("octet-stream disposition = %q, want attachment", got)
	}

	for _, ct := range []string{content.TypeHTML, content.TypeText, "image/png", "video/mp4", "application/pdf"} {
		if got := content.Disposition(ct); got != "inline" {
			t.Errorf("Disposition(%q) = %q, want inline", ct, got)
		}
	}
}

func TestIsMarkdown(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a.md", "a.MD", "a.markdown"} {
		if !content.IsMarkdown(name) {
			t.Errorf("IsMarkdown(%q) = false, want true", name)
		}
	}

	for _, name := range []string{"a.html", "a.txt", "md", "a.mdx"} {
		if content.IsMarkdown(name) {
			t.Errorf("IsMarkdown(%q) = true, want false", name)
		}
	}
}

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain", "report.html", "report.html"},
		{"spaces", "my report.html", "my report.html"},
		{"unicode", "café ☕.md", "café ☕.md"},
		{"unix traversal", "../../etc/passwd", "passwd"},
		{"absolute path", "/etc/shadow", "shadow"},
		{"windows path", `C:\Windows\System32\evil.dll`, "evil.dll"},
		{"windows traversal", `..\..\evil.exe`, "evil.exe"},
		{"control characters", "re\x00po\x1frt\x7f.html", "report.html"},
		{"newline injection", "a.txt\r\nX-Evil: 1", "a.txtX-Evil: 1"},
		{"dotfile is kept", ".env", ".env"},
		{"trailing slash", "dir/", "dir"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := content.SanitizeFilename(tc.input)
			if err != nil {
				t.Fatalf("SanitizeFilename(%q) returned error: %v", tc.input, err)
			}

			if got != tc.want {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestSanitizeFilenameRejects(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"", ".", "..", "/", "   ", "\x00\x01", "../..", "./"} {
		if got, err := content.SanitizeFilename(input); err == nil {
			t.Errorf("SanitizeFilename(%q) = %q, want an error", input, got)
		}
	}
}

func TestSanitizeFilenameCapsLength(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 300) + ".txt"

	got, err := content.SanitizeFilename(long)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != content.MaxFilenameBytes {
		t.Errorf("length = %d, want %d", len(got), content.MaxFilenameBytes)
	}

	// Truncation must not split a multi-byte rune.
	multibyte := strings.Repeat("é", 200)

	got, err = content.SanitizeFilename(multibyte)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) > content.MaxFilenameBytes {
		t.Errorf("length = %d, want <= %d", len(got), content.MaxFilenameBytes)
	}

	if strings.ContainsRune(got, '\uFFFD') {
		t.Errorf("truncation split a rune: %q", got)
	}
}

func TestContentDisposition(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		disposition string
		filename    string
		want        string
	}{
		{
			name:        "ascii",
			disposition: "inline",
			filename:    "report.html",
			want:        `inline; filename="report.html"`,
		},
		{
			name:        "attachment",
			disposition: "attachment",
			filename:    "data.bin",
			want:        `attachment; filename="data.bin"`,
		},
		{
			// The fallback is lossy here, so the true name is still carried
			// by the extended form.
			name:        "quotes are neutralized",
			disposition: "inline",
			filename:    `a"b\c.txt`,
			want:        `inline; filename="a_b_c.txt"; filename*=UTF-8''a%22b%5Cc.txt`,
		},
		{
			name:        "non-ascii gets both forms",
			disposition: "inline",
			filename:    "café.md",
			want:        `inline; filename="caf__.md"; filename*=UTF-8''caf%C3%A9.md`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := content.ContentDisposition(tc.disposition, tc.filename)
			if got != tc.want {
				t.Errorf("ContentDisposition(%q, %q) =\n  %q\nwant\n  %q",
					tc.disposition, tc.filename, got, tc.want)
			}

			if strings.ContainsAny(got, "\r\n") {
				t.Errorf("header value contains a line break: %q", got)
			}
		})
	}
}
