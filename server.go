package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
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

func readTailwindLogs(input, stream string, r io.Reader, onDone func()) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		log.Printf("[tailwind:%s:%s] %s", input, stream, line)
		if strings.Contains(line, "Done in") {
			onDone()
		}
	}
	if err := scanner.Err(); err != nil {
		warn("tailwind log stream read failed for %s (%s): %v", input, stream, err)
	}
}

func startTailwindWatcher(dir string) (string, <-chan struct{}, func()) {
	raw, err := os.ReadFile(filepath.Join(dir, "pager.yaml"))
	if err != nil {
		return "", nil, func() {}
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		warn("could not parse pager.yaml for Tailwind watcher: %v", err)
		return "", nil, func() {}
	}

	if !cfg.Tailwind {
		return "", nil, func() {}
	}

	tmpDir, err := os.MkdirTemp("", "pager-tailwind-*")
	if err != nil {
		warn("could not create temp directory for Tailwind watcher: %v", err)
		return "", nil, func() {}
	}
	inputPath := filepath.Join(tmpDir, "input.css")
	if err := os.WriteFile(inputPath, []byte(syntheticTailwindInput), 0644); err != nil {
		warn("could not create synthetic Tailwind input: %v", err)
		_ = os.RemoveAll(tmpDir)
		return "", nil, func() {}
	}
	outPath := filepath.Join(tmpDir, "output.css")

	tailwindDone := make(chan struct{}, 1)
	notifyTailwindDone := func() {
		select {
		case tailwindDone <- struct{}{}:
		default:
		}
	}

	cmd := exec.Command("tailwindcss", "-i", inputPath, "-o", outPath, "--watch=always")
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		warn("could not capture Tailwind stdout: %v", err)
		_ = os.RemoveAll(tmpDir)
		return "", nil, func() {}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		warn("could not capture Tailwind stderr: %v", err)
		_ = os.RemoveAll(tmpDir)
		return "", nil, func() {}
	}
	if err := cmd.Start(); err != nil {
		warn("could not start Tailwind watcher: %v", err)
		_ = os.RemoveAll(tmpDir)
		return "", nil, func() {}
	}
	go readTailwindLogs("synthetic", "stdout", stdout, notifyTailwindDone)
	go readTailwindLogs("synthetic", "stderr", stderr, notifyTailwindDone)
	log.Printf("Started Tailwind watcher (synthetic input)")

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
		_ = os.RemoveAll(tmpDir)
	}

	return outPath, tailwindDone, cleanup
}

func run(dir string, port int) error {
	tailwindOutputPath, tailwindDone, stopTailwindWatcher := startTailwindWatcher(dir)
	defer stopTailwindWatcher()
	runBuild := func(trigger string) error {
		started := time.Now()
		log.Printf("[perf] rebuild_start trigger=%s", trigger)
		err := buildWithTailwindOutput(dir, tailwindOutputPath)
		if err != nil {
			log.Printf("[perf] rebuild_done trigger=%s status=error elapsed=%s", trigger, time.Since(started))
			return err
		}
		log.Printf("[perf] rebuild_done trigger=%s status=ok elapsed=%s", trigger, time.Since(started))
		return nil
	}

	if err := runBuild("startup"); err != nil {
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
		const fsDebounceDelay = 300 * time.Millisecond
		const tailwindDebounceDelay = 50 * time.Millisecond
		const relevantOps = fsnotify.Write | fsnotify.Create | fsnotify.Rename | fsnotify.Remove
		const generatedHTML = "index.html"
		const generatedMarkdown = "index.md"
		timer := time.NewTimer(fsDebounceDelay)
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		defer timer.Stop()

		var timerC <-chan time.Time
		pendingRebuild := false
		pendingFSEvents := 0
		pendingTailwindEvents := 0
		tailwindAwaitingFS := false
		scheduleRebuild := func(delay time.Duration) {
			pendingRebuild = true
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(delay)
			timerC = timer.C
		}

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&relevantOps == 0 {
					continue
				}
				base := filepath.Base(event.Name)
				if base == generatedHTML || base == generatedMarkdown {
					continue
				}
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						watcher.Add(event.Name)
					}
				}
				pendingFSEvents++
				tailwindAwaitingFS = true
				scheduleRebuild(fsDebounceDelay)
			case _, ok := <-tailwindDone:
				if !ok {
					tailwindDone = nil
					continue
				}
				if !tailwindAwaitingFS {
					// Ignore spontaneous Tailwind completions to avoid rebuild loops
					// caused by generated index.html changes being re-scanned.
					continue
				}
				pendingTailwindEvents++
				tailwindAwaitingFS = false
				scheduleRebuild(tailwindDebounceDelay)
			case <-timerC:
				timerC = nil
				if !pendingRebuild {
					continue
				}
				trigger := "unknown"
				switch {
				case pendingFSEvents > 0 && pendingTailwindEvents > 0:
					trigger = fmt.Sprintf("fs+tailwind fs_events=%d tailwind_events=%d", pendingFSEvents, pendingTailwindEvents)
				case pendingFSEvents > 0:
					trigger = fmt.Sprintf("fs fs_events=%d", pendingFSEvents)
				case pendingTailwindEvents > 0:
					trigger = fmt.Sprintf("tailwind tailwind_events=%d", pendingTailwindEvents)
				}
				pendingRebuild = false
				pendingFSEvents = 0
				pendingTailwindEvents = 0
				tailwindAwaitingFS = false
				if err := runBuild(trigger); err != nil {
					buildFail(err)
				} else {
					log.Printf("Rebuilt index.html")
				}
				notifyClients()
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
