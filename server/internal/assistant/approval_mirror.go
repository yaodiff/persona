package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// defaultApprovalMirrorTools limits the two new routes to operations that can
// change something. Read-only runtime tools are deliberately absent. A node
// command gate contributes only its explicit WriteOperation classification;
// extension writes are admitted through ToolAccess below, so installing a
// connector does not require editing this list. After the read-only exclusion,
// an explicit approval_mirror_tools replaces the complete write list and both
// fallbacks.
var defaultApprovalMirrorTools = []string{
	"Write", "Edit", "MultiEdit", "NotebookEdit",
	"agent_archive", "agent_autoname", "agent_interrupt", "agent_modes", "agent_send", "agent_start", "agent_supervise",
	"cancel_followup", "conversation_new", "conversation_send", "direction_name", "kb_update", "mail_send", "notify_user",
	"persona_settings", "repository_add", "schedule_followup", "schedule_routine", "skill_import", "supervision_finish",
	"task_plan", "task_review", "task_scope", "todo_complete", "todo_create", "todo_delete", "todo_reopen", "todo_update",
}

// These reads stay out even if a malformed card claims write metadata or an
// explicit custom allowlist names one. The allowlist selects among writes; it
// is not a way to route read-only confirmations.
var defaultApprovalMirrorReadTools = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "WebFetch": true, "WebSearch": true,
	"inbox_suggest": true, "kb_get": true, "kb_list": true, "kb_search": true,
	"lark_chats": true, "lark_messages": true, "lark_search": true,
	"larkcli_chats": true, "larkcli_messages": true, "larkcli_search": true,
}

// Built-in read families that are not connector declarations or Inbox-policy entries.
var approvalMirrorAssistantReadTools = map[string]bool{
	"a2a_get": true, "a2a_read": true, "a2a_sessions": true,
	"mail_accounts": true, "mail_read": true, "mail_search": true,
	"resource_get": true, "resource_status": true, "todo_decisions": true,
}

func approvalMirrorKnownReadTool(name string) bool {
	if defaultApprovalMirrorReadTools[name] || approvalMirrorAssistantReadTools[name] {
		return true
	}
	// The Inbox policy deliberately permits a few internal writes (kb_update and task_scope).
	// Approval routing's explicit write catalog wins for those; every other Inbox/phone read is
	// still a read when a custom allowlist or forged write metadata names it.
	if slices.Contains(defaultApprovalMirrorTools, name) {
		return false
	}
	return inboxReadOnlyTools[name] || inboxReadOnlyPhoneTools[name]
}

func approvalMirrorToolName(name string) string {
	name = compat.CanonicalMCPToolName(strings.TrimSpace(name))
	rest, ok := strings.CutPrefix(name, "mcp__")
	i := strings.LastIndex(rest, "__")
	if !ok || i <= 0 || !compat.IsMCPServer(strings.TrimPrefix(rest[:i], "plugin_")) || rest[i+2:] == "" {
		return name
	}
	return rest[i+2:]
}

func cleanApprovalMirrorTools(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		name = approvalMirrorToolName(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func approvalMirrorToolNames(v Settings) []string {
	if v.ApprovalMirrorTools != nil {
		return cleanApprovalMirrorTools(*v.ApprovalMirrorTools)
	}
	return slices.Clone(defaultApprovalMirrorTools)
}

func (s *Service) approvalMirrorToolAllowed(ctx context.Context, ix proto.Interaction) bool {
	v := s.settings(ctx)
	name := approvalMirrorToolName(ix.ToolName)
	if approvalMirrorKnownReadTool(name) {
		return false
	}
	// A command gate's explicit classification wins over any configured name:
	// an unknown or read-only command is not made a write by calling its card a
	// write tool. Bypass-mode writes carry no Always keys and use the same bit.
	if name == "Bash" || slices.ContainsFunc(ix.AlwaysKeys, func(key string) bool { return strings.HasPrefix(key, "lark:") }) {
		if !ix.WriteOperation {
			return false
		}
		if v.ApprovalMirrorTools != nil {
			return name != "" && slices.Contains(cleanApprovalMirrorTools(*v.ApprovalMirrorTools), name)
		}
		return true
	}
	extensionKnown, extensionReadOnly := false, false
	if name != "" && s.ext != nil {
		extensionKnown, extensionReadOnly = s.ext.ToolAccess(name)
		if extensionKnown && extensionReadOnly {
			return false
		}
	}
	if v.ApprovalMirrorTools != nil {
		return name != "" && slices.Contains(cleanApprovalMirrorTools(*v.ApprovalMirrorTools), name)
	}
	if name != "" && slices.Contains(defaultApprovalMirrorTools, name) {
		return true
	}
	if ix.WriteOperation {
		return true
	}
	return extensionKnown && !extensionReadOnly
}

// approvalMirrorMain controls display copies for new cards. Existing copies keep
// following their authoritative card until that card resolves.
func (s *Service) approvalMirrorMain(ctx context.Context) bool {
	return quietOn(s.settings(ctx).ApprovalMirrorMain)
}

// approvalMirrorID is stable across updates and reconnects of one occurrence,
// and distinct when a runtime later reuses the original ID for another card.
func approvalMirrorID(ix proto.Interaction) string {
	return proto.ApprovalMirrorID(ix.ID, ix.CreatedAt)
}

// assistantApprovalMirrorEligible is the boundary that persisted display intent
// may never bypass: even an existing copy belongs only to an eligible Assistant
// permission, never to an Agent, a space the main conversation may not read,
// private, nested or server-owned exact proposal card. Exact replies must target
// the authoritative owner-action book.
func (s *Service) assistantApprovalMirrorEligible(ctx context.Context, ix proto.Interaction) bool {
	return ix.Kind == proto.IxPermission && !ix.Private && !ix.OwnerProposal && ix.SessionID != "" && ix.MirrorOf == "" &&
		!s.ixPolicy(ctx, ix).DenyMainRead && s.ixInAssistant(ctx, ix)
}

// nonMainAssistantPermission is the narrow routing boundary shared by the main
// conversation copy and the bot's direct chat. Only owner-decision write tools
// opt in. Agent sessions, private cards, spaces the main conversation may not
// read, questions and display copies themselves never opt in. Existing main
// copies bypass this new-card filter until their authoritative operation ends.
func (s *Service) nonMainAssistantPermission(ctx context.Context, ix proto.Interaction) bool {
	if !s.assistantApprovalMirrorEligible(ctx, ix) || !s.approvalMirrorToolAllowed(ctx, ix) {
		return false
	}
	v := s.settings(ctx)
	if v.Main == "" || ix.SessionID != v.Main {
		return true
	}
	mainMachine := s.confirmedMainMachine(ctx, v)
	return mainMachine != "" && ix.MachineID != mainMachine
}

// approvalMirrorHub reports whether this hub understands display-copy identity,
// validation, normalization and synchronized withdrawal. An older hub receives
// only the authoritative card and keeps its existing behavior.
func (s *Service) approvalMirrorHub(ctx context.Context) bool {
	hello, err := s.hub.Hello(ctx)
	return err == nil && hello.ApprovalMirrors
}

// mainApprovalMirror returns the display-only copy for ix. Only the hub keeps
// this copy; the manager's pending map, decision boundary and aggregate counts
// continue to contain just the authoritative interaction. MirrorMain preserves
// an already-created copy after the preference changes or either side restarts.
func (s *Service) mainApprovalMirror(ctx context.Context, ix proto.Interaction) (proto.Interaction, bool) {
	if !s.assistantApprovalMirrorEligible(ctx, ix) || !s.approvalMirrorHub(ctx) {
		return proto.Interaction{}, false
	}
	if !ix.MirrorMain && (!s.approvalMirrorMain(ctx) || !s.nonMainAssistantPermission(ctx, ix)) {
		return proto.Interaction{}, false
	}
	v := s.settings(ctx)
	mainMachine := s.confirmedMainMachine(ctx, v)
	if v.Main == "" || mainMachine == "" {
		return proto.Interaction{}, false
	}
	mirror := ix
	mirror.ID = approvalMirrorID(ix)
	mirror.MirrorOf = ix.ID
	mirror.MirrorMain = false
	mirror.MachineID = mainMachine
	mirror.SessionID = v.Main
	mirror.FoldedUnder, mirror.FoldedAs, mirror.FoldedSide = "", "", ""
	mirror.Muted = false
	return mirror, true
}

// sameApprovalOccurrence distinguishes a resumed card from a later operation
// whose runtime happened to reuse its ID. Ordinary cards use their creation
// timestamp. A detachable original replay may refresh it, so exact call bytes
// are its occurrence identity instead.
func sameApprovalOccurrence(a, b proto.Interaction) bool {
	if a.ID != b.ID || a.MachineID != b.MachineID || a.SessionID != b.SessionID {
		return false
	}
	if a.Detachable || b.Detachable {
		if !a.Detachable || !b.Detachable {
			return false
		}
		af, aerr := detachedFingerprint(a)
		bf, berr := detachedFingerprint(b)
		return aerr == nil && berr == nil && af == bf
	}
	return !a.CreatedAt.IsZero() && a.CreatedAt.Equal(b.CreatedAt)
}

// sameDurableApprovalOccurrence is the strict-finalization counterpart. Zero
// timestamps are equal for legacy tombstones; detachable calls retain exact
// fingerprint identity even when a replay refreshed its display timestamp.
func sameDurableApprovalOccurrence(a, b proto.Interaction) bool {
	if a.ID != b.ID || a.MachineID != b.MachineID || a.SessionID != b.SessionID {
		return false
	}
	if a.Detachable || b.Detachable {
		if !a.Detachable || !b.Detachable {
			return false
		}
		af, aerr := detachedFingerprint(a)
		bf, berr := detachedFingerprint(b)
		return aerr == nil && berr == nil && af == bf
	}
	return a.CreatedAt.Equal(b.CreatedAt)
}

// recordedApprovalMirrorIntent restores both true and false for the same
// pending occurrence, together with its canonical creation time for the display
// ID. A non-resumable node-listed dialog may be relisted after startup cleanup
// cancelled its row; only the identical timestamp with no owner answer restores
// that intent. Only a genuinely new card falls back to the current setting.
func (s *Service) recordedApprovalMirrorIntent(ctx context.Context, ix proto.Interaction) (intent bool, at time.Time, found bool) {
	if ix.CreatedAt.IsZero() {
		return false, time.Time{}, false
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	if p != nil {
		same := sameApprovalOccurrence(ix, p.ix)
		intent, at := p.ix.MirrorMain, p.ix.CreatedAt
		s.fleet.mu.Unlock()
		if same {
			return intent, at, true
		}
		return false, time.Time{}, false
	}
	s.fleet.mu.Unlock()
	st, ok := s.store().(interactionRecorder)
	if !ok {
		return false, time.Time{}, false
	}
	readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	rec, err := st.InteractionRecord(readCtx, ix.ID)
	if err != nil || rec == nil || !sameApprovalOccurrence(ix, rec.Interaction) {
		return false, time.Time{}, false
	}
	startupCancelled := !ix.Resumable && rec.Status == "cancelled" && rec.Resolution == nil
	if rec.Status != "pending" && !startupCancelled {
		return false, time.Time{}, false
	}
	return rec.Interaction.MirrorMain, rec.Interaction.CreatedAt, true
}

// pendingDisplayInteractions is the reconnect snapshot: authoritative pending
// operations plus their display copies, adjacent and in the same order.
func (s *Service) pendingDisplayInteractions(ctx context.Context) []proto.Interaction {
	pending := s.pendingInteractions()
	out := make([]proto.Interaction, 0, len(pending)*2)
	for _, ix := range pending {
		out = append(out, ix)
		if !ix.MirrorMain {
			continue // an existing unmirrored occurrence must not gain a copy here
		}
		if mirror, ok := s.mainApprovalMirror(ctx, ix); ok {
			out = append(out, mirror)
		}
	}
	return out
}
