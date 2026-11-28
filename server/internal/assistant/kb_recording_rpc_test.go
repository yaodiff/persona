package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

// kb.put_recording as the manager hands it over: the extension's own recording is written, kept as its own, written
// again without a change, and refused to another extension and without the envelope.
func TestKBRPCPutRecording(t *testing.T) {
	s := kbServiceOn(t, newFakeHub(), newKBStore(), t.TempDir(), nil)
	e := kbEng(t, s)
	id := "55555555-5555-4555-8555-555555555555"
	start := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	params := `{"id":"` + id + `","title":"Weekly sync","started_at":"` + start + `","minutes":{"markdown":"# Weekly sync\n\n- Shipped the importer","points":[{"text":"Shipped the importer","at":0}]}}`
	ask := func(method, extension, params string) (string, error) {
		raw, _ := json.Marshal(assistantwire.KBExtensionUpdate{Extension: extension, Params: json.RawMessage(params)})
		if extension == "" {
			raw = json.RawMessage(params)
		}
		out, ok, err := s.kbRequest(context.Background(), method, raw)
		if !ok {
			t.Fatalf("%s not handled", method)
		}
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}
	code := func(err error) string {
		var re *rpc.Error
		if errors.As(err, &re) {
			return re.Code
		}
		return ""
	}
	if got, err := ask(assistantwire.MethodKBExtensionPutRecording, "minutes-ext", params); err != nil || !strings.Contains(got, `"created":true`) || !strings.Contains(got, `"revision":1`) {
		t.Fatalf("create: %s %v", got, err)
	}
	if r, ok := e.Store.Recording(id); !ok || r.Writer == nil || r.Writer.By != "ext:minutes-ext" {
		t.Fatalf("stored %+v", r)
	}
	if m, err := e.Store.ReadMinutes(id); err != nil || !strings.Contains(m.Markdown, "importer") {
		t.Fatalf("minutes %+v %v", m, err)
	}
	if got, err := ask(assistantwire.MethodKBExtensionPutRecording, "minutes-ext", params); err != nil || !strings.Contains(got, `"unchanged":true`) {
		t.Fatalf("again: %s %v", got, err)
	}
	if got, err := ask(assistantwire.MethodKBSearch, "", `{"query":"importer","messages":false}`); err != nil || !strings.Contains(got, id) {
		t.Fatalf("search: %s %v", got, err)
	}
	if _, err := ask(assistantwire.MethodKBExtensionPutRecording, "other-ext", params); code(err) != assistantwire.CodeKBNotWriter {
		t.Fatalf("another extension: %v", err)
	}
	if _, err := ask(assistantwire.MethodKBPutRecording, "", params); code(err) != bus.CodeInvalidParams {
		t.Fatalf("without the envelope: %v", err)
	}
	if _, err := ask(assistantwire.MethodKBExtensionPutRecording, "minutes-ext", `{"id":"`+id+`","started_at":"`+start+`","minutes":{"markdown":"x"},"owner":"me"}`); code(err) != bus.CodeInvalidParams {
		t.Fatalf("an unknown field: %v", err)
	}
	if _, err := ask(assistantwire.MethodKBExtensionPutRecording, "minutes-ext", `{"id":"not-a-uuid","started_at":"`+start+`","minutes":{"markdown":"x"}}`); code(err) != bus.CodeInvalidParams {
		t.Fatalf("a bad id: %v", err)
	}
	if _, err := e.DeleteRecording(id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ask(assistantwire.MethodKBExtensionPutRecording, "minutes-ext", params); code(err) != assistantwire.CodeKBDeleted {
		t.Fatalf("after the user deleted it: %v", err)
	}
}
