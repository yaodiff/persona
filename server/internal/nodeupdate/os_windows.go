package nodeupdate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/osproc"
)

// exeSuffix is what a file needs at the end of its name to run: CreateProcess (and exec.Command's lookup) will not
// start a file without it, so the staged build and the backup the watchdog runs from carry it too.
const exeSuffix = ".exe"

// retiredPrefix names a program file replaceFile moved aside: removeRetired deletes it once nothing runs from it.
const retiredPrefix = ".node-update-retired-"

// Restart asks the supervisor to start the installed program again: Windows has no exec, so this process exits with
// nodeproto.RestartExitCode, which persona-link's supervisor takes as "start it again now". Only a node run with
// --supervised gets here (New refuses updates otherwise). Like an exec it never returns: deferred cleanup is skipped.
func Restart(string) error {
	os.Exit(nodeproto.RestartExitCode)
	return nil
}

// replaceFile puts src at dst. Windows refuses to replace a file a process runs from, or one anybody holds open
// without FILE_SHARE_DELETE (os.Open never shares delete), but lets it be renamed: the file at dst moves aside to a
// retired name first, and back when src cannot take its place. MOVEFILE_WRITE_THROUGH returns only once the rename is
// on disk, which syncDir cannot do here.
func replaceFile(src, dst string) error {
	aside, err := retiredPath(filepath.Dir(dst))
	if err != nil {
		return err
	}
	if err := moveFile(dst, aside); err != nil {
		return err
	}
	if err := moveFile(src, dst); err != nil {
		_ = moveFile(aside, dst)
		return err
	}
	return nil
}

// retiredPath is a new retired name in dir.
func retiredPath(dir string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return filepath.Join(dir, retiredPrefix+hex.EncodeToString(random[:])), nil
}

// discard deletes path, or, while a process still runs from it (the watchdog runs from the backup it may restore),
// moves it to a retired name for removeRetired to delete once that process is gone.
func discard(path string) {
	if err := os.Remove(path); err == nil || os.IsNotExist(err) {
		return
	}
	if aside, err := retiredPath(filepath.Dir(path)); err == nil {
		_ = moveFile(path, aside)
	}
}

func moveFile(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(f, t, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	return nil
}

// removeRetired deletes the files replaceFile moved aside in dir. One a process still runs from cannot be deleted
// yet and stays for a later call (the next confirmation, recovery or sweep).
func removeRetired(dir string) {
	paths, _ := filepath.Glob(filepath.Join(dir, retiredPrefix+"*"))
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

// syncFile flushes the staged file to disk. FlushFileBuffers needs a handle that may write: one opened for reading
// only gets "access denied".
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncDir does nothing: Windows cannot flush a directory handle opened for reading (FlushFileBuffers says access
// denied). The renames that matter are made durable by MOVEFILE_WRITE_THROUGH (replaceFile) instead.
func syncDir(string) error { return nil }

// processStart is pid's creation time through a process handle, exact to 100 ns (osproc.Identify): Windows has no ps.
func processStart(pid int) (string, error) {
	if id := osproc.Identify(pid); id.Start != "" {
		return id.Start, nil
	}
	return "", fmt.Errorf("no creation time for process %d", pid)
}

// supervisorOf is the process that started this node and its identity: persona-link's supervisor, which starts the
// updated program as a new process (stopRestarted finds that process through it).
func supervisorOf() (int, string) {
	pid := os.Getppid()
	return pid, osproc.Identify(pid).Start
}

// stopRestarted stops the node the supervisor restarted onto the new program, once the watchdog has put the old one
// back at restored. With no exec that node is a new process; a build that can take updates here records its pid at
// startup (recover) and killRecorded stops it, but one that cannot (a build from before Windows updates) never does,
// and would keep running. It stops the recorded supervisor's children that run from the node's file (now moved aside)
// and started between the supervisor and the restore: never another program, never the old node it starts next.
func stopRestarted(p pending, restored time.Time) {
	if p.SupervisorPID < 2 || p.SupervisorStart == "" {
		return
	}
	since, ok := createdAt(p.SupervisorPID, p.SupervisorStart)
	if !ok {
		return // the supervisor is gone (its job took its children with it) or the pid is another process now
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if e.ParentProcessID == uint32(p.SupervisorPID) {
			stopIfRestarted(e.ProcessID, p.Exe, since, restored)
		}
	}
}

// stopIfRestarted terminates pid when it runs from exe, or from a file replaceFile moved aside from there, and started
// after since and before until, checked and terminated through one handle so a reused pid is never hit.
func stopIfRestarted(pid uint32, exe string, since, until time.Time) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	// Where its file is now: moved aside (retired) when the rollback put the old program back.
	if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) != nil {
		return
	}
	image := windows.UTF16ToString(buf[:size])
	if !strings.EqualFold(image, exe) && !(strings.EqualFold(filepath.Dir(image), filepath.Dir(exe)) && strings.HasPrefix(filepath.Base(image), retiredPrefix)) {
		return
	}
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return
	}
	if at := time.Unix(0, created.Nanoseconds()); at.After(since) && at.Before(until) {
		_ = windows.TerminateProcess(h, 1)
	}
}

// createdAt is when pid started, while it is still the process whose identity is start.
func createdAt(pid int, start string) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil || osproc.Identify(pid).Start != start {
		return time.Time{}, false
	}
	return time.Unix(0, created.Nanoseconds()), true
}
