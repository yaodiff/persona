package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// The audio socket and the gap uploads (docs/assistant/meeting-minutes.md "Audio from the phone", "Gaps and the final
// pass"). Names and texts are placeholders.

// ServeSocket serves exactly the WebSocket routes the hub relays to it.
func TestSocketRoutesAreTheWireOnes(t *testing.T) {
	var got []string
	for _, r := range socketRoutes {
		got = append(got, r.pattern)
	}
	if !slices.Equal(got, assistantwire.SocketRoutes) {
		t.Fatalf("socket routes %v, the wire's %v", got, assistantwire.SocketRoutes)
	}
}

// The upload's 200 says the mode in effect: hub when asked and ready, device with unavailable when the contribution
// is not ready, device on a meeting naming none, not_meeting for hub asked of a note; the mode never changes after.
func TestTranscriptionModeOnUpload(t *testing.T) {
	h := newHubFixture(t)
	if out := h.hubUpload(meetingRec, 1, nil); out["transcription"] != "hub" || out["transcription_problem"] != nil {
		t.Fatalf("hub: %v", out)
	}
	if out := h.upload(meetingRec, 2, map[string]any{"transcription": "device"}); out["transcription"] != "hub" {
		t.Fatalf("a later upload changed the mode: %v", out)
	}
	if out := h.upload(meetingOther, 1, nil); out["transcription"] != "device" || out["transcription_problem"] != nil {
		t.Fatalf("a meeting naming none: %v", out)
	}
	note := "66666666-6666-4666-8666-666666666666"
	if out := h.upload(note, 1, map[string]any{"kind": "note", "transcription": "hub"}); out["transcription"] != "device" ||
		out["transcription_problem"] != "not_meeting" {
		t.Fatalf("a note: %v", out)
	}
	if out := h.upload("77777777-7777-4777-8777-777777777777", 1, map[string]any{"kind": "note"}); out["transcription"] != nil {
		t.Fatalf("a note naming none: %v", out)
	}
	h.tr.set(func(f *fakeTranscriber) { f.infoErr = errDial })
	h.s.transcriptionReadinessChanged()
	notReady := "88888888-8888-4888-8888-888888888888"
	if out := h.hubUpload(notReady, 1, nil); out["transcription"] != "device" || out["transcription_problem"] != "unavailable" {
		t.Fatalf("not ready: %v", out)
	}
	if tr := h.transcription(notReady); tr["mode"] != "device" || tr["state"] != nil {
		t.Fatalf("device mode's transcription: %v", tr)
	}
	if tr := h.transcription(meetingRec); tr["mode"] != "hub" || tr["state"] != "live" || tr["gaps"] == nil {
		t.Fatalf("hub mode's transcription: %v", tr)
	}
	// POST …/live of a device-mode meeting says its transcription too (once a mode was named); detail.meta has it.
	device := "14141414-1414-4414-8414-141414141414"
	h.upload(device, 1, map[string]any{"attendance": "listener", "transcription": "device"})
	out := h.do("POST", livePath(device), "", `{"from":0,"segments":[{"start":1,"end":2,"text":"Hello"}]}`, http.StatusOK)
	if tr, _ := out["transcription"].(map[string]any); tr["mode"] != "device" || out["revs"] != nil {
		t.Fatalf("POST …/live: %v", out)
	}
	// Not closed after gaps: the not-ready transcription above ends the hub run, which may already have turned on the
	// phone's fallback ("fallback":true follows gaps) by now.
	d, err := h.engine().Get(kb.KindRecording, meetingRec, 0)
	if err != nil || !strings.Contains(string(d.Meta), `"transcription":{"mode":"hub","state":"live","gaps":[]`) {
		t.Fatalf("detail.meta: %s %v", d.Meta, err)
	}
}

// Before the upgrade only the caller (403) and the knowledge base (503) refuse the socket.
func TestAudioSocketRefusedBeforeTheUpgrade(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	url := "ws" + strings.TrimPrefix(h.sockets.URL, "http") + "/api/v1/recordings/" + meetingRec + "/audio"
	_, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: http.Header{RoleHeader: {"answer"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("answer role: %v %v", err, resp)
	}
	noKB := newTestService(t, newFakeHub())
	srv := httptest.NewServer(http.HandlerFunc(noKB.ServeSocket))
	defer srv.Close()
	_, resp, err = websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/recordings/"+meetingRec+"/audio", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no knowledge base: %v %v", err, resp)
	}
}

// The recording's refusals come as the first frame after the upgrade, a fatal error, in the contract's order, and the
// socket closes with 1008.
func TestAudioSocketRefusalsAreTheFirstFrame(t *testing.T) {
	h := newHubFixture(t)
	refused := func(id, code string) {
		t.Helper()
		p := dialPhone(t, h.sockets, id)
		f := p.wait("error", 1)
		if f["code"] != code || f["fatal"] != true {
			t.Fatalf("%s: %v, want %s", id, f, code)
		}
		if got := p.waitClosed(); got != websocket.StatusPolicyViolation {
			t.Fatalf("%s: closed %v", id, got)
		}
		if len(p.of("ready")) > 0 {
			t.Fatalf("%s: ready after a refusal", id)
		}
	}
	refused(meetingRec, codeRecordingNotFound) // no upload yet
	h.hubUpload(meetingRec, 1, nil)
	h.do("DELETE", "/api/v1/recordings/"+meetingRec, "", nil, http.StatusOK)
	refused(meetingRec, codeRecordingDeleted)

	h.upload(meetingOther, 1, nil) // device mode
	refused(meetingOther, codeNotHub)

	final := "99999999-9999-4999-8999-999999999999"
	h.hubUpload(final, 1, nil)
	if _, err := h.engine().TranscriptionUpdate(final, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.FinalFrom = time.Now()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refused(final, codeTranscriptionFinal)

	h.f.mu.Lock()
	h.f.hello.KB = &KBSettings{ReadOnly: true}
	h.f.mu.Unlock()
	refused(meetingOther, "kb_read_only") // before not_hub
	refused("12121212-1212-4212-8212-121212121212", codeRecordingNotFound)
}

// A trimmed recording's socket is refused transcript_expired.
func TestAudioSocketTrimmed(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, map[string]any{"complete": true, "duration": 60.0, "ended_at": time.Now().UTC().Format(time.RFC3339),
		"segments": []map[string]any{{"start": 0, "end": 9, "text": "We keep the cap."}}})
	e := h.engine()
	if _, err := e.TranscriptionUpdate(meetingRec, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.State = kb.TranscriptionFailed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.minutesFor(meetingRec, `{"title":"Design review","decisions":[{"text":"Keep the cap","line":1}]}`)
	e.PruneRecordings(time.Now().AddDate(0, 0, 30))
	if rec, _ := e.Store.Recording(meetingRec); rec == nil || !rec.Trimmed() {
		t.Fatal("not trimmed")
	}
	p := dialPhone(t, h.sockets, meetingRec)
	if f := p.wait("error", 1); f["code"] != codeTranscriptExpired {
		t.Fatalf("trimmed: %v", f)
	}
	if tr := h.transcription(meetingRec); tr["mode"] != "hub" || tr["state"] != "failed" || len(tr["gaps"].([]any)) != 0 {
		t.Fatalf("a trimmed recording's transcription: %v", tr)
	}
}

// start within audioStartWait, no audio before ready; ready for a fresh recording is {from 0, gaps [], next 0}; the
// hub acknowledges what it stored; a reconnect resends from ready.from; a seek forward leaves a gap, one back is
// harmless; an odd frame is bad_frame.
func TestAudioSocketPositions(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	p, ready := h.open(meetingRec, 0, nil)
	if ready["from"] != 0.0 || len(ready["gaps"].([]any)) != 0 || ready["next"] != 0.0 {
		t.Fatalf("a fresh recording's ready: %v", ready)
	}
	p.audio(2)
	waitFor(t, "ack at 2", func() bool {
		acks := p.of("ack")
		return len(acks) > 0 && acks[len(acks)-1]["at"] == 2.0
	})
	p.send(map[string]any{"type": "seek", "at": 5.0})
	p.audio(1)
	waitFor(t, "ack at 6", func() bool {
		acks := p.of("ack")
		return len(acks) > 0 && acks[len(acks)-1]["at"] == 6.0
	})
	p.send(map[string]any{"type": "seek", "at": 0.5}) // held already: ignored
	p.audio(0.5)
	waitFor(t, "ack around the latest frames", func() bool { // the contiguous audio around them
		acks := p.of("ack")
		return len(acks) > 0 && acks[len(acks)-1]["at"] == 2.0
	})
	_ = p.c.Close(websocket.StatusNormalClosure, "")

	again, ready := h.open(meetingRec, 0, nil)
	gaps := ready["gaps"].([]any)
	if ready["from"] != 6.0 || len(gaps) != 1 || gaps[0].([]any)[0] != 2.0 || gaps[0].([]any)[1] != 5.0 {
		t.Fatalf("a reconnect's ready: %v", ready)
	}
	if tr := h.transcription(meetingRec); len(tr["gaps"].([]any)) != 1 {
		t.Fatalf("the transcription's gaps: %v", tr)
	}
	if err := again.c.Write(context.Background(), websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if f := again.wait("error", 1); f["code"] != codeBadFrame {
		t.Fatalf("an odd frame: %v", f)
	}
	again.waitClosed()

	late := dialPhone(t, h.sockets, meetingRec)
	if err := late.c.Write(context.Background(), websocket.MessageBinary, make([]byte, 320)); err != nil {
		t.Fatal(err)
	}
	if f := late.wait("error", 1); f["code"] != codeBadFrame {
		t.Fatalf("audio before ready: %v", f)
	}
	late.waitClosed()
	audioStartWait = 100 * time.Millisecond
	quiet := dialPhone(t, h.sockets, meetingRec)
	if f := quiet.wait("error", 1); f["code"] != codeBadFrame || len(quiet.of("ready")) > 0 {
		t.Fatalf("no start: %v", f)
	}
}

// A start frame with a whole timeline at the contract's limits (kb.MaxTimelineRoles role changes, kb.MaxTimelineSpans
// spans with 4000 bytes of answer each, about 4 MB) is taken: ready, and the timeline joins meeting.json's.
func TestAudioSocketStartTakesAWholeTimeline(t *testing.T) {
	for _, tc := range []struct{ name, said string }{
		{"utf8", strings.Repeat("答", 1333) + "a"}, // 4000 bytes, i18n-ignore: synthetic speech
		{"escaped", strings.Repeat(`"`, 4000)},    // 4000 bytes that JSON doubles
	} {
		t.Run(tc.name, func(t *testing.T) { wholeTimeline(t, tc.said) })
	}
}

func wholeTimeline(t *testing.T, said string) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	roles := make([]map[string]any, 0, kb.MaxTimelineRoles)
	for i := range kb.MaxTimelineRoles {
		roles = append(roles, map[string]any{"at": float64(i * 30), "attendance": []string{"listener", "participant"}[i%2]})
	}
	spans := make([]map[string]any, 0, kb.MaxTimelineSpans)
	for i := range kb.MaxTimelineSpans {
		spans = append(spans, map[string]any{"start": float64(i * 10), "end": float64(i*10 + 5), "text": said})
	}
	tl := map[string]any{"roles": roles, "speaking": spans}
	if raw, _ := json.Marshal(map[string]any{"type": "start", "at": 0, "timeline": tl}); len(raw) < 4_000_000 {
		t.Fatalf("the start frame is %d bytes, not a whole timeline at the limits", len(raw))
	}
	p, ready := h.open(meetingRec, 0, tl)
	if ready["from"] != 0.0 || len(p.of("error")) > 0 {
		t.Fatalf("ready %v, errors %v", ready, p.of("error"))
	}
	var m kb.Meeting
	waitFor(t, "the timeline in meeting.json", func() bool { // joined after ready
		m, _, _ = h.engine().MeetingRead(meetingRec)
		return m.Transcription != nil && m.Transcription.Timeline != nil
	})
	if got := m.Transcription.Timeline; len(got.Roles) != kb.MaxTimelineRoles || len(got.Speaking) != kb.MaxTimelineSpans ||
		got.Speaking[999] != (kb.SpokenSpan{Start: 9990, End: 9995, Text: said}) {
		t.Fatalf("the timeline has %d roles and %d spans", len(got.Roles), len(got.Speaking))
	}
}

// A new socket replaces the one before (replaced); end is acknowledged at end.at and closes 1000, with the
// recording's end stored.
func TestAudioSocketReplacedAndEnd(t *testing.T) {
	h := newHubFixture(t)
	h.hubUpload(meetingRec, 1, nil)
	first, _ := h.open(meetingRec, 0, nil)
	first.audio(1)
	waitFor(t, "ack at 1", func() bool {
		acks := first.of("ack")
		return len(acks) > 0 && acks[len(acks)-1]["at"] == 1.0
	})
	second, ready := h.open(meetingRec, 0, nil)
	if f := first.wait("error", 1); f["code"] != codeReplaced {
		t.Fatalf("the older socket: %v", f)
	}
	first.waitClosed()
	if ready["from"] != 1.0 {
		t.Fatalf("ready after a replace: %v", ready)
	}
	second.audio(1)
	second.send(map[string]any{"type": "end", "at": 2.0})
	if got := second.waitClosed(); got != websocket.StatusNormalClosure {
		t.Fatalf("after end: %v", got)
	}
	acks := second.of("ack")
	if len(acks) == 0 || acks[len(acks)-1]["at"] != 2.0 {
		t.Fatalf("acks: %v", acks)
	}
	m, _, err := h.engine().MeetingRead(meetingRec)
	if err != nil || m.Transcription.End != 2 {
		t.Fatalf("the end: %+v %v", m.Transcription, err)
	}
}

// PUT …/audio?at= stores what the hub lacks and answers the gaps left; its refusals in the contract's order.
func TestAudioPut(t *testing.T) {
	h := newHubFixture(t)
	put := func(id, at string, body []byte, role string, want int) map[string]any {
		t.Helper()
		path := "/api/v1/recordings/" + id + "/audio"
		if at != "-" {
			path += "?at=" + at
		}
		req, _ := http.NewRequest("PUT", h.srv.URL+path, bytes.NewReader(body))
		if role != "" {
			req.Header.Set(RoleHeader, role)
		}
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("PUT %s: %d %s, want %d", path, resp.StatusCode, raw, want)
		}
		out := map[string]any{}
		_ = json.Unmarshal(raw, &out)
		return out
	}
	second := make([]byte, kb.AudioRate*2)
	put(meetingRec, "0", second, "answer", http.StatusForbidden)
	put(meetingRec, "0", make([]byte, maxAudioBody+2), "", http.StatusRequestEntityTooLarge)
	wantCode(t, put(meetingRec, "0", second, "", http.StatusNotFound), codeRecordingNotFound)
	h.hubUpload(meetingRec, 1, map[string]any{"complete": true, "duration": 3.0})
	for _, at := range []string{"-", "-1", "x", "1e2", ""} {
		wantCode(t, put(meetingRec, at, second, "", http.StatusBadRequest), codeBadAudio)
	}
	wantCode(t, put(meetingRec, "0", []byte{1, 2, 3}, "", http.StatusBadRequest), codeBadAudio)
	out := put(meetingRec, "1", second, "", http.StatusOK)
	gaps := out["gaps"].([]any)
	if len(gaps) != 2 || gaps[0].([]any)[1] != 1.0 || gaps[1].([]any)[0] != 2.0 || gaps[1].([]any)[1] != 3.0 {
		t.Fatalf("gaps: %v", out)
	}
	out = put(meetingRec, "0", append(slices.Clone(second), second...), "", http.StatusOK)
	if gaps := out["gaps"].([]any); len(gaps) != 1 {
		t.Fatalf("gaps after the second: %v", out)
	}
	out = put(meetingRec, "2", second, "", http.StatusOK)
	if gaps := out["gaps"].([]any); len(gaps) != 0 {
		t.Fatalf("no gap left: %v", out)
	}
	h.upload(meetingOther, 1, nil)
	wantCode(t, put(meetingOther, "0", second, "", http.StatusConflict), codeNotHub)
	if _, err := h.engine().TranscriptionUpdate(meetingRec, func(t *kb.MeetingTranscription, _ kb.Recording) error {
		t.FinalFrom = time.Now()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, put(meetingRec, "0", second, "", http.StatusConflict), codeTranscriptionFinal)
	h.do("DELETE", "/api/v1/recordings/"+meetingOther, "", nil, http.StatusOK)
	wantCode(t, put(meetingOther, "0", second, "", http.StatusGone), codeRecordingDeleted)
	h.f.mu.Lock()
	h.f.hello.KB = &KBSettings{ReadOnly: true}
	h.f.mu.Unlock()
	wantCode(t, put(meetingRec, "0", second, "", http.StatusForbidden), "kb_read_only")
	wantCode(t, put(meetingRec, "-", second, "", http.StatusBadRequest), codeBadAudio) // 400 before 403
}
