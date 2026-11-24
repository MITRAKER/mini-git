package index

import (
	"os"
	"syscall"
)

// fillStat copies the stat fields Git records. macOS names the timestamps
// Ctimespec and Mtimespec.
func fillStat(e *Entry, fi os.FileInfo) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		fillPortable(e, fi)
		return
	}
	e.CtimeSec, e.CtimeNsec = low32(st.Ctimespec.Sec), low32(st.Ctimespec.Nsec)
	e.MtimeSec, e.MtimeNsec = low32(st.Mtimespec.Sec), low32(st.Mtimespec.Nsec)
	e.Dev, e.Ino = low32(int64(st.Dev)), low32u(st.Ino)
	e.UID, e.GID = st.Uid, st.Gid
}
