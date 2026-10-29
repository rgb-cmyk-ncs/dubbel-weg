package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type FolderInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"` // -1 = protected, size unknown
}

type BinInfo struct {
	Root      string       `json:"root"`
	Label     string       `json:"label"`
	Kind      string       `json:"kind"`
	MineSize  int64        `json:"mineSize"`
	MineCount int64        `json:"mineCount"`
	Others    []FolderInfo `json:"others"`
	Folders   []FolderInfo `json:"folders"`
}

// emptyOwnFolder removes one of our own collection folders (or SchijfSync's bin) at a drive root.
func emptyOwnFolder(p string) error {
	name := filepath.Base(p)
	if name != quarantineName && name != brokenName && name != ".SchijfSync-prullenbak" {
		return errors.New("dat is geen map die DubbelWeg mag legen")
	}
	if filepath.Clean(filepath.Dir(p)) != filepath.Clean(rootOf(p)) {
		return errors.New("alleen mappen in de hoofdmap van een schijf")
	}
	logLine("map leeggemaakt: " + p)
	return os.RemoveAll(p)
}

func isOtherBin(p string) bool {
	low := strings.ToLower(p)
	return strings.Contains(low, `\$recycle.bin\`) || strings.Contains(low, `\recycler\`) || strings.Contains(low, `\recycled\`)
}
