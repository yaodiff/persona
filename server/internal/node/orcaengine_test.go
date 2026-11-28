package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// hubEvents connects n to a hub end that keeps every notification named event, and returns a function that gives
// them as of now: it sends a marker after everything the node sent so far and waits for it, so a notification the
// node did not send is known not to be on its way.
func hubEvents(t *testing.T, n *Node, event string) func() []json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	var mu sync.Mutex
	var got []json.RawMessage
	marks := 0
	hub.OnEvent = func(e string, params json.RawMessage) {
		mu.Lock()
		defer mu.Unlock()
		switch e {
		case event:
			got = append(got, params)
		case "test.mark":
			marks++
		}
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	sent := 0
	return func() []json.RawMessage {
		t.Helper()
		sent++
		if err := n.peer.Notify("test.mark", map[string]any{}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the hub end", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return marks >= sent
		})
		mu.Lock()
		defer mu.Unlock()
		return append([]json.RawMessage(nil), got...)
	}
}

// withOrcaClient points the node's own Orca asks (node.info, the watcher, the doctor) at the fake o.
func withOrcaClient(t *testing.T, o *orcarttest.Fake) {
	t.Helper()
	saved := orcaClient
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: o.MetaPath} }
	t.Cleanup(func() { orcaClient = saved })
}

// orcaRowOf is Orca's row in an engines.changed's params.
func orcaRowOf(t *testing.T, params json.RawMessage) proto.Engine {
	t.Helper()
	var p proto.EnginesChanged
	if err := json.Unmarshal(params, &p); err != nil {
		t.Fatal(err)
	}
	for _, e := range p.Engines {
		if e.ID == proto.EngineOrca {
			return e
		}
	}
	t.Fatalf("no orca row in %s", params)
	return proto.Engine{}
}

// Orca quit and opened again after the node connected reach the hub as engines.changed, with the rest of what
// node.info said; asks that find nothing new tell nothing, and nothing is told before node.info said any engines
// (#17037). The watcher asks on its own every orcaEngineEvery.
func TestOrcaEngineChangeIsToldToTheHub(t *testing.T) {
	n, _ := testNode(t)
	o := orcarttest.New(t)
	withOrcaClient(t, o)
	withOrcaInstalled(t, true, "")
	events := hubEvents(t, n, proto.EventEnginesChanged)
	ctx := context.Background()

	n.checkOrcaEngine(ctx) // before node.info: the hub has no engines to update
	herdrRow := proto.Engine{ID: proto.EngineHerdr, Name: "herdr", Available: true, Version: "0.9.5"}
	n.engRep.said([]proto.Engine{herdrRow, orcaEngine(ctx, orcaClient())})
	n.checkOrcaEngine(ctx) // Orca as node.info said it
	if got := events(); len(got) != 0 {
		t.Fatalf("told with nothing changed: %s", got)
	}

	o.Stop() // Orca quit
	n.checkOrcaEngine(ctx)
	got := events()
	if len(got) != 1 {
		t.Fatalf("Orca quit: told %d times", len(got))
	}
	if e := orcaRowOf(t, got[0]); e.Available || e.State != proto.EngineStopped {
		t.Errorf("Orca quit: %+v", e)
	}
	var p proto.EnginesChanged
	_ = json.Unmarshal(got[0], &p)
	if len(p.Engines) != 2 || p.Engines[0] != herdrRow {
		t.Errorf("the other rows are not node.info's: %+v", p.Engines)
	}
	n.checkOrcaEngine(ctx)
	if got := events(); len(got) != 1 {
		t.Errorf("told again with nothing changed: %d", len(got))
	}

	old := orcaEngineEvery
	orcaEngineEvery = 20 * time.Millisecond
	t.Cleanup(func() { orcaEngineEvery = old })
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	go n.watchOrcaEngine(wctx)
	if err := o.Start(); err != nil { // Orca opened again
		t.Fatal(err)
	}
	waitFor(t, "engines.changed after Orca opened", func() bool { return len(events()) >= 2 })
	stop()
	got = events()
	if e := orcaRowOf(t, got[1]); !e.Available || e.State != proto.EngineRunning {
		t.Errorf("Orca opened: %+v", e)
	}
	if len(got) != 2 {
		t.Errorf("told %d times for two changes", len(got))
	}
}

// A start Orca cannot take is refused engine_missing naming Orca and why (not herdr's words), with the data the hub
// words it by in the reader's language (#17037): not running when it is installed, not installed when it is not, and
// incompatible when it answers with another protocol. Nothing is created.
func TestOrcaStartRefusalNamesOrca(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed bool
		stopped   bool
		status    map[string]any
		reason    string
	}{
		{"not running", true, true, nil, engineNotRunning},
		{"not installed", false, true, nil, engineNotInstalled},
		{"incompatible", true, false, map[string]any{"runtimeProtocolVersion": 2}, engineIncompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _, o, _, home := orcaNode(t)
			withOrcaInstalled(t, tc.installed, "")
			repoDir := mkdir(t, filepath.Join(home, "proj"))
			o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
			if tc.status != nil {
				o.SetStatus(tc.status)
			}
			if tc.stopped {
				o.Stop()
			}
			_, err := request(t, n, "session.start", map[string]any{"cwd": repoDir})
			if !isRefusal(err, engineMissing) {
				t.Fatalf("err %v", err)
			}
			want := i18n.T(i18n.Default, "rpc-errors.engine_"+tc.reason, i18n.Args{"engine": "Orca"})
			var re *rpc.Error
			if !errors.As(err, &re) || re.Message != want {
				t.Errorf("message %q, want %q", err, want)
			}
			var data struct{ Engine, Reason, Detail string }
			_ = json.Unmarshal([]byte(refusalData(t, err)), &data)
			if data.Engine != proto.EngineOrca || data.Reason != tc.reason || !strings.Contains(strings.ToLower(data.Detail), "orca") ||
				strings.Contains(data.Detail, "herdr") {
				t.Errorf("data %+v", data)
			}
			if got := o.Trace("terminal.create"); len(got) != 0 {
				t.Errorf("created %v", got)
			}

			// A start into a workspace it names goes to the terminal directly; it is refused the same way.
			_, err = request(t, n, "session.start", map[string]any{"cwd": repoDir, "workspace_id": "wt-proj"})
			if !isRefusal(err, engineMissing) || !strings.Contains(refusalData(t, err), `"reason":"`+tc.reason+`"`) {
				t.Errorf("into a named workspace: %v", err)
			}
		})
	}
}

// node doctor's orca line: running (ok, with its version), installed and stopped or incompatible (warnings that say
// what to do), and no line at all on a machine without Orca.
func TestDoctorOrcaLine(t *testing.T) {
	for _, tc := range []struct {
		e      proto.Engine
		status string
		key    string
	}{
		{proto.Engine{ID: "orca", Available: true, Version: "1.4.277", State: proto.EngineRunning}, DoctorOK, "node-doctor.orca_ok"},
		{proto.Engine{ID: "orca", State: proto.EngineStopped}, DoctorWarn, "node-doctor.orca_stopped"},
		{proto.Engine{ID: "orca", Version: "1.4.100", State: proto.EngineIncompatible}, DoctorWarn, "node-doctor.orca_incompatible"},
		{proto.Engine{ID: "orca", State: proto.EngineMissing}, "", ""},
		{proto.Engine{ID: "orca"}, "", ""}, // a state this build does not know
	} {
		c, ok := checkOrcaIn(context.Background(), func(context.Context) proto.Engine { return tc.e }, i18n.En)
		if tc.key == "" {
			if ok {
				t.Errorf("%+v: a line %+v for a machine without Orca", tc.e, c)
			}
			continue
		}
		want := i18n.T(i18n.En, tc.key, i18n.Args{"version": tc.e.Version})
		if !ok || c.Name != "orca" || c.Status != tc.status || c.Detail != want || tc.status == DoctorWarn && c.Hint == "" {
			t.Errorf("%+v: %+v, want %s %q", tc.e, c, tc.status, want)
		}
	}
}
