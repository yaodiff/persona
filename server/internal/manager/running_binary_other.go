//go:build !linux

package manager

import "os"

func runningBinaryInfo(path string) (os.FileInfo, error) { return os.Stat(path) }

func launcherBinary() (string, os.FileInfo) { return "", nil }
