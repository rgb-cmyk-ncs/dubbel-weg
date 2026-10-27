//go:build !windows

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) string {
	d := t.TempDir()
	big := bytes.Repeat([]byte("abcdefgh"), 100_000) // 800 KB: needs the full check
	same := append([]byte{}, big...)
	diffMiddle := append([]byte{}, big...)
	diffMiddle[400_000] = 'X' // same size, same start and end, different middle
	write(t, filepath.Join(d, "a", "song.mp3"), big)
	write(t, filepath.Join(d, "b", "song copy.mp3"), same)
	write(t, filepath.Join(d, "c", "other.mp3"), diffMiddle)
	write(t, filepath.Join(d, "a", "photo.jpg"), []byte("small photo data"))
	write(t, filepath.Join(d, "b", "photo (1).jpg"), []byte("small photo data"))
	write(t, filepath.Join(d, "b", "photo (2).jpg"), []byte("small photo data"))
	write(t, filepath.Join(d, "c", "unique.txt"), []byte("only one"))
	write(t, filepath.Join(d, "a", "empty1.txt"), nil)
	write(t, filepath.Join(d, "b", "empty2.txt"), nil)
	write(t, filepath.Join(d, "AppData", "cache.jpg"), []byte("small photo data"))
	write(t, filepath.Join(d, quarantineName, "old.jpg"), []byte("small photo data"))
	return d
}

func TestFindsOnlyRealDuplicates(t *testing.T) {
	d := setup(t)
	g := Scan(Options{Roots: []string{d}, SkipSystem: true}, &Progress{})
	if len(g) != 2 {
		for _, x := range g {
			t.Log(x.Size, x.Files)
		}
		t.Fatalf("want 2 groups, got %d", len(g))
	}
	// Biggest waste first: the 800 KB song.
	if g[0].Size != 800_000 || len(g[0].Files) != 2 {
		t.Fatalf("first group wrong: %+v", g[0])
	}
	for _, f := range g[0].Files {
		if strings.Contains(f.Path, "other.mp3") {
			t.Fatal("different middle must not count as duplicate")
		}
	}
	if len(g[1].Files) != 3 {
		t.Fatalf("photos: want 3 copies (AppData and quarantine skipped), got %d", len(g[1].Files))
	}
}

func TestSystemFoldersOptional(t *testing.T) {
	d := setup(t)
	g := Scan(Options{Roots: []string{d}, SkipSystem: false}, &Progress{})
	for _, x := range g {
		if x.Size == 16 && len(x.Files) != 4 {
			t.Fatalf("with system folders: want 4 photos, got %d", len(x.Files))
		}
	}
}

func TestKindsAndMinSize(t *testing.T) {
	d := setup(t)
	g := Scan(Options{Roots: []string{d}, Kinds: []string{"muziek"}, SkipSystem: true}, &Progress{})
	if len(g) != 1 || !strings.HasSuffix(g[0].Files[0].Path, ".mp3") {
		t.Fatalf("music only: %+v", g)
	}
	g = Scan(Options{Roots: []string{d}, MinSize: 1000, SkipSystem: true}, &Progress{})
	if len(g) != 1 {
		t.Fatalf("min size: want 1 group, got %d", len(g))
	}
}

func TestOverlappingRootsCountOnce(t *testing.T) {
	d := setup(t)
	g := Scan(Options{Roots: []string{d, filepath.Join(d, "b")}, SkipSystem: true}, &Progress{})
	if len(g) != 2 || len(g[1].Files) != 3 {
		t.Fatalf("overlapping roots counted twice: %+v", g)
	}
}

func TestRemoveKeepsOneAndQuarantines(t *testing.T) {
	d := setup(t)
	testRoot = d
	g := Scan(Options{Roots: []string{d}, SkipSystem: true}, &Progress{})
	photos := g[1]
	// Removing every copy is refused.
	var all []string
	for _, f := range photos.Files {
		all = append(all, f.Path)
	}
	if _, err := Remove(g, all); err == nil {
		t.Fatal("removing all copies must be refused")
	}
	// A file outside the results is refused.
	if _, err := Remove(g, []string{filepath.Join(d, "c", "unique.txt")}); err == nil {
		t.Fatal("file outside results must be refused")
	}
	// Two of three go.
	res, err := Remove(g, all[1:])
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.OK {
			t.Fatalf("not removed: %+v", r)
		}
		if _, err := os.Stat(r.Path); err == nil {
			t.Fatal("file still there")
		}
		rel, _ := filepath.Rel(d, r.Path)
		if _, err := os.Stat(filepath.Join(d, quarantineName, rel)); err != nil {
			t.Fatal("not in quarantine with its folder path:", err)
		}
	}
	if _, err := os.Stat(all[0]); err != nil {
		t.Fatal("the kept copy is gone!")
	}
}

func TestChangedFileIsLeftAlone(t *testing.T) {
	d := setup(t)
	testRoot = d
	g := Scan(Options{Roots: []string{d}, SkipSystem: true}, &Progress{})
	p := g[1].Files[1].Path
	os.WriteFile(p, []byte("changed after the search!"), 0o644)
	res, err := Remove(g, []string{p})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].OK {
		t.Fatal("a changed file must not be removed")
	}
}

func TestCancel(t *testing.T) {
	d := setup(t)
	p := &Progress{}
	p.Cancelled.Store(true)
	if g := Scan(Options{Roots: []string{d}}, p); g != nil {
		t.Fatal("cancelled search must return nothing")
	}
}
