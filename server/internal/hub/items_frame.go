package hub

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// wsItemsFrameBudgetBytes caps the serialized JSON size of a single outbound
	// type="items" WebSocket frame. Oversized batches are split into consecutive
	// contiguous frames so clients do not parse multi-megabyte JSON payloads on
	// their UI thread. A single item larger than the budget is still emitted in
	// its own one-item frame so delivery always makes progress.
	wsItemsFrameBudgetBytes = 512 << 10

	// wsLargeFrameWarnBytes logs a structured warning (metadata only, never body)
	// whenever an outbound WebSocket frame written by writeWS reaches this size.
	wsLargeFrameWarnBytes = 512 << 10
)

type itemsFrameOpts struct {
	machineID string
	sessionID string
	items     []proto.Item
	span      itemSpan
	backlog   bool
	partial   bool
	moreAfter bool
	hasMore   *bool
}

func buildItemsFrameMap(opts itemsFrameOpts, items []proto.Item, span itemSpan, includeHasMore, includeMoreAfter bool) map[string]any {
	if items == nil {
		items = []proto.Item{}
	}
	out := span.into(map[string]any{
		"type":       "items",
		"machine_id": opts.machineID,
		"session_id": opts.sessionID,
		"items":      items,
	})
	if opts.backlog {
		out["backlog"] = true
	}
	if opts.partial {
		out["partial"] = true
	}
	if includeHasMore && opts.hasMore != nil {
		out["has_more"] = *opts.hasMore
	}
	if includeMoreAfter && opts.moreAfter {
		out["more_after"] = true
	}
	return out
}

func splitChunkSpan(orig itemSpan, chunk []proto.Item, first, last bool, prevTo int) itemSpan {
	sp := itemSpan{History: orig.History}
	if first {
		sp.FromSeq = orig.FromSeq
	} else if orig.ToSeq != nil {
		from := prevTo
		sp.FromSeq = &from
	}
	if orig.ToSeq == nil {
		return sp
	}
	if last {
		sp.ToSeq = orig.ToSeq
		return sp
	}
	chunkTo := prevTo
	if first && orig.FromSeq != nil && *orig.FromSeq > chunkTo {
		chunkTo = *orig.FromSeq
	}
	for _, it := range chunk {
		if it.Seq > chunkTo {
			chunkTo = it.Seq
		}
	}
	if chunkTo > *orig.ToSeq {
		chunkTo = *orig.ToSeq
	}
	sp.ToSeq = &chunkTo
	return sp
}

func marshalItemsFrames(opts itemsFrameOpts) ([][]byte, error) {
	return marshalItemsFramesWithBudget(opts, wsItemsFrameBudgetBytes)
}

func marshalItemsFramesWithBudget(opts itemsFrameOpts, budget int) ([][]byte, error) {
	items := opts.items
	if items == nil {
		items = []proto.Item{}
	}
	full, err := marshalItems(buildItemsFrameMap(opts, items, opts.span, true, true))
	if err != nil {
		return nil, err
	}
	if budget <= 0 || len(full) <= budget || len(items) <= 1 {
		return [][]byte{full}, nil
	}

	itemSizes := make([]int, len(items))
	for i := range items {
		b, err := marshalItems(items[i])
		if err != nil {
			return nil, err
		}
		itemSizes[i] = len(b)
	}

	sampleSpan := opts.span
	if sampleSpan.ToSeq != nil && sampleSpan.FromSeq == nil {
		zero := 0
		sampleSpan.FromSeq = &zero
	}
	baseFrame, err := marshalItems(buildItemsFrameMap(opts, []proto.Item{}, sampleSpan, true, true))
	if err != nil {
		return nil, err
	}
	// Reserve 64 bytes of slack for multi-digit from_seq/to_seq values.
	baseBytes := len(baseFrame) + 64

	var frames [][]byte
	var prevTo int
	for start := 0; start < len(items); {
		first := start == 0
		end := start + 1
		est := baseBytes + itemSizes[start]
		for end < len(items) && est+1+itemSizes[end] <= budget {
			est += 1 + itemSizes[end]
			end++
		}
		for {
			last := end == len(items)
			chunk := items[start:end]
			chunkSpan := splitChunkSpan(opts.span, chunk, first, last, prevTo)
			frame, err := marshalItems(buildItemsFrameMap(opts, chunk, chunkSpan, first, last))
			if err != nil {
				return nil, err
			}
			if len(frame) <= budget || end-start == 1 {
				if chunkSpan.ToSeq != nil {
					prevTo = *chunkSpan.ToSeq
				}
				frames = append(frames, frame)
				start = end
				break
			}
			end--
		}
	}
	return frames, nil
}

func (c *client) warnOversizedFrame(b []byte) {
	c.warnOversizedFrameAt(b, wsLargeFrameWarnBytes)
}

func (c *client) warnOversizedFrameAt(b []byte, threshold int) {
	if threshold <= 0 || len(b) < threshold || c.hub == nil || c.hub.logger == nil {
		return
	}
	var meta struct {
		Type      string `json:"type"`
		MachineID string `json:"machine_id"`
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(b, &meta)
	c.hub.logf("warn: oversized websocket frame type=%q machine_id=%q session_id=%q bytes=%d", meta.Type, meta.MachineID, meta.SessionID, len(b))
}
