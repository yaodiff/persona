package recordingspeakersadapt

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/domainkit/domaintest"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/recordingspeakers"
)

const (
	alignFixturesPath = "../../../../spec/fixtures/speakers-align/cases.json"
	namesFixturesPath = "../../../../spec/fixtures/speakers-names/cases.json"
)

type turnTriple [3]json.RawMessage

func decodeTurnTriple(t *testing.T, raw turnTriple) (float64, float64, string) {
	t.Helper()
	var start, end float64
	var id string
	if err := json.Unmarshal(raw[0], &start); err != nil {
		t.Fatalf("turn start: %v", err)
	}
	if err := json.Unmarshal(raw[1], &end); err != nil {
		t.Fatalf("turn end: %v", err)
	}
	if err := json.Unmarshal(raw[2], &id); err != nil {
		t.Fatalf("turn id: %v", err)
	}
	return start, end, id
}

type decodedTurn struct {
	start float64
	end   float64
	id    string
}

type segmentFixture struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Source string  `json:"source"`
	Echo   bool    `json:"echo"`
	Text   string  `json:"text"`
}

type alignResultFixture struct {
	Over          map[string]float64 `json:"over"`
	Speaker       string             `json:"speaker,omitempty"`
	SpeakerUnsure bool               `json:"speaker_unsure,omitempty"`
	Skip          string             `json:"skip,omitempty"`
}

type alignCase struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Input       struct {
		RawTurns  []turnTriple     `json:"raw_turns"`
		PrevTurns []turnTriple     `json:"prev_turns"`
		Turns     []turnTriple     `json:"turns"`
		Covered   [][2]float64     `json:"covered"`
		Duration  float64          `json:"duration"`
		Length    float64          `json:"length"`
		Timing    string           `json:"timing"`
		Quality   string           `json:"quality"`
		Attendees int              `json:"attendees"`
		Next      int              `json:"next"`
		People    []personRecord   `json:"people"`
		Segments  []segmentFixture `json:"segments"`
	} `json:"input"`
	Expected struct {
		Turns    []turnTriple         `json:"turns"`
		Next     int                  `json:"next"`
		People   []personRecord       `json:"people"`
		Results  []alignResultFixture `json:"results"`
		Original map[string][2]any    `json:"original"`
		Merged   map[string][2]any    `json:"merged"`
		MinorIDs []string             `json:"minor_ids"`
		Quality  string               `json:"quality"`
		Reason   string               `json:"reason"`
	} `json:"expected"`
}

type displayWire struct {
	Kind   string `json:"kind"`
	Value  string `json:"value,omitempty"`
	Number int    `json:"number,omitempty"`
}

type claimWire struct {
	Speaker       string `json:"speaker,omitempty"`
	SpeakerUnsure bool   `json:"speaker_unsure,omitempty"`
	Who           string `json:"who,omitempty"`
}

type namesCase struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Input       struct {
		Next     int               `json:"next"`
		People   []personRecord    `json:"people"`
		Stats    map[string][2]any `json:"stats"`
		Edits    editsRecord       `json:"edits"`
		Ops      []editOpWire      `json:"ops"`
		Position struct {
			Speaker       string      `json:"speaker,omitempty"`
			SpeakerUnsure bool        `json:"speaker_unsure,omitempty"`
			Who           string      `json:"who,omitempty"`
			Also          []claimWire `json:"also,omitempty"`
		} `json:"position"`
	} `json:"input"`
	Expected struct {
		Displays            []displayWire  `json:"displays"`
		People              []personRecord `json:"people"`
		Edits               editsRecord    `json:"edits"`
		Undo                []editOpWire   `json:"undo"`
		VerifyUndoRoundtrip bool           `json:"verify_undo_roundtrip"`
		Error               string         `json:"error"`
		ErrorCode           string         `json:"error_code"`
	} `json:"expected"`
}

func TestSpeakersAlignFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(alignFixturesPath))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int         `json:"version"`
		Cases   []alignCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || len(file.Cases) == 0 {
		t.Fatalf("speakers-align fixtures: version=%d cases=%d err=%v", file.Version, len(file.Cases), err)
	}
	seen := make(map[string]bool, len(file.Cases))
	for _, c := range file.Cases {
		if c.Name == "" || seen[c.Name] {
			t.Fatalf("duplicate or empty case name %q", c.Name)
		}
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) {
			switch c.Kind {
			case "number":
				rawTurns := buildRawTurns(t, c.Input.RawTurns)
				res := recordingspeakers.Number(rawTurns)
				gotTurns, gotRoster := extractNumbered(res)
				assertTurnsEqual(t, gotTurns, decodeTurns(t, c.Expected.Turns))
				assertRosterMatches(t, Store(gotRoster), c.Expected.Next, c.Expected.People)
			case "pair":
				prev := buildTurns(t, c.Input.PrevTurns)
				roster := domaintest.Ok(t, Restore(Record{Next: c.Input.Next, People: c.Input.People}))
				nextRaw := buildRawTurns(t, c.Input.RawTurns)
				res := recordingspeakers.Pair(prev, roster, nextRaw)
				gotTurns, gotRoster := extractNumbered(res)
				assertTurnsEqual(t, gotTurns, decodeTurns(t, c.Expected.Turns))
				assertRosterMatches(t, Store(gotRoster), c.Expected.Next, c.Expected.People)
			case "trim":
				turns := buildTurns(t, c.Input.Turns)
				length := domaintest.Ok(t, recordingspeakers.ParseSeconds(c.Input.Length))
				got := extractTurns(recordingspeakers.TrimTurns(turns, length))
				assertTurnsEqual(t, got, decodeTurns(t, c.Expected.Turns))
			case "align":
				turns := buildTurns(t, c.Input.Turns)
				cov := buildCovered(t, c.Input.Covered)
				timing := parseTimingWord(c.Input.Timing)
				quality := parseQualityWord(c.Input.Quality)
				roster := domaintest.Ok(t, Restore(Record{Next: c.Input.Next, People: c.Input.People}))
				env := domaintest.Ok(t, recordingspeakers.NewAlignEnv(turns, cov, timing))
				segs := buildSegments(t, c.Input.Segments)
				if len(segs) != len(c.Expected.Results) {
					t.Fatalf("segments=%d results=%d", len(segs), len(c.Expected.Results))
				}
				for i, seg := range segs {
					got := extractAlignment(recordingspeakers.AlignLine(seg, env, roster, quality))
					want := c.Expected.Results[i]
					assertAlignmentEqual(t, i, got, want)
				}
			case "stats":
				turns := buildTurns(t, c.Input.Turns)
				cov := buildCovered(t, c.Input.Covered)
				timing := parseTimingWord(c.Input.Timing)
				roster := domaintest.Ok(t, Restore(Record{Next: c.Input.Next, People: c.Input.People}))
				segs := recordingspeakers.NewSegments(buildSegments(t, c.Input.Segments))
				orig := recordingspeakers.ComputeStats(turns, segs, timing, cov)
				merged := recordingspeakers.SumStats(orig, roster)
				marked := recordingspeakers.MarkMinor(orig, roster)
				assertStatsEqual(t, "original", extractStats(orig), decodeStatsMap(t, c.Expected.Original))
				assertStatsEqual(t, "merged", extractStats(merged), decodeStatsMap(t, c.Expected.Merged))
				var gotMinors []string
				for _, p := range Store(marked).People {
					if p.Minor {
						gotMinors = append(gotMinors, p.ID)
					}
				}
				if !slices.Equal(gotMinors, c.Expected.MinorIDs) {
					t.Errorf("minor_ids = %v, want %v", gotMinors, c.Expected.MinorIDs)
				}
			case "quality":
				turns := buildTurns(t, c.Input.Turns)
				cov := buildCovered(t, c.Input.Covered)
				timing := parseTimingWord(c.Input.Timing)
				roster := domaintest.Ok(t, Restore(Record{Next: c.Input.Next, People: c.Input.People}))
				env := domaintest.Ok(t, recordingspeakers.NewAlignEnv(turns, cov, timing))
				segs := recordingspeakers.NewSegments(buildSegments(t, c.Input.Segments))
				labels := recordingspeakers.AlignSegments(segs, env, roster, recordingspeakers.QualityOk())
				stats := recordingspeakers.ComputeStats(turns, segs, timing, cov)
				dur := domaintest.Ok(t, recordingspeakers.ParseSeconds(c.Input.Duration))
				att := domaintest.Ok(t, recordingspeakers.ParseAttendeeCount(c.Input.Attendees))
				qi := domaintest.Ok(t, recordingspeakers.NewQualityInputBuilder(segs, labels).
					Coverage(cov, dur).
					Context(stats, roster, att).
					Build())
				gotQ, gotWhy := extractQuality(recordingspeakers.EvaluateQuality(qi))
				if gotQ != c.Expected.Quality || gotWhy != c.Expected.Reason {
					t.Errorf("quality = (%q, %q), want (%q, %q)", gotQ, gotWhy, c.Expected.Quality, c.Expected.Reason)
				}
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}
		})
	}
}

func TestSpeakersNamesFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(namesFixturesPath))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int         `json:"version"`
		Cases   []namesCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || len(file.Cases) == 0 {
		t.Fatalf("speakers-names fixtures: version=%d cases=%d err=%v", file.Version, len(file.Cases), err)
	}
	seen := make(map[string]bool, len(file.Cases))
	for _, c := range file.Cases {
		if c.Name == "" || seen[c.Name] {
			t.Fatalf("duplicate or empty case name %q", c.Name)
		}
		seen[c.Name] = true
		t.Run(c.Name, func(t *testing.T) {
			switch c.Kind {
			case "display":
				nextVal := c.Input.Next
				if nextVal == 0 {
					nextVal = maxNextForPeople(c.Input.People)
				}
				roster := domaintest.Ok(t, Restore(Record{Next: nextVal, People: c.Input.People}))
				mainClaim := buildClaim(t, claimWire{
					Speaker:       c.Input.Position.Speaker,
					SpeakerUnsure: c.Input.Position.SpeakerUnsure,
					Who:           c.Input.Position.Who,
				})
				var alsoList []recordingspeakers.Claim
				for _, ac := range c.Input.Position.Also {
					alsoList = append(alsoList, buildClaim(t, ac))
				}
				displays := recordingspeakers.ResolvePositionDisplay(mainClaim, recordingspeakers.NewAlsoClaims(alsoList), roster)
				got := extractDisplays(displays)
				if len(got) == 0 && len(c.Expected.Displays) == 0 {
					// Also verify ResolveDisplay matches when there is no also.
					if len(alsoList) == 0 {
						single := extractDisplay(recordingspeakers.ResolveDisplay(mainClaim, roster))
						if single.Kind != "none" {
							t.Errorf("ResolveDisplay = %+v, want none", single)
						}
					}
					return
				}
				if !reflect.DeepEqual(got, c.Expected.Displays) {
					t.Errorf("displays = %+v, want %+v", got, c.Expected.Displays)
				}
				if len(alsoList) == 0 && len(got) == 1 {
					single := extractDisplay(recordingspeakers.ResolveDisplay(mainClaim, roster))
					if single != got[0] {
						t.Errorf("ResolveDisplay = %+v, want %+v", single, got[0])
					}
				}
			case "edit":
				roster := domaintest.Ok(t, Restore(Record{
					Next:   c.Input.Next,
					People: c.Input.People,
					Edits:  c.Input.Edits,
				}))
				stats := buildStats(t, c.Input.Stats)
				ops, parseFail := outcome.Unpack(parseEditOps(c.Input.Ops))
				if c.Expected.Error != "" {
					var f fail.Fail = parseFail
					if f == nil {
						_, ef := outcome.Unpack(recordingspeakers.Edit(roster, stats, ops))
						f = ef
					}
					if f == nil {
						t.Fatalf("expected error %s/%s, got success", c.Expected.Error, c.Expected.ErrorCode)
					}
					gotKind, gotCode := extractFail(f)
					if gotKind != c.Expected.Error || gotCode != c.Expected.ErrorCode {
						t.Errorf("error = (%q, %q), want (%q, %q)", gotKind, gotCode, c.Expected.Error, c.Expected.ErrorCode)
					}
					return
				}
				if parseFail != nil {
					t.Fatalf("unexpected parse failure: %s", domaintest.Describe(parseFail))
				}
				edited := domaintest.Ok(t, recordingspeakers.Edit(roster, stats, ops))
				gotRoster, gotUndo := extractEdited(edited)
				gotRec := Store(gotRoster)
				assertPeopleEqual(t, gotRec.People, c.Expected.People)
				if gotRec.Edits != c.Expected.Edits {
					t.Errorf("edits = %+v, want %+v", gotRec.Edits, c.Expected.Edits)
				}
				if len(gotUndo) == 0 && len(c.Expected.Undo) == 0 {
					// both empty
				} else if !reflect.DeepEqual(gotUndo, c.Expected.Undo) {
					t.Errorf("undo = %+v, want %+v", gotUndo, c.Expected.Undo)
				}
				if c.Expected.VerifyUndoRoundtrip {
					if len(gotUndo) == 0 {
						t.Fatalf("verify_undo_roundtrip requested but undo is empty")
					}
					undoOps := domaintest.Ok(t, parseEditOps(gotUndo))
					undone := domaintest.Ok(t, recordingspeakers.Edit(gotRoster, stats, undoOps))
					restoredRoster, _ := extractEdited(undone)
					assertPeopleEqual(t, Store(restoredRoster).People, c.Input.People)
				}
			default:
				t.Fatalf("unknown kind %q", c.Kind)
			}
		})
	}
}

// --- Fixture conversion & assertion helpers ---

func maxNextForPeople(people []personRecord) int {
	maxN := 0
	var zeroID recordingspeakers.SpeakerID
	for _, p := range people {
		var id recordingspeakers.SpeakerID
		if parsed, f := outcome.Unpack(recordingspeakers.ParseSpeakerID(p.ID)); f == nil {
			id = parsed
		}
		if id != zeroID {
			if n := recordingspeakers.SpeakerNumberOf(id).Wire(); n > maxN {
				maxN = n
			}
		}
	}
	return maxN + 1
}

func buildRawTurns(t *testing.T, in []turnTriple) recordingspeakers.RawTurns {
	t.Helper()
	list := make([]recordingspeakers.RawTurn, 0, len(in))
	for _, raw := range in {
		s, e, cid := decodeTurnTriple(t, raw)
		sp := domaintest.Ok(t, recordingspeakers.NewSpan(
			domaintest.Ok(t, recordingspeakers.ParseSeconds(s)),
			domaintest.Ok(t, recordingspeakers.ParseSeconds(e)),
		))
		c := domaintest.Ok(t, recordingspeakers.ParseClusterID(cid))
		list = append(list, domaintest.Ok(t, recordingspeakers.NewRawTurn(sp, c)))
	}
	return recordingspeakers.NewRawTurns(list)
}

func buildTurns(t *testing.T, in []turnTriple) recordingspeakers.Turns {
	t.Helper()
	list := make([]recordingspeakers.Turn, 0, len(in))
	for _, raw := range in {
		s, e, sid := decodeTurnTriple(t, raw)
		sp := domaintest.Ok(t, recordingspeakers.NewSpan(
			domaintest.Ok(t, recordingspeakers.ParseSeconds(s)),
			domaintest.Ok(t, recordingspeakers.ParseSeconds(e)),
		))
		id := domaintest.Ok(t, recordingspeakers.ParseSpeakerID(sid))
		list = append(list, domaintest.Ok(t, recordingspeakers.NewTurn(sp, id)))
	}
	return recordingspeakers.NewTurns(list)
}

func decodeTurns(t *testing.T, in []turnTriple) []decodedTurn {
	t.Helper()
	out := make([]decodedTurn, 0, len(in))
	for _, raw := range in {
		s, e, id := decodeTurnTriple(t, raw)
		out = append(out, decodedTurn{start: s, end: e, id: id})
	}
	return out
}

func buildCovered(t *testing.T, in [][2]float64) recordingspeakers.Covered {
	t.Helper()
	spans := make([]recordingspeakers.Span, 0, len(in))
	for _, p := range in {
		spans = append(spans, domaintest.Ok(t, recordingspeakers.NewSpan(
			domaintest.Ok(t, recordingspeakers.ParseSeconds(p[0])),
			domaintest.Ok(t, recordingspeakers.ParseSeconds(p[1])),
		)))
	}
	return domaintest.Ok(t, recordingspeakers.NewCovered(spans))
}

func buildSegments(t *testing.T, in []segmentFixture) []recordingspeakers.Segment {
	t.Helper()
	out := make([]recordingspeakers.Segment, 0, len(in))
	for _, s := range in {
		sp := domaintest.Ok(t, recordingspeakers.NewSpan(
			domaintest.Ok(t, recordingspeakers.ParseSeconds(s.Start)),
			domaintest.Ok(t, recordingspeakers.ParseSeconds(s.End)),
		))
		echo := recordingspeakers.EchoSpeech()
		if s.Echo {
			echo = recordingspeakers.EchoPersona()
		}
		out = append(out, domaintest.Ok(t, recordingspeakers.NewSegment(
			sp, parseSourceWord(s.Source), echo, recordingspeakers.ParseLineText(s.Text),
		)))
	}
	return out
}

func buildStats(t *testing.T, in map[string][2]any) recordingspeakers.Stats {
	t.Helper()
	var list []recordingspeakers.SpeakerStat
	for k, v := range in {
		id := domaintest.Ok(t, recordingspeakers.ParseSpeakerID(k))
		sec := domaintest.Ok(t, recordingspeakers.ParseSeconds(v[0].(float64)))
		lines := domaintest.Ok(t, recordingspeakers.ParseLineCount(int(v[1].(float64))))
		list = append(list, domaintest.Ok(t, recordingspeakers.NewSpeakerStat(id, sec, lines)))
	}
	return domaintest.Ok(t, recordingspeakers.NewStats(list))
}

func buildClaim(t *testing.T, cw claimWire) recordingspeakers.Claim {
	t.Helper()
	spk := opt.None[recordingspeakers.SpeakerID]()
	cert := recordingspeakers.CertaintyUnsure()
	if cw.Speaker != "" {
		spk = opt.Some(domaintest.Ok(t, recordingspeakers.ParseSpeakerID(cw.Speaker)))
		if !cw.SpeakerUnsure {
			cert = recordingspeakers.CertaintySure()
		}
	}
	who := opt.None[recordingspeakers.SpeakerName]()
	if cw.Who != "" {
		who = opt.Some(domaintest.Ok(t, recordingspeakers.ParseSpeakerName(cw.Who)))
	}
	return domaintest.Ok(t, recordingspeakers.NewClaim(spk, cert, who))
}

// --- Visitors to read domain results in tests ---

type numberedReader struct {
	turns  []decodedTurn
	roster recordingspeakers.Roster
}

func (r *numberedReader) VisitNumbered(turns recordingspeakers.Turns, roster recordingspeakers.Roster) {
	r.turns = extractTurns(turns)
	r.roster = roster
}

func extractNumbered(n recordingspeakers.Numbered) ([]decodedTurn, recordingspeakers.Roster) {
	var r numberedReader
	n.Accept(&r)
	return r.turns, r.roster
}

type turnsReader struct {
	out  []decodedTurn
	turn turnReader
}

func (r *turnsReader) VisitTurn(t recordingspeakers.Turn) {
	t.Accept(&r.turn)
	r.out = append(r.out, r.turn.out)
}

type turnReader struct {
	out  decodedTurn
	span spanReader
}

func (r *turnReader) VisitTurn(span recordingspeakers.Span, speaker recordingspeakers.SpeakerID) {
	span.Accept(&r.span)
	r.out = decodedTurn{start: r.span.start, end: r.span.end, id: speaker.Wire()}
}

type spanReader struct {
	start float64
	end   float64
}

func (r *spanReader) VisitSpan(start, end recordingspeakers.Seconds) {
	r.start = start.Wire()
	r.end = end.Wire()
}

func extractTurns(turns recordingspeakers.Turns) []decodedTurn {
	var r turnsReader
	turns.Accept(&r)
	return r.out
}

type alignmentReader struct {
	out   alignResultFixture
	overs overlapsReader
	label labelReader
}

func (r *alignmentReader) VisitLineAlignment(overlaps recordingspeakers.Overlaps, label recordingspeakers.LineLabel) {
	r.overs.out = map[string]float64{}
	overlaps.Accept(&r.overs)
	r.label = labelReader{}
	label.Accept(&r.label)
	r.out = alignResultFixture{
		Over:          r.overs.out,
		Speaker:       r.label.speaker,
		SpeakerUnsure: r.label.unsure,
		Skip:          r.label.skip,
	}
}

type overlapsReader struct {
	out  map[string]float64
	item overlapItemReader
}

func (r *overlapsReader) VisitOverlap(o recordingspeakers.Overlap) {
	o.Accept(&r.item)
	r.out[r.item.id] = r.item.sec
}

type overlapItemReader struct {
	id  string
	sec float64
}

func (r *overlapItemReader) VisitOverlap(speaker recordingspeakers.SpeakerID, seconds recordingspeakers.Seconds) {
	r.id = speaker.Wire()
	r.sec = seconds.Wire()
}

type labelReader struct {
	speaker string
	unsure  bool
	skip    string
	cert    certaintyBool
	skipW   skipReasonWord
}

func (r *labelReader) VisitAssigned(speaker recordingspeakers.SpeakerID, certainty recordingspeakers.Certainty) {
	r.speaker = speaker.Wire()
	certainty.Accept(&r.cert)
	r.unsure = r.cert.unsure
}

func (r *labelReader) VisitSkipped(reason recordingspeakers.SkipReason) {
	reason.Accept(&r.skipW)
	r.skip = r.skipW.word
}

func extractAlignment(a recordingspeakers.LineAlignment) alignResultFixture {
	var r alignmentReader
	a.Accept(&r)
	return r.out
}

type statVal struct {
	seconds float64
	lines   int
}

type statsReader struct {
	out  map[string]statVal
	item statItemReader
}

func (r *statsReader) VisitSpeakerStat(st recordingspeakers.SpeakerStat) {
	st.Accept(&r.item)
	r.out[r.item.id] = statVal{seconds: r.item.sec, lines: r.item.lines}
}

type statItemReader struct {
	id    string
	sec   float64
	lines int
}

func (r *statItemReader) VisitSpeakerStat(speaker recordingspeakers.SpeakerID, seconds recordingspeakers.Seconds, lines recordingspeakers.LineCount) {
	r.id = speaker.Wire()
	r.sec = seconds.Wire()
	r.lines = lines.Wire()
}

func extractStats(s recordingspeakers.Stats) map[string]statVal {
	r := statsReader{out: map[string]statVal{}}
	s.Accept(&r)
	return r.out
}

func decodeStatsMap(t *testing.T, raw map[string][2]any) map[string]statVal {
	t.Helper()
	out := make(map[string]statVal, len(raw))
	for k, v := range raw {
		out[k] = statVal{seconds: v[0].(float64), lines: int(v[1].(float64))}
	}
	return out
}

type qualityVerdictReader struct {
	quality string
	reason  string
	qw      qualityWord
	rw      qualityReasonWord
}

func (r *qualityVerdictReader) VisitQualityVerdict(q recordingspeakers.Quality, reason opt.Option[recordingspeakers.QualityReason]) {
	q.Accept(&r.qw)
	r.quality = r.qw.word
	reason.Match(func(qr recordingspeakers.QualityReason) {
		qr.Accept(&r.rw)
		r.reason = r.rw.word
	}, func() {})
}

func extractQuality(v recordingspeakers.QualityVerdict) (string, string) {
	var r qualityVerdictReader
	v.Accept(&r)
	return r.quality, r.reason
}

type displaysReader struct {
	out  []displayWire
	item displayReader
}

func (r *displaysReader) VisitDisplay(d recordingspeakers.Display) {
	r.item.out = displayWire{}
	d.Accept(&r.item)
	r.out = append(r.out, r.item.out)
}

type displayReader struct{ out displayWire }

func (r *displayReader) VisitNamed(name recordingspeakers.SpeakerName) {
	r.out = displayWire{Kind: "name", Value: name.Wire()}
}

func (r *displayReader) VisitNumbered(num recordingspeakers.SpeakerNumber) {
	r.out = displayWire{Kind: "numbered", Number: num.Wire()}
}

func (r *displayReader) VisitWho(who recordingspeakers.SpeakerName) {
	r.out = displayWire{Kind: "who", Value: who.Wire()}
}

func (r *displayReader) VisitNone() {
	r.out = displayWire{Kind: "none"}
}

func extractDisplay(d recordingspeakers.Display) displayWire {
	var r displayReader
	d.Accept(&r)
	return r.out
}

func extractDisplays(ds recordingspeakers.Displays) []displayWire {
	var r displaysReader
	ds.Accept(&r)
	return r.out
}

type editedReader struct {
	roster recordingspeakers.Roster
	undo   []editOpWire
	ops    editOpsRecorder
}

func (r *editedReader) VisitEdited(roster recordingspeakers.Roster, undo recordingspeakers.EditOps) {
	r.roster = roster
	r.ops.ops = nil
	undo.Accept(&r.ops)
	r.undo = r.ops.ops
}

func extractEdited(e recordingspeakers.Edited) (recordingspeakers.Roster, []editOpWire) {
	var r editedReader
	e.Accept(&r)
	return r.roster, r.undo
}

type failReader struct {
	kind string
	code string
}

func (r *failReader) VisitInvalid(code fail.Code, _ fail.Part) {
	if r.kind == "" {
		r.kind, r.code = "invalid", code.Wire()
	}
}

func (r *failReader) VisitRefused(code fail.Code)  { r.kind, r.code = "refused", code.Wire() }
func (r *failReader) VisitMissing(code fail.Code)  { r.kind, r.code = "missing", code.Wire() }
func (r *failReader) VisitConflict(code fail.Code) { r.kind, r.code = "conflict", code.Wire() }

func extractFail(f fail.Fail) (string, string) {
	var r failReader
	f.Accept(&r)
	return r.kind, r.code
}

// --- Assertions ---

func assertTurnsEqual(t *testing.T, got, want []decodedTurn) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("turns len = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range got {
		if got[i].id != want[i].id || math.Abs(got[i].start-want[i].start) > 1e-6 || math.Abs(got[i].end-want[i].end) > 1e-6 {
			t.Errorf("turn[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func assertRosterMatches(t *testing.T, got Record, wantNext int, wantPeople []personRecord) {
	t.Helper()
	if got.Next != wantNext {
		t.Errorf("next = %d, want %d", got.Next, wantNext)
	}
	assertPeopleEqual(t, got.People, wantPeople)
}

func assertPeopleEqual(t *testing.T, got, want []personRecord) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("people =\n  %#v\nwant\n  %#v", got, want)
	}
}

func assertAlignmentEqual(t *testing.T, idx int, got, want alignResultFixture) {
	t.Helper()
	if got.Speaker != want.Speaker || got.SpeakerUnsure != want.SpeakerUnsure || got.Skip != want.Skip {
		t.Errorf("segment[%d] label = (speaker=%q unsure=%v skip=%q), want (speaker=%q unsure=%v skip=%q)",
			idx, got.Speaker, got.SpeakerUnsure, got.Skip, want.Speaker, want.SpeakerUnsure, want.Skip)
	}
	if len(got.Over) != len(want.Over) {
		t.Errorf("segment[%d] over = %v, want %v", idx, got.Over, want.Over)
		return
	}
	for k, wv := range want.Over {
		gv, ok := got.Over[k]
		if !ok || math.Abs(gv-wv) > 1e-6 {
			t.Errorf("segment[%d] over[%s] = %v, want %v", idx, k, gv, wv)
		}
	}
}

func assertStatsEqual(t *testing.T, label string, got, want map[string]statVal) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s stats = %v, want %v", label, got, want)
		return
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok || gv.lines != wv.lines || math.Abs(gv.seconds-wv.seconds) > 1e-6 {
			t.Errorf("%s stats[%s] = %+v, want %+v", label, k, gv, wv)
		}
	}
}
