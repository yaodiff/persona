package managerconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Move is MoveExcept with nothing dropped.
func Move(hubDir, file string) (Report, error) { return MoveExcept(hubDir, file, nil) }

// oldHubJSON is a hub.json of the layout before manager.json: the hub's own keys and every manager section.
const oldHubJSON = `{
  "listen": ":18660",
  "token": "hub-token",
  "database_url": "postgres://u:p@[fd00::20]:5432/persona",
  "machines": [{"id": "server", "kind": "local"}],
  "assistant": {"owner": "Example", "notes": "- Code is in ~/code", "machine": "server", "launcher": "claude",
    "external": false, "persona_view": true, "memory_dreaming": true, "complete_agentless_todos": true,
    "auto_approve_reversible_settings": true, "supervision_batch_minutes": 45, "task_execution_wait_seconds": 45,
    "task_execution_legacy_fallback": false, "node_update_handoff_grace_seconds": 30},
  "agents": {"triage": {"max_retries": 2}},
  "kb": {"enabled": false, "text_days": 30},
  "ai": {"base_url": "https://gateway.example.com", "model": "m"},
  "inbox": {"rules": "mine", "triage_ai": {"model": "t"}},
  "hook_timeout_minutes": 45,
  "notify": {"done_delay_seconds": 30},
  "lark": {"account": 2},
  "lark_gate": [{"method": "POST", "path": "/x", "action": "allow"}],
  "connector_gate": [],
  "connector_auto_confirm": [],
  "github": {"proxy": "http://[fd00::20]:3128"},
  "imessage": {"machine": "mac"},
  "google": {"client_id": "example.apps.googleusercontent.com"},
  "microsoft": {"client_id": "00000000-0000-0000-0000-000000000000"},
  "bots": {"feishu": {"app_id": "cli_example"}},
  "extensions": [{"id": "example"}],
  "plugins": [{"id": "older"}],
  "trusted_extension_markets": ["https://market.example.com"],
  "extension_market": {"proxy": "socks5://[fd00::20]:1080"},
  "extension_runtime": {"path": "/runtime/deno", "version": "2.9.6", "sha256": "example-digest"},
  "ui_page_sessions": {"principal": 4, "extension": 32, "runtime": 128},
  "usage": {"sources": [{"id": "relay", "key": "secret-key"}]},
  "sent_ledger_days": 30,
  "sent_recall_hours": 48
}
`

func readObject(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// An old-layout home: the first start moves every manager section into manager.json, once, with a marker and a copy
// of those sections (not the hub's token or database password); hub.json is not changed; a second start moves
// nothing and writes nothing.
func TestMoveTakesTheManagerSectionsOnce(t *testing.T) {
	home := t.TempDir()
	hubPath := filepath.Join(home, "hub.json")
	if err := os.WriteFile(hubPath, []byte(oldHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	file := DefaultFile(home)
	rep, err := Move(home, file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Moved, Moved) {
		t.Fatalf("moved %v, want every manager section %v", rep.Moved, Moved)
	}
	hubObj, _ := ParseObject([]byte(oldHubJSON))
	mineRaw, _ := os.ReadFile(file)
	mineObj, _ := ParseObject(mineRaw)
	for _, k := range Moved {
		want, _ := getPath(hubObj, k)
		got, ok := getPath(mineObj, k)
		if !ok || compact(t, got) != compact(t, want) {
			t.Errorf("manager.json's %s = %s, hub.json had %s", k, got, want)
		}
	}
	mine := readObject(t, file)
	for _, k := range []string{"listen", "token", "database_url", "machines", "notify"} {
		if _, ok := mine[k]; ok {
			t.Errorf("manager.json took the hub's %s", k)
		}
	}
	// assistant is split: the manager's keys moved, the rest (external, the retired persona_view) did not.
	if compact(t, mine["assistant"]) != `{"auto_approve_reversible_settings":true,"complete_agentless_todos":true,"launcher":"claude","machine":"server","memory_dreaming":true,"node_update_handoff_grace_seconds":30,"notes":"- Code is in ~/code","owner":"Example","supervision_batch_minutes":45,"task_execution_legacy_fallback":false,"task_execution_wait_seconds":45}` {
		t.Errorf("manager.json's assistant %s", mine["assistant"])
	}
	if b, _ := os.ReadFile(hubPath); string(b) != oldHubJSON {
		t.Fatalf("hub.json changed:\n%s", b)
	}
	var m Marker
	if err := json.Unmarshal(mine[MarkerKey], &m); err != nil || !slices.Equal(m.Keys, Moved) || m.At.IsZero() {
		t.Fatalf("marker %s (%v)", mine[MarkerKey], err)
	}
	if filepath.Dir(rep.Backup) != DefaultDir(home) {
		t.Fatalf("backup %s", rep.Backup)
	}
	backupRaw, _ := os.ReadFile(rep.Backup)
	backup, err := ParseObject(backupRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range Moved {
		want, _ := getPath(hubObj, k)
		if got, ok := getPath(backup, k); !ok || compact(t, got) != compact(t, want) {
			t.Errorf("backup's %s = %s, hub.json had %s", k, got, want)
		}
	}
	if string(Only(backup, Moved)) != string(backup.Marshal()) {
		t.Fatalf("the backup holds more than the moved sections: %s", backupRaw)
	}
	if b, _ := os.ReadFile(rep.Backup); strings.Contains(string(b), "hub-token") || strings.Contains(string(b), "postgres://") {
		t.Fatalf("the backup holds the hub's secrets:\n%s", b)
	}
	for _, p := range []string{file, rep.Backup} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, st, err)
		}
	}
	if st, err := os.Stat(DefaultDir(home)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("directory: %v %v", st, err)
	}
	before, _ := os.ReadFile(file)
	stBefore, _ := os.Stat(file)
	// The manager changes its own copy (the admin page's usage sources); the next start keeps it.
	if err := Edit(file, func(o *Object) error { return o.Set("usage", map[string]any{"enabled": false}) }); err != nil {
		t.Fatal(err)
	}
	edited, _ := os.ReadFile(file)
	rep, err = Move(home, file)
	if err != nil || len(rep.Moved) != 0 || rep.Backup != "" || len(rep.Stale) != 0 {
		t.Fatalf("second start: %+v %v", rep, err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(edited) {
		t.Fatalf("second start rewrote manager.json:\n%s", after)
	}
	if string(before) == string(edited) || stBefore == nil {
		t.Fatal("the edit did not land")
	}
	backups, _ := filepath.Glob(filepath.Join(DefaultDir(home), backupPrefix+"*"))
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
}

// hub.json edited by hand after the move (as the old docs said), or by an older build switched back: a section
// moved before is not read from hub.json again, and Move says so; one hub.json did not have then is moved now.
func TestMoveAfterHubJSONEdits(t *testing.T) {
	home := t.TempDir()
	hubPath := filepath.Join(home, "hub.json")
	if err := os.WriteFile(hubPath, []byte(`{"token":"t","lark":{"account":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := DefaultFile(home)
	if _, err := Move(home, file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hubPath, []byte(`{"token":"t2","lark":{"account":3},"usage":{"sources":[{"id":"x"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Move(home, file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Stale, []string{"lark"}) || !slices.Equal(rep.Moved, []string{"usage"}) {
		t.Fatalf("report %+v", rep)
	}
	mine := readObject(t, file)
	if compact(t, mine["lark"]) != `{"account":2}` || mine["usage"] == nil {
		t.Fatalf("manager.json %v", mine)
	}
	var m Marker
	if err := json.Unmarshal(mine[MarkerKey], &m); err != nil || m.Backups["lark"] == m.Backups["usage"] || m.Backups["lark"] == "" {
		t.Fatalf("marker %s", mine[MarkerKey])
	}
	// Each section is compared with the copy kept when it moved: the later copy does not hide the lark edit.
	if rep, err := Move(home, file); err != nil || !slices.Equal(rep.Stale, []string{"lark"}) || len(rep.Moved) != 0 {
		t.Fatalf("third start %+v %v", rep, err)
	}
}

// A manager made in the app: its manager.json already has who it is and maybe a section of its own; a hub.json in
// its directory (an older build read its sections there) moves in, without overwriting either.
func TestMoveKeepsWhatManagerJSONHas(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	if err := os.WriteFile(file, []byte(`{"id":"work","hub":"https://hub.example.com","token":"bus","google":{"client_id":"mine"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"google":{"client_id":"old"},"bots":{"feishu":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Move(dir, file)
	if err != nil || !slices.Equal(rep.Moved, []string{"bots"}) {
		t.Fatalf("%+v %v", rep, err)
	}
	// hub.json's google lost to manager.json's: said, not silently dropped.
	if !slices.Equal(rep.Ignored, []string{"google"}) {
		t.Fatalf("ignored %v, want google", rep.Ignored)
	}
	mine := readObject(t, file)
	if compact(t, mine["google"]) != `{"client_id":"mine"}` || compact(t, mine["token"]) != `"bus"` || mine["bots"] == nil {
		t.Fatalf("manager.json %v", mine)
	}
	b, _ := os.ReadFile(file)
	if !strings.HasPrefix(strings.TrimSpace(string(b)), `{
  "id": "work"`) {
		t.Fatalf("key order changed:\n%s", b)
	}
}

// No hub.json (a manager elsewhere, or a new one): nothing is written.
func TestMoveWithoutHubJSONWritesNothing(t *testing.T) {
	home := t.TempDir()
	rep, err := Move(home, DefaultFile(home))
	if err != nil || len(rep.Moved) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := os.Stat(DefaultDir(home)); !os.IsNotExist(err) {
		t.Fatalf("made %s: %v", DefaultDir(home), err)
	}
}

// A section added to Moved later is moved by the next start; those moved before are not taken again.
func TestMoveTakesNewSectionsLater(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(`{"google":{"client_id":"g"},"later":{"x":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := DefaultFile(home)
	if _, err := Move(home, file); err != nil {
		t.Fatal(err)
	}
	if err := Edit(file, func(o *Object) error { o.Del("google"); return nil }); err != nil { // the manager dropped it
		t.Fatal(err)
	}
	old := Moved
	Moved = append(slices.Clone(old), "later")
	t.Cleanup(func() { Moved = old })
	rep, err := Move(home, file)
	if err != nil || !slices.Equal(rep.Moved, []string{"later"}) {
		t.Fatalf("%+v %v", rep, err)
	}
	if mine := readObject(t, file); mine["google"] != nil || mine["later"] == nil {
		t.Fatalf("manager.json %v", mine)
	}
}

// Sections hands a manager's struct only the sections: who it is (its bus token) and the marker stay out.
func TestSectionsLeaveIdentityOut(t *testing.T) {
	file := filepath.Join(t.TempDir(), File)
	if err := os.WriteFile(file, []byte(`{"id":"work","token":"bus","lark":{"account":2},"moved_from_hub_json":{"keys":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Sections(file)
	if err != nil || string(b) != `{"lark":{"account":2}}` {
		t.Fatalf("%s %v", b, err)
	}
	if b, err := Sections(filepath.Join(t.TempDir(), File)); err != nil || string(b) != "{}" {
		t.Fatalf("missing file: %s %v", b, err)
	}
}

// Only and Without split a section by its dotted keys: the manager reads its part, the hub keeps the rest.
func TestOnlyAndWithoutSplitSections(t *testing.T) {
	o, err := ParseObject([]byte(`{"token":"t","assistant":{"owner":"o","external":true},"kb":{"enabled":false},"other":1}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"kb", "assistant.owner", "assistant.notes"}
	if got := string(Only(o, keys)); got != `{"assistant":{"owner":"o"},"kb":{"enabled":false}}` {
		t.Errorf("Only %s", got)
	}
	if got := string(Without(o, keys)); got != `{"token":"t","assistant":{"external":true},"other":1}` {
		t.Errorf("Without %s", got)
	}
	// A section that is not an object is left alone by both (the hub's parser says what is wrong with it).
	o, _ = ParseObject([]byte(`{"assistant":"x"}`))
	if got := string(Only(o, keys)); got != `{}` {
		t.Errorf("Only %s", got)
	}
	if got := string(Without(o, keys)); got != `{"assistant":"x"}` {
		t.Errorf("Without %s", got)
	}
}

// A section both have with the same value is not reported as ignored.
func TestMoveSameValueIsNotIgnored(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	if err := os.WriteFile(file, []byte(`{"google":{"client_id":"same"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"google": {"client_id": "same"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Move(dir, file)
	if err != nil || len(rep.Ignored) != 0 || len(rep.Moved) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// A copy of the whole hub.json an earlier build kept (the hub's token and database password in it) is cut down to
// the sections it was kept for at the next start; one that holds only those is left as it is.
func TestMoveCutsWholeHubJSONCopiesDown(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(oldHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	file := DefaultFile(home)
	if err := os.MkdirAll(DefaultDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	old := backupPrefix + "20261106T000000.000000000Z"
	oldPath := filepath.Join(DefaultDir(home), old)
	if err := os.WriteFile(oldPath, []byte(oldHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := `{"keys":["lark","usage"],"at":"2026-11-06T00:00:00Z","backups":{"lark":"` + old + `","usage":"` + old + `"}}`
	if err := os.WriteFile(file, []byte(`{"lark":{"account":2},"usage":{"sources":[]},"`+MarkerKey+`":`+marker+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Move(home, file)
	if err != nil || len(rep.Untrimmed) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	cut := readObject(t, oldPath)
	if len(cut) != 2 || cut["lark"] == nil || cut["usage"] == nil {
		t.Fatalf("the old copy holds %v, want only lark and usage", cut)
	}
	if st, err := os.Stat(oldPath); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	if len(rep.Stale) != 0 {
		t.Fatalf("stale %v", rep.Stale)
	}
	stCut, _ := os.Stat(oldPath)
	// The cut copy still tells a later hand edit of hub.json's lark from what moved.
	edited := strings.Replace(oldHubJSON, `"lark": {"account": 2}`, `"lark": {"account": 3}`, 1)
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = Move(home, file)
	if err != nil || !slices.Equal(rep.Stale, []string{"lark"}) {
		t.Fatalf("after a hand edit: %+v %v", rep, err)
	}
	if st, _ := os.Stat(oldPath); !st.ModTime().Equal(stCut.ModTime()) {
		t.Fatal("a copy already cut down was written again")
	}
}

// A whole copy the marker doesn't name (its manager.json was not written then) is cut down to the Moved sections;
// names in the marker that are not Move's copies (manager.json, a file outside the directory: a hand edit) are never
// touched, since only the directory's own copies are looked at; and with hub.json gone the copies are still cut down.
func TestTrimBackupsOnlyTouchesItsCopies(t *testing.T) {
	home := t.TempDir()
	dir := DefaultDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlisted := filepath.Join(dir, backupPrefix+"20261106T010000.000000000Z")
	if err := os.WriteFile(unlisted, []byte(oldHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	hubJSON := filepath.Join(home, "hub.json")
	if err := os.WriteFile(hubJSON, []byte(oldHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	file := DefaultFile(home)
	// A whole copy outside the manager's directory, which the marker points at: never touched.
	outside := filepath.Join(home, backupPrefix+"20261106T000000.000000000Z")
	if err := os.WriteFile(outside, []byte(oldHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	mine := `{"id":"keep","lark":{"account":2},"` + MarkerKey + `":{"keys":["lark","usage"],"at":"2026-11-06T00:00:00Z",` +
		`"backups":{"lark":"manager.json","usage":"../../` + filepath.Base(outside) + `"}}}`
	if err := os.WriteFile(file, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(hubJSON); err != nil { // no hub.json: nothing to take over, the copies still cut down
		t.Fatal(err)
	}
	rep, err := Move(home, file)
	if err != nil || len(rep.Untrimmed) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if b, _ := os.ReadFile(file); string(b) != mine {
		t.Fatalf("manager.json changed:\n%s", b)
	}
	cutRaw, _ := os.ReadFile(unlisted)
	cut, err := ParseObject(cutRaw)
	if _, ok := cut.Get("token"); err != nil || ok || string(Only(cut, Moved)) != string(cut.Marshal()) ||
		len(cut.Keys()) == 0 {
		t.Fatalf("the unlisted copy holds %s (%v)", cutRaw, err)
	}
	if b, _ := os.ReadFile(outside); string(b) != oldHubJSON {
		t.Fatalf("a file outside the manager's directory was cut:\n%s", b)
	}
	// A broken marker stops it there too.
	if err := os.WriteFile(file, []byte(`{"`+MarkerKey+`":"yes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(home, file); err == nil {
		t.Fatal("a broken marker without hub.json passed")
	}
}

// The same value with its keys in another order or other spacing is the same: not reported.
func TestSameJSONIgnoresKeyOrder(t *testing.T) {
	if !sameJSON(json.RawMessage(`{"client_id":"x","proxy":"p"}`), json.RawMessage(`{ "proxy": "p", "client_id": "x" }`)) {
		t.Fatal("key order made a difference")
	}
	if sameJSON(json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":1.0}`)) || sameJSON(json.RawMessage(`[1,2]`), json.RawMessage(`[2,1]`)) {
		t.Fatal("different values are the same")
	}
}

// A marker naming manager.json as a section's copy, with hub.json there: not read as the copy (no false stale
// report), and manager.json is not cut.
func TestStaleIgnoresNamesThatAreNotCopies(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	mine := `{"id":"keep","lark":{"account":2},"` + MarkerKey + `":{"keys":["lark"],"at":"2026-11-06T00:00:00Z",` +
		`"backups":{"lark":"manager.json"}}}`
	if err := os.WriteFile(file, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"lark":{"account":3}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Move(dir, file)
	if err != nil || len(rep.Stale) != 0 || len(rep.Moved) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if b, _ := os.ReadFile(file); string(b) != mine {
		t.Fatalf("manager.json changed:\n%s", b)
	}
}

// A temporary file a copy was being written to when a start was cut short goes; a copy that is not JSON or not a
// plain file stays, and is said.
func TestTrimBackupsLeftovers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	if err := os.WriteFile(file, []byte(`{"id":"keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(dir, backupPrefix+"20261106T010000.000000000Z"+tempInfix+"123")
	short := filepath.Join(dir, backupPrefix+"20261106T020000.000000000Z")
	link := filepath.Join(dir, backupPrefix+"20261106T030000.000000000Z")
	for _, p := range []string{temp, short} {
		if err := os.WriteFile(p, []byte(`{"token":"hub-token","database_url":"pos`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	rep, err := Move(dir, file) // no hub.json
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Fatalf("the temporary file stayed: %v", err)
	}
	if len(rep.Untrimmed) != 2 || !strings.HasPrefix(rep.Untrimmed[0], short) || !strings.HasPrefix(rep.Untrimmed[1], link) {
		t.Fatalf("untrimmed %q", rep.Untrimmed)
	}
}

// manager.json a link to nothing (a volume not mounted yet): an error, not a fresh install, and the link stays.
func TestDanglingManagerJSONIsAnError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	if err := os.Symlink(filepath.Join(dir, "not-mounted", File), file); err != nil {
		t.Fatal(err)
	}
	if _, err := Sections(file); err == nil {
		t.Fatal("Sections read a link to nothing as no file")
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"lark":{"account":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(dir, file); err == nil {
		t.Fatal("Move wrote over a link to nothing")
	}
	if st, err := os.Lstat(file); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v %v", st, err)
	}
	// The manager's directory a link to nothing, no hub.json: an error too, not a fresh install.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "managers"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "not-mounted"), DefaultDir(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(home, DefaultFile(home)); err != nil {
		t.Fatal(err) // nothing to take over: Move has nothing to say
	}
	if _, err := Sections(DefaultFile(home)); err == nil {
		t.Fatal("Sections read a directory that is a link to nothing as no file")
	}
	// With a hub.json to move from, the move says it too, naming the link.
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(`{"lark":{"account":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(home, DefaultFile(home)); err == nil || !strings.Contains(err.Error(), "link to nothing") {
		t.Fatalf("Move: %v", err)
	}
	if err := os.Remove(filepath.Join(home, "hub.json")); err != nil {
		t.Fatal(err)
	}
	// A link to a directory that is there, without manager.json yet: none, as for a fresh install.
	if err := os.MkdirAll(filepath.Join(home, "not-mounted"), 0o700); err != nil {
		t.Fatal(err)
	}
	if b, err := Sections(DefaultFile(home)); err != nil || string(b) != "{}" {
		t.Fatalf("%s %v", b, err)
	}
}

// An empty manager.json (cut short, emptied by a failed save) is a file that does not read, not an empty
// configuration: Move does not take hub.json's copies over again into it, and Sections says so.
func TestEmptyManagerJSONIsAnError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	if err := os.WriteFile(file, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sections(file); err == nil {
		t.Fatal("Sections read an empty file")
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"lark":{"account":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(dir, file); err == nil {
		t.Fatal("Move took hub.json over into an empty file")
	}
	if b, _ := os.ReadFile(file); string(b) != "  \n" {
		t.Fatalf("manager.json changed: %q", b)
	}
}

// hub.json a link to nothing (an older build's, on a volume not mounted yet): an error, not "no hub.json".
func TestDanglingHubJSONIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "not-mounted", "hub.json"), filepath.Join(dir, "hub.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Move(dir, filepath.Join(dir, File)); err == nil || !strings.Contains(err.Error(), "link to nothing") {
		t.Fatalf("Move: %v", err)
	}
}

// The hub's hello-era sections, handed over raw to a built-in manager elsewhere: taken over once, only those; a
// section manager.json has keeps its value; the next hello writes nothing.
func TestMoveFromTheHubsSections(t *testing.T) {
	legacy := Legacy([]byte(oldHubJSON))
	lo, err := ParseObject(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lo.Get("token"); ok || string(Only(lo, HelloSections)) != string(lo.Marshal()) {
		t.Fatalf("legacy holds more than the hello's sections: %s", legacy)
	}
	if Legacy([]byte(`{"token":"t","lark":{}}`)) != nil {
		t.Fatal("legacy of a hub.json without them")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, File)
	if err := os.WriteFile(file, []byte(`{"ai":{"model":"mine"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := MoveFrom(legacy, file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(rep.Moved, "kb") || !slices.Contains(rep.Moved, "assistant.owner") || slices.Contains(rep.Moved, "ai") ||
		!slices.Contains(rep.Moved, "inbox.triage_ai") || !slices.Equal(rep.Ignored, []string{"ai"}) || rep.Backup == "" {
		t.Fatalf("%+v", rep)
	}
	mineRaw, _ := os.ReadFile(file)
	mine, _ := ParseObject(mineRaw)
	if v, _ := getPath(mine, "assistant.owner"); compact(t, v) != `"Example"` {
		t.Fatalf("manager.json %s", mineRaw)
	}
	if _, ok := mine.Get("lark"); ok {
		t.Fatalf("took a section the hello never had: %s", mineRaw)
	}
	before, _ := os.ReadFile(file)
	if rep, err := MoveFrom(legacy, file); err != nil || len(rep.Moved) != 0 || len(rep.Ignored) != 0 {
		t.Fatalf("the next hello: %+v %v", rep, err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("the next hello rewrote manager.json")
	}
}

// manager.json's assistant not an object: an error, not "no owner, no notes".
func TestSectionsRefusesASplitSectionThatIsNoObject(t *testing.T) {
	file := filepath.Join(t.TempDir(), File)
	if err := os.WriteFile(file, []byte(`{"assistant":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sections(file); err == nil || !strings.Contains(err.Error(), "assistant") {
		t.Fatalf("%v", err)
	}
}

// A built-in manager elsewhere whose machine has a hub.json of its own (one setup wrote, with placeholders for the
// hello's sections): its start leaves those out, and the hub's, handed over at the handshake, are taken once; of
// inbox, this machine's rules and switch stay and the hub's models join them. A later change of the hub's is not
// taken, and the next start says nothing about them.
func TestMoveElsewhereTakesTheHellosSectionsFromTheHub(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "hub.json"), []byte(`{"token":"t","ai":{"api":""},"kb":{},
		"hook_timeout_minutes":30,"inbox":{"rules":"mine","agent":{"enabled":true,"machine":"desk"},"triage_ai":{"model":"stale-local"}},
		"lark":{"account":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	file := DefaultFile(home)
	rep, err := MoveExcept(home, file, HubOnly)
	if err != nil || !slices.Equal(rep.Moved, []string{"lark", "inbox"}) {
		t.Fatalf("local: %+v %v", rep, err)
	}
	hub := `{"ai":{"base_url":"https://gateway.example.com"},"kb":{"enabled":false},"hook_timeout_minutes":30,
		"inbox":{"triage_ai":{"model":"t"},"agent":{"machine":"server"}}}`
	rep, err = MoveFrom([]byte(hub), file)
	// The twin's machine was always this machine's to say: kept, the hub's differing one said.
	if err != nil || !slices.Equal(rep.Moved, []string{"kb", "ai", "hook_timeout_minutes", "inbox.triage_ai"}) ||
		!slices.Equal(rep.Ignored, []string{"inbox.agent.machine"}) || rep.Backup == "" {
		t.Fatalf("from the hub: %+v %v", rep, err)
	}
	mine := readObject(t, file)
	if compact(t, mine["kb"]) != `{"enabled":false}` || compact(t, mine["ai"]) != `{"base_url":"https://gateway.example.com"}` ||
		compact(t, mine["inbox"]) != `{"agent":{"enabled":true,"machine":"desk"},"rules":"mine","triage_ai":{"model":"t"}}` {
		t.Fatalf("manager.json %v", mine)
	}
	before, _ := os.ReadFile(file)
	if rep, err := MoveFrom([]byte(strings.Replace(hub, `"enabled":false`, `"enabled":true`, 1)), file); err != nil ||
		len(rep.Moved)+len(rep.Ignored) != 0 {
		t.Fatalf("a later handshake: %+v %v", rep, err)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("a later handshake rewrote manager.json")
	}
	if rep, err := MoveExcept(home, file, HubOnly); err != nil || len(rep.Stale)+len(rep.Moved) != 0 {
		t.Fatalf("next start: %+v %v", rep, err)
	}
}
