package assistant

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A space's open conversations (#15991, docs/assistant/health.md "Tracks and kinds"): GET
// /api/v1/assistant/tracks/{slug}/conversations lists a space track's conversations (one with a directory of its own,
// spaceTrack), newest first. It is the one route an extension's token (with the spaces scope) is shown a space's
// conversations through: never those of a space hidden from it (extensionHides, 404), nor a row its information
// policy hides (extensionHidesConversation, left out of the total too). GET /api/v1/assistant?space= keeps showing an
// extension none of a space's (extensionViewConversations).

func (s *Service) trackConversationRoutes() {
	s.handle("GET /api/v1/assistant/tracks/{slug}/conversations", s.trackConversations)
	s.handle("GET /api/v1/assistant/tracks/launchers", s.trackLaunchers)
}

func (s *Service) trackConversations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := assistantwire.TrackConversationsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || raw != strconv.Itoa(n) || n < 1 || n > assistantwire.TrackConversationsMaxLimit {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("limit %q must be 1 to %d", raw, assistantwire.TrackConversationsMaxLimit))
			return
		}
		limit = n
	}
	slug := r.PathValue("slug")
	extension := r.Header.Get(RoleHeader) == roleExtension
	if _, ok := s.spaceTrack(ctx, slug); !ok || extension && s.extensionHides(ctx, slug) {
		apierr.Write(w, http.StatusNotFound, fmt.Errorf("track %q is not a space", slug))
		return
	}
	rows := s.view(ctx, false, slug).Conversations
	if extension {
		rows = slices.DeleteFunc(rows, func(c proto.Session) bool { return s.extensionHidesConversation(ctx, c) })
	}
	slices.SortStableFunc(rows, func(a, b proto.Session) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	machine := s.settings(ctx).MachineID
	out := assistantwire.TrackConversations{MachineID: assistantwire.SpaceMachineID(machine), Total: len(rows),
		Conversations: make([]assistantwire.TrackConversation, 0, min(len(rows), limit))}
	for _, c := range rows[:min(len(rows), limit)] {
		out.Conversations = append(out.Conversations, assistantwire.TrackConversation{SessionID: assistantwire.SpaceSessionID(c.SessionID),
			MachineID: assistantwire.SpaceMachineID(cmp.Or(c.MachineID, machine)), Title: c.Title,
			LastText:  clipRunesTo(c.LastText, assistantwire.TrackConversationTextMax),
			UpdatedAt: c.UpdatedAt, Space: c.Space})
	}
	apierr.WriteJSON(w, out)
}
