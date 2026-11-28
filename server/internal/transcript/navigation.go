package transcript

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// coldInputStructure covers the prefix before the first item window. It keeps membership and branch topology without
// loading old chat bodies into the page cache. The topology is installed in File.tree so both reads use the same history.
type coldInputStructure struct {
	inputs []inputGroup
}

// CompactUserInputs counts all retained inputs, parsing the older part of a cold file separately from its chat pages.
func (f *File) CompactUserInputs(ctx context.Context, before int) (proto.UserInputIndex, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureColdInputStructure(ctx); err != nil {
		return proto.UserInputIndex{}, err
	}
	var compact CompactInputIndex
	if f.coldInputs != nil {
		for _, g := range f.coldInputs.inputs {
			if g.position.Seq > f.base || (before > 0 && g.position.Seq >= before) {
				break
			}
			if !f.tree.hidden[uuidOf(g.position.ID)] {
				compact.add(g)
			}
		}
	}
	compact.Add(f.items[:f.before(before)])
	return compact.Index(f.history()), nil
}

// InputPreviews reads only the requested inputs and their bounded reply windows.
func (f *File) InputPreviews(ctx context.Context, p proto.InputPreviewRequest) (proto.InputPreviews, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureColdInputStructure(ctx); err != nil {
		return proto.InputPreviews{}, err
	}
	return ReadInputPreviews(ctx, p, f.history(), func(from, through int) ([]proto.Item, error) {
		return f.navigationItems(ctx, from, through)
	})
}

// ReadInputPreviews uses one bounded source read for each distinct requested sequence.
func ReadInputPreviews(ctx context.Context, p proto.InputPreviewRequest, history string, read func(int, int) ([]proto.Item, error)) (proto.InputPreviews, error) {
	out := proto.InputPreviews{History: history, Inputs: []proto.UserInputPosition{}}
	positions := slices.Clone(p.Positions)
	sort.SliceStable(positions, func(i, j int) bool { return positions[i].Seq < positions[j].Seq })
	for len(positions) > 0 {
		if err := ctx.Err(); err != nil {
			return proto.InputPreviews{}, err
		}
		seq := positions[0].Seq
		end := 1
		for end < len(positions) && positions[end].Seq == seq {
			end++
		}
		if through := p.Through(seq); through >= seq {
			items, err := read(seq, through)
			if err != nil {
				return proto.InputPreviews{}, err
			}
			out.Inputs = append(out.Inputs, UserInputPreviewsFor(items, positions[:end])...)
		}
		positions = positions[end:]
	}
	return out, nil
}

// ItemsWindow seeks a bounded line interval and returns the cursors on both sides of it.
func (f *File) ItemsWindow(ctx context.Context, p proto.ItemsWindowRequest) (proto.ItemsWindow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.ensureColdInputStructure(ctx); err != nil {
		return proto.ItemsWindow{}, err
	}
	end := f.lines
	if p.BeforeSeq > 0 {
		end = min(end, p.BeforeSeq-1)
	}
	out := proto.ItemsWindow{History: f.history(), Items: []proto.Item{}}
	out.FromSeq, out.ToSeq = min(p.FromSeq-1, end), min(p.ThroughSeq, end)
	out.HasMore, out.MoreAfter = out.FromSeq > 0, out.ToSeq < end
	if out.FromSeq == out.ToSeq {
		return out, nil
	}
	items, err := f.navigationItems(ctx, out.FromSeq+1, out.ToSeq)
	if len(items) > 0 {
		out.Items = items
	}
	return out, err
}

// navigationItems joins a detached read of the older interval to any cached rows. It never backfills the lines
// between the interval and the cached tail. Callers hold f.mu and have established the branch topology.
func (f *File) navigationItems(ctx context.Context, from, through int) ([]proto.Item, error) {
	through = min(through, f.lines)
	if from > through {
		return nil, nil
	}
	var out []proto.Item
	if from <= f.base {
		fh, err := f.openNavigationFile()
		if err != nil {
			return nil, err
		}
		defer fh.Close()
		w := f.win
		last := min(through, f.base)
		start := from
		if w.codex {
			// A completed native item may appear twice in a turn. Recreate the deduplication state from its start.
			start = max(1, f.findTurn(fh, w, from, w.starts[from], 0))
		}
		ids := map[string]bool{}
		if err := readNavigationLines(ctx, fh, w, start, last, func(_ int, line []byte) {
			maps.Copy(ids, seedTaskIDs(line))
		}); err != nil {
			return nil, err
		}
		meta := f.seeded(fh, w, start, w.starts[last], ids)
		if err := readNavigationLines(ctx, fh, w, start, last, func(seq int, line []byte) {
			items := ParseLine(seq, line, &meta)
			if seq < from {
				return
			}
			for _, it := range items {
				if !f.tree.hidden[uuidOf(it.ID)] {
					out = append(out, LightItem(it))
				}
			}
		}); err != nil {
			return nil, err
		}
	}
	return append(out, f.items[f.before(from):f.before(through+1)]...), nil
}

// ensureColdInputStructure reads just the missing prefix using structural decoders. It is installed only after a
// complete scan, leaving the ordinary tail and its parser state intact. Callers hold f.mu.
func (f *File) ensureColdInputStructure(ctx context.Context) error {
	if err := ctx.Err(); err != nil || f.base == 0 || f.coldInputs != nil {
		return err
	}
	fh, err := f.openNavigationFile()
	if err != nil {
		return err
	}
	defer fh.Close()
	prefix, tree := &coldInputStructure{}, f.freshTree()
	meta := Meta{Sidechain: f.meta.Sidechain, Codex: f.win.codex}
	if err := readNavigationLines(ctx, fh, f.win, 1, f.base, func(seq int, line []byte) {
		items := navigationInputLine(seq, line, &meta)
		if tl := meta.tree; tl.uuid != "" && !tl.sidechain && !meta.Sidechain && !meta.Codex {
			tree.add(tl.rec(items))
		}
		meta.tree = treeLine{}
		for i := range items {
			if items[i].Role == "user" && items[i].Kind == proto.KindText {
				items[i] = LightItem(items[i])
			}
		}
		prefix.inputs = append(prefix.inputs, inputGroups(items, nil)...)
	}); err != nil {
		return err
	}
	if f.tree != nil {
		for _, rec := range f.tree.recs {
			tree.add(rec)
		}
	}
	f.items = withoutLines(f.items, tree.hidden)
	f.tree, f.coldInputs = tree, prefix
	return nil
}

func (f *File) openNavigationFile() (*os.File, error) {
	fh, err := openTranscript(f.Path)
	if err != nil {
		return nil, err
	}
	st, err := statOpenedTranscript(fh)
	if err == nil && (st.Size() < f.offset || (f.ino != 0 && inode(st) != f.ino)) {
		err = errors.New("transcript changed during navigation read")
	}
	if err != nil {
		fh.Close()
		return nil, err
	}
	return fh, nil
}

// readNavigationLines seeks directly to the first complete line. Memory is bounded by the largest line read.
func readNavigationLines(ctx context.Context, fh *os.File, w *window, first, last int, visit func(int, []byte)) error {
	start, end := w.starts[first-1], w.starts[last]
	reader := bufio.NewReader(io.NewSectionReader(fh, start, end-start))
	for seq := first; seq <= last; seq++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return err
		}
		visit(seq, line[:len(line)-1])
	}
	return ctx.Err()
}

// navigationInputLine decodes the branch fields and sends only lines that can produce user inputs to the ordinary
// parser. Claude tool, thinking and reply bodies and Codex completed work stay outside the compact projection.
func navigationInputLine(seq int, line []byte, meta *Meta) []proto.Item {
	if meta.Codex {
		return codexNavigationInput(seq, line, meta)
	}
	var h struct {
		Type        string    `json:"type"`
		Subtype     string    `json:"subtype"`
		UUID        string    `json:"uuid"`
		ParentUUID  string    `json:"parentUuid"`
		IsSidechain bool      `json:"isSidechain"`
		Timestamp   time.Time `json:"timestamp"`
		Attachment  *struct {
			Type string `json:"type"`
		} `json:"attachment"`
	}
	if unmarshalLine(line, &h) != nil {
		return nil
	}
	if h.Type == "user" || h.Type == "system" && h.Subtype == "local_command" ||
		h.Type == "attachment" && h.Attachment != nil && h.Attachment.Type == "queued_command" {
		return ParseLine(seq, line, meta)
	}
	meta.tree = treeLine{uuid: h.UUID, parent: h.ParentUUID, sidechain: h.IsSidechain, assistant: h.Type == "assistant",
		system: h.Type == "system", turnEnd: h.Type == "system" && h.Subtype == "turn_duration"}
	return nil
}

func codexNavigationInput(seq int, line []byte, meta *Meta) []proto.Item {
	// Keep the duplicate-ID state across compacted records without decoding their tool bodies.
	var h struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Item *struct {
				Type   string `json:"type"`
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"item"`
		} `json:"payload"`
	}
	if len(bytes.TrimSpace(line)) == 0 || unmarshalLine(line, &h) != nil {
		return nil
	}
	if h.Type == "history_mutation" || h.Type == "event_msg" && h.Payload.Type == "task_started" {
		return ParseLine(seq, line, meta)
	}
	it := h.Payload.Item
	if h.Type != "event_msg" || h.Payload.Type != "item_completed" || it == nil {
		return nil
	}
	if it.Type == "UserMessage" {
		return ParseLine(seq, line, meta)
	}
	if it.ID != "" && (it.Type != "CommandExecution" || it.Status != "backgrounded") {
		meta.codexSeenAdd(it.ID)
	}
	return nil
}
