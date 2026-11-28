package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extwire"
)

func pageFixture(t *testing.T, h *Manager, method string, params any) {
	t.Helper()
	p := h.extensions.get("example-page").proc
	if err := p.Call(t.Context(), "test."+method, params, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPageCanceledMountDisposesPendingFactory(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	pageFixture(t, h, "blockRoot", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	request := wire.UIStreamRequest{Request: "open", Protocol: 1, RequestID: "cancel", Extension: "example-page", Contribution: "home", Context: &wire.UIContext{UIVersion: 6, Stream: &wire.UIStreamCapability{Protocol: 1, Components: []string{"text", "button", "row", "code"}}}}
	raw, _ := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &request})
	if _, err := h.uiStream(ctx, extwire.UIStreamCall{Who: pageWho, Envelope: raw}); err == nil {
		t.Fatal("canceled mount succeeded")
	}
	pageFixture(t, h, "releaseRoot", nil)
	stats := pageStats(t, process)
	if stats["disposes"] != 1 || stats["renders"] != 0 {
		t.Fatalf("pending factory retained: %v", stats)
	}
}

func TestPageRecoveryCannotUndoNewerSuspension(t *testing.T) {
	for _, method := range []string{"snapshot", "resume"} {
		t.Run(method, func(t *testing.T) {
			h, process, _ := pageProcessManager(t)
			tree := pageOpenTest(t, h, pageWho)
			lifecycle := func(kind string) (json.RawMessage, error) {
				req := wire.UIStreamRequest{Request: kind, Protocol: 1, RequestID: kind, Session: tree.Session(), Epoch: tree.Epoch()}
				if kind == "snapshot" || kind == "resume" {
					req.Version = tree.Version()
				}
				return pageCall(t, h, pageWho, req)
			}
			if method == "resume" {
				if _, err := lifecycle("suspend"); err != nil {
					t.Fatal(err)
				}
			}
			pageFixture(t, h, "delay", map[string]string{"method": "ui.page." + method})
			done := make(chan error, 1)
			go func() { _, err := lifecycle(method); done <- err }()
			pageAwait(t, func() bool { return pageStats(t, process)["held"] == 1 })
			if _, err := lifecycle("suspend"); err != nil {
				t.Fatal(err)
			}
			pageFixture(t, h, "release", nil)
			if err := <-done; err == nil {
				t.Fatal("older recovery succeeded after newer suspension")
			}
			h.uiPages.mu.Lock()
			s := h.uiPages.sessions[tree.Session()]
			suspended, expires := s.suspended, s.expires
			h.uiPages.mu.Unlock()
			if !suspended || expires.IsZero() {
				t.Fatal("recovery erased latest visibility/expiry")
			}
		})
	}
}

func TestPageRecoveryCoalescesRenderBeforeSourceReply(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	tree := pageOpenTest(t, h, pageWho)
	if _, err := pageCall(t, h, pageWho, wire.UIStreamRequest{Request: "suspend", Protocol: 1, RequestID: "hide", Session: tree.Session(), Epoch: tree.Epoch()}); err != nil {
		t.Fatal(err)
	}
	pageFixture(t, h, "effect", map[string]bool{"enabled": true})
	raw, err := pageCall(t, h, pageWho, wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "resume", Session: tree.Session(), Epoch: tree.Epoch(), Version: tree.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"text":"1"`)) {
		t.Fatalf("recovery lost effect render: %s", raw)
	}
	current := pageTree(h, tree.Session())
	reply, err := pageCall(t, h, pageWho, pageEvent(t, current, "no-ui", "after-resume"))
	if err != nil || !bytes.Contains(reply, []byte(`"status":"accepted"`)) || pageStats(t, process)["writes"] != 1 {
		t.Fatalf("fresh callback rejected: %s %v", reply, err)
	}
}

func TestPageSourceNotificationsDoNotWaitForFittingCapacity(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	tree := pageOpenTest(t, h, pageWho)
	slots := make(chan struct{}, 4)
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	h.uiPages.mu.Lock()
	h.uiPages.renderSlots = slots
	h.uiPages.mu.Unlock()
	defer func() {
		for i := 0; i < cap(slots); i++ {
			<-slots
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	// The newer render is queued, but its source reader can still consume replies.
	if err := process.Call(ctx, "test.update", map[string]int{"count": 9}, nil); err != nil {
		t.Fatal(err)
	}
	if pageTree(h, tree.Session()).Version() != tree.Version() {
		t.Fatal("background fitting ignored its manager-wide bound")
	}
	request := wire.UIStreamRequest{Request: "suspend", Protocol: 1, RequestID: "hide", Session: tree.Session(), Epoch: tree.Epoch()}
	if _, err := pageCall(t, h, pageWho, request); err != nil {
		t.Fatalf("queued fitting blocked lifecycle/source RPC: %v", err)
	}
}

func TestPageAmbiguousWriteEvictionCannotRepeatCallback(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	tree := pageOpenTest(t, h, pageWho)
	first := pageEvent(t, tree, "ambiguous", "failed-write")
	receipt, err := pageCall(t, h, pageWho, first)
	if err != nil || !bytes.Contains(receipt, []byte(`"status":"ambiguous"`)) {
		t.Fatalf("missing ambiguous receipt: %s %v", receipt, err)
	}
	if replay, err := pageCall(t, h, pageWho, first); err != nil || !bytes.Equal(replay, receipt) {
		t.Fatalf("failed write replay: %s %v", replay, err)
	}
	for i := 0; i < 65; i++ {
		if _, err := pageCall(t, h, pageWho, pageEvent(t, pageTree(h, tree.Session()), "no-ui", fmt.Sprintf("evict-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pageCall(t, h, pageWho, first); err == nil {
		t.Fatal("evicted ambiguous event executed again")
	}
	if pageStats(t, process)["writes"] != 66 {
		t.Fatal("ambiguous retry repeated the callback")
	}
}

func TestPageEmptyNegotiatedComponentsRejectsNativeItems(t *testing.T) {
	h, _, _ := pageProcessManager(t)
	_, err := pageCall(t, h, pageWho, wire.UIStreamRequest{Request: "open", Protocol: 1, RequestID: "empty", Extension: "example-page", Contribution: "home", Context: &wire.UIContext{UIVersion: 6, Stream: &wire.UIStreamCapability{Protocol: 1, Components: []string{}}}})
	if err == nil {
		t.Fatal("empty capabilities meant unrestricted native vocabulary")
	}
}

func TestPageLegacyMountCoalescesAndExpiredReadRefreshes(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	pageFixture(t, h, "effect", map[string]bool{"enabled": true})
	var in uiRequest
	in.Context.UIVersion = 6
	result, err := h.uiCall(t.Context(), "example-page", "home", in)
	if err != nil {
		t.Fatal(err)
	}
	if result.View.Sections[0].Items[0].Text != "1" {
		t.Fatal("legacy mount lost effect render")
	}
	pageFixture(t, h, "effect", map[string]bool{"enabled": false})
	in.State = result.View.State
	in.Action = ""
	// Look up the named button rather than relying on native layout order.
	for _, item := range result.View.Sections[0].Items {
		if item.Key == "no-ui" {
			in.Action = item.Action.ID
		}
	}
	if _, err = h.uiCall(t.Context(), "example-page", "home", in); err != nil {
		t.Fatal(err)
	}
	if pageStats(t, process)["writes"] != 1 {
		t.Fatal("first legacy callback lost to pending render")
	}
	h.pageSweep(time.Now().Add(6 * time.Minute))
	if _, err = h.uiCall(t.Context(), "example-page", "home", in); err == nil {
		t.Fatal("expired write recreated a page")
	}
	in.Action = ""
	if _, err = h.uiCall(t.Context(), "example-page", "home", in); err != nil {
		t.Fatalf("read-only expired checkpoint cannot refresh: %v", err)
	}
}

func TestPageLegacyPauseRetainsRequestOwnership(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	pageFixture(t, h, "delay", map[string]string{"method": "ui.page.suspend"})
	defer pageFixture(t, h, "release", nil)
	var in uiRequest
	in.Context.UIVersion = 6
	first := make(chan error, 1)
	initial := in
	go func() { _, err := h.uiCall(t.Context(), "example-page", "home", initial); first <- err }()
	pageAwait(t, func() bool { return pageStats(t, process)["held"] == 1 })
	h.uiPages.mu.Lock()
	for _, s := range h.uiPages.sessions {
		in.State = fmt.Sprintf("%s%s.%s.%d", legacyPagePrefix, s.id, s.epoch, s.tree.Version())
	}
	h.uiPages.mu.Unlock()
	second := make(chan error, 1)
	go func() { _, err := h.uiCall(t.Context(), "example-page", "home", in); second <- err }()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("overlapping HTTP lifecycle accepted")
		}
	case <-time.After(150 * time.Millisecond):
		t.Error("deferred pause did not retain HTTP request ownership")
	}
	pageFixture(t, h, "release", nil)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestPageCloseCancelsAwaitingMount(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	pageFixture(t, h, "blockRoot", nil)
	defer pageFixture(t, h, "releaseRoot", nil)
	opened := make(chan error, 1)
	go func() {
		_, err := pageCall(t, h, pageWho, wire.UIStreamRequest{Request: "open", Protocol: 1, RequestID: "mount", Extension: "example-page", Contribution: "home", Context: &wire.UIContext{UIVersion: 6, Stream: &wire.UIStreamCapability{Protocol: 1, Components: []string{"text", "button", "row", "code"}}}})
		opened <- err
	}()
	var session, epoch string
	pageAwait(t, func() bool {
		h.uiPages.mu.Lock()
		defer h.uiPages.mu.Unlock()
		for _, s := range h.uiPages.sessions {
			if s.mountDone != nil && s.run != 0 {
				session, epoch = s.id, s.epoch
				return true
			}
		}
		return false
	})
	closed := make(chan error, 1)
	go func() {
		_, err := pageCall(t, h, pageWho, wire.UIStreamRequest{Request: "close", Protocol: 1, RequestID: "close", Session: session, Epoch: epoch})
		closed <- err
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close waited for the unresolved root factory")
	}
	select {
	case err := <-opened:
		if err == nil {
			t.Fatal("closed mount succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed mount was not canceled")
	}
	pageFixture(t, h, "releaseRoot", nil)
	if stats := pageStats(t, process); stats["disposes"] != 1 || stats["renders"] != 0 {
		t.Fatalf("closed factory retained: %v", stats)
	}
}

func TestPageLegacySessionCannotRebindToAppSocket(t *testing.T) {
	h, _, _ := pageProcessManager(t)
	var in uiRequest
	in.Context.UIVersion = 6
	if _, err := h.uiCall(t.Context(), "example-page", "home", in); err != nil {
		t.Fatal(err)
	}
	h.uiPages.mu.Lock()
	var s *pageSession
	for _, candidate := range h.uiPages.sessions {
		s = candidate
	}
	who := s.who
	who.Connection = "app-socket"
	request := wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "resume", Session: s.id, Epoch: s.epoch, Version: s.tree.Version()}
	h.uiPages.mu.Unlock()
	if _, err := pageCall(t, h, who, request); err == nil {
		t.Fatal("HTTP-only session rebound to app socket")
	}
	h.uiPages.mu.Lock()
	defer h.uiPages.mu.Unlock()
	if s.who.Connection != "http" || !s.suspended {
		t.Fatal("rejected rebind changed HTTP ownership or visibility")
	}
}
