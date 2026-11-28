package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentProgressAcceptsOnlySafeFields(t *testing.T) {
	input := `{"phase":"building","step":"go_build","step_index":5,"step_total":6,"item_label":"personad (linux-amd64)","item_done":0,"item_total":4,"percent":0,"extra":"/private/fixture"}
{"phase":"building","step":"go_build","step_index":5,"step_total":6,"item_label":"personad (/private/fixture)"}
{"phase":"building","step":"private-fixture","step_index":5,"step_total":6}
{"phase":"backing_up","step":"go_build","step_index":5,"step_total":6}
{"phase":"building","step":"go_build","step_index":5,"step_total":6,"bytes_done":-1}
{"phase":"building","step":"go_build","step_index":5,"step_total":6,"percent":101}
child process output
{"phase":"building","step":"manifest","step_index":6,"step_total":6,"elapsed_seconds":42}`
	var events []DevelopmentProgress
	readDevelopmentProgress(strings.NewReader(input), "build", func(p DevelopmentProgress) {
		events = append(events, p)
	})
	if len(events) != 2 || events[0].ItemLabel != "personad (linux-amd64)" || events[0].Percent == nil || *events[0].Percent != 0 ||
		events[1].Step != "manifest" || events[1].ElapsedSeconds != 42 {
		t.Fatalf("wrong events: %+v", events)
	}
	encoded, err := json.Marshal(events)
	if err != nil || strings.Contains(string(encoded), "private-fixture") || strings.Contains(string(encoded), "/private/fixture") {
		t.Fatal("unrecognized fields were exposed", err)
	}
}

func TestDevelopmentRequestStreamsBackupWhileHelperRuns(t *testing.T) {
	python, err := exec.LookPath("python3")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("development helper needs Python on Unix")
	}
	dir := t.TempDir()
	tools := t.TempDir()
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	dumped := filepath.Join(dir, "continue")
	t.Setenv("FIXTURE_DUMP_CONTINUE", dumped)
	const private = "fixture-private-diagnostic"
	for name, source := range map[string]string{
		"pg_dump": `import os, pathlib, sys, time
print("fixture-private-diagnostic", file=sys.stderr)
print('{"phase":"backing_up","step":"archive_programs","step_index":4,"step_total":5,"bytes_done":999999}')
pathlib.Path(sys.argv[-1]).write_bytes(b"PGDMP" + bytes(1024))
deadline = time.monotonic() + 5
while not pathlib.Path(os.environ["FIXTURE_DUMP_CONTINUE"]).exists():
    if time.monotonic() >= deadline:
        sys.exit("no live bytes received")
    time.sleep(0.02)
`,
		"pg_restore": "print('fixture dump inventory')\n",
	} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!"+python+"\n"+source), 0700); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(`{"database_url":"fixture-db"}`), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "personad")
	if err := os.WriteFile(binary, []byte("pinned binary"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var saved DevelopmentBackup
	var events []DevelopmentProgress
	var gateErr, resumeErr error
	resume := func() {
		// Packaging must finish from the pinned file after its live name goes away.
		resumeErr = os.Remove(binary)
	}
	err = DevelopmentRequest(ctx, &DevelopmentConfig{Backup: DevelopmentBackupConfig{Builtin: true}}, dir, "backup",
		map[string]any{"runtime": home, "candidate": DevelopmentCandidate{Commit: strings.Repeat("a", 40)},
			"platform": "linux-amd64", "executables": map[string]string{"personad": binary}},
		&saved, resume, func(p DevelopmentProgress) {
			events = append(events, p)
			if p.Step == "database_dump" && p.ItemDone != nil && *p.ItemDone == 0 &&
				p.BytesDone != nil && *p.BytesDone == 1029 && p.BytesPerSecond != nil && *p.BytesPerSecond > 0 {
				gateErr = os.WriteFile(dumped, nil, 0600)
			}
		})
	if resumeErr != nil {
		t.Fatal(resumeErr)
	}
	if err != nil || gateErr != nil || saved.ID == "" || saved.Retained != 1 {
		t.Fatalf("backup result %+v, helper %v, gate %v", saved, err, gateErr)
	}
	var steps []string
	for _, p := range events {
		steps = append(steps, p.Step)
		if p.Steps[p.StepIndex-1] != p.Step {
			t.Fatalf("step order differs from progress: %+v", p)
		}
	}
	if got := slices.Compact(steps); !slices.Equal(got, []string{"preflight", "quiesce", "database_dump", "archive_files", "resume_writers", "archive_programs", "verify_snapshot"}) {
		t.Fatalf("backup steps: %v", got)
	}
	if _, err := os.Stat(dumped); err != nil {
		t.Fatal("did not receive live dump bytes", err)
	}
	encoded, err := json.Marshal(events)
	for _, value := range []string{private, home, binary, "999999", "fixture-db"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("private command output reached the progress pipe")
		}
	}
	log, logErr := os.ReadFile(filepath.Join(dir, "backup.log"))
	if err != nil || logErr != nil || !strings.Contains(string(log), private) {
		t.Fatalf("missing private diagnostics: %v, %v", err, logErr)
	}
}

func TestDevelopmentBackupFailureRotation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("development helper needs Unix")
	}
	dir, home := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(`{"database_url":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &DevelopmentConfig{Backup: DevelopmentBackupConfig{Builtin: true, Directory: "relative"}}
	input := map[string]any{"runtime": home}
	run := func(reason string) {
		t.Helper()
		var steps []string
		err := DevelopmentRequest(context.Background(), cfg, dir, "backup", input, &DevelopmentBackup{}, nil, func(p DevelopmentProgress) {
			steps = append(steps, p.Step)
		})
		var failure backupFailure
		if !errors.As(err, &failure) || failure.Reason != reason || strings.Join(steps, ",") != "preflight" {
			t.Fatalf("unexpected failure or steps: reason %q, want %q; steps %v; err %v", failure.Reason, reason, steps, err)
		}
	}
	run("directory")
	old, err := os.ReadFile(filepath.Join(dir, "backup.log"))
	if err != nil || !strings.Contains(string(old), "invalid backup directory") {
		t.Fatal("missing first failure", err)
	}
	cfg.Backup.Directory = ""
	// Rejected before libpq is loaded (a NUL byte), so the case is the same on a host without libpq; the secret must
	// still stay out of the log.
	input["hub_database"] = "password=fixture-secret\x00invalid-key=private"
	run("database")
	previous, err := os.ReadFile(filepath.Join(dir, "backup.prev.log"))
	if err != nil || string(previous) != string(old) {
		t.Fatal("previous log was not preserved", err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "backup.log"))
	if err != nil || !strings.Contains(string(current), "invalid database connection") || strings.Contains(string(current), "fixture-secret") {
		t.Fatal("incorrect current log", err)
	}
}

func TestDevelopmentWebRecoveryPreservesConflictingOverride(t *testing.T) {
	dir := t.TempDir()
	p := Pending{RecoveryReady: true, Web: filepath.Join(dir, "web"), WebPrev: filepath.Join(dir, "previous")}
	for _, path := range []string{p.Web, p.WebPrev} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "index.html"), []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RestoreAll(p); err == nil {
		t.Fatal("overwrote a changed override")
	}
	for _, path := range []string{p.Web, p.WebPrev} {
		if got, err := os.ReadFile(filepath.Join(path, "index.html")); err != nil || string(got) != path {
			t.Fatal("changed Web recovery contents", err)
		}
	}
}

func TestDevelopmentWatchRetainsJournalUntilRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"build":2,"update":{"confirmed":false}}`))
	}))
	defer srv.Close()
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "restarted", true: "restart failed"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "personad")
			if err := os.WriteFile(target, []byte("old program"), 0700); err != nil {
				t.Fatal(err)
			}
			p := Pending{FromBuild: 1, ToBuild: 2, Target: target, HealthURL: srv.URL,
				Development: &DevelopmentCandidate{Commit: strings.Repeat("a", 40)}}
			if err := BeginDevelopmentAdoption(dir, p); err != nil {
				t.Fatal(err)
			}
			if err := PrepareDevelopmentAdoption(dir, &p); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("new program"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := Save(PendingPath(dir), p); err != nil {
				t.Fatal(err)
			}
			journal, err := os.ReadFile(DevelopmentAdoptionPath(dir))
			if err != nil {
				t.Fatal(err)
			}
			restartFailure := errors.New("restored program could not start")
			restarted := false
			err = Watch(context.Background(), srv.Client(), p, PendingPath(dir), time.Now(), time.Millisecond,
				func() error { return RestoreAll(p) }, func() error {
					restarted = true
					if got, err := os.ReadFile(target); err != nil || string(got) != "old program" {
						t.Fatal("restart did not receive the restored program", err)
					}
					if got, err := os.ReadFile(DevelopmentAdoptionPath(dir)); err != nil || string(got) != string(journal) {
						t.Error("recovery journal was removed before restart", err)
					}
					if failed {
						return restartFailure
					}
					return nil
				})
			if !restarted || (failed && !errors.Is(err, restartFailure)) || (!failed && err != nil) {
				t.Fatal("unexpected restart outcome", err)
			}
			got, readErr := os.ReadFile(DevelopmentAdoptionPath(dir))
			if failed {
				if readErr != nil || string(got) != string(journal) {
					t.Fatal("failed restart lost recovery journal", readErr)
				}
				// The retained preimage remains reusable by the next Hub boot.
				if err := RestoreAll(p); err != nil {
					t.Fatal("recovery could not retry", err)
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatal("successful restart retained the journal", readErr)
			}
		})
	}
}

func TestDevelopmentAdoptionFenceMatchesExactAttempt(t *testing.T) {
	dir := t.TempDir()
	p := Pending{ToBuild: 202611130059, Development: &DevelopmentCandidate{Commit: strings.Repeat("a", 40)}}
	if err := BeginDevelopmentAdoption(dir, p); err != nil {
		t.Fatal(err)
	}
	if err := BeginDevelopmentAdoption(dir, p); err == nil {
		t.Fatal("replayed unknown adoption")
	}
	other := p
	other.ToBuild++
	ClearDevelopmentAdoption(dir, other)
	if _, err := os.Stat(DevelopmentAdoptionPath(dir)); err != nil {
		t.Fatal("another attempt cleared the fence")
	}
	ClearDevelopmentAdoption(dir, p)
	if _, err := os.Stat(DevelopmentAdoptionPath(dir)); !os.IsNotExist(err) {
		t.Fatal("matching fence retained", err)
	}
	if got := DevelopmentNumber(p.ToBuild); got <= p.ToBuild {
		t.Fatal("non-monotonic identity", got)
	}
}

func TestDevelopmentBackupConfigCoordinatedRequiresHooksForExtras(t *testing.T) {
	cases := []struct {
		name        string
		cfg         DevelopmentBackupConfig
		coordinated bool
		external    bool
	}{
		{"builtin default", DevelopmentBackupConfig{}, true, false},
		{"external hooks", DevelopmentBackupConfig{Quiesce: []string{"p"}, Resume: []string{"r"}}, true, true},
		{"only quiesce", DevelopmentBackupConfig{Quiesce: []string{"p"}}, false, false},
		{"only resume", DevelopmentBackupConfig{Resume: []string{"r"}}, false, false},
		{"extra database without hooks", DevelopmentBackupConfig{ExtraDatabases: []string{"postgres://localhost/extra"}}, false, false},
		{"extra root without hooks", DevelopmentBackupConfig{ExtraRoots: []string{"/srv/extra"}}, false, false},
		{"extra database with hooks", DevelopmentBackupConfig{Quiesce: []string{"p"}, Resume: []string{"r"}, ExtraDatabases: []string{"postgres://localhost/extra"}}, true, true},
		{"extra root with hooks", DevelopmentBackupConfig{Quiesce: []string{"p"}, Resume: []string{"r"}, ExtraRoots: []string{"/srv/extra"}}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Coordinated(); got != tc.coordinated {
				t.Fatalf("Coordinated() = %v, want %v", got, tc.coordinated)
			}
			if got := tc.cfg.ExternalQuiesce(); got != tc.external {
				t.Fatalf("ExternalQuiesce() = %v, want %v", got, tc.external)
			}
		})
	}
}

func TestCancelDevelopmentBuildReapsProcessGroupAndRemovesWork(t *testing.T) {
	python, err := exec.LookPath("python3")
	if runtime.GOOS == "windows" || err != nil {
		t.Skip("development helper needs Python on Unix")
	}
	dir, bin := t.TempDir(), t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FIXTURE_SIGNALS", bin)
	// The grandchild exits only on SIGTERM. Its parent waits for the child
	// before exiting so the test observes both group delivery and reaping.
	git := `import os, pathlib, signal, time
root = pathlib.Path(os.environ["FIXTURE_SIGNALS"])
signal.signal(signal.SIGINT, lambda *_: (root / ("int-" + str(os.getpid()))).touch())
child = os.fork()
if child:
    signal.signal(signal.SIGTERM, lambda *_: None)
    os.waitpid(child, 0)
else:
    signal.signal(signal.SIGTERM, lambda *_: exit(0))
    (root / "ready").write_text(str(os.getpid()))
    while True:
        signal.pause()
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!"+python+"\n"+git), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- DevelopmentRequest(ctx, &DevelopmentConfig{}, dir, "build", map[string]any{
			"candidate": DevelopmentCandidate{Commit: strings.Repeat("a", 40)},
		}, &struct{}{}, nil, nil)
	}()
	for {
		if _, err := os.Stat(filepath.Join(bin, "ready")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("helper exited before launching its child: %v", err)
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("child group did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not reap the helper")
	}
	signals, _ := filepath.Glob(filepath.Join(bin, "int-*"))
	work, _ := filepath.Glob(filepath.Join(dir, "build-*"))
	if len(signals) != 2 || len(work) != 0 {
		t.Fatalf("signal recipients %v, residual work %v", signals, work)
	}
}
