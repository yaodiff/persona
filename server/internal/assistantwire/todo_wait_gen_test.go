package assistantwire

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/jsonsubset"
)

// Every expected TodoWait in spec/fixtures/todo-wait (#16168) reads into the generated Go type (todo_wait_gen.go),
// writes back as the same JSON, and holds to schema/wire/todo-wait.schema.json.
func TestTodoWaitSamplesRoundTrip(t *testing.T) {
	schemaBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "wire", "todo-wait.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := jsonsubset.Compile(schemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "spec", "fixtures", "todo-wait", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("spec/fixtures/todo-wait: %v", err)
	}
	seenKinds := map[TodoWaitKind]bool{}
	seenBlockers := map[TodoWaitPRBlocker]bool{}
	sawContact := false
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases []struct {
				Name     string `json:"name"`
				Expected struct {
					Wait json.RawMessage `json:"wait"`
				} `json:"expected"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, c := range doc.Cases {
			if len(c.Expected.Wait) == 0 || string(bytes.TrimSpace(c.Expected.Wait)) == "null" {
				continue
			}
			var got TodoWait
			d := json.NewDecoder(bytes.NewReader(c.Expected.Wait))
			d.DisallowUnknownFields()
			if err := d.Decode(&got); err != nil {
				t.Fatalf("%s: decode: %v", c.Name, err)
			}
			if !got.Kind.Valid() {
				t.Errorf("%s: invalid kind %q", c.Name, got.Kind)
			}
			seenKinds[got.Kind] = true
			if got.Contact != nil {
				sawContact = true
			}
			for _, b := range got.PrBlockers {
				if !b.Valid() {
					t.Errorf("%s: invalid pr_blocker %q", c.Name, b)
				}
				seenBlockers[b] = true
			}
			out, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("%s: marshal: %v", c.Name, err)
			}
			var wantVal, gotVal any
			if err := json.Unmarshal(c.Expected.Wait, &wantVal); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &gotVal); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(wantVal, gotVal) {
				t.Errorf("%s: round-trip mismatch\n want %s\n got  %s", c.Name, c.Expected.Wait, out)
			}
			if problem := checkTodoView(t, root, "TodoWait", out); problem != "" {
				t.Errorf("%s: schema violation: %s", c.Name, problem)
			}
		}
	}
	for _, k := range TodoWaitKinds {
		if !seenKinds[k] {
			t.Errorf("spec/fixtures/todo-wait misses kind %q", k)
		}
	}
	for _, b := range TodoWaitPRBlockers {
		if !seenBlockers[b] {
			t.Errorf("spec/fixtures/todo-wait misses pr_blocker %q", b)
		}
	}
	if !sawContact {
		t.Error("spec/fixtures/todo-wait has no TodoWaitContact sample")
	}
}

func TestTodoWaitUnknownEnumReportsDefectAndRoundTrips(t *testing.T) {
	var seen []string
	prev := TodoWaitWireUnknownEnum
	TodoWaitWireUnknownEnum = func(enum, raw string) { seen = append(seen, enum+":"+raw) }
	defer func() { TodoWaitWireUnknownEnum = prev }()

	raw := `{"kind":"future_wait","waiting_on":"Upstream","pr_blockers":["future_blocker"]}`
	var w TodoWait
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		t.Fatal(err)
	}
	if w.Kind.Valid() || w.Kind != "future_wait" {
		t.Fatalf("kind = %q (valid=%v)", w.Kind, w.Kind.Valid())
	}
	if len(w.PrBlockers) != 1 || w.PrBlockers[0].Valid() || w.PrBlockers[0] != "future_blocker" {
		t.Fatalf("pr_blockers = %v", w.PrBlockers)
	}
	if !reflect.DeepEqual(seen, []string{"TodoWaitKind:future_wait", "TodoWaitPRBlocker:future_blocker"}) {
		t.Fatalf("defect hook saw %v", seen)
	}
	out, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != raw {
		t.Fatalf("round-trip = %s, want %s", out, raw)
	}
}
