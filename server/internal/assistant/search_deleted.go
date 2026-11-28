package assistant

import (
	"context"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/proto"
)

// The index mirrors transcript words, unlike saved task briefs and knowledge.
// A durable tombstone is already committed before this cleanup; retries must
// finish it too. Index writes hold conversationWrites until their DB commit.
func (s *Service) dropSearchIndex(ctx context.Context, st searchStore, machine, id string) error {
	if st != nil {
		ctx, cancel := context.WithTimeout(ctx, searchCallTimeout)
		defer cancel()
		if err := st.DropSearchConversation(ctx, machine, id); err != nil {
			return err
		}
	}
	k := searchKey{machine, id}
	s.search.mu.Lock()
	delete(s.search.cursors, k)
	delete(s.search.readRev, k)
	delete(s.search.readAt, k)
	delete(s.search.retry, k)
	delete(s.search.gone, k)
	s.search.mu.Unlock()
	return nil
}

func (s *Service) finishDeletedConversation(ctx context.Context, machine, id string) error {
	s.clearConversationOriginDevice(id)
	s.logicalChanged()
	s.historyWake()
	if err := s.dropSearchIndex(ctx, s.searchDB(), machine, id); err != nil {
		return err
	}
	// A deleted conversation takes its user-defined tags and direction filing with it; a missing record is fine.
	if err := s.dropConversationTags(ctx, id); err != nil {
		return err
	}
	if err := s.dropDirectionSession(ctx, machine, id); err != nil {
		return err
	}
	s.sessionsChanged(machine)
	s.publish(ctx)
	return nil
}

func (s *Service) writeSearchAnswer(ctx context.Context, w http.ResponseWriter, answer searchAnswer) {
	// Database reads happen before this fence. The bus recorder buffers the
	// final response, so no client/network stream holds this manager lock.
	s.conversationWrites.RLock()
	defer s.conversationWrites.RUnlock()
	allowed := map[searchKey]bool{}
	var remaining []proto.Session
	for _, c := range answer.candidates {
		_, deleted, err := s.deletedConversation(ctx, c.MachineID, c.SessionID)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		if !deleted {
			allowed[searchKey{c.MachineID, c.SessionID}] = true
			remaining = append(remaining, c)
		}
	}
	kept := answer.Results[:0]
	for _, r := range answer.Results {
		if allowed[searchKey{r.MachineID, r.SessionID}] {
			kept = append(kept, r)
		}
	}
	answer.Results = kept
	answer.Indexed.Total = len(remaining)
	answer.Indexed.Done = s.searchProgress(remaining)
	answer.Partial = answer.Partial || answer.Indexed.Done < answer.Indexed.Total
	apierr.WriteJSON(w, answer)
}
