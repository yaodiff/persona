package orcart

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Structured agent sessions: Orca's own chats around Claude Code / Codex (no terminal), driven with agentSession.*
// on the same socket. Only the unary methods work here (subscribe* need Orca's paired WebSocket). Writes carry an
// envelope Orca checks: an operation id (replaying the same id with the same payload answers as the first time) and
// a fingerprint of the payload. Learned from Orca 1.4.243 / 1.4.246 (docs/integrations/orca.md "Structured sessions").

// SessionPanePrefix starts the pane key of a structured session's row in worktree.ps:
// "structured-agent-session-<session id>:<uuid>".
const SessionPanePrefix = "structured-agent-session-"

// SessionOf is the structured session a worktree.ps pane key names; ok false for a terminal's pane.
func SessionOf(paneKey string) (id string, ok bool) {
	rest, ok := strings.CutPrefix(paneKey, SessionPanePrefix)
	if !ok {
		return "", false
	}
	id, _, _ = strings.Cut(rest, ":")
	return id, id != ""
}

const (
	// HistoryTimeout bounds agentSession.history (a page can be a megabyte).
	HistoryTimeout = 10 * time.Second
	// WriteTimeout bounds a write: Orca answers once it accepted it (a send does not wait for the agent).
	WriteTimeout = 30 * time.Second
)

// OperationID is a write's clientOperationId: Orca's "<13-digit ms>-<32 hex>". The same key (a message's
// client_msg_id) gives the same hex, so a retry with the same at replays instead of sending twice; "" is random.
func OperationID(at time.Time, key string) string {
	var hexPart string
	if key != "" {
		sum := sha256.Sum256([]byte(key))
		hexPart = hex.EncodeToString(sum[:16])
	} else {
		var b [16]byte
		_, _ = rand.Read(b[:])
		hexPart = hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%013d-%s", at.UnixMilli(), hexPart)
}

// Fingerprint is a write's payloadFingerprint: the SHA-256 of the canonical JSON of {method, sessionId, fields}
// (keys sorted, no spaces, strings escaped as JavaScript's JSON.stringify does), in lowercase hex.
func Fingerprint(method, sessionID string, fields any) (string, error) {
	f, err := plain(fields)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := canon(&b, map[string]any{"method": method, "sessionId": sessionID, "fields": f}); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

// plain is v as JSON values (maps, slices, strings, numbers kept as written, bools, nil).
func plain(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out any
	return out, d.Decode(&out)
}

func canon(b *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case json.Number:
		b.WriteString(x.String())
	case string:
		jsString(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canon(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys) // the keys are ASCII: byte order is JavaScript's UTF-16 order
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			jsString(b, k)
			b.WriteByte(':')
			if err := canon(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("orca fingerprint: unexpected %T", v)
	}
	return nil
}

// jsString writes s as JSON.stringify does: Go's escaping without the HTML escapes and with U+2028 / U+2029 raw.
func jsString(b *bytes.Buffer, s string) {
	var e bytes.Buffer
	enc := json.NewEncoder(&e)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := bytes.TrimSuffix(e.Bytes(), []byte("\n"))
	out = bytes.ReplaceAll(out, []byte(`\u2028`), []byte("\u2028"))
	out = bytes.ReplaceAll(out, []byte(`\u2029`), []byte("\u2029"))
	b.Write(out)
}

// Write is one agentSession write: the session, its runtime fence (agentSession.history's page.fence) and the
// operation id (OperationID).
type Write struct {
	SessionID   string
	Fence       int64
	OperationID string
}

// mutate sends a write: fields (hashed under hashMethod, Orca's name for it) next to the envelope. A refusal in the
// result comes back as an *Error with Orca's refusal code.
func (c *Client) mutate(ctx context.Context, method, hashMethod string, w Write, fields map[string]any, out any) error {
	fence := w.Fence
	if fence <= 0 {
		fence = 1 // hosts since 1.4.246 ignore it; a positive number passes the older ones' schema
	}
	return c.write(ctx, method, hashMethod, w, fence, fields, out)
}

// write is mutate with the envelope's expectedRuntimeFence as given (nil for a session not created yet).
func (c *Client) write(ctx context.Context, method, hashMethod string, w Write, fence any, fields map[string]any, out any) error {
	fp, err := Fingerprint(hashMethod, w.SessionID, fields)
	if err != nil {
		return err
	}
	params := map[string]any{"envelope": map[string]any{"sessionId": w.SessionID, "clientOperationId": w.OperationID,
		"expectedRuntimeFence": fence, "payloadFingerprint": fp}}
	for k, v := range fields {
		params[k] = v
	}
	var res struct {
		OK      bool            `json:"ok"`
		Value   json.RawMessage `json:"value"`
		Refusal *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"refusal"`
	}
	if err := c.Call(ctx, WriteTimeout, method, params, &res); err != nil {
		return err
	}
	if !res.OK {
		e := &Error{Method: method, Code: "agent_session_refused"}
		if res.Refusal != nil {
			e.Code, e.Message = res.Refusal.Code, res.Refusal.Message
		}
		return e
	}
	if out != nil && len(res.Value) > 0 {
		return json.Unmarshal(res.Value, out)
	}
	return nil
}

// Head is what a session's newest history page says about it.
type Head struct {
	// Fence is the runtime fence writes name (expectedRuntimeFence).
	Fence int64
	// Provider is the agent's own session: Claude Code's session id (its transcript), Codex's thread.
	Provider string
	// Working: a turn is running; Prompts are the ids of the approvals and questions waiting for an answer.
	Working bool
	Prompts []string
	// Epoch is the journal's incarnation (a rewind starts a new one); history cursors name it.
	Epoch string
}

// Head reads the newest page of a session's history (the smallest one Orca gives).
func (c *Client) Head(ctx context.Context, sessionID string) (*Head, error) {
	var res struct {
		OK   bool `json:"ok"`
		Page struct {
			Epoch               string   `json:"epoch"`
			Fence               int64    `json:"fence"`
			Working             bool     `json:"working"`
			ActionablePromptIDs []string `json:"actionablePromptIds"`
		} `json:"page"`
		ProviderSession *struct {
			ID string `json:"id"`
		} `json:"providerSession"`
	}
	err := c.Call(ctx, HistoryTimeout, "agentSession.history",
		map[string]any{"sessionId": sessionID, "direction": "tail", "limit": 1}, &res)
	if err != nil {
		return nil, err
	}
	h := &Head{Fence: res.Page.Fence, Working: res.Page.Working, Prompts: res.Page.ActionablePromptIDs, Epoch: res.Page.Epoch}
	if res.ProviderSession != nil {
		h.Provider = res.ProviderSession.ID
	}
	return h, nil
}

// PromptOption is one choice of an approval or a question.
type PromptOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// PromptQuestion is one question of a question prompt.
type PromptQuestion struct {
	ID          string         `json:"id"`
	Question    string         `json:"question"`
	Header      string         `json:"header,omitempty"`
	MultiSelect bool           `json:"multiSelect"`
	Options     []PromptOption `json:"options"`
}

// Prompt is an approval or a question waiting for an answer (agentSession.currentRequests).
type Prompt struct {
	ItemID   string `json:"itemId"`
	Revision int64  `json:"revision"`
	// AgentID is set for a subagent's prompt.
	AgentID string `json:"agentId,omitempty"`
	Body    struct {
		Kind           string `json:"kind"` // approval | question
		Title          string `json:"title,omitempty"`
		DisplayName    string `json:"displayName,omitempty"`
		Description    string `json:"description,omitempty"`
		DecisionReason string `json:"decisionReason,omitempty"`
		// Detail is the request rendered as text (a command, a file and its edit).
		Detail         *string `json:"detail,omitempty"`
		MatchedAskRule *struct {
			ToolName string `json:"toolName"`
		} `json:"matchedAskRule,omitempty"`
		// Subject is set for a plan to approve (Claude's ExitPlanMode).
		Subject *struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		} `json:"subject,omitempty"`
		Options    []PromptOption   `json:"options"`
		Question   string           `json:"question,omitempty"`
		Questions  []PromptQuestion `json:"questions,omitempty"`
		Resolution struct {
			State string `json:"state"`
		} `json:"resolution"`
	} `json:"body"`
}

// requestsProbe is the version asked first: never Orca's, so it answers with its current marker.
const requestsProbe = "00000000-0000-4000-8000-000000000000"

// Prompts is agentSession.currentRequests: the approvals and questions of a session waiting for an answer. Orca sends
// them as one JSON text in pieces under a marker; a marker that changes on the way starts the read again.
func (c *Client) Prompts(ctx context.Context, sessionID string) ([]Prompt, error) {
	type piece struct {
		Marker struct {
			Version string `json:"version"`
		} `json:"marker"`
		JSON       string `json:"json"`
		NextCursor *int   `json:"nextCursor"`
		Restart    bool   `json:"restart"`
	}
	version := requestsProbe
	for attempt := 0; attempt < 4; attempt++ {
		var text strings.Builder
		cursor, restarted := 0, false
		for pieces := 0; pieces < 256; pieces++ {
			var p piece
			params := map[string]any{"sessionId": sessionID, "version": version}
			if version != requestsProbe {
				params["cursor"] = cursor
			}
			if err := c.Call(ctx, CallTimeout, "agentSession.currentRequests", params, &p); err != nil {
				return nil, err
			}
			if p.Restart || p.Marker.Version != version {
				version, restarted = p.Marker.Version, true
				break
			}
			text.WriteString(p.JSON)
			if p.NextCursor == nil {
				var out []Prompt
				if strings.TrimSpace(text.String()) == "" {
					return nil, nil
				}
				if err := json.Unmarshal([]byte(text.String()), &out); err != nil {
					return nil, fmt.Errorf("orca agentSession.currentRequests: %w", err)
				}
				pending := out[:0]
				for _, p := range out {
					if p.Body.Resolution.State == "" || p.Body.Resolution.State == "pending" {
						pending = append(pending, p)
					}
				}
				return pending, nil
			}
			cursor = *p.NextCursor
		}
		if !restarted || version == "" {
			break
		}
	}
	return nil, fmt.Errorf("orca agentSession.currentRequests: the requests kept changing")
}

// Options is the part of agentSession.options Persona shows: the session's permission mode, interaction mode ("plan"
// or "default"; "" from hosts without one), effort and model, Orca's words; the models it lists (their ids in Orca's
// order, their names, the one it runs when none is picked), the efforts each takes (by model id, weakest first; none
// when Orca does not know them), and whether the chat can be rewound.
type Options struct {
	PermissionMode  string
	InteractionMode string
	Effort          string
	Model           string
	Models          []string
	ModelNames      map[string]string
	DefaultModel    string
	Efforts         map[string][]string
	Rewind          bool
}

// Options reads a session's current options.
func (c *Client) Options(ctx context.Context, sessionID string) (*Options, error) {
	var res struct {
		Current struct {
			PermissionMode  string `json:"permissionMode"`
			InteractionMode string `json:"interactionMode"`
			Effort          string `json:"effort"`
			Model           string `json:"model"`
		} `json:"current"`
		Models []struct {
			ID        string            `json:"id"`
			Label     string            `json:"label"`
			IsDefault bool              `json:"isDefault"`
			Efforts   []json.RawMessage `json:"efforts"`
		} `json:"models"`
		Rewind *struct {
			Supported bool `json:"supported"`
		} `json:"rewind"`
	}
	if err := c.Call(ctx, CallTimeout, "agentSession.options", map[string]any{"sessionId": sessionID}, &res); err != nil {
		return nil, err
	}
	o := &Options{PermissionMode: res.Current.PermissionMode, InteractionMode: res.Current.InteractionMode,
		Effort: res.Current.Effort, Model: res.Current.Model, Rewind: res.Rewind != nil && res.Rewind.Supported}
	for _, m := range res.Models {
		if m.ID == "" { // Orca's stand-in for a model it was not told
			continue
		}
		o.Models = append(o.Models, m.ID)
		if m.Label != "" {
			if o.ModelNames == nil {
				o.ModelNames = map[string]string{}
			}
			o.ModelNames[m.ID] = m.Label
		}
		if m.IsDefault {
			o.DefaultModel = m.ID
		}
		if efforts := effortsOf(m.Efforts); len(efforts) > 0 {
			if o.Efforts == nil {
				o.Efforts = map[string][]string{}
			}
			o.Efforts[m.ID] = efforts
		}
	}
	return o, nil
}

// SendSession is agentSession.send: text and images (files on this machine) as the user's next message; queued
// behind the turn when one runs. Orca answers once it took the message, before the agent does.
func (c *Client) SendSession(ctx context.Context, w Write, text string, images []string) error {
	var blocks []any
	if t := strings.TrimRight(text, " \t\r\n"); t != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": t})
	}
	for _, p := range images {
		blocks = append(blocks, map[string]any{"type": "image-ref", "path": p})
	}
	if len(blocks) == 0 {
		return fmt.Errorf("orca agentSession.send: nothing to send")
	}
	fields := map[string]any{"body": map[string]any{"kind": "message", "role": "user", "blocks": blocks},
		"delivery": "queue-if-active"}
	return c.mutate(ctx, "agentSession.send", "agentSession.send", w, fields, nil)
}

// CancelSession is agentSession.cancel with nothing named: stop whatever the session is doing.
func (c *Client) CancelSession(ctx context.Context, w Write) error {
	return c.mutate(ctx, "agentSession.cancel", "agentSession.cancel", w, map[string]any{}, nil)
}

// Approve answers an approval with one of its options.
func (c *Client) Approve(ctx context.Context, w Write, p Prompt, optionID string) error {
	fields := map[string]any{"itemId": p.ItemID, "expectedRevision": p.Revision, "optionId": optionID}
	return c.mutate(ctx, "agentSession.respondToApproval", "agentSession.respondTo:approval", w, fields, nil)
}

// Answer is one question's answer: the options picked and any text written instead.
type Answer struct {
	QuestionID string   `json:"questionId"`
	OptionIDs  []string `json:"optionIds"`
	Other      string   `json:"other,omitempty"`
}

// AnswerQuestion answers a question prompt: answers for one with questions, else optionID (the older single one).
func (c *Client) AnswerQuestion(ctx context.Context, w Write, p Prompt, answers []Answer, optionID string) error {
	fields := map[string]any{"itemId": p.ItemID, "expectedRevision": p.Revision}
	if len(answers) > 0 {
		fields["answers"] = answers
	} else {
		fields["optionId"] = optionID
	}
	return c.mutate(ctx, "agentSession.respondToQuestion", "agentSession.respondTo:question", w, fields, nil)
}

// SetOption sets one of a session's options (permissionMode, effort…), Orca's words.
func (c *Client) SetOption(ctx context.Context, w Write, key, value string) error {
	return c.mutate(ctx, "agentSession.setOption", "agentSession.setOption", w,
		map[string]any{"key": key, "value": value}, nil)
}

// CloseSession closes a session as Orca's close button does (its tab goes; its history stays).
func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	return c.Call(ctx, WriteTimeout, "agentSession.close", map[string]any{"sessionId": sessionID}, nil)
}
