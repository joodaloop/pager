package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		if unicode.IsSpace(r) || r == '-' || r == '_' {
			return '-'
		}
		return -1
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

func textContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

type processState struct {
	dir      string
	headings []heading
	ids      map[string]bool
	links    []string
}

func (s *processState) uniqueID(id string) string {
	if !s.ids[id] {
		s.ids[id] = true
		return id
	}
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s-%d", id, i)
		if !s.ids[candidate] {
			s.ids[candidate] = true
			return candidate
		}
	}
}

func processNode(n *html.Node, s *processState) {
	if n.Type == html.ElementNode {
		// Auto-ID headings based on text content
		if len(n.Data) == 2 && n.Data[0] == 'h' && n.Data[1] >= '1' && n.Data[1] <= '6' {
			text := textContent(n)
			level := int(n.Data[1] - '0')
			if !hasAttr(n, "id") {
				slug := slugify(text)
				if slug != "" {
					slug = s.uniqueID(slug)
					n.Attr = append(n.Attr, html.Attribute{Key: "id", Val: slug})
					s.headings = append(s.headings, heading{Level: level, ID: slug, Text: strings.TrimSpace(text)})
				}
			} else {
				id := s.uniqueID(getAttr(n, "id"))
				setAttr(n, "id", id)
				s.headings = append(s.headings, heading{Level: level, ID: id, Text: strings.TrimSpace(text)})
			}
		}

		// Collect IDs from any element
		if n.Data != "h1" && n.Data != "h2" && n.Data != "h3" && n.Data != "h4" && n.Data != "h5" && n.Data != "h6" {
			if hasAttr(n, "id") {
				id := s.uniqueID(getAttr(n, "id"))
				setAttr(n, "id", id)
			}
		}

		// Add aspect-ratio to images and warn on missing alt
		if n.Data == "img" {
			if !hasAttr(n, "alt") {
				src := getAttr(n, "src")
				warn("<img src=%q> missing alt text", src)
			}
			src := getAttr(n, "src")
			if src != "" && !strings.HasPrefix(src, "http") {
				imgPath := filepath.Join(s.dir, src)
				if f, err := os.Open(imgPath); err == nil {
					if cfg, _, err := image.DecodeConfig(f); err == nil {
						style := fmt.Sprintf("aspect-ratio: %d / %d", cfg.Width, cfg.Height)
						found := false
						for i, a := range n.Attr {
							if a.Key == "style" {
								n.Attr[i].Val = a.Val + "; " + style
								found = true
								break
							}
						}
						if !found {
							n.Attr = append(n.Attr, html.Attribute{Key: "style", Val: style})
						}
					}
					f.Close()
				}
			}
		}

		// Warn on empty or missing-file src/poster attributes
		for _, attr := range []string{"src", "poster"} {
			if hasAttr(n, attr) {
				val := getAttr(n, attr)
				if val == "" {
					warn("<%s> has empty %s attribute", n.Data, attr)
				} else if !strings.HasPrefix(val, "http") && !strings.HasPrefix(val, "data:") && !strings.HasPrefix(val, "//") {
					path := filepath.Join(s.dir, val)
					if _, err := os.Stat(path); err != nil {
						warn("<%s %s=%q> references missing file", n.Data, attr, val)
					}
				}
			}
		}

		// External links: add target="_blank" and rel="noopener"
		// Local links: collect for validation
		if n.Data == "a" {
			href := getAttr(n, "href")
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
				if !hasAttr(n, "target") {
					n.Attr = append(n.Attr, html.Attribute{Key: "target", Val: "_blank"})
				}
				if !hasAttr(n, "rel") {
					n.Attr = append(n.Attr, html.Attribute{Key: "rel", Val: "noopener"})
				}
			} else if href == "" {
				warn("<a> has empty href attribute")
			} else {
				s.links = append(s.links, href)
			}
			// Warn on icon-only links missing aria-label
			text := strings.TrimSpace(textContent(n))
			if text == "" && !hasAttr(n, "aria-label") {
				warn("<a href=%q> has no text and no aria-label", href)
			}
		}
	}

	for c := n.FirstChild; c != nil; c = c.NextSibling {
		processNode(c, s)
	}
}

func buildTOC(headings []heading) string {
	if len(headings) == 0 {
		return ""
	}
	var sb strings.Builder
	// Find the minimum level to use as base depth
	minLevel := headings[0].Level
	for _, h := range headings[1:] {
		if h.Level < minLevel {
			minLevel = h.Level
		}
	}
	depth := 0
	indent := func() string { return strings.Repeat("  ", depth) }

	for i, h := range headings {
		level := h.Level - minLevel + 1
		if i == 0 {
			sb.WriteString("<ul>\n")
			depth++
		} else if level > depth {
			// Open nested lists for each level increase
			for depth < level {
				sb.WriteString(indent() + "<ul>\n")
				depth++
			}
		} else if level < depth {
			// Close lists and parent <li> for each level decrease
			for depth > level {
				depth--
				sb.WriteString(indent() + "</ul>\n")
				depth--
				sb.WriteString(indent() + "</li>\n")
				depth++
			}
		}
		// Write the <li> — leave it open in case children follow
		sb.WriteString(fmt.Sprintf("%s<li><a href=\"#%s\">%s</a>", indent(), h.ID, h.Text))
		// Check if the next heading is deeper (has children)
		hasChildren := i+1 < len(headings) && headings[i+1].Level-minLevel+1 > level
		if hasChildren {
			sb.WriteString("\n")
		} else {
			sb.WriteString("</li>\n")
		}
	}
	// Close all remaining open lists
	for depth > 1 {
		depth--
		sb.WriteString(indent() + "</ul>\n")
		depth--
		sb.WriteString(indent() + "</li>\n")
		depth++
	}
	sb.WriteString("</ul>")
	return sb.String()
}

func processContent(content string, dir string) string {
	// Expand <convert src="..."> tags: .md → HTML, .csv → table
	convertRe := regexp.MustCompile(`<convert\s+src="([^"]*)"\s*/?>(?:</convert>)?`)
	content = convertRe.ReplaceAllStringFunc(content, func(match string) string {
		m := convertRe.FindStringSubmatch(match)
		src := m[1]
		if src == "" {
			warn("<convert> has empty src attribute")
			return ""
		}
		filePath := filepath.Join(dir, src)
		data, err := os.ReadFile(filePath)
		if err != nil {
			warn("<convert src=%q> references missing file", src)
			return ""
		}
		ext := strings.ToLower(filepath.Ext(src))
		switch ext {
		case ".md":
			var buf bytes.Buffer
			if err := goldmark.Convert(data, &buf); err != nil {
				warn("<convert src=%q> failed to convert markdown: %v", src, err)
				return ""
			}
			return buf.String()
		case ".csv":
			return csvToTable(data, src)
		default:
			warn("<convert src=%q> unsupported extension %q (use .md or .csv)", src, ext)
			return ""
		}
	})

	// Expand <syntax src="..."> tags: syntax-highlighted code block
	syntaxRe := regexp.MustCompile(`<syntax\s+src="([^"]*)"\s*/?>(?:</syntax>)?`)
	content = syntaxRe.ReplaceAllStringFunc(content, func(match string) string {
		m := syntaxRe.FindStringSubmatch(match)
		src := m[1]
		if src == "" {
			warn("<syntax> has empty src attribute")
			return ""
		}
		filePath := filepath.Join(dir, src)
		data, err := os.ReadFile(filePath)
		if err != nil {
			warn("<syntax src=%q> references missing file", src)
			return ""
		}
		return highlightCode(data, src)
	})

	// Replace <toc /> and <toc></toc> with a placeholder before parsing
	hasTOC := false
	const tocPlaceholder = "<!--TOC_PLACEHOLDER-->"
	tocRe := regexp.MustCompile(`<toc\s*/>|<toc>\s*</toc>`)
	if tocRe.MatchString(content) {
		content = tocRe.ReplaceAllString(content, tocPlaceholder)
		hasTOC = true
	}

	context := &html.Node{
		Type:     html.ElementNode,
		Data:     "body",
		DataAtom: atom.Body,
	}
	nodes, err := html.ParseFragment(strings.NewReader(content), context)
	if err != nil {
		return content
	}
	s := &processState{dir: dir, ids: make(map[string]bool)}
	for _, n := range nodes {
		processNode(n, s)
	}

	// Validate local links
	for _, link := range s.links {
		if strings.HasPrefix(link, "#") {
			id := link[1:]
			if !s.ids[id] {
				warn("<a href=%q> references missing id", link)
			}
		} else if !strings.HasPrefix(link, "http") && !strings.HasPrefix(link, "mailto:") && !strings.HasPrefix(link, "tel:") {
			path := filepath.Join(dir, link)
			if _, err := os.Stat(path); err != nil {
				warn("<a href=%q> references missing file", link)
			}
		}
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		html.Render(&buf, n)
	}
	result := buf.String()

	if hasTOC {
		result = strings.ReplaceAll(result, tocPlaceholder, buildTOC(s.headings))
	}

	return result
}
