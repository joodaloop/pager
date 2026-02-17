package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"golang.org/x/net/html"
)

func csvToTable(data []byte, src string) string {
	reader := csv.NewReader(bytes.NewReader(data))
	records, err := reader.ReadAll()
	if err != nil {
		warn("<include src=%q> failed to parse CSV: %v", src, err)
		return ""
	}
	if len(records) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<table>\n<thead>\n<tr>")
	for _, cell := range records[0] {
		sb.WriteString("<th>")
		sb.WriteString(html.EscapeString(cell))
		sb.WriteString("</th>")
	}
	sb.WriteString("</tr>\n</thead>\n<tbody>\n")
	for _, row := range records[1:] {
		sb.WriteString("<tr>")
		for _, cell := range row {
			sb.WriteString("<td>")
			sb.WriteString(html.EscapeString(cell))
			sb.WriteString("</td>")
		}
		sb.WriteString("</tr>\n")
	}
	sb.WriteString("</tbody>\n</table>")
	return sb.String()
}

func highlightCode(data []byte, src string) string {
	ext := filepath.Ext(src)
	lexer := lexers.Match(src)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	formatter := chromahtml.New(
		chromahtml.WithClasses(true),
		chromahtml.PreventSurroundingPre(false),
	)

	iterator, err := lexer.Tokenise(nil, string(data))
	if err != nil {
		lang := strings.TrimPrefix(ext, ".")
		return fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", lang, html.EscapeString(string(data)))
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, styles.Fallback, iterator); err != nil {
		lang := strings.TrimPrefix(ext, ".")
		return fmt.Sprintf("<pre><code class=\"language-%s\">%s</code></pre>", lang, html.EscapeString(string(data)))
	}
	return buf.String()
}

// syntaxThemeCSS returns the chroma CSS for the given theme name, or "" if not found.
func syntaxThemeCSS(theme string) string {
	style := styles.Get(theme)
	if style == styles.Fallback && theme != "fallback" {
		return ""
	}
	formatter := chromahtml.New(chromahtml.WithClasses(true))
	var buf bytes.Buffer
	if err := formatter.WriteCSS(&buf, style); err != nil {
		return ""
	}
	return buf.String()
}
