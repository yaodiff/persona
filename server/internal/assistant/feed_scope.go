package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension's token (assistant.ask) is shown of the assistant's conversations (#10941,
// docs/extensions/extensions.md "Never shown to extensions"), by the rule the to-do routes keep (todo_scope.go): GET
// /api/v1/assistant lists none of a space's conversations (extensionConversations), and a conversation's feed answers
// 404, as /sessions does for a chat of a space, for a conversation of a space hidden from extensions and for one whose
// space cannot be told (feedHiddenFromExtension). A feed it is shown leaves out what the to-do routes do not show it
// (extensionFeed): the items, plans and task cards of a to-do todosHidden hides, a plan with a task in such a space,
// and every answered card, as the timeline does. Nor the digests that gather many to-dos and cards at once (a routine's
// brief, the Today card), which would each need the rule row by row. The kinds it is shown are listed, so a kind added
// later is left out until it says which to-do it is about.

// errNoSuchConversation is the feed's 404 to an extension's token, as /sessions answers it for a chat it is not shown.
var errNoSuchConversation = i18n.Err("hub-deleted_sessions.unknown")

// todoFeedKinds are the feed's kinds about one to-do, by its id in ref (tasks.go). feedAnswered is one too, never shown.
var todoFeedKinds = []string{feedTask, feedReceipt, feedRouted, feedReport, feedReview, feedStartRemedy}

// extensionViewConversations are those of list (GET /api/v1/assistant) an extension's token is shown:
// none of a space, by its space or its directory (without the registry here, any space is left out),
// and never a LogicalPlaceholder whose space is not affirmatively known to be the default empty space.
// The one route that lists a space's conversations to an extension is GET
// /api/v1/assistant/tracks/{slug}/conversations, behind the spaces scope and row by row
// (trackConversations, track_conversations.go).
func (s *Service) extensionViewConversations(ctx context.Context, list []proto.Session) []proto.Session {
	return slices.DeleteFunc(list, func(c proto.Session) bool {
		if runSpace(c) != "" {
			return true
		}
		if s.isLogicalOfflineRow(c) {
			sp, known := s.logicalRowSpace(ctx, c)
			return !known || sp != ""
		}
		return false
	})
}

// feedHiddenFromExtension: an extension's token is not shown conversation id's feed: one of a space hidden from
// extensions, or one whose space cannot be told. One noted in such a space is one whatever a computer lists under its
// id now; else one a computer lists (the assistant's, the one the main conversation is routed to, the one it was
// opened on) as the assistant's conversation or in such a space, by its space or its directory; else as
// conversationSpace tells it (its note, a task leader's agent, the record of its deletion). A session started under
// the id in a terminal (a resume of an id its computer has no transcript of) is not the conversation the feed is of
// (#10941).
func (s *Service) feedHiddenFromExtension(ctx context.Context, id string) bool {
	if _, _, leader := parseLeaderRef(id); !leader {
		if s.extensionHides(ctx, s.notedSpaces(ctx, []string{id})[id]) {
			return true
		}
		if c, ok := s.session(ctx, s.conversationMachine(ctx, id, ""), id); ok {
			if hidden := s.extensionHides(ctx, runSpace(c)); hidden {
				return true
			}
			if s.isAssistantRow(c) && !(s.isLogicalOfflineRow(c) && c.Space == "" && c.Cwd == "") {
				return false
			}
		}
	}
	space, known := s.conversationSpace(ctx, id)
	return !known || s.extensionHides(ctx, space)
}

// extensionFeed is what of a feed's items and of the plans they name (feedPlans) an extension's token is shown, and
// which of the task cards worked out from them (above). One read (todosHidden) decides every to-do the items, their
// message cards and the plans name; a plan is judged as it is now and as its item keeps it.
func (s *Service) extensionFeed(ctx context.Context, items []FeedItem, plans map[string]*TaskPlan) ([]FeedItem, map[string]*TaskPlan, func(TaskCard) bool) {
	aboutTodo := func(it FeedItem) bool {
		return slices.Contains(todoFeedKinds, it.Kind) || it.Kind == feedNotice && it.Ref != ""
	}
	messageTodo := func(it FeedItem) string {
		var c MessageCard
		_ = json.Unmarshal(it.Body, &c)
		return c.TodoID
	}
	itemPlan := func(it FeedItem) *TaskPlan {
		var b struct {
			Plan *TaskPlan `json:"plan"`
		}
		_ = json.Unmarshal(it.Body, &b)
		return b.Plan
	}
	var ids []string
	planTodos := func(p *TaskPlan) {
		for _, t := range p.Tasks {
			ids = append(ids, t.TodoID)
		}
	}
	for _, it := range items {
		switch {
		case aboutTodo(it):
			ids = append(ids, it.Ref)
		case it.Kind == feedMessage:
			ids = append(ids, messageTodo(it))
		case it.Kind == feedPlan:
			if p := itemPlan(it); p != nil {
				planTodos(p)
			}
		}
	}
	for _, p := range plans {
		planTodos(p)
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return id == "" })
	slices.Sort(ids)
	ids = slices.Compact(ids)
	hidden := map[string]bool{} // none judged when the to-dos cannot be read: none shown
	if len(ids) > 0 && s.ensureTodosLoaded(ctx) == nil {
		hidden = s.todosHidden(ctx, ids, nil)
	}
	todoShown := func(id string) bool {
		h, judged := hidden[id]
		return judged && !h
	}
	planShown := func(p *TaskPlan) bool {
		return p != nil && !slices.ContainsFunc(p.Tasks, func(t PlanTask) bool {
			return t.TodoID != "" && !todoShown(t.TodoID) || s.extensionHides(ctx, identity.SpaceAt(t.Cwd))
		})
	}

	shownPlans := make(map[string]*TaskPlan, len(plans))
	for id, p := range plans {
		if planShown(p) {
			shownPlans[id] = p
		}
	}
	planItems := map[string]bool{} // the plans whose items it is shown
	out := make([]FeedItem, 0, len(items))
	for _, it := range items {
		// A report-only routine stores its provider digest as an ordinary notice for older
		// clients. Give it the same extension visibility as the structured brief item.
		if it.Kind == feedNotice && strings.HasPrefix(it.ID, routineContributionNoticePrefix) {
			continue
		}
		var shown bool
		switch {
		case aboutTodo(it):
			shown = todoShown(it.Ref)
		case it.Kind == feedMessage:
			shown = todoShown(messageTodo(it))
		case it.Kind == feedPlan:
			now, ok := plans[it.Ref]
			shown = planShown(itemPlan(it)) && (!ok || planShown(now))
			planItems[it.Ref] = planItems[it.Ref] || shown
		case it.Kind == feedNotice, it.Kind == feedInboxSuggestion:
			shown = true // the conversation's own: a line the manager writes, an Inbox item handed to it
		}
		if shown {
			out = append(out, it)
		}
	}
	cardShown := func(c TaskCard) bool {
		if c.TodoID != "" {
			return todoShown(c.TodoID)
		}
		return planItems[c.PlanID] // a task of a plan not started yet: only of a plan whose item is shown
	}
	return out, shownPlans, cardShown
}
