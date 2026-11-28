package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Without a BotMux Owner Union ID a send never speaks for the owner, even to a botmux that takes actor "owner": it is
// the plain external-event trigger, and the session says why (owner_unset), not "upgrade botmux".
func TestBotmuxOwnerUnsetSendsExternalTrigger(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.caps = []string{transcript.BotmuxCapOwnerActor, transcript.BotmuxCapOwnerUnionID}
	h := fs.makeHost("")
	h.setOwnerUnionID("")

	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	last := fs.triggers[len(fs.triggers)-1]
	if _, ok := last["actor"]; ok {
		t.Fatalf("no owner set: the trigger must not carry actor: %+v", last)
	}
	if _, ok := last["actorUnionId"]; ok {
		t.Fatalf("no owner set: the trigger must not carry actorUnionId: %+v", last)
	}
	if got := h.externalTriggerNotice(); got != proto.BotmuxTriggerOwnerUnset {
		t.Fatalf("notice = %q, want owner_unset", got)
	}

	// Setting one switches the next send to the owner, and the notice goes.
	h.setOwnerUnionID(bmxOwner)
	if got := h.externalTriggerNotice(); got != "" {
		t.Fatalf("notice with an owner = %q, want none", got)
	}
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "as owner"); err != nil {
		t.Fatalf("prompt as owner: %v", err)
	}
	last = fs.triggers[len(fs.triggers)-1]
	if last["actor"] != "owner" || last["actorUnionId"] != string(bmxOwner) {
		t.Fatalf("with an owner and ownerUnionId: want actor owner and actorUnionId, got %+v", last)
	}
}

// A botmux that takes actor "owner" but not actorUnionId (before the ownerUnionId capability) gets only actor: an
// unknown field could be refused with a 400. One older still (no ownerActor) gets neither.
func TestBotmuxOwnerUnionIDNeedsItsCapability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		caps      []string
		actor     any
		unionID   any
		wantNotes proto.BotmuxExternalTriggerReason
	}{
		{"ownerActor only", []string{transcript.BotmuxCapOwnerActor}, "owner", nil, ""},
		{"ownerUnionId without ownerActor", []string{transcript.BotmuxCapOwnerUnionID}, nil, nil, proto.BotmuxTriggerUpgrade},
		{"neither", []string{}, nil, nil, proto.BotmuxTriggerUpgrade},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeBotmuxTurnsServer(t)
			fs.caps = tc.caps
			h := fs.makeHost("")
			if err := h.Prompt(context.Background(), host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello"); err != nil {
				t.Fatalf("prompt: %v", err)
			}
			last := fs.triggers[len(fs.triggers)-1]
			if last["actor"] != tc.actor || last["actorUnionId"] != tc.unionID {
				t.Fatalf("trigger = %+v, want actor %v actorUnionId %v", last, tc.actor, tc.unionID)
			}
			if got := h.externalTriggerNotice(); got != tc.wantNotes {
				t.Fatalf("notice = %q, want %q", got, tc.wantNotes)
			}
		})
	}
}

// botmux's 403 owner_unresolved / owner_mismatch refuses the owner, not the token: one POST, no dashboard refresh, and
// a refusal that says which (reason), worded by the hub, never the generic "try again shortly".
func TestBotmuxOwnerRefusalIsClearAndNotRetried(t *testing.T) {
	for code, reason := range map[string]string{
		"owner_unresolved": botmuxOwnerUnresolvedReason,
		"owner_mismatch":   botmuxOwnerMismatchReason,
		"owner_disabled":   botmuxOwnerRefusedReason,
	} {
		t.Run(code, func(t *testing.T) {
			fs := newFakeBotmuxTurnsServer(t)
			fs.caps = []string{transcript.BotmuxCapOwnerActor, transcript.BotmuxCapOwnerUnionID}
			fs.triggerStatus, fs.triggerErr = http.StatusForbidden, code
			h := fs.makeHost("")
			dashboards := 0
			run := h.run
			h.run = func(ctx context.Context, args ...string) ([]byte, error) {
				if len(args) > 0 && args[0] == "dashboard" {
					dashboards++
				}
				return run(ctx, args...)
			}
			err := h.Prompt(context.Background(), host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello")
			if !errors.Is(err, host.ErrNotDelivered) {
				t.Fatalf("err = %v, want not delivered", err)
			}
			if len(fs.triggers) != 1 {
				t.Fatalf("an owner refusal must not be retried: %d POSTs", len(fs.triggers))
			}
			if dashboards != 1 {
				t.Fatalf("an owner refusal must not refresh the dashboard token: %d dashboard reads", dashboards)
			}
			var re *rpc.Error
			if !errors.As(deliveryRefusal(err), &re) || re.Code != notDelivered {
				t.Fatalf("refusal = %v, want not_delivered", deliveryRefusal(err))
			}
			var data map[string]any
			_ = json.Unmarshal(re.Data, &data)
			if data["reason"] != reason || data["delivered"] != deliveredNone {
				t.Fatalf("refusal data = %v, want reason %q and delivered none", data, reason)
			}
			if strings.Contains(re.Message, "稍后再发") {
				t.Fatalf("owner refusal must not read as the generic retry: %q", re.Message)
			}
		})
	}

	// A 403 without an owner code is still a stale token: refreshed and tried once more.
	fs := newFakeBotmuxTurnsServer(t)
	fs.caps = []string{transcript.BotmuxCapOwnerActor}
	fs.triggerStatus = http.StatusForbidden
	h := fs.makeHost("")
	if err := h.Prompt(context.Background(), host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello"); !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("err = %v, want not delivered", err)
	}
	if len(fs.triggers) != 2 {
		t.Fatalf("a plain 403 is retried once after a refresh: %d POSTs", len(fs.triggers))
	}
}

// node.configure hands the id to the botmux host, also to one made later (engine change, botmux turned on); anything
// that is not a union_id clears it; a call without the key leaves it.
func TestBotmuxOwnerConfigure(t *testing.T) {
	n, _, _, _ := bmxNode(t)
	h := n.hosts.Get(host.Botmux).(*botmuxHost)
	if _, err := request(t, n, "node.configure", map[string]any{"botmux_owner_union_id": " " + string(bmxOwner) + " "}); err != nil {
		t.Fatal(err)
	}
	if got := h.ownerUnionID(); got != bmxOwner {
		t.Fatalf("owner = %q, want %q", got, bmxOwner)
	}
	if _, err := request(t, n, "node.configure", map[string]any{"botmux": true}); err != nil {
		t.Fatal(err)
	}
	if got := h.ownerUnionID(); got != bmxOwner {
		t.Fatalf("a call without the key changed the owner to %q", got)
	}
	later := &botmuxHost{}
	n.eng.mu.Lock()
	n.hosts.Set(later)
	n.syncBotmuxOwner()
	n.eng.mu.Unlock()
	if got := later.ownerUnionID(); got != bmxOwner {
		t.Fatalf("a new botmux host got owner %q, want %q", got, bmxOwner)
	}
	if _, err := request(t, n, "node.configure", map[string]any{"botmux_owner_union_id": "ou_not_a_union_id"}); err != nil {
		t.Fatal(err)
	}
	if got := later.ownerUnionID(); got != "" {
		t.Fatalf("an invalid id must clear the owner, got %q", got)
	}
}

// The scan reads only union_ids from bots.json: the ownerUnionId and allowedUsers' on_… entries, deduplicated, those a
// bot names as its only person first. Secrets, open_ids, e-mail addresses and the path never reach the answer.
func TestBotmuxOwnerCandidatesScan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bots.json")
	bots := []map[string]any{
		{"larkAppId": "cli_a", "larkAppSecret": "s3cr3t-a", "allowedUsers": []any{"on_alice"}, "ownerOpenId": "ou_alice"},
		{"larkAppId": "cli_b", "larkAppSecret": "s3cr3t-b", "allowedUsers": []any{"on_alice"}},
		{"larkAppId": "cli_c", "larkAppSecret": "s3cr3t-c", "allowedUsers": []any{"on_bob", "carol@example.com", "on_alice", 7}},
		{"larkAppId": "cli_d", "ownerUnionId": "on_dave", "allowedUsers": []any{"on_bob", "on_dave"}, "env": map[string]any{"TOKEN": "on_fake"}},
		{"larkAppId": "cli_e", "ownerUnionId": "bad value", "allowedUsers": []any{"on_x y"}},
	}
	b, _ := json.Marshal(bots)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	got := botmuxOwnerCandidates(path)
	want := []proto.BotmuxOwnerCandidate{
		{UnionID: "on_alice", Bots: 3, Sole: 2},
		{UnionID: "on_dave", Bots: 1, Sole: 1},
		{UnionID: "on_bob", Bots: 2, Sole: 0},
	}
	if !reflect.DeepEqual(got.Candidates, want) {
		t.Fatalf("candidates = %+v, want %+v", got.Candidates, want)
	}
	raw, _ := json.Marshal(got)
	for _, secret := range []string{"s3cr3t", "ou_alice", "example.com", "cli_", "on_fake", dir} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the answer leaks %q: %s", secret, raw)
		}
	}
	before, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before, b) {
		t.Fatal("the scan changed bots.json")
	}

	// {"bots": [...]} is read the same; a missing, oversized or broken file is no candidates.
	wrapped, _ := json.Marshal(map[string]any{"bots": bots[:1]})
	_ = os.WriteFile(path, wrapped, 0o600)
	if got := botmuxOwnerCandidates(path).Candidates; len(got) != 1 || got[0].UnionID != "on_alice" {
		t.Fatalf("wrapped bots = %+v", got)
	}
	_ = os.WriteFile(path, []byte("{not json"), 0o600)
	if got := botmuxOwnerCandidates(path).Candidates; got == nil || len(got) != 0 {
		t.Fatalf("broken file = %+v, want an empty list", got)
	}
	if got := botmuxOwnerCandidates(filepath.Join(dir, "missing.json")).Candidates; got == nil || len(got) != 0 {
		t.Fatalf("missing file = %+v, want an empty list", got)
	}
	big := make([]byte, botmuxBotsMaxBytes+1)
	_ = os.WriteFile(path, big, 0o600)
	if got := botmuxOwnerCandidates(path).Candidates; len(got) != 0 {
		t.Fatalf("oversized file = %+v, want an empty list", got)
	}
}

// BOTS_CONFIG names the exact file botmux loads; else ~/.botmux/bots.json.
func TestBotmuxBotsPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BOTS_CONFIG", "")
	if got := botmuxBotsPath(); got != filepath.Join(home, ".botmux", "bots.json") {
		t.Fatalf("default path = %q", got)
	}
	custom := filepath.Join(home, "alt", "bots.json")
	t.Setenv("BOTS_CONFIG", custom)
	if got := botmuxBotsPath(); got != custom {
		t.Fatalf("BOTS_CONFIG path = %q, want %q", got, custom)
	}
}

// The method is answered through the node's method table: a computer without botmux has no candidates; with it, its
// bots.json's.
func TestBotmuxOwnerCandidatesMethod(t *testing.T) {
	n, _ := testNode(t)
	oldBin := botmuxBin
	t.Cleanup(func() { botmuxBin = oldBin })
	botmuxBin = func() string { return "" }
	got, err := request(t, n, proto.MethodBotmuxOwnerCandidates, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if list, ok := got["candidates"].([]any); !ok || len(list) != 0 {
		t.Fatalf("without botmux: %v", got)
	}
	path := filepath.Join(t.TempDir(), "bots.json")
	_ = os.WriteFile(path, []byte(`[{"larkAppSecret":"x","allowedUsers":["on_solo"]}]`), 0o600)
	t.Setenv("BOTS_CONFIG", path)
	botmuxBin = func() string { return "/usr/local/bin/botmux" }
	got, err = request(t, n, proto.MethodBotmuxOwnerCandidates, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if string(raw) != `{"candidates":[{"bots":1,"sole":1,"union_id":"on_solo"}]}` {
		t.Fatalf("with botmux: %s", raw)
	}
}

// botmux puts its code in errorCode and its words in error (deepcoldy/botmux#3954); an answer with only a bare code in
// error is read the same, and words alone are no code.
func TestBotmuxTriggerAnswerCode(t *testing.T) {
	for _, tc := range []struct {
		raw, code string
	}{
		{`{"ok":false,"errorCode":"owner_mismatch","error":"actorUnionId does not name this bot's owner"}`, "owner_mismatch"},
		{`{"ok":false,"error":"owner_unresolved"}`, "owner_unresolved"},
		{`{"ok":false,"error":"bot owner is not configured"}`, ""},
		{`{"ok":true}`, ""},
	} {
		var a botmuxTriggerAnswer
		if err := json.Unmarshal([]byte(tc.raw), &a); err != nil {
			t.Fatal(err)
		}
		if got := a.code(); got != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.raw, got, tc.code)
		}
	}
}
