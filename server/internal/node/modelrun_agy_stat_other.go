//go:build windows

package node

import "os"

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
	return agyFileStamp{
		exists: true,
		size:   info.Size(),
		mtime:  info.ModTime().UnixNano(),
	}
}
