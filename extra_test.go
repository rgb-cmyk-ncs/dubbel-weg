//go:build !windows

package main

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func noisy(w, h int, flatFrom int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y >= flatFrom {
				img.Set(x, y, color.RGBA{128, 128, 128, 255})
			} else {
				img.Set(x, y, color.RGBA{uint8(x * 7 % 256), uint8(y * 13 % 256), uint8((x + y) * 5 % 256), 255})
			}
		}
	}
	return img
}

func TestBrokenFiles(t *testing.T) {
	d := t.TempDir()
	var good, flat bytes.Buffer
	jpeg.Encode(&good, noisy(300, 200, 999), nil)
	jpeg.Encode(&flat, noisy(300, 200, 120), nil)
	write(t, filepath.Join(d, "goed.jpg"), good.Bytes())
	write(t, filepath.Join(d, "afgekapt.jpg"), good.Bytes()[:good.Len()/2])
	write(t, filepath.Join(d, "nullen.jpg"), make([]byte, 5000))
	write(t, filepath.Join(d, "leeg.png"), nil)
	write(t, filepath.Join(d, "half-grijs.jpg"), flat.Bytes())
	write(t, filepath.Join(d, "random.jpg"), bytes.Repeat([]byte("garbage!"), 900))
	var pngb bytes.Buffer
	png.Encode(&pngb, noisy(50, 50, 999))
	write(t, filepath.Join(d, "goed.png"), pngb.Bytes())
	write(t, filepath.Join(d, "goed.pdf"), []byte("%PDF-1.4\n...stuff...\n%%EOF\n"))
	write(t, filepath.Join(d, "afgekapt.pdf"), []byte("%PDF-1.4\n...stuff without end"))
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	f, _ := zw.Create("word/document.xml")
	f.Write(bytes.Repeat([]byte("<w:p>hallo</w:p>"), 500))
	zw.Close()
	write(t, filepath.Join(d, "goed.docx"), zb.Bytes())
	write(t, filepath.Join(d, "kapot.docx"), zb.Bytes()[:zb.Len()-30])

	j := &Job{}
	got := map[string]string{}
	for _, b := range ScanBroken([]string{d}, nil, j) {
		got[filepath.Base(b.Path)] = b.Level
	}
	want := map[string]string{"afgekapt.jpg": "kapot", "nullen.jpg": "kapot", "leeg.png": "kapot", "half-grijs.jpg": "verdacht",
		"random.jpg": "kapot", "afgekapt.pdf": "kapot", "kapot.docx": "kapot"}
	for name, lvl := range want {
		if got[name] != lvl {
			t.Errorf("%s: got %q want %q", name, got[name], lvl)
		}
	}
	for _, name := range []string{"goed.jpg", "goed.png", "goed.pdf", "goed.docx"} {
		if got[name] != "" {
			t.Errorf("%s is fine but reported as %q", name, got[name])
		}
	}

	// Move them apart: flat, with a note of where they came from.
	testRoot = d
	res := moveBroken([]string{filepath.Join(d, "afgekapt.jpg")})
	if !res[0].OK {
		t.Fatal(res[0].Err)
	}
	if _, err := os.Stat(filepath.Join(d, brokenName, "afgekapt.jpg")); err != nil {
		t.Fatal("not moved apart")
	}
	if b, _ := os.ReadFile(filepath.Join(d, brokenName, "herkomst.txt")); !bytes.Contains(b, []byte("afgekapt.jpg")) {
		t.Fatal("no note of the origin")
	}
	// The folder is not searched again.
	j2 := &Job{}
	for _, b := range ScanBroken([]string{d}, nil, j2) {
		if filepath.Dir(b.Path) == filepath.Join(d, brokenName) {
			t.Fatal("the review folder should be skipped")
		}
	}
}

func TestMergeFolders(t *testing.T) {
	d := t.TempDir()
	testRoot = d
	from, to := filepath.Join(d, "Foto's oud"), filepath.Join(d, "Foto's")
	write(t, filepath.Join(from, "2019", "a.jpg"), []byte("AAA"))
	write(t, filepath.Join(to, "2019", "a.jpg"), []byte("AAA"))         // same: not needed twice
	write(t, filepath.Join(from, "2019", "b.jpg"), []byte("only here")) // new
	write(t, filepath.Join(from, "c.txt"), []byte("newer text"))
	write(t, filepath.Join(to, "c.txt"), []byte("older"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(to, "c.txt"), old, old)

	if err := checkMergeFolders(from, filepath.Join(from, "sub")); err == nil {
		t.Fatal("target inside source must be refused")
	}
	plan, err := PlanMerge(from, to, &Job{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.New != 1 || plan.Same != 1 || plan.Diff != 1 {
		t.Fatalf("plan wrong: %+v", plan)
	}
	res := DoMerge(plan, "beide", &Job{})
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors)
	}
	if b, _ := os.ReadFile(filepath.Join(to, "2019", "b.jpg")); string(b) != "only here" {
		t.Fatal("new file not moved")
	}
	if b, _ := os.ReadFile(filepath.Join(to, "c (2).txt")); string(b) != "newer text" {
		t.Fatal("keep both: incoming should be renamed (2)")
	}
	if b, _ := os.ReadFile(filepath.Join(to, "c.txt")); string(b) != "older" {
		t.Fatal("existing file must stay")
	}
	if _, err := os.Stat(from); err == nil {
		t.Fatal("empty source folder should be gone")
	}
	if _, err := os.Stat(filepath.Join(d, quarantineName)); err != nil {
		t.Fatal("the identical copy should be kept in quarantine, not deleted")
	}
}

func TestMergeKeepNewest(t *testing.T) {
	d := t.TempDir()
	testRoot = d
	from, to := filepath.Join(d, "A"), filepath.Join(d, "B")
	write(t, filepath.Join(from, "x.txt"), []byte("new version"))
	write(t, filepath.Join(to, "x.txt"), []byte("old"))
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(to, "x.txt"), old, old)
	plan, _ := PlanMerge(from, to, &Job{})
	DoMerge(plan, "nieuwste", &Job{})
	if b, _ := os.ReadFile(filepath.Join(to, "x.txt")); string(b) != "new version" {
		t.Fatal("newest should win")
	}
}
