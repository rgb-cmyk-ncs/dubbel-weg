package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// On drives without a Recycle Bin (USB sticks, SD cards) removed copies are moved here, at the
// root of the same drive, so nothing is lost until the user empties the folder.
const quarantineName = "_Dubbelen (verwijderd)"

type RemoveResult struct {
	Path string `json:"path"`
	OK   bool   `json:"ok"`
	How  string `json:"how,omitempty"`
	Err  string `json:"err,omitempty"`
}

var logMu sync.Mutex

func logLine(s string) {
	logMu.Lock()
	defer logMu.Unlock()
	dir := appDataDir()
	os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "verwijderd.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\r\n", time.Now().Format("2006-01-02 15:04:05"), s)
}

// Remove takes away the chosen copies. It refuses to touch anything that is not part of the
// last search, and never removes every copy of a file.
func Remove(groups []Group, paths []string) ([]RemoveResult, error) {
	where := map[string]int{}
	for gi, g := range groups {
		for _, f := range g.Files {
			where[strings.ToLower(f.Path)] = gi
		}
	}
	chosen := map[int]int{}
	uniq := map[string]bool{}
	var list []string
	for _, p := range paths {
		k := strings.ToLower(p)
		if uniq[k] {
			continue
		}
		uniq[k] = true
		gi, ok := where[k]
		if !ok {
			return nil, fmt.Errorf("%s hoort niet bij de zoekresultaten", p)
		}
		chosen[gi]++
		list = append(list, p)
	}
	for gi, n := range chosen {
		if n >= len(groups[gi].Files) {
			return nil, errors.New("van elk bestand moet minstens 1 exemplaar blijven staan: " + filepath.Base(groups[gi].Files[0].Path))
		}
	}

	sizes := map[string]int64{}
	for _, g := range groups {
		for _, f := range g.Files {
			sizes[strings.ToLower(f.Path)] = f.Size
		}
	}
	out := make([]RemoveResult, 0, len(list))
	for _, p := range list {
		r := RemoveResult{Path: p}
		st, err := os.Stat(p)
		switch {
		case err != nil:
			r.Err = "niet meer gevonden"
		case st.Size() != sizes[strings.ToLower(p)]:
			r.Err = "bestand is veranderd sinds het zoeken; niet aangeraakt"
		case recycleAvailable(p):
			if err := recycle(p); err != nil {
				r.Err = err.Error()
			} else {
				r.OK, r.How = true, "Prullenbak"
			}
		default:
			if dest, err := quarantine(p); err != nil {
				r.Err = err.Error()
			} else {
				r.OK, r.How = true, dest
			}
		}
		if r.OK {
			logLine("weg (" + r.How + "): " + p)
		} else {
			logLine("NIET weg (" + r.Err + "): " + p)
		}
		out = append(out, r)
	}
	return out, nil
}

// quarantine moves a file into the quarantine folder at the root of its own drive,
// keeping its folder path so it is easy to put back.
func quarantine(p string) (string, error) {
	root := rootOf(p)
	rel, err := filepath.Rel(root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", errors.New("kan de schijf van dit bestand niet bepalen")
	}
	dest := filepath.Join(root, quarantineName, rel)
	if _, err := os.Stat(dest); err == nil {
		ext := filepath.Ext(dest)
		dest = strings.TrimSuffix(dest, ext) + fmt.Sprintf(" (%d)", time.Now().UnixNano()%100000) + ext
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(p, dest); err != nil {
		return "", err
	}
	return filepath.Join(root, quarantineName), nil
}
