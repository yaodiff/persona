package assistantwire

import (
	"context"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

const MethodAssistantEntriesList = "assistant.entries.list"
const EventAssistantEntriesChanged = "assistant.entries.changed"

type AssistantEntriesParams = proto.AssistantEntriesParams
type AssistantEntriesResult = proto.AssistantEntriesResult

var AssistantEntriesListMethod = buswire.Method[AssistantEntriesParams, AssistantEntriesResult]{Name: MethodAssistantEntriesList}

// Entry changes are invalidation hints, not entry contents. RecordID names the
// appended correction; ID names the entry that was marked wrong.
type AssistantEntriesChanged struct {
	ID       string `json:"id"`
	RecordID string `json:"record_id"`
	Status   string `json:"status"`
	// Personal is internal routing metadata, omitted from plugin notifications.
	Personal bool `json:"personal,omitempty"`
}

func init() {
	Routes = append(Routes, "GET /api/v1/assistant/entries", "POST /api/v1/assistant/entries/{id}/wrong")
}

type extensionReaderKey struct{}

// WithEntriesReader is transport-only identity: the extension manager sets it
// after checking the caller's live grant. Request JSON cannot assert it.
func WithEntriesReader(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, extensionReaderKey{}, id)
}
func EntriesReader(ctx context.Context) string {
	id, _ := ctx.Value(extensionReaderKey{}).(string)
	return id
}
