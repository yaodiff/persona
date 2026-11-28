//go:build !windows

package node

import (
	"os"
	"syscall"
)

type agyFileStamp struct {
	exists bool
	size   int64
	mtime  int64
	dev    uint64
	ino    uint64
}

func statAgyFileStamp(path string) agyFileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return agyFileStamp{}
	}
	stamp := agyFileStamp{
		exists: true,
		size:   info.Size(),
		mtime:  info.ModTime().UnixNano(),
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.dev = uint64(st.Dev)
		stamp.ino = st.Ino
	}
	return stamp
}
