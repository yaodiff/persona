//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

// Restart delays, as the Linux units have them (RestartSec): persona-link 5 s, the node 10 s.
var linkRestart, nodeRestart = 5 * time.Second, 10 * time.Second

// supervise is `persona-link supervise [--no-node]`, what the scheduled task runs (winservice.go), from the
// console-less copy persona-link-service.exe. It keeps persona-link (`run --log`) and, unless --no-node, the resident
// node (`node --attach --supervised`) running: each started with CREATE_NO_WINDOW, so it has a console but no window,
// and started again a few seconds after it exits, however it exits, or at once when it asks to be (an update, below).
// --supervised tells the node that this restarts it, which is what lets the hub update it (nodeupdate.Options): with no
// exec on Windows, the node ends with nodeproto.RestartExitCode once the update or a rollback put another file in place.
// Both are in a job that kills them when the supervisor ends:
// ending the task (install, uninstall, signing out) ends them too, and a supervisor the task starts again never finds
// the old ones still running. It writes what it does to link-service.log, persona-link's own errors included; the
// node's log goes to node-attach.log, as on Linux.
func supervise(e *env, args []string) error {
	withNode := true
	for _, a := range args {
		if a != "--no-node" {
			return errUsage
		}
		withNode = false
	}
	root := e.personaDir()
	release, err := link.LockRun(filepath.Join(root, "link-service")) // one supervisor per computer
	if err != nil {
		return err
	}
	defer release()
	slog, err := openRotating(e.serviceLog(), logMax)
	if err != nil {
		return err
	}
	defer slog.Close()
	logger := log.New(slog, "", log.LstdFlags)
	logger.Printf("supervisor %s started (pid %d)", version, os.Getpid())
	if err := killWithMe(); err != nil {
		logger.Printf("job object: %v (what it starts may outlive it)", err)
	}
	vars := append(os.Environ(), "PERSONA_HOME="+root)
	ctx := context.Background()
	var wg sync.WaitGroup
	// The node first: persona-link up before it, the hub would open a stream to a node not listening yet, call this
	// computer's node offline and show it so for a moment.
	if withNode {
		nlog, err := openRotating(e.nodeLog(), nodeLogMax)
		if err != nil {
			return err
		}
		defer nlog.Close()
		began := time.Now()
		wg.Add(1)
		go func() {
			defer wg.Done()
			keepRunning(ctx, logger, "node", nodeRestart, func() *exec.Cmd {
				cmd := exec.Command(e.nodeBin(), "node", "--attach", "--supervised")
				cmd.Env, cmd.Stderr = vars, nlog
				return cmd
			})
		}()
		if !waitListening(link.AttachSocket(), began, nodeListenWait) {
			logger.Printf("node has not made %s within %s; starting persona-link anyway", link.AttachSocket(), nodeListenWait)
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		keepRunning(ctx, logger, "persona-link", linkRestart, func() *exec.Cmd {
			cmd := exec.Command(e.binPath(), "run", "--log", e.logPath())
			cmd.Env, cmd.Stderr = vars, slog // its log goes to link.log; what reaches stderr is a crash
			return cmd
		})
	}()
	wg.Wait() // never: the task ends the supervisor
	return nil
}

// nodeListenWait: how long persona-link waits for a node that is starting (it listens within a second here).
const nodeListenWait = 10 * time.Second

// waitListening waits, up to timeout, for a socket file made at or after began: the node removes a leftover one and
// listens anew when it starts. The file is only looked at, never dialled, because a connection to the node's attach
// socket is an attach: it would take the node from the hub.
func waitListening(sock string, began time.Time, timeout time.Duration) bool {
	for deadline := time.Now().Add(timeout); ; time.Sleep(100 * time.Millisecond) {
		if fi, err := os.Stat(sock); err == nil && !fi.ModTime().Before(began.Add(-time.Second)) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
	}
}

// keepRunning starts what mk makes, waits for it to exit and starts it again delay later, until ctx ends (the
// supervisor's never does). One that exits with nodeproto.RestartExitCode is started again at once, unless the run
// before it also asked within delay of starting: a program that keeps asking still waits delay between runs. Its
// stdout goes nowhere: persona-link's status lines are for the menu bar app.
func keepRunning(ctx context.Context, logger *log.Logger, name string, delay time.Duration, mk func() *exec.Cmd) {
	quick := false // the last run asked to start again within delay of starting
	for ctx.Err() == nil {
		cmd := mk()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
		began := time.Now()
		err := cmd.Start()
		if err == nil {
			logger.Printf("%s started (pid %d)", name, cmd.Process.Pid)
			err = cmd.Wait()
		}
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == nodeproto.RestartExitCode && !quick {
			quick = time.Since(began) < delay
			logger.Printf("%s ended after %s asking to start again: starting it now", name, time.Since(began).Round(time.Second))
			continue
		}
		quick = false
		logger.Printf("%s ended after %s: %v; starting it again in %s", name, time.Since(began).Round(time.Second), err, delay)
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}
}

// killWithMe puts this process in a job that kills every process in it when the last handle to it closes, which is
// when this process ends; what it starts from then on is in the job too.
func killWithMe() error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return err
	}
	if err := windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(job)
		return err
	}
	return nil // the handle stays open until this process ends
}
