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
	e.CtimeSec, e.CtimeNsec = uint32(st.Ctimespec.Sec), uint32(st.Ctimespec.Nsec)
	e.MtimeSec, e.MtimeNsec = uint32(st.Mtimespec.Sec), uint32(st.Mtimespec.Nsec)
	e.Dev, e.Ino = uint32(st.Dev), uint32(st.Ino)
	e.UID, e.GID = st.Uid, st.Gid
}
