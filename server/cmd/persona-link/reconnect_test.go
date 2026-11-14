package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/link"
)

func TestReconnectRequiresRunningConnector(t *testing.T) {
	e := newFakeEnv(t)
	if err := reconnectNow(e.env, nil); err == nil {
		t.Fatal("an unstarted connector must not look reconnected")
	}
	if err := reconnectNow(e.env, []string{"unexpected"}); err == nil {
		t.Fatal("unexpected arguments must be refused")
	}
}

func TestReconnectUsesExistingPlatformPoke(t *testing.T) {
	e := newFakeEnv(t)
	path := e.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := link.LockRun(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// The lock is enough to test the command's result; no real process or live hub is touched on Windows.
	if err := reconnectNow(e.env, nil); err != nil {
		t.Fatal(err)
	}
	if got := e.out.String(); got != "Reconnect requested.\n" {
		t.Fatalf("result %q", got)
	}
}
