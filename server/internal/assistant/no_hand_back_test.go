package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

type stubNoHandBackStaleStore struct {
	resumable []proto.Interaction
}

func (st stubNoHandBackStaleStore) ExpireStaleInteractions(context.Context) ([]proto.Interaction, []proto.Interaction, error) {
	out := append([]proto.Interaction(nil), st.resumable...)
	return out, nil, nil
}

func TestPermissionNoHandBackIsTheManagersWord(t *testing.T) {
	no := false
	ctx := context.Background()

	ft := newFleetTest(t)
	s := ft.s
	m, ok := s.fleetMachine("mac")
	if !ok {
		t.Fatal("expected test machine mac")
	}

	ft.sessions("mac",
		proto.Session{SessionID: "term-claude", Agent: "claude", Cwd: "/work/repo", PaneID: "%1", Status: proto.StatusWorking},
		proto.Session{SessionID: "asst-codex", Agent: "codex", Cwd: m.assistantCwd(), Status: proto.StatusWorking},
		proto.Session{SessionID: "asst-flagged", Agent: "claude", Cwd: "/work/other", Assistant: true, Status: proto.StatusWorking},
		proto.Session{SessionID: "agy-agent", Agent: "agy", SourceKind: proto.SourceRuntime, SourceInstanceID: "agy", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "agy-custom", Agent: "agy", SourceKind: proto.SourceRuntime, SourceInstanceID: "agy-work", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "grok-agent", Agent: "grok", SourceKind: proto.SourceRuntime, SourceInstanceID: "grok", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "grok-chat-agent", Agent: "grok", SourceKind: proto.SourceRuntime, SourceInstanceID: "grok-chat", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "paseo-grok", Agent: "grok", SourceKind: proto.SourceRuntime, SourceInstanceID: "paseo", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "opencode-acp", Agent: "opencode", SourceKind: proto.SourceRuntime, SourceInstanceID: "opencode", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "opencode-tui", Agent: "opencode", SourceKind: proto.SourceRuntime, SourceInstanceID: "opencode-1234", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "dsh-agent", Agent: "dsh", SourceKind: proto.SourceRuntime, SourceInstanceID: "dsh", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "dsh-custom", Agent: "dsh", SourceKind: proto.SourceRuntime, SourceInstanceID: "dsh-files", Cwd: "/work/repo", Status: proto.StatusWorking},
		proto.Session{SessionID: "codex-remote", Agent: "codex", SourceKind: "codex_app_server", Cwd: "/work/repo", Status: proto.StatusWorking},
	)

	askNode := func(ix proto.Interaction) proto.Interaction {
		t.Helper()
		if ix.Kind == proto.IxPermission {
			ix.Secret = true // require the owner without forcing the connector-only NoHandBack rule
		}
		if ix.CreatedAt.IsZero() {
			ix.CreatedAt = time.Now().UTC()
		}
		if ix.ExpiresAt.IsZero() {
			ix.ExpiresAt = time.Now().Add(time.Hour).UTC()
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = s.nodeInteraction(ctx, ix)
		}()
		got := ft.waitPending(1)[0]
		if ix.Kind == proto.IxPermission {
			_ = s.resolve(ix.ID, proto.Resolution{Allow: &no})
		} else {
			_ = s.resolve(ix.ID, proto.Resolution{Dismiss: true})
		}
		<-done
		ft.waitPending(0)
		return got
	}

	// Interactive terminal and desktop-UI-backed permission cards keep hand-back (and drop any node-forged NoHandBack).
	for _, tc := range []struct {
		name string
		ix   proto.Interaction
	}{
		{
			name: "interactive claude terminal",
			ix:   proto.Interaction{ID: "ix_term", MachineID: "mac", SessionID: "term-claude", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", NoHandBack: true},
		},
		{
			name: "paseo hosting grok",
			ix:   proto.Interaction{ID: "ix_paseo", MachineID: "mac", SessionID: "paseo-grok", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "grok"},
		},
		{
			name: "opencode tui plugin runtime",
			ix:   proto.Interaction{ID: "ix_opencode_tui", MachineID: "mac", SessionID: "opencode-tui", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "opencode"},
		},
		{
			name: "opencode unlisted conservatively keeps hand-back",
			ix:   proto.Interaction{ID: "ix_opencode_unlisted", MachineID: "mac", SessionID: "opencode-unknown", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "opencode"},
		},
		{
			name: "dsh web runtime",
			ix:   proto.Interaction{ID: "ix_dsh", MachineID: "mac", SessionID: "dsh-agent", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "dsh"},
		},
		{
			name: "dsh custom runtime",
			ix:   proto.Interaction{ID: "ix_dsh_custom", MachineID: "mac", SessionID: "dsh-custom", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "dsh"},
		},
		{
			name: "codex joined desktop app",
			ix:   proto.Interaction{ID: "ix_codex_remote", MachineID: "mac", SessionID: "codex-remote", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "codex"},
		},
		{
			name: "script ask cli",
			ix:   proto.Interaction{ID: "ix_script", MachineID: "mac", SessionID: "term-claude", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "personad_ask", Agent: "script"},
		},
		{
			name: "non-permission question card",
			ix: proto.Interaction{
				ID: "ix_q", MachineID: "mac", SessionID: "agy-agent", Cwd: "/work/repo", Kind: proto.IxQuestion, Agent: "agy",
				Questions: []proto.Question{{Question: "Continue?", Options: []proto.QuestionOption{{Label: "Yes"}}}},
			},
		},
	} {
		got := askNode(tc.ix)
		if got.NoHandBack {
			t.Fatalf("%s: expected NoHandBack=false, got true", tc.name)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if strings.Contains(string(raw), "no_hand_back") {
			t.Fatalf("%s: expected no_hand_back omitted from JSON, got %s", tc.name, raw)
		}
	}

	// Headless assistant sessions, command gates, and headless ACP companions set NoHandBack=true.
	for _, tc := range []struct {
		name string
		ix   proto.Interaction
	}{
		{
			name: "claude headless assistant by cwd",
			ix:   proto.Interaction{ID: "ix_asst_cwd", MachineID: "mac", SessionID: "conv-claude", Cwd: m.assistantCwd(), Kind: proto.IxPermission, ToolName: "Bash"},
		},
		{
			name: "codex headless assistant by listed row",
			ix:   proto.Interaction{ID: "ix_asst_codex", MachineID: "mac", SessionID: "asst-codex", Cwd: "", Kind: proto.IxPermission, ToolName: "Bash", Agent: "codex"},
		},
		{
			name: "assistant row flag",
			ix:   proto.Interaction{ID: "ix_asst_flag", MachineID: "mac", SessionID: "asst-flagged", Cwd: "/work/other", Kind: proto.IxPermission, ToolName: "Bash"},
		},
		{
			name: "outward write command gate",
			ix:   proto.Interaction{ID: "ix_gate_outward", MachineID: "mac", SessionID: "conv-gate", Cwd: m.assistantCwd(), Kind: proto.IxPermission, ToolName: "飞书", OutwardWrite: true},
		},
		{
			name: "agy acp listed runtime",
			ix:   proto.Interaction{ID: "ix_agy_listed", MachineID: "mac", SessionID: "agy-agent", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "agy"},
		},
		{
			name: "agy acp custom runtime id (--runtime agy-work)",
			ix:   proto.Interaction{ID: "ix_agy_custom", MachineID: "mac", SessionID: "agy-custom", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "agy"},
		},
		{
			name: "agy acp before session row listed",
			ix:   proto.Interaction{ID: "ix_agy_unlisted", MachineID: "mac", SessionID: "agy-new", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "agy"},
		},
		{
			name: "grok acp listed runtime",
			ix:   proto.Interaction{ID: "ix_grok_listed", MachineID: "mac", SessionID: "grok-agent", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "grok"},
		},
		{
			name: "grok-chat production acp runtime",
			ix:   proto.Interaction{ID: "ix_grok_chat", MachineID: "mac", SessionID: "grok-chat-agent", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "grok"},
		},
		{
			name: "grok acp before session row listed",
			ix:   proto.Interaction{ID: "ix_grok_unlisted", MachineID: "mac", SessionID: "grok-new", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "grok"},
		},
		{
			name: "opencode builtin headless acp companion",
			ix:   proto.Interaction{ID: "ix_opencode_acp", MachineID: "mac", SessionID: "opencode-acp", Cwd: "/work/repo", Kind: proto.IxPermission, ToolName: "Bash", Agent: "opencode"},
		},
	} {
		got := askNode(tc.ix)
		if !got.NoHandBack {
			t.Fatalf("%s: expected NoHandBack=true, got false", tc.name)
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		if !strings.Contains(string(raw), `"no_hand_back":true`) {
			t.Fatalf("%s: expected no_hand_back:true in JSON, got %s", tc.name, raw)
		}
	}

	// Detached assistant command-gate card keeps CanClarify and sets NoHandBack.
	detached := detachedTestCard("ix_detached_no_hand_back")
	detached.Cwd = m.assistantCwd()
	detached.ExecutionCwd = m.assistantCwd()
	doneDetached := startDetachedTest(t, ft, detached)
	gotDetached := ft.waitPending(1)[0]
	if !gotDetached.NoHandBack || !gotDetached.CanClarify {
		t.Fatalf("detached command gate: NoHandBack=%v CanClarify=%v, want both true", gotDetached.NoHandBack, gotDetached.CanClarify)
	}
	_ = s.resolve(detached.ID, proto.Resolution{Allow: &no})
	awaitDetachedTest(t, doneDetached)
	ft.waitPending(0)

	// Codex TUI cards clear any forged NoHandBack both in codexPending and in syncCodexInteractions broadcasts/records.
	codexP := s.codexPending("mac", proto.Interaction{ID: "cx_1", MachineID: "mac", SessionID: "cx", Kind: proto.IxPermission, NoHandBack: true})
	if codexP.ix.NoHandBack {
		t.Fatal("codexPending must clear NoHandBack for terminal TUI cards")
	}
	listCodex(s, proto.Interaction{
		ID: "cx_sync_forged", MachineID: "mac", SessionID: "cx", Kind: proto.IxPermission, Secret: true,
		ToolName: "Bash", Summary: "ls", NoHandBack: true,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
	})
	toldCodex, toldN := toldCard(ft, "cx_sync_forged")
	if toldN != 1 || toldCodex.NoHandBack {
		t.Fatalf("syncCodexInteractions broadcast: count=%d NoHandBack=%v, want count=1 NoHandBack=false", toldN, toldCodex.NoHandBack)
	}
	var recCodex proto.Interaction
	okRec := false
	ft.st.fmu.Lock()
	for _, r := range ft.st.recs {
		if r.Interaction.ID == "cx_sync_forged" {
			recCodex, okRec = r.Interaction, true
		}
	}
	ft.st.fmu.Unlock()
	if !okRec || recCodex.NoHandBack {
		t.Fatalf("syncCodexInteractions recorded card: ok=%v NoHandBack=%v, want ok=true NoHandBack=false", okRec, recCodex.NoHandBack)
	}
	listCodex(s) // clear Codex pending card
	ft.waitPending(0)

	// Manager's own permission cards (askHub, s.ask, waitPreparedCard, OwnerProposal) all set NoHandBack=true.
	ownDone := make(chan struct{})
	go func() {
		defer close(ownDone)
		_, _ = s.AskCard(ctx, proto.Interaction{
			ID: "ix_own_hub", MachineID: "mac", SessionID: "asst-codex", Kind: proto.IxPermission,
			ToolName: "iphone_do", SessionTitle: "Persona", ExpiresAt: time.Now().Add(time.Hour),
		})
	}()
	gotOwn := ft.waitPending(1)[0]
	if !gotOwn.NoHandBack {
		t.Fatal("askHub permission card: expected NoHandBack=true")
	}
	_ = s.resolve("ix_own_hub", proto.Resolution{Allow: &no})
	<-ownDone
	ft.waitPending(0)

	prepDone := make(chan struct{})
	go func() {
		defer close(prepDone)
		_, _ = s.waitPreparedCard(ctx, proto.Interaction{
			ID: "ix_own_prep", MachineID: "mac", SessionID: "asst-codex", Kind: proto.IxPermission,
			ToolName: "persona_settings", ExpiresAt: time.Now().Add(time.Hour),
		}, approvalops.Operation{
			Apply: func(context.Context, proto.Resolution) (string, error) { return "applied", nil },
		})
	}()
	gotPrep := ft.waitPending(1)[0]
	if !gotPrep.NoHandBack || !gotPrep.CanClarify {
		t.Fatalf("prepared card: NoHandBack=%v CanClarify=%v, want both true", gotPrep.NoHandBack, gotPrep.CanClarify)
	}
	_ = s.resolve("ix_own_prep", proto.Resolution{Allow: &no})
	<-prepDone
	ft.waitPending(0)

	propDone := make(chan struct{})
	go func() {
		defer close(propDone)
		_, _ = s.waitInteraction(ctx, proto.Interaction{
			ID: "ix_owner_prop", MachineID: "mac", SessionID: "asst-codex", Kind: proto.IxPermission,
			ToolName: "mail_send", OwnerProposal: true, ApprovalBatchID: "oab_1",
			ExpiresAt: time.Now().Add(time.Hour),
		})
	}()
	gotProp := ft.waitPending(1)[0]
	if !gotProp.NoHandBack {
		t.Fatal("owner proposal permission card: expected NoHandBack=true")
	}
	_ = s.resolve("ix_owner_prop", proto.Resolution{Allow: &no})
	<-propDone
	ft.waitPending(0)

	// Rolling-upgrade restoreStoredInteractions backfills NoHandBack via permissionNoHandBack and preserves terminal cards.
	exp := time.Now().Add(time.Hour).UTC()
	resumable := []proto.Interaction{
		{ID: "ix_restore_grok", MachineID: "mac", SessionID: "grok-chat-agent", Cwd: "/work/repo", Kind: proto.IxPermission, Secret: true, ToolName: "Bash", Agent: "grok", Resumable: true, ExpiresAt: exp},
		{ID: "ix_restore_opencode_acp", MachineID: "mac", SessionID: "opencode-acp", Cwd: "/work/repo", Kind: proto.IxPermission, Secret: true, ToolName: "Bash", Agent: "opencode", Resumable: true, ExpiresAt: exp},
		{ID: "ix_restore_asst", MachineID: "mac", SessionID: "conv-claude", Cwd: m.assistantCwd(), Kind: proto.IxPermission, Secret: true, ToolName: "Bash", Resumable: true, ExpiresAt: exp},
		{ID: "ix_restore_term", MachineID: "mac", SessionID: "term-claude", Cwd: "/work/repo", Kind: proto.IxPermission, Secret: true, ToolName: "Bash", Resumable: true, ExpiresAt: exp},
		{ID: "ix_restore_opencode_tui", MachineID: "mac", SessionID: "opencode-tui", Cwd: "/work/repo", Kind: proto.IxPermission, Secret: true, ToolName: "Bash", Agent: "opencode", Resumable: true, ExpiresAt: exp},
	}
	for _, ix := range resumable {
		if err := ft.st.InsertInteraction(ctx, ix); err != nil {
			t.Fatalf("seed resumable %s: %v", ix.ID, err)
		}
	}
	s.restoreStoredInteractions(ctx, stubNoHandBackStaleStore{resumable: resumable})
	byID := map[string]proto.Interaction{}
	for _, ix := range ft.waitPending(5) {
		byID[ix.ID] = ix
	}
	for _, id := range []string{"ix_restore_grok", "ix_restore_opencode_acp", "ix_restore_asst"} {
		if !byID[id].NoHandBack {
			t.Fatalf("restoreStoredInteractions %s: expected NoHandBack=true, got false", id)
		}
	}
	for _, id := range []string{"ix_restore_term", "ix_restore_opencode_tui"} {
		if byID[id].NoHandBack {
			t.Fatalf("restoreStoredInteractions %s: expected NoHandBack=false, got true", id)
		}
	}
	for id := range byID {
		_ = s.resolve(id, proto.Resolution{Allow: &no})
	}
	ft.waitPending(0)

	// Reclaiming a resumable card that flips NoHandBack from false to true broadcasts the updated card immediately.
	reclaimCreatedAt := time.Now().UTC()
	s.fleet.mu.Lock()
	s.ixMapLocked()["ix_reclaim_hb"] = &pending{
		ix: proto.Interaction{
			ID: "ix_reclaim_hb", MachineID: "mac", SessionID: "agy-custom", Cwd: "/work/repo",
			Kind: proto.IxPermission, ToolName: "Bash", Agent: "agy", Resumable: true, Secret: true,
			NoHandBack: false, CreatedAt: reclaimCreatedAt, ExpiresAt: exp,
		},
		done: make(chan proto.Resolution, 1),
	}
	s.fleet.mu.Unlock()
	reclaimedReady := make(chan struct{})
	reclaimedDone := make(chan struct{})
	go func() {
		defer close(reclaimedDone)
		_, _ = s.waitInteractionReady(ctx, proto.Interaction{
			ID: "ix_reclaim_hb", MachineID: "mac", SessionID: "agy-custom", Cwd: "/work/repo",
			Kind: proto.IxPermission, ToolName: "Bash", Agent: "agy", Resumable: true, Secret: true,
			NoHandBack: true, CreatedAt: reclaimCreatedAt, ExpiresAt: exp,
		}, nil, reclaimedReady)
	}()
	<-reclaimedReady
	toldReclaimed, reclaimedN := toldCard(ft, "ix_reclaim_hb")
	if reclaimedN == 0 || !toldReclaimed.NoHandBack {
		t.Fatalf("resumable reclaim NoHandBack upgrade: count=%d NoHandBack=%v, want count>=1 NoHandBack=true", reclaimedN, toldReclaimed.NoHandBack)
	}
	_ = s.resolve("ix_reclaim_hb", proto.Resolution{Allow: &no})
	<-reclaimedDone
	ft.waitPending(0)
}
