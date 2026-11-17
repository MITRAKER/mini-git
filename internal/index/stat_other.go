//go:build !linux && !darwin

package index

import "os"

// fillStat on other systems (such as Windows) only has the modification time.
func fillStat(e *Entry, fi os.FileInfo) { fillPortable(e, fi) }
