package index

import (
	"os"
	"syscall"
)

// fillStat copies the stat fields Git records. Linux names the timestamps
// Ctim and Mtim.
func fillStat(e *Entry, fi os.FileInfo) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		fillPortable(e, fi)
		return
	}
	e.CtimeSec, e.CtimeNsec = uint32(st.Ctim.Sec), uint32(st.Ctim.Nsec)
	e.MtimeSec, e.MtimeNsec = uint32(st.Mtim.Sec), uint32(st.Mtim.Nsec)
	e.Dev, e.Ino = uint32(st.Dev), uint32(st.Ino)
	e.UID, e.GID = st.Uid, st.Gid
}
