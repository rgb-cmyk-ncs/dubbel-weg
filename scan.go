package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Kinds of files the user can limit the search to.
var kindExt = map[string][]string{
	"muziek": {".mp3", ".wav", ".flac", ".aif", ".aiff", ".m4a", ".aac", ".ogg", ".opus", ".wma", ".alac"},
	"fotos":  {".jpg", ".jpeg", ".png", ".gif", ".bmp", ".tif", ".tiff", ".heic", ".webp", ".raw", ".cr2", ".nef", ".arw", ".dng", ".psd"},
	"video":  {".mp4", ".mov", ".avi", ".mkv", ".wmv", ".m4v", ".mpg", ".mpeg", ".webm", ".3gp"},
	"documenten": {".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".rtf", ".odt", ".dwg", ".dxf", ".skp", ".3dm",
		".indd", ".ai", ".eps", ".zip", ".rar", ".7z"},
}

// Folders that are never searched: Windows' own, and our own quarantine.
var alwaysSkip = map[string]bool{
	"$recycle.bin": true, "system volume information": true, strings.ToLower(quarantineName): true,
	strings.ToLower(brokenName): true, ".schijfsync-prullenbak": true,
	"recycler": true, "$windows.~bt": true, "$windows.~ws": true, "windows.old": true,
}

// Skipped when "skip system folders" is on (the default).
var systemSkip = map[string]bool{
	"windows": true, "program files": true, "program files (x86)": true, "programdata": true,
	"appdata": true, "$sysreset": true, "recovery": true, "perflogs": true, "msocache": true,
	"node_modules": true, ".git": true, "onedrivetemp": true,
}

type Options struct {
	Roots      []string `json:"roots"`
	MinSize    int64    `json:"minSize"`
	Kinds      []string `json:"kinds"` // empty = all files
	SkipSystem bool     `json:"skipSystem"`
}

type File struct {
	Path string    `json:"path"`
	Size int64     `json:"size"`
	Mod  time.Time `json:"mod"`
}

type Group struct {
	Size  int64  `json:"size"`
	Hash  string `json:"hash"`
	Files []File `json:"files"`
}

// Progress is read by the screen while a search runs.
type Progress struct {
	Phase      atomic.Value // string
	Files      atomic.Int64 // files looked at
	Candidates atomic.Int64 // files that share a size with another
	BytesTotal atomic.Int64 // bytes that need reading
	BytesDone  atomic.Int64
	Current    atomic.Value // string
	Skipped    atomic.Int64 // files or folders that could not be read
	Cancelled  atomic.Bool
}

func (p *Progress) phase() string {
	if s, ok := p.Phase.Load().(string); ok {
		return s
	}
	return ""
}

func (p *Progress) current() string {
	if s, ok := p.Current.Load().(string); ok {
		return s
	}
	return ""
}

func wanted(name string, exts map[string]bool) bool {
	if len(exts) == 0 {
		return true
	}
	return exts[strings.ToLower(filepath.Ext(name))]
}

// Scan finds files with exactly the same content. Files are first grouped by size,
// then by a quick fingerprint of their start and end, and only then read in full.
func Scan(opt Options, p *Progress) []Group {
	exts := map[string]bool{}
	for _, k := range opt.Kinds {
		for _, e := range kindExt[k] {
			exts[e] = true
		}
	}
	minSize := opt.MinSize
	if minSize < 1 {
		minSize = 1 // empty files are all "the same" but not worth anything
	}

	// 1. Walk.
	p.Phase.Store("zoeken")
	bySize := map[int64][]File{}
	seen := map[string]bool{}
	for _, root := range opt.Roots {
		root = filepath.Clean(root)
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if p.Cancelled.Load() {
				return filepath.SkipAll
			}
			if err != nil {
				p.Skipped.Add(1)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.ToLower(d.Name())
			if d.IsDir() {
				if path != root && (alwaysSkip[name] || (opt.SkipSystem && systemSkip[name]) || isLink(path, d)) {
					return filepath.SkipDir
				}
				p.Current.Store(path)
				return nil
			}
			if !d.Type().IsRegular() || !wanted(name, exts) {
				return nil
			}
			key := strings.ToLower(path)
			if seen[key] {
				return nil // folders chosen twice, or one inside another
			}
			seen[key] = true
			info, err := d.Info()
			if err != nil {
				p.Skipped.Add(1)
				return nil
			}
			p.Files.Add(1)
			if info.Size() < minSize || skipFile(info) {
				return nil
			}
			bySize[info.Size()] = append(bySize[info.Size()], File{Path: path, Size: info.Size(), Mod: info.ModTime()})
			return nil
		})
	}
	if p.Cancelled.Load() {
		return nil
	}

	// 2. Quick fingerprint (first and last 64 KB) for files that share a size.
	var cands [][]File
	var quickBytes int64
	for size, fl := range bySize {
		if len(fl) > 1 {
			cands = append(cands, fl)
			p.Candidates.Add(int64(len(fl)))
			quickBytes += int64(len(fl)) * min(size, 2*quickChunk)
		}
	}
	p.Phase.Store("vergelijken")
	p.BytesTotal.Store(quickBytes)
	quick := hashAll(cands, p, quickHash)
	if p.Cancelled.Load() {
		return nil
	}

	// 3. Full read, only where the fingerprints match and the file is longer than them.
	var full [][]File
	var fullBytes int64
	var groups []Group
	for _, g := range quick {
		if g.Size <= 2*quickChunk {
			groups = append(groups, g) // the fingerprint already covered the whole file
			continue
		}
		full = append(full, g.Files)
		fullBytes += g.Size * int64(len(g.Files))
	}
	p.Phase.Store("controleren")
	p.BytesDone.Store(0)
	p.BytesTotal.Store(fullBytes)
	groups = append(groups, hashAll(full, p, fullHash)...)
	if p.Cancelled.Load() {
		return nil
	}

	for i := range groups {
		sort.Slice(groups[i].Files, func(a, b int) bool { return groups[i].Files[a].Path < groups[i].Files[b].Path })
	}
	sort.Slice(groups, func(a, b int) bool {
		wa := groups[a].Size * int64(len(groups[a].Files)-1)
		wb := groups[b].Size * int64(len(groups[b].Files)-1)
		if wa != wb {
			return wa > wb
		}
		return groups[a].Files[0].Path < groups[b].Files[0].Path
	})
	p.Phase.Store("klaar")
	return groups
}

const quickChunk = 64 << 10

type hashFunc func(path string, size int64, p *Progress) (string, error)

// hashAll splits each set of same-sized files into groups with equal hashes.
func hashAll(sets [][]File, p *Progress, h hashFunc) []Group {
	type job struct{ set, idx int }
	jobs := make(chan job)
	sums := make([][]string, len(sets))
	for i, s := range sets {
		sums[i] = make([]string, len(s))
	}
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				f := sets[j.set][j.idx]
				if p.Cancelled.Load() {
					continue
				}
				p.Current.Store(f.Path)
				s, err := h(f.Path, f.Size, p)
				if err != nil {
					p.Skipped.Add(1)
					continue
				}
				sums[j.set][j.idx] = s
			}
		}()
	}
	for i, s := range sets {
		for k := range s {
			jobs <- job{i, k}
		}
	}
	close(jobs)
	wg.Wait()

	var out []Group
	for i, s := range sets {
		by := map[string][]File{}
		for k, f := range s {
			if sums[i][k] != "" {
				by[sums[i][k]] = append(by[sums[i][k]], f)
			}
		}
		for sum, fl := range by {
			if len(fl) > 1 {
				out = append(out, Group{Size: fl[0].Size, Hash: sum, Files: fl})
			}
		}
	}
	return out
}

func quickHash(path string, size int64, p *Progress) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, quickChunk)
	if size <= 2*quickChunk {
		b, err := io.ReadAll(io.LimitReader(f, size+1))
		if err != nil {
			return "", err
		}
		if int64(len(b)) != size {
			return "", io.ErrUnexpectedEOF // the file changed while searching
		}
		h.Write(b)
		p.BytesDone.Add(size)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	if _, err := io.ReadFull(f, buf); err != nil {
		return "", err
	}
	h.Write(buf)
	if _, err := f.Seek(-quickChunk, io.SeekEnd); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(f, buf); err != nil {
		return "", err
	}
	h.Write(buf)
	p.BytesDone.Add(2 * quickChunk)
	return hex.EncodeToString(h.Sum(nil)), nil
}

type countingWriter struct {
	p *Progress
}

func (c countingWriter) Write(b []byte) (int, error) {
	c.p.BytesDone.Add(int64(len(b)))
	return len(b), nil
}

func fullHash(path string, size int64, p *Progress) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(h, countingWriter{p}), cancelReader{f, p}, make([]byte, 1<<20))
	if err != nil {
		return "", err
	}
	if n != size {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cancelReader stops reading a big file as soon as the search is cancelled.
type cancelReader struct {
	r io.Reader
	p *Progress
}

func (c cancelReader) Read(b []byte) (int, error) {
	if c.p.Cancelled.Load() {
		return 0, io.ErrUnexpectedEOF
	}
	return c.r.Read(b)
}
