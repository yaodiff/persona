package main

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// normalize keeps letters and digits after NFKC and lower case: punctuation, spaces and symbols never count as errors.
func normalize(s string) string {
	s = norm.NFKC.String(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cer is the character error rate of hyp against ref: the edit distance of their normalized texts over ref's length.
func cer(ref, hyp string) float64 {
	a, b := []rune(normalize(ref)), []rune(normalize(hyp))
	if len(a) == 0 {
		if len(b) == 0 {
			return 0
		}
		return 1
	}
	return float64(editDistanceRunes(a, b, nil, nil)) / float64(len(a))
}

func editDistanceRunes(a, b []rune, prev, cur []int) int {
	if slices.Equal(a, b) {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	need := len(b) + 1
	if cap(prev) < need {
		prev = make([]int, need)
	} else {
		prev = prev[:need]
	}
	if cap(cur) < need {
		cur = make([]int, need)
	} else {
		cur = cur[:need]
	}
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		ai := a[i-1]
		for j := 1; j <= len(b); j++ {
			c := 1
			if ai == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// latinWords are the words in Latin script of a text, lower case, two letters or more.
func latinWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(norm.NFKC.String(s)), func(r rune) bool {
		return !(r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)))
	}) {
		if len(w) >= 2 {
			out = append(out, w)
		}
	}
	return out
}

// latinRecall is the share of the reference lines' Latin words found, still in Latin script, in hyp: whether English
// said inside Chinese (and English lines) came out as said rather than translated.
func latinRecall(refs []string, hyp string) float64 {
	h := strings.ToLower(norm.NFKC.String(hyp))
	n, found := 0, 0
	for _, r := range refs {
		for _, w := range latinWords(r) {
			n++
			if strings.Contains(h, w) {
				found++
			}
		}
	}
	if n == 0 {
		return 0
	}
	return float64(found) / float64(n)
}

// hanShare is the share of Han characters among the Han and Latin letters of a text: near 1 for Chinese prose with a
// few names, near 0 for English.
func hanShare(s string) float64 {
	han, latin := 0, 0
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case r < 128 && unicode.IsLetter(r):
			latin++
		}
	}
	if han+latin == 0 {
		return 0
	}
	return float64(han) / float64(han+latin)
}

// summary is n, min, p50, p90 and max of a sample; nil for none.
func summary(v []float64) map[string]float64 {
	if len(v) == 0 {
		return nil
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	q := func(p float64) float64 { return s[min(len(s)-1, int(p*float64(len(s))))] }
	return map[string]float64{"n": float64(len(s)), "min": s[0], "p50": q(0.5), "p90": q(0.9), "max": s[len(s)-1]}
}

const (
	alignSkipWeight    = 0.35
	alignGapOpen       = 0.01
	alignMergeTieBreak = 1e-4
	liveKeptTimeTol    = 0.01 + 1e-9
)

type hubSentence struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
	Echo  bool    `json:"echo,omitempty"`
}

type textGroup struct {
	ScriptIdx []int
	HubIdx    []int
}

type matchCounts struct {
	ScriptLines           int `json:"script_lines"`
	HubSentences          int `json:"hub_sentences"`
	MatchedGroups         int `json:"matched_groups"`
	MatchedScriptLines    int `json:"matched_script_lines"`
	MatchedHubSentences   int `json:"matched_hub_sentences"`
	UnmatchedScriptLines  int `json:"unmatched_script_lines"`
	UnmatchedHubSentences int `json:"unmatched_hub_sentences"`
}

type textMatchResult struct {
	Groups          []textGroup
	ScriptLines     int
	HubSentences    int
	MatchedScript   int
	UnmatchedScript int
	MatchedHub      int
	UnmatchedHub    int
}

func (m textMatchResult) counts() matchCounts {
	return matchCounts{
		ScriptLines:           m.ScriptLines,
		HubSentences:          m.HubSentences,
		MatchedGroups:         len(m.Groups),
		MatchedScriptLines:    m.MatchedScript,
		MatchedHubSentences:   m.MatchedHub,
		UnmatchedScriptLines:  m.UnmatchedScript,
		UnmatchedHubSentences: m.UnmatchedHub,
	}
}

type groupTiming struct {
	StartError     map[string]float64 `json:"start_error"`
	EndError       map[string]float64 `json:"end_error"`
	StartSignedP50 *float64           `json:"start_signed_p50"`
	EndSignedP50   *float64           `json:"end_signed_p50"`
}

type finalSplit struct {
	Replay   int `json:"replay"`
	LiveKept int `json:"live_kept"`
}

type timingReport struct {
	Match       map[string]matchCounts `json:"match"`
	Live        groupTiming            `json:"live"`
	Final       groupTiming            `json:"final"`
	FinalReplay groupTiming            `json:"final_replay"`
	FinalSplit  finalSplit             `json:"final_split"`
}

// parseScriptJSON decodes a script.json into a fresh slice so omitted fields keep their zero values.
func parseScriptJSON(b []byte) ([]line, error) {
	var lines []line
	if err := json.Unmarshal(b, &lines); err != nil {
		return nil, err
	}
	return lines, nil
}

// liveSentencesFromFrames extracts live "line" frames ordered stably by their sequence number n.
func liveSentencesFromFrames(frames []frame) []hubSentence {
	type item struct {
		n    float64
		sent hubSentence
	}
	var items []item
	for _, f := range frames {
		var msg struct {
			Type  string   `json:"type"`
			N     *float64 `json:"n"`
			Start float64  `json:"start"`
			End   float64  `json:"end"`
			Text  string   `json:"text"`
			Echo  bool     `json:"echo"`
		}
		if err := json.Unmarshal(f.Frame, &msg); err != nil || msg.Type != "line" {
			continue
		}
		seq := math.Inf(1)
		if msg.N != nil {
			seq = *msg.N
		}
		items = append(items, item{
			n:    seq,
			sent: hubSentence{Start: msg.Start, End: msg.End, Text: msg.Text, Echo: msg.Echo},
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].n < items[j].n
	})
	out := make([]hubSentence, len(items))
	for i, it := range items {
		out[i] = it.sent
	}
	return out
}

// alignByText monotonically aligns non-echo hub sentences to non-echo script lines by normalized text alone.
// Times are never read. Allowed match groups are 1:1, 1:2, 1:3, 2:1, 3:1, plus skips on either side.
func alignByText(script []line, hub []hubSentence) textMatchResult {
	var sIdx []int
	var sNorm [][]rune
	for i, l := range script {
		if l.Echo {
			continue
		}
		sIdx = append(sIdx, i)
		sNorm = append(sNorm, []rune(normalize(l.Text)))
	}
	var hIdx []int
	var hNorm [][]rune
	for j, h := range hub {
		if h.Echo {
			continue
		}
		hIdx = append(hIdx, j)
		hNorm = append(hNorm, []rune(normalize(h.Text)))
	}
	n, m := len(sIdx), len(hIdx)
	res := textMatchResult{
		ScriptLines:     n,
		HubSentences:    m,
		UnmatchedScript: n,
		UnmatchedHub:    m,
	}
	if n == 0 || m == 0 {
		return res
	}

	buildCat := func(norms [][]rune) ([][4][]rune, [][4]bool) {
		k := len(norms)
		cats := make([][4][]rune, k)
		ok := make([][4]bool, k)
		for i := 0; i < k; i++ {
			var buf []rune
			allNonEmpty := true
			for span := 1; span <= 3 && i+span <= k; span++ {
				part := norms[i+span-1]
				if len(part) == 0 {
					allNonEmpty = false
				}
				buf = append(buf, part...)
				cats[i][span] = append([]rune(nil), buf...)
				ok[i][span] = allNonEmpty
			}
		}
		return cats, ok
	}
	sCat, sOK := buildCat(sNorm)
	hCat, hOK := buildCat(hNorm)

	const (
		stMatch = 0
		stSkipS = 1
		stSkipH = 2
	)
	type step struct {
		prev uint8
		dm   uint8
		dk   uint8
	}
	cols := m + 1
	total := (n + 1) * cols * 3
	dp := make([]float64, total)
	for i := range dp {
		dp[i] = math.Inf(1)
	}
	back := make([]step, total)
	cell := func(i, j, st int) int { return (i*cols+j)*3 + st }
	dp[cell(0, 0, stMatch)] = 0

	matchPairs := [5][2]int{{1, 1}, {2, 1}, {3, 1}, {1, 2}, {1, 3}}
	var prevBuf, curBuf []int

	for i := 0; i <= n; i++ {
		for j := 0; j <= m; j++ {
			if i > 0 {
				sc := alignSkipWeight * float64(len(sNorm[i-1]))
				dst := cell(i, j, stSkipS)
				best := dp[dst]
				var bestBack step
				for ps := 0; ps < 3; ps++ {
					base := dp[cell(i-1, j, ps)]
					if math.IsInf(base, 1) {
						continue
					}
					c := base + sc
					if ps != stSkipS && j > 0 && j < m {
						c += alignGapOpen
					}
					if c < best {
						best = c
						bestBack = step{prev: uint8(ps), dm: 1, dk: 0}
					}
				}
				dp[dst] = best
				back[dst] = bestBack
			}
			if j > 0 {
				hc := alignSkipWeight * float64(len(hNorm[j-1]))
				dst := cell(i, j, stSkipH)
				best := dp[dst]
				var bestBack step
				for ps := 0; ps < 3; ps++ {
					base := dp[cell(i, j-1, ps)]
					if math.IsInf(base, 1) {
						continue
					}
					c := base + hc
					if ps != stSkipH && i > 0 && i < n {
						c += alignGapOpen
					}
					if c < best {
						best = c
						bestBack = step{prev: uint8(ps), dm: 0, dk: 1}
					}
				}
				dp[dst] = best
				back[dst] = bestBack
			}
			if i > 0 && j > 0 {
				dst := cell(i, j, stMatch)
				best := dp[dst]
				var bestBack step
				for _, pair := range matchPairs {
					dm, dk := pair[0], pair[1]
					if i < dm || j < dk {
						continue
					}
					if !sOK[i-dm][dm] || !hOK[j-dk][dk] {
						continue
					}
					base := math.Inf(1)
					bestPS := 0
					for ps := 0; ps < 3; ps++ {
						if v := dp[cell(i-dm, j-dk, ps)]; v < base {
							base = v
							bestPS = ps
						}
					}
					if math.IsInf(base, 1) {
						continue
					}
					a, b := sCat[i-dm][dm], hCat[j-dk][dk]
					tie := alignMergeTieBreak * float64(dm+dk-2)
					lb := len(a) - len(b)
					if lb < 0 {
						lb = -lb
					}
					if base+float64(lb)+tie >= best {
						continue
					}
					// cer(a, b) * len(a) == editDistanceRunes(a, b) since len(a) > 0.
					d := editDistanceRunes(a, b, prevBuf, curBuf)
					c := base + float64(d) + tie
					if c < best {
						best = c
						bestBack = step{prev: uint8(bestPS), dm: uint8(dm), dk: uint8(dk)}
					}
				}
				dp[dst] = best
				back[dst] = bestBack
			}
		}
	}

	bestSt := stMatch
	for st := 1; st < 3; st++ {
		if dp[cell(n, m, st)] < dp[cell(n, m, bestSt)] {
			bestSt = st
		}
	}

	var groups []textGroup
	ci, cj, st := n, m, bestSt
	for ci > 0 || cj > 0 {
		b := back[cell(ci, cj, st)]
		dm, dk := int(b.dm), int(b.dk)
		if dm == 0 && dk == 0 {
			break
		}
		if dm > 0 && dk > 0 {
			groups = append(groups, textGroup{
				ScriptIdx: append([]int(nil), sIdx[ci-dm:ci]...),
				HubIdx:    append([]int(nil), hIdx[cj-dk:cj]...),
			})
			res.MatchedScript += dm
			res.MatchedHub += dk
		}
		ci -= dm
		cj -= dk
		st = int(b.prev)
	}
	slices.Reverse(groups)
	res.Groups = groups
	res.UnmatchedScript = res.ScriptLines - res.MatchedScript
	res.UnmatchedHub = res.HubSentences - res.MatchedHub
	return res
}

// splitFinalSegments marks each non-echo final segment as live-kept when a non-echo live line frame has the same
// normalized text and start and end within 0.01 s; the remaining non-echo final segments are replay.
func splitFinalSegments(final, live []hubSentence) ([]bool, finalSplit) {
	type liveItem struct {
		norm  string
		start float64
		end   float64
	}
	byText := map[string][]liveItem{}
	for _, l := range live {
		if l.Echo {
			continue
		}
		k := normalize(l.Text)
		byText[k] = append(byText[k], liveItem{norm: k, start: l.Start, end: l.End})
	}
	mask := make([]bool, len(final))
	var split finalSplit
	for i, s := range final {
		if s.Echo {
			continue
		}
		k := normalize(s.Text)
		kept := false
		for _, l := range byText[k] {
			if math.Abs(l.start-s.Start) <= liveKeptTimeTol && math.Abs(l.end-s.End) <= liveKeptTimeTol {
				kept = true
				break
			}
		}
		if kept {
			mask[i] = true
			split.LiveKept++
		} else {
			split.Replay++
		}
	}
	return mask, split
}

// filterReplayMatch restricts final's text alignment to groups whose hub sentences were all written by the final pass.
func filterReplayMatch(finalMatch textMatchResult, liveKeptMask []bool, replayCount int) textMatchResult {
	res := textMatchResult{
		ScriptLines:  finalMatch.ScriptLines,
		HubSentences: replayCount,
	}
	for _, g := range finalMatch.Groups {
		allReplay := len(g.HubIdx) > 0
		for _, hi := range g.HubIdx {
			if hi < 0 || hi >= len(liveKeptMask) || liveKeptMask[hi] {
				allReplay = false
				break
			}
		}
		if !allReplay {
			continue
		}
		res.Groups = append(res.Groups, g)
		res.MatchedScript += len(g.ScriptIdx)
		res.MatchedHub += len(g.HubIdx)
	}
	res.UnmatchedScript = res.ScriptLines - res.MatchedScript
	res.UnmatchedHub = res.HubSentences - res.MatchedHub
	return res
}

func signedMedian(v []float64) *float64 {
	s := summary(v)
	if s == nil {
		return nil
	}
	p50 := cleanZero(s["p50"])
	return &p50
}

func computeGroupTiming(script []line, hub []hubSentence, groups []textGroup) groupTiming {
	if len(groups) == 0 {
		return groupTiming{}
	}
	absStart := make([]float64, 0, len(groups))
	absEnd := make([]float64, 0, len(groups))
	signedStart := make([]float64, 0, len(groups))
	signedEnd := make([]float64, 0, len(groups))
	for _, g := range groups {
		if len(g.ScriptIdx) == 0 || len(g.HubIdx) == 0 {
			continue
		}
		hFirst, hLast := hub[g.HubIdx[0]], hub[g.HubIdx[len(g.HubIdx)-1]]
		sFirst, sLast := script[g.ScriptIdx[0]], script[g.ScriptIdx[len(g.ScriptIdx)-1]]
		se := hFirst.Start - sFirst.Start
		ee := hLast.End - sLast.End
		signedStart = append(signedStart, se)
		signedEnd = append(signedEnd, ee)
		absStart = append(absStart, math.Abs(se))
		absEnd = append(absEnd, math.Abs(ee))
	}
	return groupTiming{
		StartError:     summary(absStart),
		EndError:       summary(absEnd),
		StartSignedP50: signedMedian(signedStart),
		EndSignedP50:   signedMedian(signedEnd),
	}
}
