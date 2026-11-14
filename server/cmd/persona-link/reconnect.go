package main

import (
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/link"
)

// reconnectNow is the public, narrow command the desktop settings app uses instead of duplicating OS-specific pokes.
// The long-running connector retains its connection, credentials and state; this only asks it to redial.
func reconnectNow(e *env, args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	path := e.configPath()
	pokeRunning(path)
	if !link.Running(path) {
		return errors.New("persona-link is not running; start its service first")
	}
	fmt.Fprintln(e.stdout, "Reconnect requested.")
	return nil
}
