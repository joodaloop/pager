package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"os"

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
	Domain      string   `yaml:"domain"`
	Port        int      `yaml:"port"`
	CSS         []string `yaml:"css"`
	InlineCSS   bool     `yaml:"inline_css"`
	Inject      string   `yaml:"inject"`
	Theme       string   `yaml:"theme"`
	Deploy      string   `yaml:"deploy"`
}

type heading struct {
	Level int
	ID    string
	Text  string
}

type PageData struct {
	Title        string
	Description  string
	Favicon      string
	Card         string
	Site         struct{ Domain string }
	CSS          []string
	InlineStyles []template.CSS
	Inject       template.HTML
	Content      template.HTML
}

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "new" {
		if err := scaffold(os.Args[2]); err != nil {
			log.Fatal(err)
		}
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "build" {
		if err := build("."); err != nil {
			log.Fatal(err)
		}
		log.Printf("Built index.html")
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "deploy" {
		if err := deploy("."); err != nil {
			log.Fatal(err)
		}
		return
	}

	port := 0
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-p" && i+1 < len(os.Args) {
			fmt.Sscanf(os.Args[i+1], "%d", &port)
			i++
		}
	}
	// Read port from pager.yaml if not overridden by -p flag
	if port == 0 {
		if raw, err := os.ReadFile("pager.yaml"); err == nil {
			var cfg Config
			if err := yaml.Unmarshal(raw, &cfg); err == nil && cfg.Port > 0 {
				port = cfg.Port
			}
		}
	}
	if port == 0 {
		port = 8080
	}
	if err := run(".", port); err != nil {
		log.Fatal(err)
	}
}
