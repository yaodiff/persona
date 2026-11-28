package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// inboxStoredSources reports every built-in source the manager retains, not only the sources in the bounded list.
// Memory covers changes not saved yet; the store also covers completed history memory no longer holds. A failed store
// read makes the answer unknown, never an empty archive, so clients keep their previous source entries visible.
func (s *Service) inboxStoredSources(ctx context.Context) (assistantwire.InboxStoredSources, bool) {
	seen := map[string]bool{}
	b := &s.inbox
	b.mu.Lock()
	for _, it := range b.items {
		seen[it.Source] = true
	}
	b.mu.Unlock()
	if st := s.inboxDB(); st != nil {
		sources, err := st.StoredInboxSources(ctx)
		if err != nil {
			s.logf("inbox: stored sources: %v", err)
			return assistantwire.InboxStoredSources{}, false
		}
		for _, source := range sources {
			seen[source] = true
		}
	}
	var out assistantwire.InboxStoredSources
	for source := range seen {
		switch source {
		case "lark", "larkcli":
			out.Lark = true
		case "mail":
			out.Mail = true
		}
	}
	return out, true
}
