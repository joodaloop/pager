# Pager ([pager.joodaloop.com](https:pager.joodaloop.com))

A single-page site builder with live reload, auto-linked headings, image aspect ratios, markdown support, and accessibility warnings.

You set up your site details in `pager.yaml` like so:
```yaml
title: My Site
description: A cool site
favicon: favicon.png
card: card.png
domain: example.com
port: 8080
css:
  - style.css
inject: '<!-- anything you want to add to the <head> of the page -->'
```

And write your content in `pager.html`, no need for `<body>` or `<head>` tags, start directly with the content. I use `<main>` as content root myself. Add your stylesheets, assets and other resources however you like as long as you link to them correctly. But don't worry, if you have any broken links, the server will warn you.

Speaking of which, you can start the server by entering the site's root and running `pager`. This builds `index.html` and `index.md`, starts a server that live-reloads on any file change. 

#### **NOTE:** Unlike what you might expect, the `index.html` and `index.md` files are not meant to be edited by you. They are generated from the `pager.html` file, which is the thing you *should* be editing.


## Additional features

Apart from giving you good HTML skeleton from your config details, Pager also comes with:

### <convert> snippets

Convert markdown or CSV files to HTML with the `<convert>` tag:

```html
<convert src="about.md" />
<convert src="data.csv" />
```

- `.md` — converted to HTML via goldmark
- `.csv` — rendered as an HTML `<table>` (first row becomes `<thead>`)

### Syntax highlighting

Embed any file as a syntax-highlighted code block with the `<syntax>` tag, with the language auto- detected from the file extension using [chroma](https://github.com/alecthomas/chroma).

```html
<syntax src="example.py" />
<syntax src="config.yaml" />
```

### Table of contents

Add `<toc />` anywhere in `content.html` to render a list of links to all headings.

### Misc. 
- **Markdown page for LLMs to read/humans to copy** — generates `index.md` with YAML frontmatter as a render-equivalent of the HTML page.
- **Headings** without an `id` get an auto-generated `id` based on their text content: `<h2>My Section</h2>` → `<h2 id="my-section">My Section</h2>`
- **Images** with local `src` get `aspect-ratio` from actual file dimensions
- **External links** get `target="_blank"` and `rel="noopener"`
- **Local link checking** — warns on `<a href="#missing-id">` and `<a href="missing-file.pdf">`
- **Asset hashing** — links to CSS files using content hashes query strings for cache busting.
- **Warnings** for missing alt text, icon-only links without `aria-label`, missing frontmatter fields, missing referenced files, title > 60 chars, description > 160 chars


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

We have a small set of CLI commands, and all you really need in practice is the `pager` command.

### Create a new site:

```sh
pager new mysite
cd mysite
```

This is optional, Pager will work in any folder that has a `pager.html` and `pager.yaml` file, and you can arrange everything else however you like. This command just scaffolds my preferred structure to get you up and running.

### Run the dev server:

```sh
pager
```

This builds `index.html` and `index.md`, starts a server that live-reloads on any file change.

Use `-p` for a different port:

```sh
pager -p 3000
```


### Plain build:

```sh
pager build
```

Builds `index.html` and `index.md` without starting the server.
