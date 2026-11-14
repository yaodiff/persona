package node

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPortsHelper is the child TestScanListenersFindsAChildsServer starts: it serves a page on loopback, prints its
// port and waits until killed.
func TestPortsHelper(t *testing.T) {
	if os.Getenv("PERSONA_PORTS_HELPER") == "" {
		t.Skip("helper process only")
	}
	ln, err := net.Listen("tcp", os.Getenv("PERSONA_PORTS_HELPER"))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("port", ln.Addr().(*net.TCPAddr).Port)
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>Child</title>")
	}))
}

// On Windows the scan finds a server a pane's process started (its child, on IPv4 and on IPv6), from the system's
// tables, with no ps or lsof on PATH; a process outside the pane is not the pane's.
func TestScanListenersFindsAChildsServer(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // nothing to run: the scan must not need a program
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestPortsHelper$")
			cmd.Env = append(os.Environ(), "PERSONA_PORTS_HELPER="+addr)
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			port := 0
			lines := make(chan string, 1)
			go func() {
				sc := bufio.NewScanner(out)
				for sc.Scan() {
					if v, ok := strings.CutPrefix(sc.Text(), "port "); ok {
						lines <- v
						return
					}
				}
			}()
			select {
			case v := <-lines:
				port, _ = strconv.Atoi(v)
			case <-time.After(30 * time.Second):
				t.Fatal("the helper did not listen")
			}
			// The pane: the test process is its shell, the helper its child.
			procs, err := listProcs(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			found, err := scanListeners(context.Background(), procs, map[string][]int{"w1:p1": {1 << 30, os.Getpid()}})
			if err != nil {
				t.Fatal(err)
			}
			ls := found["w1:p1"]
			i := slices.IndexFunc(ls, func(l listener) bool { return l.port == port })
			if i < 0 {
				t.Fatalf("port %d not found in %+v", port, ls)
			}
			if ls[i].pid != cmd.Process.Pid || ls[i].cmd == "" {
				t.Fatalf("listener = %+v, want pid %d", ls[i], cmd.Process.Pid)
			}
			if got := probePort(context.Background(), port, ls[i].addr); !got.http || got.title != "Child" {
				t.Fatalf("probe %+v", got)
			}
			// Another pane, rooted at a sibling of the helper (not its ancestor), does not get it.
			sib := exec.Command(os.Args[0], "-test.run=^TestProcHelper$")
			sib.Env = append(os.Environ(), "PERSONA_PROC_HELPER=1")
			if err := sib.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sib.Process.Kill(); _ = sib.Wait() })
			if procs, err = listProcs(context.Background()); err != nil { // the table again: it has the sibling now
				t.Fatal(err)
			}
			other, err := scanListeners(context.Background(), procs, map[string][]int{"w2:p1": {1 << 30, sib.Process.Pid}})
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(other["w2:p1"], func(l listener) bool { return l.port == port }) {
				t.Fatalf("a sibling's pane got the helper's port: %+v", other["w2:p1"])
			}
		})
	}
}
