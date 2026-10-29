// DubbelWeg: finds files with exactly the same content (on any drive, also USB) and lets you
// remove the extra copies safely: to the Recycle Bin, or on USB sticks to a folder
// "_Dubbelen (verwijderd)" on that stick. At least one copy of every file always stays.
// The screen is a small page shown in an Edge/Chrome app window; nothing goes on the internet.
package main

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/index.html
var page []byte

const addr = "127.0.0.1:47124"

var version = "dev"

type state struct {
	mu       sync.Mutex
	running  bool
	progress *Progress
	groups   []Group
	started  time.Time
	took     time.Duration
	lastPing time.Time
}

var st = &state{}

func main() {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Already running: just show its window again.
		openBrowser()
		return
	}
	mux := http.NewServeMux()
	routes(mux)
	go func() {
		time.Sleep(200 * time.Millisecond)
		openBrowser()
	}()
	go watchWindow()
	http.Serve(ln, mux)
}

// watchWindow quits the program a while after its window was closed.
func watchWindow() {
	for {
		time.Sleep(5 * time.Second)
		st.mu.Lock()
		last := st.lastPing
		st.mu.Unlock()
		if !last.IsZero() && time.Since(last) > 90*time.Second {
			os.Exit(0)
		}
		if last.IsZero() && time.Since(startTime) > 3*time.Minute {
			os.Exit(0) // the window never opened
		}
	}
}

var startTime = time.Now()

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<20)).Decode(v)
}

// inResults: the page may only open, show or remove files that the search found.
func inResults(p string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	k := strings.ToLower(p)
	for _, g := range st.groups {
		for _, f := range g.Files {
			if strings.ToLower(f.Path) == k {
				return true
			}
		}
	}
	return false
}

func routes(mux *http.ServeMux) {
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(page)
	})
	post := func(path string, h http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			// Only this program's own page may ask for anything.
			if r.Method != http.MethodPost || r.Header.Get("X-DubbelWeg") != "1" {
				http.Error(w, "nee", http.StatusForbidden)
				return
			}
			h(w, r)
		})
	}
	post("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		st.lastPing = time.Now()
		st.mu.Unlock()
		writeJSON(w, map[string]string{"version": version})
	})
	post("/api/drives", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, drives()) })
	post("/api/pick", func(w http.ResponseWriter, r *http.Request) {
		p, err := pickFolder()
		if err != nil {
			p = ""
		}
		writeJSON(w, map[string]string{"path": p})
	})
	post("/api/scan", func(w http.ResponseWriter, r *http.Request) {
		var opt Options
		if err := readJSON(r, &opt); err != nil || len(opt.Roots) == 0 {
			http.Error(w, "kies eerst een schijf of map", http.StatusBadRequest)
			return
		}
		st.mu.Lock()
		if st.running {
			st.mu.Unlock()
			http.Error(w, "er wordt al gezocht", http.StatusConflict)
			return
		}
		p := &Progress{}
		st.running, st.progress, st.groups, st.started = true, p, nil, time.Now()
		st.mu.Unlock()
		go func() {
			g := Scan(opt, p)
			st.mu.Lock()
			st.running, st.groups, st.took = false, g, time.Since(st.started)
			st.mu.Unlock()
		}()
		writeJSON(w, map[string]bool{"ok": true})
	})
	post("/api/cancel", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		if st.progress != nil {
			st.progress.Cancelled.Store(true)
		}
		st.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
	})
	post("/api/status", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		out := map[string]any{"running": st.running, "hasResults": st.groups != nil}
		if p := st.progress; p != nil {
			out["phase"] = p.phase()
			out["files"] = p.Files.Load()
			out["candidates"] = p.Candidates.Load()
			out["bytesTotal"] = p.BytesTotal.Load()
			out["bytesDone"] = p.BytesDone.Load()
			out["current"] = p.current()
			out["skipped"] = p.Skipped.Load()
			out["cancelled"] = p.Cancelled.Load()
			el := time.Since(st.started)
			if !st.running {
				el = st.took
			}
			out["seconds"] = int(el.Seconds())
		}
		writeJSON(w, out)
	})
	post("/api/results", func(w http.ResponseWriter, r *http.Request) {
		st.mu.Lock()
		defer st.mu.Unlock()
		g := st.groups
		if g == nil {
			g = []Group{}
		}
		writeJSON(w, map[string]any{"groups": g})
	})
	post("/api/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := readJSON(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		st.mu.Lock()
		if st.running {
			st.mu.Unlock()
			http.Error(w, "wacht tot het zoeken klaar is", http.StatusConflict)
			return
		}
		groups := st.groups
		st.mu.Unlock()
		res, err := Remove(groups, req.Paths)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Take the removed files out of the results.
		gone := map[string]bool{}
		for _, x := range res {
			if x.OK {
				gone[strings.ToLower(x.Path)] = true
			}
		}
		st.mu.Lock()
		var keep []Group
		for _, g := range st.groups {
			var fl []File
			for _, f := range g.Files {
				if !gone[strings.ToLower(f.Path)] {
					fl = append(fl, f)
				}
			}
			if len(fl) > 1 {
				g.Files = fl
				keep = append(keep, g)
			}
		}
		if keep == nil {
			keep = []Group{}
		}
		st.groups = keep
		st.mu.Unlock()
		writeJSON(w, map[string]any{"results": res})
	})
	pathAction := func(fn func(string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Path string `json:"path"`
			}
			if err := readJSON(r, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			ok := inResults(req.Path)
			// The quarantine folder of a drive may also be opened.
			if !ok && filepath.Base(req.Path) == quarantineName {
				ok = true
			}
			if !ok {
				http.Error(w, "nee", http.StatusForbidden)
				return
			}
			if err := fn(req.Path); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
		}
	}
	post("/api/reveal", pathAction(reveal))
	post("/api/open", pathAction(func(p string) error {
		if filepath.Base(p) == quarantineName {
			return openFolder(p)
		}
		return openFile(p)
	}))
	post("/api/log", func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(appDataDir(), "verwijderd.txt")
		if _, err := os.Stat(p); err != nil {
			http.Error(w, "er is nog niets verwijderd", http.StatusNotFound)
			return
		}
		openFile(p)
		writeJSON(w, map[string]bool{"ok": true})
	})
	// Preview of a picture, song or video from the results.
	mux.HandleFunc("/api/file", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		if !inResults(p) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "nee", http.StatusForbidden)
			return
		}
		http.ServeFile(w, r, p)
	})
}

func openBrowser() {
	url := "http://" + addr + "/"
	if b := findBrowser(); b != "" {
		profile := filepath.Join(appDataDir(), "browser")
		cmd := exec.Command(b, "--app="+url, "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
			"--window-size=1250,880", "--disable-background-timer-throttling", "--disable-renderer-backgrounding",
			"--disable-features=TranslateUI")
		if cmd.Start() == nil {
			return
		}
	}
	openFile(url)
}

func findBrowser() string {
	pf, pf86, local := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")
	if pf == "" {
		return ""
	}
	for _, p := range []string{
		filepath.Join(pf86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pf86, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(local, `Google\Chrome\Application\chrome.exe`),
	} {
		if s, err := os.Stat(p); err == nil && !s.IsDir() {
			return p
		}
	}
	return ""
}
