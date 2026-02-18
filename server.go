package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

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

func run(dir string, port int) error {
	if err := build(dir); err != nil {
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
				if filepath.Base(event.Name) == "index.html" || filepath.Base(event.Name) == "index.md" {
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
				timer = time.AfterFunc(300*time.Millisecond, func() {
					if err := build(dir); err != nil {
						buildFail(err)
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

	for attempts := 0; attempts < 50; attempts++ {
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			port++
			continue
		}
		log.Printf("Serving at http://localhost:%d", port)
		return http.Serve(ln, mux)
	}
	return fmt.Errorf("could not find an open port after 50 attempts")
}
