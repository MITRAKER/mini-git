package repo

import (
	"fmt"
	"os"
	"path/filepath"
)

// maxEmptyFolders bounds how many folders RemoveEmptyTree visits (coding
// standard, rule 2).
const maxEmptyFolders = 1 << 20

// RemoveEmptyTree removes the folder root if it holds nothing but empty
// folders, and those folders with it. If it finds anything else inside, a
// file or a symbolic link, it removes nothing and returns false.
//
// Git never tracks an empty folder, so such folders are left over from
// files or refs that were deleted. They can stand where a new file or ref
// must go, and then Git removes them the same way.
func RemoveEmptyTree(root string) (bool, error) {
	var dirs []string
	stack := []string{root}
	for visited := 0; len(stack) > 0; visited++ {
		if visited >= maxEmptyFolders {
			return false, fmt.Errorf("more than %d folders under %s", maxEmptyFolders, root)
		}
		dir := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dirs = append(dirs, dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false, err
		}
		for _, de := range entries {
			if !de.IsDir() { // a symbolic link to a folder is not a folder here
				return false, nil
			}
			stack = append(stack, filepath.Join(dir, de.Name()))
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- { // each folder was listed after its parent
		if err := os.Remove(dirs[i]); err != nil {
			return false, err
		}
	}
	return true, nil
}
