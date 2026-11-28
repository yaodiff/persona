package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type briefingMirrorPost struct{ UUID, Card string }

type briefingMirrorPosts struct {
	mu    sync.Mutex
	posts []briefingMirrorPost
}

// The generic mirror uses the same retry helper as a briefing. This fixture has no scheduled-run watcher, so only
// the retry under test reads status after the first post fails. Both results keep posting disabled while uncertain.
func TestFeishuMirrorStatusRetryBoundary(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name      string
		statusErr error
		wantReads int32
	}{
		{name: "temporary outage uses only the existing slots", statusErr: errors.New("status transport unavailable"), wantReads: 3},
		{name: "definitive refusal stops immediately", statusErr: &rpc.Error{Code: "forbidden", Message: "status refused"}, wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := mirrorRetryHub(t, func(int) error { return errLost })
			synctest.Test(t, func(t *testing.T) {
				var unavailable atomic.Bool
				var failedReads atomic.Int32
				w.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
					if unavailable.Load() {
						failedReads.Add(1)
						return nil, tc.statusErr
					}
					return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
						Features: FeishuFeatures{Main: true, Sync: true}}, nil
				})
				w.s.feishuMirror(context.Background(), "server", "m1")
				if len(w.tried()) != 1 {
					t.Fatal("the first mirrored post did not reach the failing fake transport")
				}
				unavailable.Store(true)
				time.Sleep(time.Duration(feishuMirrorRetries+1)*quickMirrorRetry + time.Nanosecond) // sleep-ok: advance only synctest fake time beyond the finite retry budget
				synctest.Wait()
				if got := failedReads.Load(); got != tc.wantReads {
					t.Errorf("failed status reads = %d, want %d", got, tc.wantReads)
				}
				if got := len(w.tried()); got != 1 {
					t.Errorf("posted without confirmed status: %d calls", got)
				}
			})
		})
	}
}

func (p *briefingMirrorPosts) record(raw json.RawMessage) (int, error) {
	var post briefingMirrorPost
	if err := json.Unmarshal(raw, &post); err != nil {
		return 0, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts = append(p.posts, post)
	return len(p.posts), nil
}

func (p *briefingMirrorPosts) snapshot() []briefingMirrorPost {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]briefingMirrorPost(nil), p.posts...)
}

// This drives the scheduled run's production completion callback. The fake model's HTTP server and runNow setup
// stay outside synctest; only direct fake app calls, channels and retry timers belong to the virtual-clock bubble.
func TestBriefingMirrorCompletion(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name                        string
		postError                   error
		statusGap, replay, inFlight bool
		disabled                    bool
		wantPosts                   int
	}{
		{name: "normal", wantPosts: 1},
		{name: "temporary post failure", postError: errors.New("HTTP 503"), wantPosts: 2},
		{name: "unknown post result", postError: errLost, wantPosts: 2},
		{name: "temporary status failure during retry", postError: errors.New("HTTP 503"), statusGap: true, wantPosts: 2},
		{name: "replayed completion", replay: true, wantPosts: 1},
		{name: "completion while post is pending", inFlight: true, wantPosts: 1},
		{name: "permanent post refusal", postError: &botcard.APIError{Op: "send", Code: 230002}, wantPosts: 1},
		{name: "push channel disabled", disabled: true, wantPosts: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBriefRunEnv(t, true) // includes the real runTask/runNow -> briefSent path
			synctest.Test(t, func(t *testing.T) {
				var posts briefingMirrorPosts
				var statusUnavailable atomic.Bool
				e.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
					if statusUnavailable.Load() {
						return nil, errors.New("temporary status transport failure")
					}
					return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
						Features: FeishuFeatures{Main: true, Pushes: !tc.disabled, Reports: true}}, nil
				})
				entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				releasePost := func() { releaseOnce.Do(func() { close(release) }) }
				t.Cleanup(releasePost)
				e.f.answerAppBlocking(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
					n, err := posts.record(raw)
					if err != nil {
						return nil, err
					}
					if n == 1 {
						if tc.inFlight {
							close(entered)
							<-release
						}
						if tc.postError != nil {
							return nil, tc.postError
						}
					}
					return map[string]any{"message_id": "om_briefing_test"}, nil
				})
				complete := func() { e.s.channelTurnEnded(context.Background(), e.sess.MachineID, e.sess.SessionID) }
				if tc.inFlight {
					go func() { defer close(finished); complete() }()
					select {
					case <-entered:
					case <-time.After(time.Minute):
						t.Fatal("completion did not enter the fake first post")
					}
					complete()
					releasePost()
					<-finished
				} else {
					complete()
				}
				if tc.replay {
					complete()
				}
				if tc.statusGap {
					statusUnavailable.Store(true)
					time.Sleep(feishuMirrorRetry) // sleep-ok: advance synctest-only retry time; no wall-clock wait
					synctest.Wait()
					if got := len(posts.snapshot()); got != 1 {
						t.Fatalf("posted while status was unknown: %d calls", got)
					}
					statusUnavailable.Store(false)
				}
				time.Sleep(time.Duration(feishuMirrorRetries+1) * feishuMirrorRetry) // sleep-ok: exhaust the bounded synctest-only retry window, not real time
				synctest.Wait()
				got := posts.snapshot()
				if len(got) != tc.wantPosts {
					t.Errorf("Feishu post count = %d, want %d; posts=%+v", len(got), tc.wantPosts, got)
				}
				for i, post := range got {
					if !strings.HasPrefix(post.UUID, "b") || post.UUID != got[0].UUID || post.Card != got[0].Card {
						t.Errorf("post %d lost the original briefing UUID/card: %+v", i, post)
					}
					if !strings.Contains(post.Card, "旧的问题") {
						t.Errorf("post %d lost the original facts", i)
					}
				}
			})
		})
	}

	t.Run("claimed completion after facts expiry", func(t *testing.T) {
		e := newBriefRunEnv(t, true)
		complete := func() { e.s.channelTurnEnded(context.Background(), e.sess.MachineID, e.sess.SessionID) }
		complete()
		if len(e.cards()) != 1 {
			t.Fatal("the initial briefing was not posted")
		}
		// Replace immutable facts rather than modifying an object another run observer may still read.
		run := *e.s.briefOf(e.sess.SessionID)
		facts := *run.Facts
		facts.At = time.Now().Add(-briefKeep - time.Minute)
		run.Facts = &facts
		e.s.briefSetSessionRun(e.sess.SessionID, &run)
		complete()
		if got := len(e.cards()); got != 1 {
			t.Errorf("same retained completion posted again after facts expired: %d cards", got)
		}
		// Suppressing a retained briefing claim must not swallow the owner's next ordinary turn.
		e.f.setItems(proto.Item{ID: "followup-user", Role: "user", Kind: proto.KindText, Text: "Explain the next step"},
			proto.Item{ID: "followup-answer", Role: "assistant", Kind: proto.KindText, Text: "An ordinary follow-up answer"})
		before := len(e.cards())
		complete()
		cards := e.cards()
		if len(cards) != before+1 || !strings.Contains(cards[len(cards)-1], "An ordinary follow-up answer") {
			t.Fatal("the ordinary follow-up did not receive its own card")
		}
	})

	t.Run("later routine in the same conversation", func(t *testing.T) {
		e := newBriefRunEnv(t, true)
		complete := func() { e.s.channelTurnEnded(context.Background(), e.sess.MachineID, e.sess.SessionID) }
		complete()
		next, err := runNow(t, e.s, "morning")
		if err != nil || next.SessionID != e.sess.SessionID {
			t.Fatalf("second scheduled run did not reuse its conversation: %+v, %v", next, err)
		}
		e.f.setItems(proto.Item{ID: "next-user", Role: "user", Kind: proto.KindText, Text: "⏰ another briefing"},
			proto.Item{ID: "next-answer", Role: "assistant", Kind: proto.KindText, Text: "The next routine's answer"})
		complete()
		cards := e.cards()
		if len(cards) != 2 || !strings.Contains(cards[1], "The next routine's answer") || !strings.Contains(cards[1], "旧的问题") {
			t.Fatalf("second routine did not retain its own facts and answer: %v", cards)
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.posts[0]["uuid"] == nil || e.posts[1]["uuid"] == nil || e.posts[0]["uuid"] == e.posts[1]["uuid"] {
			t.Fatalf("separate routine turns shared a UUID: %v", e.posts)
		}
	})
}
