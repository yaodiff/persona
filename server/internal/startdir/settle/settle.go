// Package settle decides where a command runs when its folder did not answer at start, and package startdir moved it
// to "/" so that Go's os package could start (#14400).
//
// A `personad tool` call goes on from "/": none of its calls reads its folder, so it answers, or fails within its own
// deadline, instead of waiting for the folder. Every other command goes back to its folder, waiting for the folder to
// answer as Go's start would have waited, and runs there as before; so does `tool secret_store`, whose manifest and
// field files can be named relative to the folder.
//
// It runs soon after os has started, before main and before most packages: besides os it imports only packages os
// imports, so Go initializes it as soon as os is and nothing ready sorts before it. Package nodecmd imports it, so every
// program with the device's commands (personad, persona-node, persona-manager) has both packages.
package settle

import (
	"errors"
	"os"
	"syscall"

	"github.com/nwverse/persona/server/internal/startdir"
)

func init() {
	if err := settle(os.Args, startdir.Left(), os.Chdir, os.Setenv); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

// goesOn: the command line (os.Args) is a `tool` call that reads nothing from its folder.
func goesOn(args []string) bool {
	return len(args) > 1 && args[1] == "tool" && (len(args) < 3 || args[2] != "secret_store")
}

// settle takes a command whose folder did not answer at start (left) back to it, unless it goes on without it
// (goesOn). Going back waits for the folder: a change of directory that fails with EINTR is made again for as long as
// it fails, as os's start did with its calls.
func settle(args []string, left string, chdir func(dir string) error, setenv func(key, value string) error) error {
	if left == "" || goesOn(args) {
		return nil
	}
	err := chdir(left)
	for errors.Is(err, syscall.EINTR) {
		err = chdir(left)
	}
	if err != nil {
		return errors.New(program(args) + ": the folder it was started in (" + left + ") did not answer then, and cannot be opened now: " + err.Error())
	}
	return setenv("PWD", left)
}

// program is the program's name, for messages: the last element of args[0].
func program(args []string) string {
	if len(args) == 0 {
		return "persona"
	}
	name := args[0]
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' || name[i] == '\\' {
			return name[i+1:]
		}
	}
	return name
}
