package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Decisions of the write gate.
const (
	Allow   = "allow"   // run it
	Confirm = "confirm" // the user allows it on the phone first (the default)
	Deny    = "deny"    // refuse, and tell the model why
)

// Rule is one rule of the write gate (hub.json connector_gate): an action of a connector — both path.Match
// patterns, "*" for any — whose arguments' top-level fields equal Match when it is set (a string value is a pattern
// too) gets Decision. The first matching rule decides; when none matches, the user confirms.
type Rule struct {
	Connector string         `json:"connector"`
	Action    string         `json:"action"`
	Match     map[string]any `json:"match,omitempty"`
	Decision  string         `json:"decision"`

	// fromUser marks a rule the user made from the ledger (SetUserRules), so a write it let through is told apart from one a
	// hand-written rule did (LetThrough.UserRule). Not in the JSON: no file can say it.
	fromUser bool
}

// Lark is the connector id whose rules are the assistant's Feishu writes (an extension's gated command, lark-cli). Those run on
// the assistant's machine, where node/larkgate.go reads the command: they are not the Registry's actions, so a rule
// of connector "lark" goes there instead (LarkRules) and its action is "<METHOD> <path>" in lark_gate's terms —
// "POST /conversations/*/messages", "CLI /im/+messages-send", "*" for any call.
const Lark = "lark"

// ValidateRules reports a rule the gate could not use, so a typo in hub.json stops the start instead of quietly
// asking (or worse, quietly allowing) something else.
func ValidateRules(rules []Rule) error {
	for i, r := range rules {
		switch r.Decision {
		case Allow, Confirm, Deny:
		default:
			return fmt.Errorf("connector_gate[%d]: decision %q is not allow, confirm or deny", i, r.Decision)
		}
		for _, p := range []string{r.Connector, r.Action} {
			if _, err := path.Match(p, ""); err != nil || p == "" {
				return fmt.Errorf("connector_gate[%d]: bad pattern %q", i, p)
			}
		}
		if r.Connector == Lark {
			if _, ok := larkRule(r); !ok {
				return fmt.Errorf("connector_gate[%d]: a lark action is \"<METHOD> <path>\" or \"*\", not %q", i, r.Action)
			}
		}
	}
	return nil
}

// LarkRules are the rules the assistant's Feishu gate gets (proto.AssistantLark.Rules): connector_gate's rules of
// connector "lark" first, then hub.json's older lark_gate ones, which keep working as they are; the node puts its
// built-in rules after them.
func LarkRules(rules []Rule, larkGate []proto.LarkRule) []proto.LarkRule {
	var out []proto.LarkRule
	for _, r := range rules {
		if r.Connector != Lark {
			continue
		}
		if lr, ok := larkRule(r); ok {
			out = append(out, lr)
		}
	}
	return append(out, larkGate...)
}

// GateRules are connector_gate's rules of connector id written as HTTP calls ("<METHOD> <path>" or "*"), for a
// plugin's command gate whose calls go to that connector's service (docs/extensions/extensions.md "Assistant"): the user's say first.
func GateRules(rules []Rule, id string) []proto.LarkRule {
	var out []proto.LarkRule
	for _, r := range rules {
		if r.Connector != id {
			continue
		}
		if lr, ok := larkRule(r); ok {
			out = append(out, lr)
		}
	}
	return out
}

// larkRule is r in lark_gate's terms: the action's method and path, Match as the body's fields.
func larkRule(r Rule) (proto.LarkRule, bool) {
	lr := proto.LarkRule{Method: "*", Path: "**", Body: r.Match, Action: r.Decision}
	if r.Action == "*" {
		return lr, true
	}
	method, p, ok := strings.Cut(strings.TrimSpace(r.Action), " ")
	p = strings.TrimSpace(p)
	if !ok || method == "" || !strings.HasPrefix(p, "/") {
		return lr, false
	}
	lr.Method, lr.Path = strings.ToUpper(method), p
	return lr, true
}

// Decide is what the gate says about the action name of connector id (whose manifest is m) with args: the first
// matching rule's decision, Confirm when none matches. An action the manifest lists in ConfirmAlways is never
// Allow: a rule that allows it (a "*" one, say) still has the user confirm on the phone. A call whose args give a field
// more than once is Deny whatever the rules say (repeatedField): no rule is read for it. A Deny or a Confirm with a
// Match reads a name whatever its capitals, so a field spelled in other capitals cannot slip past it (matchFieldsAny);
// an Allow reads the exact names.
func Decide(rules []Rule, m Manifest, id, name string, args json.RawMessage) string {
	d, _ := decide(rules, m, id, name, args)
	return d
}

// decide is Decide and the rule that said so: nil when no rule did (Confirm because none matched, or because
// ConfirmAlways turned an Allow into one; Deny because args give a field more than once).
func decide(rules []Rule, m Manifest, id, name string, args json.RawMessage) (string, *Rule) {
	if _, repeated := repeatedField(args); repeated {
		return Deny, nil
	}
	var fields map[string]any
	_ = json.Unmarshal(args, &fields)
	for _, r := range rules {
		if !matches(r.Connector, id) || !matches(r.Action, name) {
			continue
		}
		hit := matchFields(r.Match, fields)
		if r.Decision != Allow {
			hit = matchFieldsAny(r.Match, fields)
		}
		if !hit {
			continue
		}
		if r.Decision == Allow && m.AlwaysConfirms(name) {
			return Confirm, nil
		}
		return r.Decision, &r
	}
	return Confirm, nil
}

// repeatedField is the first top-level field args give more than once, as it was first written, and whether there is one.
// Two names are the same field when they are written alike or when encoding/json would take them for one (they
// differ only in capitals, by its case folding). A connector that decodes its arguments into a struct reads such a
// call by rules a map of the same text does not follow: it takes the last value in the plain case, but ignores a null
// and decodes an object or an array onto what the earlier one left. A rule reads the exact names and cannot know which
// value the write will use, and an extension written in another language may read the names some other way. So such a
// call is not run and no rule sees it; the model is told to give each field once (repeatedText). Only the top level is
// read, which is all a rule's Match reads.
func repeatedField(args json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(args))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return "", false
	}
	first := map[string]string{} // a name as folded -> as first written
	for dec.More() {
		t, err := dec.Token()
		key, isName := t.(string)
		if err != nil || !isName {
			return "", false
		}
		folded := strings.Map(foldRune, key)
		if was, again := first[folded]; again {
			return was, true
		}
		first[folded] = key
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return "", false
		}
	}
	return "", false
}

// repeatedText is what the model is told when repeatedField refuses its call: which field, and what to do.
func repeatedText(name, field string) string {
	if r := []rune(field); len(r) > 60 {
		field = string(r[:60]) + "…"
	}
	return fmt.Sprintf("The hub's gate does not run %s: the field %q is written more than once in its arguments (names that differ only in capitals count as the same field). Write each field once, spelled as the tool defines it.", name, field)
}

// foldRune is the smallest rune of r's case-folding orbit, so that names equal under strings.EqualFold map to the same
// string.
func foldRune(r rune) rune {
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		m = min(m, f)
	}
	return m
}

type confirmEachKey struct{}

// ConfirmEach is ctx for a call whose writes the user confirms every time, whatever a rule allows (a health
// conversation's, docs/assistant/health.md "Outbound"). Deny stays Deny.
func ConfirmEach(ctx context.Context) context.Context {
	return context.WithValue(ctx, confirmEachKey{}, true)
}

func confirmsEach(ctx context.Context) bool { v, _ := ctx.Value(confirmEachKey{}).(bool); return v }

// cardMost is the most of Describe's words the card shows (a program's could be any length): more than a Telegram
// message or a long comment, and the card says when it was cut.
const cardMost = 16000

// card is Describe's words as the card shows them: at most cardMost runes, saying how much was left out.
func card(summary string) string {
	r := []rune(summary)
	if len(r) <= cardMost {
		return summary
	}
	return string(r[:cardMost]) + fmt.Sprintf("\n\n（后面还有 %d 字，卡片只显示前 %d 字）", len(r)-cardMost, cardMost)
}

// gate lets the action name of c through or answers instead of it: Deny refuses, Confirm asks the user with the
// card's words (Describe, and who will see it) and refuses unless they allow it — or, when eligible under a hub-side
// guarded auto-confirm policy and the extension returns a valid short-lived credential, lets it through with an
// AutoConfirmApproval — Allow says nothing. A nil result means run it, with the very args the card was made from;
// how it was let through comes with it.
func gate(ctx context.Context, rules []Rule, autoConfirm []AutoConfirmPolicy, c Connector, a Actions, name string, args json.RawMessage, ask Ask) (*proto.ToolResult, LetThrough, *AutoConfirmApproval) {
	d, rule := decide(rules, c.Manifest(), c.ID(), name, args)
	if d == Allow && confirmsEach(ctx) {
		d = Confirm
	}
	var through LetThrough
	switch d {
	case Deny:
		text := "The hub's rules (connector_gate) do not allow " + name + "."
		if rule == nil { // no rule said it: the arguments give a field more than once
			field, _ := repeatedField(args)
			text = repeatedText(name, field)
		}
		res := proto.ErrorResult(proto.ToolErrorDeclined, text, 0)
		return &res, through, nil
	case Allow:
		through = LetThrough{Kind: ThroughRule, Rule: rule, UserRule: rule != nil && rule.fromUser}
	case Confirm:
		var (
			act     Action
			summary string
			err     error
		)
		// Guarded conditional auto-confirm requires ALL of:
		//  1. no explicit Confirm rule in connector_gate (rule == nil)
		//  2. not a ConfirmEach conversation (health / space / restricted)
		//  3. verified owner assistant caller (OwnerCallerOf)
		//  4. connector supporting ApprovalActions and carrying a hub-verified ExtensionIdentity
		//  5. an enabled hub-side AutoConfirmPolicy matching that extension, connector and exact action
		//  6. a valid, unexpired, TTL-bounded AutoConfirmGrant returned from negotiated actions.describe
		// If any condition is unmet, fall back to the ordinary phone confirmation card.
		if rule == nil && !confirmsEach(ctx) && OwnerCallerOf(ctx) {
			if aa, ok := a.(ApprovalActions); ok {
				if ic, ok := c.(IdentifiedConnector); ok {
					if ident, ok := ic.ExtensionIdentity(); ok {
						if match, ok := matchAutoConfirmPolicy(autoConfirm, ident, c.ID(), name); ok {
							var grant *AutoConfirmGrant
							act, summary, grant, err = aa.DescribeWithApproval(ctx, name, args, match.protocol)
							if err != nil {
								res := proto.TextResult(err.Error(), true)
								return &res, through, nil
							}
							summary = card(summary)
							if validateAutoConfirmGrant(grant, autoConfirmNow(), match.maxTTL) {
								through = LetThrough{
									Kind:    ThroughAutoConfirm,
									Summary: summary,
									AutoConfirm: &AutoConfirmAudit{
										Policy:          match.name,
										Extension:       ident.Extension,
										Version:         ident.Version,
										Source:          ident.Source,
										Protocol:        match.protocol,
										OwnerAuthorized: true,
									},
								}
								return nil, through, &AutoConfirmApproval{
									Mode:      ApprovalModeAutoConfirm,
									Token:     grant.Token,
									ExpiresAt: grant.ExpiresAt,
								}
							}
							// Missing, malformed, expired or over-TTL grant: fall back to the phone card using the
							// same Describe result without calling Describe a second time.
							goto confirmCard
						}
					}
				}
			}
		}
		act, summary, err = a.Describe(ctx, name, args)
		if err != nil {
			res := proto.TextResult(err.Error(), true)
			return &res, through, nil
		}
		summary = card(summary)
	confirmCard:
		through = LetThrough{Kind: ThroughCard, Summary: summary}
		if act.Visible != "" {
			summary += "\n\n谁会看到：" + act.Visible
		}
		askCtx, told := withAskOutcome(ctx)
		if ask == nil || !ask(askCtx, summary, args) {
			if outcome := *told; ask != nil && outcome != "" && outcome != proto.OutcomeDeclined {
				// Nobody answered the card (TellAskOutcome): not the user's decline, and never told as one.
				res := proto.ErrorResult(proto.ToolErrorUnanswered, prompts.RenderOutboundUnanswered(prompts.OutboundUnanswered{
					Outcome: outcome, Nothing: name + " did not run"}), 0)
				return &res, through, nil
			}
			res := proto.ErrorResult(proto.ToolErrorDeclined, "The user declined "+name+".", 0)
			return &res, through, nil
		}
	}
	return nil, through, nil
}

func matches(pattern, s string) bool {
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

func matchFields(want, got map[string]any) bool {
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			return false
		}
		if ws, isStr := w.(string); isStr {
			if gs, ok := g.(string); !ok || !matches(ws, gs) {
				return false
			}
		} else if !reflect.DeepEqual(w, g) {
			return false
		}
	}
	return true
}

// matchFieldsAny is matchFields that reads names without regard to capitals: a field of want is met when a key that is
// the same name as it, under encoding/json's case folding, has a value that meets it. A Deny or a Confirm uses it, so
// that a call cannot get past one by spelling a field in other capitals (a rule's own key may be spelled so, too). If
// several keys were the same name it would be enough that one of them met the field. Each field is judged by
// matchFields itself, so a pattern and a value compare as they always do.
func matchFieldsAny(want, got map[string]any) bool {
	for k, w := range want {
		name, met := strings.Map(foldRune, k), false
		for gk, g := range got {
			if strings.Map(foldRune, gk) == name && matchFields(map[string]any{k: w}, map[string]any{k: g}) {
				met = true
				break
			}
		}
		if !met {
			return false
		}
	}
	return true
}

type askOutcomeKey struct{}

// withAskOutcome is ctx for an Ask the gate calls, with where the Ask tells how its card ended (TellAskOutcome).
func withAskOutcome(ctx context.Context) (context.Context, *string) {
	told := new(string)
	return context.WithValue(ctx, askOutcomeKey{}, told), told
}

// TellAskOutcome is how an Ask that showed a card says how the card ended (proto.Resolution.Outcome), with the ctx the
// gate called it with: a refusal whose card the user did not decline themselves (it expired, was withdrawn or cleared,
// someone else answered it) is then no decline (proto.ToolErrorUnanswered). An Ask that tells nothing refuses as the
// user declining, as before.
func TellAskOutcome(ctx context.Context, outcome string) {
	if told, ok := ctx.Value(askOutcomeKey{}).(*string); ok {
		*told = outcome
	}
}
