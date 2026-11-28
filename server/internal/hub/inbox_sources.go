package hub

import (
	"bytes"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// inboxStoredSourceMerge combines the managers' Core-computed source-presence facts; it never infers presence from
// the bounded item list. If any manager is too old, failed, or cannot answer, the merged fact is null (unknown), so a
// client keeps its previous entries rather than possibly hiding an archive. With no such field anywhere, the answers
// are byte-for-byte the old merge's.
func inboxStoredSourceMerge(key string, answers []listAnswer) map[string]json.RawMessage {
	if key != "items" {
		return nil
	}
	var merged assistantwire.InboxStoredSources
	found, complete := false, true
	for i := range answers {
		a := &answers[i]
		if !a.ok() {
			complete = false
			continue
		}
		// Cache the same top-level scan the merge writes from. Decoding each whole response into a map here doubled
		// the ordinary Inbox merge's allocations (TestMergedListAllocation).
		s, use := scanAnswer(a.body, key)
		a.scanned = &scannedAnswer{s: s, use: use}
		if use == scanDecode {
			found = found || bytes.Contains(a.body, []byte(`"stored_sources"`))
			complete = false // only the decoded path can read this unusual response; stay conservative
			continue
		}
		if use == scanSkip {
			complete = false
			continue
		}
		var raw json.RawMessage
		for _, member := range s.members {
			if bytes.Equal(member.key, []byte("stored_sources")) {
				raw = member.val
				break
			}
		}
		if raw == nil {
			complete = false
			continue
		}
		found = true
		var current assistantwire.InboxStoredSources
		if string(raw) == "null" || json.Unmarshal(raw, &current) != nil {
			complete = false
			continue
		}
		merged.Lark = merged.Lark || current.Lark
		merged.Mail = merged.Mail || current.Mail
	}
	if !found {
		return nil
	}
	if !complete {
		return map[string]json.RawMessage{"stored_sources": json.RawMessage("null")}
	}
	raw, _ := json.Marshal(merged)
	return map[string]json.RawMessage{"stored_sources": raw}
}
