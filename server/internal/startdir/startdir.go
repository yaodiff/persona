// Package startdir keeps a folder that does not answer from holding Persona's commands before they start (#14400).
//
// On macOS, Go's os package asks for the working directory while it initializes, before main
// (os/executable_darwin.go: var initCwd, initCwdErr = Getwd()). Getwd stats "." and $PWD (or calls getcwd) and makes
// each call again for as long as it fails with EINTR. A folder whose every call stalls and then fails with EINTR, such
// as a protected folder while the system service that checks access to it is stuck, therefore holds a program started
// in it before any code of the program runs, and none of its own timeouts can end the wait: `personad tool` printed
// nothing until its caller killed it.
//
// This package runs before package os. Go initializes a program's packages in the order of their import paths, each
// after the packages it imports (the Go specification, "Package initialization"). This one imports only packages that
// os imports itself, and its path sorts before "os". On macOS it asks the working directory and the folder $PWD names
// to answer within wait; when neither does, it moves the process to "/" and sets PWD to "/" so that os can start, and
// keeps the folder it left (Left). Package settle (internal/startdir/settle) runs once os has started and decides where
// the command runs: a `tool` call goes on from "/", every other command goes back to its folder.
//
// Nothing here may import os, directly or through another package: TestStartsBeforeOS checks the order.
package startdir

import (
	"syscall"
	"time"
)

// wait is how long the folder gets to answer: far longer than a working folder takes (a stat takes microseconds, a
// disk waking up a second or so), and far shorter than a stalled one, whose calls fail only after many seconds.
const wait = 2 * time.Second

// left is the folder this process started in, when that folder did not answer at start.
var left string

// Left is the folder this process started in when that folder did not answer at start: the process then runs from
// "/", with PWD set to "/". It is "" when the folder answered, off macOS, and when $PWD named no folder to go back to.
func Left() string { return left }

func init() {
	if answers == nil { // os's start asks for no folder here
		return
	}
	pwd, _ := syscall.Getenv("PWD")
	left = check(answers, pwd, wait, syscall.Chdir, syscall.Setenv)
}

// check asks "." and pwd to answer (answers) within wait. When neither does, it moves the process to "/" (chdir) and
// sets PWD to "/" (setenv), so that os's Getwd answers at once, and returns pwd, the folder it left. It moves nothing
// and returns "" when "." answers (the folder works), when pwd does (it names another folder, so it is no way back to
// this one: os's start then waits for the folder as it always did), and without an absolute pwd, for the same reason.
func check(answers func(path string) bool, pwd string, wait time.Duration, chdir func(string) error,
	setenv func(key, value string) error) string {
	if len(pwd) == 0 || pwd[0] != '/' {
		return ""
	}
	dot, named := ask(answers, "."), ask(answers, pwd)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for dot != nil || named != nil {
		select {
		case ok := <-dot:
			if ok {
				return ""
			}
			dot = nil
		case ok := <-named:
			if ok {
				return ""
			}
			named = nil
		case <-timer.C:
			dot, named = nil, nil
		}
	}
	if chdir("/") != nil {
		return ""
	}
	_ = setenv("PWD", "/") // a valid name and value: it does not fail
	return pwd
}

// ask runs answers(path) in a goroutine of its own and gives its answer on the channel. A call that hangs is left to
// end by itself: nothing waits for it.
func ask(answers func(path string) bool, path string) <-chan bool {
	c := make(chan bool, 1)
	go func() { c <- answers(path) }()
	return c
}
