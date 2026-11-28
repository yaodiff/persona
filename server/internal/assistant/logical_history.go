package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func visibleSegments(c *db.LogicalConversation) []conversationlog.Segment {
	out := make([]conversationlog.Segment, 0, len(c.Segments))
	for _, seg := range c.Segments {
		if seg.State == "active" || seg.State == "retired" {
			out = append(out, seg)
		}
	}
	return out
}

func (s *Service) physicalHistoryCall(ctx context.Context, machine, method string, p any, out any) error {
	ctx = context.WithValue(ctx, logicalSourceKey{}, true)
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	data, handled, err := s.historyCall(ctx, machine, method, raw)
	if !handled {
		data, err = s.logicalPhysical(ctx, machine, method, p)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (s *Service) logicalHistoryCall(ctx context.Context, st logicalConversationStore, c *db.LogicalConversation, method string, raw json.RawMessage) (json.RawMessage, error) {
	switch method {
	case proto.MethodSessionInputPreviews:
		return s.logicalInputPreviews(ctx, st, c, raw)
	case proto.MethodSessionItemsWindow:
		return s.logicalItemsWindow(ctx, st, c, raw)
	}
	var p historyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if method == "session.item" || method == "session.image" {
		return s.logicalFullItem(ctx, c, method, p)
	}
	if method == DeviceAssistantHandoff {
		p.Turns, p.LazyImages = 6, true
		page, err := s.logicalHistoryPage(ctx, st, c, p)
		if err != nil {
			return nil, err
		}
		h := mainHandoff{Partial: page.Partial}
		if hs, ok := s.store().(conversationHistoryStore); ok {
			segs := visibleSegments(c)
			for i := len(segs) - 1; i >= 0; i-- {
				seg := segs[i]
				source, e := hs.ConversationHistory(ctx, seg.MachineID, seg.SessionID)
				if e != nil {
					return nil, e
				}
				if source != nil && source.SummaryKnown {
					h.Summary = source.Summary
					break
				}
			}
		}
		for _, it := range page.Items {
			if it.Kind != proto.KindText || (it.Role != "user" && it.Role != "assistant") {
				continue
			}
			if it.Stored != nil && !it.Stored.Complete {
				h.Partial = true
				continue
			}
			text := strings.TrimSpace(transcript.StripInjected(it.Text))
			if text == "" {
				continue
			}
			h.Recent = append(h.Recent, struct {
				Role string `json:"role"`
				Text string `json:"text"`
			}{it.Role, clip(text, 800)})
		}
		if len(h.Recent) > 12 {
			h.Recent = h.Recent[len(h.Recent)-12:]
		}
		return json.Marshal(h)
	}
	if p.UserInputs {
		return s.logicalUserInputs(ctx, st, c, p.MetaOnly)
	}
	page, err := s.logicalHistoryPage(ctx, st, c, p)
	if err != nil {
		return nil, err
	}
	return json.Marshal(page)
}

func (s *Service) logicalFullItem(ctx context.Context, c *db.LogicalConversation, method string, p historyParams) (json.RawMessage, error) {
	segs := visibleSegments(c)
	if len(segs) == 0 {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	seg, source := segs[0], p.ItemID
	if id, item, ok := proto.ParseSegmentItemID(p.ItemID); ok {
		for _, candidate := range segs {
			if candidate.Ordinal > 1 && candidate.ID == id {
				seg, source = candidate, item
				break
			}
		}
	}
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	check := func() error {
		var tail historyPage
		if err := s.physicalHistoryCall(ctx, seg.MachineID, DeviceSessionItems, map[string]any{"session_id": seg.SessionID, "limit": 1, "lazy_images": true, "strict_read": p.Strict}, &tail); err != nil {
			return err
		}
		if tail.History == "" {
			return logicalRefusal(ctx, "missing_generation")
		}
		if err := st.ObserveConversationSource(ctx, seg.MachineID, seg.SessionID, tail.History); err != nil {
			return err
		}
		return s.checkLogicalRead(ctx, st, c)
	}
	if err := check(); err != nil {
		return nil, err
	}
	params := map[string]any{"session_id": seg.SessionID, "item_id": source, "strict_read": p.Strict}
	// Fetch the item first even for images: prove it is inside the frozen public
	// range before fetching or returning its bytes through a scoped logical link.
	var it proto.Item
	if err := s.physicalHistoryCall(ctx, seg.MachineID, "session.item", params, &it); err != nil {
		return nil, err
	}
	projected, ok, err := seg.Project(it, c.MachineID, c.SessionID, c.History)
	if err != nil {
		return nil, err
	}
	if !ok || projected.ID != p.ItemID {
		return nil, logicalRefusal(ctx, "private_segment")
	}
	if method == "session.image" {
		var image proto.Image
		if err := s.physicalHistoryCall(ctx, seg.MachineID, method, params, &image); err != nil {
			return nil, err
		}
		if err := check(); err != nil {
			return nil, err
		}
		return json.Marshal(image)
	}
	if err := check(); err != nil {
		return nil, err
	}
	return json.Marshal(projected)
}

func logicalPageLimit(p historyParams) int {
	switch {
	case p.Through != nil:
		return 1000
	case p.After != nil:
		return 500
	case p.Turns > 0:
		return 2000
	}
	if p.Limit > 0 && p.Limit <= 500 {
		return p.Limit
	}
	return 80
}

func mergeLogicalSpan(coverage []conversationlog.Range, end int, seg conversationlog.Segment, from, to int) ([]conversationlog.Range, int, error) {
	a, b, err := seg.Span(from, to)
	if err != nil {
		return coverage, end, err
	}
	var r conversationlog.Range
	r.From, r.Through = a, b
	return conversationlog.MergeRanges(coverage, r), max(end, b), nil
}

// Physical line intervals, not normalized-item counts, compose the public cursor.
// A second read of the root detects activation or source rewrites during a page.
func (s *Service) logicalHistoryPage(ctx context.Context, st logicalConversationStore, c *db.LogicalConversation, p historyParams) (historyPage, error) {
	page := historyPage{History: c.History, Items: []proto.Item{}}
	segs := visibleSegments(c)
	forward := p.After != nil || p.Through != nil
	if !forward {
		for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
			segs[i], segs[j] = segs[j], segs[i]
		}
	}
	limit := logicalPageLimit(p)
	var coverage []conversationlog.Range
	end := 0
	moreSource := false
	for index, seg := range segs {
		if p.Before != nil && *p.Before > 0 && seg.Offset >= *p.Before {
			continue
		}
		if p.Through != nil && seg.Offset >= *p.Through {
			continue
		}
		if p.Through != nil && seg.FrozenEnd != nil && seg.Offset+*seg.FrozenEnd < max(1, intValue(p.From)) {
			continue
		}
		if p.After != nil && seg.FrozenEnd != nil && seg.Offset+*seg.FrozenEnd <= *p.After {
			continue
		}
		if seg.FrozenEnd != nil && *seg.FrozenEnd == 0 {
			var err error
			coverage, end, err = mergeLogicalSpan(coverage, end, seg, 0, 0)
			if err != nil {
				return page, err
			}
			continue
		}
		params := map[string]any{"session_id": seg.SessionID, "limit": limit, "lazy_images": p.LazyImages, "strict_read": p.Strict}
		if p.After != nil {
			params["after_seq"] = max(0, *p.After-seg.Offset)
		}
		if p.Through != nil {
			params["from_seq"] = max(1, intValue(p.From)-seg.Offset)
			params["through_seq"] = max(0, *p.Through-seg.Offset)
		}
		if p.Before != nil && *p.Before > 0 {
			params["before_seq"] = max(1, *p.Before-seg.Offset)
		}
		if seg.FrozenEnd != nil {
			if forward {
				params["from_seq"] = max(1, intValue(p.After)+1-seg.Offset)
				if p.Through != nil {
					params["from_seq"] = max(1, intValue(p.From)-seg.Offset)
				}
				params["through_seq"] = *seg.FrozenEnd
				if p.Through != nil {
					params["through_seq"] = min(*seg.FrozenEnd, max(0, *p.Through-seg.Offset))
				}
				delete(params, "after_seq")
			} else {
				before := *seg.FrozenEnd + 1
				if v, ok := params["before_seq"].(int); ok {
					before = min(before, v)
				}
				params["before_seq"] = before
			}
		}
		if p.Turns > 0 && !forward {
			params["turns"] = min(p.Turns, 10)
		}
		segCtx := ctx
		if seg.FrozenEnd != nil {
			segCtx = withoutLiveHistoryRead(ctx) // retired frozen segments cannot receive new sends; durable history stays authoritative
		}
		var source historyPage
		if err := s.physicalHistoryCall(segCtx, seg.MachineID, DeviceSessionItems, params, &source); err != nil {
			if isMissingTranscriptErr(err) && s.isZeroTurnPreparedSegment(ctx, c, seg) {
				var spanErr error
				coverage, end, spanErr = mergeLogicalSpan(coverage, end, seg, 0, 0)
				if spanErr != nil {
					return page, spanErr
				}
				continue
			}
			return page, err
		}
		// A raw range can omit its generation. In a frozen segment, a newer source can supply a verified
		// continuation so a cursor from a detached window can still page forward through that segment.
		if source.History == "" && p.After != nil && seg.FrozenEnd != nil {
			from := max(1, *p.After+1-seg.Offset)
			through := from + min(*seg.FrozenEnd-from, 2*proto.MaxWindowRadius)
			scoped := proto.ItemsWindowRequest{SessionID: proto.NavigationSessionID(seg.SessionID), FromSeq: from, ThroughSeq: through, BeforeSeq: *seg.FrozenEnd + 1}
			var window proto.ItemsWindow
			if err := s.physicalHistoryCall(segCtx, seg.MachineID, proto.MethodSessionItemsWindow, scoped, &window); err == nil && window.Matches(scoped) {
				source = historyPage{Items: window.Items, History: window.History, FromSeq: &window.FromSeq, ToSeq: &window.ToSeq, MoreAfter: window.MoreAfter}
			}
		}
		if source.History == "" {
			if len(source.Items) == 0 && intValue(source.ToSeq) == 0 && s.isZeroTurnPreparedSegment(ctx, c, seg) {
				var spanErr error
				coverage, end, spanErr = mergeLogicalSpan(coverage, end, seg, 0, 0)
				if spanErr != nil {
					return page, spanErr
				}
				continue
			}
			return page, logicalRefusal(ctx, "missing_generation")
		}
		if err := st.ObserveConversationSource(ctx, seg.MachineID, seg.SessionID, source.History); err != nil {
			return page, err
		}
		// A fold must be built after combining segments, never imported as source data.
		for _, it := range source.Items {
			projected, ok, err := seg.Project(it, c.MachineID, c.SessionID, c.History)
			if err != nil {
				return page, err
			}
			if !ok {
				continue
			}
			if p.After != nil && projected.Seq <= *p.After {
				continue
			}
			if p.Before != nil && *p.Before > 0 && projected.Seq >= *p.Before {
				continue
			}
			if p.Through != nil && (projected.Seq < intValue(p.From) || projected.Seq > *p.Through) {
				continue
			}
			page.Items = append(page.Items, projected)
		}
		from, to := intValue(source.FromSeq), intValue(source.ToSeq)
		if source.ToSeq == nil {
			for _, it := range source.Items {
				to = max(to, it.Seq)
			}
		}
		var err error
		coverage, end, err = mergeLogicalSpan(coverage, end, seg, from, to)
		if err != nil {
			return page, err
		}
		if seg.FrozenEnd != nil {
			segEnd := seg.Offset + *seg.FrozenEnd
			if p.Through != nil {
				segEnd = min(segEnd, *p.Through)
			}
			end = max(end, segEnd)
		}
		page.Partial = page.Partial || source.Partial
		moreSource = moreSource || source.MoreAfter || source.More
		if (p.After != nil || p.Through != nil) && seg.FrozenEnd != nil && (source.MoreAfter || source.More) {
			break // finish this segment before advancing to its successor
		}
		if len(page.Items) >= limit && p.Turns <= 0 {
			moreSource = moreSource || forward && index+1 < len(segs)
			break
		}
		if p.Turns > 0 {
			seen := 0
			for _, it := range page.Items {
				if transcript.IsPagePrompt(it) {
					seen++
				}
			}
			if seen >= min(p.Turns, 10) {
				break
			}
		}
	}
	sort.SliceStable(page.Items, func(i, j int) bool { return page.Items[i].Seq < page.Items[j].Seq })
	if len(page.Items) > limit {
		if forward {
			last := limit
			for last < len(page.Items) && page.Items[last].Seq == page.Items[last-1].Seq {
				last++
			}
			page.Items = page.Items[:last]
		} else {
			first := len(page.Items) - limit
			for first > 0 && page.Items[first].Seq == page.Items[first-1].Seq {
				first--
			}
			page.Items = page.Items[first:]
		}
	}
	if p.Turns > 0 && !forward {
		first, seen := len(page.Items), 0
		for first > 0 && seen < min(p.Turns, 10) {
			first--
			if transcript.IsPagePrompt(page.Items[first]) {
				seen++
			}
		}
		for first > 0 && page.Items[first].Seq == page.Items[first-1].Seq {
			first--
		}
		page.Items = page.Items[first:]
	}
	from, to := 0, end
	if len(coverage) > 0 {
		from = coverage[0].From
		to = coverage[len(coverage)-1].Through
	}
	if p.After != nil {
		from = max(from, *p.After)
	}
	if p.Through != nil {
		from = max(from, intValue(p.From)-1)
		to = min(to, *p.Through)
	}
	if p.Before != nil && *p.Before > 0 {
		to = min(to, *p.Before-1)
	}
	if len(page.Items) > 0 && len(page.Items) >= limit {
		if forward {
			to = min(to, page.Items[len(page.Items)-1].Seq)
		} else {
			from = max(from, page.Items[0].Seq-1)
		}
	}
	if p.Turns > 0 && len(page.Items) > 0 {
		from = max(from, page.Items[0].Seq-1)
	}
	if to < from {
		to = from
	}
	page.FromSeq, page.ToSeq = &from, &to
	page.Partial = page.Partial || !conversationlog.Covers(coverage, from, to)
	if forward {
		page.MoreAfter = moreSource || to < end
	} else {
		more := from > 0
		page.HasMore = &more
	}
	if p.Through != nil {
		page.More = page.MoreAfter
		page.MoreAfter = false
	}
	if p.FoldWork && p.After == nil && p.Through == nil {
		if p.Before == nil || *p.Before <= 0 {
			page.Items = transcript.FoldWorkLatest(page.Items)
		} else {
			page.Items = transcript.FoldWork(page.Items)
		}
	}
	if p.LazyImages {
		page.Items = proto.WithoutImageData(page.Items)
	}
	if err := s.checkLogicalRead(ctx, st, c); err != nil {
		return page, err
	}
	return page, nil
}

func (s *Service) checkLogicalRead(ctx context.Context, st logicalConversationStore, c *db.LogicalConversation) error {
	latest, err := st.LogicalConversation(ctx, c.MachineID, c.SessionID)
	if err != nil {
		return err
	}
	if latest == nil || latest.Deleted {
		return db.ErrConversationDeleted
	}
	// Preparing a switch freezes the active source before activation moves epoch or history.
	if latest.Epoch != c.Epoch || latest.History != c.History ||
		!slices.EqualFunc(visibleSegments(c), visibleSegments(latest), func(a, b conversationlog.Segment) bool {
			return a.ID == b.ID && a.Offset == b.Offset && (a.FrozenEnd == nil) == (b.FrozenEnd == nil) &&
				intValue(a.FrozenEnd) == intValue(b.FrozenEnd)
		}) {
		s.logicalChanged()
		return db.ErrConversationHistoryChanged
	}
	return nil
}

func (s *Service) logicalUserInputs(ctx context.Context, st logicalConversationStore, c *db.LogicalConversation, metaOnly bool) (json.RawMessage, error) {
	out := proto.UserInputIndex{History: c.History, Inputs: []proto.UserInputPosition{}}
	for _, seg := range visibleSegments(c) {
		if seg.FrozenEnd != nil && *seg.FrozenEnd == 0 {
			continue
		}
		params := map[string]any{"session_id": seg.SessionID, "user_inputs": true}
		if metaOnly {
			params["meta_only"] = true
		}
		if seg.FrozenEnd != nil {
			// Scope excerpts as well as positions. The final visible input can have a reply in the private tail.
			params["before_seq"] = *seg.FrozenEnd + 1
		}
		var source proto.UserInputIndex
		if err := s.physicalHistoryCall(ctx, seg.MachineID, DeviceSessionItems, params, &source); err != nil {
			if isMissingTranscriptErr(err) && s.isZeroTurnPreparedSegment(ctx, c, seg) {
				continue
			}
			return nil, err
		}
		if source.Total != len(source.Inputs) || source.Inputs == nil {
			return nil, errors.New("incomplete source input positions")
		}
		if source.History != "" {
			if err := st.ObserveConversationSource(ctx, seg.MachineID, seg.SessionID, source.History); err != nil {
				return nil, err
			}
		}
		for _, it := range source.Inputs {
			// Older nodes ignore the boundary and send positions alone.
			if seg.FrozenEnd != nil && it.Seq > *seg.FrozenEnd {
				continue
			}
			if metaOnly {
				it = it.WithoutExcerpts()
			}
			seq, err := seg.Seq(it.Seq)
			if err != nil {
				return nil, err
			}
			it.ID, it.Seq = seg.ItemID(it.ID), seq
			out.Inputs = append(out.Inputs, it)
		}
	}
	if err := s.checkLogicalRead(ctx, st, c); err != nil {
		return nil, err
	}
	out.Total = len(out.Inputs)
	return json.Marshal(out)
}
