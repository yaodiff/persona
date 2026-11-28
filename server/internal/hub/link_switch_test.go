package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The ids these tests use (cleared first: the test database outlives a run).
var switchTestIDs = []string{"lsw-box", "lsw-bare", "lsw-link", "lsw-far", "lsw-desk"}

// newSwitchOp is an operation id of the manager's form, new for each test (the audit outlives a run).
func newSwitchOp() string { return "ls_" + randomHex(8) }

// clearSwitchIDs forgets what an earlier test left of these computers: trust rows, credentials, open codes, a switch
// under way.
func clearSwitchIDs(t *testing.T, database *db.DB) {
	t.Helper()
	ctx := context.Background()
	for _, id := range switchTestIDs {
		if err := database.ForgetMachineTrust(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, err := database.RevokeMachineCredentials(ctx, id); err != nil {
			t.Fatal(err)
		}
		// A switch an earlier test left under way holds the computer (the audit is not cleared): ended here.
		latest, err := database.LinkSwitchLatest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if latest != nil && !switchEnded(latest.Step) {
			detail, _ := json.Marshal(map[string]string{"operation": latest.Operation, "step": switchRolledBack})
			if err := database.AddMachineAudit(ctx, id, "link_switch", "test", detail); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// switchMachines are switchHub's computers: two ssh ones (lsw-box named, with a key the hub does not read; lsw-bare
// with no name and no kind) and a link one.
var switchMachines = []MachineConfig{
	{ID: "lsw-box", Name: "Box", Kind: "ssh", SSHHost: "me@box.example", Icon: "desktop"},
	{ID: "lsw-bare", SSHHost: "bare.example"},
	{ID: "lsw-link", Name: "Link", Kind: kindLink},
}

const switchHubJSON = `{"token":"t","public_url":"https://hub.example.com","machines":[` +
	`{"id":"lsw-box","name":"Box","kind":"ssh","ssh_host":"me@box.example","icon":"desktop","note":"kept as it is"},` +
	`{"id":"lsw-bare","ssh_host":"bare.example"},` +
	`{"id":"lsw-link","name":"Link","kind":"link"}]}`

// switchHub is a running hub (its manager has the entries) behind an https public_url, so a Persona Link can pair, with
// the database and switchMachines in hub.json, all confirmed and reported by their manager; its log is kept.
func switchHub(t *testing.T) (*Hub, *db.DB, *syncLog) {
	t.Helper()
	database := testDB(t)
	clearSwitchIDs(t, database)
	h := newTestHub(t)
	h.store = database
	logs := &syncLog{}
	h.logger = log.New(logs, "", 0)
	h.cfg.PublicURL = "https://hub.example.com"
	h.cfg.Machines = slices.Clone(switchMachines)
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(switchHubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	h.devices.entries, h.devices.started = slices.Clone(h.cfg.Machines), true
	h.loadMachineTrust(context.Background())
	h.trust.pending = map[string]time.Time{} // only this test's
	switchReport(h, time.Time{}, nil)
	return h, database, logs
}

// switchReport is the manager's report of switchHub's computers: each connected since at (zero: offline), of its
// entry's kind unless kinds says otherwise.
func switchReport(h *Hub, at time.Time, kinds map[string]string) {
	var st devicewire.States
	for _, mc := range switchMachines {
		kind := cmp.Or(kinds[mc.ID], mc.Kind)
		st.Machines = append(st.Machines, devicewire.State{Machine: proto.Machine{ID: mc.ID, Name: mc.Name, Kind: kind},
			Connected: !at.IsZero(), ConnectedAt: at})
	}
	h.applyDeviceStates(st)
}

func busBegin(h *Hub, c *busConn, p devicewire.LinkSwitchBegin) (devicewire.LinkSwitchBegun, error) {
	raw, _ := json.Marshal(p)
	out, err := busMethods[devicewire.MethodLinkSwitchBegin](h, context.Background(), c, raw)
	if err != nil {
		return devicewire.LinkSwitchBegun{}, err
	}
	return out.(devicewire.LinkSwitchBegun), nil
}

func busEnd(h *Hub, c *busConn, p devicewire.LinkSwitchEnd) error {
	raw, _ := json.Marshal(p)
	out, err := busMethods[devicewire.MethodLinkSwitchEnd](h, context.Background(), c, raw)
	if err == nil {
		if b, _ := json.Marshal(out); string(b) != "{}" {
			return errors.New("end answered " + string(b))
		}
	}
	return err
}

func refusedWith(t *testing.T, what string, err error, code string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("%s: %v, want %s", what, err, code)
	}
}

// pairTokenOf is the pairing token a link carries.
func pairTokenOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "persona" || u.Host != "link" {
		t.Fatalf("not a Persona Link pairing link: %v", err)
	}
	return u.Query().Get("token")
}

// redeem trades a pairing token for a credential as persona-link pair does; the answer and its status.
func redeem(t *testing.T, h *Hub, tok, body string) (int, map[string]any) {
	t.Helper()
	return call(t, h.routes(), "POST", "/api/v1/auth/tokens", tok, body)
}

func diskKind(t *testing.T, h *Hub, id string) any { t.Helper(); return diskEntry(t, h, id)["kind"] }

// begin on a computer of hub.json: its kind becomes link and nothing else of it changes, at once (what the manager is
// handed, no restart waiting), and the one pairing code made for it redeems once, for it only, without renaming it
// after its system. The mirror takes the new kind on the next report, the same machine, what ran on the old way
// ended. Neither the link nor its token is in the log or the audit.
func TestLinkSwitchBeginsOnAHubJSONComputer(t *testing.T) {
	h, database, logs := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	switchReport(h, at, nil)
	m := h.machineByID("lsw-bare")
	m.mu.Lock()
	oldMux := m.streams
	m.mu.Unlock()
	if oldMux == nil {
		t.Fatal("no streams on the ssh connection")
	}

	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-bare", Operation: op, SSHHost: "bare.example", By: "device"})
	if err != nil {
		t.Fatal(err)
	}
	tok := pairTokenOf(t, got.Link)
	if got.PairHash != hashToken(tok) || !strings.HasPrefix(tok, "jp_") {
		t.Fatalf("answer %+v", got)
	}
	if p, err := database.PairTokenByHash(ctx, got.PairHash); err != nil || p == nil || p.MachineID != "lsw-bare" || p.UsedAt != nil {
		t.Fatalf("its pairing code: %+v %v", p, err)
	}
	if e := diskEntry(t, h, "lsw-bare"); e["kind"] != "link" || e["ssh_host"] != "bare.example" || len(e) != 3 {
		t.Fatalf("hub.json entry %v", e)
	}
	if mc := h.cfgEntry("lsw-bare"); mc == nil || mc.Kind != kindLink || mc.SSHHost != "bare.example" || mc.Name != "" {
		t.Fatalf("the manager's entry %+v", mc)
	}
	cfg := h.devicesConfigFor(devicesOwner)
	handed := ""
	for _, raw := range cfg.Machines {
		var mc MachineConfig
		if json.Unmarshal(raw, &mc) == nil && mc.ID == "lsw-bare" {
			handed = mc.Kind
		}
	}
	if !cfg.LinkSwitch || handed != kindLink {
		t.Fatalf("handed over (link_switch %v) %s", cfg.LinkSwitch, slices.Concat(cfg.Machines...))
	}
	if r := h.pendingRestart("en"); r.Required {
		t.Fatalf("the switch waits for a restart: %v", r.Reasons)
	}
	if !h.linkSwitch.live("lsw-bare", time.Now()) {
		t.Fatal("no switch under way")
	}

	// The manager reports it reached the new way: the same machine, its kind link, the old connection's streams over.
	switchReport(h, at, map[string]string{"lsw-bare": kindLink})
	if again := h.machineByID("lsw-bare"); again != m || m.kind() != kindLink {
		t.Fatalf("the mirror: same %v, kind %q", again == m, m.kind())
	}
	if err := oldMux.Open("after"); err == nil {
		t.Fatal("the ssh connection's streams were left open")
	}
	for _, s := range h.machineList() {
		if s.ID == "lsw-bare" && (s.Route == nil || s.Route.Via != kindLink) {
			t.Fatalf("shown reached through %+v", s.Route)
		}
	}

	// The code: for another computer, refused; for this one, a link credential, once; and no system name.
	if code, _ := redeem(t, h, tok, `{"machine":"lsw-box"}`); code != http.StatusForbidden {
		t.Fatalf("redeemed for another computer: %d", code)
	}
	code, out := redeem(t, h, tok, `{"system_name":"bare-host"}`)
	if code != 200 || out["machine"] != "lsw-bare" || out["role"] != roleLink {
		t.Fatalf("redeem: %d %v", code, out)
	}
	if code, _ := redeem(t, h, tok, `{}`); code != http.StatusUnauthorized {
		t.Fatalf("redeemed twice: %d", code)
	}
	if got := listedName(h, "lsw-bare"); got != "lsw-bare" {
		t.Fatalf("renamed after its system while switching: %q", got)
	}

	for _, secret := range []string{tok, got.Link} {
		if logs.about(secret) > 0 {
			t.Fatal("the log has the pairing link")
		}
	}
	audits, err := database.MachineAudits(ctx, "lsw-bare")
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, a := range audits {
		if strings.Contains(string(a.Detail), tok) || strings.Contains(string(a.Detail), "persona://") {
			t.Fatalf("the audit has the pairing link: %s", a.Detail)
		}
		var d map[string]any
		_ = json.Unmarshal(a.Detail, &d)
		if a.Action == "link_switch" && d["operation"] == op {
			if d["by"] != "device" || a.Actor != "manager:"+devicesOwner || d["step"] == "begun" && d["pair_hash"] != got.PairHash {
				t.Fatalf("audit %s by %s", a.Detail, a.Actor)
			}
			steps = append(steps, d["step"].(string))
		}
	}
	if !slices.Equal(steps, []string{"begin", "begun"}) {
		t.Fatalf("audited steps %v", steps)
	}
}

// begin on a computer its manager keeps in its own list (machines_kept.go): the kind changes there, every other key
// (its secrets too) as it was; hub.json is not touched. rolled_back puts it back there.
func TestLinkSwitchBeginsOnAKeptComputer(t *testing.T) {
	switchOp := newSwitchOp() // the audit outlives a run, and an operation id is used once
	ctx := context.Background()
	database := testDB(t)
	clearSwitchIDs(t, database)
	h := newTestHub(t)
	h.store = database
	h.bus.idents = &keptIdents{ids: &memIdents{}, kept: map[string]json.RawMessage{}}
	h.cfg.PublicURL = "https://hub.example.com"
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"public_url":"https://hub.example.com","machines":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.startDevices(ctx)
	h.trust.pending = map[string]time.Time{}
	srv := newHubServer(t, h)
	far := json.RawMessage(`{"id":"lsw-far","name":"Far","kind":"ssh","ssh_host":"far.example","tailcat_addr":"tc-s3cret","note":"kept as it is"}`)
	k := connectKeeper(t, h, srv, "m-0f0f0f", far)
	h.devices.moving.Lock()
	err := h.setKept(ctx, "lsw-far", k.id)
	h.devices.moving.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.seedMachineEntry(k.id, "lsw-far", far) // as its report says
	c := h.busConnOf(k.id)

	// Only its keeper may.
	if _, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-far", Operation: switchOp, SSHHost: "far.example"}); err == nil {
		t.Fatal("another manager began it")
	}
	got, err := busBegin(h, c, devicewire.LinkSwitchBegin{MachineID: "lsw-far", Operation: switchOp, SSHHost: "far.example", By: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	var e map[string]any
	if err := json.Unmarshal(k.has("lsw-far"), &e); err != nil {
		t.Fatal(err)
	}
	if e["kind"] != "link" || e["ssh_host"] != "far.example" || e["tailcat_addr"] != "tc-s3cret" || e["note"] != "kept as it is" || e["name"] != "Far" || len(e) != 6 {
		t.Fatalf("its keeper's entry %v", e)
	}
	if mc := h.knownEntry("lsw-far"); mc == nil || mc.Kind != kindLink {
		t.Fatalf("what the hub knows of it: %+v", mc)
	}
	if ids := hubJSONMachines(t, h.cfg.dir); len(ids) != 0 {
		t.Fatalf("hub.json changed: %v", ids)
	}
	if p, _ := database.PairTokenByHash(ctx, got.PairHash); p == nil || p.MachineID != "lsw-far" {
		t.Fatalf("its code %+v", p)
	}
	if r := h.pendingRestart("en"); r.Required {
		t.Fatalf("restart %v", r.Reasons)
	}

	if err := busEnd(h, c, devicewire.LinkSwitchEnd{MachineID: "lsw-far", Operation: switchOp, PairHash: got.PairHash,
		Outcome: devicewire.LinkSwitchRolledBack}); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(k.has("lsw-far"), &e)
	if e["kind"] != "ssh" || e["tailcat_addr"] != "tc-s3cret" || len(e) != 6 {
		t.Fatalf("rolled back: its keeper's entry %v", e)
	}
	if ok, _ := database.MachineLinkInUse(ctx, "lsw-far"); ok {
		t.Fatal("its code still works")
	}
}

// begin is refused, with nothing written and no code made, when the hub cannot or must not switch the computer.
func TestLinkSwitchBeginRefusals(t *testing.T) {
	switchOp := newSwitchOp() // the audit outlives a run, and an operation id is used once
	h, database, _ := switchHub(t)
	ctx := context.Background()
	other := &busConn{ident: db.BusIdentity{ID: "m-0d0d0d"}}
	begin := func(c *busConn, mid, host string) error {
		_, err := busBegin(h, c, devicewire.LinkSwitchBegin{MachineID: mid, Operation: switchOp, SSHHost: host})
		return err
	}
	refusedWith(t, "no such computer", begin(nil, "lsw-none", ""), devicewire.LinkSwitchCodeNoMachine)
	refusedWith(t, "no id", begin(nil, "", ""), devicewire.LinkSwitchCodeNoMachine)
	refusedWith(t, "another manager's", begin(other, "lsw-box", "me@box.example"), devicewire.LinkSwitchCodeNoMachine)
	refusedWith(t, "a link computer", begin(nil, "lsw-link", ""), devicewire.LinkSwitchCodeNotSSH)
	refusedWith(t, "another ssh_host", begin(nil, "lsw-box", "me@old.example"), devicewire.LinkSwitchCodeChanged)

	h.setPendingLocal("lsw-box", time.Now(), true)
	refusedWith(t, "not confirmed", begin(nil, "lsw-box", "me@box.example"), devicewire.LinkSwitchCodePending)
	h.setPendingLocal("lsw-box", time.Time{}, false)

	h.cfg.PublicURL = "http://hub.example.com" // no TLS port either
	refusedWith(t, "no Persona Link", begin(nil, "lsw-box", "me@box.example"), devicewire.LinkSwitchCodeUnavailable)
	if h.devicesConfigFor(devicesOwner).LinkSwitch {
		t.Fatal("the manager is told the hub switches computers it cannot pair")
	}
	h.cfg.PublicURL = "https://hub.example.com"

	// A credential already, or a code not used yet.
	if err := database.CreateDeviceToken(ctx, db.DeviceToken{ID: "dev_lswbox", Name: "link", Kind: "link", Role: roleLink, MachineID: "lsw-box"},
		hashToken("jd_lsw-box-old")); err != nil {
		t.Fatal(err)
	}
	refusedWith(t, "a credential already", begin(nil, "lsw-box", "me@box.example"), devicewire.LinkSwitchCodeExistingLink)
	if _, err := h.newPairCode(ctx, "lsw-bare"); err != nil {
		t.Fatal(err)
	}
	refusedWith(t, "an open code", begin(nil, "lsw-bare", "bare.example"), devicewire.LinkSwitchCodeExistingLink)

	if b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json")); string(b) != switchHubJSON {
		t.Fatalf("hub.json changed:\n%s", b)
	}
	for _, id := range []string{"lsw-box", "lsw-bare"} {
		if mc := h.cfgEntry(id); kindOf(*mc) != "ssh" {
			t.Fatalf("%s's kind %q", id, mc.Kind)
		}
		if running := h.linkSwitch.live(id, time.Now()); running {
			t.Fatalf("%s: a refused switch left a guard", id)
		}
	}
}

// begin's compare-and-swap: the running hub's entry is the manager's, but hub.json says otherwise (edited, waiting for
// a restart): refused as changed, nothing written, no code.
func TestLinkSwitchBeginComparesTheEntry(t *testing.T) {
	switchOp := newSwitchOp() // the audit outlives a run, and an operation id is used once
	h, database, _ := switchHub(t)
	ctx := context.Background()
	edited := strings.Replace(switchHubJSON, "me@box.example", "me@new.example", 1)
	edited = strings.Replace(edited, `{"id":"lsw-bare","ssh_host":"bare.example"}`, `{"id":"lsw-bare","kind":"tailcat","ssh_host":"bare.example"}`, 1)
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: switchOp, SSHHost: "me@box.example"})
	refusedWith(t, "ssh_host edited", err, devicewire.LinkSwitchCodeChanged)
	_, err = busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-bare", Operation: switchOp, SSHHost: "bare.example"})
	refusedWith(t, "kind edited", err, devicewire.LinkSwitchCodeNotSSH)
	if b, _ := os.ReadFile(filepath.Join(h.cfg.dir, "hub.json")); string(b) != edited {
		t.Fatalf("hub.json changed:\n%s", b)
	}
	for _, id := range []string{"lsw-box", "lsw-bare"} {
		if mc := h.cfgEntry(id); kindOf(*mc) != "ssh" {
			t.Fatalf("%s's running kind %q", id, mc.Kind)
		}
		if ok, _ := database.MachineLinkInUse(ctx, id); ok {
			t.Fatalf("%s: a code was made", id)
		}
	}
}

// noCodeStore refuses to keep a pairing code.
type noCodeStore struct{ *db.DB }

func (noCodeStore) CreatePairToken(context.Context, string, time.Time, string) error {
	return errors.New("the database refused")
}

// No pairing code: the kind goes back to ssh at once, and the manager is told it failed.
func TestLinkSwitchBeginWithoutACodePutsTheKindBack(t *testing.T) {
	switchOp := newSwitchOp() // the audit outlives a run, and an operation id is used once
	h, database, _ := switchHub(t)
	h.store = noCodeStore{database}
	_, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: switchOp, SSHHost: "me@box.example"})
	if err == nil || !strings.Contains(err.Error(), "the database refused") {
		t.Fatalf("begin: %v", err)
	}
	if k := diskKind(t, h, "lsw-box"); k != "ssh" {
		t.Fatalf("hub.json kind %v", k)
	}
	if e := diskEntry(t, h, "lsw-box"); e["note"] != "kept as it is" || e["ssh_host"] != "me@box.example" {
		t.Fatalf("hub.json entry %v", e)
	}
	if mc := h.cfgEntry("lsw-box"); mc.Kind != "ssh" {
		t.Fatalf("running kind %q", mc.Kind)
	}
	if running := h.linkSwitch.live("lsw-box", time.Now()); running {
		t.Fatal("a failed begin left its guard")
	}
	if r := h.pendingRestart("en"); r.Required {
		t.Fatalf("restart %v", r.Reasons)
	}
}

// A switch that succeeded keeps the computer on Persona Link with its credential; a credential made after it names
// a computer after its system again.
func TestLinkSwitchSucceeded(t *testing.T) {
	h, database, _ := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-bare", Operation: op, SSHHost: "bare.example", By: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{"system_name":"bare-host"}`)
	if code != 200 {
		t.Fatalf("redeem %d %v", code, out)
	}
	end := devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op, PairHash: got.PairHash, Outcome: devicewire.LinkSwitchSucceeded}
	for range 2 {
		if err := busEnd(h, nil, end); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := h.authenticate(ctx, out["token"].(string)); !ok || diskKind(t, h, "lsw-bare") != "link" {
		t.Fatal("a switch that succeeded lost its credential or its kind")
	}
	if running := h.linkSwitch.live("lsw-bare", time.Now()); running {
		t.Fatal("still switching")
	}
	h.nameAfterSystem(ctx, "lsw-bare", "bare-host")
	if got := listedName(h, "lsw-bare"); got != "bare-host" {
		t.Fatalf("not named after its system once the switch ended: %q", got)
	}
	audits, _ := database.MachineAudits(ctx, "lsw-bare")
	var steps []string
	for _, a := range audits {
		var d map[string]any
		_ = json.Unmarshal(a.Detail, &d)
		if a.Action == "link_switch" && d["operation"] == op {
			if d["step"] == "begun" && d["by"] != "admin" {
				t.Fatalf("audit %s", a.Detail)
			}
			steps = append(steps, d["step"].(string))
		}
	}
	if !slices.Equal(steps, []string{"begin", "begun", "succeeded"}) {
		t.Fatalf("audited %v", steps)
	}
}

// A rollback without the code's hash (the manager lost it) revokes the code the switch recorded and the credential it
// became, and puts the computer back to ssh; again, it answers the same. A code made for it otherwise, and another
// computer's credential, are not touched.
func TestLinkSwitchRollbackWithoutItsHash(t *testing.T) {
	op := newSwitchOp()
	h, _, _ := switchHub(t)
	ctx := context.Background()
	deskTok, _ := linkToken(t, h, "lsw-desk")
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d", code)
	}
	cred := out["token"].(string)
	more, err := h.newPairCode(ctx, "lsw-box")
	if err != nil {
		t.Fatal(err)
	}
	end := devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, Outcome: devicewire.LinkSwitchRolledBack}
	for range 2 {
		if err := busEnd(h, nil, end); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := h.authenticate(ctx, cred); ok {
		t.Fatal("its credential still works")
	}
	if p, _ := h.store.PairTokenByHash(ctx, hashToken(more.Token)); p == nil || p.ExpiresAt.Before(time.Now()) {
		t.Fatal("a code the switch did not make was revoked")
	}
	if _, ok := h.authenticate(ctx, deskTok); !ok {
		t.Fatal("another computer's credential was revoked")
	}
	if e := diskEntry(t, h, "lsw-box"); e["kind"] != "ssh" || e["note"] != "kept as it is" || e["name"] != "Box" || len(e) != 6 {
		t.Fatalf("hub.json entry %v", e)
	}
	if mc := h.cfgEntry("lsw-box"); mc.Kind != "ssh" {
		t.Fatalf("running kind %q", mc.Kind)
	}
	if r := h.pendingRestart("en"); r.Required {
		t.Fatalf("restart %v", r.Reasons)
	}
	// On a computer that never switched: nothing to undo.
	if err := busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op, Outcome: devicewire.LinkSwitchRolledBack}); err != nil {
		t.Fatal(err)
	}
	if diskEntry(t, h, "lsw-bare")["kind"] != nil {
		t.Fatal("an ssh computer's entry was written")
	}
}

// What end refuses: another manager's computer, an outcome it does not know, a hash that is no code of the computer,
// and a computer that is neither link nor ssh now.
func TestLinkSwitchEndRefusals(t *testing.T) {
	h, _, _ := switchHub(t)
	op := newSwitchOp()
	other := &busConn{ident: db.BusIdentity{ID: "m-0d0d0d"}}
	refusedWith(t, "another manager's", busEnd(h, other, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op,
		Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeNoMachine)
	refusedWith(t, "an unknown outcome", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, Outcome: "maybe"}),
		bus.CodeInvalidParams)
	for _, outcome := range []string{devicewire.LinkSwitchRolledBack, devicewire.LinkSwitchSucceeded} {
		refusedWith(t, "no operation", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Outcome: outcome}), bus.CodeInvalidParams)
	}
	if _, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"}); err != nil {
		t.Fatal(err)
	}
	refusedWith(t, "a hash of nothing", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op,
		PairHash: hashToken("jp_none"), Outcome: devicewire.LinkSwitchRolledBack}), bus.CodeInvalidParams)
	if k := diskKind(t, h, "lsw-box"); k != "link" {
		t.Fatalf("a refused end changed its kind: %v", k)
	}
	edited := strings.Replace(switchHubJSON, `"kind":"ssh"`, `"kind":"tailcat"`, 1)
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	refusedWith(t, "neither link nor ssh", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op,
		Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeNotSSH)
}

// A rollback undoes only a switch whose begin took effect. One that begin refused (the computer had a Persona Link
// already), one of another operation, or a manager's end on its own computer that never switched changes nothing:
// the computer's own credential survives, its kind stays. A switch this hub no longer remembers (it restarted) is
// still found by its audit row and undone.
func TestLinkSwitchRollbackOnlyUndoesABegunSwitch(t *testing.T) {
	h, _, _ := switchHub(t)
	ctx := context.Background()
	rollBack := func(mid, op, hash string) error {
		return busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: mid, Operation: op, PairHash: hash, Outcome: devicewire.LinkSwitchRolledBack})
	}

	// Begin refused: the user's own credential for it.
	refused := newSwitchOp()
	userTok, _ := linkToken(t, h, "lsw-box")
	_, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: refused, SSHHost: "me@box.example"})
	refusedWith(t, "a credential already", err, devicewire.LinkSwitchCodeExistingLink)
	if err := rollBack("lsw-box", refused, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.authenticate(ctx, userTok); !ok {
		t.Fatal("a rollback of a refused begin revoked the computer's own credential")
	}

	// A link computer that never switched.
	linkTok, _ := linkToken(t, h, "lsw-link")
	if err := rollBack("lsw-link", newSwitchOp(), ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.authenticate(ctx, linkTok); !ok || diskKind(t, h, "lsw-link") != "link" {
		t.Fatal("a rollback on a computer that never switched revoked its credential or changed its kind")
	}

	// Begun, then a rollback of another operation: nothing; of its own: undone.
	op := newSwitchOp()
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-bare", Operation: op, SSHHost: "bare.example"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d", code)
	}
	cred := out["token"].(string)
	if err := rollBack("lsw-bare", newSwitchOp(), ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.authenticate(ctx, cred); !ok || diskKind(t, h, "lsw-bare") != "link" {
		t.Fatal("a rollback of another operation undid this switch")
	}
	// This hub restarted: its memory of the switch is gone, the audit says it began.
	h.linkSwitch = linkSwitches{}
	if err := rollBack("lsw-bare", op, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.authenticate(ctx, cred); ok {
		t.Fatal("the switch's credential still works")
	}
	if diskKind(t, h, "lsw-bare") != "ssh" || h.cfgEntry("lsw-bare").Kind != "ssh" {
		t.Fatal("not back to ssh")
	}
	// Begun, but put back to ssh meanwhile and paired by its user: a rollback without the hash leaves that alone.
	held := newSwitchOp()
	if _, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-bare", Operation: held, SSHHost: "bare.example"}); err != nil {
		t.Fatal(err)
	}
	h.kindLive("lsw-bare", "ssh")
	if err := editHubJSON(h.cfg.dir, func(o *jsonObject) error { return setMachineField(o, "lsw-bare", "kind", "ssh") }); err != nil {
		t.Fatal(err)
	}
	ownTok, _ := linkToken(t, h, "lsw-bare")
	if err := rollBack("lsw-bare", held, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.authenticate(ctx, ownTok); !ok {
		t.Fatal("a rollback without the hash revoked a credential of a computer no longer on Persona Link")
	}
}

// A computer moved to another manager while it switched: end from the manager that began it is refused as not its
// own, yet a rollback that names its code still revokes that code's credential first; one without a hash revokes
// nothing of another manager's computer.
func TestLinkSwitchRollbackAfterTheComputerMoved(t *testing.T) {
	switchOp := newSwitchOp() // the audit outlives a run, and an operation id is used once
	h, _, _ := switchHub(t)
	ctx := context.Background()
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: switchOp, SSHHost: "me@box.example"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d", code)
	}
	cred := out["token"].(string)
	more, err := h.newPairCode(ctx, "lsw-box")
	if err != nil {
		t.Fatal(err)
	}
	if code, out = redeem(t, h, more.Token, `{}`); code != 200 {
		t.Fatalf("redeem the other code %d", code)
	}
	other := out["token"].(string)
	h.devices.mu.Lock()
	h.devices.assign = map[string]string{"lsw-box": "m-0e0e0e"}
	h.devices.mu.Unlock()
	refusedWith(t, "without a hash", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: switchOp,
		Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeNoMachine)
	if _, ok := h.authenticate(ctx, cred); !ok {
		t.Fatal("a rollback without a hash revoked another manager's computer's credential")
	}
	refusedWith(t, "with its hash", busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: switchOp,
		PairHash: got.PairHash, Outcome: devicewire.LinkSwitchRolledBack}), devicewire.LinkSwitchCodeRolledBackGone)
	if _, ok := h.authenticate(ctx, cred); ok {
		t.Fatal("its credential still works")
	}
	if _, ok := h.authenticate(ctx, other); !ok {
		t.Fatal("a credential the switch did not make was revoked")
	}
	if k := diskKind(t, h, "lsw-box"); k != "link" {
		t.Fatalf("another manager's computer's kind was changed: %v", k)
	}
	if running := h.linkSwitch.live("lsw-box", time.Now()); running {
		t.Fatal("the switch is still under way")
	}
}

// A switch that succeeded is not rolled back by a stale record of its manager: already_succeeded, nothing revoked,
// its kind kept, with or without its code's hash, from another manager, and after this hub restarted (the audit says).
func TestLinkSwitchRollbackAfterSuccessIsRefused(t *testing.T) {
	h, _, _ := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-bare", Operation: op, SSHHost: "bare.example"})
	if err != nil {
		t.Fatal(err)
	}
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d", code)
	}
	cred := out["token"].(string)
	if err := busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op, PairHash: got.PairHash,
		Outcome: devicewire.LinkSwitchSucceeded}); err != nil {
		t.Fatal(err)
	}
	other := &busConn{ident: db.BusIdentity{ID: "m-0d0d0d"}}
	tries := func() {
		t.Helper()
		for _, c := range []struct {
			name string
			conn *busConn
			hash string
			code string
		}{{"with its hash", nil, got.PairHash, devicewire.LinkSwitchCodeSucceeded}, {"without", nil, "", devicewire.LinkSwitchCodeSucceeded},
			{"from another manager", other, got.PairHash, devicewire.LinkSwitchCodeNoMachine}} {
			refusedWith(t, c.name, busEnd(h, c.conn, devicewire.LinkSwitchEnd{MachineID: "lsw-bare", Operation: op, PairHash: c.hash,
				Outcome: devicewire.LinkSwitchRolledBack}), c.code)
		}
		if _, ok := h.authenticate(ctx, cred); !ok || diskKind(t, h, "lsw-bare") != "link" || h.cfgEntry("lsw-bare").Kind != kindLink {
			t.Fatal("a succeeded switch was undone")
		}
	}
	tries()
	h.linkSwitch = linkSwitches{} // this hub restarted
	tries()
}

// While a computer switches, it is neither moved to another manager nor removed: 409 link_switch_in_progress, in the
// asker's language. Once the switch ended, both may go ahead.
func TestLinkSwitchHoldsTheComputer(t *testing.T) {
	h, _, _ := switchHub(t)
	ctx := context.Background()
	op := newSwitchOp()
	if _, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: op, SSHHost: "me@box.example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.moveMachine(ctx, "lsw-box", "m-0e0e0e"); !errors.Is(err, errLinkSwitchInProgress) {
		t.Fatalf("moved while switching: %v", err)
	}
	mux := http.NewServeMux()
	h.adminMachineRoutes(mux)
	w := langRecorder{httptest.NewRecorder(), i18n.Ja}
	mux.ServeHTTP(w, httptest.NewRequest("DELETE", "/api/v1/admin/machines/lsw-box", nil))
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body["code"] != "link_switch_in_progress" || body["error"] != i18n.T(i18n.Ja, "hub-link_switch.in_progress") {
		t.Fatalf("removed while switching: %d %v", w.Code, body)
	}
	if diskEntry(t, h, "lsw-box") == nil {
		t.Fatal("gone")
	}
	if err := busEnd(h, nil, devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: op, Outcome: devicewire.LinkSwitchRolledBack}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.moveMachine(ctx, "lsw-box", "m-0e0e0e"); errors.Is(err, errLinkSwitchInProgress) {
		t.Fatal("still held after the switch ended")
	}
	if w := adminMachinesRequest(h, "DELETE", "/api/v1/admin/machines/lsw-box", ""); w.Code != http.StatusOK {
		t.Fatalf("remove after the switch: %d %s", w.Code, w.Body.String())
	}
}

// langRecorder records an answer to a request in l (as the hub's request logger's writer carries it).
type langRecorder struct {
	*httptest.ResponseRecorder
	l i18n.Lang
}

func (r langRecorder) Lang() i18n.Lang { return r.l }

// Nor is the manager that carries it removed meanwhile (its computers would go to the carrier mid-switch).
func TestLinkSwitchHoldsItsManager(t *testing.T) {
	h, srv, store := managersHub(t)
	busToken(t, h, assistantwire.AssistantID, "assistant")
	busToken(t, h, "m-0c0c0c", "other")
	h.devices.mu.Lock()
	h.devices.entries, h.devices.started = []MachineConfig{{ID: "lsw-desk", Kind: kindLink}}, true
	h.devices.assign = map[string]string{"lsw-desk": "m-0c0c0c"}
	h.devices.mu.Unlock()
	op := newSwitchOp()
	if err := h.linkSwitch.claim("lsw-desk", &linkSwitchRun{operation: op, by: "device", until: time.Now().Add(time.Minute)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if code := managersDo(t, h, srv, "DELETE", "/api/v1/managers/m-0c0c0c", nil, &out); code != http.StatusConflict || out["code"] != "link_switch_in_progress" {
		t.Fatalf("removed while one of its computers switches: %d %v", code, out)
	}
	if b, _ := store.BusIdentityByID(context.Background(), "m-0c0c0c"); b == nil {
		t.Fatal("its identity went")
	}
	h.linkSwitch.finish("lsw-desk", linkSwitchEnded{operation: op, step: switchRolledBack})
	if code := managersDo(t, h, srv, "DELETE", "/api/v1/managers/m-0c0c0c", nil, nil); code != http.StatusOK {
		t.Fatalf("remove once the switch ended: %d", code)
	}
}

// An app's pairing code stands in for a full device until it is traded, but starts no switch; a signed-in full
// device's request reaches the computer's manager (away here).
func TestAPairingCodeStartsNoSwitch(t *testing.T) {
	if !slices.Contains(devicewire.Routes, linkSwitchRoute) {
		t.Fatalf("%q is not the manager's route", linkSwitchRoute)
	}
	h, _, _ := switchHub(t)
	code, err := h.newPairCode(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if status, out := call(t, h.routes(), "POST", "/api/v1/machines/lsw-box/link-switch", code.Token, `{}`); status != http.StatusForbidden || out["code"] != "forbidden" {
		t.Fatalf("an app's pairing code: %d %v", status, out)
	}
	if status, out := call(t, h.routes(), "POST", "/api/v1/machines/lsw-box/link-switch", h.cfg.Token, `{}`); status != http.StatusServiceUnavailable || out["code"] != "assistant_offline" {
		t.Fatalf("a full device: %d %v", status, out)
	}
}

// Right after a start the admin page's switch waits for the manager to report its machines, as the apps' routes wait
// for it, before it says there is no such computer.
func TestAdminLinkSwitchWaitsForTheManager(t *testing.T) {
	for _, owner := range []string{devicesOwner, "other-manager"} {
		t.Run(owner, func(t *testing.T) {
			h := newTestHub(t)
			h.cfg.Machines = []MachineConfig{{ID: "lsw-box", Kind: "ssh"}}
			h.devices.assign = map[string]string{"lsw-box": owner}
			h.devices.mu.Lock()
			// No up channel yet: waitDevicesFor makes it, which is how this test knows the request is waiting for the manager
			// (a lookup before the wait answers 404 at once and never makes it).
			h.devices.up, h.devices.warmUntil = nil, time.Now().Add(5*time.Second)
			h.devices.mu.Unlock()
			mux := http.NewServeMux()
			h.adminLinkSwitchRoutes(mux)
			done := make(chan int, 1)
			go func() {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/admin/machines/lsw-box/link-switch", strings.NewReader(`{}`)))
				done <- w.Code
			}()
			var up chan struct{}
			eventually(t, 5*time.Second, "the request waiting for the manager", func() bool {
				h.devices.mu.Lock()
				defer h.devices.mu.Unlock()
				up = h.devices.up[owner]
				return up != nil
			})
			m := newMachine(h, MachineConfig{ID: "lsw-box", Kind: "ssh"})
			h.fleet.mu.Lock()
			h.machines, h.byID["lsw-box"] = append(h.machines, m), m
			h.fleet.mu.Unlock()
			h.devices.mu.Lock()
			h.devices.upOpen[owner] = true
			h.devices.mu.Unlock()
			close(up)
			select {
			case status := <-done:
				if status == http.StatusNotFound {
					t.Fatal("answered no such computer before the manager reported")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no answer")
			}
		})
	}
}

// During startup the assigned manager must report its machine even when the carrier is already up.
func TestAdminLinkSwitchWaitsForAssignedManagerWithCarrierUp(t *testing.T) {
	h := newTestHub(t)
	const mid, owner = "lsw-box", "m-0e0e0e"
	h.cfg.Machines = []MachineConfig{{ID: mid, Kind: "ssh"}}
	h.devices.assign = map[string]string{mid: owner}
	h.devices.warmUntil = time.Now().Add(devicesWarmGrace)
	h.applyDeviceStatesFrom(devicesOwner, devicewire.States{})

	mux := http.NewServeMux()
	h.adminLinkSwitchRoutes(mux)
	r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/admin/machines/"+mid+"/link-switch", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(w, r)
		close(done)
	}()
	eventually(t, 5*time.Second, "the request waiting for the machine's manager", func() bool {
		select {
		case <-done:
			t.Fatalf("answered before the machine's manager reported: %d %s", w.Code, w.Body.String())
		default:
		}
		h.devices.mu.Lock()
		defer h.devices.mu.Unlock()
		return h.devices.up[owner] != nil
	})
	h.applyDeviceStatesFrom(owner, devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: mid}}}})
	select {
	case <-done:
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("after the report, want 503 from forwarding without a bus connection: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting after the machine's manager reported")
	}
}

// The whole hub side with a real manager and a real Persona Link connection: begin hands the manager the computer as
// kind link and its mirror follows; the code becomes a credential that connects; rolled_back with the code's hash
// revokes exactly that credential (its cached lookup too) and closes its link as revoked (4002), and the computer is
// on ssh again. A credential made for it otherwise and another computer's stay. Repeated, end answers the same.
func TestLinkSwitchRollbackClosesItsLink(t *testing.T) {
	switchOp := newSwitchOp() // the audit outlives a run, and an operation id is used once
	database := testDB(t)
	clearSwitchIDs(t, database)
	ctx := context.Background()
	h := newTestHub(t)
	h.store = database
	logs := &syncLog{}
	h.logger = log.New(logs, "", 0)
	h.cfg.Machines = []MachineConfig{{ID: "lsw-desk", Name: "Desk", Kind: kindLink}, {ID: "lsw-box", Name: "Box", Kind: "ssh", SSHHost: "box.example"}}
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"machines":[{"id":"lsw-desk","name":"Desk","kind":"link"},`+
		`{"id":"lsw-box","name":"Box","kind":"ssh","ssh_host":"box.example"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.devices.entries, h.devices.started = slices.Clone(h.cfg.Machines), true
	h.loadMachineTrust(ctx)
	h.trust.pending = map[string]time.Time{}
	srv := httptest.NewTLSServer(h.routes())
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	h.public.mu.Lock()
	h.public.port, h.public.fp = port, link.Pin(srv.Certificate())
	h.public.endpoints = []Endpoint{{URL: srv.URL, Kind: "lan", Port: port, Scheme: "https"}}
	h.public.mu.Unlock()
	h.cfg.PublicURL = "http://127.0.0.1:" + strconv.Itoa(port)
	// Over ssh, no node answers (no ssh in tests); as kind link, the manager waits for its Persona Link.
	startTestManagerDialing(t, h, srv, func(id string) func(context.Context) (net.Conn, error) {
		if mc := h.cfgEntry(id); mc == nil || kindOf(*mc) != "ssh" {
			return nil
		}
		return func(context.Context) (net.Conn, error) { return nil, errors.New("no ssh in tests") }
	}, "")
	m := mirror(t, h, "lsw-box")
	deskTok, _ := linkToken(t, h, "lsw-desk")

	got, err := busBegin(h, nil, devicewire.LinkSwitchBegin{MachineID: "lsw-box", Operation: switchOp, SSHHost: "box.example", By: "device"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "the manager reports it as kind link", func() bool { return m.kind() == kindLink })
	code, out := redeem(t, h, pairTokenOf(t, got.Link), `{}`)
	if code != 200 {
		t.Fatalf("redeem %d %v", code, out)
	}
	cred, credID := out["token"].(string), out["id"].(string)
	if p, ok := h.authenticate(ctx, cred); !ok || p.ID != credID { // its lookup is cached now
		t.Fatal("its credential does not work")
	}
	// Another credential for it, made as any pairing would while it is kind link: not this switch's.
	more, err := h.newPairCode(ctx, "lsw-box")
	if err != nil {
		t.Fatal(err)
	}
	code, out = redeem(t, h, more.Token, `{}`)
	if code != 200 {
		t.Fatalf("redeem the other code %d", code)
	}
	other := out["token"].(string)
	// The computer's persona-link connects with it, and its node answers through it.
	statuses, _, _ := runLink(t, link.Config{Hub: "hub", Machine: "lsw-box", FP: link.Pin(srv.Certificate()), Token: cred,
		Endpoints: []link.Endpoint{{URL: srv.URL, Kind: "lan"}}}, fakeResidentNode(t))
	eventually(t, 10*time.Second, "online through its Persona Link", func() bool { return m.snapshot().Online && linkVersion(m) != "" })

	end := devicewire.LinkSwitchEnd{MachineID: "lsw-box", Operation: switchOp, PairHash: got.PairHash, Outcome: devicewire.LinkSwitchRolledBack}
	if err := busEnd(h, nil, end); err != nil {
		t.Fatal(err)
	}
	// Closed as revoked (4002): persona-link says its credential was revoked, and stops trying.
	waitStatus(t, statuses, "its credential revoked", func(s link.Status) bool {
		return s.State == "refused" && s.Error == i18n.T(i18n.Default, "link-errors.credential_revoked")
	})
	if _, ok := h.authenticate(ctx, cred); ok {
		t.Fatal("its credential still works (a cached lookup)")
	}
	for name, tok := range map[string]string{"the other credential for it": other, "another computer's": deskTok} {
		if _, ok := h.authenticate(ctx, tok); !ok {
			t.Fatalf("%s was revoked", name)
		}
	}
	if k := diskKind(t, h, "lsw-box"); k != "ssh" {
		t.Fatalf("hub.json kind %v", k)
	}
	eventually(t, 10*time.Second, "the manager reports it as ssh again", func() bool { return kindOf(MachineConfig{Kind: m.kind()}) == "ssh" })
	if err := busEnd(h, nil, end); err != nil {
		t.Fatalf("again: %v", err)
	}
	if _, ok := h.authenticate(ctx, other); !ok {
		t.Fatal("repeating revoked more")
	}
	for _, secret := range []string{pairTokenOf(t, got.Link), got.Link, cred} {
		if logs.about(secret) > 0 {
			t.Fatal("the log has a secret")
		}
	}
}

// The admin page's twin of the manager's route: behind the admin sign-in and its CSRF token; relayed to the computer's
// manager as the manager's own route, told an admin session asks, without the admin's CSRF token; to the manager that
// carries the computer, not the default one.
func TestAdminLinkSwitchRoute(t *testing.T) {
	_, _, mux := adminTestHub(t)
	anon := &adminClient{t: t, h: mux, remote: ip4(192, 168, 1, 20) + ":1"}
	if status, out, _ := anon.do("POST", "/api/v1/admin/machines/lsw-box/link-switch", `{}`); status != 401 || out["code"] != "login_required" {
		t.Fatalf("not signed in: %d %v", status, out)
	}
	in := signedIn(t, mux, ip4(192, 168, 1, 20)+":1")
	csrf := in.csrf
	in.csrf = ""
	if status, out, _ := in.do("POST", "/api/v1/admin/machines/lsw-box/link-switch", `{}`); status != 403 || out["code"] != "csrf" {
		t.Fatalf("no CSRF token: %d %v", status, out)
	}
	in.csrf = csrf
	if status, _, _ := in.do("POST", "/api/v1/admin/machines/lsw-box/link-switch", `{}`); status != 404 {
		t.Fatalf("signed in, no such computer: %d", status)
	}

	h, srv, _ := newBusHub(t)
	got := make(chan assistantwire.HTTPRequest, 4)
	helloAssistant(t, h, srv, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP || json.Unmarshal(params, &req) != nil {
			return map[string]any{}, nil
		}
		got <- req
		return assistantwire.HTTPResponse{Status: 202, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"link_switch":{"available":false,"phase":"checking"}}`)}, nil
	})
	h.cfg.Machines = []MachineConfig{{ID: "lsw-box", Kind: "ssh", SSHHost: "box.example"}}
	mb := newMachine(h, h.cfg.Machines[0])
	h.machines, h.byID["lsw-box"] = append(h.machines, mb), mb
	signedInMux := http.NewServeMux()
	h.adminLinkSwitchRoutes(signedInMux)
	admin := &adminClient{t: t, h: signedInMux, remote: "127.0.0.1:1", csrf: "csrf-token", header: http.Header{assistantwire.AdminHeader: {"0"}}}
	status, out, _ := admin.do("POST", "/api/v1/admin/machines/lsw-box/link-switch", `{"note":1}`)
	if status != 202 || out["link_switch"] == nil {
		t.Fatalf("relayed answer %d %v", status, out)
	}
	req := <-got
	if req.Method != "POST" || req.Path != "/api/v1/machines/lsw-box/link-switch" || string(req.Body) != `{"note":1}` ||
		len(req.Header[assistantwire.AdminHeader]) != 1 || req.Header[assistantwire.AdminHeader][0] != "1" || req.Header["X-Persona-Csrf"] != nil {
		t.Fatalf("reached the manager as %+v", req)
	}
	// Carried by another manager (away): answered for it, the default one asked nothing.
	h.devices.mu.Lock()
	h.devices.assign = map[string]string{"lsw-box": "m-0e0e0e"}
	h.devices.mu.Unlock()
	if status, out, _ := admin.do("POST", "/api/v1/admin/machines/lsw-box/link-switch", `{}`); status != 503 || out["code"] != "assistant_offline" {
		t.Fatalf("its manager away: %d %v", status, out)
	}
	select {
	case req := <-got:
		t.Fatalf("the default manager was asked %+v", req)
	default:
	}
}

// The admin page's machines carry the manager's switch state.
func TestAdminMachinesCarryTheLinkSwitch(t *testing.T) {
	h, _, _ := switchHub(t)
	h.applyDeviceStates(devicewire.States{Machines: []devicewire.State{
		{Machine: proto.Machine{ID: "lsw-box", Kind: "ssh", LinkSwitch: &proto.MachineLinkSwitch{Available: true}}},
		{Machine: proto.Machine{ID: "lsw-bare", Kind: "ssh"}},
		{Machine: proto.Machine{ID: "lsw-link", Kind: kindLink}}}})
	for _, a := range h.adminMachines() {
		switch a.ID {
		case "lsw-box":
			if a.LinkSwitch == nil || !a.LinkSwitch.Available {
				t.Fatalf("lsw-box %+v", a.LinkSwitch)
			}
		case "lsw-bare":
			if a.LinkSwitch != nil {
				t.Fatalf("lsw-bare %+v", a.LinkSwitch)
			}
		}
	}
	b, _ := json.Marshal(h.adminMachines())
	if !strings.Contains(string(b), `"link_switch":{"available":true`) {
		t.Fatalf("the admin list %s", b)
	}
}

// The assigned manager must report even after a different manager has connected.
func TestAdminLinkSwitchWaitsForTheManagerAfterAnotherManagerReports(t *testing.T) {
	for _, owner := range []string{devicesOwner, perManagerOther} {
		t.Run(owner, func(t *testing.T) {
			h := newTestHub(t)
			const mid = "lsw-box"
			other := perManagerOther
			if owner == other {
				other = devicesOwner
			}
			h.cfg.Machines = []MachineConfig{{ID: mid, Kind: "ssh"}}
			h.devices.assign = map[string]string{mid: owner}
			h.devices.warmUntil = time.Now().Add(5 * time.Second)
			h.applyDeviceStatesFrom(other, devicewire.States{})
			mux := http.NewServeMux()
			h.adminLinkSwitchRoutes(mux)
			r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/admin/machines/"+mid+"/link-switch", strings.NewReader("{}"))
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { mux.ServeHTTP(w, r); close(done) }()
			eventually(t, 5*time.Second, "the request waiting for the machine's manager", func() bool {
				select {
				case <-done:
					t.Fatalf("answered before the machine's manager reported: %d %s", w.Code, w.Body.String())
				default:
				}
				h.devices.mu.Lock()
				defer h.devices.mu.Unlock()
				return h.devices.up[owner] != nil
			})
			h.applyDeviceStatesFrom(owner, devicewire.States{Machines: []devicewire.State{{Machine: proto.Machine{ID: mid}}}})
			select {
			case <-done:
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("after the report, want 503 from forwarding without a bus connection: %d %s", w.Code, w.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("still waiting after the machine's manager reported")
			}
		})
	}
}
