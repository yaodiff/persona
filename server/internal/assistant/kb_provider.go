package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Match the installation before opening the archive. An unselected provider sees
// the same error for every chat, account, date and import state.
func (s *Service) kbProviderBinding(ctx context.Context, req assistantwire.KBProviderRequest) (assistantwire.KBImportProvider, *assistantwire.KBHistory, bool) {
	set := s.kbSettings(ctx)
	if set == nil || set.Off || set.ImportProvider == nil || !set.History.On() {
		return assistantwire.KBImportProvider{}, nil, false
	}
	b := *set.ImportProvider
	stamp, err := time.Parse(time.RFC3339Nano, req.InstalledAt)
	if err != nil || stamp.IsZero() || !b.InstalledAt.Equal(stamp) || b.Extension == "" || b.Extension != req.Extension ||
		b.StorageID == "" || b.StorageID != req.StorageID || b.Connector == "" || b.SourceID == "" || b.FiledAs == "" {
		return assistantwire.KBImportProvider{}, nil, false
	}
	return b, set.History, true
}

func kbProviderDenied() *rpc.Error {
	return &rpc.Error{Code: "permission_denied", Message: "knowledge import selection unavailable"}
}

func (s *Service) kbProviderCheckpointRPC(ctx context.Context, req assistantwire.KBProviderRequest) (*wire.KBImportCheckpointResult, error) {
	b, history, ok := s.kbProviderBinding(ctx, req)
	if !ok || s.ui == nil {
		return nil, kbProviderDenied()
	}
	if req.Params != (assistantwire.KBImportQuery{}) {
		return nil, kbInvalidParams(assistantwire.MethodKBProviderCheckpoint, "expected an empty checkpoint query")
	}
	_, cp, ok := s.kbProviderCheckpoint(ctx, b, history, "")
	if !ok {
		return nil, kbProviderDenied()
	}
	current, _, ok := s.kbProviderBinding(ctx, req)
	if !ok || current != b {
		return nil, kbProviderDenied()
	}
	return &cp, nil
}

func (s *Service) kbProviderSentMarkersRPC(ctx context.Context, req assistantwire.KBProviderRequest) (*wire.KBSentMarkersResult, error) {
	b, history, ok := s.kbProviderBinding(ctx, req)
	if !ok || s.ui == nil {
		return nil, kbProviderDenied()
	}
	query := req.Params
	if _, err := time.Parse(time.DateOnly, query.Day); err != nil || query.After < 0 || query.Approval == "" {
		return nil, kbInvalidParams(assistantwire.MethodKBProviderSentMarkers, "invalid day, approval or starting line")
	}
	engine, cp, ok := s.kbProviderCheckpoint(ctx, b, history, query.Day)
	if !ok || cp.Approval != query.Approval {
		return nil, kbProviderDenied()
	}
	day, err := s.kbDaySent(ctx, engine, assistantwire.KBDayParams{ID: kb.ChatID(b.FiledAs, string(b.SourceID)), Day: query.Day})
	if err != nil || day.SentOrigin != "available" {
		return nil, &rpc.Error{Code: "unavailable", Message: "sent markers unavailable"}
	}
	// The ledger lookup can span a change to the selected account, retention or plan.
	current, history, ok := s.kbProviderBinding(ctx, req)
	if !ok || current != b {
		return nil, kbProviderDenied()
	}
	fresh, ok := s.ui.ProviderCheckpoint(engine, history, b, query.Day, time.Now())
	if !ok || fresh.Approval != cp.Approval {
		return nil, kbProviderDenied()
	}
	page := kbMarkerPage(day.Messages, query.After)
	return &page, nil
}

func (s *Service) kbProviderCheckpoint(ctx context.Context, b assistantwire.KBImportProvider, history *assistantwire.KBHistory, day string) (*kb.Engine, wire.KBImportCheckpointResult, bool) {
	engine, err := s.kbGet(ctx)
	if err != nil {
		return nil, wire.KBImportCheckpointResult{}, false
	}
	cp, ok := s.ui.ProviderCheckpoint(engine, history, b, day, time.Now())
	return engine, cp, ok
}

func kbMarkerPage(messages []kb.DayMessage, after int) wire.KBSentMarkersResult {
	const pageSize = 100
	result := wire.KBSentMarkersResult{Markers: []wire.KBSentMarker{}}
	for _, line := range messages {
		if line.N <= after || line.SentBy != "assistant" {
			continue
		}
		if len(result.Markers) == pageSize {
			result.NextAfter = result.Markers[pageSize-1].N
			break
		}
		marker := wire.KBSentMarker{N: line.N, SentBy: line.SentBy}
		if line.RecalledAt != nil {
			marker.RecalledAt = *line.RecalledAt
		}
		result.Markers = append(result.Markers, marker)
	}
	return result
}
