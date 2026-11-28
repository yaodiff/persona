package transcript

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// Codex rollouts (~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl) are a different JSONL: every line is
// {timestamp, type, payload}. The conversation lives in event_msg/item_completed (UserMessage, AgentMessage,
// Reasoning, CommandExecution, FileChange, McpToolCall, ImageView, Extension, SubAgentActivity, CollabAgentToolCall,
// ContextCompaction). A confirmed update_plan call can also arrive in the response_item wire records. Items map
// onto the same tools as Claude's transcript (Bash, Edit, Read, TodoWrite, mcp__server__tool).

type codexLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type       string          `json:"type"`
	TurnID     string          `json:"turn_id"`
	DurationMS json.RawMessage `json:"duration_ms"`
	// task_complete: what failed the turn, null when it ended well ({message, codex_error_info}, CodexTurnError)
	Error json.RawMessage `json:"error"`
	// session_meta
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	CLIVersion string `json:"cli_version"`
	Originator string `json:"originator"`
	// turn_context
	Model          string `json:"model"`
	ApprovalPolicy string `json:"approval_policy"`
	SandboxPolicy  struct {
		Type string `json:"type"`
	} `json:"sandbox_policy"`
	Effort string `json:"effort"`
	// event_msg
	Item       *codexItem          `json:"item"`
	Goal       json.RawMessage     `json:"goal"` // thread_goal_updated (goal.go)
	Reason     string              `json:"reason"`
	Info       *tokenuse.CodexInfo `json:"info"`        // token_count
	RateLimits *codexRateLimits    `json:"rate_limits"` // token_count: the account's limits as the last response gave them
	// thread_settings_applied: the settings the thread runs its next turn with, written when a turn starts and when they
	// change during the session (the TUI's /model, thread/settings/update)
	ThreadSettings *CodexThreadSettings `json:"thread_settings"`
}

// CodexThreadSettings is the part of a rollout's thread_settings_applied that says what the thread runs with (Codex
// 0.169): its model, service tier, and reasoning effort, null when the model takes none (node/codex_terminal_modes.go
// confirms a typed /model by it).
type CodexThreadSettings struct {
	Model           string          `json:"model"`
	ServiceTier     json.RawMessage `json:"service_tier"`
	ReasoningEffort *string         `json:"reasoning_effort"`
}

// codexRateLimits is a token_count's rate_limits (node/usagewindows.go reads the same for the usage windows).
type codexRateLimits struct {
	LimitID   *string           `json:"limit_id"` // "codex", or a model's own limit ("premium")
	Primary   *codexLimitWindow `json:"primary"`
	Secondary *codexLimitWindow `json:"secondary"`
}

type codexLimitWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	ResetsAt    *int64   `json:"resets_at"` // epoch seconds
}

// fullUntil is when the full windows (used to 100%) reset, the latest of them: the limit lifts when all have; zero when
// none is full. ok is false for a model's own limit, which says nothing of the account's.
func (rl *codexRateLimits) fullUntil() (until time.Time, ok bool) {
	if rl.LimitID != nil && *rl.LimitID != "" && *rl.LimitID != "codex" {
		return time.Time{}, false
	}
	for _, w := range [...]*codexLimitWindow{rl.Primary, rl.Secondary} {
		if w != nil && w.UsedPercent != nil && *w.UsedPercent >= 100 && w.ResetsAt != nil && *w.ResetsAt > 0 {
			if t := time.Unix(*w.ResetsAt, 0).UTC(); t.After(until) {
				until = t
			}
		}
	}
	return until, true
}

type codexItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Path string `json:"path"`
	} `json:"content"`
	Phase       string   `json:"phase"`
	SummaryText []string `json:"summary_text"`
	// CommandExecution
	Command          []string `json:"command"`
	Status           string   `json:"status"`
	AggregatedOutput string   `json:"aggregated_output"`
	Stdout           string   `json:"stdout"`
	Stderr           string   `json:"stderr"`
	ExitCode         *int     `json:"exit_code"`
	// Duration: how long the command ran (its outcome's duration_ms).
	Duration codexDuration `json:"duration"`
	// FileChange
	Changes map[string]struct {
		Type        string `json:"type"`
		UnifiedDiff string `json:"unified_diff"`
		Content     string `json:"content"`
	} `json:"changes"`
	// McpToolCall
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			// An image block (MCP content: base64 data and its type), a connector's or computer use's picture.
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error     json.RawMessage `json:"error"`
	Path      string          `json:"path"`      // ImageView
	SavedPath string          `json:"savedPath"` // Extension (image_gen.generation)
	// Extension (kind web.search): what was searched or opened, and what came back
	Kind   string `json:"kind"`
	Query  string `json:"query"`
	Action *struct {
		Type    string   `json:"type"`
		Queries []string `json:"queries"`
		URL     string   `json:"url"`
	} `json:"action"`
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Domain  string `json:"domain"`
		Snippet string `json:"snippet"`
	} `json:"results"`
	// Extension (kind clock.sleep): how long the agent waited
	DurationMs *int64 `json:"durationMs"`
	// SubAgentActivity (kind started, interacted or completed): the subagent, and on start its model and effort
	AgentThreadID   string  `json:"agent_thread_id"`
	AgentPath       string  `json:"agent_path"`
	Model           *string `json:"model"`
	ReasoningEffort *string `json:"reasoning_effort"`
	// CollabAgentToolCall (tool wait…): the subagents it was about, and their states when it returned
	ReceiverThreadIDs []string        `json:"receiver_thread_ids"`
	ReceiverAgents    json.RawMessage `json:"receiver_agents"`
	AgentsStates      json.RawMessage `json:"agents_states"`
}

type codexChatItem struct {
	codexItem
}

func (it *codexChatItem) UnmarshalJSON(data []byte) error {
	var typed codexItem
	err := json.Unmarshal(data, &typed)
	if err == nil {
		it.codexItem = typed
		return nil
	}
	var mismatch *json.UnmarshalTypeError
	if !errors.As(err, &mismatch) || mismatch.Value != "string" || mismatch.Field != "result" {
		return err
	}
	var extension struct {
		codexItem
		Result json.RawMessage `json:"result"`
	}
	if retryErr := json.Unmarshal(data, &extension); retryErr != nil {
		return retryErr
	}
	if extension.Type != "Extension" || extension.Kind != "image_gen.generation" || len(extension.Result) == 0 || extension.Result[0] != '"' {
		return err
	}
	it.codexItem = extension.codexItem
	return nil
}

type codexChatPayload struct {
	codexPayload
	Item *codexChatItem `json:"item"`
}

// isCodexLine tells a rollout line from a Claude transcript line: only rollouts wrap everything in "payload" under a
// top-level rollout type. The substring test rejects almost every Claude line cheaply; the scan then checks that the
// type and payload are the line's own, not those of a Claude tool input shaped like a rollout line, which used to
// switch the rest of the session to the Codex parser.
func isCodexLine(line []byte) bool {
	if !bytes.Contains(line, []byte(`"payload"`)) ||
		!(bytes.Contains(line, []byte(`"type":"session_meta"`)) || bytes.Contains(line, []byte(`"type":"event_msg"`)) ||
			bytes.Contains(line, []byte(`"type":"response_item"`)) || bytes.Contains(line, []byte(`"type":"turn_context"`)) ||
			bytes.Contains(line, []byte(`"type":"history_mutation"`))) {
		return false
	}
	typ, payload := rolloutHead(line)
	if !payload {
		return false
	}
	switch string(typ) {
	case "session_meta", "event_msg", "response_item", "turn_context", "history_mutation":
		return true
	}
	return false
}

// rolloutHead scans a JSON object's top-level keys without decoding it: the value of "type" (as written, when it is a
// string) and whether there is a "payload". Nested objects, arrays and strings are stepped over byte by byte.
func rolloutHead(line []byte) (typ []byte, payload bool) {
	depth, key := 0, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; c {
		case '"':
			end := jsonStringEnd(line, i)
			if end < 0 {
				return typ, payload
			}
			if depth != 1 || !key {
				i = end
				continue
			}
			key = false
			name := line[i+1 : end]
			j := skipJSONSpace(line, end+1)
			if j >= len(line) || line[j] != ':' {
				return typ, payload
			}
			j = skipJSONSpace(line, j+1)
			switch string(name) {
			case "payload":
				payload = true
			case "type":
				if j < len(line) && line[j] == '"' {
					if e := jsonStringEnd(line, j); e >= 0 {
						typ, j = line[j+1:e], e+1
					}
				}
			}
			if typ != nil && payload {
				return typ, payload
			}
			i = j - 1 // the value is read from j on
		case '{', '[':
			depth++
			key = c == '{' && depth == 1
		case '}', ']':
			depth--
		case ',':
			key = depth == 1
		}
	}
	return typ, payload
}

// jsonStringEnd is the index of the quote that ends the JSON string starting at line[i], or -1.
func jsonStringEnd(line []byte, i int) int {
	for j := i + 1; j < len(line); j++ {
		switch line[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return -1
}

func skipJSONSpace(line []byte, i int) int {
	for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\n' || line[i] == '\r') {
		i++
	}
	return i
}

// historyMutation is the newer rollout format (a compatible CLI, a Codex fork): the conversation's messages arrive as
// display_completions, each holding an item shaped like event_msg/item_completed's. The rest of the line (the model
// context, up to a megabyte after a compaction) is not needed.
type historyMutation struct {
	TurnID             string `json:"turn_id"`
	DisplayCompletions []struct {
		Item *codexChatItem `json:"item"`
	} `json:"display_completions"`
}

func parseCodexLine(seq int, line []byte, meta *Meta) []proto.Item {
	var l codexLine
	if json.Unmarshal(line, &l) != nil {
		return nil
	}
	if l.Type == "history_mutation" {
		var h historyMutation
		if json.Unmarshal(l.Payload, &h) != nil {
			return nil
		}
		var out []proto.Item
		for _, c := range h.DisplayCompletions {
			if c.Item != nil {
				out = append(out, codexItemLines(seq, l.Timestamp, h.TurnID, &c.Item.codexItem, meta)...)
			}
		}
		meta.codexWorked(out)
		return meta.codexModel(out)
	}
	if l.Type == "response_item" {
		return meta.codexPlanResponse(seq, l)
	}
	var p codexChatPayload
	if json.Unmarshal(l.Payload, &p) != nil {
		return nil
	}
	switch l.Type {
	case "session_meta":
		meta.Cwd, meta.Version, meta.Surface = p.Cwd, p.CLIVersion, CodexSurface(p.Originator)
		if meta.StartedAt.IsZero() {
			meta.StartedAt = l.Timestamp
		}
		return nil
	case "turn_context":
		if p.Model != "" {
			meta.observeCodexSettings(CodexThreadSettings{Model: p.Model}, l.Timestamp)
		}
		meta.codexTurnModel = p.Model
		meta.PermissionMode, meta.CodexSandbox, meta.ModeAt = p.ApprovalPolicy, p.SandboxPolicy.Type, l.Timestamp
		if p.Effort != "" {
			meta.Effort, meta.EffortAt = p.Effort, l.Timestamp
		}
		return nil
	case "event_msg":
	default:
		return nil
	}
	mk := func(idx int, id, role, kind string) proto.Item {
		if id == "" {
			id = fmt.Sprintf("line%d", seq)
		}
		return proto.Item{ID: fmt.Sprintf("%s#%d", id, idx), Seq: seq, TS: l.Timestamp, Role: role, Kind: kind}
	}
	switch p.Type {
	case "plan_update":
		return meta.codexPlanEvent(seq, l.Timestamp, l.Payload)
	case "thread_settings_applied":
		// What the thread runs its next turn with, as soon as it changes (a /model typed mid-turn, #12572), not only at
		// the next turn_context; the turn already running keeps its own (codexTurnModel). No effort leaves the last one,
		// as a turn_context without one does.
		if p.ThreadSettings == nil {
			return nil
		}
		previous := meta.Model
		meta.observeCodexSettings(*p.ThreadSettings, l.Timestamp)
		if previous != "" && p.ThreadSettings.Model != "" && previous != meta.Model {
			it := mk(0, "", "system", "model_change")
			it.Text, it.Model = previous, meta.Model
			return []proto.Item{it}
		}
		return nil
	case "token_count":
		if p.Info != nil {
			meta.ContextTokens, meta.OutputTokens = int(p.Info.Last.Input), int(p.Info.Total.Output)
			if p.Info.ModelContextWindow > 0 {
				meta.ContextWindow = p.Info.ModelContextWindow
			}
			meta.codexRequest(p.Info.Total.Total, p.Info.Last)
		}
		if p.RateLimits != nil { // none: the provider gave none this time, the last ones stand
			if until, ok := p.RateLimits.fullUntil(); ok {
				meta.codexLimitFull = until
			}
		}
		return nil
	case "task_started":
		meta.TurnOpen, meta.TurnClosed = true, false
		meta.CodexClosedTurnID = ""
		meta.codexTurnID, meta.codexUserID, meta.codexTurnModel = p.TurnID, "", ""
		meta.codexUsage = proto.TokenUsage{}
		meta.clearTurnError()
		meta.turnWorked, meta.turnErrorCounted = false, false
		meta.turnParts = nil
		meta.codexSeen = nil // an item repeats only within its turn: a long rollout keeps no ids of the turns before
		return nil
	case "task_complete":
		meta.endTurn(seq, l.Timestamp)
		meta.CodexClosedTurnID = p.TurnID
		// A turn the model service failed writes no answer: its error is here (Codex 0.166, #2185).
		meta.clearTurnError()
		if meta.TurnError, meta.TurnErrorKind = CodexTurnError(p.Error); meta.TurnError != "" {
			meta.TurnErrorAfterWork = meta.turnWorked
			// A usage limit lifts when the windows the rollout last saw full reset; one that reset before this turn
			// ended is an old sample, not why it failed.
			if meta.TurnErrorKind == proto.TurnErrorUsageLimit && meta.codexLimitFull.After(l.Timestamp) {
				meta.TurnErrorResetsAt = meta.codexLimitFull
			}
			meta.noteUpstreamTurnError(meta.TurnError, meta.TurnErrorKind, p.TurnID)
		} else if meta.turnWorked {
			meta.resetUpstreamStreak()
		}
		meta.turnWorked, meta.turnErrorCounted = false, false
		usage := meta.codexTurnUsage()
		ms, ok := recordedMilliseconds(p.DurationMS)
		if !ok || strings.TrimSpace(p.TurnID) == "" || (meta.codexTurnID != "" && meta.codexTurnID != p.TurnID) {
			return nil
		}
		it := mk(0, "", "system", "turn_duration")
		it.TurnDuration = &proto.TurnDuration{DurationMS: ms, Source: "codex_task_complete", TurnID: p.TurnID, UserItemID: meta.codexUserID}
		it.Usage = usage // the turn's requests, summed (item_usage.go)
		return []proto.Item{it}
	case "turn_aborted":
		meta.endTurn(seq, l.Timestamp)
		meta.CodexClosedTurnID = p.TurnID
		meta.clearTurnError() // stopped by the user: no failure
		meta.turnWorked = false
		it := mk(0, "", "system", proto.KindNotice)
		it.Text = InterruptedNotice
		it.Usage = meta.codexTurnUsage() // what the turn used before it was stopped
		return []proto.Item{it}
	case "thread_goal_updated":
		return meta.codexGoalUpdated(p.Goal, l.Timestamp, func() proto.Item { return mk(0, "", "system", proto.KindNotice) })
	case "context_compacted", "compacted":
		meta.ContextTokens, meta.ContextCompacted = 0, true
		meta.ContextStats.ResetCompacted()
		return nil
	case "item_completed":
	default:
		return nil
	}
	if p.Item == nil {
		return nil
	}
	out := codexItemLines(seq, l.Timestamp, p.TurnID, &p.Item.codexItem, meta)
	meta.codexWorked(out)
	return meta.codexModel(out)
}

// codexModel marks the agent's items with the model of the turn they came in (Item.Model): none before the turn's
// turn_context, as a window starting at the turn would have none either.
func (m *Meta) codexModel(items []proto.Item) []proto.Item {
	if m.codexTurnModel == "" {
		return items
	}
	for i := range items {
		if items[i].Role == "assistant" {
			items[i].Model = m.codexTurnModel
		}
	}
	return items
}

// codexWorked notes the turn did some work when the items are the agent's (a message, reasoning, a tool call):
// TurnErrorAfterWork of a turn that then fails.
func (m *Meta) codexWorked(items []proto.Item) {
	for _, it := range items {
		if it.Role == "assistant" {
			m.turnWorked = true
			m.resetUpstreamStreak()
			return
		}
	}
}

// codexItemLines maps one finished item (UserMessage, AgentMessage, CommandExecution…) to chat items, once per id.
func codexItemLines(seq int, ts time.Time, turnID string, it *codexItem, meta *Meta) (items []proto.Item) {
	// A command can finish after its turn, with no new prompt or reply. Keep
	// the chat items, but do not move the time used for unseen and triage.
	// Older rollouts can omit the next start marker; a different turn id is
	// evidence that its command belongs to a new exchange instead.
	lateCommand := it.Type == "CommandExecution" && meta.TurnClosed &&
		(turnID == "" || meta.CodexClosedTurnID == "" || turnID == meta.CodexClosedTurnID)
	if !lateCommand {
		defer func() {
			for _, item := range items {
				meta.observeActivity(item)
			}
		}()
	}
	if it.Type == "CommandExecution" { // its call and result can be on two lines (backgrounded, then its end)
		return codexCommandLines(seq, ts, it, meta)
	}
	if it.ID != "" {
		if meta.codexSeen[it.ID] {
			return nil
		}
		if meta.codexSeen == nil {
			meta.codexSeen = map[string]bool{}
		}
		meta.codexSeen[it.ID] = true
	}
	mk := func(idx int, id, role, kind string) proto.Item {
		if id == "" {
			id = fmt.Sprintf("line%d", seq)
		}
		return proto.Item{ID: fmt.Sprintf("%s#%d", id, idx), Seq: seq, TS: ts, Role: role, Kind: kind}
	}
	switch it.Type {
	case "UserMessage":
		var parts, images []string
		for _, c := range it.Content {
			switch {
			case c.Type == "text" && strings.TrimSpace(c.Text) != "":
				parts = append(parts, c.Text)
			case c.Type == "local_image" && c.Path != "":
				images = append(images, strings.TrimPrefix(c.Path, "file://"))
			}
		}
		// Match the envelope before removing other kinds of input metadata or clipping long prompts.
		if len(parts) == 1 && len(images) == 0 {
			if prompt, ok := codexHeartbeatPrompt(strings.TrimSpace(parts[0])); ok {
				meta.observeUserText(prompt, false)
				u := mk(0, it.ID, "user", proto.KindText)
				u.Source = &proto.MessageSource{Kind: proto.SourceScheduledTask}
				u.Text = clip(prompt)
				if meta.codexUserID == "" {
					meta.codexUserID = u.ID
				}
				return []proto.Item{u}
			}
		}
		var quoted []proto.QuestionReply
		visibleParts := 0
		for i := range parts {
			text, replies := codexUserPart(parts[i])
			parts[i] = text
			if text != "" {
				visibleParts++
				quoted = replies
			}
		}
		text := strings.TrimSpace(strings.Join(parts, "\n"))
		items, paseo := paseoInput(func(n int, role, kind string) proto.Item { return mk(n, it.ID, role, kind) }, 0, text)
		if paseo && (len(images) == 0 || attachImages(items, images)) {
			meta.observeUserText(text, false)
			if len(images) > 0 {
				meta.ContextStats.ConversationBytes += int64(len(images)) * imageTokenBytesEstimate
			}
			if meta.codexUserID == "" && len(items) > 0 { // it started the turn: its notices woke the agent
				markWake(items)
				meta.codexUserID = items[0].ID
			}
			if meta.FirstUser == "" && len(items) > 0 && items[0].Kind == proto.KindText {
				meta.FirstUser = items[0].Text
			}
			return items
		}
		fullText := strings.TrimSpace(text + imagesNote(images))
		if fullText == "" {
			return nil
		}
		meta.observeUserText(text, false)
		if len(images) > 0 {
			meta.ContextStats.ConversationBytes += int64(len(images)) * imageTokenBytesEstimate
		}
		u := mk(0, it.ID, "user", proto.KindText)
		clipUserText(&u, fullText)
		if visibleParts == 1 {
			u.QuestionReplies = quoted
		}
		if meta.codexUserID == "" {
			meta.codexUserID = u.ID
		}
		if meta.FirstUser == "" {
			meta.FirstUser = u.Text
		}
		return []proto.Item{u}
	case "AgentMessage":
		var b strings.Builder
		for _, c := range it.Content {
			b.WriteString(c.Text)
		}
		text := strings.TrimSpace(b.String())
		quiet := false
		if it.Phase == "final_answer" {
			text, quiet = codexHeartbeatAnswer(text)
		}
		text, vizRefs := splitVisualizations(codexThreadLinks(text))
		if text == "" && len(vizRefs) == 0 {
			return nil
		}
		// AgentMessage is what Codex said, including narration between tool calls; only Reasoning is thinking.
		if t := WithoutBotmuxSentinel(text); t != "" {
			meta.LastText = t
			meta.turnSaid(t)
		}
		var out []proto.Item
		if text != "" {
			meta.ContextStats.ConversationBytes += estimateWeightedBytes(text)
			a := mk(0, it.ID, "assistant", proto.KindText)
			a.Narration = it.Phase == "commentary"
			a.CodexHeartbeatQuiet = quiet
			a.Text = clip(text)
			out = append(out, a)
		}
		for _, ref := range vizRefs {
			v := mk(len(out), it.ID, "assistant", proto.KindVisualization)
			v.Narration = it.Phase == "commentary"
			v.Visualization = &proto.Visualization{Path: ref.Path, Mode: ref.Mode, Title: ref.Title}
			out = append(out, v)
		}
		return out
	case "Reasoning":
		text := strings.TrimSpace(strings.Join(it.SummaryText, "\n\n"))
		if text == "" {
			return nil
		}
		meta.ContextStats.ConversationBytes += estimateWeightedBytes(text)
		t := mk(0, it.ID, "assistant", proto.KindThinking)
		t.Text = clip(text)
		return []proto.Item{t}
	case "FileChange":
		// One Edit per file, the patch as a unified diff (the apps draw "diff" when there is no old/new string).
		// In path order: map order would give the same file a different id on every parse.
		paths := make([]string, 0, len(it.Changes))
		for path := range it.Changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		var out []proto.Item
		n := 0
		for _, path := range paths {
			ch := it.Changes[path]
			diff := ch.UnifiedDiff
			if diff == "" {
				diff = ch.Content
			}
			input, _ := json.Marshal(map[string]string{"file_path": path, "diff": diff, "change": ch.Type})
			resText := ch.Type + " " + path
			meta.observeToolContext("Edit", input, resText)
			id := fmt.Sprintf("%s-%d", it.ID, n)
			use := mk(2*n, id, "assistant", proto.KindToolUse)
			use.ToolUseID, use.ToolName, use.ToolInput = id, "Edit", clipJSON(input) // a new file's whole content
			res := mk(2*n+1, id, "user", proto.KindToolResult)
			res.ToolUseID, res.Text, res.IsError = id, resText, it.Status == "failed"
			res.Outcome = &proto.ToolOutcome{Status: codexStatus(it.Status, res.IsError)}
			out = append(out, use, res)
			n++
		}
		return out
	case "McpToolCall":
		args := it.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		var parts []string
		var images []*proto.Image
		isErr := it.Status == "failed" || len(it.Error) > 0 && string(it.Error) != "null"
		if it.Result != nil {
			isErr = isErr || it.Result.IsError
			for _, c := range it.Result.Content {
				switch {
				case c.Type == "text":
					parts = append(parts, c.Text)
				case c.Type == "image" && c.Data != "" && len(c.Data) <= maxImageLen && strings.HasPrefix(c.MimeType, "image/"):
					images = append(images, &proto.Image{Mime: c.MimeType, Data: c.Data})
				}
			}
		}
		if len(parts) == 0 && isErr && len(it.Error) > 0 {
			parts = append(parts, string(it.Error))
		}
		items := codexTool(meta, mk, it.ID, "mcp__"+it.Server+"__"+it.Tool, args, strings.Join(parts, "\n"), isErr)
		items[1].Outcome.Status = codexStatus(it.Status, isErr)
		if len(images) > 0 {
			meta.ContextStats.ToolOutputBytes += int64(len(images)) * imageTokenBytesEstimate
		}
		for j, img := range images { // after the result, on the call's card, as a Claude tool result's pictures are
			im := mk(2+j, it.ID, "user", proto.KindImage)
			im.ToolUseID, im.Image = it.ID, img
			items = append(items, im)
		}
		return items
	case "ImageView":
		return codexImageRead(meta, mk, it.ID, it.Path)
	case "Extension":
		switch it.Kind {
		case "image_gen.generation":
			if it.Status == "completed" && it.SavedPath != "" {
				return codexImageRead(meta, mk, it.ID, it.SavedPath)
			}
		case "web.search":
			return codexWebSearch(meta, mk, it)
		case "clock.sleep":
			return codexSleep(meta, mk, it)
		}
		n := mk(0, it.ID, "system", proto.KindNotice)
		n.Text = "Codex 扩展事件：" + it.Kind + "（完整记录在原会话中）"
		return []proto.Item{n}
	case "SubAgentActivity":
		return codexSubagentActivity(meta, mk, it)
	case "CollabAgentToolCall":
		return codexCollabCall(meta, mk, it)
	case "ContextCompaction":
		meta.Compactions++
		meta.ContextTokens, meta.ContextCompacted = 0, true
		meta.ContextStats.ResetCompacted()
		n := mk(0, it.ID, "system", proto.KindNotice)
		n.Text = CompactedNotice
		return []proto.Item{n}
	}
	if it.Type == "" {
		return nil
	}
	n := mk(0, it.ID, "system", proto.KindNotice)
	n.Text = "Codex 事件：" + it.Type + "（完整记录在原会话中）"
	return []proto.Item{n}
}

func codexImageRead(meta *Meta, mk func(int, string, string, string) proto.Item, id, path string) []proto.Item {
	input, _ := json.Marshal(map[string]string{"file_path": strings.TrimPrefix(path, "file://")})
	return codexTool(meta, mk, id, "Read", input, "", false)
}

// codexPendingKept bounds the backgrounded command ids a Meta keeps: a rollout that starts commands and never records
// their end must not grow the parser's state with every one.
const codexPendingKept = 1024

// codexSeenAdd marks an item id as read in the turn under way.
func (m *Meta) codexSeenAdd(id string) {
	if m.codexSeen == nil {
		m.codexSeen = map[string]bool{}
	}
	m.codexSeen[id] = true
}

// codexPendingAdd remembers a backgrounded command until its terminal record. codexPendingIDs holds exactly the pending
// ids, oldest first, so the cap evicts the oldest genuinely pending command.
func (m *Meta) codexPendingAdd(id string) {
	if m.codexPending == nil {
		m.codexPending = map[string]bool{}
	}
	if m.codexPending[id] {
		return
	}
	m.codexPending[id] = true
	m.codexPendingIDs = append(m.codexPendingIDs, id)
	for len(m.codexPending) > codexPendingKept && len(m.codexPendingIDs) > 0 {
		oldest := m.codexPendingIDs[0]
		m.codexPendingIDs = m.codexPendingIDs[1:]
		delete(m.codexPending, oldest)
	}
}

// codexPendingDone says whether id is a backgrounded command waiting for this terminal record, and stops waiting. Its
// queue entry goes too: a later command that reuses the id starts a new entry and is not tied to this one.
func (m *Meta) codexPendingDone(id string) bool {
	if !m.codexPending[id] {
		return false
	}
	delete(m.codexPending, id)
	for i, pending := range m.codexPendingIDs {
		if pending == id {
			m.codexPendingIDs = append(m.codexPendingIDs[:i], m.codexPendingIDs[i+1:]...)
			break
		}
	}
	return true
}

// codexCommandLines maps a CommandExecution to its call and result. A backgrounded command is still running: its call
// is shown at once, without a result, and the terminal record with the same id (completed, failed or declined) gives
// the result later, in this turn or a later one. A record with no backgrounded call before it is a synchronous call
// and still gives both items on its line.
func codexCommandLines(seq int, ts time.Time, it *codexItem, meta *Meta) []proto.Item {
	mk := func(idx int, id, role, kind string) proto.Item {
		if id == "" {
			id = fmt.Sprintf("line%d", seq)
		}
		return proto.Item{ID: fmt.Sprintf("%s#%d", id, idx), Seq: seq, TS: ts, Role: role, Kind: kind}
	}
	input, _ := json.Marshal(map[string]string{"command": shellCommand(it.Command)})
	if it.Status == "backgrounded" {
		if it.ID != "" {
			// A genuinely pending command suppresses its repeated background record, even after task_started (the
			// record can be written again in a later turn); a terminal record of this turn suppresses a late repeat.
			if meta.codexPending[it.ID] || meta.codexSeen[it.ID] {
				return nil
			}
			meta.codexPendingAdd(it.ID)
		}
		meta.observeToolContext("Bash", input, "")
		use := mk(0, it.ID, "assistant", proto.KindToolUse)
		use.ToolUseID, use.ToolName, use.ToolInput = it.ID, "Bash", clipJSON(input)
		return []proto.Item{use}
	}
	out := it.AggregatedOutput
	if out == "" {
		out = strings.TrimSpace(it.Stdout + "\n" + it.Stderr)
	}
	failed := it.Status == "failed" || (it.ExitCode != nil && *it.ExitCode != 0)
	if it.ExitCode != nil && *it.ExitCode != 0 { // older apps read the exit code from the text: it stays there too
		out = strings.TrimSpace(out + fmt.Sprintf("\n(exit code %d)", *it.ExitCode))
	}
	if it.ID != "" && meta.codexPendingDone(it.ID) {
		// The terminal record of a backgrounded call: only its result, under the call already shown.
		meta.ContextStats.ToolOutputBytes += estimateWeightedBytes(out)
		res := mk(1, it.ID, "user", proto.KindToolResult)
		res.ToolUseID, res.Text, res.IsError = it.ID, clip(out), failed
		res.Outcome = &proto.ToolOutcome{Status: codexStatus(it.Status, failed), ExitCode: it.ExitCode, DurationMS: it.Duration.ms, Tail: outputTail(out)}
		meta.codexSeenAdd(it.ID) // a repeat of this terminal record is dropped
		return []proto.Item{res}
	}
	if it.ID != "" {
		if meta.codexSeen[it.ID] { // a finished command recorded again in this turn
			return nil
		}
		meta.codexSeenAdd(it.ID)
	}
	items := codexTool(meta, mk, it.ID, "Bash", input, out, failed)
	o := items[1].Outcome
	o.Status, o.ExitCode, o.DurationMS = codexStatus(it.Status, failed), it.ExitCode, it.Duration.ms
	return items
}

// LocalMachine is the id the hub gave this node's machine (set by the node): links to its sessions name it.
var LocalMachine atomic.Value

// The Codex app's directive for a thread it just created (its sidebar shows a card for it); in Persona a link that
// opens that thread's chat (persona://session/<machine>/<session>, the in-chat link the apps navigate by).
var codexCreatedThreadRe = lazyre.New(`::created-thread\{threadId="([0-9a-fA-F-]+)"\}`)

func codexThreadLinks(s string) string {
	machine, _ := LocalMachine.Load().(string)
	if machine == "" {
		return s
	}
	return codexCreatedThreadRe.ReplaceAllStringFunc(s, func(m string) string {
		id := codexCreatedThreadRe.FindStringSubmatch(m)[1]
		return "[打开新建的对话](persona://session/" + url.PathEscape(machine) + "/" + url.PathEscape(id) + ")"
	})
}

var (
	// Ambient state the Codex app attaches to a message (open browser tabs and such), not something the user said.
	codexAmbientRe = lazyre.New(`(?s)<in-app-browser-context[^>]*>.*?</in-app-browser-context>`)
)

// imagesNote is the note a user message's pictures become after its words ("" for none): their local paths, which the
// apps show as thumbnails (the phone's own photos travel the same way).
func imagesNote(images []string) string {
	if len(images) == 0 {
		return ""
	}
	return "\n\n<attached_images>\n" + strings.Join(images, "\n") + "\n</attached_images>"
}

// attachImages puts a message's pictures (imagesNote) after the words of the last user text among items: false when
// none can carry them.
func attachImages(items []proto.Item, images []string) bool {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role == "user" && items[i].Kind == proto.KindText {
			items[i].Text = strings.TrimSpace(items[i].Text + imagesNote(images))
			return true
		}
	}
	return false
}

// codexUserPart keeps the legacy plain answer for all clients. Only a standalone reply
// envelope supplies a quoted question: prose quoting the same XML is not a reply.
func codexUserPart(s string) (string, []proto.QuestionReply) {
	s = StripInterject(s)
	s = codexAmbientRe.ReplaceAllString(s, "")
	s = dropTagged(ViewErrorsRe, "<persona-view-errors", s)
	s = strings.TrimSpace(legacyLarkAgent(strings.TrimSpace(ContextRe.ReplaceAllString(s, ""))))
	var quotes []proto.QuestionReply
	text := CodexReplyRe.ReplaceAllStringFunc(s, func(block string) string {
		replies := codexReplies(CodexReplyRe.FindStringSubmatch(block)[1])
		if len(replies) == 0 {
			return block
		}
		if len(replies) == 1 {
			q := strings.TrimSpace(replies[0].Question)
			if block == s && q != "" {
				quotes = []proto.QuestionReply{{Question: q}}
			}
			return replies[0].Answer
		}
		lines := make([]string, 0, len(replies))
		for _, r := range replies {
			q := strings.TrimSpace(r.Question)
			lines = append(lines, q+"："+r.Answer)
			if block == s {
				quotes = append(quotes, proto.QuestionReply{Question: q, Answer: r.Answer})
			}
		}
		return strings.Join(lines, "\n")
	})
	return strings.TrimSpace(text), quotes
}

// codexWebSearch shows a web search (or a page it opened) the way Claude's WebSearch / WebFetch calls look: the query or
// URL as input, the results as one "title  url" line each.
func codexWebSearch(meta *Meta, mk func(int, string, string, string) proto.Item, it *codexItem) []proto.Item {
	name, input := "WebSearch", map[string]string{"query": it.Query}
	if a := it.Action; a != nil {
		switch {
		case a.Type == "openPage" && a.URL != "":
			name, input = "WebFetch", map[string]string{"url": a.URL}
		case it.Query == "" && len(a.Queries) > 0:
			input["query"] = strings.Join(a.Queries, "\n")
		}
	}
	var lines []string
	for _, r := range it.Results {
		where := r.URL
		if where == "" {
			where = r.Domain
		}
		line := strings.TrimSpace(strings.TrimSpace(r.Title) + "  " + where)
		if line != "" {
			lines = append(lines, line)
		}
	}
	raw, _ := json.Marshal(input)
	return codexTool(meta, mk, it.ID, name, raw, strings.Join(lines, "\n"), false)
}

// codexSleep shows the agent waiting (Codex's clock.sleep, a coordinator's pause while its subagents work) as a call of
// its own: work that folds with the calls around it, and a run of sleeps folds into one row.
func codexSleep(meta *Meta, mk func(int, string, string, string) proto.Item, it *codexItem) []proto.Item {
	input := map[string]any{}
	if ms := it.DurationMs; ms != nil && *ms >= 0 {
		input["duration"], input["duration_ms"] = sleepDuration(*ms), *ms
	}
	raw, _ := json.Marshal(input)
	return codexTool(meta, mk, it.ID, "sleep", raw, "", false)
}

// sleepDuration is a wait as people read it: 25s, 1m30s, 2m.
func sleepDuration(ms int64) string {
	s := (time.Duration(ms) * time.Millisecond).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// codexSubagentActivity shows a subagent started, messaged or finished (SubAgentActivity) as a call named by the
// subagent's task; started also carries the model and effort Codex gave it.
func codexSubagentActivity(meta *Meta, mk func(int, string, string, string) proto.Item, it *codexItem) []proto.Item {
	name := it.AgentThreadID
	if p := strings.TrimRight(it.AgentPath, "/"); p != "" {
		name = p[strings.LastIndex(p, "/")+1:]
	}
	raw, _ := json.Marshal(struct {
		Name            string `json:"name,omitempty"`
		Event           string `json:"event,omitempty"`
		AgentPath       string `json:"agent_path,omitempty"`
		AgentThreadID   string `json:"agent_thread_id,omitempty"`
		Model           string `json:"model,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
	}{name, it.Kind, it.AgentPath, it.AgentThreadID, derefString(it.Model), derefString(it.ReasoningEffort)})
	return codexTool(meta, mk, it.ID, "subagent", raw, "", false)
}

// codexCollabCall shows a call that coordinates subagents (CollabAgentToolCall): wait is Codex's wait_agent, the
// subagents it names are its input and their states when it returned its result.
func codexCollabCall(meta *Meta, mk func(int, string, string, string) proto.Item, it *codexItem) []proto.Item {
	name := it.Tool
	switch name {
	case "":
		name = "collab_agent"
	case "wait":
		name = "wait_agent"
	}
	input := map[string]any{}
	if len(it.ReceiverThreadIDs) > 0 {
		input["receiver_thread_ids"] = it.ReceiverThreadIDs
	}
	if jsonNonEmpty(it.ReceiverAgents) {
		input["receiver_agents"] = it.ReceiverAgents
	}
	raw, _ := json.Marshal(input)
	result := ""
	if jsonNonEmpty(it.AgentsStates) {
		result = string(it.AgentsStates)
	}
	failed := it.Status == "failed"
	items := codexTool(meta, mk, it.ID, name, raw, result, failed)
	items[1].Outcome.Status = codexStatus(it.Status, failed)
	return items
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// jsonNonEmpty is false for a missing, null, empty array or empty object value.
func jsonNonEmpty(raw json.RawMessage) bool {
	switch string(bytes.TrimSpace(raw)) {
	case "", "null", "[]", "{}":
		return false
	}
	return true
}

// codexTool is a finished tool call: Codex writes the call and its result in one line. Its input is clipped as a
// Claude tool_use's is (a heredoc command, an MCP call's long argument would go out whole in every page and push). Its
// outcome says ok or failed and has the output's tail; callers that know more (a status, an exit code) add it.
func codexTool(meta *Meta, mk func(int, string, string, string) proto.Item, id, name string, input json.RawMessage, result string, isErr bool) []proto.Item {
	meta.observeToolContext(name, input, result)
	use := mk(0, id, "assistant", proto.KindToolUse)
	use.ToolUseID, use.ToolName, use.ToolInput = id, name, clipJSON(input)
	res := mk(1, id, "user", proto.KindToolResult)
	res.ToolUseID, res.Text, res.IsError = id, clip(result), isErr
	res.Outcome = &proto.ToolOutcome{Status: codexStatus("", isErr), Tail: outputTail(result)}
	return []proto.Item{use, res}
}

// codexStatus is an outcome's status from an item's: declined is a call the user did not approve.
func codexStatus(status string, failed bool) string {
	switch {
	case status == "declined":
		return proto.OutcomeDenied
	case failed:
		return proto.OutcomeFailed
	}
	return proto.OutcomeOK
}

// codexDuration is how long a command ran, as rollouts write it (Rust's Duration: {"secs":11,"nanos":121827375}), in
// milliseconds (nil when not recorded). Any other shape leaves it unread rather than failing the line.
type codexDuration struct{ ms *int64 }

func (d *codexDuration) UnmarshalJSON(b []byte) error {
	d.ms = nil
	var v struct {
		Secs  *int64 `json:"secs"`
		Nanos int64  `json:"nanos"`
	}
	if json.Unmarshal(b, &v) != nil || v.Secs == nil || *v.Secs < 0 || *v.Secs > 1<<40 || v.Nanos < 0 {
		return nil
	}
	ms := *v.Secs*1000 + v.Nanos/1_000_000
	d.ms = &ms
	return nil
}

// shellCommand drops the login-shell wrapper Codex runs everything through: ["/bin/zsh", "-lc", "ls"] → "ls".
func shellCommand(argv []string) string {
	if len(argv) == 3 && (argv[1] == "-lc" || argv[1] == "-c") {
		return argv[2]
	}
	return strings.Join(argv, " ")
}
