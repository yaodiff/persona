package fleetcountadapt

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
)

// viewSamples are the counts views of schema/wire/counts.schema.json the clients' round trips read
// (docs/protocol/counts.md, #15512).
const viewSamples = "../../../../../spec/fixtures/counts/view_samples.json"

// Samples naming a fleet fixture hold the counts and totals View writes for that fleet. A sample that also names
// a todo-counts case holds todos_needs_you as View writes it from the list's counts.
func TestViewSamplesAreFleetcountsRows(t *testing.T) {
	byName := map[string]needsYouCase{}
	for _, c := range needsYouCases(t) {
		byName[c.Name] = c
	}
	persona := map[string]personaCardsCase{}
	for _, c := range personaCardsCases(t) {
		persona[c.Name] = c
	}
	b, err := os.ReadFile(viewSamples)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name  string `json:"name"`
			Input struct {
				NeedsYou     string `json:"needs_you"`
				TodoCounts   string `json:"todo_counts"`
				PersonaCards string `json:"persona_cards"`
			} `json:"input"`
			Expected struct {
				View assistantwire.CountsView `json:"view"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("%s: %v", viewSamples, err)
	}
	lists := todoCountsCases(t)
	checked := 0
	for _, s := range file.Cases {
		if s.Input.NeedsYou == "" && s.Input.PersonaCards == "" {
			continue // a sample of the shape alone (more, an absent count), which no fixture case gives
		}
		t.Run(s.Name, func(t *testing.T) {
			var f *Fleet
			if s.Input.PersonaCards != "" {
				c, ok := persona[s.Input.PersonaCards]
				if !ok {
					t.Fatalf("no persona-cards case %q", s.Input.PersonaCards)
				}
				f = c.fleet(t)
			} else {
				c, ok := byName[s.Input.NeedsYou]
				if !ok {
					t.Fatalf("no needs-you case %q", s.Input.NeedsYou)
				}
				f = c.fleet(t)
			}
			want := s.Expected.View.Counts.AgentsNeedsYou
			if want == nil {
				t.Fatal("no agents_needs_you")
			}
			todos := opt.None[assistantwire.TodoCounts]()
			if s.Input.TodoCounts != "" {
				counts, ok := lists[s.Input.TodoCounts]
				if !ok {
					t.Fatalf("no todo-counts case %q", s.Input.TodoCounts)
				}
				todos = opt.Some(counts)
			}
			got := f.View(f.decide(fleetcount.SurfaceBadge()), todos)
			// A sample may leave its totals out (they are optional); the manager always writes them.
			if tot := s.Expected.View.Totals; !reflect.DeepEqual(got.Counts, s.Expected.View.Counts) ||
				tot != nil && !reflect.DeepEqual(got.Totals, tot) {
				g, _ := json.Marshal(got)
				w, _ := json.Marshal(s.Expected.View)
				t.Fatalf("what View writes\n %s\nthe sample's\n %s", g, w)
			}
			for _, h := range want.Held {
				if !h.Why.Valid() {
					t.Errorf("%s: held as %q, a word the wire does not know", h.ID, h.Why)
				}
			}
			if tot := got.Totals; tot == nil || tot.AgentsNeedsYou == nil || *tot.AgentsNeedsYou != want.N {
				t.Errorf("totals %v, want agents_needs_you %d", tot, want.N)
			}
		})
		checked++
	}
	if checked == 0 {
		t.Fatalf("%s: no sample names a fleet fixture", viewSamples)
	}
}
