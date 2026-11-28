package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/node/larkgateport"
	"github.com/nwverse/persona/server/internal/proto"
)

func larkGateRules() larkgateport.Gate { return larkgateport.Get(nodeModules()) }

// larkDecision is the gate's call on one call: its command gate's rules (the user's first, the hub put them there),
// or for a lark-cli command the configured rules and then the lark module's CLI risk/method rules (a rule for one:
// `{"method":"CLI","path":"im/+messages-send","action":"allow"}`).
func larkDecision(cfg proto.AssistantLark, c larkCall) string {
	if c.CLI {
		if action, ok := matchLark(cfg.Rules, c); ok {
			return action
		}
		if r := larkGateRules(); r != nil && r.CLIRead(c.Method, c.Risk) {
			return "allow"
		}
		return "confirm"
	}
	if c.Gate < 0 || c.Gate >= len(cfg.Gates) {
		return "confirm"
	}
	g := cfg.Gates[c.Gate]
	if c.fileBody && g.Auth == nil {
		return decideLarkFileBody(g.Rules, c)
	}
	rules := g.Rules
	read := larkGateRead(g, c)
	bodyScoped := read && slices.ContainsFunc(rules, func(candidate proto.LarkRule) bool {
		return len(candidate.Body) > 0 &&
			(candidate.Method == "*" || strings.EqualFold(candidate.Method, c.Method)) &&
			pathMatches(candidate.Path, c.Path)
	})
	// Deletion-classified GET/HEAD calls were confirmed before the read default
	// was introduced. Neither an unmatched read nor a generic confirm catch-all
	// may silently downgrade that existing boundary.
	cardFreeRead := read && !bodyScoped && !larkDeletionCall(c)
	if action, ok := matchLark(rules, c); ok {
		rule, _ := firstLark(rules, c)
		// A blanket read allow is the common default, not an authorization for
		// the Agent API's side-effecting GET/HEAD routes. An exact or narrower
		// route rule still has its explicit, first-match meaning.
		if !read && action == "allow" && strings.Trim(rule.Path, "/") == "**" && len(rule.Body) == 0 &&
			(c.Method == http.MethodGet && strings.EqualFold(rule.Method, http.MethodGet) ||
				c.Method == http.MethodHead && strings.EqualFold(rule.Method, http.MethodHead)) {
			return "confirm"
		}
		if read && action == "confirm" {
			if rule.Method == "*" && strings.Trim(rule.Path, "/") == "**" && len(rule.Body) == 0 && cardFreeRead {
				return "allow"
			}
		}
		return action
	}
	// A command gate's explicit exceptions come first. An ordinary GET/HEAD is
	// a read even when the gate only declares its writes and a catch-all, but a
	// body-scoped rule still has to match before that read can go.
	if cardFreeRead {
		return "allow"
	}
	return "confirm"
}

// decideLarkFileBody decides a command-gate call whose body comes from @file on a gate without Auth.
// Without a proxy to enforce rules on the actual HTTP body at send time, check-time @file contents
// cannot select a body-scoped allow rule or silently clear an earlier body-scoped deny/confirm rule.
// Body-independent rules without a preceding body-scoped restriction, and body-scoped deny/confirm
// rules that match the check-time snapshot, still apply.
func decideLarkFileBody(rules []proto.LarkRule, c larkCall) string {
	sawBodyRestriction := false
	for _, r := range rules {
		if (r.Method != "*" && !strings.EqualFold(r.Method, c.Method)) || !pathMatches(r.Path, c.Path) {
			continue
		}
		if len(r.Body) > 0 {
			if r.Action == "allow" {
				continue
			}
			if bodyMatches(r.Body, c.Body) {
				return r.Action
			}
			sawBodyRestriction = true
			continue
		}
		if r.Action == "allow" && sawBodyRestriction {
			return "confirm"
		}
		return r.Action
	}
	return "confirm"
}

// larkRead: a call that only reads (lark-cli's own read risk via the lark module, or an HTTP GET / HEAD).
func larkRead(c larkCall) bool {
	if c.CLI {
		r := larkGateRules()
		return r != nil && r.CLIRead(c.Method, c.Risk)
	}
	return c.Method == "GET" || c.Method == "HEAD"
}

// Agent API's NOT_READ_ONLY_GET_ROUTES are shared with the manager's ledger
// through proto.AgentAPIWriteGET. Ordinary GET/HEAD routes remain card-free.
func larkGateRead(g proto.CommandGate, c larkCall) bool {
	if larkRead(c) && (c.Method == http.MethodGet || c.Method == http.MethodHead) {
		if _, ok := gateAgentToken(g); ok && proto.AgentAPIWriteGET(c.Method, c.Path) {
			return false
		}
	}
	return larkRead(c)
}

func larkCallRead(gates []proto.CommandGate, c larkCall) bool {
	if !c.CLI && c.Gate >= 0 && c.Gate < len(gates) {
		return larkGateRead(gates[c.Gate], c)
	}
	return larkRead(c)
}

// matchLark is the action of the first rule that matches the call.
func matchLark(rules []proto.LarkRule, c larkCall) (string, bool) {
	r, ok := firstLark(rules, c)
	return r.Action, ok
}

// firstLark is the first rule that matches the call.
func firstLark(rules []proto.LarkRule, c larkCall) (proto.LarkRule, bool) {
	for _, r := range rules {
		if (r.Method == "*" || strings.EqualFold(r.Method, c.Method)) && pathMatches(r.Path, c.Path) && bodyMatches(r.Body, c.Body) {
			return r, true
		}
	}
	return proto.LarkRule{}, false
}

func pathMatches(pattern, path string) bool {
	p := strings.Split(strings.Trim(pattern, "/"), "/")
	s := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range p {
		if seg == "**" {
			return true
		}
		if i >= len(s) || (seg != "*" && seg != s[i]) {
			return false
		}
	}
	return len(p) == len(s)
}

func bodyMatches(want map[string]any, body string) bool {
	if len(want) == 0 {
		return true
	}
	var got map[string]any
	if json.Unmarshal([]byte(body), &got) != nil {
		return false
	}
	for k, v := range want {
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

// larkPattern: the call's route with its ids blanked, what "Always allow for this session" remembers.
func larkPattern(c larkCall) string {
	segs := strings.Split(strings.Trim(c.Path, "/"), "/")
	for i, s := range segs {
		if strings.ContainsAny(s, "0123456789") || len(s) > 24 {
			segs[i] = "*"
		}
	}
	return c.Method + " /" + strings.Join(segs, "/")
}

// genericGateActions holds generic HTTP deletion and outbound action patterns shared by all proto.CommandGates.
// lark-cli patterns come from the lark module via larkgateport, but the OpenAPI IM route stays in core because
// non-CLI extension gates must still require confirmation when the lark module is excluded or switched off.
var genericGateActions = []struct {
	re       *lazyre.Regexp
	what     string
	boundary int
}{
	{lazyre.New(`^DELETE `), "删除", larkgateport.BoundaryDeletion},
	{lazyre.New(`^(?:GET|HEAD|POST|PUT|PATCH|OPTIONS) /.*(?:delete|remove|trash|recall)`), "删除", larkgateport.BoundaryDeletion},
	{lazyre.New(`^(POST|PUT|PATCH) /(?:open-apis/)?im/(?:v[0-9]+/)?(?:messages|batch_messages|threads)(?:/|$)`), "发消息", larkgateport.BoundaryOutbound},
	{lazyre.New(`^POST /(?:conversations/[^/]+/messages/[^/]+/replies|messages/[^/]+/replies)(?:/|$)`), "回复消息", larkgateport.BoundaryOutbound},
	{lazyre.New(`^POST /(?:conversations/[^/]+/(?:messages|files|images)|messages/[^/]+/(?:reply|forward|recall|reactions))`), "发消息", larkgateport.BoundaryOutbound},
	{lazyre.New(`^(POST|PUT|PATCH) /.*(?:comments|replies)(?:/|$)`), "发表评论", larkgateport.BoundaryOutbound},
	{lazyre.New(`^POST /.*(?:mail|mailboxes).*/(?:send|reply|forward)$`), "发邮件", larkgateport.BoundaryOutbound},
}

func classifyGateAction(method, path string) (string, int) {
	target := method + " " + path
	for _, a := range genericGateActions[:2] {
		if a.re.MatchString(target) {
			return a.what, a.boundary
		}
	}
	if r := larkGateRules(); r != nil {
		if what, boundary, ok := r.ClassifyAction(method, path); ok {
			return what, boundary
		}
	}
	for _, a := range genericGateActions[2:] {
		if a.re.MatchString(target) {
			return a.what, a.boundary
		}
	}
	return "", larkgateport.BoundaryOrdinary
}

func larkDeletionCall(c larkCall) bool {
	_, boundary := classifyGateAction(c.Method, c.Path) // the lark module's rules live in modules/lark/larkgate now
	return boundary == larkgateport.BoundaryDeletion
}

// larkSummary is the phone card's text: what each write does, then its method, path and body.
func larkSummary(gates []proto.CommandGate, calls []larkCall) string {
	return larkSummaryWithContext(gates, calls, nil, i18n.Default)
}

// larkSummaryWithContext renders clean human-readable summaries for unambiguous message sends/replies (with
// resolved conversation/reply context when available, or target id fallback otherwise). Ambiguous, dynamic,
// query-bearing, extra-field, or non-message calls keep the full method/path/body summary.
func larkSummaryWithContext(gates []proto.CommandGate, calls []larkCall, contexts []larkCardContext, lang i18n.Lang) string {
	var b strings.Builder
	for i, c := range calls {
		if i > 0 {
			b.WriteString("\n\n")
		}
		what := "写操作"
		if larkCallRead(gates, c) {
			what = "读操作"
		}
		if action, _ := classifyGateAction(c.Method, c.Path); action != "" {
			what = action
		}
		if c.Gate >= 0 && c.Gate < len(gates) {
			for _, a := range gates[c.Gate].Actions {
				if re, err := regexp.Compile(a.Pattern); err == nil && re.MatchString(c.Method+" "+c.Path) {
					what = a.What
					break
				}
			}
		}
		if c.CLI {
			if r := larkGateRules(); r != nil {
				b.WriteString(r.CLISummary(c.Method, c.Path, c.Risk, c.Args, what))
				continue
			}
			b.WriteString("lark-cli " + c.Method + " " + c.Path)
			continue
		}
		if i < len(contexts) {
			if summary, ok := larkMessageSummary(gates, c, contexts[i], lang); ok {
				b.WriteString(summary)
				continue
			}
		}
		b.WriteString("以你的身份在" + callTitle(gates, c) + "上" + what + "\n" + c.Method + " " + c.Path)
		if c.Query != "" {
			b.WriteString("?" + c.Query)
		}
		if body := prettyBody(c.Body); body != "" {
			b.WriteString("\n" + body)
		}
	}
	return b.String()
}

func prettyBody(body string) string {
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		if b, err := json.MarshalIndent(v, "", "  "); err == nil {
			body = string(b)
		}
	}
	if r := []rune(body); len(r) > 2000 {
		body = string(r[:2000]) + "…"
	}
	return body
}

// larkBypassSafe uses the same action classification as the card. Unknown CLI risk is not an ordinary write.
func larkBypassSafe(c larkCall) bool {
	if c.CLI {
		r := larkGateRules()
		if r == nil {
			return false
		}
		if c.Method == "CLI" {
			return r.CLIBypassSafe(c.Path, c.Risk, c.Args)
		}
	}
	if _, boundary := classifyGateAction(c.Method, c.Path); boundary != larkgateport.BoundaryOrdinary {
		return false
	}
	return httpMethod.MatchString(c.Method) && !strings.Contains(c.Path, "$")
}

// larkCallOutwardWrite reports whether c is a gated connector write acting as the owner
// (not a read-only call and not an explicit lark-cli --as bot call).
func larkCallOutwardWrite(gates []proto.CommandGate, c larkCall) bool {
	if larkCallRead(gates, c) {
		return false
	}
	if c.CLI && c.Method == "CLI" {
		if r := larkGateRules(); r != nil && r.CLIBotActor(c.Args) {
			return false
		}
	}
	return true
}

// wholeCommandOutwardWrite reports whether a complex-command gate card for title is an outward
// write as the owner, matching the gate's write wording and excluding read-only or bot-only calls.
func wholeCommandOutwardWrite(gates []proto.CommandGate, v larkVerdict, write bool, title string) bool {
	if title == "" || wholeCommandReads(gates, v, write) {
		return false
	}
	if write || len(v.calls) == 0 || v.unknown || v.dynamic || v.unparsed {
		return true
	}
	return slices.ContainsFunc(v.calls, func(c larkCall) bool { return larkCallOutwardWrite(gates, c) })
}
