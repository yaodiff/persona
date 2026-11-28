package fleetcountadapt

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/proto"
)

const personaCardsFixtures = "../../../../../spec/fixtures/persona-cards/cases.json"

type personaCardsCase struct {
	Name  string `json:"name"`
	Input struct {
		Sessions     []proto.Session     `json:"sessions"`
		Interactions []proto.Interaction `json:"interactions"`
		Muted        []string            `json:"muted"`
	} `json:"input"`
	Expected struct {
		CardIDs []assistantwire.CountsCardID `json:"card_ids"`
	} `json:"expected"`
}

func personaCardsCases(t *testing.T) []personaCardsCase {
	t.Helper()
	b, err := os.ReadFile(personaCardsFixtures)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int                `json:"version"`
		Cases   []personaCardsCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &file); err != nil || file.Version != 1 || len(file.Cases) == 0 {
		t.Fatalf("%s: version %d, %d cases, %v", personaCardsFixtures, file.Version, len(file.Cases), err)
	}
	return file.Cases
}

func (c personaCardsCase) fleet(t *testing.T) *Fleet {
	t.Helper()
	var f Fleet
	for i := range c.Input.Sessions {
		s := &c.Input.Sessions[i]
		muted := slices.Contains(c.Input.Muted, s.MachineID+"/"+s.SessionID)
		if s.Quiet {
			if _, failed := outcome.Unpack(f.PersonaAgent(s, muted)); failed != nil {
				t.Fatal(Log(failed))
			}
		} else if _, failed := outcome.Unpack(f.Agent("Computer", s, muted)); failed != nil {
			t.Fatal(Log(failed))
		}
	}
	for i := range c.Input.Interactions {
		if _, failed := outcome.Unpack(f.Card(&c.Input.Interactions[i], false)); failed != nil {
			t.Fatal(Log(failed))
		}
	}
	return &f
}

func TestPersonaCardsViewFollowsTheSharedFixture(t *testing.T) {
	for _, c := range personaCardsCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			v := badgeView(c.fleet(t))
			got := v.Counts.PersonaCards
			want := c.Expected.CardIDs
			if want == nil {
				t.Fatal("fixture must give the expected card ids")
			}
			if got == nil || v.Totals == nil || v.Totals.PersonaCards == nil || *v.Totals.PersonaCards != len(want) ||
				got.N != len(want) || got.More != 0 || len(got.Held) != 0 || len(got.Keys) != len(want) {
				b, _ := json.Marshal(v)
				t.Fatalf("view %s, want one Persona row per card in %v", b, want)
			}
			for i, key := range got.Keys {
				if key.ID != assistantwire.CountsRowKey("card:"+want[i]) ||
					!slices.Equal(key.Cards, []assistantwire.CountsCardID{want[i]}) {
					t.Fatalf("Persona key %d: %+v, want %s", i, key, want[i])
				}
			}
		})
	}
}

func TestPersonaCardsShareTheViewBudget(t *testing.T) {
	for _, c := range []struct {
		name                string
		agents, held, cards int
		idSuffix            string
		wantKeys            int // -1 when the bytes allow some cards
	}{
		{name: "row cap", agents: 3, held: 2, cards: 205, wantKeys: 200},
		{name: "escaped ids", agents: 3, held: 2, cards: 50, idSuffix: strings.Repeat("<&", 30), wantKeys: -1},
		{name: "Agents rows take precedence", agents: 300, held: 50, cards: 5, idSuffix: strings.Repeat("<&", 30)},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := waitingFleet(t, "studio", c.agents, c.held, longIDs(""))
			before := badgeView(f).Counts.AgentsNeedsYou
			s := &proto.Session{MachineID: "studio", SessionID: "persona", Assistant: true}
			if _, failed := outcome.Unpack(f.Agent("Computer", s, false)); failed != nil {
				t.Fatal(Log(failed))
			}
			var ids []assistantwire.CountsCardID
			first := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
			for i := range c.cards {
				_, id := shortIDs(i)
				id = "persona-" + id + c.idSuffix
				ix := &proto.Interaction{ID: id, MachineID: s.MachineID, SessionID: s.SessionID,
					Kind: proto.IxPermission, CreatedAt: first.Add(time.Duration(i) * time.Minute)}
				if _, failed := outcome.Unpack(f.Card(ix, false)); failed != nil {
					t.Fatal(Log(failed))
				}
				ids = append(ids, assistantwire.CountsCardID(id))
			}
			v := badgeView(f)
			v.Epoch, v.Rev, v.AsOf, v.Build = "example-epoch", 1, 1795593601000, "1.0.0"
			b, err := json.Marshal(v)
			if err != nil || len(b) > assistantwire.CountsViewMaxBytes {
				t.Fatalf("view: %d bytes (max %d), %v", len(b), assistantwire.CountsViewMaxBytes, err)
			}
			if !reflect.DeepEqual(v.Counts.AgentsNeedsYou, before) {
				t.Fatal("Persona keys took room from the Agents count")
			}
			got := v.Counts.PersonaCards
			listed := len(got.Keys)
			if got.N != c.cards || *v.Totals.PersonaCards != got.N || listed+got.More != got.N || len(got.Held) != 0 {
				t.Fatalf("Persona: n %d, keys %d, more %d, held %d, total %d. Want %d cards.",
					got.N, listed, got.More, len(got.Held), *v.Totals.PersonaCards, c.cards)
			}
			if c.wantKeys >= 0 && listed != c.wantKeys || c.wantKeys < 0 && (listed == 0 || listed >= c.cards) {
				t.Fatalf("listed %d of %d Persona cards (case expects %d keys)", listed, c.cards, c.wantKeys)
			}
			for i, key := range got.Keys {
				if key.ID != assistantwire.CountsRowKey("card:"+ids[i]) ||
					!slices.Equal(key.Cards, []assistantwire.CountsCardID{ids[i]}) {
					t.Fatalf("Persona key %d: %+v, want %s", i, key, ids[i])
				}
			}
		})
	}
}
