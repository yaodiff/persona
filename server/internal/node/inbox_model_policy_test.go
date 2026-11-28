package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Independent of the production list: a mutation that puts SendMessage back must fail this exact comparison.
const wantInboxClaudeBuiltins = "Bash,Read,Write,Edit,Monitor,Skill,Agent,ListAgents,TaskStop,WebFetch,WebSearch,ToolSearch"

// A native child Agent's MCP server is a descendant of the same assistant process. Even if it names another session,
// the node resolves it to the parent and attests the parent's Inbox origin to the manager.
func TestInboxNativeChildMCPInheritsParentOrigin(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer process here")
	}
	n, hub := runtimeNode(t)
	ask := mcpRelay(t, n, hub)
	self := &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	n.mu.Lock()
	n.procs["parent-session"] = &assistantProc{id: "parent-session", origin: originInbox, cmd: self}
	n.mu.Unlock()
	call := mcpCall(true, "child-supplied-session")
	got := ask(call)
	if got["assistant"] != true || got["session_id"] != "parent-session" || got["origin"] != originInbox {
		t.Fatalf("native child MCP call relayed as %v, want the parent's Inbox identity", got)
	}
}

// Codex's temporary process key and its eventual thread id must resolve to the same origin during startup, before the
// manager's origin book can contain the new thread.
func TestInboxCodexStartupKeyKeepsOrigin(t *testing.T) {
	empty := &Node{}
	if got := empty.assistantOrigin("not-started"); got != "" {
		t.Fatalf("a node without loaded session state returned origin %q", got)
	}
	n, _ := testNode(t)
	p := &assistantProc{id: "thread-session", origin: originInbox, cx: &codexConn{key: "proc-inbox"}}
	n.mu.Lock()
	n.akeys = map[string]*assistantProc{"proc-inbox": p}
	n.mu.Unlock()
	for _, id := range []string{"proc-inbox", "thread-session"} {
		if got := n.assistantOrigin(id); got != originInbox {
			t.Fatalf("assistantOrigin(%q)=%q, want inbox", id, got)
		}
	}
}

// The built-in Claude CLI receives non-bypass default through its permission-mode control, even when its start asks
// for bypass. Unlike Plan, default allows the explicit Persona MCP rules to run investigation and inbox_suggest
// without a permission prompt. Origin is recorded before the first turn and re-attested by the manager on later sends,
// so a node restart cannot silently return the conversation to bypass or lose its policy identity. (Only bypass is a
// startup flag; other modes use the control request.)
func TestInboxClaudeUsesDefaultAndPersistsOrigin(t *testing.T) {
	f := cliTest(t)
	// No model fingerprint: filling the launcher's default execution must not discard the start's attested origin.
	execution := modelExecution{Origin: originInbox}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "Investigate", nil, proto.ModeBypass, "", execution)
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	in := f.file("stdin.jsonl")
	if !strings.Contains(in, `"subtype":"set_permission_mode"`) || !strings.Contains(in, `"mode":"default"`) {
		t.Fatalf("Claude control requests did not select non-bypass default: %s", in)
	}
	argv := f.file("cli-argv")
	if !strings.Contains(argv, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(argv, "SendMessage") {
		t.Fatalf("Inbox built-ins must retain investigation tools but exclude cross-Agent SendMessage: %s", argv)
	}
	if !strings.Contains(argv, assistantAllowedTools) {
		t.Fatalf("Persona's own MCP tools lost their permission allowlist: %s", argv)
	}
	st, ok := f.n.state.get(id)
	if !ok || st.Origin != originInbox || !st.OriginKnown || st.AppMode != "default" {
		t.Fatalf("persisted session state=%+v present=%v", st, ok)
	}
	p := f.proc(id)
	p.mu.Lock()
	processOrigin, executionOrigin := p.origin, p.model.Origin
	p.mu.Unlock()
	if processOrigin != originInbox || executionOrigin != originInbox {
		t.Fatalf("spawned process origin=%q execution origin=%q, want inbox", processOrigin, executionOrigin)
	}
	if got := f.n.assistantOrigin(id); got != originInbox {
		t.Fatalf("live process origin=%q, want inbox", got)
	}
}

// Inbox may read local files outside its working directory, including project instructions and installed Skills.
// Directory-specific rules keep their linked-root coverage. Network, shell and mutation tools get no bare allow,
// ordinary conversations keep the old boundary, and Claude's health denies stay in force.
func TestInboxClaudeReadOnlyLocalResourceRules(t *testing.T) {
	n, home := testNode(t)
	n.dir = filepath.Join(home, ".persona")
	cwd := filepath.Join(n.dir, "assistant")
	skillRoot := filepath.Join(home, ".claude", "skills")
	linkedSkill := filepath.Join(home, "skill-checkout")
	linkedData := filepath.Join(home, "knowledge")
	for _, dir := range []string{skillRoot, linkedSkill, linkedData, n.dir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(linkedSkill, filepath.Join(skillRoot, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkedData, filepath.Join(n.dir, "data")); err != nil {
		t.Fatal(err)
	}
	// A link's target is allowed by its physical path: macOS's temporary directory is behind a link.
	for _, dir := range []*string{&linkedSkill, &linkedData} {
		physical, err := filepath.EvalSymlinks(*dir)
		if err != nil {
			t.Fatal(err)
		}
		*dir = physical
	}
	const id = "inbox-read-only-resources"
	n.state.put(id, sessionState{Origin: originInbox})
	flags := n.claudeAssistantMCPAndTools(id, cwd, true)
	allowed := strings.Split(mcpFlag(t, flags, "--allowedTools"), ",")
	if !slices.Contains(allowed, "Read") {
		t.Errorf("Inbox --allowedTools lacks the bare Read rule needed by project files and other installed Skill roots: %q", allowed)
	}
	for _, tool := range []string{"WebFetch", "WebSearch", "Bash", "Write", "Edit"} {
		if slices.Contains(allowed, tool) {
			t.Errorf("Inbox gained a bare %q allow rule: %q", tool, allowed)
		}
	}
	fileRule := func(tool, dir string) string {
		path := filepath.ToSlash(filepath.Clean(dir))
		if path[0] != '/' {
			path = "/" + path // a Windows drive path
		}
		return tool + "(/" + path + "/**)"
	}
	var readRules []string
	for _, dir := range []string{skillRoot, filepath.Join(n.dir, "skills"), filepath.Join(n.dir, "data"), linkedSkill, linkedData} {
		rule := fileRule("Read", dir)
		readRules = append(readRules, rule)
		if !slices.Contains(allowed, rule) {
			t.Errorf("Inbox --allowedTools lacks %q: %q", rule, allowed)
		}
	}
	for _, rule := range allowed {
		if strings.HasPrefix(rule, "Write(") || strings.HasPrefix(rule, "Edit(") {
			t.Errorf("Inbox gained a file-write allow rule: %q", rule)
		}
	}
	ordinary := strings.Split(mcpFlag(t, n.claudeAssistantMCPAndTools("ordinary", cwd, true), "--allowedTools"), ",")
	if slices.Contains(ordinary, "Read") {
		t.Errorf("ordinary conversation gained the Inbox-only bare Read rule: %q", ordinary)
	}
	for _, rule := range readRules {
		if slices.Contains(ordinary, rule) {
			t.Errorf("ordinary conversation gained Inbox-only rule %q", rule)
		}
	}
	denyAt := slices.Index(flags, "--disallowedTools")
	if denyAt < 0 {
		t.Fatalf("Inbox lost its health denies: %q", flags)
	}
	denied := flags[denyAt+1:]
	for _, tool := range []string{"Read", "Edit"} {
		rule := fileRule(tool, filepath.Join(n.dir, "health"))
		if !slices.Contains(denied, rule) {
			t.Errorf("Inbox --disallowedTools lacks %q: %q", rule, denied)
		}
	}
}

// inboxPermissionCLI models the narrow Claude Code permission contracts from the regressions: Plan rejects Persona MCP
// before allowedTools, default runs calls covered by mcp__persona without asking, and built-in tools need their own bare
// allow entry. It records completion and permission requests instead of contacting a hub. The manager test below
// exercises the real read-only tool path.
const inboxPermissionCLI = `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
mode=default
allowed=
denied=
collect=
for argument in "$@"; do
  case "$argument" in
    --allowedTools) collect=allowed; continue ;;
    --disallowedTools) collect=denied; continue ;;
    --*) collect= ;;
  esac
  case "$collect" in
    allowed) allowed=$argument ;;
    denied) denied="${denied}${argument}
" ;;
  esac
done
printf '%s' "$denied" >> "$CLI_FAKE_DIR/denied-tools"
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLI_FAKE_DIR/stdin.jsonl"
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      case "$line" in
        *'"subtype":"set_permission_mode"'*) mode=$(printf '%s' "$line" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p') ;;
      esac
      echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\"}}" ;;
    *'"type":"user"'*)
      case "$line" in
        *secret-read-replay*)
          # Narrow fake: one exact Read(path) rule, supplied by the test, models deny-over-allow. This is not the real
          # Claude permission engine; the static launch tests separately enumerate exact and glob rules.
          case ",$allowed," in
            *",Read,"*)
              printf '%s\n' 'Read(public)' >> "$CLI_FAKE_DIR/completed-tools"
              secret_rule=
              if [ -f "$CLI_FAKE_DIR/secret-read-rule" ]; then IFS= read -r secret_rule < "$CLI_FAKE_DIR/secret-read-rule"; fi
              if [ -n "$secret_rule" ] && printf '%s\n' "$denied" | grep -F -x -q -- "$secret_rule"; then
                printf '%s\n' 'Read(secret)' >> "$CLI_FAKE_DIR/permission-requests"
              else
                printf '%s\n' 'Read(secret)' >> "$CLI_FAKE_DIR/completed-tools"
              fi ;;
            *) printf '%s\n' 'Read(public)' 'Read(secret)' >> "$CLI_FAKE_DIR/permission-requests" ;;
          esac ;;
        *permission-replay*)
          for tool in Read WebFetch Write Edit Bash; do
            case ",$allowed," in
              *",$tool,"*) printf '%s\n' "$tool" >> "$CLI_FAKE_DIR/completed-tools" ;;
              *) printf '%s\n' "$tool" >> "$CLI_FAKE_DIR/permission-requests" ;;
            esac
          done ;;
        *)
          if [ "$mode" = "plan" ]; then
            printf '%s\n' 'Cannot call mcp__persona__lark_messages while in plan mode.' >> "$CLI_FAKE_DIR/permission-requests"
          else
            case ",$allowed," in
              *",mcp__persona,"*)
                printf '%s\n' 'mcp__persona__lark_messages' 'mcp__persona__inbox_suggest' >> "$CLI_FAKE_DIR/completed-tools" ;;
              *) printf '%s\n' 'Persona MCP was not allowed without asking' >> "$CLI_FAKE_DIR/permission-requests" ;;
            esac
          fi ;;
      esac
      echo '{"type":"result","subtype":"success","result":"done"}' ;;
  esac
done
`

// A read-only MCP investigation and inbox_suggest both complete without a permission request. Reverting the Inbox
// clamp to Plan makes the CLI record the observed mode:plan rejection; removing mcp__persona records a prompt instead.
func TestInboxClaudeReadOnlyMCPAndSuggestionSkipPermission(t *testing.T) {
	f := cliTestBash(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "Investigate", nil, proto.ModeBypass, "", modelExecution{Origin: originInbox})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := f.file("permission-requests"); got != "" {
		t.Fatalf("Persona MCP stopped at a permission request: %s", got)
	}
	const completed = "mcp__persona__lark_messages\nmcp__persona__inbox_suggest\n"
	if got := f.file("completed-tools"); got != completed {
		t.Fatalf("completed Persona MCP calls = %q, want %q", got, completed)
	}
	if got := len(f.fh.interactions()); got != 0 {
		t.Fatalf("handling opened %d permission cards, want none", got)
	}
}

// A current manager attests the AI twin's own saved mode separately from the caller-controlled permission_mode.
// The dedicated field is the only one a new node may honor: an older manager's ordinary bypass still gets default.
func TestInboxStartUsesAttestedOwnPermissionMode(t *testing.T) {
	f := cliTestBash(t)
	raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"text": "Investigate", "cwd": t.TempDir(), "launcher": "claude", "origin": originInbox, "origin_known": true,
		"permission_mode": proto.ModeDefault, "inbox_permission_mode": proto.ModeBypass,
	})
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.SessionID == "" {
		t.Fatalf("start result=%s err=%v", raw, err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != proto.ModeBypass || st.Origin != originInbox || !st.OriginKnown {
		t.Fatalf("started mode=%q origin=%q known=%t, want attested Inbox bypass", st.AppMode, st.Origin, st.OriginKnown)
	}
	if argv := f.file("cli-argv"); !strings.Contains(argv, "--permission-mode "+proto.ModeBypass) {
		t.Fatal("attested AI-twin bypass did not reach the real CLI start arguments")
	}

	// Changing the setting does not replace a live process. It takes effect on its next stopped-process resume.
	running := f.proc(started.SessionID)
	if running == nil {
		t.Fatal("the started Inbox process is not available for the live-mode control")
	}
	if _, err := nodeCall(t, f.n, "assistant.send", map[string]any{
		"session_id": started.SessionID, "text": "Keep running", "origin": originInbox, "origin_known": true,
		"inbox_permission_mode": proto.ModeDefault,
	}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if f.proc(started.SessionID) != running {
		t.Fatal("a permission setting change replaced a live Inbox process")
	}
	if mode, _ := f.n.modesOf(started.SessionID); mode != proto.ModeBypass {
		t.Fatalf("live process mode=%q, want its original bypass", mode)
	}
	f.n.stopAssistantProcess(started.SessionID)
	if _, err := f.n.assistantSend(context.Background(), started.SessionID, "", "", "Resume with the new setting", nil, "", "",
		modelExecution{Origin: originInbox, OriginKnown: true, InboxPermissionMode: proto.ModeDefault}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, started.SessionID)
	if st, _ := f.n.state.get(started.SessionID); st.AppMode != proto.ModeDefault {
		t.Fatalf("stopped process resumed in %q, want the newly saved default", st.AppMode)
	}
}

// A live default process must ignore the generic caller mode whether an older manager omits the dedicated field or a
// current manager explicitly keeps default. The generic bypass must neither replace the process nor become its next mode.
func TestInboxLiveProcessIgnoresGenericCallerMode(t *testing.T) {
	for _, tc := range []struct {
		name, own string
	}{
		{name: "older manager omits dedicated mode"},
		{name: "current manager keeps default", own: proto.ModeDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTestBash(t)
			raw, err := nodeCall(t, f.n, "assistant.send", map[string]any{
				"text": "Start default", "cwd": t.TempDir(), "launcher": "claude", "origin": originInbox, "origin_known": true,
				"permission_mode": proto.ModeDefault, "inbox_permission_mode": proto.ModeDefault,
			})
			if err != nil {
				t.Fatal(err)
			}
			var started struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &started); err != nil || started.SessionID == "" {
				t.Fatalf("start result=%s err=%v", raw, err)
			}
			f.idle(t, started.SessionID)
			running := f.proc(started.SessionID)
			if running == nil {
				t.Fatal("default Inbox process is not live")
			}
			params := map[string]any{
				"session_id": started.SessionID, "text": "Caller tries to elevate", "origin": originInbox, "origin_known": true,
				"permission_mode": proto.ModeBypass,
			}
			if tc.own != "" {
				params["inbox_permission_mode"] = tc.own
			}
			if _, err := nodeCall(t, f.n, "assistant.send", params); err != nil {
				t.Fatal(err)
			}
			f.idle(t, started.SessionID)
			if f.proc(started.SessionID) != running {
				t.Fatal("generic caller bypass replaced the live default Inbox process")
			}
			if mode, _ := f.n.modesOf(started.SessionID); mode != proto.ModeDefault {
				t.Fatalf("live Inbox mode=%q after generic caller bypass, want default", mode)
			}
		})
	}
}

// The same launch contract covers built-in tools: the Inbox-only bare Read completes, while network, shell and file
// mutations still reach the permission prompt. The fake CLI records prompts rather than contacting a hub.
func TestInboxClaudeBuiltInReadAndMutationPermissionReplay(t *testing.T) {
	f := cliTestBash(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "permission-replay", nil, proto.ModeBypass, "", modelExecution{Origin: originInbox})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := f.file("completed-tools"); got != "Read\n" {
		t.Fatalf("completed built-in tools = %q, want only Read", got)
	}
	const requests = "WebFetch\nWrite\nEdit\nBash\n"
	if got := f.file("permission-requests"); got != requests {
		t.Fatalf("built-in permission requests = %q, want %q", got, requests)
	}
	if got := len(f.fh.interactions()); got != 0 {
		t.Fatalf("fake CLI contacted the hub with %d permission cards, want none", got)
	}
}

// The fake CLI models the documented deny-over-allow contract for one exact synthetic path: a normal Read completes
// under bare Read, while the same allow does not complete a credential Read covered by --disallowedTools. This checks
// Persona's launch arguments, not the real Claude Code permission engine.
func TestInboxClaudeSecretReadDenyOverridesBareAllow(t *testing.T) {
	f := cliTestBash(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(f.n.dir, "hub.json")
	rule := inboxTestReadRule(secret, false)
	if err := os.WriteFile(filepath.Join(f.dir, "secret-read-rule"), []byte(rule+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "secret-read-replay", nil, proto.ModeBypass, "", modelExecution{Origin: originInbox})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := f.file("completed-tools"); got != "Read(public)\n" {
		t.Fatalf("completed tools = %q, want only an ordinary Read; rule=%q denied=%q argv=%s", got, rule, f.file("denied-tools"), f.file("cli-argv"))
	}
	if got := f.file("permission-requests"); got != "Read(secret)\n" {
		t.Fatalf("permission requests = %q, want the denied credential Read", got)
	}
	if argv := f.file("cli-argv"); !strings.Contains(argv, rule) {
		t.Fatalf("launch argv lacks exact credential deny %q: %s", rule, argv)
	}
}

func inboxResumeTranscript(t *testing.T, n *Node, id string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","permissionMode":"plan","message":{"role":"user","content":"Earlier investigation"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n.setPath(id, path)
}

// A stopped handling chat applies the current AI-twin setting when its next message resumes it. Plan and unknown
// values remain default because Plan rejects the required MCP tools; the ordinary caller mode cannot elevate either.
func TestInboxSessionSendUsesOnlyAttestedOwnPermissionMode(t *testing.T) {
	for _, tc := range []struct {
		name, id, own, caller, want string
	}{
		{name: "own bypass resumes bypass", id: "11111111-1111-4111-8111-111111111121", own: proto.ModeBypass, caller: proto.ModeDefault, want: proto.ModeBypass},
		{name: "plan stays default", id: "11111111-1111-4111-8111-111111111122", own: proto.ModePlan, caller: proto.ModeBypass, want: proto.ModeDefault},
		{name: "unknown stays default", id: "11111111-1111-4111-8111-111111111123", own: "invalid", caller: proto.ModeBypass, want: proto.ModeDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTestBash(t)
			cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			f.n.state.put(tc.id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: proto.ModeDefault})
			inboxResumeTranscript(t, f.n, tc.id)
			if _, err := nodeCall(t, f.n, "session.send", map[string]any{
				"session_id": tc.id, "text": "Continue", "origin": originInbox, "origin_known": true,
				"permission_mode": tc.caller, "inbox_permission_mode": tc.own,
			}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, tc.id)
			st, _ := f.n.state.get(tc.id)
			argv := f.file("cli-argv")
			if st.AppMode != tc.want {
				t.Fatalf("resumed mode=%q, want %q; argv=%s", st.AppMode, tc.want, argv)
			}
			if hasBypass := strings.Contains(argv, "--permission-mode "+proto.ModeBypass); hasBypass != (tc.want == proto.ModeBypass) {
				t.Fatalf("resumed argv bypass=%t, want %t; argv=%s", hasBypass, tc.want == proto.ModeBypass, argv)
			}
		})
	}
}

// session.send is the continuation RPC used by Inbox deliverNotes. Its attested origin must reach assistantSend even
// when the reloaded session record and transcript still say Plan; the restricted tools apply to the resumed process too.
func TestInboxSessionSendRestoresDefaultAndRestrictedTools(t *testing.T) {
	f := cliTest(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-1111-4111-8111-111111111111"
	cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "plan"})
	inboxResumeTranscript(t, f.n, id)
	if _, err := nodeCall(t, f.n, "session.send", map[string]any{"session_id": id, "text": "Continue", "origin": originInbox, "origin_known": true}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := f.file("permission-requests"); got != "" {
		t.Fatalf("session.send resumed a permission stall: %s", got)
	}
	const completed = "mcp__persona__lark_messages\nmcp__persona__inbox_suggest\n"
	if got := f.file("completed-tools"); got != completed {
		t.Fatalf("completed Persona MCP calls = %q, want %q", got, completed)
	}
	argv := f.file("cli-argv")
	if !strings.Contains(argv, "--resume") || !strings.Contains(argv, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(argv, "SendMessage") {
		t.Fatalf("resumed Inbox argv lacks its origin-specific tool policy: %s", argv)
	}
	if in := f.file("stdin.jsonl"); !strings.Contains(in, `"subtype":"set_permission_mode"`) || !strings.Contains(in, `"mode":"default"`) {
		t.Fatalf("session.send did not replace the old Plan mode with default: %s", in)
	}
}

// If a manager cannot make a fresh decision (for example, its origin store is unavailable), a persisted Inbox decision
// remains authoritative. Missing wire fields must not downgrade it to ordinary tools.
func TestSessionSendWithoutNewAttestationRetainsPersistedInbox(t *testing.T) {
	f := cliTest(t)
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-1111-4111-8111-111111111116"
	cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "plan", Origin: originInbox, OriginKnown: true})
	inboxResumeTranscript(t, f.n, id)
	if _, err := nodeCallWithoutAttestation(t, f.n, "session.send", map[string]any{"session_id": id, "text": "Continue"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	st, _ := f.n.state.get(id)
	argv := f.file("cli-argv")
	if st.Origin != originInbox || !st.OriginKnown || !strings.Contains(argv, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(argv, "SendMessage") {
		t.Fatalf("missing fresh attestation downgraded persisted Inbox: state=%+v argv=%s", st, argv)
	}
	if got := f.file("permission-requests"); got != "" {
		t.Fatalf("persisted Inbox resumed a permission stall: %s", got)
	}
}

func TestUnattestedOriginCannotOverridePersistedInbox(t *testing.T) {
	f := cliTest(t)
	const id = "11111111-1111-4111-8111-111111111118"
	cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default", Origin: originInbox, OriginKnown: true})
	inboxResumeTranscript(t, f.n, id)
	// An older manager can supply a non-empty origin string, but without origin_known it is not a new decision and
	// cannot replace the node's persisted Inbox policy with ordinary tools.
	if _, err := nodeCallWithoutAttestation(t, f.n, "session.send", map[string]any{"session_id": id, "text": "Continue", "origin": "user"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	st, _ := f.n.state.get(id)
	argv := f.file("cli-argv")
	if st.Origin != originInbox || !st.OriginKnown || !strings.Contains(argv, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(argv, "SendMessage") {
		t.Fatalf("unattested caller origin replaced persisted Inbox: state=%+v argv=%s", st, argv)
	}
}

// Node-owned recovery runs before any manager continuation can re-attest the conversation. Load the hub's persisted
// NodeSession representation through the real state.load/fetchState path, then recover its lost background task. Both
// a new record with origin and a pre-upgrade record without it must recover safely; assistant.model re-attests the latter.
func TestInboxNodeRecoveryKeepsPersistedOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, persistedOrigin string
		originKnown           bool
		model                 map[string]any
	}{
		{name: "origin persisted", persistedOrigin: originInbox, originKnown: true},
		{name: "legacy record re-attested by assistant model", model: map[string]any{"origin": originInbox, "origin_known": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
				t.Fatal(err)
			}
			const id = "11111111-1111-4111-8111-111111111112"
			cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			inboxResumeTranscript(t, f.n, id)
			f.fh.mu.Lock()
			f.fh.state = map[string]db.NodeSession{id: {
				Launcher: "claude", Cwd: cwd, Origin: tc.persistedOrigin, OriginKnown: tc.originKnown,
				AppMode: "plan", Tasks: []string{"watch a background task"},
			}}
			f.fh.model = tc.model
			f.fh.mu.Unlock()
			if !f.n.fetchState(context.Background()) {
				t.Fatal("state.load failed")
			}
			f.idle(t, id)
			const completed = "mcp__persona__lark_messages\nmcp__persona__inbox_suggest\n"
			cliWait(t, "recovered read-only investigation and suggestion", func() bool { return f.file("completed-tools") == completed })
			if got := f.file("permission-requests"); got != "" {
				t.Fatalf("node-owned recovery resumed a permission stall: %s", got)
			}
			argv := f.file("cli-argv")
			if !strings.Contains(argv, "--resume") || !strings.Contains(argv, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(argv, "SendMessage") {
				t.Fatalf("node-owned recovery lost the Inbox tool policy: %s", argv)
			}
			if in := f.file("stdin.jsonl"); !strings.Contains(in, `"mode":"default"`) {
				t.Fatalf("node-owned recovery did not force default: %s", in)
			}
			// The CLI can publish its result before the recovery caller clears the delivered lost-task record.
			cliWait(t, "cleared lost tasks and retained Inbox origin", func() bool {
				st, ok := f.n.state.get(id)
				return ok && len(st.Tasks) == 0 && st.Origin == originInbox && st.OriginKnown
			})
			if st, _ := f.n.state.get(id); len(st.Tasks) != 0 || st.Origin != originInbox || !st.OriginKnown {
				t.Fatalf("recovered session state=%+v, want cleared tasks and retained origin decision", st)
			}
		})
	}
}

// Neither an unavailable nor a successful empty assistant.model response is an ordinary-origin attestation. An
// originless pre-upgrade record keeps its tasks for retry without starting a CLI; a persisted ordinary decision retains
// the older-hub fallback, and a new manager can explicitly attest a legacy record ordinary.
func TestRecoveryWithoutModelAnswerRequiresKnownOrigin(t *testing.T) {
	for _, tc := range []struct {
		name          string
		originKnown   bool
		model         map[string]any
		wantRecovered bool
	}{
		{name: "legacy origin unknown stays pending"},
		{name: "empty model answer stays pending", model: map[string]any{}},
		{name: "known ordinary retains fallback", originKnown: true, wantRecovered: true},
		{name: "legacy ordinary explicitly attested", model: map[string]any{"origin_known": true}, wantRecovered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			const id = "11111111-1111-4111-8111-111111111113"
			cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			inboxResumeTranscript(t, f.n, id)
			f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default", OriginKnown: tc.originKnown,
				Tasks: []string{"watch a background task"}})
			f.fh.mu.Lock()
			f.fh.model = tc.model
			f.fh.mu.Unlock()
			f.n.recoverAssistantTasks(f.n.recoverAssistantSend)
			st, _ := f.n.state.get(id)
			if tc.wantRecovered {
				f.idle(t, id)
				if len(st.Tasks) != 0 || st.Origin != "" || !st.OriginKnown {
					t.Fatalf("known ordinary recovery state=%+v, want completed with its origin decision", st)
				}
				if argv := f.file("cli-argv"); !strings.Contains(argv, "--tools "+assistantBuiltinTools) || !strings.Contains(argv, "SendMessage") {
					t.Fatalf("known ordinary fallback lost its existing tools: %s", argv)
				}
			} else {
				if len(st.Tasks) != 1 || st.OriginKnown || f.proc(id) != nil || f.file("cli-argv") != "" {
					t.Fatalf("unknown legacy recovery started a process or cleared tasks: state=%+v argv=%q", st, f.file("cli-argv"))
				}
			}
		})
	}
}

// Recovery without a fresh manager answer retains both the persisted origin decision and its working directory. The
// send's empty cwd means "use the stored directory", not "clear it before the storedCwd fallback runs".
func TestRecoveryWithoutModelAnswerPreservesStoredCwd(t *testing.T) {
	for _, origin := range []string{"user", originInbox} {
		t.Run(origin, func(t *testing.T) {
			f := cliTest(t)
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\" >> \"$CLI_FAKE_DIR/cli-cwd\"\n"+fakeCLI), 0o700); err != nil {
				t.Fatal(err)
			}
			const id = "11111111-1111-4111-8111-11111111111a"
			// The fake CLI writes the shell's $PWD, the physical path: macOS's temporary directory is behind a link.
			cwd := filepath.Join(physicalTempDir(t), ".persona", "assistant")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default", Origin: origin, OriginKnown: true,
				Tasks: []string{"watch a background task"}})
			inboxResumeTranscript(t, f.n, id)
			f.n.recoverAssistantTasks(f.n.recoverAssistantSend)
			f.idle(t, id)
			st, _ := f.n.state.get(id)
			p := f.proc(id)
			if p == nil {
				t.Fatal("known-origin recovery did not start a process")
			}
			p.mu.Lock()
			processCwd := p.cwd
			p.mu.Unlock()
			if st.Cwd != cwd || processCwd != cwd || strings.TrimSpace(f.file("cli-cwd")) != cwd {
				t.Fatalf("recovery lost stored cwd %q: state=%q process=%q actual=%q", cwd, st.Cwd, processCwd, strings.TrimSpace(f.file("cli-cwd")))
			}
		})
	}
}

// A legacy manager cannot attest an ordinary empty origin. Both continuation RPCs fail closed on a pre-upgrade record,
// even if it supplies an origin string without origin_known; the new manager's explicit ordinary decision resumes it.
func TestLegacyManagerSendRequiresExplicitOriginDecision(t *testing.T) {
	for _, method := range []string{"assistant.send", "session.send"} {
		for _, tc := range []struct {
			name      string
			params    map[string]any
			wantStart bool
		}{
			{name: "older manager"},
			{name: "origin without explicit decision", params: map[string]any{"origin": originInbox}},
			{name: "explicit ordinary decision", params: map[string]any{"origin_known": true}, wantStart: true},
		} {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				f := cliTest(t)
				const id = "11111111-1111-4111-8111-111111111114"
				cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
				if err := os.MkdirAll(cwd, 0o700); err != nil {
					t.Fatal(err)
				}
				f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default"})
				inboxResumeTranscript(t, f.n, id)
				params := map[string]any{"session_id": id, "text": "Continue"}
				for k, v := range tc.params {
					params[k] = v
				}
				_, err := nodeCallWithoutAttestation(t, f.n, method, params)
				if !tc.wantStart {
					if !errors.Is(err, errAssistantOriginUnknown) {
						t.Fatalf("legacy send error=%v, want errAssistantOriginUnknown", err)
					}
					st, _ := f.n.state.get(id)
					if st.OriginKnown || f.proc(id) != nil || f.file("cli-argv") != "" {
						t.Fatalf("unattested manager send started ordinary tools: state=%+v argv=%q", st, f.file("cli-argv"))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				f.idle(t, id)
				st, _ := f.n.state.get(id)
				if !st.OriginKnown || st.Origin != "" {
					t.Fatalf("explicit ordinary decision was not persisted: %+v", st)
				}
				if argv := f.file("cli-argv"); !strings.Contains(argv, "--tools "+assistantBuiltinTools) || !strings.Contains(argv, "SendMessage") {
					t.Fatalf("explicit ordinary continuation lost its existing tools: %s", argv)
				}
			})
		}
	}
}

// Cold compaction directly spawns a stopped process. It must consume the same explicit origin decision as recovery and
// record it before tool selection, rather than starting an originless legacy Inbox transcript with ordinary built-ins.
func TestColdCompactionRequiresAndCarriesExplicitOriginDecision(t *testing.T) {
	for _, tc := range []struct {
		name        string
		originKnown bool
		model       map[string]any
		wantStart   bool
		wantOrigin  string
	}{
		{name: "unavailable model answer stays stopped"},
		{name: "empty model answer stays stopped", model: map[string]any{}},
		{name: "known ordinary retains fallback", originKnown: true, wantStart: true},
		{name: "legacy Inbox re-attested", model: map[string]any{"origin": originInbox, "origin_known": true}, wantStart: true, wantOrigin: originInbox},
		{name: "legacy ordinary re-attested", model: map[string]any{"origin_known": true}, wantStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			const id = "11111111-1111-4111-8111-111111111115"
			cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default", OriginKnown: tc.originKnown})
			inboxResumeTranscript(t, f.n, id)
			f.fh.mu.Lock()
			f.fh.model = tc.model
			f.fh.mu.Unlock()
			err := f.n.assistantCompact(context.Background(), id, false)
			st, _ := f.n.state.get(id)
			if !tc.wantStart {
				if !errors.Is(err, errAssistantOriginUnknown) || f.proc(id) != nil || f.file("cli-argv") != "" || st.OriginKnown || st.Compaction != nil {
					t.Fatalf("unattested cold compaction started: err=%v state=%+v argv=%q", err, st, f.file("cli-argv"))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			cliWait(t, "cold compaction process arguments", func() bool { return strings.Contains(f.file("cli-argv"), "--resume") })
			argv := f.file("cli-argv")
			if tc.wantOrigin == originInbox {
				if !strings.Contains(argv, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(argv, "SendMessage") {
					t.Fatalf("cold-compacted Inbox process lost restricted tools: %s", argv)
				}
			} else if !strings.Contains(argv, "--tools "+assistantBuiltinTools) || !strings.Contains(argv, "SendMessage") {
				t.Fatalf("cold-compacted ordinary process lost its existing tools: %s", argv)
			}
			if !st.OriginKnown || st.Origin != tc.wantOrigin {
				t.Fatalf("cold compaction persisted origin=%q known=%v, want %q/true", st.Origin, st.OriginKnown, tc.wantOrigin)
			}
		})
	}
}

// Model-error recovery and impossible-usage resend can restart a stopped old process without a new manager RPC.
// Their production send helpers must apply the same explicit-origin gate rather than reviving ordinary built-ins.
func TestAutonomousRetriesRequireKnownOrigin(t *testing.T) {
	for _, name := range []string{"model recovery", "usage resend"} {
		t.Run(name, func(t *testing.T) {
			f := cliTest(t)
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+inboxPermissionCLI), 0o700); err != nil {
				t.Fatal(err)
			}
			const id = "11111111-1111-4111-8111-111111111117"
			cwd := filepath.Join(t.TempDir(), ".persona", "assistant")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default"})
			inboxResumeTranscript(t, f.n, id)
			var err error
			if name == "model recovery" {
				err = sendAssistantRecovery(context.Background(), f.n, &assistantProc{id: id}, "claude", cwd, modelExecution{})
			} else {
				err = sendAssistantUsageRetry(context.Background(), f.n, id, cwd, "claude", "retry", modelExecution{})
			}
			if !errors.Is(err, errAssistantOriginUnknown) || f.proc(id) != nil || f.file("cli-argv") != "" {
				t.Fatalf("unattested %s restarted ordinary tools: err=%v argv=%q", name, err, f.file("cli-argv"))
			}
		})
	}
}

func TestOriginReattestationRestartsClaudeToolPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, initialOrigin, targetOrigin, wantTools string
	}{
		{name: "ordinary to Inbox", targetOrigin: originInbox, wantTools: wantInboxClaudeBuiltins},
		{name: "Inbox to ordinary", initialOrigin: originInbox, wantTools: assistantBuiltinTools},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			cwd := t.TempDir()
			id, err := f.n.assistantSend(context.Background(), "", cwd, "claude", "Investigate", nil, "default", "", modelExecution{Origin: tc.initialOrigin})
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			first := f.proc(id)
			inboxResumeTranscript(t, f.n, id)
			if _, err := f.n.assistantSend(context.Background(), id, cwd, "claude", "Continue", nil, "default", "", modelExecution{Origin: tc.targetOrigin, OriginKnown: true, OriginDecisionRequired: true}); err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			second := f.proc(id)
			if second == nil || second == first {
				t.Fatal("origin re-attestation retained the process with the previous built-in tools")
			}
			lines := strings.Split(strings.TrimSpace(f.file("cli-argv")), "\n")
			argv := lines[len(lines)-1]
			if !strings.Contains(argv, "--tools "+tc.wantTools) {
				t.Fatalf("re-attested process argv has the wrong tool policy: %s", argv)
			}
			if tc.targetOrigin == originInbox && strings.Contains(argv, "SendMessage") {
				t.Fatalf("ordinary process retained SendMessage after Inbox re-attestation: %s", argv)
			}
			if tc.targetOrigin == "" && !strings.Contains(argv, "SendMessage") {
				t.Fatalf("ordinary re-attestation did not restore SendMessage: %s", argv)
			}
			st, _ := f.n.state.get(id)
			if st.Origin != tc.targetOrigin || !st.OriginKnown {
				t.Fatalf("re-attested state origin=%q known=%v, want %q/true", st.Origin, st.OriginKnown, tc.targetOrigin)
			}
		})
	}
}

// A fallback hold changes only the model choice. It must not restore the fallback's old origin and thereby deliver an
// Inbox message to a process that still has the ordinary built-in SendMessage tool.
func TestInboxReattestationOverridesActiveFallbackHold(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hold, release func(*assistantProc)
		wantBusy      bool
	}{
		{name: "idle", hold: func(*assistantProc) {}, release: func(*assistantProc) {}},
		{name: "compacting", hold: func(p *assistantProc) { p.compacting = true }, release: func(p *assistantProc) { p.compacting = false }, wantBusy: true},
		{name: "queued input", hold: func(p *assistantProc) {
			p.written = []assistantInput{{queued: true, at: time.Now()}}
		}, release: func(p *assistantProc) { p.written = nil }, wantBusy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			cwd := t.TempDir()
			from := modelExecution{Model: "primary-model", Fingerprint: "fp", Origin: "user", OriginKnown: true}
			fallback := modelExecution{Model: "fallback-model", Fingerprint: "fp", Origin: "user", OriginKnown: true}
			id, err := f.n.assistantSend(context.Background(), "", cwd, "claude", "Investigate", nil, "default", "", fallback)
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			first := f.proc(id)
			f.n.mu.Lock()
			f.n.holdFallback(id, "claude", from, fallback)
			f.n.mu.Unlock()
			first.mu.Lock()
			tc.hold(first)
			first.mu.Unlock()
			defer func() {
				first.mu.Lock()
				tc.release(first)
				first.mu.Unlock()
			}()
			argvBefore, stdinBefore := f.file("cli-argv"), f.file("stdin.jsonl")
			asked := from
			asked.Origin, asked.OriginKnown, asked.OriginDecisionRequired = originInbox, true, true
			_, err = f.n.assistantSend(context.Background(), id, cwd, "claude", "Continue as Inbox", nil, "default", "", asked)
			if tc.wantBusy {
				if !errors.Is(err, errAssistantOriginBusy) {
					t.Fatalf("fallback-held working process error=%v, want errAssistantOriginBusy", err)
				}
				first.mu.Lock()
				stillWorking := first.workingLocked()
				first.mu.Unlock()
				if !stillWorking || f.proc(id) != first || f.file("cli-argv") != argvBefore || f.file("stdin.jsonl") != stdinBefore {
					t.Fatal("fallback hold let an Inbox message reach or replace the ordinary working process")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			second := f.proc(id)
			lines := strings.Split(strings.TrimSpace(f.file("cli-argv")), "\n")
			latest := lines[len(lines)-1]
			if second == nil || second == first {
				t.Fatalf("fallback hold retained the ordinary process for Inbox input; latest argv=%s", latest)
			}
			if !strings.Contains(latest, "--tools "+wantInboxClaudeBuiltins) || strings.Contains(latest, "SendMessage") {
				t.Fatalf("fallback-held Inbox process has the wrong built-in tools: %s", latest)
			}
			st, _ := f.n.state.get(id)
			if st.Origin != originInbox || !st.OriginKnown {
				t.Fatalf("fallback-held Inbox state origin=%q known=%v", st.Origin, st.OriginKnown)
			}
		})
	}
}

func TestOrdinaryOriginAliasesKeepClaudeProcess(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%v", busy), func(t *testing.T) {
			f := cliTest(t)
			cwd := t.TempDir()
			id, err := f.n.assistantSend(context.Background(), "", cwd, "claude", "Investigate", nil, "default", "",
				modelExecution{Origin: "user", OriginKnown: true})
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			first := f.proc(id)
			argvBefore := f.file("cli-argv")
			first.mu.Lock()
			first.busy = busy
			first.mu.Unlock()
			_, err = f.n.assistantSend(context.Background(), id, cwd, "claude", "Continue", nil, "default", "",
				modelExecution{OriginKnown: true, OriginDecisionRequired: true})
			first.mu.Lock()
			first.busy = false
			first.mu.Unlock()
			if err != nil {
				t.Fatalf("ordinary origin alias changed the tool-policy boundary: %v", err)
			}
			f.idle(t, id)
			if f.proc(id) != first || f.file("cli-argv") != argvBefore {
				t.Fatal("ordinary user to empty origin restarted an identical tool policy")
			}
			st, _ := f.n.state.get(id)
			if st.Origin != "" || !st.OriginKnown {
				t.Fatalf("ordinary re-attestation was not persisted: %+v", st)
			}
		})
	}
}

func TestInboxReattestationRefusesBusyOrdinaryProcess(t *testing.T) {
	for _, tc := range []struct {
		name          string
		hold, release func(*assistantProc)
	}{
		{name: "turn", hold: func(p *assistantProc) { p.busy = true }, release: func(p *assistantProc) { p.busy = false }},
		{name: "compaction", hold: func(p *assistantProc) { p.compacting = true }, release: func(p *assistantProc) { p.compacting = false }},
		{name: "queued input", hold: func(p *assistantProc) {
			p.written = []assistantInput{{queued: true, at: time.Now()}}
		}, release: func(p *assistantProc) { p.written = nil }},
		{name: "accepted unacknowledged input", hold: func(p *assistantProc) {
			p.lifecycle = true
			p.written = []assistantInput{{at: time.Now()}}
		}, release: func(p *assistantProc) {
			p.lifecycle = false
			p.written = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := cliTest(t)
			cwd := t.TempDir()
			id, err := f.n.assistantSend(context.Background(), "", cwd, "claude", "Investigate", nil, "default", "", modelExecution{})
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			p := f.proc(id)
			p.mu.Lock()
			tc.hold(p)
			p.mu.Unlock()
			defer func() {
				p.mu.Lock()
				tc.release(p)
				p.mu.Unlock()
			}()
			argvBefore, stdinBefore := f.file("cli-argv"), f.file("stdin.jsonl")
			_, err = f.n.assistantSend(context.Background(), id, cwd, "claude", "Continue as Inbox", nil, "default", "", modelExecution{Origin: originInbox, OriginKnown: true, OriginDecisionRequired: true})
			if !errors.Is(err, errAssistantOriginBusy) {
				t.Fatalf("working origin re-attestation error=%v, want errAssistantOriginBusy", err)
			}
			p.mu.Lock()
			stillWorking := p.workingLocked()
			p.mu.Unlock()
			if !stillWorking {
				t.Fatal("origin re-attestation discarded the process's held work")
			}
			if f.proc(id) != p || f.file("cli-argv") != argvBefore || f.file("stdin.jsonl") != stdinBefore {
				t.Fatal("Inbox message reached or replaced the working process that still had ordinary built-in tools")
			}
		})
	}
}

func TestExplicitOrdinaryDecisionClearsPersistedInboxBeforeSpawn(t *testing.T) {
	f := cliTest(t)
	const id = "11111111-1111-4111-8111-111111111119"
	cwd := t.TempDir()
	f.n.state.put(id, sessionState{Launcher: "claude", Cwd: cwd, AppMode: "default", Origin: originInbox, OriginKnown: true})
	inboxResumeTranscript(t, f.n, id)
	if _, err := f.n.assistantSend(context.Background(), id, cwd, "claude", "Continue", nil, "default", "", modelExecution{OriginKnown: true, OriginDecisionRequired: true}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	st, _ := f.n.state.get(id)
	argv := f.file("cli-argv")
	if st.Origin != "" || !st.OriginKnown || !strings.Contains(argv, "--tools "+assistantBuiltinTools) || !strings.Contains(argv, "SendMessage") {
		t.Fatalf("stale Inbox state restricted explicitly ordinary spawn: state=%+v argv=%s", st, argv)
	}
}

// A warm spare was started with ordinary built-ins. Inbox must cold-start with its restricted list rather than inherit
// SendMessage from that process; an ordinary conversation still adopts the same spare.
func TestInboxDoesNotAdoptOrdinaryToolSpare(t *testing.T) {
	n := assistantNode(t)
	p := &assistantProc{id: "spare", agent: "claude", launcher: "claude", cwd: n.home, born: time.Now(),
		fingerprint: n.executionFingerprint("claude", modelExecution{})}
	n.spare = p
	if got := n.adoptSpareFor(n.home, "claude", "default", modelExecution{Origin: originInbox}); got != nil {
		t.Fatal("Inbox adopted a process started with ordinary built-in tools")
	}
	if n.spare != p {
		t.Fatal("the refused spare was consumed")
	}
	if got := n.adoptSpareFor(n.home, "claude", "default", modelExecution{}); got != p {
		t.Fatal("ordinary conversation did not retain warm-spare adoption")
	}
	// Isolate the explicit guard from the origin-bearing execution fingerprint: even matching metadata must not let an
	// Inbox conversation adopt a spare whose actual ordinary process already exposed SendMessage.
	matching := &assistantProc{id: "matching-spare", agent: "claude", launcher: "claude", cwd: n.home, born: time.Now(),
		fingerprint: n.executionFingerprint("claude", modelExecution{Origin: originInbox})}
	n.spare = matching
	if got := n.adoptSpareFor(n.home, "claude", "default", modelExecution{Origin: originInbox}); got != nil {
		t.Fatal("matching fingerprint bypassed the explicit ordinary-tool spare guard")
	}
	if n.spare != matching {
		t.Fatal("the guarded matching spare was consumed")
	}
	ordinaryAlias := &assistantProc{id: "ordinary-alias-spare", agent: "claude", launcher: "claude", cwd: n.home, born: time.Now(),
		fingerprint: n.executionFingerprint("claude", modelExecution{})}
	n.spare = ordinaryAlias
	if got := n.adoptSpareFor(n.home, "claude", "default", modelExecution{Origin: "user"}); got != ordinaryAlias {
		t.Fatal("an ordinary origin alias rejected a spare with the same built-in tool policy")
	}
}

func TestOrdinaryClaudeKeepsBuiltInSendMessage(t *testing.T) {
	f := cliTest(t)
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "Investigate", nil, "default", "", modelExecution{})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if argv := f.file("cli-argv"); !strings.Contains(argv, "--tools "+assistantBuiltinTools) || !strings.Contains(argv, "SendMessage") {
		t.Fatalf("ordinary conversation lost its existing built-in tools: %s", argv)
	}
}

// Inbox handling never inherits the ordinary bypass-safe bot send/reply exemption. Reads remain available, and the
// same bot sends and replies still work for an ordinary bypass conversation.
func TestInboxLarkOutboundDeniedOrdinaryBypassPreserved(t *testing.T) {
	for _, tc := range []struct {
		name, origin, command, want string
	}{
		{"Inbox bot send", originInbox, "lark-cli im +messages-send --as bot --text hello", "deny"},
		{"Inbox bot reply", originInbox, "lark-cli im +messages-reply --as bot --text hello", "deny"},
		{"Inbox raw API send", originInbox, "lark-cli api POST /open-apis/im/v1/messages --as bot --data '{}'", "deny"},
		{"Inbox ordinary write", originInbox, "lark-cli docs +create --title example", "deny"},
		{"Inbox unknown write", originInbox, "lark-cli custom $action", "deny"},
		{"Inbox read", originInbox, "lark-cli docs +fetch --doc-id example", "allow"},
		{"Inbox ol write", originInbox, "ol POST /messages/1/recall", "deny"},
		{"Inbox ol read", originInbox, "ol GET /messages/1", "allow"},
		{"Inbox special Agent API write", originInbox, "ol GET /links/open", "deny"},
		{"ordinary bot send", "", "lark-cli im +messages-send --as bot --text hello", "allow"},
		{"ordinary bot reply", "", "lark-cli im +messages-reply --as bot --text hello", "allow"},
		{"ordinary read", "", "lark-cli docs +fetch --doc-id example", "allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			needsLarkGateForCommand(t, tc.command)
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			const sid = "11111111-1111-4111-8111-111111111111"
			cfg := olCfg()
			cfg.Gates[0].Command = "ol"
			if tc.name == "Inbox special Agent API write" {
				cfg.Gates[0] = proxiedGate("http://example.invalid")
				cfg.Gates[0].Command = "ol"
				cfg.Gates[0].PathPrefix = "/agent/v1"
			}
			cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
			n.larkRisks = map[string]string{"\x00docs +fetch": "read", "\x00docs +create": "write"}
			n.mu.Lock()
			n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
			n.procs[sid] = &assistantProc{id: sid, agent: "claude", origin: tc.origin, exited: make(chan struct{})}
			n.mu.Unlock()
			n.persistModes(sid, proto.ModeBypass, "")
			decision, reason, err := n.larkCheck(context.Background(), sid, t.TempDir(), tc.command,
				proto.Interaction{SessionID: sid, Cwd: t.TempDir(), ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil || decision != tc.want {
				t.Fatalf("decision=%q reason=%q err=%v, want %q", decision, reason, err, tc.want)
			}
			if tc.origin == originInbox && tc.want == "deny" && !strings.Contains(reason, "inbox_suggest") {
				t.Fatalf("Inbox refusal is not actionable: %q", reason)
			}
			if len(fh.interactions()) != 0 {
				t.Fatalf("policy test unexpectedly opened a confirmation card: %v", fh.interactions())
			}
		})
	}
}
