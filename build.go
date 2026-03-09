package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"
	"gopkg.in/yaml.v3"
)

const syntheticTailwindInput = `@import "tailwindcss";
@source not "./index.html";
@source not "./index.md";
`

type perfStep struct {
	name     string
	duration time.Duration
}

type buildPerf struct {
	startedAt time.Time
	steps     []perfStep
}

func newBuildPerf() *buildPerf {
	return &buildPerf{startedAt: time.Now()}
}

func (p *buildPerf) mark(step string, started time.Time) {
	p.steps = append(p.steps, perfStep{name: step, duration: time.Since(started)})
}

func (p *buildPerf) logSummary() {
	parts := make([]string, 0, len(p.steps))
	for _, step := range p.steps {
		parts = append(parts, fmt.Sprintf("%s=%s", step.name, step.duration))
	}
	log.Printf("[perf] build total=%s %s", time.Since(p.startedAt), strings.Join(parts, " "))
}

func isRemoteAsset(path string) bool {
	return strings.HasPrefix(path, "http")
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

func build(dir string) error {
	return buildWithTailwindOutput(dir, "")
}

func buildWithTailwindOutput(dir, tailwindOutputPath string) error {
	perf := newBuildPerf()
	defer perf.logSummary()

	stepStarted := time.Now()
	raw, err := os.ReadFile(filepath.Join(dir, "pager.yaml"))
	if err != nil {
		return fmt.Errorf("pager.yaml: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("pager.yaml: %w", err)
	}
	perf.mark("config", stepStarted)

	stepStarted = time.Now()
	// Warn on missing essential frontmatter
	if cfg.Title == "" {
		warn("missing 'title' in pager.yaml")
	}
	if cfg.Description == "" {
		warn("missing 'description' in pager.yaml")
	}
	if cfg.Domain == "" {
		warn("missing 'domain' in pager.yaml")
	}

	// Warn on title/description length
	if len(cfg.Title) > 60 {
		warn("title exceeds 60 characters (%d)", len(cfg.Title))
	}
	if len(cfg.Description) > 160 {
		warn("description exceeds 160 characters (%d)", len(cfg.Description))
	}

	// Warn on missing referenced files
	if cfg.Favicon == "" {
		warn("missing 'favicon' in pager.yaml")
	} else if !isRemoteAsset(cfg.Favicon) {
		if _, err := os.Stat(filepath.Join(dir, cfg.Favicon)); err != nil {
			warn("favicon file not found: %s", cfg.Favicon)
		}
	}
	if cfg.Card == "" {
		warn("missing 'card' in pager.yaml")
	} else if !isRemoteAsset(cfg.Card) {
		if _, err := os.Stat(filepath.Join(dir, cfg.Card)); err != nil {
			warn("card image not found: %s", cfg.Card)
		}
	}
	for _, css := range cfg.CSS {
		if !isRemoteAsset(css) {
			if _, err := os.Stat(filepath.Join(dir, css)); err != nil {
				warn("CSS file not found: %s", css)
			}
		}
	}
	perf.mark("warnings", stepStarted)

	stepStarted = time.Now()
	content, err := os.ReadFile(filepath.Join(dir, "pager.html"))
	if err != nil {
		return fmt.Errorf("pager.html: %w", err)
	}
	perf.mark("read_html", stepStarted)

	// Tailwind CSS: compile from a synthetic entry and inline the output.
	stepStarted = time.Now()
	var inlineStyles []template.CSS
	if cfg.Tailwind {
		out, err := readOrCompileTailwindCSS(dir, tailwindOutputPath)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				warn("tailwindcss not found — install with: npm i -g @tailwindcss/cli or download the binary from https://github.com/tailwindlabs/tailwindcss/releases/tag/v4.2.0")
			} else {
				warn("tailwind failed: %v", err)
			}
		} else if len(out) > 0 {
			inlineStyles = append(inlineStyles, template.CSS(out))
		}
	}
	perf.mark("tailwind", stepStarted)

	// Inline CSS: read file contents into <style> tags instead of <link>
	stepStarted = time.Now()
	if cfg.InlineCSS {
		for _, css := range cfg.CSS {
			if isRemoteAsset(css) {
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
	perf.mark("inline_css", stepStarted)

	// Build <link> refs: exclude local files when inlining
	stepStarted = time.Now()
	var cssRefs []string
	for _, css := range cfg.CSS {
		if cfg.InlineCSS && !isRemoteAsset(css) {
			continue
		}
		cssRefs = append(cssRefs, css)
	}
	perf.mark("css_refs", stepStarted)

	// Cache-busting: append content hash as query string
	stepStarted = time.Now()
	{
		var versioned []string
		for _, css := range cssRefs {
			if isRemoteAsset(css) {
				versioned = append(versioned, css)
				continue
			}
			hash, err := hashFile(filepath.Join(dir, css))
			if err != nil {
				versioned = append(versioned, css)
				continue
			}
			versioned = append(versioned, fmt.Sprintf("%s?v=%s", css, hash))
		}
		cssRefs = versioned
	}
	perf.mark("hash_css_refs", stepStarted)

	// Syntax theme: inline chroma CSS if theme is set
	// Supports "light" or "light/dark" format (e.g. "github" or "github/monokai")
	stepStarted = time.Now()
	if cfg.Theme != "" {
		if parts := strings.SplitN(cfg.Theme, "/", 2); len(parts) == 2 {
			lightCSS := syntaxThemeCSS(parts[0])
			if lightCSS == "" {
				warn("unknown light syntax theme: %s", parts[0])
			} else {
				inlineStyles = append(inlineStyles, template.CSS(lightCSS))
			}
			darkCSS := syntaxThemeDarkCSS(parts[1])
			if darkCSS == "" {
				warn("unknown dark syntax theme: %s", parts[1])
			} else {
				inlineStyles = append(inlineStyles, template.CSS(darkCSS))
			}
		} else {
			css := syntaxThemeCSS(cfg.Theme)
			if css == "" {
				warn("unknown syntax theme: %s", cfg.Theme)
			} else {
				inlineStyles = append(inlineStyles, template.CSS(css))
			}
		}
	}
	perf.mark("syntax_theme", stepStarted)

	stepStarted = time.Now()
	processedContent := template.HTML(processContent(string(content), dir))
	perf.mark("process_content", stepStarted)

	data := PageData{
		Title:        cfg.Title,
		Description:  cfg.Description,
		Favicon:      cfg.Favicon,
		Card:         cfg.Card,
		CSS:          cssRefs,
		InlineStyles: inlineStyles,
		Inject:       template.HTML(cfg.Inject),
		Content:      processedContent,
	}
	domain := cfg.Domain
	if domain != "" && !strings.HasPrefix(domain, "http://") && !strings.HasPrefix(domain, "https://") {
		domain = "https://" + domain
	}
	data.Site.Domain = domain

	stepStarted = time.Now()
	tmpl, err := template.New("page").Parse(templateHTML)
	if err != nil {
		return fmt.Errorf("template: %w", err)
	}
	perf.mark("template_parse", stepStarted)

	stepStarted = time.Now()
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	perf.mark("template_exec", stepStarted)

	stepStarted = time.Now()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), buf.Bytes(), 0644); err != nil {
		return err
	}
	perf.mark("write_index_html", stepStarted)

	stepStarted = time.Now()
	if err := writeMarkdownFile(dir, cfg, data.Content); err != nil {
		return err
	}
	perf.mark("write_markdown", stepStarted)

	return nil
}

func readOrCompileTailwindCSS(dir, tailwindOutputPath string) ([]byte, error) {
	totalStarted := time.Now()
	if tailwindOutputPath != "" {
		out, err := os.ReadFile(tailwindOutputPath)
		if err == nil && len(out) > 0 {
			log.Printf("[perf] tailwind source=watcher-output bytes=%d elapsed=%s", len(out), time.Since(totalStarted))
			return out, nil
		}
		if err != nil && !os.IsNotExist(err) {
			warn("tailwind watcher output unavailable: %v", err)
		}
	}
	out, err := compileTailwindFromSyntheticInput(dir)
	if err == nil {
		log.Printf("[perf] tailwind source=synthetic-cli bytes=%d elapsed=%s", len(out), time.Since(totalStarted))
	}
	return out, err
}

func compileTailwindFromSyntheticInput(dir string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "pager-tailwind-build-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	inputPath := filepath.Join(tmpDir, "input.css")
	if err := os.WriteFile(inputPath, []byte(syntheticTailwindInput), 0644); err != nil {
		return nil, err
	}

	cmd := exec.Command("tailwindcss", "-i", inputPath, "--output", "-")
	cmd.Dir = dir
	cmdStarted := time.Now()
	out, err := cmd.Output()
	log.Printf("[perf] tailwind_cli_compile elapsed=%s", time.Since(cmdStarted))
	return out, err
}

func writeMarkdownFile(dir string, cfg Config, content template.HTML) error {
	md, err := htmltomd.ConvertString(string(content))
	if err != nil {
		warn("failed to generate index.md: %v", err)
		return nil
	}

	header := fmt.Sprintf("<!-- THIS FILE IS AUTO-GENERATED FROM INDEX.HTML -->\n---\ntitle: %q\ndescription: %q\ndomain: %q\n---\n\n", cfg.Title, cfg.Description, cfg.Domain)
	if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(header+md), 0644); err != nil {
		return err
	}

	return nil
}

func deploy(dir string) error {
	if err := build(dir); err != nil {
		return err
	}
	log.Printf("Built index.html")

	raw, err := os.ReadFile(filepath.Join(dir, "pager.yaml"))
	if err != nil {
		return fmt.Errorf("pager.yaml: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("pager.yaml: %w", err)
	}
	if cfg.Deploy == "" {
		return fmt.Errorf("no 'deploy' command defined in pager.yaml")
	}

	log.Printf("Running: %s", cfg.Deploy)
	cmd := exec.Command("sh", "-c", cfg.Deploy)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func scaffold(name string) error {
	return fs.WalkDir(starterFS, "starter", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("starter", path)
		dest := filepath.Join(name, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		if _, err := os.Stat(dest); err == nil {
			warn("skipping existing file: %s", dest)
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		data, err := starterFS.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0644); err != nil {
			return err
		}
		return nil
	})
}
