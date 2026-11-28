package recordingwire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDebateStatusValid(t *testing.T) {
	if !DebateOpen.Valid() || !DebateSettled.Valid() {
		t.Fatal("open and settled must be valid")
	}
	for _, s := range []DebateStatus{"", "unknown", "OPEN", "resolved"} {
		if s.Valid() {
			t.Errorf("%q should not be valid", s)
		}
	}
}

func TestMinutesDebatesMarshalAndLenientUnmarshal(t *testing.T) {
	// No debates: omitempty keeps "debates" out of minutes.json.
	empty := Minutes{Title: "Sync", Points: []MinutesPoint{{Text: "One", At: 1}}}
	raw, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"debates"`) {
		t.Errorf("empty debates serialized: %s", raw)
	}

	// Decision index 0 serializes explicitly; nil decision is omitted.
	zero := 0
	with := Minutes{
		Title:     "Sync",
		Decisions: []MinutesItem{{Text: "Per item", At: 1504.5}},
		Debates: []MinutesDebate{
			{
				Question: "Per item or per recording?",
				Topic:    "Export",
				Status:   DebateSettled,
				At:       312.0,
				Positions: []MinutesDebatePosition{
					{Stance: "Per item", At: 312.0, Who: "Ada"},
					{Stance: "Per recording", At: 340.5},
				},
				Outcome: &MinutesDebateOutcome{Text: "Per item for this release", At: 1504.5, Decision: &zero},
			},
			{
				Question: "Weekly or daily?",
				Status:   DebateOpen,
				At:       400.0,
				Positions: []MinutesDebatePosition{
					{Stance: "Weekly", At: 400.0},
					{Stance: "Daily", At: 420.0},
				},
			},
		},
	}
	raw, err = json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"decision":0`) {
		t.Errorf("decision index 0 missing: %s", raw)
	}
	var decoded Minutes
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Debates) != 2 || decoded.Debates[0].Outcome == nil || decoded.Debates[0].Outcome.Decision == nil || *decoded.Debates[0].Outcome.Decision != 0 {
		t.Fatalf("round-trip failed: %+v", decoded.Debates)
	}
	if decoded.Debates[1].Outcome != nil || decoded.Debates[1].Status != DebateOpen {
		t.Fatalf("open debate mismatch: %+v", decoded.Debates[1])
	}

	// Malformed debates never fail decoding the rest of minutes.json.
	for _, bad := range []string{
		`{"title":"Sync","points":[{"text":"One","at":1}],"debates":"bad"}`,
		`{"title":"Sync","points":[{"text":"One","at":1}],"debates":{}}`,
		`{"title":"Sync","points":[{"text":"One","at":1}],"debates":null}`,
		`{"title":"Sync","points":[{"text":"One","at":1}],"debates":[]}`,
		`{"title":"Sync","points":[{"text":"One","at":1}],"debates":["x",null,{"question":"","positions":[{"stance":"a","at":1},{"stance":"b","at":2}]},{"question":"Only one","positions":[{"stance":"a","at":1}]}]}`,
	} {
		var m Minutes
		if err := json.Unmarshal([]byte(bad), &m); err != nil {
			t.Errorf("%s: unmarshal error %v", bad, err)
		}
		if m.Title != "Sync" || len(m.Points) != 1 || m.Debates != nil {
			t.Errorf("%s: got title=%q points=%d debates=%+v", bad, m.Title, len(m.Points), m.Debates)
		}
	}

	// Unknown status is preserved (not Valid), unknown keys ignored, out-of-bounds decision dropped.
	var extra Minutes
	payload := `{"title":"Sync","decisions":[{"text":"D0","at":10}],"debates":[
		{"question":"Q1","status":"escalated","at":1,"extra":true,"positions":[
			{"stance":"A","at":1,"extra":1},{"stance":"B","at":2}
		],"outcome":{"text":"Ignored","at":10,"decision":0}},
		{"question":"Q2","status":"settled","at":3,"positions":[
			{"stance":"C","at":3},{"stance":"D","at":4}
		],"outcome":{"text":"Picked","at":10,"decision":5,"extra":"x"}}
	]}`
	if err := json.Unmarshal([]byte(payload), &extra); err != nil {
		t.Fatal(err)
	}
	if len(extra.Debates) != 2 {
		t.Fatalf("got %d debates", len(extra.Debates))
	}
	if extra.Debates[0].Status != "escalated" || extra.Debates[0].Status.Valid() || extra.Debates[0].Outcome != nil {
		t.Errorf("unknown status debate: %+v", extra.Debates[0])
	}
	if extra.Debates[1].Outcome == nil || extra.Debates[1].Outcome.Decision != nil {
		t.Errorf("out-of-bounds decision should be nil: %+v", extra.Debates[1].Outcome)
	}
}

func TestMinutesDebatesSpeakerAttributionRoundTripAndLenient(t *testing.T) {
	// Positions without speaker fields omit every new attribution key.
	plain := Minutes{
		Title: "Sync",
		Debates: []MinutesDebate{{
			Question: "A or B?",
			Status:   DebateOpen,
			At:       10,
			Positions: []MinutesDebatePosition{
				{Stance: "A", At: 10, Who: "Ada"},
				{Stance: "B", At: 20},
			},
		}},
	}
	plainRaw, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"span"`, `"est"`, `"over"`, `"said_by"`, `"speaker"`, `"speaker_unsure"`, `"also"`} {
		if strings.Contains(string(plainRaw), forbidden) {
			t.Errorf("plain position serialized %s: %s", forbidden, plainRaw)
		}
	}

	// Full attribution round-trip including span, est, over, said_by, speaker, speaker_unsure and also.
	sp1 := SpeakerSpan{10.5, 19.0}
	sp2 := SpeakerSpan{20.0, 31.5}
	spAlso := SpeakerSpan{40.0, 48.2}
	with := Minutes{
		Title: "Sync",
		Debates: []MinutesDebate{{
			Question: "Per item or per recording?",
			Status:   DebateOpen,
			At:       10.5,
			Positions: []MinutesDebatePosition{
				{
					Stance:        "Per item",
					At:            10.5,
					Span:          &sp1,
					Over:          map[SpeakerID]float64{"s1": 7.8, "s2": 0.4},
					SaidBy:        "s1",
					Speaker:       "s1",
					SpeakerUnsure: false,
					Also: []MinutesDebateAlso{{
						At:      40.0,
						Span:    &spAlso,
						Over:    map[SpeakerID]float64{"s3": 6.2},
						SaidBy:  "s3",
						Speaker: "s3",
					}},
					Who: "Ada",
				},
				{
					Stance:        "Per recording",
					At:            20.0,
					Span:          &sp2,
					Est:           true,
					SaidBy:        "s2",
					Speaker:       "s2",
					SpeakerUnsure: true,
				},
			},
		}},
	}
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	var got Minutes
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Debates) != 1 || len(got.Debates[0].Positions) != 2 {
		t.Fatalf("round-trip debates: %+v", got.Debates)
	}
	p0, p1 := got.Debates[0].Positions[0], got.Debates[0].Positions[1]
	if p0.Span == nil || *p0.Span != sp1 || p0.Over["s1"] != 7.8 || p0.SaidBy != "s1" || p0.Speaker != "s1" || p0.SpeakerUnsure {
		t.Errorf("p0 mismatch: %+v", p0)
	}
	if len(p0.Also) != 1 || p0.Also[0].At != 40.0 || p0.Also[0].Span == nil || *p0.Also[0].Span != spAlso || p0.Also[0].Speaker != "s3" {
		t.Errorf("p0.Also mismatch: %+v", p0.Also)
	}
	if p1.Span == nil || *p1.Span != sp2 || !p1.Est || p1.SaidBy != "s2" || p1.Speaker != "s2" || !p1.SpeakerUnsure {
		t.Errorf("p1 mismatch: %+v", p1)
	}

	// Lenient decoding: malformed speaker fields drop only the bad fields, never the position or minutes.
	badPayload := `{"title":"Sync","points":[{"text":"One","at":1}],"debates":[{"question":"Q","status":"open","at":10,"positions":[
		{"stance":"Keep me","at":10,"span":[20,10],"est":"yes","over":{"bad":3,"s1":-1,"s2":4.5},"said_by":"bad","speaker":"S1","speaker_unsure":"true",
		 "also":["not-obj",{"at":-5},{"at":30,"span":[30,35],"over":{"s3":4},"speaker":"s3"},{"at":40,"speaker":"s4"},{"at":50,"speaker":"s5"}]},
		{"stance":"Keep me too","at":20,"span":"bad","over":"bad","also":"bad"}
	]}]}`
	var lenient Minutes
	if err := json.Unmarshal([]byte(badPayload), &lenient); err != nil {
		t.Fatalf("lenient unmarshal failed: %v", err)
	}
	if len(lenient.Debates) != 1 || len(lenient.Debates[0].Positions) != 2 {
		t.Fatalf("lenient dropped valid positions: %+v", lenient.Debates)
	}
	lp0 := lenient.Debates[0].Positions[0]
	if lp0.Span != nil || lp0.Est || lp0.SaidBy != "" || lp0.Speaker != "" || lp0.SpeakerUnsure {
		t.Errorf("malformed fields were not dropped: %+v", lp0)
	}
	if len(lp0.Over) != 1 || lp0.Over["s2"] != 4.5 {
		t.Errorf("expected only valid over entry s2=4.5, got %+v", lp0.Over)
	}
	if len(lp0.Also) != 2 || lp0.Also[0].At != 30 || lp0.Also[0].Speaker != "s3" || lp0.Also[1].At != 40 || lp0.Also[1].Speaker != "s4" {
		t.Errorf("expected 2 valid also entries (capped at maxDebateAlso), got %+v", lp0.Also)
	}
}
