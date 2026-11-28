package node

import (
	"cmp"
	"context"
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// The assistant's skills call HTTP APIs as the user through commands (`<command> METHOD PATH [BODY]`, the
// command gates the hub hands over: plugins' assistant.gate, docs/extensions/extensions.md "Assistant") and through lark-cli. Every Bash
// call of the assistant passes a PreToolUse hook that reads the command (docs/assistant/assistant.md):
//
//   - only gated calls (and a few output filters): each call goes by its gate's rules (the first match: allow, confirm
//     on the phone first with method, path and body on the card, or deny; none matching is confirmed); lark-cli's
//     by its own risk level (larkcligate.go). Assistant bypass skips cards only for classified reads, ordinary
//     writes, and calls allowed by configured or per-chat rules; unconfigured Lark sends as the user, deletion and
//     calls it cannot classify still ask. Non-Lark commands such as
//     rm, git and tee remain outside this gate. Local redirect targets do not change a call's classification.
//     Opaque execution requires confirmation when the
//     command text names the CLI (including paths or interpreter code), or an expansion supplies
//     a command name. A script file with no CLI name in the command text is outside this gate:
//     its contents may still invoke the CLI. This residual risk is accepted; we do not read scripts.
//     Explicit deny and space rules take priority.
//   - anything naming a gate's secret, a process's environ, curl / wget at a gate's API, or curl flags that would
//     print or redirect the credentials: denied.
//   - anything else: no decision; the usual permission flow applies, including its bypass mode.

// larkCall is one gated command's or lark-cli's invocation read off a Bash command.
type larkCall struct {
	Gate    int    // the command gate (index in the config's Gates); -1 for lark-cli
	Method  string // HTTP method; "CLI" for a lark-cli command (larkcligate.go)
	Path    string // without /agent/v1 and the query string; a lark-cli command's words joined by "/"
	Query   string
	Body    string // JSON text, a file's content (@file, cut), or "upload file X" (in Chinese)
	Account string // Agent API's actual routed account; "1" is normalized to the primary "".

	CLI  bool     // lark-cli
	Risk string   // lark-cli's own risk level for a "CLI" call: read | write | high-risk-write ("" unknown, larkCLIMissing)
	Args []string // lark-cli's arguments, for the phone card

	fileBody     bool // an @file body input whose content can change between check time and send time (#8413)
	relFile      bool // a relative @file body or query input, read from the conversation's directory (classifyLark, #8404)
	queryUnknown bool // execution-time stdin, expansion, or -G data cannot be bound to a query-confirmed grant
	dataInQuery  bool // -G moved the parsed data argument to the query, leaving the actual request body empty
}

// larkVerdict: what the gate makes of a whole Bash command.
type larkVerdict struct {
	calls    []larkCall
	deny     string // why the command is refused
	other    bool   // something besides gated calls and filters: the usual permission flow decides
	unsure   bool   // a command-word substitution prevents bypass and requires whole-command confirmation
	unparsed bool   // the command could not be split into its parts at all
	unknown  bool   // a CLI call or indirect execution could not be classified
	dynamic  bool   // an expansion supplies an executable name
	cliWrite bool   // a lark-cli call that is not a known read, even in a command the gate cannot follow
	unread   []int  // gates (indexes in the config's Gates) called in a way the gate could not read
}

// larkGate decides an assistant Bash call (see the top of the file); nil leaves it to the usual permission flow.
func (n *Node) larkGate(ctx context.Context, in hookInput, ix proto.Interaction) (any, error) {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.ToolInput, &a) != nil || a.Command == "" {
		return nil, nil
	}
	decision, reason, err := n.larkCheck(ctx, in.SessionID, in.Cwd, a.Command, ix)
	if err != nil || decision == "" {
		return nil, err
	}
	return preToolDecision(decision, reason), nil
}

// larkCheckOutcome carries the full decision details from larkCheckOutcome for callers that apply edited text or
// distinguish detached pending waits, explicit user denials with notes, and configured rule denials.
type larkCheckOutcome struct {
	decision   string
	reason     string
	card       proto.LarkLetThrough
	editedText string
	pending    bool
	dismiss    bool
	approvalID string
	userDenied bool
	userNote   string
}

// larkCheck is the gate itself, for any agent: "allow" / "deny" with the reason to give the model, or "" when the
// command is not the gate's to decide (the usual permission flow). Writes are confirmed on the phone here, so a
// "deny" may carry the user's own words. Claude reaches it through the Bash PreToolUse hook (larkGate), Codex through
// the app-server's command approvals (assistant_codex_approve.go).
func (n *Node) larkCheck(ctx context.Context, session, cwd, command string, ix proto.Interaction) (string, string, error) {
	out, err := n.larkCheckOutcome(ctx, session, cwd, command, ix)
	return out.decision, out.reason, err
}

func (n *Node) larkCheckOutcome(ctx context.Context, session, cwd, command string, ix proto.Interaction) (larkCheckOutcome, error) {
	// Cwd on the eventual card may be normalized to the conversation's display
	// directory. Keep the actual execution directory separately for the
	// manager's exact-call continuation fingerprint.
	ix.ExecutionCwd = cwd
	ix.OutwardWrite = false
	cfg := n.assistantLark(ctx)
	if slices.ContainsFunc(cfg.Gates, func(g proto.CommandGate) bool { return g.Auth != nil }) {
		n.gates() // the proxy goes by this Node's config from now on (a resident node's next connection)
	}
	inSpace := n.gates().spaceSession(session) || identity.InSpace(cwd)
	remote := n.isRemoteRuntimeSession(session)
	confirmWrites := inSpace || remote
	mode, _ := n.modesOf(session)
	bypass := !confirmWrites && mode == proto.ModeBypass && n.isAssistantSession(session) && n.guestOf(session) == nil
	// Only bypass reads file-driven read loops and notes through path variables as what they are (#15860): every
	// other mode keeps the usual reading and its cards.
	analysisCommand := normalizeLarkCommandRelaxed(command, cwd, cfg.Gates, nil, bypass)
	v := classifyLarkRelaxed(command, cfg.Gates, cwd, n.larkCLIRisk(cfg), n.larkLoginPATH, bypass)
	origin, launchCwd := n.assistantOriginCwd(session)
	if origin == originInbox && inboxLarkOutbound(cfg, v, analysisCommand) {
		return larkCheckOutcome{decision: "deny", reason: inboxLarkRefusal}, nil
	}
	if v.deny != "" {
		return larkCheckOutcome{decision: "deny", reason: v.deny}, nil
	}
	// classified: every call read off the command and nothing else in it that the gate cannot follow.
	classified := !v.unparsed && !v.unknown && !v.unsure
	for _, c := range v.calls {
		decision := larkDecision(cfg, c)
		if decision == "deny" {
			return larkCheckOutcome{decision: "deny", reason: "这个操作按配置不允许（" + c.Method + " " + c.Path + "）。"}, nil
		}
		if decision == "allow" && c.dataInQuery && c.Gate >= 0 && c.Gate < len(cfg.Gates) && !(c.fileBody && cfg.Gates[c.Gate].Auth == nil) {
			// firstLark matched the data as a request body, but curl -G sends it in the query with an empty body. A
			// body-scoped allow therefore does not authorize the actual call, especially a direct non-Auth command.
			// For Auth-less @file calls, decideLarkFileBody already skips body-scoped allows and only allows
			// body-independent rules with no earlier body restriction.
			if rule, ok := firstLark(cfg.Gates[c.Gate].Rules, c); ok && len(rule.Body) > 0 {
				return larkCheckOutcome{decision: "deny", reason: "-G/--get 会把 data 移到查询并留下空请求体，实际请求不再满足按请求体放行的规则。"}, nil
			}
		}
		// Only a rule-allowed call needs no query-bound grant. A confirmation-required GET needs one too, and a non-Auth
		// script has no proxy backstop. Writes in a space or remote runtime always need confirmation. Refuse every unknown query in those cases
		// rather than confirm different bytes.
		needsConfirmation := decision != "allow" || (confirmWrites && !larkCallRead(cfg.Gates, c))
		if c.queryUnknown && needsConfirmation {
			return larkCheckOutcome{decision: "deny", reason: "查询要在执行时才能确定，无法绑定这次确认；请把查询直接写在路径或 --url-query 参数里。"}, nil
		}
		classified = classified && larkBypassSafe(c)
	}
	safe := len(v.calls) > 0 && classified
	boundary := slices.ContainsFunc(v.calls, func(c larkCall) bool { return !larkBypassSafe(c) })
	// This exception is deliberately default-only. Check it before both the mixed-command and ordinary read paths:
	// plan and bypass retain their normal permission flow even when every filter is already known to classifyLark.
	if origin == originInbox && safe && len(v.calls) == 1 && launchCwd != "" &&
		realPath(filepath.Clean(cwd)) == realPath(filepath.Clean(launchCwd)) &&
		larkDecision(cfg, v.calls[0]) == "allow" && larkReadFormattingPipeline(command, cfg.Gates) {
		if mode == proto.ModeDefault {
			return larkCheckOutcome{decision: "allow", reason: "读操作"}, nil
		}
		return larkCheckOutcome{}, nil
	}
	if v.other {
		// A write through a gate whose credentials are at the proxy goes there only as a call read off the command and
		// let through here: in a command the gate cannot follow it would be refused after the phone said yes.
		for _, c := range v.calls {
			if !c.CLI && c.Gate >= 0 && cfg.Gates[c.Gate].Auth != nil && !larkCallRead(cfg.Gates, c) {
				return larkCheckOutcome{decision: "deny", reason: "Proxy-authenticated writes must be standalone gated calls, outside loops, scripts and substitutions."}, nil
			}
		}
		for _, g := range cfg.Gates {
			if _, write := gateWrite([]proto.CommandGate{g}, analysisCommand); write && g.Auth != nil {
				return larkCheckOutcome{decision: "deny", reason: "写操作要单独一条 `" + g.Command + " METHOD PATH [BODY]` 发：不放进循环、脚本或 $(…) 里，输出可以接 jq / head / grep，" +
					"或者用 -o 写到 /tmp。这样手机上能逐条确认，确认过的才发得出去。"}, nil
			}
		}
		// Classified reads and ordinary writes can be mixed with non-Lark commands in bypass.
		// Unknown CLI-related execution or an expanded command name must ask; ordinary
		// interpreter calls and argument substitutions without a CLI name stay outside this gate.
		// Proxy-authenticated writes above must remain standalone so their one-use grants cannot be lost.
		if bypass && safe {
			return larkCheckOutcome{decision: "allow", reason: "Conversation permission mode allows these classified calls."}, nil
		}
		g, write, knownGateWrite := gateWriteDepth(cfg.Gates, analysisCommand, 0)
		// Text naming lark-cli in a command with no call is not one: classifyLark marks unknown what could run it
		// (larkgate_text.go), so a grep pattern or a note written to a file is no card (#8119).
		if write || boundary || v.cliWrite || v.unknown || larkCLIRe.MatchString(analysisCommand) && !classified {
			ix.Kind = proto.IxPermission
			title := wholeCommandTitle(cfg.Gates, v, analysisCommand, g, write)
			if title != "" {
				what := "写"
				if wholeCommandReads(cfg.Gates, v, write) {
					what = "读"
				}
				ix.ToolName, ix.Summary = title, "以你的身份在"+title+"上"+what+"（命令较复杂，整条显示）\n"+prettyBody(command)
			} else {
				// Nothing in the command names a gate or lark-cli: an expansion names a command (`$A POST /api/…`),
				// which could be anything, the CLI included. Still asked, as the ordinary Bash card it is (#4890).
				ix.ToolName, ix.Summary = cmp.Or(ix.ToolName, "Bash"), prettyBody(command)
			}
			ix.WriteOperation = knownGateWrite || slices.ContainsFunc(v.calls, larkKnownWrite)
			ix.OutwardWrite = wholeCommandOutwardWrite(cfg.Gates, v, write, title)
			return n.confirmLark(ctx, session, ix, nil, confirmWrites, title)
		}
		return larkCheckOutcome{}, nil
	}
	// A conversation in a space or remote runtime has its writes confirmed each time: no rule's allow, no "Always allow for this session" (docs/assistant/health.md "Outbound").
	// Its space is the one its process was started in (assistantLarkEnv notes it, and the gate proxy goes by the same),
	// not the directory the hook says: that is where the model's last `cd` left it. A hook directory in a space
	// (the space's own or one inside it, identity.InSpace) counts too, so a mix-up can only ask more.
	// ask: the calls to confirm on the phone; covered: those "Always allow for this session" covers. Once let through
	// here, the gate proxy lets each go once (gateproxy.go).
	var ask, covered, bypassed []larkCall
	for _, c := range v.calls {
		decision := larkDecision(cfg, c)
		if bypass && decision != "allow" {
			if larkBypassSafe(c) {
				bypassed = append(bypassed, c)
				continue
			}
			decision = "confirm" // unconfigured outbound/deletion still requires confirmation
		}
		if decision == "allow" && confirmWrites && !larkCallRead(cfg.Gates, c) {
			decision = "confirm"
		}
		switch decision {
		case "deny":
			return larkCheckOutcome{decision: "deny", reason: "这个操作按配置不允许（" + c.Method + " " + c.Path + "）。"}, nil
		case "allow":
		default:
			if confirmWrites || n.isAssistantSession(session) || !n.larkAlways(session, larkPattern(c)) {
				ask = append(ask, c)
			} else {
				covered = append(covered, c)
			}
		}
	}
	if len(ask) == 0 {
		n.grantGateCalls(cfg, session, bypassed, proto.LarkLetThrough{Kind: proto.LarkThroughBypass})
		n.grantGateCalls(cfg, session, covered, proto.LarkLetThrough{Kind: proto.LarkThroughAlways}) // "Always allow for this session" covers them
		if v.unsure && !bypass {
			return larkCheckOutcome{}, nil // reads, but $(…) could run anything: the usual flow asks about the whole command
		}
		if len(bypassed) > 0 {
			return larkCheckOutcome{decision: "allow", reason: "Conversation permission mode allows these classified calls."}, nil
		}
		return larkCheckOutcome{decision: "allow", reason: "读操作"}, nil
	}
	title := callTitle(cfg.Gates, ask[0])
	ix.Kind, ix.ToolName, ix.Summary = proto.IxPermission, cmp.Or(title, "Bash"),
		larkSummaryWithContext(cfg.Gates, ask, n.larkCardContexts(ctx, cfg, ask), i18n.From(ctx))
	ix.WriteOperation = slices.ContainsFunc(ask, larkKnownWrite)
	// The hub remembers these routes for assistant conversations and checks them again on every call.
	ix.AlwaysKeys = nil
	for _, c := range ask {
		if k := "lark:" + larkPattern(c); !confirmWrites && !bypass && !slices.Contains(ix.AlwaysKeys, k) {
			ix.AlwaysKeys = append(ix.AlwaysKeys, k)
		}
	}
	remember := ask
	if confirmWrites || bypass {
		remember = nil // hard boundaries are confirmed each time
	}
	ix.OutwardWrite = slices.ContainsFunc(ask, func(c larkCall) bool { return larkCallOutwardWrite(cfg.Gates, c) })
	out, err := n.confirmLark(ctx, session, ix, remember, confirmWrites, ix.ToolName)
	if out.decision == "allow" {
		// When the owner changed editable_text, never mint an unedited grant for the original body: the edit path
		// validates the replacement and mints only the single edited/rewrite grant, leaving zero TOCTOU window.
		if ix.EditableText == "" || out.editedText == "" || out.editedText == ix.EditableText {
			n.grantGateCalls(cfg, session, ask, out.card)
		}
		n.grantGateCalls(cfg, session, bypassed, proto.LarkLetThrough{Kind: proto.LarkThroughBypass})
		n.grantGateCalls(cfg, session, covered, proto.LarkLetThrough{Kind: proto.LarkThroughAlways})
	}
	return out, err
}

// grantGateCalls lets calls larkCheck let through for session go once through the gate proxy: those of gates with
// Auth (lark-cli and the others carry their own credentials). through is how they were let through, which the proxy
// tells the manager once the API has taken one (the sent-as-user ledger).
func (n *Node) grantGateCalls(cfg proto.AssistantLark, session string, calls []larkCall, through proto.LarkLetThrough) {
	for _, c := range calls {
		if c.queryUnknown {
			continue // rule-allowed calls need no grant; larkCheck refused every confirmation-required unknown query
		}
		if !c.CLI && c.Gate >= 0 && c.Gate < len(cfg.Gates) && cfg.Gates[c.Gate].Auth != nil {
			n.gates().grant(cfg.Gates[c.Gate].Command, session, c, through)
		}
	}
}

// larkKnownWrite is display-routing metadata, not the execution gate's
// conservative "not known to be a read" classification. Unknown CLI risk and a
// variable HTTP method still require confirmation, but never opt into the
// write-only owner routes.
func larkKnownWrite(c larkCall) bool {
	if c.CLI && c.Method == "CLI" {
		if c.Risk == "write" || c.Risk == "high-risk-write" {
			return true
		}
		if c.Risk != "" {
			return false
		}
		// Risk discovery can be unavailable. Only an exact registered command
		// path is a fallback; a verb-like suffix on an unknown command is not
		// evidence that the operation is a known write.
		return larkCLIKnownWrites[strings.Trim(c.Path, "/")]
	}
	switch strings.ToUpper(c.Method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	default:
		return false
	}
}

var (
	larkCLIKnownWrites     = map[string]bool{"im/+messages-send": true, "im/+messages-reply": true}
	gateLiteralWriteMethod = lazyre.New(`(?i)(POST|PUT|PATCH|DELETE)\b`)
)

// gateWrite: a gated command (or its script) called with a method that is not a read, anywhere in a command.
func gateWrite(gates []proto.CommandGate, command string) (proto.CommandGate, bool) {
	g, write, _ := gateWriteDepth(gates, command, 0)
	return g, write
}

func gateWriteDepth(gates []proto.CommandGate, command string, depth int) (proto.CommandGate, bool, bool) {
	if depth < 16 {
		if segments, ok := shellcmd.Split(command); ok {
			for _, segment := range segments {
				words := segment.Words
				for len(words) > 0 && isAssignment(words[0]) {
					words = words[1:]
				}
				words, parsed := unwrapCommand(words)
				if !parsed {
					continue
				}
				script := ""
				if i := shellScriptIndex(words); i >= 0 {
					script = words[i]
				}
				if len(words) > 0 && words[0] == "eval" {
					script = strings.Join(words[1:], " ")
				}
				if script != "" {
					if g, write, known := gateWriteDepth(gates, script, depth+1); write {
						return g, true, known
					}
				}
			}
		}
	}
	for _, g := range gates {
		var names []string
		if g.Command != "" {
			names = append(names, regexp.QuoteMeta(g.Command))
		}
		if g.Script != "" {
			names = append(names, `\S*/`+regexp.QuoteMeta(path.Base(g.Script)))
		}
		if len(names) == 0 {
			continue
		}
		matches := gateWriteRe(strings.Join(names, "|")).FindAllString(command, -1)
		if len(matches) == 0 {
			continue
		}
		known := slices.ContainsFunc(matches, gateLiteralWriteMethod.MatchString)
		return g, true, known
	}
	return proto.CommandGate{}, false, false
}

// gateWriteRes caches gateWrite's regexp by its alternation of names: gates come from config, so there are few.
var gateWriteRes sync.Map // string -> *regexp.Regexp

// gateWriteRe matches one of names (an alternation of quoted names) called with a write method.
func gateWriteRe(names string) *regexp.Regexp {
	if re, ok := gateWriteRes.Load(names); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile("(?i)(^|[\\s;&|(`])(" + names + ")\\s+((POST|PUT|PATCH|DELETE)\\b|\"?\\$)")
	gateWriteRes.Store(names, re)
	return re
}

// callTitle is what the phone card names as the place a call acts on.
func callTitle(gates []proto.CommandGate, c larkCall) string {
	if c.Gate >= 0 && c.Gate < len(gates) && gates[c.Gate].Title != "" {
		return gates[c.Gate].Title
	}
	if r := larkGateRules(); r != nil {
		return r.DefaultTitle()
	}
	return ""
}

// wholeCommandReads says whether every call the gate identified is an ordinary read. An opaque non-Lark part may
// still need the whole-command card, but does not turn those identified reads into writes. Zero calls, an explicit
// write, an outbound/delete action, an unknown CLI risk or an expanded command name keeps the existing wording.
func wholeCommandReads(gates []proto.CommandGate, v larkVerdict, write bool) bool {
	if write || v.cliWrite || v.dynamic || len(v.calls) == 0 {
		return false
	}
	return !slices.ContainsFunc(v.calls, func(c larkCall) bool { return !larkCallRead(gates, c) || !larkBypassSafe(c) })
}

// wholeCommandTitle is what a whole-command card names as the place the command acts on, from what the command
// shows: the gate it writes through (g, write: gateWrite's), lark-cli named in it, or a gate call read off it. ""
// when it shows none of them: the card names no service then (#4890).
func wholeCommandTitle(gates []proto.CommandGate, v larkVerdict, command string, g proto.CommandGate, write bool) string {
	if write {
		return g.Title
	}
	if v.cliWrite || larkCLINamed.MatchString(command) || slices.ContainsFunc(v.calls, func(c larkCall) bool { return c.CLI }) {
		return callTitle(gates, larkCall{Gate: -1})
	}
	if len(v.calls) > 0 {
		return callTitle(gates, v.calls[0])
	}
	if len(v.unread) > 0 {
		return callTitle(gates, larkCall{Gate: v.unread[0]})
	}
	return ""
}

// confirmLark asks the phone after larkCheck has applied the permission mode and action classification.
// Always grants cover routes, not whole commands. title is what the card names as the place it acts on, for the
// reason a refusal gives the model ("" for a card that names none).
// When it allows, card is how: the card, with who answered it, or that "Always allow" covered it (the manager's
// answer says it kept one).
func (n *Node) confirmLark(ctx context.Context, session string, ix proto.Interaction, ask []larkCall, confirmWrites bool, title string) (larkCheckOutcome, error) {
	if ix.ID == "" { // a gated tool's card has none yet: the ledger names the card by it
		ix.ID = "ix_" + randHex(6)
	}
	ix.Detachable = n.state != nil && n.isAssistantSession(session)
	res, ok, err := n.ask(ctx, ix)
	if err != nil {
		return larkCheckOutcome{}, err
	}
	if !ok || res.Allow == nil || !*res.Allow {
		dismissed := res.Dismiss || res.Cancel || res.Allow == nil || !ok
		msg := strings.TrimSpace(res.Message)
		if msg == "" {
			switch {
			case res.Pending:
				msg = approvalops.WaitingText + " The command was not executed."
			case dismissed:
				msg = "审批已取消或未完成，没有执行，请勿自动重试。"
			case title != "":
				msg = "用户没有同意这个" + title + "写操作，没有执行。"
			default:
				msg = "用户没有同意执行这条命令，没有执行。"
			}
		}
		out := larkCheckOutcome{
			decision:   "deny",
			reason:     msg,
			pending:    res.Pending,
			dismiss:    dismissed,
			approvalID: res.ApprovalID,
		}
		if ok && res.UserDenied() && !res.Pending {
			out.userDenied = true
			out.userNote = strings.TrimSpace(res.Message)
		}
		return out, nil
	}
	// Assistant conversations consult the hub on every call, including after revocation; hard confirm-writes boundaries never remember.
	if res.Always && !confirmWrites && !n.isAssistantSession(session) {
		for _, c := range ask {
			n.rememberLarkAlways(session, larkPattern(c))
		}
	}
	approvalID := ix.ID
	if res.ApprovalID != "" {
		approvalID = res.ApprovalID
	}
	card := proto.LarkLetThrough{Kind: proto.LarkThroughCard, InteractionID: approvalID, AnsweredBy: res.AnsweredBy}
	if res.Remembered { // the manager answered from an "Always allow" it kept: no card was answered for this one
		card = proto.LarkLetThrough{Kind: proto.LarkThroughAlways}
	}
	editedText := ""
	if ix.EditableText != "" && res.EditedText != "" && !res.Remembered {
		editedText = res.EditedText
	}
	return larkCheckOutcome{
		decision:   "allow",
		reason:     "用户在手机上同意了",
		card:       card,
		editedText: editedText,
		approvalID: res.ApprovalID,
	}, nil
}

func preToolDecision(decision, reason string) any {
	return map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": decision, "permissionDecisionReason": reason,
	}}
}

func (n *Node) larkAlways(session, pattern string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.larkAllowed[session+" "+pattern]
}

func (n *Node) rememberLarkAlways(session, pattern string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.larkAllowed == nil {
		n.larkAllowed = map[string]bool{}
	}
	n.larkAllowed[session+" "+pattern] = true
}

// assistantLark asks the hub for the assistant's Lark config (gate rules, lark-cli, plugins' command gates), at
// most once a minute (it may change); an empty config when the hub has none or is unreachable.
func (n *Node) assistantLark(ctx context.Context) proto.AssistantLark {
	n.mu.Lock()
	cached, at := n.larkCfg, n.larkCfgAt
	n.mu.Unlock()
	if time.Since(at) < time.Minute || n.peer == nil {
		return cached
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cfg proto.AssistantLark
	if err := n.peer.Call(ctx, "assistant.lark", map[string]any{}, &cfg); err != nil {
		n.logger.Printf("assistant: Feishu gate config from hub: %v", err) // never the config itself
		return cached
	}
	relayGates(&cfg)
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = cfg, time.Now()
	n.mu.Unlock()
	return cfg
}

// gateRefreshInterval: how often assistantLarkForGate asks the hub again when a gate's address is missing.
const gateRefreshInterval = 2 * time.Second

// assistantLarkForGate is assistantLark for a request at a gate's address: when the config has no such gate, or its
// address is not in its environment yet (a gate added or re-configured on the hub after the process started), the hub is
// asked again at most every gateRefreshInterval instead of waiting out assistantLark's minute (#13540).
func (n *Node) assistantLarkForGate(ctx context.Context, command string) proto.AssistantLark {
	cfg := n.assistantLark(ctx)
	missing := command == ""
	for i := range cfg.Gates {
		g := cfg.Gates[i]
		if g.Command != command {
			continue
		}
		missing = !g.Relay && g.Env[g.BaseEnv] == ""
		break
	}
	if !missing || n.peer == nil {
		return cfg
	}
	// Both the last success and the last attempt throttle it, so an outage does not call the hub on every gate request.
	n.mu.Lock()
	at, tried := n.larkCfgAt, n.larkCfgTryAt
	if time.Since(tried) < gateRefreshInterval || time.Since(at) < gateRefreshInterval {
		n.mu.Unlock()
		return cfg
	}
	n.larkCfgTryAt = time.Now()
	n.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var fresh proto.AssistantLark
	if err := n.peer.Call(ctx, "assistant.lark", map[string]any{}, &fresh); err != nil {
		n.logger.Printf("assistant: Feishu gate config from hub: %v", err) // never the config itself
		return cfg
	}
	relayGates(&fresh)
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = fresh, time.Now()
	n.mu.Unlock()
	return fresh
}

// relayGates makes each relayed gate (proto.CommandGate.Relay, #12904) one the gate proxy stands in for: an Auth of
// its own (no header: the manager puts the credentials on the call), so that everything that holds a gate with Auth to
// the proxy (larkgate.go, larkgate_env.go, gateproxy.go) holds this one too. Its socket and token stay out of the
// assistant's process (larkgate_env.go).
func relayGates(cfg *proto.AssistantLark) {
	for i := range cfg.Gates {
		if g := &cfg.Gates[i]; g.Relay && g.BaseEnv != "" && g.Auth == nil {
			g.Auth = &proto.GateAuth{}
		}
	}
}
