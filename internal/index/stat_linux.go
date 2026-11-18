package index

import (
	"os"
	"syscall"
)

// fillStat copies the stat fields Git records. Linux names the timestamps
// Ctim and Mtim. Their fields are int32 on 32-bit systems and int64 on 64-bit
// ones, hence the conversions.
//
//nolint:unconvert // the int64 conversions are needed on 32-bit platforms
func fillStat(e *Entry, fi os.FileInfo) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		fillPortable(e, fi)
		return
	}
	e.CtimeSec, e.CtimeNsec = low32(int64(st.Ctim.Sec)), low32(int64(st.Ctim.Nsec))
	e.MtimeSec, e.MtimeNsec = low32(int64(st.Mtim.Sec)), low32(int64(st.Mtim.Nsec))
	e.Dev, e.Ino = low32u(uint64(st.Dev)), low32u(uint64(st.Ino))
	e.UID, e.GID = st.Uid, st.Gid
}
