package hub

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/procinfo"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

const residentFixture = `package main
import ("fmt"; "os"; "os/exec"; "os/signal"; "path/filepath"; "runtime"; "syscall")
func main() {
	const version = %q
	const program = %q
	const build = %d
	switch os.Args[1] {
	case "version":
		fmt.Printf("%%s\nchannel release, build %%d\n", version, build)
	case "node-update-check":
		fmt.Printf("{\"version\":%%q,\"program\":%%q,\"os\":%%q,\"arch\":%%q,\"resident_handover\":true}\n", version, program, runtime.GOOS, runtime.GOARCH)
	default:
		role := os.Args[1]
		dir := os.Getenv("PERSONA_HOME")
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		if role == "node" {
			exe, _ := os.Executable()
			cmd := exec.Command(exe, "child")
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
			if err := cmd.Start(); err != nil { panic(err) }
		}
		os.WriteFile(filepath.Join(dir, role+".ready"), []byte(fmt.Sprint(os.Getpid())), 0600)
		for range hup {
			f, err := os.OpenFile(filepath.Join(dir, role+".hup"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
			if err != nil { panic(err) }
			f.WriteString("HUP\n")
			f.Close()
		}
	}
}
`

func TestConfirmedUpdateRequestsOneManagedResidentHandover(t *testing.T) {
	for _, mode := range []string{"legacy-ExecStart", "split-node"} {
		t.Run(mode, func(t *testing.T) {
			root, config := t.TempDir(), t.TempDir()
			dir := filepath.Join(root, "bin")
			units := filepath.Join(config, "systemd", "user")
			for _, p := range []string{dir, units} {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PERSONA_HOME", root)
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("PERSONA_NODE_BINARIES", filepath.Join(dir, "nodes"))
			const build = 202611270123
			installed := "personad"
			unit := "personad-node.service"
			if mode == "split-node" {
				installed, unit = "persona-node", "persona-node.service"
			}
			old := updatetest.ProgramSource(t, installed, "", "", fmt.Sprintf(residentFixture, "v1", installed, build-1))
			new := updatetest.ProgramSource(t, installed, "", "", fmt.Sprintf(residentFixture, "v2", installed, build))
			target := filepath.Join(dir, installed)
			if err := update.InstallPrepared(target, old); err != nil {
				t.Fatal(err)
			}
			start := target
			if mode == "legacy-ExecStart" {
				start, unit = filepath.Join(dir, compat.DaemonBinary), compat.HubService+"-node.service"
				if err := os.Symlink("personad", start); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(units, unit), []byte(fmt.Sprintf("[Service]\nExecStart=%q node --attach\n", start)), 0o644); err != nil {
				t.Fatal(err)
			}
			// A managed drop-in resets the old ExecStart.
			dropin := filepath.Join(units, unit+".d")
			if err := os.Mkdir(dropin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dropin, "90-persona-compat.conf"), []byte(fmt.Sprintf("[Service]\nExecStart=\nExecStart=%q node --attach\n", start)), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(start, "node", "--attach")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			waitFor(t, "fixture main and child", func() bool {
				_, a := os.Stat(filepath.Join(root, "node.ready"))
				_, b := os.Stat(filepath.Join(root, "child.ready"))
				return a == nil && b == nil
			})
			systemctl := filepath.Join(t.TempDir(), "systemctl")
			script := fmt.Sprintf("#!/bin/sh\n[ \"${1:-}\" = --user ] || exit 1\n[ \"$8\" = %q ] || exit 1\nprintf 'ActiveState=active\\nMainPID=%d\\n'\n", unit, cmd.Process.Pid)
			if err := os.WriteFile(systemctl, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", filepath.Dir(systemctl)+string(os.PathListSeparator)+os.Getenv("PATH"))
			service := systemServices()
			if got := activeMainPID(t.Context(), true, unit); got != cmd.Process.Pid {
				t.Fatalf("fixture MainPID = %d, want %d", got, cmd.Process.Pid)
			}
			if err := syncLocalNode(t.Context(), root, service, &update.Manifest{Version: "v1", Build: build - 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "node.hup")); !os.IsNotExist(err) {
				t.Fatalf("same inode was signaled: %v", err)
			}

			h := newTestHub(t)
			h.version = "v2"
			h.build = update.Build{Version: h.version, Number: build}
			h.cfg.AutoUpdateNodes = true
			h.upd.services = service
			p := update.Pending{FromVersion: "v1", ToVersion: "v2", ToBuild: build, Target: target, Started: time.Now()}
			if mode == "split-node" {
				p.Nodes = t.TempDir()
				pkg := filepath.Join(p.Nodes, update.PackageName(installed, "linux-"+runtime.GOARCH))
				if err := update.InstallPrepared(pkg, new); err != nil {
					t.Fatal(err)
				}
			} else if err := update.InstallPrepared(target, new); err != nil {
				t.Fatal(err)
			}
			if err := update.Save(update.PendingPath(h.upd.dir), p); err != nil {
				t.Fatal(err)
			}
			h.upd.confirm(t.Context(), p)
			if exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", cmd.Process.Pid)); !strings.HasSuffix(exe, " (deleted)") {
				t.Fatalf("fixture must still drain on the old inode: %q", exe)
			}
			waitFor(t, "main-pid HUP", func() bool {
				data, _ := os.ReadFile(filepath.Join(root, "node.hup"))
				return string(data) == "HUP\n"
			})
			if err := SyncLocalNode(t.Context(), p.ToVersion, p.ToBuild); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(filepath.Join(root, "node.hup")); string(b) != "HUP\n" {
				t.Fatalf("MainPID received %q", b)
			}
			if _, err := os.Stat(filepath.Join(root, "child.hup")); !os.IsNotExist(err) {
				t.Fatalf("child was signaled: %v", err)
			}
			ready, _ := os.ReadFile(filepath.Join(root, "child.ready"))
			child, _ := strconv.Atoi(string(ready))
			if procinfo.StartMark(child) == "" {
				t.Fatal("the resident's child stopped")
			}
		})
	}
}
