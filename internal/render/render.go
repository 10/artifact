// Package render turns Markdown into a standalone HTML page.
//
// Rendering happens at request time, not at upload time, so an update is
// visible immediately and a template change applies to every artifact that
// already exists.
package render

import (
	"bufio"
	"bytes"
	"embed"
	"fmt"
	stdhtml "html"
	"html/template"
	"io"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

//go:embed page.html
var files embed.FS

var page = template.Must(template.ParseFS(files, "page.html"))

// Raw HTML in Markdown is passed through. Artifact serves arbitrary .html
// files verbatim anyway, so sanitizing here would be inconsistent theatre
// rather than a security boundary; origin separation is the actual control.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

type pageData struct {
	Title string
	Body  template.HTML
}

// Markdown renders src and wraps it in the embedded page template. The page
// is titled by the document's first top-level h1, or by filename without one.
func Markdown(w io.Writer, filename string, src []byte) error {
	doc := markdown.Parser().Parse(text.NewReader(src))

	var body bytes.Buffer

	if err := markdown.Renderer().Render(&body, src, doc); err != nil {
		return fmt.Errorf("convert markdown: %w", err)
	}

	title := headingTitle(doc, src)
	if title == "" {
		title = filename
	}

	if err := page.Execute(w, pageData{Title: title, Body: template.HTML(body.String())}); err != nil {
		return fmt.Errorf("execute page template: %w", err)
	}

	return nil
}

// headingTitle returns the plain text of the first non-empty h1 directly
// under the document, or "" when there is none. An h1 nested in a list or
// quote is content, not the document's name.
func headingTitle(doc ast.Node, src []byte) string {
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		if h, ok := n.(*ast.Heading); ok && h.Level == 1 {
			if title := plainText(h, src); title != "" {
				return title
			}
		}
	}

	return ""
}

func plainText(n ast.Node, src []byte) string {
	var buf bytes.Buffer

	w := bufio.NewWriter(&buf)

	_ = ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		// The renderer's own writer, so backslash escapes and entities read
		// in the title exactly as they do in the visible heading.
		switch n := n.(type) {
		case *ast.Text:
			if n.IsRaw() {
				html.DefaultWriter.RawWrite(w, n.Value(src))
			} else {
				html.DefaultWriter.Write(w, n.Value(src))
			}

			if n.SoftLineBreak() || n.HardLineBreak() {
				_ = w.WriteByte(' ')
			}
		case *ast.String:
			html.DefaultWriter.RawWrite(w, n.Value)
		case *ast.AutoLink:
			html.DefaultWriter.RawWrite(w, n.Label(src))
		case *ast.RawHTML:
			// Other inline tags only style their text; a <br> breaks the line.
			if n.Segments.Len() > 0 {
				tag := n.Segments.At(0)
				if isBreak(tag.Value(src)) {
					_ = w.WriteByte(' ')
				}
			}
		}

		return ast.WalkContinue, nil
	})

	_ = w.Flush()

	return strings.Join(strings.Fields(stdhtml.UnescapeString(buf.String())), " ")
}

// isBreak reports whether a raw inline tag is <br>, not merely a tag whose
// name starts with "br".
func isBreak(tag []byte) bool {
	rest, ok := bytes.CutPrefix(bytes.ToLower(tag), []byte("<br"))

	return ok && len(rest) > 0 && bytes.IndexByte([]byte(">/ \t\r\n"), rest[0]) >= 0
}
