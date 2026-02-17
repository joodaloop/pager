package main

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"html/template"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/fsnotify/fsnotify"
	"github.com/yuin/goldmark"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"gopkg.in/yaml.v3"
)

//go:embed template.html
var templateHTML string

//go:embed starter/*
var starterFS embed.FS

const liveReloadScript = `<script>new EventSource("/_reload").onmessage=()=>location.reload()</script>`

func warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Printf("\033[33mWARNING\033[0m %s", msg)
}

type Config struct {
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Favicon     string   `yaml:"favicon"`
	Card        string   `yaml:"card"`
	BaseURL     string   `yaml:"base_url"`
	CSS       []string `yaml:"css"`
	InlineCSS bool     `yaml:"inline_css"`
	Inject    string   `yaml:"inject"`
}

type heading struct {
	Level int
	ID    string
	Text  string
}

type PageData struct {
	Title       string
	Description string
	Favicon     string
	Card        string
	Site        struct{ BaseURL string }
	CSS         []string
	InlineStyles []template.CSS
	Inject      template.HTML
	Content     template.HTML
}

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
	sb.WriteString("<ul>\n")
	for _, h := range headings {
		sb.WriteString(fmt.Sprintf("  <li><a href=\"#%s\">%s</a></li>\n", h.ID, h.Text))
	}
	sb.WriteString("</ul>")
	return sb.String()
}

func processContent(content string, dir string) string {
	// Expand <md src="..."> tags by reading and converting markdown files
	mdRe := regexp.MustCompile(`<md\s+src="([^"]*)"\s*/?>(?:</md>)?`)
	content = mdRe.ReplaceAllStringFunc(content, func(match string) string {
		m := mdRe.FindStringSubmatch(match)
		src := m[1]
		if src == "" {
			warn("<md> has empty src attribute")
			return ""
		}
		if !strings.HasSuffix(src, ".md") {
			warn("<md src=%q> is not a markdown file", src)
			return ""
		}
		mdPath := filepath.Join(dir, src)
		data, err := os.ReadFile(mdPath)
		if err != nil {
			warn("<md src=%q> references missing file", src)
			return ""
		}
		var buf bytes.Buffer
		if err := goldmark.Convert(data, &buf); err != nil {
			warn("<md src=%q> failed to convert: %v", src, err)
			return ""
		}
		return buf.String()
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

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:8], nil
}

func build(dir string, production bool) error {
	raw, err := os.ReadFile(filepath.Join(dir, "site.yaml"))
	if err != nil {
		return fmt.Errorf("site.yaml: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("site.yaml: %w", err)
	}

	// Warn on missing essential frontmatter
	if cfg.Title == "" {
		warn("missing 'title' in site.yaml")
	}
	if cfg.Description == "" {
		warn("missing 'description' in site.yaml")
	}
	if cfg.BaseURL == "" {
		warn("missing 'base_url' in site.yaml")
	}

	// Warn on title/description length
	if len(cfg.Title) > 60 {
		warn("title exceeds 60 characters (%d)", len(cfg.Title))
	}
	if len(cfg.Description) > 160 {
		warn("description exceeds 160 characters (%d)", len(cfg.Description))
	}

	// Warn on missing referenced files
	if cfg.Favicon != "" && !strings.HasPrefix(cfg.Favicon, "http") {
		if _, err := os.Stat(filepath.Join(dir, cfg.Favicon)); err != nil {
			warn("favicon file not found: %s", cfg.Favicon)
		}
	}
	if cfg.Card != "" && !strings.HasPrefix(cfg.Card, "http") {
		if _, err := os.Stat(filepath.Join(dir, cfg.Card)); err != nil {
			warn("card image not found: %s", cfg.Card)
		}
	}
	for _, css := range cfg.CSS {
		if !strings.HasPrefix(css, "http") {
			if _, err := os.Stat(filepath.Join(dir, css)); err != nil {
				warn("CSS file not found: %s", css)
			}
		}
	}
	content, err := os.ReadFile(filepath.Join(dir, "content.html"))
	if err != nil {
		return fmt.Errorf("content.html: %w", err)
	}

	// Inline CSS: read file contents into <style> tags instead of <link>
	var inlineStyles []template.CSS
	if cfg.InlineCSS {
		for _, css := range cfg.CSS {
			if strings.HasPrefix(css, "http") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, css))
			if err != nil {
				warn("could not read CSS for inlining: %s", css)
				continue
			}
			inlineStyles = append(inlineStyles, template.CSS(data))
		}
	}

	// When inlining, only keep remote CSS as <link> refs
	cssRefs := cfg.CSS
	if cfg.InlineCSS {
		var remote []string
		for _, css := range cfg.CSS {
			if strings.HasPrefix(css, "http") {
				remote = append(remote, css)
			}
		}
		cssRefs = remote
	}

	// Asset hashing for production builds
	if production {
		var hashed []string
		for _, css := range cfg.CSS {
			if strings.HasPrefix(css, "http") {
				hashed = append(hashed, css)
				continue
			}
			srcPath := filepath.Join(dir, css)
			hash, err := hashFile(srcPath)
			if err != nil {
				hashed = append(hashed, css)
				continue
			}
			ext := filepath.Ext(css)
			base := css[:len(css)-len(ext)]
			hashedName := fmt.Sprintf("%s.%s%s", base, hash, ext)
			data, err := os.ReadFile(srcPath)
			if err != nil {
				hashed = append(hashed, css)
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, hashedName), data, 0644); err != nil {
				hashed = append(hashed, css)
				continue
			}
			hashed = append(hashed, hashedName)
		}
		cssRefs = hashed
	}

	data := PageData{
		Title:        cfg.Title,
		Description:  cfg.Description,
		Favicon:      cfg.Favicon,
		Card:         cfg.Card,
		CSS:          cssRefs,
		InlineStyles: inlineStyles,
		Inject:       template.HTML(cfg.Inject),
		Content:      template.HTML(processContent(string(content), dir)),
	}
	baseURL := cfg.BaseURL
	if baseURL != "" && !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	data.Site.BaseURL = baseURL

	tmpl, err := template.New("page").Parse(templateHTML)
	if err != nil {
		return fmt.Errorf("template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("template: %w", err)
	}

	return os.WriteFile(filepath.Join(dir, "index.html"), buf.Bytes(), 0644)
}

var (
	clients   = make(map[chan struct{}]struct{})
	clientsMu sync.Mutex
)

func notifyClients() {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	for ch := range clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan struct{}, 1)
	clientsMu.Lock()
	clients[ch] = struct{}{}
	clientsMu.Unlock()
	defer func() {
		clientsMu.Lock()
		delete(clients, ch)
		clientsMu.Unlock()
	}()

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	for {
		select {
		case <-ch:
			fmt.Fprintf(w, "data: reload\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// fileServer serves static files but injects the livereload script into index.html in-memory.
func fileServer(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve index.html with injected livereload script
		p := r.URL.Path
		if p == "/" || p == "/index.html" {
			data, err := os.ReadFile(filepath.Join(dir, "index.html"))
			if err != nil {
				fs.ServeHTTP(w, r)
				return
			}
			injected := strings.Replace(string(data), "</body>", liveReloadScript+"\n  </body>", 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(injected))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	case "windows":
		cmd = "start"
	default:
		return
	}
	exec.Command(cmd, url).Start()
}

func run(dir string, port int) error {
	if err := build(dir, false); err != nil {
		return err
	}
	log.Printf("Built index.html")

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if strings.HasPrefix(filepath.Base(path), ".") && path != dir {
				return filepath.SkipDir
			}
			watcher.Add(path)
		}
		return nil
	})

	go func() {
		var timer *time.Timer
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(event.Name) == "index.html" {
					continue
				}
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						watcher.Add(event.Name)
					}
				}
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(100*time.Millisecond, func() {
					if err := build(dir, false); err != nil {
						log.Printf("Build error: %v", err)
					} else {
						log.Printf("Rebuilt index.html")
					}
					notifyClients()
				})
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("Watch error: %v", err)
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/_reload", sseHandler)
	mux.Handle("/", fileServer(dir))

	addr := fmt.Sprintf(":%d", port)
	url := fmt.Sprintf("http://localhost%s", addr)
	log.Printf("Serving at %s", url)
	openBrowser(url)
	return http.ListenAndServe(addr, mux)
}

func scaffold(name string) error {
	if err := os.MkdirAll(name, 0755); err != nil {
		return err
	}
	entries, err := starterFS.ReadDir("starter")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := starterFS.ReadFile("starter/" + e.Name())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(name, e.Name()), data, 0644); err != nil {
			return err
		}
	}
	log.Printf("Created %s/", name)
	return nil
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "new" {
		if err := scaffold(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "build" {
		if err := build(".", true); err != nil {
			log.Fatal(err)
		}
		log.Printf("Built index.html")
		return
	}

	port := 8080
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-p" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
			i++
		}
	}
	if err := run(".", port); err != nil {
		log.Fatal(err)
	}
}
