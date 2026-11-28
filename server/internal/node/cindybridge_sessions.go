package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// cindyModes maps Cindy's permission_mode values to Persona's (Claude Code's).
var cindyToPersonaMode = map[string]string{
	"ask":               "default",
	"default":           "default",
	"acceptEdits":       "acceptEdits",
	"plan":              "plan",
	"auto":              "auto",
	"bypassPermissions": proto.ModeBypass,
}

var personaToCindyMode = map[string]string{
	"default":        "ask",
	"acceptEdits":    "acceptEdits",
	"plan":           "plan",
	"auto":           "auto",
	proto.ModeBypass: "bypassPermissions",
}

func cindyAgentName(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "codex":
		return "codex"
	case "pi":
		return "pi"
	default:
		return "claude"
	}
}

func cindyPersonaSessionID(r cindyDBRow) (sid, transcript string) {
	agent := cindyAgentName(r.AgentKind)
	sdk := strings.TrimSpace(r.SDKSessionID)
	if agent == "claude" && validID(sdk) {
		return sdk, sdk
	}
	id := strings.TrimSpace(r.ID)
	if id == "" {
		return "", ""
	}
	if strings.HasPrefix(id, "cindy-") && runtimeIDRe.MatchString(id) {
		return id, ""
	}
	cand := "cindy-" + id
	if runtimeIDRe.MatchString(cand) {
		return cand, ""
	}
	return "", ""
}

func parseCindyTime(raw json.RawMessage, fallbackMs int64) (time.Time, int64) {
	if len(raw) > 0 {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.UTC(), t.UnixMilli()
			}
		}
		var ms int64
		if json.Unmarshal(raw, &ms) == nil && ms > 0 {
			return time.UnixMilli(ms).UTC(), ms
		}
	}
	if fallbackMs > 0 {
		return time.UnixMilli(fallbackMs).UTC(), fallbackMs
	}
	now := time.Now().UTC()
	return now, now.UnixMilli()
}

func mapCindySessionRow(r cindyDBRow, isRunning, isBlocked bool, aliases map[string]string) (map[string]any, string) {
	sid, transcript := cindyPersonaSessionID(r)
	if sid == "" {
		return nil, ""
	}
	agent := cindyAgentName(r.AgentKind)
	cwd := firstOf(strings.TrimSpace(r.WorktreePath), strings.TrimSpace(r.WorkingDir), strings.TrimSpace(r.ProjectDir))
	spaceDir := firstOf(strings.TrimSpace(r.ProjectDir), strings.TrimSpace(r.WorkingDir), strings.TrimSpace(r.WorktreePath))
	title := strings.TrimSpace(r.Title)
	if title == "" || title == "New Maker" {
		if cwd != "" {
			title = filepath.Base(cwd)
		} else {
			title = "Cindy"
		}
	}
	status := proto.StatusIdle
	switch {
	case isBlocked:
		status = proto.StatusBlocked
	case isRunning:
		status = proto.StatusWorking
	case r.LastActivityAt > 0 || r.UserSendAt > 0 || strings.TrimSpace(r.ListPreview) != "":
		status = proto.StatusDone
	}
	updated, rev := parseCindyTime(r.UpdatedAt, max(r.LastActivityAt, r.UserSendAt))
	row := map[string]any{
		"session_id": sid,
		"title":      title,
		"cwd":        cwd,
		"status":     status,
		"updated_at": updated,
		"rev":        rev,
		"agent":      agent,
	}
	if transcript != "" {
		row["transcript"] = transcript
	}
	if r.WorkspaceKind != "dialogue" && spaceDir != "" {
		row["space_id"] = spaceDir
		row["space_name"] = firstOf(aliases[spaceDir], filepath.Base(spaceDir))
	}
	if wt, proj := strings.TrimSpace(r.WorktreePath), strings.TrimSpace(r.ProjectDir); wt != "" && proj != "" && wt != proj {
		row["repo_root"] = proj
	}
	if m := strings.TrimSpace(r.Model); m != "" {
		row["model"] = m
	}
	if e := strings.TrimSpace(r.Effort); e != "" && validModes("", e) == nil {
		row["effort"] = e
	}
	mode := cindyToPersonaMode[strings.TrimSpace(r.PermissionMode)]
	if r.PlanModeEnabled {
		mode = "plan"
	}
	if mode != "" {
		row["permission_mode"] = mode
	}
	if prev := strings.TrimSpace(r.ListPreview); prev != "" {
		row["last_text"] = bridgePreview(prev)
	}
	return row, sid
}

type cindyMessageRow struct {
	ID        string          `json:"id"`
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	ToolUseID string          `json:"toolUseId"`
	CreatedAt json.RawMessage `json:"createdAt"`
}

func (b *cindyBridge) sessionItems(ctx context.Context, cindyID string, limit int, beforeSeq *int) (any, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	var msgs []cindyMessageRow
	if b.writable() {
		var wrapped struct {
			Messages []cindyMessageRow `json:"messages"`
		}
		if err := b.postJSON(ctx, "/api/sessions/messages", map[string]any{"sessionId": cindyID, "limit": 500}, &wrapped); err == nil {
			msgs = wrapped.Messages
		} else {
			_ = b.postJSON(ctx, "/api/messages/list", map[string]any{"sessionId": cindyID, "limit": 500}, &msgs)
		}
	} else if b.ep != nil && b.ep.DBPath != "" {
		msgs, _ = readCindySQLiteMessages(ctx, b.ep.DBPath, cindyID)
	}
	items := make([]proto.Item, 0, len(msgs))
	for i, m := range msgs {
		ts, _ := parseCindyTime(m.CreatedAt, 0)
		seq := i + 1
		it := proto.Item{ID: m.ID, Seq: seq, TS: ts}
		text := unwrapCindyMessageContent(m.Content)
		switch m.Role {
		case "user":
			it.Role, it.Kind, it.Text = "user", proto.KindText, text
		case "assistant":
			it.Role, it.Kind, it.Text = "assistant", proto.KindText, text
		case "tool_use":
			it.Role, it.Kind, it.ToolName, it.ToolUseID, it.Text = "assistant", proto.KindToolUse, "Tool", m.ToolUseID, text
		case "tool_result":
			it.Role, it.Kind, it.ToolUseID, it.Text = "user", proto.KindToolResult, m.ToolUseID, text
		case "error":
			it.Role, it.Kind, it.Text, it.IsError = "assistant", proto.KindText, text, true
		default:
			continue
		}
		items = append(items, it)
	}
	toSeq := len(items)
	if beforeSeq != nil {
		var filtered []proto.Item
		for _, it := range items {
			if it.Seq < *beforeSeq {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	hasMore := false
	if len(items) > limit {
		items = items[len(items)-limit:]
		hasMore = true
	}
	return map[string]any{
		"items":    items,
		"to_seq":   toSeq,
		"has_more": hasMore,
		"history":  "cindy:" + cindyID,
	}, nil
}

func unwrapCindyMessageContent(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return raw
	}
	var obj struct {
		Text    string `json:"text"`
		Message string `json:"message"`
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(s), &obj) == nil {
		if v := firstOf(obj.Text, obj.Message, obj.Content); v != "" {
			return v
		}
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(s), &parts) == nil && len(parts) > 0 {
		var texts []string
		for _, p := range parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return raw
}

func readCindySQLiteSessions(ctx context.Context, dbPath string) ([]cindyDBRow, error) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	q := `SELECT id, title, COALESCE(working_dir, '') AS workingDir, COALESCE(project_dir, '') AS projectDir, ` +
		`COALESCE(workspace_kind, 'project') AS workspaceKind, COALESCE(worktree_path, '') AS worktreePath, ` +
		`COALESCE(model, '') AS model, COALESCE(effort, '') AS effort, COALESCE(permission_mode, '') AS permissionMode, ` +
		`COALESCE(status, 'active') AS status, COALESCE(sdk_session_id, '') AS sdkSessionId, ` +
		`COALESCE(agent_kind, 'cc') AS agentKind, COALESCE(provider_id, '') AS providerId, ` +
		`COALESCE(last_activity_at, 0) AS lastActivityAt, COALESCE(user_send_at, 0) AS userSendAt, ` +
		`COALESCE(updated_at, 0) AS updatedAt, COALESCE(list_preview, '') AS listPreview ` +
		`FROM sessions WHERE status = 'active' AND hidden_at IS NULL ` +
		`ORDER BY COALESCE(last_activity_at, user_send_at, updated_at) DESC LIMIT 300`
	out, err := exec.CommandContext(cctx, sqlite, "-readonly", "-bail", "-json", "-cmd", ".timeout 2000", dbPath, q).Output()
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var rows []cindyDBRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func readCindySQLiteMessages(ctx context.Context, dbPath, sessionID string) ([]cindyMessageRow, error) {
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil || strings.ContainsAny(sessionID, "'\"\x00") {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	q := fmt.Sprintf(`SELECT id, role, COALESCE(content, '') AS content, COALESCE(tool_use_id, '') AS toolUseId, `+
		`COALESCE(created_at, 0) AS createdAt FROM messages `+
		`WHERE session_id = '%s' AND rewind_at IS NULL ORDER BY created_at ASC, rowid ASC LIMIT 500`, sessionID)
	out, err := exec.CommandContext(cctx, sqlite, "-readonly", "-bail", "-json", "-cmd", ".timeout 2000", dbPath, q).Output()
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		return nil, err
	}
	var rows []cindyMessageRow
	_ = json.Unmarshal(out, &rows)
	return rows, nil
}
