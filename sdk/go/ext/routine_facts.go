package ext

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/sdk/go/wire"
)

// RoutineFactsCollector supplies plain text for a routine on the owner's local date.
// Return an empty result when there is nothing to include.
type RoutineFactsCollector func(context.Context, wire.RoutineFactsParams) (wire.RoutineFactsResult, error)

func (s *server) collectRoutineFacts(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	if method != "collect" {
		return nil, UnknownMethod("routine_facts." + method)
	}
	var req wire.RoutineFactsParams
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &Error{Code: "invalid", Message: err.Error()}
	}
	collect := s.prog.RoutineFacts[req.Contribution.String()]
	if collect == nil {
		return nil, NoContribution(req.Contribution.String())
	}
	return collect(ctx, req)
}
