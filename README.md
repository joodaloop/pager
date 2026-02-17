# pager

A single-page site builder with live reload, auto-linked headings, image aspect ratios, markdown support, and accessibility warnings.

## Install

```sh
git clone https://github.com/youruser/pager.git
cd pager
go install
```

This places the `pager` binary in `~/go/bin`. Make sure it's in your PATH:

```sh
# add to your ~/.zshrc or ~/.bashrc if not already there
export PATH=$PATH:$HOME/go/bin
```

## Usage

**Create a new site:**

```sh
pager new mysite
cd mysite
```

**Run the dev server:**

```sh
pager
```

This builds `index.html` and `index.md`, starts a server at `http://localhost:8080`, opens your browser, and live-reloads on any file change.

Use `-p` for a different port:

```sh
pager -p 3000
```

**Production build:**

```sh
pager build
```

Builds `index.html` and `index.md` with asset hashing (e.g. `style.abc123.css`) for cache busting.

## Configuration

Edit `pager.yaml`:

```yaml
title: My Site
description: A cool site
favicon: favicon.png
card: card.png
domain: example.com
port: 8080
css:
  - style.css
inline_css: true
inject: '<link rel="preconnect" href="https://fonts.googleapis.com">'
```

- `domain` doesn't need `https://` — it's added automatically.
- `port` sets the dev server port (default `8080`). Can be overridden with `pager -p 3000`.
- `inject` inserts raw HTML into `<head>`.
- `css` lists your stylesheets — linked via `<link>` tags by default.
- `inline_css: true` embeds the contents of local CSS files in `<style>` tags instead of linking them.

## Includes

Include files in `content.html` with the `<include>` tag:

```html
<include src="about.md" />
<include src="snippet.html" />
<include src="data.csv" />
<include src="example.py" />
```

The behavior depends on the file extension:

- `.html` — inserted as raw HTML
- `.md` — converted to HTML via goldmark
- `.csv` — rendered as an HTML `<table>` (first row becomes `<thead>`)
- Anything else — syntax-highlighted code block via [chroma](https://github.com/alecthomas/chroma) (language detected from extension)

## Markdown output

Every build generates `index.md` alongside `index.html` — a markdown equivalent of the page content, available at `/index.md` on your domain. It includes YAML frontmatter with the site's title, description, and domain:

```markdown
---
title: "My Site"
description: "A cool site"
domain: "example.com"
---

# Hello, world
...
```

## Table of contents

Add `<toc />` anywhere in `content.html` to render a list of links to all headings:

```html
<nav>
  <toc />
</nav>
```

## What it does

- Builds `index.html` and `index.md` from `pager.yaml` + `content.html` + a built-in HTML template
- **Markdown includes** — `<md src="file.md" />` inlines converted markdown
- **Markdown output** — generates `index.md` with YAML frontmatter as a render-equivalent of the HTML page
- **Headings** without an `id` get an auto-generated `id` based on their text content: `<h2>My Section</h2>` → `<h2 id="my-section">My Section</h2>`
- **`<toc />`** renders an unordered list of links to all headings on the page
- **Images** with local `src` get `aspect-ratio` from actual file dimensions
- **External links** get `target="_blank"` and `rel="noopener"`
- **Local link checking** — warns on `<a href="#missing-id">` and `<a href="missing-file.pdf">`
- **Asset hashing** — `pager build` copies CSS files with content hashes for cache busting
- **Warnings** for missing alt text, icon-only links without `aria-label`, missing frontmatter fields, missing referenced files, title > 60 chars, description > 160 chars
