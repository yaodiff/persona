package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

const legacyPagePrefix = "ui.session."

// Older apps get only full fitted Views and an opaque state checkpoint. Callbacks
// and component state stay in an owner-bound session, never serialized as code.
// Legacy UI work is suspended after each HTTP request: hidden pages cannot poll.
func (h *Manager) pageLegacy(ctx context.Context, p *extension.Extension, hp *hubExtension, item extension.UIItem, slot extension.UISlot, params extension.UIParams, in uiRequest) (extension.ActionResult, bool, error) {
	if !extension.FullPage(item.Placement) {
		return extension.ActionResult{}, false, nil
	}
	hasToken := strings.HasPrefix(in.State, legacyPagePrefix)
	// State is opaque for ordinary legacy pages, even when it resembles our
	// checkpoint format. Only component pages opt into interpreting that state.
	var supported struct {
		Contributions []string `json:"contributions"`
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	err := hp.proc.Call(cctx, "ui.page.supported", map[string]any{}, &supported)
	cancel()
	if err != nil || !slices.Contains(supported.Contributions, item.ID) {
		return extension.ActionResult{}, false, nil
	}
	who := principalOf(ctx)
	principal := who.ID
	if principal == "" {
		principal = "owner"
	}
	hub := who.Hub
	if hub == "" {
		hub = "local"
	}
	identity := extwire.UIPrincipal{Hub: hub, Principal: principal, Connection: "http", Role: who.Role, Paired: who.Paired, Lang: string(h.contextLang(ctx))}
	if !pagePrincipalOK(identity) {
		return extension.ActionResult{}, true, pageError("forbidden")
	}
	ps := &h.uiPages
	var s *pageSession
	var checkpoint int64
	var presentation json.RawMessage
	if hasToken {
		parts := strings.Split(strings.TrimPrefix(in.State, legacyPagePrefix), ".")
		if len(parts) != 3 {
			return extension.ActionResult{}, true, pageError("invalid")
		}
		var err error
		checkpoint, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return extension.ActionResult{}, true, pageError("invalid")
		}
		ps.mu.Lock()
		s = ps.sessions[parts[0]]
		if s != nil && (s.closed || !s.legacy || s.epoch != parts[1] || !samePageOwner(s.who, identity) || s.extension != p.Record.ID || s.contribution != item.ID || s.process != hp.proc) {
			ps.mu.Unlock()
			return extension.ActionResult{}, true, pageError("expired")
		}
		if s != nil && (s.run != hp.proc.Generation() || !s.expires.IsZero() && !time.Now().Before(s.expires)) {
			h.pageRemoveLocked(s)
			ps.mu.Unlock()
			h.pageDispose(s)
			s = nil
		} else {
			ps.mu.Unlock()
		}
		if s == nil {
			// Old apps keep failed checkpoints. Only read-only redraws may remount;
			// actions must never turn an expired checkpoint into a fresh write.
			if in.Action != "" {
				return extension.ActionResult{}, true, pageError("expired")
			}
			in.State, params.State = "", ""
			return h.pageLegacy(ctx, p, hp, item, slot, params, in)
		}
		ps.mu.Lock()
		if s.closed || s.busy || s.legacyUse || s.pauseDone != nil {
			ps.mu.Unlock()
			return extension.ActionResult{}, true, pageError("inactive")
		}
		if in.Action != "" && s.tree.Version() != checkpoint {
			ps.mu.Unlock()
			return extension.ActionResult{}, true, pageError("stale")
		}
		s.legacyUse, s.busy, s.recovering = true, true, true
		s.expires = time.Time{}
		ps.mu.Unlock()
		defer h.pageLegacyFinish(s)
		var native pageNative
		cctx, cancel := context.WithTimeout(ctx, uiTimeout)
		err = hp.proc.Call(cctx, "ui.page.resume", map[string]any{"session": s.id}, &native)
		cancel()
		ps.mu.Lock()
		s.busy, s.recovering = false, false
		if err == nil && !s.closed && s.run == hp.proc.Generation() && native.Generation == s.native.Generation && native.Version >= s.native.Version {
			if s.latest != nil && s.latest.Version > native.Version {
				native = *s.latest
			}
			s.latest = nil
			s.native = native
			err = h.pageAdvanceLocked(s, false, "")
		} else if err == nil {
			err = pageError("expired")
		}
		if err != nil || s.closed {
			h.pageRemoveLocked(s)
			ps.mu.Unlock()
			h.pageDispose(s)
			return extension.ActionResult{}, true, pageError("expired")
		}
		s.suspended = false
		ps.mu.Unlock()
	} else {
		if in.Action != "" {
			return extension.ActionResult{}, true, pageError("stale")
		}
		// The source chooses version-aware behavior as well as drawing. Final
		// fitting cannot recover in-page navigation after unsupported push/pop.
		var err error
		s, err = h.pageReserve(identity, p.Record.ID, item.ID, hp.proc, slot, params)
		if err != nil {
			return extension.ActionResult{}, true, err
		}
		ps.mu.Lock()
		s.legacy, s.legacyUse = true, true
		s.manifest, s.item = p.Manifest, item
		ps.mu.Unlock()
		defer h.pageLegacyFinish(s)
		native, err := h.pageMount(ctx, s)
		ps.mu.Lock()
		s.busy = false
		if s.latest != nil && s.latest.Generation == native.Generation && s.latest.Version > native.Version {
			native = *s.latest
		}
		s.latest = nil
		s.native = native
		if err == nil && !s.closed && s.run == hp.proc.Generation() {
			err = h.pageAdvanceLocked(s, false, "")
		} else if err == nil {
			err = pageError("expired")
		}
		if err != nil || s.closed {
			h.pageRemoveLocked(s)
			ps.mu.Unlock()
			h.pageDispose(s)
			return extension.ActionResult{}, true, err
		}
		ps.mu.Unlock()
	}
	if in.Action != "" {
		ps.mu.Lock()
		if s.tree.Version() != checkpoint {
			ps.mu.Unlock()
			return extension.ActionResult{}, true, pageError("stale")
		}
		event, ok := legacyPageEvent(s, in.Action, extension.CollectValues(in.Values))
		ps.mu.Unlock()
		if !ok {
			return extension.ActionResult{}, true, pageError("stale")
		}
		request := wire.UIStreamRequest{Request: "event", Protocol: 1, RequestID: randomHex(16), Interaction: &event}
		envelope, _ := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &request})
		frame, err := h.uiStream(ctx, extwire.UIStreamCall{Who: identity, Envelope: envelope})
		if err != nil {
			return extension.ActionResult{}, true, err
		}
		var reply struct {
			Receipt struct {
				Result json.RawMessage `json:"result"`
			} `json:"receipt"`
		}
		_ = json.Unmarshal(frame, &reply)
		presentation = reply.Receipt.Result
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	fitted, err := extension.FitNativeFor(s.tree.ViewJSON(), slot, params.UIVersion, sanitizeUnsupported(in.Context.Unsupported), h.uiFallbackLang(ctx, params.Locale))
	if err != nil {
		return extension.ActionResult{}, true, err
	}
	var view map[string]json.RawMessage
	_ = json.Unmarshal(fitted, &view)
	view["state"], _ = json.Marshal(fmt.Sprintf("%s%s.%s.%d", legacyPagePrefix, s.id, s.epoch, s.tree.Version()))
	fitted, _ = json.Marshal(view)
	result := map[string]json.RawMessage{}
	if len(presentation) != 0 {
		_ = json.Unmarshal(presentation, &result)
	}
	if params.UIVersion < 3 {
		delete(result, "push")
		delete(result, "pop")
	}
	if params.UIVersion < 5 {
		delete(result, "lines")
	}
	result["view"] = fitted
	rawResult, _ := json.Marshal(result)
	if in.nativeResult != nil {
		*in.nativeResult = rawResult
	}
	var dto extension.ActionResult
	_ = json.Unmarshal(rawResult, &dto)
	return dto, true, nil
}

func (h *Manager) pageLegacyFinish(s *pageSession) {
	ps := &h.uiPages
	ps.mu.Lock()
	for s.pauseDone != nil {
		existing := s.pauseDone
		ps.mu.Unlock()
		<-existing
		ps.mu.Lock()
	}
	if s.closed {
		s.legacyUse = false
		ps.mu.Unlock()
		return
	}
	s.visibility++
	s.recovering = false
	s.suspended = true
	s.expires = time.Now().Add(5 * time.Minute)
	done := make(chan struct{})
	s.pauseDone = done
	ps.mu.Unlock()
	_ = h.pagePause(s, done)
	ps.mu.Lock()
	s.legacyUse = false
	ps.mu.Unlock()
}

func legacyPageEvent(s *pageSession, action string, values map[string]string) (wire.UIStreamEvent, bool) {
	if values == nil {
		values = map[string]string{}
	}
	raw, _ := json.Marshal(values)
	event := wire.UIStreamEvent{Protocol: 1, Session: s.id, Epoch: s.epoch, Version: s.tree.Version(), EventID: randomHex(16), Action: action, Value: raw}
	var view any
	_ = json.Unmarshal(s.tree.ViewJSON(), &view)
	keys := []string{s.tree.RootKey()}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if key, ok := node["key"].(string); ok {
				keys = append(keys, key)
			}
			for _, child := range node {
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(view)
	for _, key := range keys {
		event.Key = key
		for _, kind := range []string{"press", "change", "submit"} {
			event.Event = kind
			if _, err := s.tree.EventAction(event); err == nil {
				return event, true
			}
		}
	}
	return event, false
}
