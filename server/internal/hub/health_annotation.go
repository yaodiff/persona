package hub

import (
	"encoding/json"
	"fmt"

	"github.com/nwverse/persona/server/internal/proto"
)

// annotateHealthSnapshot makes the locked-phone fallback explicit to callers: when the iPhone answered
// phone_health_summary from its on-disk snapshot, the JSON already carries "source":"snapshot" and "as_of",
// and the hub prepends a plain-text note saying so. The JSON content itself is untouched, so callers that
// read the existing fields keep working. Everything else (live answers, errors, other tools) passes through.
func annotateHealthSnapshot(name string, result proto.ToolResult) proto.ToolResult {
	if name != "phone_health_summary" || result.IsError || len(result.Content) == 0 {
		return result
	}
	var payload struct {
		Source string `json:"source"`
		AsOf   string `json:"as_of"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		return result
	}
	if payload.Source != "snapshot" {
		return result
	}
	note := fmt.Sprintf("Health summary from the iPhone's on-device snapshot (as_of=%s): the device is locked, "+
		"so these are the values last saved while it was unlocked, not live HealthKit readings.", payload.AsOf)
	// TextResult builds the content inside the proto package: a `Type: "text"` literal in the hub's
	// own files would be mistaken for an app message type by the broadcast-type scanner test.
	noteContent := proto.TextResult(note, false).Content
	result.Content = append(noteContent, result.Content...)
	return result
}
