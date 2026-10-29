package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Damaged files: typical after recovering a hard disk. Pictures that no longer open, documents
// that are cut off, and files that are only zeros.

const brokenName = "_Beschadigd (nakijken)"

type Broken struct {
	Path   string    `json:"path"`
	Size   int64     `json:"size"`
	Mod    time.Time `json:"mod"`
	Level  string    `json:"level"` // "kapot" or "verdacht"
	Reason string    `json:"reason"`
}

var checkKinds = map[string][]string{
	"fotos":      {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".webp"},
	"documenten": {".pdf", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".zip"},
	"media":      {".mp3", ".mp4", ".mov", ".m4a", ".avi", ".wav"},
}

// ScanBroken checks every file of the chosen kinds under the roots.
func ScanBroken(roots []string, kinds []string, j *Job) []Broken {
	exts := map[string]bool{}
	if len(kinds) == 0 {
		for k := range checkKinds {
			kinds = append(kinds, k)
		}
	}
	for _, k := range kinds {
		for _, e := range checkKinds[k] {
			exts[e] = true
		}
	}
	var files []string
	seen := map[string]bool{}
	for _, root := range roots {
		filepath.WalkDir(filepath.Clean(root), func(p string, d fs.DirEntry, err error) error {
			if j.Cancel.Load() {
				return filepath.SkipAll
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.ToLower(d.Name())
			if d.IsDir() {
				if p != root && (alwaysSkip[name] || systemSkip[name] || isLink(p, d)) {
					return filepath.SkipDir
				}
				j.Current.Store(p)
				return nil
			}
			if d.Type().IsRegular() && exts[strings.ToLower(filepath.Ext(name))] && !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				files = append(files, p)
				j.Total.Add(1)
			}
			return nil
		})
	}
	var mu sync.Mutex
	var out []Broken
	work := make(chan string)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range work {
				if j.Cancel.Load() {
					continue
				}
				j.Current.Store(p)
				level, reason := checkFile(p)
				j.Count.Add(1)
				if level == "" {
					continue
				}
				fi, err := os.Stat(p)
				if err != nil {
					continue
				}
				mu.Lock()
				out = append(out, Broken{Path: p, Size: fi.Size(), Mod: fi.ModTime(), Level: level, Reason: reason})
				mu.Unlock()
			}
		}()
	}
	for _, f := range files {
		work <- f
	}
	close(work)
	wg.Wait()
	sort.Slice(out, func(a, b int) bool {
		if out[a].Level != out[b].Level {
			return out[a].Level == "kapot"
		}
		return out[a].Path < out[b].Path
	})
	return out
}

// checkFile returns "" when the file looks fine, otherwise "kapot" or "verdacht" and why.
func checkFile(p string) (string, string) {
	f, err := os.Open(p)
	if err != nil {
		return "", "" // can't read it now (in use); don't call it broken
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", ""
	}
	size := fi.Size()
	if size == 0 {
		return "kapot", "leeg bestand (0 bytes)"
	}
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if allZero(head) {
		return "kapot", "bevat alleen nullen"
	}
	ext := strings.ToLower(filepath.Ext(p))
	tail := make([]byte, 4096)
	tn := 0
	if size > int64(len(tail)) {
		f.ReadAt(tail, size-int64(len(tail)))
		tn = len(tail)
	} else {
		copy(tail, head)
		tn = n
	}
	tail = tail[:tn]

	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif":
		want := map[string][]byte{".jpg": {0xFF, 0xD8, 0xFF}, ".jpeg": {0xFF, 0xD8, 0xFF}, ".png": []byte("\x89PNG"), ".gif": []byte("GIF8")}[ext]
		if !bytes.HasPrefix(head, want) {
			if !looksLikeImage(head) {
				return "kapot", "geen geldige afbeelding (begin van bestand klopt niet)"
			}
		}
		if size > 200<<20 {
			return "", "" // too big to decode safely; header is fine
		}
		f.Seek(0, io.SeekStart)
		cfg, _, err := image.DecodeConfig(f)
		if err != nil {
			return "kapot", "kan niet geopend worden (" + shortErr(err) + ")"
		}
		if cfg.Width*cfg.Height > 120_000_000 {
			return "", "" // enormous; skip the full decode
		}
		f.Seek(0, io.SeekStart)
		img, _, err := image.Decode(f)
		if err != nil {
			return "kapot", "afbeelding is beschadigd of afgekapt (" + shortErr(err) + ")"
		}
		if flatBottom(img) {
			return "verdacht", "onderkant is één egale kleur (vaak half hersteld)"
		}
		if (ext == ".jpg" || ext == ".jpeg") && !bytes.Contains(tail, []byte{0xFF, 0xD9}) {
			return "verdacht", "einde van de foto ontbreekt"
		}
	case ".bmp":
		if len(head) < 26 || string(head[:2]) != "BM" {
			return "kapot", "geen geldige BMP"
		}
		if declared := int64(binary.LittleEndian.Uint32(head[2:6])); declared > size {
			return "kapot", fmt.Sprintf("afgekapt (%d van %d bytes)", size, declared)
		}
	case ".tif", ".tiff":
		if !bytes.HasPrefix(head, []byte("II*\x00")) && !bytes.HasPrefix(head, []byte("MM\x00*")) {
			return "kapot", "geen geldige TIFF"
		}
	case ".webp":
		if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != "WEBP" {
			return "kapot", "geen geldige WEBP"
		}
		if declared := int64(binary.LittleEndian.Uint32(head[4:8])) + 8; declared > size {
			return "kapot", "afgekapt"
		}
	case ".pdf":
		if !bytes.Contains(head[:min(len(head), 1024)], []byte("%PDF-")) {
			return "kapot", "geen geldige PDF (begin klopt niet)"
		}
		if !bytes.Contains(tail, []byte("%%EOF")) {
			return "kapot", "PDF is afgekapt (einde ontbreekt)"
		}
	case ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".zip":
		if !bytes.HasPrefix(head, []byte("PK")) {
			return "kapot", "geen geldig document/zip (begin klopt niet)"
		}
		z, err := zip.OpenReader(p)
		if err != nil {
			return "kapot", "document is beschadigd (" + shortErr(err) + ")"
		}
		defer z.Close()
		for _, e := range z.File {
			if e.UncompressedSize64 > 512<<20 {
				continue
			}
			rc, err := e.Open()
			if err != nil {
				return "kapot", "onderdeel kan niet gelezen worden: " + e.Name
			}
			_, err = io.Copy(io.Discard, rc)
			rc.Close()
			if err != nil {
				return "kapot", "onderdeel beschadigd: " + e.Name
			}
		}
	case ".mp4", ".mov", ".m4a":
		if len(head) < 12 || (string(head[4:8]) != "ftyp" && string(head[4:8]) != "moov" && string(head[4:8]) != "mdat" &&
			string(head[4:8]) != "wide" && string(head[4:8]) != "free") {
			return "kapot", "geen geldige video/audio (begin klopt niet)"
		}
	case ".avi", ".wav":
		want := map[string]string{".avi": "AVI ", ".wav": "WAVE"}[ext]
		if len(head) < 12 || string(head[:4]) != "RIFF" || string(head[8:12]) != want {
			return "kapot", "begin van bestand klopt niet"
		}
		if declared := int64(binary.LittleEndian.Uint32(head[4:8])) + 8; declared > size+1 {
			return "verdacht", "korter dan het bestand zelf aangeeft (afgekapt)"
		}
	case ".mp3":
		if !bytes.HasPrefix(head, []byte("ID3")) && !(len(head) > 1 && head[0] == 0xFF && head[1]&0xE0 == 0xE0) {
			// Some files start with a little padding; look a bit further.
			if bytes.Index(head[:min(len(head), 8192)], []byte{0xFF, 0xFB}) < 0 && bytes.Index(head[:min(len(head), 8192)], []byte{0xFF, 0xF3}) < 0 {
				return "kapot", "geen geldige MP3 (begin klopt niet)"
			}
		}
	}
	return "", ""
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func looksLikeImage(b []byte) bool {
	return bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}) || bytes.HasPrefix(b, []byte("\x89PNG")) || bytes.HasPrefix(b, []byte("GIF8"))
}

func shortErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "unexpected EOF") {
		return "afgekapt"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// flatBottom: the lower part of the picture is one flat colour while the top is not. Half-recovered
// photos often look like this (grey or green below a certain line).
func flatBottom(img image.Image) bool {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 32 || h < 32 {
		return false
	}
	stepX := max(1, w/64)
	variance := func(y0, y1 int) (float64, uint32, uint32, uint32) {
		var n, sr, sg, sb, qr, qg, qb float64
		var r0, g0, b0 uint32
		for y := y0; y < y1; y += max(1, (y1-y0)/16) {
			for x := b.Min.X; x < b.Max.X; x += stepX {
				r, g, bb, _ := img.At(x, y).RGBA()
				r0, g0, b0 = r>>8, g>>8, bb>>8
				fr, fg, fb := float64(r>>8), float64(g>>8), float64(bb>>8)
				n++
				sr += fr
				sg += fg
				sb += fb
				qr += fr * fr
				qg += fg * fg
				qb += fb * fb
			}
		}
		if n == 0 {
			return 0, 0, 0, 0
		}
		v := (qr/n - (sr/n)*(sr/n)) + (qg/n - (sg/n)*(sg/n)) + (qb/n - (sb/n)*(sb/n))
		return v, r0, g0, b0
	}
	bottom, _, _, _ := variance(b.Min.Y+h*85/100, b.Max.Y)
	top, _, _, _ := variance(b.Min.Y, b.Min.Y+h*50/100)
	return bottom < 2 && top > 50
}

// moveBroken puts files in the "_Beschadigd (nakijken)" folder at the root of their drive, side by
// side so they are easy to look through, and notes where each one came from.
func moveBroken(paths []string) []RemoveResult {
	var out []RemoveResult
	for _, p := range paths {
		r := RemoveResult{Path: p}
		dir := filepath.Join(rootOf(p), brokenName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.Err = err.Error()
			out = append(out, r)
			continue
		}
		base := filepath.Base(p)
		dest := filepath.Join(dir, base)
		for i := 2; ; i++ {
			if _, err := os.Stat(dest); err != nil {
				break
			}
			ext := filepath.Ext(base)
			dest = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(base, ext), i, ext))
		}
		if err := moveFile(p, dest); err != nil {
			r.Err = err.Error()
		} else {
			r.OK, r.How = true, dir
			if f, err := os.OpenFile(filepath.Join(dir, "herkomst.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, "%s  <=  %s\r\n", filepath.Base(dest), p)
				f.Close()
			}
			logLine("naar " + brokenName + ": " + p)
		}
		out = append(out, r)
	}
	return out
}
