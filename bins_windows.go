//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	shQueryRecycleBin = shell32.NewProc("SHQueryRecycleBinW")
	shEmptyRecycleBin = shell32.NewProc("SHEmptyRecycleBinW")
	shellExecute      = shell32.NewProc("ShellExecuteW")
)

type shQueryRBInfo struct {
	cbSize   uint32
	_        uint32
	size     int64
	numItems int64
}

func currentSID() string {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return ""
	}
	defer t.Close()
	u, err := t.GetTokenUser()
	if err != nil {
		return ""
	}
	s, _ := u.User.Sid.String()
	return s
}

// folderSize adds up a folder; -1 when Windows doesn't let us look inside.
func folderSize(p string) int64 {
	var total int64
	denied := false
	filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			denied = true
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	if denied && total == 0 {
		return -1
	}
	return total
}

func listBins() []BinInfo {
	me := strings.ToUpper(currentSID())
	var out []BinInfo
	for _, d := range drives() {
		if d.Kind == "Netwerk" {
			continue
		}
		b := BinInfo{Root: d.Root, Label: d.Label, Kind: d.Kind}
		rp, _ := syscall.UTF16PtrFromString(d.Root)
		info := shQueryRBInfo{cbSize: uint32(unsafe.Sizeof(shQueryRBInfo{}))}
		if r, _, _ := shQueryRecycleBin.Call(uintptr(unsafe.Pointer(rp)), uintptr(unsafe.Pointer(&info))); r == 0 {
			b.MineSize, b.MineCount = info.size, info.numItems
		}
		// Bins of other users and of earlier Windows installations (common on a recovered disk).
		for _, binDir := range []string{"$Recycle.Bin", "RECYCLER", "Recycled"} {
			base := filepath.Join(d.Root, binDir)
			list, err := os.ReadDir(base)
			if err != nil {
				continue
			}
			for _, e := range list {
				if !e.IsDir() || strings.ToUpper(e.Name()) == me {
					continue
				}
				p := filepath.Join(base, e.Name())
				b.Others = append(b.Others, FolderInfo{Path: p, Size: folderSize(p)})
			}
		}
		for _, name := range []string{quarantineName, brokenName, ".SchijfSync-prullenbak"} {
			p := filepath.Join(d.Root, name)
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				b.Folders = append(b.Folders, FolderInfo{Path: p, Size: folderSize(p)})
			}
		}
		out = append(out, b)
	}
	return out
}

func emptyMyBin(root string) error {
	rp, _ := syscall.UTF16PtrFromString(root)
	r, _, _ := shEmptyRecycleBin.Call(0, uintptr(unsafe.Pointer(rp)), 0x1|0x2|0x4) // no confirmation, no progress, no sound
	if r != 0 && r != 0x8000FFFF {                                                 // E_UNEXPECTED = already empty
		return errors.New("Windows kon de prullenbak niet legen")
	}
	return nil
}

// emptyOtherBins removes old bins that belong to other (or former) Windows users. That needs
// administrator rights: Windows asks for permission once.
func emptyOtherBins(paths []string) error {
	var parts []string
	for _, p := range paths {
		q := `"` + p + `"`
		parts = append(parts, "rd /s /q "+q+" 2>nul & if exist "+q+" (takeown /f "+q+" /r /d J >nul 2>&1 & takeown /f "+q+
			" /r /d Y >nul 2>&1 & icacls "+q+" /grant *S-1-5-32-544:F /t /c /q >nul 2>&1 & rd /s /q "+q+" 2>nul)")
	}
	if len(parts) == 0 {
		return nil
	}
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString("cmd.exe")
	args, _ := syscall.UTF16PtrFromString("/c " + strings.Join(parts, " & "))
	r, _, _ := shellExecute.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(args)), 0, 0)
	if r <= 32 {
		return errors.New("geen toestemming gekregen")
	}
	return nil
}
