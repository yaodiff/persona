package transcript

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// subagentTools are the calls that launch a subagent (Claude Code's Task, renamed Agent).
var subagentTools = map[string]bool{"Task": true, "Agent": true}

// A run of work shorter than this (and lighter than foldMinBytes) stays on the page: opening it would cost a round
// trip for a few hundred bytes.
const (
	foldMinItems             = 3
	foldMinBytes             = 4 * 1024
	foldThinkingPreviewChars = 512
)

// FoldWork replaces each run of the agent's work in a historical page — tool calls with their results and images,
// thoughts and Codex commentary — with one KindFold item that counts it (proto.Fold). The prompts, final replies and
// notices stay as they are. A chat shows that work folded into one line, yet a long Codex exchange made the phone's
// first page 1400 rows and a megabyte of it (docs/protocol/protocol.md "Folded work").
func FoldWork(items []proto.Item) []proto.Item {
	return foldWork(items, false)
}

// FoldWorkLatest is FoldWork for the latest page: its newest commentary may still be the running turn's current
// answer, so it stays visible until a final answer follows it.
func FoldWorkLatest(items []proto.Item) []proto.Item {
	return foldWork(items, true)
}

// FoldPage keeps Claude's public paragraphs in order on both initial and older pages.
// A call spanning one of these paragraphs and its later receipt stay available for pairing.
func (f *File) FoldPage(items []proto.Item, latest bool) []proto.Item {
	f.mu.Lock()
	codex := f.meta.Codex
	f.mu.Unlock()
	if codex {
		// Keep a public opening available when the user steers later in this turn,
		// including when the steer arrives on a later page.
		opening := make(map[int]bool)
		first := true
		for i, it := range items {
			if it.Kind == proto.KindText && it.Role == "user" || it.Kind == "turn_duration" {
				first = true
			} else if it.Kind == proto.KindText && it.Role == "assistant" && first {
				opening[i] = it.Narration
				first = false
			}
		}
		return foldWorkKeeping(items, latest, opening)
	}
	var out []proto.Item
	start := 0
	for i, item := range items {
		if item.Kind == proto.KindText && item.Role == "assistant" {
			if i > start {
				out = append(out, FoldWork(items[start:i])...)
			}
			out = append(out, item)
			start = i + 1
		}
	}
	return append(out, FoldWork(items[start:])...)
}

// A call whose result is not in the page (still running) and a result whose call is not (on an older page) are not
// folded: the app pairs them with what comes later or earlier, and a fold hides them from that.
func foldWork(items []proto.Item, latestPage bool) []proto.Item {
	return foldWorkKeeping(items, latestPage, nil)
}

func foldWorkKeeping(items []proto.Item, latestPage bool, opening map[int]bool) []proto.Item {
	results, agents := map[string]*proto.Item{}, map[string]bool{}
	taskUses := map[string]proto.Item{}
	for i := range items {
		it := &items[i]
		switch it.Kind {
		case proto.KindToolUse:
			taskUses[it.ToolUseID] = *it
			if subagentTools[it.ToolName] {
				agents[it.ToolUseID] = true
			}
		case proto.KindToolResult:
			results[it.ToolUseID] = it
		}
	}
	// The latest commentary is the turn's current answer until a final answer follows it. Keep it visible on a
	// running page; once a final answer exists, all earlier commentary belongs to the completed work.
	latestNarration := -1
	if latestPage {
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Role == "assistant" && items[i].Kind == proto.KindText {
				if items[i].Narration {
					latestNarration = i
				}
				break
			}
		}
	}
	inFold := func(index int) bool {
		it := items[index]
		switch it.Kind {
		case proto.KindThinking:
			return true
		case proto.KindText:
			return it.Narration && index != latestNarration && !opening[index]
		case proto.KindToolUse, proto.KindToolResult, proto.KindImage: // a tool's image goes with its call
			// a subagent's call stays on the page: the chat draws it as a card, which never folds (#5602)
			use, hasUse := taskUses[it.ToolUseID]
			result, hasResult := results[it.ToolUseID]
			// Keep the silent receipt and its call so even a partial history replay can respect the disposition.
			return it.ToolUseID != "" && hasUse && hasResult && !agents[it.ToolUseID] &&
				!silentSupervisionFinish(use.ToolName, *result) && use.ToolName != claudeDesktopWrapUp &&
				(use.ToolName != claudeDesktopPlan || taskObject(use.ToolInput)["items"] != nil)
		}
		return false
	}
	out := make([]proto.Item, 0, len(items))
	for i := 0; i < len(items); {
		if !inFold(i) {
			out = append(out, items[i])
			i++
			continue
		}
		j, size := i, 0
		for j < len(items) && inFold(j) {
			size += len(items[j].Text) + len(items[j].ToolInput)
			j++
		}
		if j-i < foldMinItems && size < foldMinBytes {
			out = append(out, items[i:j]...)
		} else {
			it := fold(items[i:j])
			it.Fold.Tasks = foldTaskSnapshots(items[i:j], taskUses)
			it.Fold.Images = ViewedImages(items[i:j], taskUses)
			it.Fold.ToolViews = FoldToolViews(items[i:j], taskUses)
			it.Fold.SupervisionUser = supervisionUserAction(items[i:j], taskUses)
			out = append(out, it)
		}
		i = j
	}
	return out
}

func fold(run []proto.Item) proto.Item {
	first, last := run[0], run[len(run)-1]
	fo := &proto.Fold{Items: len(run), Until: last.TS, ThroughSeq: last.Seq, ThroughID: last.ID, ToolNames: []string{}}
	results := map[string]*proto.Item{}
	for i := range run {
		if run[i].Kind == proto.KindToolResult && run[i].ToolUseID != "" {
			results[run[i].ToolUseID] = &run[i]
		}
	}
	latestThought, latestTool := -1, -1
	var firstTTFT *int64
	for i, it := range run {
		if firstTTFT == nil && it.TTFTMS != nil {
			firstTTFT = it.TTFTMS
		}
		switch {
		case it.Kind == proto.KindToolUse:
			latestTool = i
			fo.Tools++
			if it.ToolName != "" && len(fo.ToolNames) < proto.FoldToolNamesMax && !slices.Contains(fo.ToolNames, it.ToolName) {
				fo.ToolNames = append(fo.ToolNames, it.ToolName)
			}
			if it.Botmux != nil && botmuxToolResultSucceeded(results[it.ToolUseID]) {
				fo.Botmux = append(fo.Botmux, *it.Botmux)
			}
		case it.Kind == proto.KindToolResult && it.IsError:
			fo.Errors++
		case it.Kind == proto.KindThinking && strings.TrimSpace(it.Text) != "":
			fo.Thoughts++
			latestThought = i
		}
		if it.TS.After(fo.Until) {
			fo.Until = it.TS
		}
	}
	if latestThought >= 0 {
		fo.LatestThinking = thinkingPreview(run[latestThought])
	}
	fo.Changes = foldChanges(run)
	fo.Usage = foldUsage(run)
	fo.Sources = foldSources(run)
	item := proto.Item{ID: "fold:" + first.ID, Seq: first.Seq, TS: first.TS, Role: "assistant", Kind: proto.KindFold, Fold: fo, TTFTMS: proto.CloneTTFTMS(firstTTFT)}
	if latestTool >= 0 && run[latestTool].ToolName == "Bash" {
		// Keep the same public caption when the client replaces the calls with this range.
		var input struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(run[latestTool].ToolInput, &input) == nil {
			item.Text = oneLine(input.Description, 160)
		}
	}
	return item
}

func thinkingPreview(it proto.Item) *proto.Item {
	preview := &proto.Item{ID: it.ID, Seq: it.Seq, TS: it.TS, Role: it.Role, Kind: it.Kind, Text: it.Text, FullSize: it.FullSize}
	text := []rune(it.Text)
	if len(text) > foldThinkingPreviewChars {
		preview.Text = string(text[:foldThinkingPreviewChars-1]) + "…"
		preview.FullSize = max(preview.FullSize, len(it.Text))
	}
	return preview
}

// Range returns the items of lines fromSeq through throughSeq, at most about limit of them (whole lines); more says
// the range goes on after the last one. It is how a fold is opened.
func (f *File) Range(fromSeq, throughSeq, limit int) ([]proto.Item, bool) {
	items, more, _ := f.RangeSpan(fromSeq, throughSeq, limit)
	return items, more
}

// RangeSpan returns the items of lines fromSeq through throughSeq, at most about limit of them (whole lines),
// whether more lines remain in the range, and the transcript span covering the returned lines.
func (f *File) RangeSpan(fromSeq, throughSeq, limit int) ([]proto.Item, bool, Span) {
	f.mu.Lock()
	defer f.mu.Unlock()
	from := max(0, fromSeq-1)
	f.ensureAfter(from)
	if f.base > from {
		from = f.base
	}
	i := sort.Search(len(f.items), func(k int) bool { return f.items[k].Seq >= fromSeq })
	end := i
	for end < len(f.items) && f.items[end].Seq <= throughSeq && end-i < limit {
		end++
	}
	for end > i && end < len(f.items) && f.items[end].Seq == f.items[end-1].Seq { // never split a line
		end++
	}
	more := end < len(f.items) && f.items[end].Seq <= throughSeq
	to := min(f.lines, max(0, throughSeq))
	if more && end > i {
		to = f.items[end-1].Seq
	} else if end > i {
		to = max(to, f.items[end-1].Seq)
	}
	if to < from {
		from = to
	}
	return append([]proto.Item(nil), f.items[i:end]...), more, Span{History: f.history(), From: from, To: to}
}

// TurnWindow returns the surrounding items in f needed to evaluate full turn boundaries for a slice spanning
// [firstSeq, lastSeq] (such as a fold Range, an AfterSeq batch, a live push, or a mid-turn Page):
//   - prefix: items from the enclosing user text item up to (not including) firstSeq
//   - suffix: items after lastSeq up to (not including) the next user text item
//
// Each side is bounded to avoid unbounded scans on pathological turns.
func (f *File) TurnWindow(firstSeq, lastSeq int) (prefix, suffix []proto.Item) {
	if f == nil || firstSeq <= 0 || lastSeq <= 0 {
		return nil, nil
	}
	const maxContextItems = 2000
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureAfter(firstSeq - 1)
	startIdx := sort.Search(len(f.items), func(k int) bool { return f.items[k].Seq >= firstSeq })
	// If the slice does not start on a user text item, ensure the enclosing user turn is loaded.
	if startIdx >= len(f.items) || !isBotmuxUserItem(f.items[startIdx]) {
		hasUserBefore := false
		for p := startIdx - 1; p >= 0 && startIdx-p <= maxContextItems; p-- {
			if isBotmuxUserItem(f.items[p]) {
				hasUserBefore = true
				break
			}
		}
		if !hasUserBefore && f.base > 0 {
			f.ensureTurns(firstSeq, 1, maxContextItems)
			startIdx = sort.Search(len(f.items), func(k int) bool { return f.items[k].Seq >= firstSeq })
		}
		// ensureTurns may parse back far more than maxContextItems (a whole page budget), so the prefix is cut here:
		// the enclosing user item if it is close enough, else only the newest maxContextItems items before firstSeq.
		lo := max(0, startIdx-maxContextItems)
		for p := startIdx - 1; p >= lo; p-- {
			if isBotmuxUserItem(f.items[p]) {
				lo = p
				break
			}
		}
		if lo < startIdx {
			prefix = append([]proto.Item(nil), f.items[lo:startIdx]...)
		}
	}
	endIdx := sort.Search(len(f.items), func(k int) bool { return f.items[k].Seq > lastSeq })
	q := endIdx
	for q < len(f.items) && q-endIdx < maxContextItems && !isBotmuxUserItem(f.items[q]) {
		q++
	}
	if q > endIdx {
		suffix = append([]proto.Item(nil), f.items[endIdx:q]...)
	}
	return prefix, suffix
}

func isBotmuxUserItem(it proto.Item) bool {
	return it.Role == "user" && it.Kind == proto.KindText && it.From == ""
}
