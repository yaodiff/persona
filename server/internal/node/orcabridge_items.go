package node

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// A chat's content when it is not a Claude transcript on this machine (a Codex chat, another provider's, a Claude chat
// before its transcript exists): Orca's own journal (agentSession.history), as session.items pages
// (docs/integrations/runtimes.md "Content"). An item's seq is its journal sequence, a message's blocks its items
// "<seq>#<index>"; history is the journal's epoch, which a rewind changes.

// orcaItemsMax is the most journal rows one page reads (Orca's own limit).
const orcaItemsMax = 200

// orcaItemsParams are the session.items params the bridge reads.
type orcaItemsParams struct {
	Limit     int  `json:"limit"`
	Turns     int  `json:"turns"`
	BeforeSeq *int `json:"before_seq"`
	AfterSeq  *int `json:"after_seq"`
}

// items answers session.items from the chat's journal: the newest rows, the ones before before_seq, or the ones
// written since after_seq.
func (b *orcaBridge) items(ctx context.Context, orca string, p orcaItemsParams) (any, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = max(p.Turns*8, 60)
	}
	limit = min(limit, orcaItemsMax)
	var page *orcart.HistoryPage
	var err error
	switch {
	case p.BeforeSeq != nil || p.AfterSeq != nil:
		var head *orcart.HistoryPage
		if head, err = b.c.Journal(ctx, orca, "tail", nil, 1); err != nil {
			break
		}
		if p.AfterSeq != nil {
			page, err = b.c.Journal(ctx, orca, "after", &orcart.HistoryCursor{Epoch: head.Epoch, Sequence: int64(max(*p.AfterSeq, 0))}, orcaItemsMax)
		} else {
			page, err = b.c.Journal(ctx, orca, "before", &orcart.HistoryCursor{Epoch: head.Epoch, Sequence: int64(max(*p.BeforeSeq, 0))}, limit)
		}
		if err == nil && page.Live < head.Live {
			page.Live = head.Live
		}
	default:
		page, err = b.c.Journal(ctx, orca, "tail", nil, limit)
	}
	if err != nil {
		return nil, orcaWriteErr(err)
	}
	out := map[string]any{"items": orcaItems(page.Items), "to_seq": int(page.Live), "history": "orca:" + page.Epoch}
	if p.AfterSeq == nil {
		out["has_more"] = page.HasOlder
	}
	return out, nil
}

// orcaItems are Persona's items of journal rows, oldest first: the user's and the agent's words, its thinking, its tool
// calls and their results, and the warnings and errors Orca wrote into the chat. Turns, prompts (cards) and Orca's
// own bookkeeping are left out. A result is matched to the call before it it answers (by call id when the result has
// one, else the oldest call not yet answered).
func orcaItems(rows []orcart.JournalItem) []proto.Item {
	sorted := slices.Clone(rows) // oldest first, whichever order the page came in
	slices.SortStableFunc(sorted, func(a, b orcart.JournalItem) int { return cmp.Compare(a.Sequence, b.Sequence) })
	out := []proto.Item{}
	var open []string // tool calls not answered yet, oldest first
	for _, row := range sorted {
		seq := int(row.Sequence)
		ts := time.UnixMilli(row.ObservedAt).UTC()
		n := 0 // the row's items so far: the next one's index in its id
		add := func(it proto.Item) {
			it.ID, it.Seq, it.TS = fmt.Sprintf("%d#%d", seq, n), seq, ts
			n++
			out = append(out, it)
		}
		body := row.Body
		switch body.Kind {
		case "status":
			if text := strings.TrimSpace(body.Text); text != "" && (body.Tone == "warning" || body.Tone == "error") {
				add(proto.Item{Role: "system", Kind: "notice", Text: text})
			}
			continue
		case "message":
		default:
			continue
		}
		for i, block := range body.Blocks {
			switch block.Type {
			case "text":
				text := strings.TrimSpace(block.Text)
				if text == "" {
					continue
				}
				switch body.Role {
				case "user":
					add(proto.Item{Role: "user", Kind: "text", Text: block.Text})
				case "assistant":
					add(proto.Item{Role: "assistant", Kind: "text", Text: block.Text})
				case "reasoning":
					add(proto.Item{Role: "assistant", Kind: "thinking", Text: block.Text})
				}
			case "tool-call":
				id := block.CallID
				if id == "" {
					id = fmt.Sprintf("%s#%d", row.ItemID, i)
				}
				it := proto.Item{Role: "assistant", Kind: "tool_use", ToolName: firstOf(block.Name, "tool"), ToolUseID: id}
				if in := strings.TrimSpace(string(block.Input)); in != "" && in != "null" && json.Valid(block.Input) {
					it.ToolInput = json.RawMessage(in)
				}
				add(it)
				open = append(open, id)
			case "tool-result":
				id := block.CallID
				if id == "" && len(open) > 0 {
					id = open[0]
				}
				for k, o := range open {
					if o == id {
						open = append(open[:k], open[k+1:]...)
						break
					}
				}
				add(proto.Item{Role: "user", Kind: "tool_result", ToolUseID: id, Text: block.Output, IsError: block.IsError})
			}
		}
	}
	return out
}
