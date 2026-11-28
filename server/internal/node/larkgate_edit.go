package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

// larkGateWithEdit keeps the ordinary gate untouched for ineligible commands. An eligible Claude Code Lark
// send/reply gets editable_text; when the owner changes the text, larkCheckOutcome skips granting the original call,
// and applyLarkEdit classifies, validates, and grants only the rebuilt command once.
func (n *Node) larkGateWithEdit(ctx context.Context, in hookInput, ix proto.Interaction) (any, error) {
	var a struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(in.ToolInput, &a) != nil || a.Command == "" {
		return nil, nil
	}
	cfg := n.assistantLark(ctx)
	call, span, original, ok := n.editableLarkCall(cfg, a.Command, in.Cwd)
	if !ok {
		return n.larkGate(ctx, in, ix)
	}
	ix.EditableText = original
	out, err := n.larkCheckOutcome(ctx, in.SessionID, in.Cwd, a.Command, ix)
	if err != nil || out.decision == "" {
		return nil, err
	}
	if out.editedText == "" || out.editedText == original || out.decision != "allow" {
		return preToolDecision(out.decision, out.reason), nil
	}
	card := out.card
	if card.InteractionID == "" {
		card.InteractionID = ix.ID
	}
	updated, err := n.applyLarkEdit(ctx, cfg, in.SessionID, card, a.Command, in.Cwd, call, span, out.editedText)
	if err != nil {
		return preToolDecision("deny", err.Error()), nil
	}
	var input map[string]any
	if err := json.Unmarshal(in.ToolInput, &input); err != nil {
		return preToolDecision("deny", "edited command input could not be read"), nil
	}
	input["command"] = updated
	hookOut := preToolDecision("allow", out.reason).(map[string]any)
	hookOut["hookSpecificOutput"].(map[string]any)["updatedInput"] = input
	return hookOut, nil
}

type larkBodySpan struct{ start, end int }

func (n *Node) editableLarkCall(cfg proto.AssistantLark, command, cwd string) (larkCall, larkBodySpan, string, bool) {
	if !larkLoneGateCall(command, cfg.Gates) {
		return larkCall{}, larkBodySpan{}, "", false
	}
	v := classifyLarkWith(command, cfg.Gates, cwd, n.larkCLIRisk(cfg), n.larkLoginPATH)
	if v.deny != "" || v.other || v.unparsed || v.unknown || v.unsure || len(v.calls) != 1 {
		return larkCall{}, larkBodySpan{}, "", false
	}
	c := v.calls[0]
	if c.CLI || c.Gate < 0 || c.Gate >= len(cfg.Gates) || cfg.Gates[c.Gate].Auth == nil || c.relFile || c.fileBody || c.dataInQuery || c.queryUnknown {
		return larkCall{}, larkBodySpan{}, "", false
	}
	if kind, _, _ := larkMessageTarget(c); kind == "" {
		return larkCall{}, larkBodySpan{}, "", false
	}
	_, original, ok := editableTextFields(c.Body)
	if !ok {
		return larkCall{}, larkBodySpan{}, "", false
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(tree.Stmts) != 1 {
		return larkCall{}, larkBodySpan{}, "", false
	}
	var spans []larkBodySpan
	syntax.Walk(tree.Stmts[0], func(node syntax.Node) bool {
		if w, ok := node.(*syntax.Word); ok {
			value, dynamic := executionWord(w)
			if !dynamic && value == c.Body {
				spans = append(spans, larkBodySpan{int(w.Pos().Offset()), int(w.End().Offset())})
			}
		}
		return true
	})
	if len(spans) != 1 {
		return larkCall{}, larkBodySpan{}, "", false
	}
	return c, spans[0], original, true
}

// editableTextFields accepts only a JSON object with one top-level text field. Every other field stays as raw JSON so
// mentions and ids are not reserialized or converted through float64. Duplicate keys are not an editable object.
func editableTextFields(body string) (map[string]json.RawMessage, string, bool) {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, "", false
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, "", false
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return nil, "", false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, "", false
		}
		fields[name] = raw
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, "", false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "", false
	}
	var text string
	if err := json.Unmarshal(fields["text"], &text); err != nil || strings.TrimSpace(text) == "" {
		return nil, "", false
	}
	return fields, text, true
}

func (n *Node) applyLarkEdit(ctx context.Context, oldCfg proto.AssistantLark, session string, card proto.LarkLetThrough, command, cwd string, old larkCall, span larkBodySpan, edited string) (string, error) {
	if old.Gate < 0 || old.Gate >= len(oldCfg.Gates) || oldCfg.Gates[old.Gate].Auth == nil {
		return "", fmt.Errorf("the original command has no authenticated gate")
	}
	oldFields, originalText, rewrittenBody, err := buildEditedLarkBody(old.Body, edited)
	if err != nil {
		return "", err
	}
	// Replace the one literal body word, never the route, query, mentions or another occurrence of the same text.
	// larkCheckOutcome skipped granting the original call once edited_text differed, so any failure leaves zero grants.
	updated, ok := editableSplice(command, old, span, string(rewrittenBody))
	if !ok {
		return "", fmt.Errorf("the edited command could not be rebuilt")
	}
	command = updated
	gateCmd := oldCfg.Gates[old.Gate].Command
	freshCfg := n.assistantLark(ctx)
	freshGate := slices.IndexFunc(freshCfg.Gates, func(g proto.CommandGate) bool { return g.Command == gateCmd && g.Auth != nil })
	if freshGate < 0 {
		return "", fmt.Errorf("the edited command has no authenticated gate")
	}
	v := classifyLarkWith(command, freshCfg.Gates, cwd, n.larkCLIRisk(freshCfg), n.larkLoginPATH)
	if v.deny != "" || v.other || v.unparsed || v.unknown || v.unsure || len(v.calls) != 1 {
		return "", fmt.Errorf("the edited command is no longer one standalone gated call")
	}
	c := v.calls[0]
	if c.Gate != freshGate || c.Method != old.Method || c.Path != old.Path || c.Query != old.Query || c.CLI || c.relFile {
		return "", fmt.Errorf("the edited command changed its tool, method, path or query")
	}
	if kind, _, _ := larkMessageTarget(c); larkDecision(freshCfg, c) == "deny" || kind == "" {
		return "", fmt.Errorf("the edited text is denied by the same gate")
	}
	if err := verifyEditedLarkBody(oldFields, c.Body, edited); err != nil {
		return "", err
	}
	through := card
	through.Kind = proto.LarkThroughCard
	through.EditedFrom = originalText
	if err := n.grantEdited(freshCfg, session, c, through); err != nil {
		return "", err
	}
	return command, nil
}

// applyLarkProxyEdit validates the edited text and mints a one-use rewrite grant for a local assistant_environment
// runtime whose permission response cannot rewrite tool_input. larkCheckOutcome never minted an unedited grant when
// edited_text differed; when the runtime executes its unmodified original command, gateProxy matches the original
// method, path, query and exact JSON body once and forwards the rewritten JSON body.
func (n *Node) applyLarkProxyEdit(ctx context.Context, oldCfg proto.AssistantLark, session string, card proto.LarkLetThrough, old larkCall, edited string) error {
	if old.Gate < 0 || old.Gate >= len(oldCfg.Gates) || oldCfg.Gates[old.Gate].Auth == nil {
		return fmt.Errorf("the original command has no authenticated gate")
	}
	oldFields, originalText, rewrittenBody, err := buildEditedLarkBody(old.Body, edited)
	if err != nil {
		return err
	}
	if err := verifyEditedLarkBody(oldFields, string(rewrittenBody), edited); err != nil {
		return err
	}
	gateCmd := oldCfg.Gates[old.Gate].Command
	freshCfg := n.assistantLark(ctx)
	freshGate := slices.IndexFunc(freshCfg.Gates, func(g proto.CommandGate) bool { return g.Command == gateCmd && g.Auth != nil })
	if freshGate < 0 {
		return fmt.Errorf("the edited command has no authenticated gate")
	}
	editedCall := old
	editedCall.Gate = freshGate
	editedCall.Body = string(rewrittenBody)
	if kind, _, _ := larkMessageTarget(editedCall); larkDecision(freshCfg, editedCall) == "deny" || kind == "" {
		return fmt.Errorf("the edited text is denied by the same gate")
	}
	pinnedOriginal := old
	pinnedOriginal.Gate = freshGate
	through := card
	through.Kind = proto.LarkThroughCard
	through.EditedFrom = originalText
	return n.grantEditedWithRewrite(freshCfg, session, pinnedOriginal, rewrittenBody, through)
}

func buildEditedLarkBody(oldBody, edited string) (map[string]json.RawMessage, string, []byte, error) {
	oldFields, originalText, ok := editableTextFields(oldBody)
	if !ok || strings.TrimSpace(edited) == "" {
		return nil, "", nil, fmt.Errorf("edited text must not be empty")
	}
	newFields := maps.Clone(oldFields)
	textJSON, err := json.Marshal(edited)
	if err != nil {
		return nil, "", nil, err
	}
	newFields["text"] = textJSON
	rewrittenBody, err := json.Marshal(newFields)
	if err != nil {
		return nil, "", nil, err
	}
	if len(rewrittenBody) > gateBodyMax {
		return nil, "", nil, fmt.Errorf("the edited body exceeds the maximum allowed size")
	}
	return oldFields, originalText, rewrittenBody, nil
}

func verifyEditedLarkBody(oldFields map[string]json.RawMessage, newBody, edited string) error {
	newFields, newText, ok := editableTextFields(newBody)
	if !ok || newText != edited {
		return fmt.Errorf("the edited body does not carry the requested text")
	}
	if len(newFields) != len(oldFields) {
		return fmt.Errorf("the edited body changed a field other than text")
	}
	for key, before := range oldFields {
		if key == "text" {
			continue
		}
		after, present := newFields[key]
		if !present || !compactJSONEqual(before, after) {
			return fmt.Errorf("the edited body changed a field other than text")
		}
	}
	return nil
}

func compactJSONEqual(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var ab, bb bytes.Buffer
	if json.Compact(&ab, a) != nil || json.Compact(&bb, b) != nil {
		return false
	}
	return bytes.Equal(ab.Bytes(), bb.Bytes())
}

func editableSplice(command string, old larkCall, span larkBodySpan, body string) (string, bool) {
	if span.start < 0 || span.end < span.start || span.end > len(command) {
		return "", false
	}
	segment := command[span.start:span.end]
	tree, err := syntax.NewParser().Parse(strings.NewReader(segment), "")
	if err != nil || len(tree.Stmts) != 1 {
		return "", false
	}
	stmt := tree.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || stmt.Background || stmt.Coprocess || stmt.Negated || len(stmt.Redirs) != 0 || len(call.Assigns) != 0 || len(call.Args) != 1 {
		return "", false
	}
	value, dynamic := executionWord(call.Args[0])
	if dynamic || value != old.Body {
		return "", false
	}
	return command[:span.start] + shellQuote(body) + command[span.end:], true
}

// grantEdited always pins the exact JSON body, including when the edited text contains '$'. The ordinary grant path's
// dynamic-body wildcard is deliberately not used here.
func (n *Node) grantEdited(cfg proto.AssistantLark, session string, c larkCall, through proto.LarkLetThrough) error {
	return n.grantEditedWithRewrite(cfg, session, c, nil, through)
}

func (n *Node) grantEditedWithRewrite(cfg proto.AssistantLark, session string, c larkCall, rewriteBody []byte, through proto.LarkLetThrough) error {
	if c.Gate < 0 || c.Gate >= len(cfg.Gates) || cfg.Gates[c.Gate].Auth == nil {
		return fmt.Errorf("the edited command has no authenticated gate")
	}
	var body any
	dec := json.NewDecoder(strings.NewReader(c.Body))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON in body")
	}
	g := gateGrant{command: cfg.Gates[c.Gate].Command, method: strings.ToUpper(c.Method), path: c.Path, query: gateQueryKey(c.Query), account: c.Account,
		body: body, bodyBound: true, bodyUseNumber: true, rewriteBody: bytes.Clone(rewriteBody), until: time.Now().Add(gateGrantFor), session: session, through: through}
	p := n.gates()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	p.grants = append(p.grants, g)
	if len(p.grants) > gateGrantsMax {
		p.grants = p.grants[len(p.grants)-gateGrantsMax:]
	}
	return nil
}
