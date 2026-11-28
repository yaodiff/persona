package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What the hub asks of the knowledge base and tells it (assistantwire.MethodKB*, EventKB*; BusHub passes them here):
// the hub's own features that read it (the inbox's AI twin, the agents' triage), deleting a source's data, the posts
// of the connectors' programs; a source with something new, an agent's finished turn.

// kbRequest answers one of the hub's kb.* calls; false for any other method.
func (s *Service) kbRequest(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	if out, ok, err := s.kbRPCRequest(ctx, method, params); ok {
		return out, true, err
	}
	decode := func(v any) error {
		if err := json.Unmarshal(params, v); err != nil {
			return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + err.Error()}
		}
		return nil
	}
	switch method {
	case assistantwire.MethodKBOwner:
		return handleAssistantRequest(ctx, assistantwire.KBOwnerMethod, func(out any) error {
			return kbDecodeParams(method, params, out)
		}, func(ctx context.Context, _ buswire.None) (assistantwire.KBOwner, error) {
			out, err := s.kbOwner(ctx)
			return out, kbError(err)
		})
	case assistantwire.MethodKBContext:
		return handleAssistantRequest(ctx, assistantwire.KBContextMethod, decode, func(ctx context.Context, p assistantwire.KBContextParams) (assistantwire.KBContextResult, error) {
			return assistantwire.KBContextResult{Context: s.kbChatContext(ctx, string(p.ChatID))}, nil
		})
	case assistantwire.MethodKBProjects:
		return handleAssistantRequest(ctx, assistantwire.KBProjectsMethod, decode, func(ctx context.Context, p assistantwire.KBProjectsParams) (assistantwire.KBProjectsResult, error) {
			out := assistantwire.KBProjectsResult{Projects: []assistantwire.KBProject{}}
			if e, _ := s.kbGet(ctx); e != nil {
				for _, pr := range e.ProjectsForDir(string(p.MachineID), p.Cwd) {
					out.Projects = append(out.Projects, assistantwire.KBProject{Slug: pr.Slug, Name: pr.Name})
				}
			}
			return out, nil
		})
	case assistantwire.MethodKBForget:
		return handleAssistantRequest(ctx, assistantwire.KBForgetMethod, decode, func(ctx context.Context, p assistantwire.KBForget) (assistantwire.KBForgotten, error) {
			out, err := s.kbForget(ctx, p.Source, p.ArchiveSources...)
			return out, kbError(err)
		})
	case assistantwire.MethodKBPostsPut:
		return handleAssistantRequest(ctx, assistantwire.KBPostsPutMethod, decode, func(ctx context.Context, p assistantwire.KBPosts) (assistantwire.KBPostsPutResult, error) {
			statuses, err := s.kbPutPosts(ctx, p)
			return assistantwire.KBPostsPutResult{Statuses: statuses}, err
		})
	case assistantwire.MethodKBPostsComments:
		return handleAssistantRequest(ctx, assistantwire.KBPostsCommentsMethod, decode, func(ctx context.Context, p assistantwire.KBPosts) (assistantwire.KBPostsCommentsAck, error) {
			return assistantwire.KBPostsCommentsAck{}, s.kbPutComments(ctx, p)
		})
	case assistantwire.MethodKBPostsPending:
		return handleAssistantRequest(ctx, assistantwire.KBPostsPendingMethod, decode, func(ctx context.Context, p assistantwire.KBPosts) (assistantwire.KBPostsPendingResult, error) {
			posts, err := s.kbPendingPosts(ctx, p)
			return assistantwire.KBPostsPendingResult{Posts: posts}, err
		})
	case assistantwire.MethodKBArchivedChats:
		return handleAssistantRequest(ctx, assistantwire.KBArchivedChatsMethod, decode, func(ctx context.Context, p assistantwire.KBPosts) (assistantwire.KBArchivedChatsResult, error) {
			chats, err := s.kbArchivedChats(ctx, p)
			return assistantwire.KBArchivedChatsResult{Chats: chats}, err
		})
	}
	return nil, false, nil
}

// kbEvent takes one of the hub's kb.* events; false for any other.
func (s *Service) kbEvent(ctx context.Context, name string, params json.RawMessage) (bool, error) {
	switch name {
	case assistantwire.EventKBKick:
		var k assistantwire.KBKick
		if len(params) > 0 {
			_ = json.Unmarshal(params, &k) // a manager before #1562 sends {}: only the round
		}
		s.kbNoteKick(k)
		s.kbKick()
	case assistantwire.EventKBAgentTurn:
		var t assistantwire.KBAgentTurn
		if err := json.Unmarshal(params, &t); err != nil {
			return true, err
		}
		s.kbAgentTurn(ctx, t)
	default:
		return false, nil
	}
	return true, nil
}

// kbChatContext is what the knowledge base knows about a chat and its first three projects (shared/kb_blocks.md):
// the inbox's AI twin starts with it, a plugin's /agent reads it (GET /api/v1/kb/chats/{id}/context). nil without
// such a chat.
func (s *Service) kbChatContext(ctx context.Context, id string) *prompts.KBContext {
	e, _ := s.kbGet(ctx)
	if e == nil || id == "" {
		return nil
	}
	c, ok := e.Store.Chat(id)
	if !ok {
		return nil
	}
	return kbContext(e, c)
}

func kbContext(e *kb.Engine, c *kb.Chat) *prompts.KBContext {
	out := &prompts.KBContext{ChatID: c.ID, ChatName: c.Name}
	if sum := e.Store.Summary(kb.KindChat, c.ID); sum != "" {
		out.Summary = clip(sum, 4000)
	}
	for i, slug := range c.Projects {
		if i >= 3 {
			break
		}
		if p, ok := e.Store.Project(slug); ok {
			out.Projects = append(out.Projects, prompts.KBProject{Name: p.Name, Summary: clip(e.Store.Summary(kb.KindProject, slug), 3000)})
		}
	}
	return out
}

// kbForget removes a connector's archived content, index rows, project associations and cursors (DELETE /api/v1/connectors/{id}/data and …/archive, DELETE /api/v1/imessage/data).
func (s *Service) kbForget(ctx context.Context, source string, archiveSources ...string) (assistantwire.KBForgotten, error) {
	s.kb.sourceWrites.Lock()
	defer s.kb.sourceWrites.Unlock()
	s.kb.postWrites.Lock()
	defer s.kb.postWrites.Unlock()
	if s.kbReadOnly(ctx) {
		return assistantwire.KBForgotten{}, kb.ErrReadOnly
	}
	e, err := s.kbGet(ctx)
	if errors.Is(err, errKBOff) {
		return assistantwire.KBForgotten{}, nil
	} else if err != nil {
		return assistantwire.KBForgotten{}, err
	}
	f, err := e.ForgetSource(source, archiveSources...)
	if err != nil {
		return assistantwire.KBForgotten{}, err
	}
	if idx, ok := s.store().(kbIndex); ok {
		if err := idx.DeleteKBEntities(ctx, kb.KindChat, f.Chats); err != nil {
			return assistantwire.KBForgotten{}, err
		}
		if err := idx.DeleteKBEntities(ctx, kb.KindPerson, f.People); err != nil {
			return assistantwire.KBForgotten{}, err
		}
		for kind, ids := range f.Items {
			if err := idx.DeleteKBEntities(ctx, kind, ids); err != nil {
				return assistantwire.KBForgotten{}, err
			}
		}
	}
	for _, name := range append([]string{source}, archiveSources...) {
		if err := s.kbForgetOwner(name); err != nil {
			return assistantwire.KBForgotten{}, err
		}
	}
	s.kbForgotten(source)
	items := 0
	for _, ids := range f.Items {
		items += len(ids)
	}
	return assistantwire.KBForgotten{Chats: len(f.Chats), People: len(f.People), Posts: len(f.Posts), Items: items}, nil
}

// kbAgentTurn files an agent's finished turn under the projects whose code directories it works in: one timeline
// line with what it came to, at most one per agent and 30 minutes (a busy agent finishes many short turns). One that
// comes before the knowledge base opened waits for it (the latest of each agent).
func (s *Service) kbAgentTurn(ctx context.Context, t assistantwire.KBAgentTurn) {
	if !s.kbWritable(ctx) || strings.TrimSpace(t.Line) == "" {
		return
	}
	e, err := s.kbGet(ctx)
	if errors.Is(err, errKBNotOpen) {
		s.kb.mu.Lock()
		s.kb.waiting = queueTurn(s.kb.waiting, t, kbWaitingMost)
		s.kb.mu.Unlock()
		s.kbWake() // the loop tries again in a second, not at its next round
		return
	}
	if e == nil {
		return
	}
	s.kbFileTurn(e, t)
}

// kbTakeWaiting files the turns that came before the knowledge base opened.
func (s *Service) kbTakeWaiting(e *kb.Engine) {
	s.kb.mu.Lock()
	list := s.kb.waiting
	s.kb.waiting = nil
	s.kb.mu.Unlock()
	if e.Store.ReadOnly() {
		return
	}
	for _, t := range list {
		s.kbFileTurn(e, t)
	}
}

// queueTurn adds t to list, the latest of each agent, at most most (the oldest go).
func queueTurn(list []assistantwire.KBAgentTurn, t assistantwire.KBAgentTurn, most int) []assistantwire.KBAgentTurn {
	list = slices.DeleteFunc(list, func(o assistantwire.KBAgentTurn) bool {
		return o.MachineID == t.MachineID && o.SessionID == t.SessionID
	})
	if len(list) >= most {
		list = list[len(list)-most+1:]
	}
	return append(list, t)
}

func (s *Service) kbFileTurn(e *kb.Engine, t assistantwire.KBAgentTurn) {
	if e.Store.ReadOnly() {
		return
	}
	projects := e.ProjectsForDir(t.MachineID, t.Cwd)
	if len(projects) == 0 {
		return
	}
	key := t.MachineID + "/" + t.SessionID
	s.kb.mu.Lock()
	if s.kb.agentAt == nil {
		s.kb.agentAt = map[string]time.Time{}
	}
	if time.Since(s.kb.agentAt[key]) < 30*time.Minute {
		s.kb.mu.Unlock()
		return
	}
	s.kb.agentAt[key] = time.Now()
	s.kb.mu.Unlock()
	title := t.Title
	if title == "" || title == "Claude Code" {
		title = filepath.Base(t.Cwd)
	}
	line := clip(strings.Join(strings.Fields(t.Line), " "), 160)
	for _, p := range projects {
		if _, err := e.Apply(kb.KindProject, p.Slug, kb.Update{Log: line, Source: "Agent · " + title + " · " + t.MachineID}); err != nil {
			s.logf("kb: agent log %s: %v", p.Slug, err)
		}
	}
}
