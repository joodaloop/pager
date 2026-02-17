package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"html/template"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"
	"gopkg.in/yaml.v3"
)

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
	raw, err := os.ReadFile(filepath.Join(dir, "pager.yaml"))
	if err != nil {
		return fmt.Errorf("pager.yaml: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("pager.yaml: %w", err)
	}

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
	} else if !strings.HasPrefix(cfg.Favicon, "http") {
		if _, err := os.Stat(filepath.Join(dir, cfg.Favicon)); err != nil {
			warn("favicon file not found: %s", cfg.Favicon)
		}
	}
	if cfg.Card == "" {
		warn("missing 'card' in pager.yaml")
	} else if !strings.HasPrefix(cfg.Card, "http") {
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
	content, err := os.ReadFile(filepath.Join(dir, "pager.html"))
	if err != nil {
		return fmt.Errorf("pager.html: %w", err)
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

	// Cache-busting: append content hash as query string
	{
		var versioned []string
		for _, css := range cssRefs {
			if strings.HasPrefix(css, "http") {
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

	// Syntax theme: inline chroma CSS if theme is set
	// Supports "light" or "light/dark" format (e.g. "github" or "github/monokai")
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
	domain := cfg.Domain
	if domain != "" && !strings.HasPrefix(domain, "http://") && !strings.HasPrefix(domain, "https://") {
		domain = "https://" + domain
	}
	data.Site.Domain = domain

	tmpl, err := template.New("page").Parse(templateHTML)
	if err != nil {
		return fmt.Errorf("template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("template: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "index.html"), buf.Bytes(), 0644); err != nil {
		return err
	}

	// Generate markdown equivalent
	md, err := htmltomd.ConvertString(string(data.Content))
	if err != nil {
		warn("failed to generate index.md: %v", err)
	} else {
		header := fmt.Sprintf("<!-- THIS FILE IS AUTO-GENERATED FROM INDEX.HTML -->\n---\ntitle: %q\ndescription: %q\ndomain: %q\n---\n\n", cfg.Title, cfg.Description, cfg.Domain)
		if err := os.WriteFile(filepath.Join(dir, "index.md"), []byte(header+md), 0644); err != nil {
			return err
		}
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
