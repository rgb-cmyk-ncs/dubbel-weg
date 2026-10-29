package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Merging folders, the way Windows 7 did it: everything from "Van" goes into "Naar", folder by
// folder. Files that are already there with the same content are not copied twice.

type MergeItem struct {
	Rel    string `json:"rel"`
	Size   int64  `json:"size"`
	Action string `json:"action"` // "nieuw", "gelijk", "anders"
	Newer  string `json:"newer,omitempty"`
}

type MergePlan struct {
	From  string      `json:"from"`
	To    string      `json:"to"`
	Items []MergeItem `json:"items"`
	New   int         `json:"new"`
	Same  int         `json:"same"`
	Diff  int         `json:"diff"`
	Bytes int64       `json:"bytes"`
}

func checkMergeFolders(from, to string) error {
	if from == "" || to == "" {
		return errors.New("kies beide mappen")
	}
	a, b := strings.ToLower(filepath.Clean(from)), strings.ToLower(filepath.Clean(to))
	sep := string(filepath.Separator)
	if a == b {
		return errors.New("dat is twee keer dezelfde map")
	}
	if strings.HasPrefix(b, strings.TrimSuffix(a, sep)+sep) {
		return errors.New("de doelmap ligt in de map die je samenvoegt; kies een andere doelmap")
	}
	if strings.HasPrefix(a, strings.TrimSuffix(b, sep)+sep) {
		return errors.New("de map die je samenvoegt ligt al in de doelmap")
	}
	if fi, err := os.Stat(from); err != nil || !fi.IsDir() {
		return errors.New("de map 'Van' bestaat niet")
	}
	if fi, err := os.Stat(to); err != nil || !fi.IsDir() {
		return errors.New("de map 'Naar' bestaat niet")
	}
	return nil
}

func PlanMerge(from, to string, j *Job) (*MergePlan, error) {
	if err := checkMergeFolders(from, to); err != nil {
		return nil, err
	}
	plan := &MergePlan{From: from, To: to}
	err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if j.Cancel.Load() {
			return filepath.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != from && (alwaysSkip[strings.ToLower(d.Name())] || isLink(p, d)) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(from, p)
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		j.Current.Store(p)
		j.Count.Add(1)
		it := MergeItem{Rel: rel, Size: fi.Size()}
		dst := filepath.Join(to, rel)
		di, err := os.Stat(dst)
		switch {
		case err != nil:
			it.Action = "nieuw"
			plan.New++
			plan.Bytes += fi.Size()
		case di.IsDir():
			it.Action = "anders"
			plan.Diff++
		case di.Size() == fi.Size() && sameContent(p, dst):
			it.Action = "gelijk"
			plan.Same++
		default:
			it.Action = "anders"
			plan.Diff++
			if fi.ModTime().After(di.ModTime()) {
				it.Newer = "van"
			} else {
				it.Newer = "naar"
			}
		}
		plan.Items = append(plan.Items, it)
		return nil
	})
	sort.Slice(plan.Items, func(a, b int) bool { return plan.Items[a].Rel < plan.Items[b].Rel })
	return plan, err
}

func sameContent(a, b string) bool {
	fa, err := os.Open(a)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(b)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return (ea == io.EOF || ea == io.ErrUnexpectedEOF) && (eb == io.EOF || eb == io.ErrUnexpectedEOF)
		}
	}
}

type MergeResult struct {
	Moved   int      `json:"moved"`
	Removed int      `json:"removed"`
	Renamed int      `json:"renamed"`
	Errors  []string `json:"errors"`
	Left    bool     `json:"left"` // the "Van" folder still exists (something could not be moved)
}

// DoMerge carries out a plan. keep = "beide" (keep both, the incoming one gets "(2)") or "nieuwste".
func DoMerge(plan *MergePlan, keep string, j *Job) MergeResult {
	var res MergeResult
	fail := func(rel string, err error) {
		res.Errors = append(res.Errors, rel+": "+err.Error())
		logLine("samenvoegen NIET gelukt (" + err.Error() + "): " + rel)
	}
	for _, it := range plan.Items {
		if j.Cancel.Load() {
			break
		}
		src := filepath.Join(plan.From, it.Rel)
		dst := filepath.Join(plan.To, it.Rel)
		j.Current.Store(src)
		j.Count.Add(1)
		fi, err := os.Stat(src)
		if err != nil || fi.Size() != it.Size {
			fail(it.Rel, errors.New("veranderd sinds het nakijken; overgeslagen"))
			continue
		}
		switch it.Action {
		case "nieuw":
			if _, err := os.Stat(dst); err == nil {
				fail(it.Rel, errors.New("staat inmiddels al in de doelmap; overgeslagen"))
				continue
			}
			if err := moveFile(src, dst); err != nil {
				fail(it.Rel, err)
				continue
			}
			res.Moved++
		case "gelijk":
			if err := discard(src); err != nil {
				fail(it.Rel, err)
				continue
			}
			res.Removed++
		case "anders":
			if keep == "nieuwste" {
				if it.Newer == "van" {
					if err := discard(dst); err != nil {
						fail(it.Rel, err)
						continue
					}
					if err := moveFile(src, dst); err != nil {
						fail(it.Rel, err)
						continue
					}
					res.Moved++
				} else {
					if err := discard(src); err != nil {
						fail(it.Rel, err)
						continue
					}
					res.Removed++
				}
				continue
			}
			ext := filepath.Ext(dst)
			alt := dst
			for i := 2; ; i++ {
				alt = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(dst, ext), i, ext)
				if _, err := os.Stat(alt); err != nil {
					break
				}
			}
			if err := moveFile(src, alt); err != nil {
				fail(it.Rel, err)
				continue
			}
			res.Renamed++
		}
	}
	// Empty folders that are left behind go, including "Van" itself when it is empty.
	var dirs []string
	filepath.WalkDir(plan.From, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i])
	}
	if _, err := os.Stat(plan.From); err == nil {
		res.Left = true
	}
	logLine(fmt.Sprintf("samengevoegd %s -> %s: %d verplaatst, %d dubbel weg, %d hernoemd, %d fouten",
		plan.From, plan.To, res.Moved, res.Removed, res.Renamed, len(res.Errors)))
	return res
}

// discard: to the Recycle Bin on fixed drives, otherwise to the quarantine folder.
func discard(p string) error {
	if recycleAvailable(p) {
		return recycle(p)
	}
	_, err := quarantine(p)
	return err
}

// moveFile moves a file, also to another drive (copy, check, then remove the original).
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	fi, err := in.Stat()
	if err != nil {
		in.Close()
		return err
	}
	tmp := dst + ".dubbelweg-tmp"
	out, err := os.Create(tmp)
	if err != nil {
		in.Close()
		return err
	}
	n, err := io.CopyBuffer(out, in, make([]byte, 1<<20))
	in.Close()
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != fi.Size() {
		err = errors.New("kopie is niet compleet")
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	os.Chtimes(tmp, fi.ModTime(), fi.ModTime())
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}
