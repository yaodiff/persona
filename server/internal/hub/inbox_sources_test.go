package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestWriteMergedCombinesStoredSourcesConservatively(t *testing.T) {
	answer := func(id, stored string) listAnswer {
		body := `{"as_of":"t","items":[]}`
		if stored != "" {
			body = `{"as_of":"t","items":[],"stored_sources":` + stored + `}`
		}
		return listAnswer{id: id, status: http.StatusOK, header: http.Header{}, body: []byte(body)}
	}
	for _, tc := range []struct {
		name    string
		answers []listAnswer
		want    string
	}{
		{"all old", []listAnswer{answer("a", ""), answer("b", "")}, "missing"},
		{"complete union", []listAnswer{
			answer("a", `{"lark":true,"mail":false}`), answer("b", `{"lark":false,"mail":true}`),
		}, "value"},
		{"known empty and stored", []listAnswer{
			answer("a", `{"lark":false,"mail":false}`), answer("b", `{"lark":true,"mail":false}`),
		}, "value"},
		{"mixed old and new", []listAnswer{answer("a", `{"lark":true,"mail":false}`), answer("b", "")}, "null"},
		{"explicit unknown", []listAnswer{answer("a", `{"lark":true,"mail":false}`), answer("b", "null")}, "null"},
		{"failed manager", []listAnswer{
			answer("a", `{"lark":true,"mail":false}`),
			{id: "b", status: http.StatusServiceUnavailable, header: http.Header{}, body: []byte(`{"error":"down"}`)},
		}, "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeMerged(w, "items", tc.answers)
			var top map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &top); err != nil {
				t.Fatal(err)
			}
			raw, present := top["stored_sources"]
			switch tc.want {
			case "missing":
				if present {
					t.Fatalf("stored_sources %s, want absent", raw)
				}
			case "null":
				if !present || string(raw) != "null" {
					t.Fatalf("stored_sources %s (present %v), want null", raw, present)
				}
			case "value":
				var stored assistantwire.InboxStoredSources
				if !present || json.Unmarshal(raw, &stored) != nil || !stored.Lark || !stored.Mail && tc.name == "complete union" {
					t.Fatalf("stored_sources %s, want the managers' union", raw)
				}
				if tc.name == "known empty and stored" && (!stored.Lark || stored.Mail) {
					t.Fatalf("stored_sources %s, want Lark only", raw)
				}
			}
		})
	}
}
