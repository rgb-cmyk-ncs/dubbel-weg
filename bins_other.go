//go:build !windows

package main

func listBins() []BinInfo {
	return []BinInfo{{Root: "/", Label: "test", Kind: "Vaste schijf", MineSize: 3 << 30, MineCount: 1234,
		Others:  []FolderInfo{{Path: `D:\$Recycle.Bin\S-1-5-21-111-222-333-1001`, Size: -1}},
		Folders: []FolderInfo{{Path: "/" + quarantineName, Size: 250 << 20}}}}
}
func emptyMyBin(root string) error        { return nil }
func emptyOtherBins(paths []string) error { return nil }
