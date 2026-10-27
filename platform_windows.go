//go:build windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	shell32          = syscall.NewLazyDLL("shell32.dll")
	getLogicalDrives = kernel32.NewProc("GetLogicalDrives")
	getDriveType     = kernel32.NewProc("GetDriveTypeW")
	getVolumeInfo    = kernel32.NewProc("GetVolumeInformationW")
	getDiskFree      = kernel32.NewProc("GetDiskFreeSpaceExW")
	shFileOperation  = shell32.NewProc("SHFileOperationW")
)

const (
	driveRemovable = 2
	driveFixed     = 3
	driveRemote    = 4
	driveCDROM     = 5

	attrSystem      = 0x4
	attrReparse     = 0x400
	attrOffline     = 0x1000
	attrRecallOnUse = 0x400000 // OneDrive "online only": reading would download it
)

func appDataDir() string { return filepath.Join(os.Getenv("LOCALAPPDATA"), "DubbelWeg") }

func rootOf(p string) string { return filepath.VolumeName(p) + `\` }

func attrs(path string, d fs.DirEntry) uint32 {
	info, err := d.Info()
	if err != nil {
		return 0
	}
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return a.FileAttributes
	}
	return 0
}

// isLink: junctions and links are skipped, so no folder is searched twice or in a loop.
func isLink(path string, d fs.DirEntry) bool {
	return d.Type()&fs.ModeSymlink != 0 || attrs(path, d)&attrReparse != 0
}

// skipFile: Windows system files, and cloud files that are not on this computer.
func skipFile(info fs.FileInfo) bool {
	if a, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return a.FileAttributes&(attrSystem|attrOffline|attrRecallOnUse) != 0
	}
	return false
}

func driveType(root string) uint32 {
	p, _ := syscall.UTF16PtrFromString(root)
	t, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(p)))
	return uint32(t)
}

func recycleAvailable(path string) bool { return driveType(rootOf(path)) == driveFixed }

type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

// recycle moves one file to the Recycle Bin, without any Windows dialogs.
func recycle(path string) error {
	from, err := syscall.UTF16FromString(path)
	if err != nil {
		return err
	}
	from = append(from, 0) // the list ends with two zeros
	op := shFileOpStruct{
		wFunc:  3, // FO_DELETE
		pFrom:  &from[0],
		fFlags: 0x40 | 0x10 | 0x4 | 0x400 | 0x200, // ALLOWUNDO NOCONFIRMATION SILENT NOERRORUI NOCONFIRMMKDIR
	}
	r, _, _ := shFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	if r != 0 || op.fAnyOperationsAborted != 0 {
		return errors.New("Windows kon het bestand niet naar de Prullenbak verplaatsen (in gebruik?)")
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("het bestand staat er nog (in gebruik?)")
	}
	return nil
}

type Drive struct {
	Root  string `json:"root"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
}

func drives() []Drive {
	mask, _, _ := getLogicalDrives.Call()
	var out []Drive
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := string(rune('A'+i)) + `:\`
		kind := ""
		switch driveType(root) {
		case driveFixed:
			kind = "Vaste schijf"
		case driveRemovable:
			kind = "USB / kaart"
		case driveRemote:
			kind = "Netwerk"
		default:
			continue
		}
		rp, _ := syscall.UTF16PtrFromString(root)
		var free, total, totalFree uint64
		ok, _, _ := getDiskFree.Call(uintptr(unsafe.Pointer(rp)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
		if ok == 0 {
			continue // empty card reader and the like
		}
		name := make([]uint16, 261)
		getVolumeInfo.Call(uintptr(unsafe.Pointer(rp)), uintptr(unsafe.Pointer(&name[0])), 261, 0, 0, 0, 0, 0)
		out = append(out, Drive{Root: root, Label: syscall.UTF16ToString(name), Kind: kind, Total: int64(total), Free: int64(free)})
	}
	return out
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd
}

// pickFolder shows Windows' own folder chooser, on top of everything.
func pickFolder() (string, error) {
	script := `Add-Type -AssemblyName System.Windows.Forms;` +
		`$f=New-Object System.Windows.Forms.Form;$f.TopMost=$true;$f.ShowInTaskbar=$false;$f.Opacity=0;$f.Show();$f.Activate();` +
		`$d=New-Object System.Windows.Forms.FolderBrowserDialog;$d.Description='Kies een map om te doorzoeken';$d.ShowNewFolderButton=$false;` +
		`if($d.ShowDialog($f) -eq 'OK'){[Console]::Out.Write($d.SelectedPath)};$f.Close()`
	out, err := hidden(exec.Command("powershell", "-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script)).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func reveal(path string) error {
	return exec.Command("explorer.exe", "/select,", path).Start()
}

func openFile(path string) error {
	return hidden(exec.Command("cmd", "/c", "start", "", path)).Start()
}

func openFolder(path string) error {
	return exec.Command("explorer.exe", path).Start()
}
