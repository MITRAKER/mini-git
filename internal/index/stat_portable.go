package index

import "os"

// fillPortable records only what every operating system provides.
func fillPortable(e *Entry, fi os.FileInfo) {
	mt := fi.ModTime()
	e.MtimeSec, e.MtimeNsec = uint32(mt.Unix()), uint32(mt.Nanosecond())
	e.CtimeSec, e.CtimeNsec = e.MtimeSec, e.MtimeNsec
}
