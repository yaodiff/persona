package startdir

import "syscall"

// answers says whether a stat of path came back with anything but EINTR. These are the calls os's start makes (Getwd
// stats "." and $PWD), and it makes one that fails with EINTR again for as long as it fails.
var answers = func(path string) bool {
	var st syscall.Stat_t
	return syscall.Stat(path, &st) != syscall.EINTR
}
