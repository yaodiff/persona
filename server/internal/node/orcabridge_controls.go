package node

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// A chat's controls (#17046, docs/integrations/orca.md "Structured sessions"): its permission mode, plan mode
// (Orca's interactionMode), effort and model (agentSession.setOption), its name (agentSession.rename), a rewind
// (agentSession.rewind) and its read mark (agentSession.acknowledgeAttention). An Orca without the method a control
// needs (status.get's capabilities) refuses it, and says so in the user's words.

// orcaStatusMaxAge: the runtime's status (which controls its chats take) is read again at most this often.
const orcaStatusMaxAge = time.Minute

// orcaPermissionModes are the permission modes Orca's chats take, by provider, in Orca's words (Orca 1.4.277's own
// table); a provider not in it (pi, aug, one Orca adds later) takes none, as in Orca.
var orcaPermissionModes = map[string][]string{
	"claude": {"ask", "accept-edits", "auto", "full-access"}, "qwen-code": {"ask", "accept-edits", "auto", "full-access"},
	"devin": {"ask", "accept-edits", "auto", "full-access"}, "codebuddy": {"ask", "accept-edits", "auto", "full-access"},
	"codex": {"ask", "auto", "full-access"}, "kimi": {"ask", "auto", "full-access"}, "goose": {"ask", "auto", "full-access"},
	"droid": {"ask", "auto", "full-access"}, "antigravity": {"ask", "accept-edits", "full-access"},
	"hermes": {"ask", "accept-edits", "full-access"}, "grok": {"ask", "full-access"}, "opencode": {"ask", "full-access"},
	"opencode2": {"ask", "full-access"}, "omp": {"ask", "full-access"}, "copilot": {"ask", "full-access"},
	"cursor": {"ask", "full-access"}, "kiro": {"ask", "full-access"}, "kilo": {"ask", "full-access"},
	"mimo-code": {"ask", "full-access"}, "gemini": {"ask", "full-access"}, "cline": {"ask", "full-access"},
}

// orcaTakes: Orca's chats of provider agent take permission mode m (Orca's word).
func orcaTakes(agent, m string) bool {
	return m != "" && slices.Contains(orcaPermissionModes[agent], m)
}

// orcaTakesPlan: the chat takes plan mode, Orca's interaction mode: a Claude chat always, another provider's when its
// options report an interaction mode (Orca reports one only for a provider that takes it).
func orcaTakesPlan(agent string, o *orcart.Options) bool {
	return agent == "claude" || o != nil && o.InteractionMode != ""
}

// status is the runtime's status.get, read again at most every orcaStatusMaxAge; nil while it cannot be read.
func (b *orcaBridge) status(ctx context.Context) *orcart.Status {
	b.mu.Lock()
	st, at := b.st, b.stAt
	b.mu.Unlock()
	if st != nil && time.Since(at) < orcaStatusMaxAge {
		return st
	}
	got, err := b.c.Probe(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err == nil {
		b.st, b.stAt = got, time.Now()
	}
	return b.st
}

// orcaControls adds to a chat's row what its controls are now (runtimes.md "What to implement", modes): a Claude chat
// takes Claude Code's chips, full access included at any time (bypass_available); another provider's chat lists the
// permission modes Orca takes for it (capabilities.permission_modes: its approval as default, full access as
// bypassPermissions, plan where it takes Orca's interaction mode) and the efforts its model has; a chat Orca cannot
// rewind says so.
func orcaControls(row map[string]any, agent string, o *orcart.Options, st *orcart.Status) {
	caps := map[string]any{"rewind": st.Has(orcart.CapChatRewind) && o != nil && o.Rewind}
	if agent == "claude" {
		row["bypass_available"] = true
	} else {
		var modes []string
		for _, mode := range proto.PermissionModes {
			if mode == "plan" && orcaTakesPlan(agent, o) || orcaTakes(agent, orcaModes[mode]) {
				modes = append(modes, mode)
			}
		}
		if len(modes) > 0 {
			caps["permission_modes"] = modes
		}
		if o != nil {
			var efforts []string
			for _, e := range o.Efforts[o.Model] {
				if slices.Contains(proto.EffortLevels, e) { // the levels session.modes takes (validModes)
					efforts = append(efforts, e)
				}
			}
			if len(efforts) > 0 {
				row["efforts"] = efforts
			}
		}
	}
	row["capabilities"] = caps
}

// orcaRowMode is a row's permission mode in Persona's words: plan while Orca's interaction mode is, else Orca's
// permission mode; "" for one Persona has no word for.
func orcaRowMode(o *orcart.Options) string {
	if o == nil {
		return ""
	}
	if o.InteractionMode == "plan" {
		return "plan"
	}
	for mode, theirs := range orcaModes {
		if theirs == o.PermissionMode {
			return mode
		}
	}
	return ""
}

// setModes is session.modes on chat orca of provider agent: the model first (the effort chosen with it is the new
// model's), then the effort, then the permission mode. Plan is Orca's interaction mode; any other mode leaves plan
// first. A mode Orca does not take for the chat is refused before anything changes.
func (b *orcaBridge) setModes(ctx context.Context, orca, agent, mode, effort, model string) error {
	var o *orcart.Options
	b.mu.Lock()
	if h := b.heads[orca]; h != nil {
		o = h.opts
	}
	b.mu.Unlock()
	theirs := ""
	switch {
	case mode == "plan":
		if !orcaTakesPlan(agent, o) {
			return orcaChatErr("unsupported", "orca_mode_unsupported", nil, mode)
		}
	case mode != "":
		if theirs = orcaModes[mode]; !orcaTakes(agent, theirs) {
			return orcaChatErr("unsupported", "orca_mode_unsupported", nil, mode)
		}
	}
	set := func(key, value string) error { return b.c.SetOption(ctx, b.write(orca, ""), key, value) }
	var err error
	if model != "" {
		err = set("model", model)
	}
	if err == nil && effort != "" {
		err = set("effort", effort)
	}
	if err == nil && mode != "" {
		switch {
		case mode == "plan":
			err = set("interactionMode", "plan")
		case o != nil && o.InteractionMode == "plan":
			if err = set("interactionMode", "default"); err == nil {
				err = set("permissionMode", theirs)
			}
		default:
			err = set("permissionMode", theirs)
		}
	}
	b.reread(orca, false)
	return err
}

// reread has the next refresh read a chat's options again (a change from Persona shows at once), and its head too when
// head is set (a rewind moves a Claude chat to another Claude session). What was read stays meanwhile.
func (b *orcaBridge) reread(orca string, head bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h := b.heads[orca]; h != nil {
		c := *h
		c.optsAt, c.optsRev = time.Time{}, -1
		if head && !c.readAt.IsZero() {
			c.readAt, c.readRev = time.Now().Add(-orcaHeadRetry), -1
		}
		b.heads[orca] = &c
	}
}

// rename is session.rename: agentSession.rename, on an Orca that has it.
func (b *orcaBridge) rename(ctx context.Context, orca, title string) error {
	if !b.status(ctx).Has(orcart.CapChatRename) {
		return orcaChatErr("unsupported", "orca_rename_unsupported", nil, "")
	}
	if err := b.c.Rename(ctx, orca, title); err != nil {
		return err
	}
	b.mu.Lock()
	b.treesID, b.tabsAt = "", time.Time{} // the next refresh reads the new name
	b.mu.Unlock()
	return nil
}

// orcaRewindPages bounds how far back a rewind looks for the user's message: Orca's pages are 200 rows.
const orcaRewindPages = 10

// rewind is session.rewind: the user's message prompt is found in Orca's journal by its text, its occurrence counting
// from the end and how many of the user's messages came after it (when given), and the chat goes back to before it
// (agentSession.rewind, by its item and the journal's epoch). A message not where the app saw it, or a journal that
// moved on meanwhile, is refused (rewind_mismatch) with nothing changed. Orca puts back the conversation only.
func (b *orcaBridge) rewind(ctx context.Context, orca, prompt string, occurrence int, after *int, restore string) (any, error) {
	if !b.status(ctx).Has(orcart.CapChatRewind) {
		return nil, rewindError(rewindUnsupported, "This Orca cannot rewind chats.")
	}
	if restore != "" && restore != RestoreConversation {
		return nil, rewindError(rewindUnsupported, "Orca rewinds the conversation only.")
	}
	want := orcaSquash(prompt)
	type message struct{ id, text string }
	var users []message // the user's messages, newest first
	epoch := ""
	var cursor *orcart.HistoryCursor
	for range orcaRewindPages {
		dir := "tail"
		if cursor != nil {
			dir = "before"
		}
		page, err := b.c.Journal(ctx, orca, dir, cursor, orcaItemsMax)
		if err != nil {
			return nil, orcaWriteErr(err)
		}
		if epoch == "" {
			epoch = page.Epoch
		} else if page.Epoch != epoch {
			return nil, rewindError(rewindMismatch, "The conversation changed while it was read. Nothing was changed.")
		}
		items := page.Items
		slices.SortFunc(items, func(a, b orcart.JournalItem) int { return cmp.Compare(b.Sequence, a.Sequence) })
		for _, it := range items {
			if it.Body.Kind == "message" && it.Body.Role == "user" {
				users = append(users, message{it.ItemID, orcaSquash(orcaUserText(it.Body))})
			}
		}
		if !page.HasOlder || len(items) == 0 {
			break
		}
		cursor = &orcart.HistoryCursor{Epoch: epoch, Sequence: items[len(items)-1].Sequence}
	}
	seen := 0
	for i, m := range users {
		if m.text != want {
			continue
		}
		if seen++; seen < occurrence {
			continue
		}
		if after != nil && *after != i {
			break
		}
		err := b.c.Rewind(ctx, b.write(orca, ""), m.id, epoch)
		switch reason := orcart.RewindRefusal(err); {
		case err == nil:
			b.reread(orca, true)
			// The app puts the message back in its own input box: nothing of it is left in one the next message
			// from Persona goes through.
			return map[string]any{"prompt": prompt, "restore": RestoreConversation, "code_restored": false,
				"input_cleared": true}, nil
		case reason == "stale-epoch":
			return nil, rewindError(rewindMismatch, "The conversation changed meanwhile. Nothing was changed.")
		case reason == "busy":
			return nil, rewindError(rewindBusy, "Wait for the chat to stop before rewinding it.")
		case reason != "":
			return nil, rewindError(rewindFailed, "Orca did not rewind the chat (%s). Nothing was changed.", reason)
		}
		return nil, orcaWriteErr(err)
	}
	return nil, rewindError(rewindMismatch, "The message is not where the app saw it. Reload the chat and try again. Nothing was changed.")
}

// orcaUserText is a user message's words as the user wrote them: its text blocks, one after another.
func orcaUserText(body orcart.JournalBody) string {
	var parts []string
	for _, b := range body.Blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// orcaSquash is text with each run of white space as one space: a message as the app shows it (its transcript's or
// Codex's words) and as Orca's journal keeps it compare alike.
func orcaSquash(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// seen is session.seen: Orca clears the chat's unread mark up to its journal's end (agentSession.acknowledgeAttention),
// on an Orca that has it; nothing on one that does not.
func (b *orcaBridge) seen(ctx context.Context, orca string) error {
	if !b.status(ctx).Has(orcart.CapAttentionAck) {
		return nil
	}
	page, err := b.c.Journal(ctx, orca, "tail", nil, 1)
	if err != nil {
		return err
	}
	return b.c.Seen(ctx, orca, page.Epoch, page.Live)
}

// orcaControlParams are the params of the calls about a chat's controls.
type orcaControlParams struct {
	Title        string `json:"title"`
	Model        string `json:"model"`
	Prompt       string `json:"prompt"`
	Occurrence   int    `json:"occurrence"`
	PromptsAfter *int   `json:"prompts_after"`
	Restore      string `json:"restore"`
}
