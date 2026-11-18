// Package render turns Markdown into a standalone HTML page.
//
// Rendering happens at request time, not at upload time, so an update is
// visible immediately and a template change applies to every artifact that
// already exists.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
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
	var b strings.Builder

	_ = ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch n := n.(type) {
		case *ast.Text:
			value := n.Value(src)
			if !n.IsRaw() {
				value = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(value)))
			}

			b.Write(value)

			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(n.Value)
		case *ast.AutoLink:
			b.Write(n.Label(src))
		}

		return ast.WalkContinue, nil
	})

	return strings.TrimSpace(b.String())
}
