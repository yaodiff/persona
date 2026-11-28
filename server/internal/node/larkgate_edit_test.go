package node

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

func newLarkEditRig(t *testing.T, answer func(proto.Interaction) proto.Resolution, user ...proto.LarkRule) *spaceRig {
	t.Helper()
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	api := newFakeAPI(t)
	hub := newScriptHub(t, func(_ context.Context, ix proto.Interaction) (any, error) {
		return answer(ix), nil
	}, notifyOK)
	n := hub.n
	n.mu.Lock()
	n.dir = t.TempDir()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL, user...)}}, time.Now()
	n.mu.Unlock()
	t.Cleanup(func() { n.gates().close() })
	return &spaceRig{t: t, n: n, api: api, hub: hub, state: filepath.Join(t.TempDir(), ".persona")}
}

// runLarkEditHook uses the real Claude Code PreToolUse Bash handler, including interactionID and askOnce. This is the
// only path that may declare editable_text; the shared larkCheck used by Codex and the runtimes is tested separately.
func runLarkEditHook(t *testing.T, r *spaceRig, cwd, command string, extra map[string]any) map[string]any {
	t.Helper()
	return runLarkEditHookForToolUse(t, r, cwd, command, "toolu_lark_edit", extra)
}

// runLarkEditHookForToolUse lets a detached retry arrive as a fresh runtime tool call.
func runLarkEditHookForToolUse(t *testing.T, r *spaceRig, cwd, command, toolUseID string, extra map[string]any) map[string]any {
	t.Helper()
	input := map[string]any{"command": command}
	for key, value := range extra {
		input[key] = value
	}
	raw, err := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: testThread, ToolName: "Bash",
		ToolUseID: toolUseID, Cwd: cwd, ToolInput: mustRaw(input)})
	if err != nil {
		t.Fatal(err)
	}
	r.n.mu.Lock()
	r.n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", cwd: cwd}
	r.n.mu.Unlock()
	out, err := r.n.handleHook(context.Background(), sockRequest{Kind: "hook", Input: raw, Attempt: "edit-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Fatal("the Lark hook left an eligible send to the ordinary permission flow")
	}
	spec, _ := out.(map[string]any)["hookSpecificOutput"].(map[string]any)
	if spec == nil {
		t.Fatalf("hook output has no hookSpecificOutput: %#v", out)
	}
	return spec
}

func mustRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func editedAllow(ix proto.Interaction, text string) proto.Resolution {
	res := proto.Resolution{Allow: boolp(true)}
	if ix.EditableText != "" {
		res.EditedText = text
	}
	return res
}

func TestLarkEditGrantKeepsTheApprovedAgentAccount(t *testing.T) {
	n := &Node{}
	gate := proxiedGate("http://example.invalid")
	gate.PathPrefix = "/agent/v1"
	cfg := proto.AssistantLark{Gates: []proto.CommandGate{gate}}
	call := larkCall{Gate: 0, Method: http.MethodPost, Path: "/conversations/F1/messages", Body: `{"text":"edited"}`, Account: "2"}
	if err := n.grantEdited(cfg, testThread, call, proto.LarkLetThrough{Kind: proto.LarkThroughCard}); err != nil {
		t.Fatal(err)
	}
	p := n.gates()
	t.Cleanup(p.close)
	key := gateKey{command: gate.Command, session: testThread}
	if _, ok := p.take(key, http.MethodPost, call.Path, "", "", []byte(call.Body), true); ok {
		t.Fatal("an edited added-account grant was taken by the primary account")
	}
	if _, ok := p.take(key, http.MethodPost, call.Path, "", "2", []byte(call.Body), true); !ok {
		t.Fatal("the edited added-account request lost its grant")
	}
}

func TestLarkEditHookRetiresOriginalAndGrantsOnlyEditedCallOnce(t *testing.T) {
	const original = "original text"
	edited := "Edited $HOME `printf nope` $(printf nope) \\\" ' ;\n第二行 😀"
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, edited) })
	cwd := t.TempDir()
	base := r.processIn(cwd, testThread)
	const body = `{"text":"original text","mentions":[{"key":"@_user_1","id":"ou_example"}],"large":9007199254740993}`
	const query = "receive_id_type=open_id&tag=a%20b"
	command := `ex POST '/conversations/F1/messages?` + query + `' '` + body + `'`
	spec := runLarkEditHook(t, r, cwd, command, map[string]any{
		"description": "send the reviewed update", "timeout": 120000, "run_in_background": true,
	})
	if spec["permissionDecision"] != "allow" {
		t.Fatalf("edited Allow = %#v", spec)
	}
	card := r.hub.lastCreated(t)
	if card.EditableText != original || card.Kind != proto.IxPermission {
		t.Fatalf("editable card = %#v", card)
	}
	updated, _ := spec["updatedInput"].(map[string]any)
	if updated == nil || updated["description"] != "send the reviewed update" || updated["timeout"] != float64(120000) ||
		updated["run_in_background"] != true || len(updated) != 4 {
		t.Fatalf("updatedInput did not preserve the other tool inputs: %#v", spec["updatedInput"])
	}
	updatedCommand, _ := updated["command"].(string)
	v := classifyLark(updatedCommand, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
	if len(v.calls) != 1 || v.other || v.unparsed || v.unknown || v.unsure || v.deny != "" {
		t.Fatalf("updated command is not the same one standalone call: %q\n%+v", updatedCommand, v)
	}
	call := v.calls[0]
	if call.Method != http.MethodPost || call.Path != "/conversations/F1/messages" || call.Query != query || call.Gate != 0 || call.CLI {
		t.Fatalf("edited call identity changed: %+v", call)
	}
	oldFields, _, ok := editableTextFields(body)
	if !ok {
		t.Fatal("test body is not editable")
	}
	newFields, newText, ok := editableTextFields(call.Body)
	if !ok || newText != edited {
		t.Fatalf("edited body text = %q, %v; body %s", newText, ok, call.Body)
	}
	for _, key := range []string{"mentions", "large"} {
		if string(newFields[key]) != string(oldFields[key]) {
			t.Errorf("%s changed from %s to %s", key, oldFields[key], newFields[key])
		}
	}
	url := base + "/api/v1/conversations/F1/messages"
	if code, response := send(t, http.MethodPost, url+"?receive_id_type=user_id&tag=a%20b", call.Body); code != http.StatusForbidden {
		t.Errorf("edited body with a changed query = %d %s, want 403", code, response)
	}
	if code, response := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusForbidden {
		t.Errorf("original body after edited Allow = %d %s, want its grant retired", code, response)
	}
	tampered := strings.Replace(call.Body, "ou_example", "ou_someone_else", 1)
	if code, response := send(t, http.MethodPost, url+"?"+query, tampered); code != http.StatusForbidden {
		t.Errorf("edited text with a changed mention = %d %s, want 403", code, response)
	}
	adjacentNumber := strings.Replace(call.Body, "9007199254740993", "9007199254740992", 1)
	if code, response := send(t, http.MethodPost, url+"?"+query, adjacentNumber); code != http.StatusForbidden {
		t.Errorf("a different large integer collapsed into the approved float64 body = %d %s, want 403", code, response)
	}
	expandedDollar := strings.Replace(call.Body, "$HOME", "/tmp", 1)
	if code, response := send(t, http.MethodPost, url+"?"+query, expandedDollar); code != http.StatusForbidden {
		t.Errorf("a '$' in edited text became a dynamic-body wildcard = %d %s, want 403", code, response)
	}
	if got := r.api.requests(); len(got) != 0 {
		t.Fatalf("an unapproved request reached the API: %+v", got)
	}
	if code, response := send(t, http.MethodPost, url+"?"+query, call.Body); code != http.StatusOK {
		t.Fatalf("the one edited call = %d %s, want 200", code, response)
	}
	if code, _ := send(t, http.MethodPost, url+"?"+query, call.Body); code != http.StatusForbidden {
		t.Errorf("the edited call repeated = %d, want 403", code)
	}
	got := r.api.requests()
	if len(got) != 1 || got[0].Query != query || got[0].Body != call.Body {
		t.Fatalf("upstream requests = %+v, want exactly the edited query and body once", got)
	}
	waitFor(t, "the edited sent-as-user report", func() bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return len(r.hub.larkSent) == 1
	})
	r.hub.mu.Lock()
	report := r.hub.larkSent[0]
	r.hub.mu.Unlock()
	if report.Text != edited || report.LetThrough.EditedFrom != original || report.LetThrough.InteractionID != card.ID ||
		report.LetThrough.Kind != proto.LarkThroughCard {
		t.Fatalf("sent report = %#v; card id %q", report, card.ID)
	}
}

func TestLarkEditAcceptsStaticShellQuoting(t *testing.T) {
	tests := []struct {
		name, body, wantText, command string
	}{
		{
			name:     "double quoted JSON",
			body:     `{"text":"hello"}`,
			wantText: "hello",
			command:  `ex POST /conversations/F1/messages "{\"text\":\"hello\"}"`,
		},
		{
			name:     "double quoted JSON with escaped newline and quotes",
			body:     `{"text":"line1\nline2 \"hi\""}`,
			wantText: "line1\nline2 \"hi\"",
			command:  `ex POST /conversations/F1/messages "{\"text\":\"line1\\nline2 \\\"hi\\\"\"}"`,
		},
		{
			name:     "double quoted JSON with literal backslash-n",
			body:     `{"text":"line1\nline2"}`,
			wantText: "line1\nline2",
			command:  `ex POST /conversations/F1/messages "{\"text\":\"line1\nline2\"}"`,
		},
		{
			name:     "single quote and Unicode in the original",
			body:     `{"text":"it's 雪😀"}`,
			wantText: "it's 雪😀",
			command:  `ex POST /conversations/F1/messages ` + shellQuote(`{"text":"it's 雪😀"}`),
		},
		{
			name:     "shellQuote with apostrophe, newline, quotes, backslash, dollar, and Chinese",
			body:     `{"text":"it's \"quoted\"\n第二行 C:\\tmp $5 😀"}`,
			wantText: "it's \"quoted\"\n第二行 C:\\tmp $5 😀",
			command:  `ex POST /conversations/F1/messages ` + shellQuote(`{"text":"it's \"quoted\"\n第二行 C:\\tmp $5 😀"}`),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, "changed") })
			cwd := t.TempDir()
			spec := runLarkEditHook(t, r, cwd, tc.command, nil)
			if spec["permissionDecision"] != "allow" {
				t.Fatalf("static shell quoting was refused: %#v", spec)
			}
			card := r.hub.lastCreated(t)
			if card.EditableText != tc.wantText {
				t.Fatalf("card.EditableText = %q, want %q", card.EditableText, tc.wantText)
			}
			updated, _ := spec["updatedInput"].(map[string]any)
			command, _ := updated["command"].(string)
			v := classifyLark(command, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
			if len(v.calls) != 1 {
				t.Fatalf("updated command %q parsed as %+v", command, v)
			}
			_, text, ok := editableTextFields(v.calls[0].Body)
			if !ok || text != "changed" {
				t.Fatalf("updated body = %q, %v", text, ok)
			}
		})
	}
}

func TestLarkEditRetiresTheGrantOfItsOwnCard(t *testing.T) {
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, "edited") })
	cwd := t.TempDir()
	r.processIn(cwd, testThread)
	call := larkCall{Gate: 0, Method: http.MethodPost, Path: "/conversations/F1/messages", Body: `{"text":"original"}`}
	r.n.gates().grant("ex", testThread, call, proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: "ix_another_card"})
	spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"original"}'`, nil)
	if spec["permissionDecision"] != "allow" {
		t.Fatalf("edited Allow = %#v", spec)
	}
	card := r.hub.lastCreated(t)
	p := r.n.gates()
	p.mu.Lock()
	defer p.mu.Unlock()
	var ownOriginal, otherOriginal, edited bool
	for _, grant := range p.grants {
		switch grant.through.InteractionID {
		case card.ID:
			ownOriginal = ownOriginal || grant.through.EditedFrom == ""
			edited = edited || grant.through.EditedFrom == "original"
		case "ix_another_card":
			otherOriginal = true
		}
	}
	if ownOriginal || !otherOriginal || !edited {
		t.Fatalf("grants after edit: own original %v, other card %v, edited %v; grants %+v", ownOriginal, otherOriginal, edited, p.grants)
	}
}

func TestLarkEditHookRetiresDetachedContinuationGrantByApprovalID(t *testing.T) {
	const origID = "ix_detached_claude_orig"
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Allow: boolp(true), ApprovalID: origID, EditedText: "edited after detach"}
	})
	cwd := t.TempDir()
	base := r.processIn(cwd, testThread)
	spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"original"}'`, nil)
	if spec["permissionDecision"] != "allow" {
		t.Fatalf("detached edited Allow = %#v", spec)
	}
	updated, _ := spec["updatedInput"].(map[string]any)
	command, _ := updated["command"].(string)
	v := classifyLark(command, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
	if len(v.calls) != 1 {
		t.Fatalf("updated command = %q (%+v)", command, v)
	}
	url := base + "/api/v1/conversations/F1/messages"
	if code, _ := send(t, http.MethodPost, url, `{"text":"original"}`); code != http.StatusForbidden {
		t.Fatalf("original body after detached edit = %d, want 403", code)
	}
	if code, _ := send(t, http.MethodPost, url, v.calls[0].Body); code != http.StatusOK {
		t.Fatalf("detached edited body = %d, want 200", code)
	}
	waitFor(t, "detached edited sent report", func() bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return len(r.hub.larkSent) == 1
	})
	r.hub.mu.Lock()
	report := r.hub.larkSent[0]
	r.hub.mu.Unlock()
	if report.Text != "edited after detach" || report.LetThrough.EditedFrom != "original" || report.LetThrough.InteractionID != origID {
		t.Fatalf("detached sent report = %#v, want InteractionID=%q EditedFrom=original", report, origID)
	}
}

func TestLarkEditHookUnchangedAndDenyAnswersKeepOrdinaryBehavior(t *testing.T) {
	t.Run("unchanged text uses the original grant without updatedInput", func(t *testing.T) {
		r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, "same") })
		cwd := t.TempDir()
		base := r.processIn(cwd, testThread)
		spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"same"}'`, nil)
		if spec["permissionDecision"] != "allow" || spec["updatedInput"] != nil {
			t.Fatalf("unchanged answer = %#v", spec)
		}
		url := base + "/api/v1/conversations/F1/messages"
		if code, _ := send(t, http.MethodPost, url, `{"text":"same"}`); code != http.StatusOK {
			t.Errorf("original confirmed call = %d, want 200", code)
		}
		if code, _ := send(t, http.MethodPost, url, `{"text":"same"}`); code != http.StatusForbidden {
			t.Errorf("original confirmed call repeated = %d, want 403", code)
		}
	})

	t.Run("deny note is the tool result and authorizes neither version", func(t *testing.T) {
		const note = "Use a concise status update instead"
		r := newLarkEditRig(t, func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(false), Message: note}
		})
		cwd := t.TempDir()
		base := r.processIn(cwd, testThread)
		spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"verbose"}'`, nil)
		if spec["permissionDecision"] != "deny" || spec["permissionDecisionReason"] != note || spec["updatedInput"] != nil {
			t.Fatalf("deny answer = %#v", spec)
		}
		for _, body := range []string{`{"text":"verbose"}`, `{"text":"concise"}`} {
			if code, _ := send(t, http.MethodPost, base+"/api/v1/conversations/F1/messages", body); code != http.StatusForbidden {
				t.Errorf("body %s after Deny = %d, want 403", body, code)
			}
		}
		if got := r.api.requests(); len(got) != 0 {
			t.Fatalf("a denied request reached the API: %+v", got)
		}
	})
}

func TestLarkEditEligibilityIsOnlyStaticAuthenticatedSendOrReply(t *testing.T) {
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, "changed") })
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "message.json"), []byte(`{"text":"from file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	absolute := filepath.Join(cwd, "message.json")
	tests := []struct {
		name, command string
		editable      bool
	}{
		{name: "send", command: `ex POST /conversations/F1/messages '{"text":"hello"}'`, editable: true},
		{name: "reply", command: `ex POST /messages/om_1/replies '{"text":"hello","replyMode":"thread"}'`, editable: true},
		{name: "reaction", command: `ex POST /messages/om_1/reactions '{"text":"hello"}'`},
		{name: "multiple calls", command: `ex POST /conversations/F1/messages '{"text":"one"}' && ex POST /conversations/F1/messages '{"text":"two"}'`},
		{name: "dynamic body", command: `ex POST /conversations/F1/messages "$BODY"`},
		{name: "relative file", command: `ex POST /conversations/F1/messages @message.json`},
		{name: "absolute file", command: `ex POST /conversations/F1/messages @` + absolute},
		{name: "nested content only", command: `ex POST /conversations/F1/messages '{"content":{"text":"hello"}}'`},
		{name: "duplicate text key", command: `ex POST /conversations/F1/messages '{"text":"one","text":"two"}'`},
		{name: "empty text", command: `ex POST /conversations/F1/messages '{"text":"  "}'`},
		{name: "lark cli", command: `lark-cli im +messages-send --text hello`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, got := r.n.editableLarkCall(r.n.larkCfg, tc.command, cwd)
			if got != tc.editable {
				t.Errorf("editableLarkCall(%q) = %v, want %v", tc.command, got, tc.editable)
			}
		})
	}
	r.n.larkCfg.Gates[0] = exampleGate(r.api.URL)
	if _, _, _, got := r.n.editableLarkCall(r.n.larkCfg, `ex POST /conversations/F1/messages '{"text":"hello"}'`, cwd); got {
		t.Error("a direct command whose credentials are not held by the proxy became editable")
	}
}

func TestSharedLarkCheckDoesNotDeclareEditingForCodexOrRuntimes(t *testing.T) {
	r := newSpaceRig(t)
	cwd := t.TempDir()
	if got := r.check(cwd, testThread, `ex POST /conversations/F1/messages '{"text":"hello"}'`); got != "allow" {
		t.Fatalf("shared gate decision = %q", got)
	}
	if card := r.hub.lastCreated(t); card.EditableText != "" {
		t.Fatalf("shared Codex/runtime path declared editable_text %q", card.EditableText)
	}
}

func TestLarkEditDeniedByTheSameRuleAfterApproval(t *testing.T) {
	blocked := proto.LarkRule{Method: http.MethodPost, Path: "/conversations/*/messages", Body: map[string]any{"text": "blocked"}, Action: "deny"}
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, "blocked") }, blocked)
	cwd := t.TempDir()
	base := r.processIn(cwd, testThread)
	spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"allowed"}'`, nil)
	if spec["permissionDecision"] != "deny" || spec["updatedInput"] != nil {
		t.Fatalf("edit into a deny rule = %#v", spec)
	}
	if cards := r.cards(); cards != 1 {
		t.Fatalf("edit validation created %d cards, want only the original", cards)
	}
	p := r.n.gates()
	p.mu.Lock()
	grants := len(p.grants)
	p.mu.Unlock()
	if grants != 0 {
		t.Fatalf("%d grants survived a denied edit; the original must be retired and no edited grant made", grants)
	}
	if code, _ := send(t, http.MethodPost, base+"/api/v1/conversations/F1/messages", `{"text":"allowed"}`); code != http.StatusForbidden {
		t.Errorf("original text after a denied edit = %d, want 403", code)
	}
	if got := r.api.requests(); len(got) != 0 {
		t.Fatalf("a denied edit reached the API: %+v", got)
	}
}

func TestLarkEditShellPayloadsStayInsideTheTextField(t *testing.T) {
	payloads := []string{
		`"; ex PUT /messages/om_1/replies '{"text":"other"}' #`,
		`'; ex POST /conversations/OTHER/messages '{"text":"other"}' #`,
		`$(ex POST /conversations/OTHER/messages '{"text":"other"}')`,
		"`ex POST /conversations/OTHER/messages '{\"text\":\"other\"}'`",
		"line one\nex DELETE /messages/om_1/recall",
		`","mentions":[{"id":"ou_other"}],"ignored":"x`,
		`$HOME \\ "quoted" 'single' 😀`,
	}
	for _, edited := range payloads {
		t.Run(edited, func(t *testing.T) {
			r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution { return editedAllow(ix, edited) })
			cwd := t.TempDir()
			spec := runLarkEditHook(t, r, cwd, `ex POST '/conversations/F1/messages?a=1' '{"text":"original","mentions":[]}'`, nil)
			if spec["permissionDecision"] != "allow" {
				t.Fatalf("valid text was not editable: %#v", spec)
			}
			updated, _ := spec["updatedInput"].(map[string]any)
			command, _ := updated["command"].(string)
			v := classifyLark(command, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
			if len(v.calls) != 1 || v.other || v.unparsed || v.unknown || v.unsure || v.deny != "" {
				t.Fatalf("text escaped into shell syntax: %q\n%+v", command, v)
			}
			call := v.calls[0]
			if call.Method != http.MethodPost || call.Path != "/conversations/F1/messages" || call.Query != "a=1" {
				t.Fatalf("payload changed call identity: %+v", call)
			}
			fields, text, ok := editableTextFields(call.Body)
			if !ok || text != edited || string(fields["mentions"]) != "[]" || len(fields) != 2 {
				t.Fatalf("payload changed another JSON field: %s", call.Body)
			}
		})
	}
}

func TestEditableTextFieldsRejectsAmbiguousJSON(t *testing.T) {
	valid := `{"text":"hello","large":9007199254740993,"mention":{"id":"ou_example"}}`
	fields, text, ok := editableTextFields(valid)
	if !ok || text != "hello" || string(fields["large"]) != "9007199254740993" {
		t.Fatalf("valid body = %#v, %q, %v", fields, text, ok)
	}
	for _, body := range []string{
		`{"text":"one","text":"two"}`,
		`{"text":"one","mentions":[],"mentions":[]}`,
		`["text"]`,
		`{"text":"one"} {"text":"two"}`,
		`{"text":"hi"}}`,
		`{"text":"hi"}]`,
		`{"text":null}`,
		`{"text":" "}`,
		`{"content":{"text":"nested"}}`,
		`{"text":"unterminated}`,
	} {
		if _, _, ok := editableTextFields(body); ok {
			t.Errorf("ambiguous or non-text body accepted: %s", body)
		}
	}
}

func TestLarkEditAppliesHotReloadedRulesAndGateReorderWithoutLeakingOriginalGrant(t *testing.T) {
	prevCLI := larkContextFromCLI
	larkContextFromCLI = func(context.Context, *Node, proto.AssistantLark, larkCall) larkCardContext {
		return larkCardContext{}
	}
	t.Cleanup(func() { larkContextFromCLI = prevCLI })

	t.Run("hot-reloaded deny rule during wait refuses edit with literal dollar and leaves zero grants", func(t *testing.T) {
		var r *spaceRig
		r = newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
			// Simulate a config refresh adding a deny rule while the card waits on the phone.
			r.n.mu.Lock()
			r.n.larkCfg.Gates[0].Rules = append([]proto.LarkRule{
				{Method: http.MethodPost, Path: "/conversations/*/messages", Body: map[string]any{"text": "hot-blocked"}, Action: "deny"},
			}, r.n.larkCfg.Gates[0].Rules...)
			r.n.mu.Unlock()
			return editedAllow(ix, "hot-blocked")
		})
		cwd := t.TempDir()
		base := r.processIn(cwd, testThread)
		spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"price $5"}'`, nil)
		if spec["permissionDecision"] != "deny" || spec["updatedInput"] != nil {
			t.Fatalf("hot-reloaded deny edit = %#v, want deny without updatedInput", spec)
		}
		p := r.n.gates()
		p.mu.Lock()
		grants := len(p.grants)
		p.mu.Unlock()
		if grants != 0 {
			t.Fatalf("%d grant(s) leaked after hot-reloaded deny edit", grants)
		}
		if code, _ := send(t, http.MethodPost, base+"/api/v1/conversations/F1/messages", `{"text":"price $5"}`); code != http.StatusForbidden {
			t.Fatalf("original literal-dollar body = %d, want 403", code)
		}
		if code, _ := send(t, http.MethodPost, base+"/api/v1/conversations/F1/messages", `{"text":"arbitrary"}`); code != http.StatusForbidden {
			t.Fatalf("wildcard body = %d, want 403", code)
		}
	})

	t.Run("hot-reloaded gate reorder during wait resolves gate by command name", func(t *testing.T) {
		var r *spaceRig
		r = newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
			// Reorder Gates so "ex" moves from index 0 to index 1 while the card waits.
			other := proxiedGate(r.api.URL)
			other.Command = "othercmd"
			r.n.mu.Lock()
			r.n.larkCfg.Gates = []proto.CommandGate{other, r.n.larkCfg.Gates[0]}
			r.n.mu.Unlock()
			return editedAllow(ix, "reordered ok")
		})
		cwd := t.TempDir()
		base := r.processIn(cwd, testThread)
		spec := runLarkEditHook(t, r, cwd, `ex POST /conversations/F1/messages '{"text":"before"}'`, nil)
		if spec["permissionDecision"] != "allow" {
			t.Fatalf("reordered gate edit = %#v, want allow", spec)
		}
		updated, _ := spec["updatedInput"].(map[string]any)
		cmd, _ := updated["command"].(string)
		v := classifyLark(cmd, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
		if len(v.calls) != 1 || v.calls[0].Gate != 1 {
			t.Fatalf("reordered updated command = %q (%+v)", cmd, v)
		}
		url := base + "/api/v1/conversations/F1/messages"
		if code, _ := send(t, http.MethodPost, url, `{"text":"before"}`); code != http.StatusForbidden {
			t.Fatalf("unedited original body = %d, want 403", code)
		}
		if code, _ := send(t, http.MethodPost, url, v.calls[0].Body); code != http.StatusOK {
			t.Fatalf("reordered edited call = %d, want 200", code)
		}
	})
}

func TestLarkEditExecutionWordEscapesAndFailClosedQuoting(t *testing.T) {
	parseLastWord := func(t *testing.T, cmd string) *syntax.Word {
		t.Helper()
		tree, _ := syntax.NewParser().Parse(strings.NewReader(cmd), "")
		if tree == nil || len(tree.Stmts) == 0 {
			t.Fatalf("parser produced no statements for %q", cmd)
		}
		call, ok := tree.Stmts[0].Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			t.Fatalf("parser produced no call args for %q", cmd)
		}
		return call.Args[len(call.Args)-1]
	}

	t.Run("static POSIX escapes outside and inside double quotes", func(t *testing.T) {
		cases := []struct {
			name string
			cmd  string
			want string
		}{
			{
				name: "double-quoted special escapes",
				cmd:  `echo "\" \\ \$ \` + "`" + ` "`,
				want: `" \ $ ` + "`" + ` `,
			},
			{
				name: "double-quoted preserved backslashes",
				cmd:  `echo "\n\t\r\'\a"`,
				want: `\n\t\r\'\a`,
			},
			{
				name: "double-quoted line continuation",
				cmd:  "echo \"line1\\\nline2\"",
				want: "line1line2",
			},
			{
				name: "unquoted line continuation",
				cmd:  "echo foo\\\nbar",
				want: "foobar",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, dyn := executionWord(parseLastWord(t, tc.cmd))
				if dyn || got != tc.want {
					t.Fatalf("executionWord(%q) = (%q, %v), want (%q, false)", tc.cmd, got, dyn, tc.want)
				}
			})
		}
	})

	t.Run("ANSI-C, locale, nested quotes, and dangling trailing backslash fail closed as dynamic", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			cmd  string
		}{
			{name: "ANSI-C single quote command", cmd: `$'l\x61rk-cli'`},
			{name: "ANSI-C single quote body", cmd: `ex $'{"text":"hi"}'`},
			{name: "locale double quote", cmd: `ex $"{\"text\":\"hi\"}"`},
			{name: "dangling backslash at EOF on command", cmd: `lark-c\li\`},
			{name: "dangling backslash at EOF on arg", cmd: `echo hi\`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, dyn := executionWord(parseLastWord(t, tc.cmd))
				if !dyn {
					t.Fatalf("executionWord(%q) reported dynamic=false, want true", tc.cmd)
				}
			})
		}

		// Unclosed double quote ending in backslash and programmatically nested quote nodes fail closed as dynamic.
		for _, tc := range []struct {
			name string
			part syntax.WordPart
		}{
			{name: "unclosed double quote ending in backslash", part: &syntax.Lit{Value: `foo\`}},
			{name: "nested SglQuoted inside DblQuoted", part: &syntax.SglQuoted{Value: "x"}},
			{name: "nested DblQuoted inside DblQuoted", part: &syntax.DblQuoted{Parts: []syntax.WordPart{&syntax.Lit{Value: "x"}}}},
		} {
			w := &syntax.Word{Parts: []syntax.WordPart{&syntax.DblQuoted{Parts: []syntax.WordPart{tc.part}}}}
			if _, dyn := executionWord(w); !dyn {
				t.Fatalf("%s reported dynamic=false, want true", tc.name)
			}
		}
	})

	t.Run("shellCommandUnknown, larkCheckOutcome bypass, classifyLark, and editableLarkCall fail closed", func(t *testing.T) {
		for _, cmd := range []string{
			`$'l\x61rk-cli' im +messages-send --text hi`,
			`$"lark-cli" im +messages-send --text hi`,
			`lark-c\li\`,
		} {
			if !shellCommandUnknown(cmd, false, 0) {
				t.Errorf("shellCommandUnknown(%q) = false, want true", cmd)
			}
		}

		r := newLarkEditRig(t, func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: boolp(false), Message: "blocked"}
		})
		cwd := t.TempDir()
		r.n.mu.Lock()
		r.n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", cwd: cwd}
		r.n.mu.Unlock()
		r.n.persistModes(testThread, proto.ModeBypass, "")

		for _, cmd := range []string{
			`$'l\x61rk-cli' im +messages-send --text hi`,
			`$"lark-cli" im +messages-send --text hi`,
			`lark-c\li\`,
		} {
			out, err := r.n.larkCheckOutcome(context.Background(), testThread, cwd, cmd, proto.Interaction{
				SessionID: testThread, Cwd: cwd, ToolName: "Bash",
			})
			if err != nil {
				t.Fatalf("larkCheckOutcome(%q): %v", cmd, err)
			}
			if out.decision != "deny" {
				t.Fatalf("larkCheckOutcome(%q) in bypass = %+v, want confirmation/deny (must not auto-allow)", cmd, out)
			}
		}

		for _, cmd := range []string{
			`echo hi\`,
			`echo "foo\`,
			`lark-c\li\`,
		} {
			v := classifyLark(cmd, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
			if !v.unparsed || !v.other {
				t.Errorf("classifyLark(%q) = %+v, want unparsed=true other=true", cmd, v)
			}
		}

		for _, cmd := range []string{
			`ex POST /conversations/F1/messages $'{"text":"hi"}'`,
			`ex POST /conversations/F1/messages $"{\"text\":\"hi\"}"`,
			`ex POST /conversations/F1/messages hi\`,
			`ex POST /conversations/F1/messages "foo\`,
		} {
			if _, _, _, ok := r.n.editableLarkCall(r.n.larkCfg, cmd, cwd); ok {
				t.Errorf("editableLarkCall(%q) = true, want false", cmd)
			}
		}
	})
}
