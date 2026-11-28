package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// stubLark writes a fake lark-cli: it appends a line to $KB_LARK_MARKER when run, then answers with body and exit
// status.
func stubLark(t *testing.T, body string, exit int) (bin, marker string) {
	t.Helper()
	dir := t.TempDir()
	bin, marker = filepath.Join(dir, "lark-cli"), filepath.Join(dir, "ran")
	script := "#!/bin/sh\necho ran >> \"$KB_LARK_MARKER\"\nprintf '%s\\n' '" + body + "'\nexit " + strconv.Itoa(exit) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KB_LARK_MARKER", marker)
	return bin, marker
}

// The switch decides whether lark-cli is called at all.
func TestKBLarkMinutesSwitch(t *testing.T) {
	needsLarkCLI(t)
	f := newFakeHub()
	s := newKBService(t, f)
	e := kbEng(t, s)
	bin, marker := stubLark(t, `{"ok":true,"data":{"items":[],"has_more":false}}`, 0)

	f.hello.KB.LarkMinutes = &assistantwire.KBLarkMinutes{Enabled: false, Binary: bin, IntervalMinutes: 1}
	s.kbLarkMinutes(context.Background(), e, &kbRounds{})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the disabled import ran lark-cli")
	}

	f.hello.KB.LarkMinutes.Enabled = true
	s.kbLarkMinutes(context.Background(), e, &kbRounds{})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the enabled import did not run lark-cli: %v", err)
	}
}

// The interval: a second round right after one does not run lark-cli again.
func TestKBLarkMinutesRespectsTheInterval(t *testing.T) {
	needsLarkCLI(t)
	f := newFakeHub()
	s := newKBService(t, f)
	e := kbEng(t, s)
	bin, marker := stubLark(t, `{"ok":true,"data":{"items":[],"has_more":false}}`, 0)
	f.hello.KB.LarkMinutes = &assistantwire.KBLarkMinutes{Enabled: true, Binary: bin, IntervalMinutes: 60}

	r := &kbRounds{}
	s.kbLarkMinutes(context.Background(), e, r)
	s.kbLarkMinutes(context.Background(), e, r)
	if raw, err := os.ReadFile(marker); err != nil || len(splitLines(string(raw))) != 1 {
		t.Fatalf("ran %q (%v), want once", raw, err)
	}
	r.lark = time.Time{}
	s.kbLarkMinutes(context.Background(), e, r)
	if raw, _ := os.ReadFile(marker); len(splitLines(string(raw))) != 2 {
		t.Fatalf("ran %q, want twice after the interval passed", raw)
	}
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// A failing lark-cli is logged and the round goes on: nothing is written and no panic escapes.
func TestKBLarkMinutesFailureIsLoggedOnly(t *testing.T) {
	needsLarkCLI(t)
	f := newFakeHub()
	s := newKBService(t, f)
	e := kbEng(t, s)
	bin, marker := stubLark(t, `{"error":{"type":"authentication","subtype":"not_logged_in"}}`, 1)
	f.hello.KB.LarkMinutes = &assistantwire.KBLarkMinutes{Enabled: true, Binary: bin, IntervalMinutes: 1}

	s.kbLarkMinutes(context.Background(), e, &kbRounds{})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("lark-cli was not run: %v", err)
	}
	if n := len(e.Store.Items(kb.KindMeeting)); n != 0 {
		t.Fatalf("%d minutes written by a failing lark-cli", n)
	}
}

// Without the lark module (left out of the build, or switched off as here) the import never runs lark-cli: it logs
// that lark-cli is not part of the build and the round goes on.
func TestKBLarkMinutesWithoutTheLarkModule(t *testing.T) {
	f := newFakeHub()
	s := newKBService(t, f)
	e := kbEng(t, s)
	s.moduleSwitch = moduleswitch.Off("lark")
	var mu sync.Mutex
	var logged []string
	s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	bin, marker := stubLark(t, `{"ok":true,"data":{"items":[],"has_more":false}}`, 0)
	f.hello.KB.LarkMinutes = &assistantwire.KBLarkMinutes{Enabled: true, Binary: bin, IntervalMinutes: 1}

	s.kbLarkMinutes(context.Background(), e, &kbRounds{})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the import ran lark-cli without the lark module")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.ContainsFunc(logged, func(l string) bool { return strings.Contains(l, cliport.ErrUnavailable.Error()) }) {
		t.Fatalf("logged %q, want that lark-cli is not part of this build", logged)
	}
}
