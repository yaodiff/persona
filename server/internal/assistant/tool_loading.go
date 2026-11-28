package assistant

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Which Persona tools an assistant conversation loads up front (#7317). Claude Code sends a tool personad mcp marks
// alwaysLoad in full with every request; any other it lists by name only, and the model loads it with ToolSearch when
// it needs it (the assistant's --tools has ToolSearch, node/assistant.go). Every Persona tool used to be marked, about
// 22K tokens a request for tools most conversations never call; now only coreTools and a leader's
// leaderCoreTools are, unless assistant.load_all_tools is on. Changing it wakes every running personad mcp's tool watch (mcp_tools_changes.go), so a
// running conversation is sent its new list at once; one whose personad mcp is older than tool refresh lists its tools
// only when its process starts, and keeps them until it restarts.

// coreTools are the tools assistant conversations call often: those called at least 5 times in 30 days of one
// install's assistant conversations, less the ones whose every call failed for want of setup (mail_accounts with no
// mail account, ask_escalate with no leader). The Lark tools are listed under both of their names: the Lark extension's
// and the lark-cli connector's (connector/larkcli). Any other tool, a new one too, is deferred until it is added here.
var coreTools = []string{
	"agent_archive", "agent_interrupt", "agent_read", "agent_send", "agent_start", "agent_supervise", "agents_list",
	"ask_user", "cancel_followup", "conversation_read", "conversation_send", "conversations_list", "interaction_answer",
	"kb_get", "kb_list", "kb_search", "kb_update", "lark_chats", "lark_image", "lark_messages", "lark_search", "larkcli_chats",
	"larkcli_messages", "larkcli_search", "list_followups", "machines_list", "model_profiles_list", "notify_user",
	"persona_settings", "schedule_followup", "schedule_routine", "supervision_finish", "task_plan", "task_review",
	"task_scope", "todo_get", "todo_list", "todo_update",
}

// leaderCoreTools supplement the shared list for current direction supervisors. Loading
// these definitions does not change each tool's caller authorization. agent_transfer joins
// them because a lead hands off its own open tasks with it (#13192): behind lazy loading,
// leads only discover that path after an extra ToolSearch round.
var leaderCoreTools = []string{"agent_transfer", "leader_progress", "todo_create", "ask_escalate", "direction_name"}

// The operations taught by persona-work. Match built-in names exactly: an
// extension sharing a prefix must keep its own loading policy.
var workGuideTools = []string{
	"machines_list", "agents_list", "agent_read", "agent_send", "agent_start", "agent_interrupt", "agent_modes",
	"agent_switch", "agents_switch", "agent_archive", "agent_unarchive", "agent_autoname", "agent_autoname_receipt",
	"agent_supervise", "agent_transfer", "task_plan", "task_plan_withdraw", "task_scope", "task_retry",
	"task_start_screen", "task_start_keys", "task_review", "task_decided", "leader_progress", "direction_name",
	"todo_list", "todo_get", "todo_create", "todo_update", "todo_complete", "todo_reopen", "todo_delete", "concern_report",
	"conversations_list", "conversations_switch", "conversation_read", "conversation_rename", "conversation_tags_set", "conversation_new",
	"conversation_send", "conversation_archive", "conversation_unarchive", "repositories_list", "repository_get",
	"repository_add", "todo_repositories_get", "todo_repositories_set", "todo_repository_links_add", "todo_repository_links_set",
}

// withToolLoading is a conversation's tools with DeferLoading set by the manager alone, whatever the list came with (a
// connector program's tools are decoded as it sent them): when deferring (an assistant conversation that has ToolSearch)
// all but coreTools (plus leaderCoreTools for leaders) are deferred, unless assistant.load_all_tools is on; otherwise (a guest, an agent) none is. A copy:
// the list it is given may be shared.
func (s *Service) withToolLoading(ctx context.Context, self string, tools []proto.Tool, deferring bool) []proto.Tool {
	settings := s.settings(ctx)
	guide := deferring && settings.workGuideOn() && toolSetOf(ctx) == ""
	cleanRound := guide && s.cleanRoundSession(self)
	deferring = deferring && !(settings.LoadAllTools != nil && *settings.LoadAllTools)
	leader := deferring && s.isLeader(ctx, self)
	pageRoutine := deferring && s.isPageRoutineSession(ctx, self)
	out := slices.Clone(tools)
	for i := range out {
		// Keep planning and its allowed preflight reads directly available in a granted round.
		// Discovery would add a separate tool call to the grant's transcript check.
		out[i].WorkGuideTool = guide && slices.Contains(workGuideTools, out[i].Name) &&
			!(cleanRound && (out[i].Name == "task_plan" || grantSafeTools[out[i].Name]))
		out[i].DeferLoading = deferring && !slices.Contains(coreTools, out[i].Name) &&
			!(leader && slices.Contains(leaderCoreTools, out[i].Name)) &&
			!(pageRoutine && out[i].Name == pageToolName)
	}
	return out
}

// loadAllTools: every Persona tool loads up front (assistant.load_all_tools, off by default).
func (s *Service) loadAllTools(ctx context.Context) bool {
	v := s.settings(ctx).LoadAllTools
	return v != nil && *v
}

func (s *Service) setLoadAllTools(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	change, err := s.setBoolSetting(ctx, v, s.loadAllTools, "assistant.load_all_tools is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_load_all_tools", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.LoadAllTools },
		"assistant.load_all_tools is %v: running conversations are sent their new tool list now, and new ones start with it "+
			"(a conversation on a computer whose Persona is older than tool refresh keeps its tools until its process restarts).")
	if err != nil || change.apply == nil {
		return change, err
	}
	apply := change.apply
	change.apply = func(ctx context.Context) (string, error) {
		out, err := apply(ctx)
		if err == nil {
			// Saved first, so every watch this wakes lists its tools with the new setting.
			s.mcpChanges.notify()
		}
		return out, err
	}
	return change, nil
}
