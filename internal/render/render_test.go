package render_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/10/artifact/internal/render"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// TestMarkdownGolden pins the full rendered page, template included, so a
// template change is a visible diff rather than a silent one.
func TestMarkdownGolden(t *testing.T) {
	sources, err := filepath.Glob(filepath.Join("testdata", "*.md"))
	if err != nil {
		t.Fatalf("glob testdata: %v", err)
	}

	if len(sources) == 0 {
		t.Fatal("no testdata/*.md files found")
	}

	for _, source := range sources {
		name := strings.TrimSuffix(filepath.Base(source), ".md")

		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(source)
			if err != nil {
				t.Fatalf("read %s: %v", source, err)
			}

			var got bytes.Buffer
			if err := render.Markdown(&got, filepath.Base(source), src); err != nil {
				t.Fatalf("Markdown: %v", err)
			}

			golden := filepath.Join("testdata", name+".golden.html")

			if *update {
				if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}

				return
			}

			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run: go test ./internal/render -update): %v", err)
			}

			if !bytes.Equal(got.Bytes(), want) {
				t.Errorf("rendered output differs from %s\n--- got ---\n%s", golden, got.String())
			}
		})
	}
}

func TestMarkdownRendersGFM(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile(filepath.Join("testdata", "gfm.md"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}

	var out bytes.Buffer
	if err := render.Markdown(&out, "gfm.md", src); err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	page := out.String()

	required := map[string]string{
		"table":         "<table>",
		"table header":  "<th",
		"task list":     `<input checked="" disabled="" type="checkbox">`,
		"strikethrough": "<del>",
		"autolink":      `<a href="https://example.com/docs">`,
		"code block":    `<pre><code class="language-go">`,
		"raw html":      `<div class="callout">`,
		"blockquote":    "<blockquote>",
	}

	for what, fragment := range required {
		if !strings.Contains(page, fragment) {
			t.Errorf("%s missing: %q not found in output", what, fragment)
		}
	}

	// Code inside a fence stays text, not markup.
	if !strings.Contains(page, "hello &amp; &lt;world&gt;") {
		t.Error("fenced code block was not escaped")
	}
}

func TestMarkdownPageIsSelfContained(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := render.Markdown(&out, "plan.md", []byte("# Title\n")); err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	page := out.String()

	for _, forbidden := range []string{"<script", "cdn.", "googleapis", "http://", "<link "} {
		if strings.Contains(page, forbidden) {
			t.Errorf("page template makes an external request or runs script: found %q", forbidden)
		}
	}

	for _, required := range []string{"<!doctype html>", "prefers-color-scheme", "<title>Title</title>"} {
		if !strings.Contains(page, required) {
			t.Errorf("page template missing %q", required)
		}
	}
}

// The title is what a browser tab, history, and a chat app's link preview
// show, so it should name the document rather than the file it came from.
func TestMarkdownTitle(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		src  string
		want string
	}{
		"first h1":              {"intro\n\n# Release plan\n\n# Later\n", "Release plan"},
		"inline markup dropped": {"# Ship **v2** with `ETag` & [docs](x)\n", "Ship v2 with ETag &amp; docs"},
		"setext h1":             {"Release\nplan\n===\n", "Release plan"},
		"hard line break":       {"Release  \nplan\n===\n", "Release plan"},
		"inline br":             {"# Release<br>plan, <b>bold</b>ly\n", "Release plan, boldly"},
		"br-like tag":           {"# pre<brand-name>fix</brand-name> <BR/>end\n", "prefix end"},
		"escape kept literal":   {"# Q\\&copy;A &copy;\n", "Q&amp;copy;A ©"},
		"empty h1 skipped":      {"#\n\n# Second\n", "Second"},
		"no h1 uses filename":   {"**daily finds**\n\n## Section\n", "notes.md"},
		"nested h1 ignored":     {"> # Quoted\n", "notes.md"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			if err := render.Markdown(&out, "notes.md", []byte(tc.src)); err != nil {
				t.Fatalf("Markdown: %v", err)
			}

			if want := "<title>" + tc.want + "</title>"; !strings.Contains(out.String(), want) {
				t.Errorf("page does not contain %q", want)
			}
		})
	}
}

func TestMarkdownTitleIsEscaped(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := render.Markdown(&out, `<script>alert(1)</script>.md`, []byte("hi")); err != nil {
		t.Fatalf("Markdown: %v", err)
	}

	if strings.Contains(out.String(), "<script>alert(1)</script>.md</title>") {
		t.Error("filename was interpolated into <title> unescaped")
	}
}
