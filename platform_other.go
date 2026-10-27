//go:build !windows

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Only used to test on other systems; the real program is for Windows.

var testRoot = "/"

func appDataDir() string { return filepath.Join(os.TempDir(), "DubbelWeg") }

func rootOf(p string) string { return testRoot }

func isLink(path string, d fs.DirEntry) bool { return d.Type()&fs.ModeSymlink != 0 }

func skipFile(info fs.FileInfo) bool { return false }

func recycleAvailable(path string) bool { return false }

func recycle(path string) error { return errors.New("no recycle bin here") }

type Drive struct {
	Root  string `json:"root"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
}

func drives() []Drive {
	return []Drive{{Root: "/", Label: "test", Kind: "Vaste schijf", Total: 100 << 30, Free: 20 << 30}}
}

func pickFolder() (string, error)  { return os.TempDir(), nil }
func reveal(path string) error     { return nil }
func openFile(path string) error   { return nil }
func openFolder(path string) error { return nil }
