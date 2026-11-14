// Package sessionsearch projects runtime row metadata for the transport search
// index. It reads no files, makes no model calls and never fetches a transcript.
package sessionsearch

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionlist"
	"github.com/nwverse/persona/server/internal/textfmt"
)

var modelPattern = regexp.MustCompile(`(?i)^claude-([a-z]+)((?:-[0-9]{1,2})*)(?:-[0-9]{8})?(\[1m\])?$`)
var launcherPrefix = regexp.MustCompile(`(?i)^(claude|codex|opencode|gemini|pi)[-_:/\s]+`)

func first(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
func base(value string) string {
	if value == "" {
		return ""
	}
	if (len(value) > 2 && value[1] == ':' && (value[2] == '/' || value[2] == '\\')) || strings.HasPrefix(value, `\\`) {
		value = strings.ReplaceAll(value, `\`, "/")
	}
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" {
		return value
	}
	return path.Base(trimmed)
}
func shortModel(value string) string {
	value = strings.TrimSpace(value)
	m := modelPattern.FindStringSubmatch(value)
	if m == nil {
		return value
	}
	version := strings.ReplaceAll(strings.TrimPrefix(m[2], "-"), "-", ".")
	parts := []string{strings.ToLower(m[1])}
	if version != "" {
		parts = append(parts, version)
	}
	if m[3] != "" {
		parts = append(parts, "1M")
	}
	return strings.Join(parts, " ")
}

var aliases = map[string]string{"claude-code": "claude", "claudecode": "claude", "codex-cli": "codex", "gemini-cli": "gemini", "cursor-agent": "cursor", "pi-web": "pi", "trae-cli": "trae", "traecli": "trae"}
var names = map[string]string{"claude": "Claude Code", "codex": "Codex", "opencode": "OpenCode", "pi": "pi", "gemini": "Gemini CLI", "cursor": "Cursor", "trae": "Trae"}

// Document uses complete immutable rows, not the 180-character list summaries.
func Document(item sessionlist.Item) (sessionlist.SearchDocument, error) {
	var row proto.Session
	var data []byte
	switch v := item.Full.(type) {
	case json.RawMessage:
		data = v
	case []byte:
		data = v
	default:
		var err error
		data, err = json.Marshal(v)
		if err != nil {
			return sessionlist.SearchDocument{}, err
		}
	}
	if err := json.Unmarshal(data, &row); err != nil {
		return sessionlist.SearchDocument{}, err
	}
	d := sessionlist.SearchDocument{Title: first(row.Title, base(row.Cwd)), Project: first(base(row.RepoRoot), row.SpaceName, base(row.WorkspaceCwd), base(row.Cwd)), Cwd: row.Cwd, Assistant: row.Assistant}
	d.State = row.State
	if d.State == "" {
		switch {
		case row.Status == proto.StatusOffline:
			d.State = "offline"
		case row.Status == proto.StatusWorking:
			d.State = "working"
		case row.Status == proto.StatusBlocked || row.PendingInteractions > 0:
			d.State = "needs_you"
		default:
			d.State = "idle"
		}
	}
	agent := strings.TrimSpace(strings.ToLower(row.Agent))
	if row.SourceKind == "codex_app_server" || row.SourceKind == "codex_history" {
		agent = "codex"
	}
	labelAgent := first(agent, row.Launcher)
	labelID := strings.ToLower(labelAgent)
	if normalized, ok := aliases[labelID]; ok {
		labelID = normalized
	}
	if labelID == "" {
		labelID = "claude"
	}
	if agent == "" && row.Launcher != "" {
		low := strings.ToLower(row.Launcher)
		for _, known := range []string{"claude", "codex", "opencode", "pi", "gemini", "cursor", "trae"} {
			if strings.Contains(low, known) {
				agent = known
				break
			}
		}
	}
	if normalized, ok := aliases[agent]; ok {
		agent = normalized
	}
	if agent == "" {
		agent = "claude"
	}
	d.Harness = agent
	d.HarnessLabel = first(names[labelID], row.AgentLabel, row.AgentName, labelAgent)
	model := row.Model
	if strings.TrimSpace(model) == "" {
		model = launcherPrefix.ReplaceAllString(row.Launcher, "")
	}
	d.ActiveAt = row.UpdatedAt
	if d.ActiveAt.IsZero() {
		d.ActiveAt = row.StateSince
	}
	d.Model = strings.Join([]string{shortModel(model), row.Model, row.Launcher}, " ")
	d.Place = strings.Join([]string{row.SpaceName, row.SlotName, base(row.Cwd)}, " ")
	user := []string{row.Asks}
	if row.Goal != nil {
		user = append(user, row.Goal.Objective)
	}
	if row.Todo != nil {
		user = append(user, row.Todo.Title)
	}
	d.UserText = strings.Join(user, " · ")
	failure := ""
	if d.State != "working" {
		failure = strings.TrimLeft(row.FailureReason, "⚠️ \n\t")
	}
	errors := []string{row.StartError, row.TurnError, failure, row.AttentionReason}
	work := ""
	if row.Work != nil {
		if row.State != "working" && row.Work.Phase != "working" {
			work = row.Work.Line
			if work == "" && row.Work.Closeout != nil {
				switch row.Work.Closeout.Status {
				case "blocked":
					work = row.Work.Closeout.Waiting
				case "not_done":
					work = row.Work.Closeout.Missing
				}
			}
		}
		if row.Work.Phase == "blocked" {
			errors = append(errors, row.Work.Line)
			d.HasErrors = true
		}
	}
	d.ErrorText = strings.Join(errors, " · ")
	d.HasErrors = d.HasErrors || row.StartError != "" || row.TurnError != "" || failure != "" || row.AttentionReason != "" || d.State == "needs_you" || row.PendingInteractions > 0
	d.AgentText = textfmt.SearchMarkdown(strings.Join([]string{row.LastText, work}, "\n"))
	d.Metadata = strings.Join([]string{row.Cwd, row.RepoRoot, row.Branch, row.SpaceName, row.SlotName, d.HarnessLabel, shortModel(model), row.Activity}, " · ")
	return d, nil
}
