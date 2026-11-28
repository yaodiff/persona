package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/agentref/agentrefadapt"
	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	kitoutcome "github.com/nwverse/persona/server/internal/domainkit/outcome" // the package's tests declare an outcome
)

// inboxModule is the Inbox as a module of the assistant's kernel (kernel/seam): New registers it where its routes
// were added, and it registers its routes, its bus methods, its settled-turn and handshake hooks and its part of the
// snapshot, each run where its central line ran (seam_slots.go). The zero inboxModule has no Service; only
// newInboxModule makes one.
type inboxModule struct{ s *Service }

// newInboxModule is the Inbox of s. A nil s is a defect.
func newInboxModule(s *Service) inboxModule {
	if s == nil {
		defect.Raise("assistant.newInboxModule", "the Service is nil")
	}
	return inboxModule{s: s}
}

// Name is the Inbox's name in the kernel.
func (inboxModule) Name() seam.ModuleName { return seam.NewModuleName("inbox") }

// Register adds the Inbox's entries through k. A zero inboxModule is a defect.
func (m inboxModule) Register(k seam.Kernel) {
	s := m.s
	if s == nil {
		defect.Raise("assistant.inboxModule.Register", "an inboxModule comes from newInboxModule")
	}
	s.routesThrough(k, s.inboxRoutes) // its route functions as they are (inbox_api.go)
	// The extension manager's calls about the Inbox (assistantwire.MethodInbox*).
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxCollectedMethod, s.inboxCollectedCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxItemMethod, s.inboxItemCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxForgetMethod, s.inboxForgetCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxRepoMutesMethod, s.inboxRepoMutesCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxMutesMethod, s.inboxMutesCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxPostMethod, s.inboxPostCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxTriagePreviewMethod, s.inboxTriagePreviewCall))
	k.Bus(seam.NewTypedBusMethod(assistantwire.InboxUnmuteMethod, s.inboxUnmuteCall))
	k.TurnSettled(s.inboxTurnSettled)
	k.Connected(s.inboxConnected)
	k.Snapshot(s.inboxSnapshot)
}

// inboxCollectedCall is inbox.collected: what one collection read of the sources (inbox_collect.go).
func (s *Service) inboxCollectedCall(ctx context.Context, p assistantwire.InboxRound) (assistantwire.InboxAck, error) {
	return assistantwire.InboxAck{}, s.InboxCollected(ctx, p)
}

// inboxItemCall is inbox.item: one item as it is stored, for an extension's inbox_item slot.
func (s *Service) inboxItemCall(ctx context.Context, p assistantwire.InboxItemParams) (assistantwire.InboxItemResult, error) {
	raw, ok := s.InboxItemJSON(ctx, string(p.ID))
	return assistantwire.InboxItemResult{Item: raw, OK: ok}, nil
}

// inboxForgetCall is inbox.forget: the user deleted a source's data, so its items go too.
func (s *Service) inboxForgetCall(ctx context.Context, p assistantwire.InboxForgetParams) (assistantwire.InboxForgetResult, error) {
	n, err := s.InboxForget(ctx, p.Source)
	return assistantwire.InboxForgetResult{Deleted: n}, err
}

// inboxRepoMutesCall is inbox.repo_mutes: a source's muted repositories, set first when asked (inbox_mutes.go).
func (s *Service) inboxRepoMutesCall(ctx context.Context, p assistantwire.InboxRepoMutes) (assistantwire.InboxRepoMutesResult, error) {
	repos, err := s.InboxRepoMutes(ctx, p)
	return assistantwire.InboxRepoMutesResult{Repos: repos}, err
}

// inboxMutesCall is inbox.mutes: the mutes, one source's when it names one, for the Inbox Sources pages.
func (s *Service) inboxMutesCall(ctx context.Context, p assistantwire.InboxMutesParams) (assistantwire.InboxMutesResult, error) {
	mutes, err := s.InboxSourceMutes(ctx, p.Source)
	return assistantwire.InboxMutesResult{Mutes: mutes}, err
}

// inboxPostCall is inbox.post: whether the assistant may put its conversations into the Inbox, set first when asked
// (inbox_post.go).
func (s *Service) inboxPostCall(ctx context.Context, p assistantwire.InboxPostParams) (assistantwire.InboxPostResult, error) {
	if p.On != assistantwire.InboxPostLeftOut {
		on := p.On == assistantwire.InboxPostOn
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.InboxPost = &on; return nil }); err != nil {
			return assistantwire.InboxPostResult{}, err
		}
	}
	return assistantwire.InboxPostResult{On: s.inboxPostOn(ctx)}, nil
}

// inboxTriagePreviewCall is inbox.triage_preview: the triage model's verdicts on a subscription's dry-run items
// (inbox_triage_preview.go).
func (s *Service) inboxTriagePreviewCall(ctx context.Context, p assistantwire.InboxTriagePreview) (assistantwire.InboxTriagePreviewResult, error) {
	return assistantwire.InboxTriagePreviewResult{Verdicts: s.InboxTriagePreview(ctx, p)}, nil
}

// inboxUnmuteCall is inbox.unmute: the user unmuted one on the Inbox Sources pages; what it filed stays done.
func (s *Service) inboxUnmuteCall(ctx context.Context, p assistantwire.InboxUnmuteParams) (assistantwire.InboxAck, error) {
	return assistantwire.InboxAck{}, s.InboxUnmute(ctx, string(p.ID))
}

// inboxTurnSettled is the Inbox's settled-turn hook: when an open item's agent entry links agent, the Agent's stop is
// recorded on the items that link it (inboxAgentTurnSettled, inbox_agent_stopped.go), which goes on with that link's
// ids as the item stored them.
func (s *Service) inboxTurnSettled(ctx context.Context, agent agentref.Ref) {
	if s.inboxReady(ctx) != nil {
		return
	}
	if link, ok := s.inboxAgentLinkTo(agent); ok {
		s.inboxAgentTurnSettled(ctx, link.MachineID, link.SessionID)
	}
}

// inboxAgentLinkTo is the session link of an open item's agent entry that names agent, if one does. A link whose ids
// name no session never matches. Callers have checked inboxReady.
func (s *Service) inboxAgentLinkTo(agent agentref.Ref) (InboxChainLink, bool) {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, it := range b.items {
		if !it.open() {
			continue
		}
		for _, e := range it.Chain {
			if e.Kind != ChainAgent || e.Link == nil || e.Link.Type != LinkSession {
				continue
			}
			linked, f := kitoutcome.Unpack(agentrefadapt.FromWire(SessionRef{MachineID: e.Link.MachineID, SessionID: e.Link.SessionID}))
			if f == nil && linked.IsSame(agent) {
				return *e.Link, true
			}
		}
	}
	return InboxChainLink{}, false
}

// inboxConnected is the Inbox's handshake hook: the "Needs you" count, which the hub keeps of this connection only
// (inbox_store.go). What is stored is read now if nothing read it yet, so the roster has the number before the first
// round.
func (s *Service) inboxConnected(ctx context.Context) {
	if s.inboxReady(ctx) == nil {
		s.tellInboxNeedsYou()
	}
}

// inboxSnapshot is the Inbox's part of the snapshot: what the hub shows of it (inboxReport). The snapshot is partial
// when it could not be read.
func (s *Service) inboxSnapshot(ctx context.Context, snap *assistantwire.Snapshot) {
	snap.Inbox = s.inboxReport(ctx)
	snap.Partial = snap.Partial || snap.Inbox == nil
}
