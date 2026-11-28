package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A launcher of the other agent kind picked for a message in an existing conversation is refused with
// launcher_other_kind, not replaced on the node while the app shows the pick (#4662). Same-kind picks, the Main chat
// setting (nothing picked), and unknown agent kinds still go through.
func TestSendRefusesALauncherOfAnotherAgentKind(t *testing.T) {
	ctx := context.Background()
	s, f := mainChatFixture(t)
	// "nope" below is a launcher the list never has: its send waits out the launcher recovery, 30 s by default, for
	// nothing. One second, polled fast, checks the same thing.
	fastLauncherRecovery(t)
	setLauncherRecovery(f, 1, nil)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw"})
	setSettings(t, s, func(v *Settings) { v.Main = "main" })
	f.setSessions("server",
		proto.Session{SessionID: "main", Agent: "codex", Launcher: "cx", Assistant: true},
		proto.Session{SessionID: "claude-side", Agent: "claude", Launcher: "claude", Assistant: true},
		proto.Session{SessionID: "codex-side", Agent: "codex", Launcher: "cx", Assistant: true},
		proto.Session{SessionID: "rt-side", Agent: "agy", Launcher: "agy-rt", Assistant: true, SourceKind: proto.SourceRuntime, SourceInstanceID: "agy"})
	f.launcherList = append(f.launcherList,
		proto.Launcher{Name: "agy", Source: "agy", Agent: "agy", Assistant: true},
		proto.Launcher{Name: "unknown-rt", Source: "example-runtime", Agent: "unknown", Assistant: true})
	f.answerNode("assistant.segment.check", func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			Launcher string `json:"launcher"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Launcher == "unknown-rt" {
			return segmentTarget{Registered: true, Prepare: true, Engine: "unknown|runtime:example-runtime"}, nil
		}
		return segmentTarget{Registered: true, Prepare: true, Engine: "agy|runtime:agy"}, nil
	})
	send := func(sid, launcher, lang string) (map[string]any, error) {
		p := map[string]any{"text": "hi"}
		if launcher != "" {
			p["launcher"] = launcher
		}
		return s.SendParams(ctx, SendParams{MachineID: "server", SessionID: sid, Lang: lang, Params: p})
	}
	for _, c := range []struct{ sid, launcher, conv, picked string }{
		{"claude-side", "cx", "claude", "codex"},    // a machine's Codex launcher in a Claude conversation
		{"codex-side", "claude", "codex", "claude"}, // plain claude in a Codex one
		{"codex-side", "gw", "codex", "claude"},     // a managed Claude launcher
		{"main", "gw", "codex", "claude"},           // picked in the main chat's chip
		{"rt-side", "cx", "agy", "codex"},
		{"codex-side", "agy-rt", "codex", "agy"},
		{"claude-side", "agy", "claude", "agy"},
	} {
		_, err := send(c.sid, c.launcher, "en")
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != assistantwire.CodeLauncherOtherKind {
			t.Fatalf("%s picked %s: %v", c.sid, c.launcher, err)
		}
		var data map[string]string
		if err := json.Unmarshal(re.Data, &data); err != nil || data["conversation_agent"] != c.conv || data["launcher"] != c.launcher ||
			data["launcher_agent"] != c.picked || data["delivered"] != "none" {
			t.Fatalf("%s picked %s: data %s (%v)", c.sid, c.launcher, re.Data, err)
		}
		if !strings.Contains(re.Message, c.launcher) || !strings.Contains(re.Message, "wasn't sent") {
			t.Fatalf("%s picked %s: message %q", c.sid, c.launcher, re.Message)
		}
	}
	if _, err := send("claude-side", "cx", "zh-Hans"); err == nil || !strings.Contains(err.Error(), "「cx」跑的是 Codex") || !strings.Contains(err.Error(), "跑在 Claude Code 上") {
		t.Fatalf("localized: %v", err)
	}
	if _, err := send("rt-side", "cx", "zh-Hans"); err == nil || !strings.Contains(err.Error(), "跑在 Antigravity 上") {
		t.Fatalf("localized Antigravity name: %v", err)
	}
	for _, c := range []struct{ sid, launcher string }{
		{"claude-side", "gw"}, // same kind
		{"codex-side", "cx"},  // same kind
		{"rt-side", "agy-rt"},
		{"rt-side", "agy"},
		{"not-listed", "cx"},    // a conversation the machine doesn't list (yet)
		{"main", ""},            // the Main chat setting, nothing picked: the node's swap is the backstop
		{"claude-side", "nope"}, // a launcher the list doesn't have: not refused on a guess
	} {
		p, err := send(c.sid, c.launcher, "en")
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == assistantwire.CodeLauncherOtherKind {
			t.Fatalf("%s with %q refused: %v", c.sid, c.launcher, err)
		}
		if c.sid == "rt-side" && (err != nil || p["launcher"] != c.launcher) {
			t.Fatalf("%s with %q: %v %v", c.sid, c.launcher, p, err)
		}
	}
	if _, err := send("rt-side", "unknown-rt", "en"); err == nil {
		t.Fatal("an unidentified runtime was allowed to resume another connection")
	}
	f.launcherList = nil // the machine's list can't be read: its Codex alias isn't known to be one
	s.invalidateLauncherListCache("server")
	if _, err := send("claude-side", "cx", "en"); err != nil && strings.Contains(err.Error(), "wasn't sent") && strings.Contains(err.Error(), "Codex") {
		t.Fatalf("refused without the list: %v", err)
	}
}
