package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

const recoveryFirstID = "10000000-0000-4000-8000-000000000001"
const recoveryFreshID = "10000000-0000-4000-8000-000000000002"

type recoveryReplier struct {
	extensionLark
	mu       sync.Mutex
	requests []connector.InboxReply
	err      error
	entered  chan struct{}
	release  chan struct{}
	unblock  sync.Once
}

func (c *recoveryReplier) Reply(ctx context.Context, r connector.InboxReply) error {
	c.mu.Lock()
	c.requests = append(c.requests, r)
	first, err, entered, release := len(c.requests) == 1, c.err, c.entered, c.release
	c.mu.Unlock()
	if first && entered != nil {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (c *recoveryReplier) calls() []connector.InboxReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]connector.InboxReply(nil), c.requests...)
}
func (c *recoveryReplier) answer(err error) { c.mu.Lock(); c.err = err; c.mu.Unlock() }
func (c *recoveryReplier) proceed()         { c.unblock.Do(func() { close(c.release) }) }

func recoverySuggestion(t *testing.T, handed bool, c *recoveryReplier) (*inboxHub, *InboxItem) {
	t.Helper()
	h, _, it := handledHub(t)
	h.in.mu.Lock()
	h.in.conns = []connector.Connector{c}
	h.in.mu.Unlock()
	if handed {
		setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
		handOff(t, h, it)
		if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"Original reply"}`); err != nil {
			t.Fatal(err)
		}
	} else if _, err := suggestTool(t, h, `{"kind":"reply","text":"Original reply"}`); err != nil {
		t.Fatal(err)
	}
	return h, it
}

func recoveryAttempt(t *testing.T, h *inboxHub, it *InboxItem, version int) InboxSuggestionAttempt {
	t.Helper()
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	a := suggestionAttempt(it, version)
	if a == nil {
		t.Fatalf("no result retained for version %d", version)
	}
	return *a
}

func TestInboxReplyFailureKeepsCurrentSuggestion(t *testing.T) {
	for _, handed := range []bool{false, true} {
		name := "processing"
		if handed {
			name = "handed"
		}
		t.Run(name, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.in.mu.Lock()
			h.in.conns = []connector.Connector{&codedReplier{err: &rpc.Error{Code: assistantwire.CodeInboxSendFailed, Message: "not sent"}}}
			h.in.mu.Unlock()
			if handed {
				setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
				handOff(t, h, it)
				if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"Example reply"}`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := suggestTool(t, h, `{"kind":"reply","text":"Example reply"}`); err != nil {
				t.Fatal(err)
			}
			code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"10000000-0000-4000-8000-000000000001"}`)
			if code != 502 {
				t.Fatalf("expected a definite send failure, got %d", code)
			}
			h.inbox.mu.Lock()
			g := currentSuggestion(it)
			h.inbox.mu.Unlock()
			if g == nil || g.Version != 1 || g.Text != "Example reply" {
				t.Fatalf("known not-delivered reply lost its actionable suggestion: %+v", g)
			}
		})
	}
}

func TestInboxRepeatedReplySuggestionKeepsVersion(t *testing.T) {
	h, _, it := handledHub(t)
	for range 2 {
		if _, err := suggestTool(t, h, `{"kind":"reply","text":"Example reply"}`); err != nil {
			t.Fatal(err)
		}
	}
	h.inbox.mu.Lock()
	g := currentSuggestion(it)
	h.inbox.mu.Unlock()
	if g == nil || g.Version != 1 {
		t.Fatalf("identical unsent reply replaced the current card: %+v", g)
	}
}

func TestInboxReplyRetryUsesFirstIdentityAndEditedPayload(t *testing.T) {
	for _, handed := range []bool{false, true} {
		t.Run(fmt.Sprint(handed), func(t *testing.T) {
			c := &recoveryReplier{err: &rpc.Error{Code: assistantwire.CodeInboxSendRateLimited, Data: json.RawMessage(`{"retry_after":30}`)}}
			h, it := recoverySuggestion(t, handed, c)
			code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`)
			if code != 429 || out["delivered"] != "none" || out["retry_after"] != float64(30) || out["item"] == nil {
				t.Fatalf("failure: %d %v", code, out)
			}
			a := recoveryAttempt(t, h, it, 1)
			if a.Status != suggestionFailed || !a.Retryable || a.RetryAfter != 30 || a.Text != "Edited reply" || a.ClientID != recoveryFirstID {
				t.Fatalf("attempt: %+v", a)
			}
			h.inbox.mu.Lock()
			public, _ := json.Marshal(it.listed())
			h.inbox.mu.Unlock()
			if strings.Contains(string(public), recoveryFirstID) || strings.Contains(string(public), `"proposal"`) || strings.Contains(string(public), `"source_of"`) {
				t.Fatalf("internal bindings listed: %s", public)
			}
			// The new UI omits text on Retry; a fresh default UUID must not become a second send identity.
			c.answer(nil)
			code, out = suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"`+recoveryFreshID+`"}`)
			calls := c.calls()
			if code != 200 || len(calls) != 2 || calls[0].ClientID != recoveryFirstID || calls[1].ClientID != recoveryFirstID || calls[1].Text != "Edited reply" {
				t.Fatalf("retry: %d %v calls=%+v", code, out, calls)
			}
			if a := recoveryAttempt(t, h, it, 1); a.Status != suggestionSent || a.Retryable {
				t.Fatalf("success: %+v", a)
			}
			if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 409 || len(c.calls()) != 2 {
				t.Fatal("completed confirmation was sent again")
			}
		})
	}
}

func TestInboxUnknownReplyCannotRetryOrRegenerateEditedPayload(t *testing.T) {
	for _, err := range []error{rpc.ErrDropped, errors.New("unclassified transport failure")} {
		t.Run(fmt.Sprint(err), func(t *testing.T) {
			c := &recoveryReplier{err: err}
			h, it := recoverySuggestion(t, false, c)
			code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`)
			if code != 502 || out["delivered"] != "unknown" || out["retryable"] != false || out["item"] == nil {
				t.Fatalf("unknown: %d %v", code, out)
			}
			if _, err := suggestTool(t, h, `{"kind":"reply","text":"Edited reply"}`); err != nil {
				t.Fatal(err)
			}
			if _, err := suggestTool(t, h, `{"kind":"reply","text":"Edited reply","note":"A different explanation"}`); err != nil {
				t.Fatal(err)
			}
			h.inbox.mu.Lock()
			version := it.Suggestion.Version
			h.inbox.mu.Unlock()
			if version != 1 {
				t.Fatal("edited uncertain payload acquired a fresh version and could be resent")
			}
			code, out = suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"`+recoveryFreshID+`"}`)
			if code != 502 || out["delivered"] != "unknown" || len(c.calls()) != 1 {
				t.Fatalf("blind retry: %d %v calls=%+v", code, out, c.calls())
			}
			if a := recoveryAttempt(t, h, it, 1); a.Status != suggestionUnknown || a.Retryable || a.ClientID != recoveryFirstID {
				t.Fatalf("attempt: %+v", a)
			}
			if agent, ok := out["item"].(map[string]any)["agent"].(map[string]any); ok && (agent["draft"] != "" && agent["draft"] != nil || agent["state"] == AgentDrafted) {
				t.Fatalf("legacy draft can bypass the versioned uncertain guard: %v", agent)
			}
			if code, item := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/"+it.ID, ""); code != 200 {
				t.Fatalf("readback: %d %v", code, item)
			} else if agent, ok := item["agent"].(map[string]any); ok && (agent["draft"] != "" && agent["draft"] != nil || agent["state"] == AgentDrafted) {
				t.Fatalf("reload exposes a legacy Send draft: %v", agent)
			}
		})
	}
}

func TestInboxHistoricalUnknownActionCannotAcquireNewIdentity(t *testing.T) {
	for _, handed := range []bool{false, true} {
		for _, reoffer := range []bool{false, true} {
			t.Run(fmt.Sprintf("handed=%t/reoffer=%t", handed, reoffer), func(t *testing.T) {
				c := &recoveryReplier{err: rpc.ErrDropped}
				h, it := recoverySuggestion(t, handed, c)
				if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`); code != 502 || out["delivered"] != "unknown" {
					t.Fatalf("initial uncertain send: %d %v", code, out)
				}
				propose := func(text string) {
					t.Helper()
					var err error
					if handed {
						_, err = mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"`+text+`"}`)
					} else {
						_, err = suggestTool(t, h, `{"kind":"reply","text":"`+text+`"}`)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				propose("A different action")
				if reoffer {
					propose("Edited reply")
				}
				h.inbox.mu.Lock()
				version := it.Suggestion.Version
				h.inbox.mu.Unlock()
				body := fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q`, version, recoveryFreshID)
				if !reoffer {
					body += `,"text":"Edited reply"`
				}
				body += "}"
				code, out := suggestionCall(t, h, it.ID, body)
				if len(c.calls()) != 1 || code != 502 || out["delivered"] != "unknown" || out["retryable"] != false {
					t.Fatalf("historical uncertain action was resent under a new identity: %d %v calls=%+v", code, out, c.calls())
				}
			})
		}
	}
}

func TestInboxUnknownProofSurvivesResolvedHistory(t *testing.T) {
	c := &recoveryReplier{err: rpc.ErrDropped}
	h, it := recoverySuggestion(t, false, c)
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`); code != 502 {
		t.Fatal(code)
	}
	c.answer(&rpc.Error{Code: assistantwire.CodeInboxSendFailed})
	for i := range chainMax + 1 {
		if _, err := suggestTool(t, h, fmt.Sprintf(`{"kind":"reply","text":"Different failed action %d"}`, i)); err != nil {
			t.Fatal(err)
		}
		h.inbox.mu.Lock()
		v := it.Suggestion.Version
		h.inbox.mu.Unlock()
		if code, _ := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm"}`, v)); code != 502 {
			t.Fatal(code)
		}
	}
	h.inbox.mu.Lock()
	proof := suggestionAttempt(it, 1)
	h.inbox.mu.Unlock()
	if proof == nil || proof.Status != suggestionUnknown {
		t.Error("resolved history evicted the sole unknown-operation proof")
	}
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Edited reply"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	version := it.Suggestion.Version
	h.inbox.mu.Unlock()
	before := len(c.calls())
	code, out := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q}`, version, recoveryFreshID))
	if len(c.calls()) != before || code != 502 || out["delivered"] != "unknown" {
		t.Fatalf("evicted proof permitted another send: %d %v calls %d -> %d", code, out, before, len(c.calls()))
	}
}

func TestInboxActiveHistoricalBindingSettlesOnlyItsAliases(t *testing.T) {
	for _, handed := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("handed=%t/failed=%t", handed, failed), func(t *testing.T) {
				c := &recoveryReplier{entered: make(chan struct{}), release: make(chan struct{})}
				if failed {
					c.err = &rpc.Error{Code: assistantwire.CodeInboxSendFailed}
				}
				t.Cleanup(c.proceed)
				h, it := recoverySuggestion(t, handed, c)
				finished := make(chan error, 1)
				go func() {
					text := "Edited reply"
					_, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, Text: &text, ClientID: recoveryFirstID}, true)
					finished <- err
				}()
				select {
				case <-c.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("first sender did not enter")
				}
				for _, text := range []string{"A different action", "Edited reply"} {
					var err error
					if handed {
						_, err = mainSuggest(h, "m1", fmt.Sprintf(`{"item":"lark:f1:100","kind":"reply","text":%q}`, text))
					} else {
						_, err = suggestTool(t, h, fmt.Sprintf(`{"kind":"reply","text":%q}`, text))
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				h.inbox.mu.Lock()
				version := it.Suggestion.Version
				h.inbox.mu.Unlock()
				alias := recoveryAttempt(t, h, it, version)
				if alias.Status != suggestionUnknown || alias.Retryable || alias.active || alias.ClientID != recoveryFirstID || alias.Proposal.Text != "Edited reply" {
					t.Fatalf("invalid projected binding: %+v", alias)
				}
				if code, out := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q}`, version, recoveryFreshID)); code != 502 || out["delivered"] != "unknown" || len(c.calls()) != 1 {
					t.Fatalf("active alias was resent: %d %v calls=%+v", code, out, c.calls())
				}
				c.proceed()
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Fatal("owned sender did not finish")
				}
				after := recoveryAttempt(t, h, it, version)
				want := suggestionSent
				if failed {
					want = suggestionFailed
				}
				h.inbox.mu.Lock()
				current := it.listed()
				h.inbox.mu.Unlock()
				if after.Status != want || after.Retryable != failed || after.ClientID != recoveryFirstID || len(c.calls()) != 1 {
					t.Fatalf("canonical outcome did not settle its alias: %+v", after)
				}
				if failed {
					if current.Suggestion == nil || current.Suggestion.Version != version || current.Suggestion.Text != "Edited reply" || current.DoneAt != nil {
						t.Fatalf("known-none lost its current retry: %+v", current)
					}
					c.answer(nil)
					if code, out := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q}`, version, recoveryFreshID)); code != 200 {
						t.Fatalf("settled alias cannot safely retry: %d %v", code, out)
					}
					calls := c.calls()
					if len(calls) != 2 || calls[1].ClientID != recoveryFirstID || calls[1].Text != "Edited reply" {
						t.Fatalf("alias retry lost its frozen operation: %+v", calls)
					}
				} else {
					if current.Suggestion != nil {
						t.Fatalf("sent alias left an already fulfilled card pending: %+v", current.Suggestion)
					}
					if handed {
						if current.DoneAt == nil || current.DoneBy != "replied" {
							t.Fatalf("handed alias did not use the existing replied policy: %+v", current)
						}
					} else if current.Now != NowWaiting || current.Follow == nil || current.Handling.State != HandlingWaiting || current.DoneAt != nil {
						t.Fatalf("processing alias did not wait for the other side: %+v", current)
					}
					if code, _ := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm"}`, version)); code != 409 || len(c.calls()) != 1 {
						t.Fatal("delivered alias was sent again")
					}
				}
			})
		}
	}
}

type recoveryReservationGateStore struct {
	*inboxMemStore
	once, releaseOnce sync.Once
	entered, release  chan struct{}
	fail              bool
}

func (s *recoveryReservationGateStore) PutInboxItem(ctx context.Context, id string, data []byte, updated time.Time, done *time.Time) error {
	first := false
	s.once.Do(func() {
		first = true
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	})
	if first && s.fail {
		return errors.New("controlled reservation failure")
	}
	return s.inboxMemStore.PutInboxItem(ctx, id, data, updated, done)
}

func TestInboxHistoricalAliasSettlesWhenReservationCannotSend(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			c := &recoveryReplier{}
			h, it := recoverySuggestion(t, false, c)
			st := &recoveryReservationGateStore{inboxMemStore: h.st, entered: make(chan struct{}), release: make(chan struct{}), fail: fail}
			release := func() { st.releaseOnce.Do(func() { close(st.release) }) }
			t.Cleanup(release)
			h.setStore(func() Store { return st })
			confirmed := make(chan int, 1)
			go func() {
				code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`)
				confirmed <- code
			}()
			select {
			case <-st.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("reservation did not enter the controlled store")
			}
			proposed := []chan error{make(chan error, 1), make(chan error, 1)}
			for i, text := range []string{"A different action", "Edited reply"} {
				go func() {
					_, err := suggestTool(t, h, fmt.Sprintf(`{"kind":"reply","text":%q}`, text))
					proposed[i] <- err
				}()
				waitFor(t, "proposal visible before its queued save", func() bool {
					h.inbox.mu.Lock()
					defer h.inbox.mu.Unlock()
					return it.Suggestion != nil && it.Suggestion.Text == text
				})
			}
			h.inbox.mu.Lock()
			version := it.Suggestion.Version
			h.inbox.mu.Unlock()
			if a := recoveryAttempt(t, h, it, version); a.Status != suggestionUnknown || a.Retryable || len(c.calls()) != 0 {
				t.Fatalf("not an uncertain unsent alias: %+v", a)
			}
			release()
			select {
			case code := <-confirmed:
				if code == 200 {
					t.Fatal("older version unexpectedly sent")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("confirmation did not end")
			}
			for _, result := range proposed {
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("proposal save did not finish")
				}
			}
			a := recoveryAttempt(t, h, it, version)
			if a.Status != suggestionFailed || !a.Retryable || a.ClientID != recoveryFirstID || len(c.calls()) != 0 {
				t.Fatalf("confirmed-none did not unlock its valid current alias: %+v calls=%+v", a, c.calls())
			}
			if code, out := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q}`, version, recoveryFreshID)); code != 200 || len(c.calls()) != 1 || c.calls()[0].ClientID != recoveryFirstID || c.calls()[0].Text != "Edited reply" {
				t.Fatalf("alias did not retry its frozen operation: %d %v calls=%+v", code, out, c.calls())
			}
		})
	}
}

func TestInboxHistoricalUnknownAllowsDifferentIntent(t *testing.T) {
	for _, differentOwner := range []bool{false, true} {
		t.Run(fmt.Sprint(differentOwner), func(t *testing.T) {
			c := &recoveryReplier{err: rpc.ErrDropped}
			h, it := recoverySuggestion(t, false, c)
			if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`); code != 502 {
				t.Fatal(code)
			}
			c.answer(nil)
			if differentOwner {
				setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
				handOff(t, h, it)
				if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"Edited reply"}`); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := suggestTool(t, h, `{"kind":"reply","text":"A different action"}`); err != nil {
					t.Fatal(err)
				}
			}
			h.inbox.mu.Lock()
			version := it.Suggestion.Version
			h.inbox.mu.Unlock()
			if code, out := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q}`, version, recoveryFreshID)); code != 200 || len(c.calls()) != 2 {
				t.Fatalf("unrelated intent was blocked: %d %v", code, out)
			}
		})
	}
	// The same text on a genuinely different incoming target is another explicit intent, not a retry of the old send.
	c := &recoveryReplier{err: rpc.ErrDropped}
	h, it := recoverySuggestion(t, false, c)
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`); code != 502 {
		t.Fatal(code)
	}
	h.inbox.mu.Lock()
	it.Last = "a-new-source-message"
	h.inbox.mu.Unlock()
	c.answer(nil)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Edited reply"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	version := it.Suggestion.Version
	h.inbox.mu.Unlock()
	if code, out := suggestionCall(t, h, it.ID, fmt.Sprintf(`{"version":%d,"action":"confirm","client_id":%q}`, version, recoveryFreshID)); code != 200 || len(c.calls()) != 2 {
		t.Fatalf("new source intent was blocked: %d %v", code, out)
	}
}

func TestInboxReplyCompletionDoesNotMoveReplacementOrFiling(t *testing.T) {
	for _, handed := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			for _, filed := range []bool{false, true} {
				t.Run(fmt.Sprintf("handed=%t/fail=%t/filed=%t", handed, failed, filed), func(t *testing.T) {
					c := &recoveryReplier{entered: make(chan struct{}), release: make(chan struct{})}
					if failed {
						c.err = &rpc.Error{Code: assistantwire.CodeInboxSendFailed}
					}
					t.Cleanup(c.proceed)
					h, it := recoverySuggestion(t, handed, c)
					done := make(chan error, 1)
					go func() {
						_, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFirstID}, true)
						done <- err
					}()
					select {
					case <-c.entered:
					case <-time.After(3 * time.Second):
						t.Fatal("sender did not start")
					}
					// Another caller cannot turn the same active version into another send with a fresh UUID.
					if _, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFreshID}, true); !replyInDoubt(err) {
						t.Fatalf("active duplicate: %v", err)
					}
					if filed {
						if _, err := h.setInboxDone(t.Context(), it.ID, true, false); err != nil {
							t.Fatal(err)
						}
					} else {
						var err error
						if handed {
							_, err = mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"New reply"}`)
						} else {
							_, err = suggestTool(t, h, `{"kind":"reply","text":"New reply"}`)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					h.inbox.mu.Lock()
					before := it.listed()
					beforeSentOf := it.Agent.SentOf
					before.Chain = append([]InboxChainEntry(nil), before.Chain...)
					h.inbox.mu.Unlock()
					c.proceed()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("send did not complete")
					}
					h.inbox.mu.Lock()
					after := it.listed()
					afterSentOf := it.Agent.SentOf
					h.inbox.mu.Unlock()
					if beforeSentOf != afterSentOf || !reflect.DeepEqual(before.Suggestion, after.Suggestion) || !reflect.DeepEqual(before.Handling, after.Handling) || !reflect.DeepEqual(before.Follow, after.Follow) || !reflect.DeepEqual(before.Chain, after.Chain) || before.DoneBy != after.DoneBy || !reflect.DeepEqual(before.DoneAt, after.DoneAt) {
						t.Fatalf("old completion changed newer work:\nbefore=%+v\nafter=%+v", before, after)
					}
					a := recoveryAttempt(t, h, it, 1)
					want := suggestionSent
					if failed {
						want = suggestionFailed
					}
					if a.Status != want || len(c.calls()) != 1 {
						t.Fatalf("old outcome=%+v calls=%+v", a, c.calls())
					}
				})
			}
		}
	}
}

func TestInboxReplyTargetIgnoresReadDecorationButNotAuthority(t *testing.T) {
	c := &recoveryReplier{err: &rpc.Error{Code: assistantwire.CodeInboxSendFailed}}
	h, _, it := handledHub(t)
	h.in.mu.Lock()
	h.in.conns = []connector.Connector{c}
	h.in.mu.Unlock()
	h.inbox.mu.Lock()
	it.Lark = nil
	it.Kind = KindDoc
	it.Doc = &InboxDoc{URL: "https://example.com/doc", NoticeID: "notice-1", Reply: &DocReply{Kind: "thread", Thread: "thread-1", Label: "Reply"}}
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Original reply"}`); err != nil {
		t.Fatal(err)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"`+recoveryFirstID+`"}`); code != 502 {
		t.Fatalf("failure=%d", code)
	}
	h.inbox.mu.Lock()
	now := time.Now()
	it.Doc.ReadAt = &now
	it.Doc.Error = "temporary read error"
	it.Doc.Reply.Label = "New display label"
	it.Doc.Rows = []DocRow{{Ref: "r1", Fields: []DocField{{Name: "Display", Value: "Updated"}}}}
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Original reply"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	version := it.Suggestion.Version
	h.inbox.mu.Unlock()
	if version != 1 {
		t.Fatal("read-only decoration replaced a pending reply")
	}
	c.answer(nil)
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"`+recoveryFreshID+`"}`); code != 200 {
		t.Fatalf("read-only refresh prevented safe retry: %d %v", code, out)
	}
}

func TestInboxReplyCoalesceKeepsMailAndConversationFences(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.Lark = nil
	it.Source = "mail"
	it.Account = "example"
	it.Last = "9"
	it.Mail = &InboxMail{AccountID: "example", Mailbox: "INBOX", UIDs: []uint32{9}, MessageID: "m9"}
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Same words"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	it.Last = "10"
	it.Mail.UIDs = []uint32{10}
	it.Mail.MessageID = "m10"
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"Same words"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	g := it.Suggestion
	h.inbox.mu.Unlock()
	if g.Version != 2 || g.MailTarget == nil || g.MailTarget.UID != 10 {
		t.Fatalf("mail target was coalesced by text: %+v", g)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 409 {
		t.Fatal("old mail target became actionable")
	}
	setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
	handOff(t, h, it)
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"Same words"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	g = it.Suggestion
	h.inbox.mu.Unlock()
	if g.Version != 3 || g.Conversation == nil || g.Conversation.SessionID != "m1" {
		t.Fatalf("conversation authority was coalesced by text: %+v", g)
	}
}

func TestInboxReplyOutcomeWriteFailureRetainsReservation(t *testing.T) {
	c := &recoveryReplier{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(c.proceed)
	h, it := recoverySuggestion(t, false, c)
	done := make(chan error, 1)
	go func() {
		_, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFirstID}, true)
		done <- err
	}()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("send did not start")
	}
	h.st.imu.Lock()
	h.st.down = errors.New("outcome save unavailable")
	h.st.imu.Unlock()
	c.proceed()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("send did not complete")
	}
	s := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	if err := s.inboxReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.inbox.mu.Lock()
	loaded := s.inbox.items[it.ID]
	a := suggestionAttempt(loaded, 1)
	public := loaded.listed()
	s.inbox.mu.Unlock()
	if a == nil || a.ClientID != recoveryFirstID || a.Status != suggestionUnknown || public.SuggestionAttempts[0].Status != suggestionUnknown {
		t.Fatalf("reservation was lost: %+v", a)
	}
	if _, err := s.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFreshID}, true); !replyInDoubt(err) || len(c.calls()) != 1 {
		t.Fatalf("write failure permitted a fresh-ID duplicate: %v %+v", err, c.calls())
	}
}

func TestInboxWrapUpReusesPendingRequestAndCurrentCard(t *testing.T) {
	h, _, it := handoffHub(t)
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, id := range []string{recoveryFirstID, recoveryFreshID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := handoffCall(t, h, it.ID, `{"text":"Wrap up","client_id":"`+id+`","reuse_pending":true}`)
			results <- code
		}()
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 {
			t.Fatalf("wrap up=%d", code)
		}
	}
	if len(sendOnces(h.f)) != 1 {
		t.Fatalf("two requests dispatched work twice: %+v", sendOnces(h.f))
	}
	h.inbox.mu.Lock()
	it.UpdatedAt = time.Now()
	it.Read = true
	it.Lark.ReadPosition = "read-decoration"
	h.inbox.mu.Unlock()
	if code, out := handoffCall(t, h, it.ID, `{"text":"Wrap up","client_id":"30000000-0000-4000-8000-000000000001","reuse_pending":true}`); code != 200 || out["reused"] != true || len(sendOnces(h.f)) != 1 {
		t.Fatalf("read decoration became new incoming identity: %d %v", code, out)
	}
	// Pending intent survives a manager reload before the new suggestion arrives.
	s := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	if err := s.inboxReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.handOff(t.Context(), it.ID, handoffReq{Text: "Wrap up", ClientID: handoffClient, ReusePending: true}); err != nil || len(sendOnces(h.f)) != 1 {
		t.Fatalf("pending reload=%v", err)
	}
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"Current reply"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := handoffCall(t, h, it.ID, `{"text":"Wrap up","client_id":"`+handoffClient+`","reuse_pending":true}`); code != 200 || out["reused"] != true || out["item"].(map[string]any)["suggestion"] == nil || len(sendOnces(h.f)) != 1 {
		t.Fatalf("existing card: %d %v", code, out)
	}
	// Different typed instructions remain a distinct explicit intent.
	if code, out := handoffCall(t, h, it.ID, `{"text":"New instructions","client_id":"20000000-0000-4000-8000-000000000001"}`); code != 200 || len(sendOnces(h.f)) != 2 {
		t.Fatalf("typed intent: %d %v", code, out)
	}
	// A later incoming source epoch is not swallowed by an earlier pending request.
	h.inbox.mu.Lock()
	it.Last = "new-message"
	h.inbox.mu.Unlock()
	if code, out := handoffCall(t, h, it.ID, `{"text":"Wrap up","client_id":"20000000-0000-4000-8000-000000000002","reuse_pending":true}`); code != 200 || len(sendOnces(h.f)) != 3 {
		t.Fatalf("new incoming: %d %v", code, out)
	}
	if code, out := handoffCall(t, h, it.ID, `{"text":"Wrap up","client_id":"20000000-0000-4000-8000-000000000003","reuse_pending":true}`); code != 200 || out["reused"] != true || len(sendOnces(h.f)) != 3 {
		t.Fatalf("retained older-source card lost pending intent: %d %v", code, out)
	}
}

func TestInboxWrapUpBindsIncomingBeforeTheSend(t *testing.T) {
	h, _, it := handoffHub(t)
	h.f.mu.Lock()
	h.f.onSendOnce = func(f *fakeHub, _ map[string]any) {
		if len(f.sentOnce) == 1 {
			h.inbox.mu.Lock()
			it.Last = "arrived-during-send"
			h.inbox.mu.Unlock()
		}
	}
	h.f.mu.Unlock()
	for _, id := range []string{recoveryFirstID, recoveryFreshID} {
		if code, out := handoffCall(t, h, it.ID, `{"text":"Wrap up","client_id":"`+id+`","reuse_pending":true}`); code != 200 {
			t.Fatalf("handoff: %d %v", code, out)
		}
	}
	if calls := sendOnces(h.f); len(calls) != 2 {
		t.Fatalf("the old send claimed a message that arrived during I/O: %+v", calls)
	}
}

func TestInboxReplyReservationMustBeSavedBeforeSending(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprint(absent), func(t *testing.T) {
			c := &recoveryReplier{}
			h, it := recoverySuggestion(t, false, c)
			if absent {
				h.setStore(func() Store { return nil })
			} else {
				h.st.imu.Lock()
				h.st.down = errors.New("store unavailable")
				h.st.imu.Unlock()
			}
			code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"`+recoveryFirstID+`"}`)
			if code != 503 || out["code"] != assistantwire.CodeInboxSendUnrecorded || len(c.calls()) != 0 {
				t.Fatalf("unrecorded send: %d %v calls=%+v", code, out, c.calls())
			}
		})
	}
}

type recoveryBeforeWriteStore struct {
	*inboxMemStore
	once   sync.Once
	before func()
}

func (s *recoveryBeforeWriteStore) PutInboxItem(ctx context.Context, id string, data []byte, updated time.Time, done *time.Time) error {
	s.once.Do(s.before)
	return s.inboxMemStore.PutInboxItem(ctx, id, data, updated, done)
}

func TestInboxReplyReservationRejectsReplacedItem(t *testing.T) {
	c := &recoveryReplier{}
	h, it := recoverySuggestion(t, false, c)
	st := &recoveryBeforeWriteStore{inboxMemStore: h.st, before: func() {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		raw, _ := json.Marshal(it)
		var replacement InboxItem
		_ = json.Unmarshal(raw, &replacement)
		replacement.Suggestion.Version = 2
		replacement.Suggestion.Text = "Replacement"
		replacement.SuggestSeq = 2
		h.inbox.items[it.ID] = &replacement
	}}
	h.setStore(func() Store { return st })
	code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","client_id":"`+recoveryFirstID+`"}`)
	if code != 409 || out["code"] != "suggestion_stale" || len(c.calls()) != 0 || out["item"].(map[string]any)["suggestion"].(map[string]any)["text"] != "Replacement" {
		t.Fatalf("replaced row was sent: %d %v calls=%+v", code, out, c.calls())
	}
}

func TestInboxReplySurvivesMetadataRollbackWhileSending(t *testing.T) {
	c := &recoveryReplier{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(c.proceed)
	h, it := recoverySuggestion(t, false, c)
	done := make(chan error, 1)
	go func() {
		_, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFirstID}, true)
		done <- err
	}()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not enter")
	}
	h.inbox.mu.Lock()
	a := suggestionAttempt(it, 1)
	w := beginWrite(it)
	it.Recap = "Metadata that cannot be saved"
	h.inbox.settle(w)
	h.inbox.mu.Unlock()
	h.st.imu.Lock()
	h.st.down = errors.New("metadata write unavailable")
	h.st.imu.Unlock()
	h.saveWrites(t.Context(), []*inboxWrite{w}, "synthetic metadata")
	h.inbox.mu.Lock()
	same := suggestionAttempt(it, 1) == a && a.active
	h.inbox.mu.Unlock()
	if !w.undone || !same {
		t.Fatal("unrelated rollback replaced the authoritative active attempt")
	}
	h.st.imu.Lock()
	h.st.down = nil
	h.st.imu.Unlock()
	c.proceed()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sender did not finish")
	}
	if a := recoveryAttempt(t, h, it, 1); a.Status != suggestionSent {
		t.Fatalf("outcome lost after metadata rollback: %+v", a)
	}
}

func replyRecoveryDatabase(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

func TestInboxReplyAttemptReconstruction(t *testing.T) {
	database := replyRecoveryDatabase(t)
	c := &recoveryReplier{err: &rpc.Error{Code: assistantwire.CodeInboxSendFailed}}
	h, it := recoverySuggestion(t, false, c)
	h.setStore(func() Store { return database })
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm","text":"Edited reply","client_id":"`+recoveryFirstID+`"}`); code != 502 {
		t.Fatalf("failure=%d", code)
	}
	reload := func() *Service {
		s := New(Options{Hub: h.f, Store: func() Store { return database }, Logf: testLogf(t)})
		if err := s.inboxReady(t.Context()); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := reload()
	s.inbox.mu.Lock()
	loaded := s.inbox.items[it.ID]
	a := suggestionAttempt(loaded, 1)
	s.inbox.mu.Unlock()
	if a == nil || a.Status != suggestionFailed || a.ClientID != recoveryFirstID || a.Text != "Edited reply" {
		t.Fatalf("stored failed attempt: %+v", a)
	}
	c.answer(nil)
	if _, err := s.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFreshID}, true); err != nil {
		t.Fatal(err)
	}
	if calls := c.calls(); len(calls) != 2 || calls[1].ClientID != recoveryFirstID || calls[1].Text != "Edited reply" {
		t.Fatalf("reloaded retry: %+v", calls)
	}
	// Finish the successful manager's actual note writer before its owned database is closed. Do not write a
	// synthetic older state through h while this fresh manager still owns the completed item.
	s.deliverNotes(t.Context(), loaded)
}

func TestInboxSendingReplyAttemptReconstruction(t *testing.T) {
	database := replyRecoveryDatabase(t)
	c := &recoveryReplier{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(c.proceed)
	h, it := recoverySuggestion(t, false, c)
	h.setStore(func() Store { return database })
	finished := make(chan error, 1)
	go func() {
		text := "Edited reply"
		_, err := h.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm,
			Text: &text, ClientID: recoveryFirstID}, true)
		finished <- err
	}()
	select {
	case <-c.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the persisted reservation never reached the controlled provider")
	}
	// The first sender cannot commit a successful outcome while reconstruction is checked. This reads the real
	// sending row written by the production path, rather than fabricating one from a stale manager snapshot.
	s := New(Options{Hub: h.f, Store: func() Store { return database }, Logf: testLogf(t)})
	if err := s.inboxReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.inbox.mu.Lock()
	loaded := s.inbox.items[it.ID]
	a := suggestionAttempt(loaded, 1)
	public := loaded.listed()
	s.inbox.mu.Unlock()
	if a == nil || a.Status != suggestionUnknown || a.Retryable || a.ClientID != recoveryFirstID || a.Text != "Edited reply" ||
		a.Proposal == nil || a.Proposal.Text != "Original reply" || public.SuggestionAttempts[0].Status != suggestionUnknown {
		t.Fatalf("reconstructed sending binding: %+v / %+v", a, public.SuggestionAttempts)
	}
	if _, err := s.actOnSuggestion(t.Context(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm, ClientID: recoveryFreshID}, true); !replyInDoubt(err) {
		t.Fatalf("reconstructed sending: %v", err)
	}
	if calls := c.calls(); len(calls) != 1 || calls[0].ClientID != recoveryFirstID || calls[0].Text != "Edited reply" {
		t.Fatalf("reconstruction sent another reply while the original was unresolved: %+v", calls)
	}
	select {
	case err := <-finished:
		t.Fatalf("the first sender ended before its provider was released: %v", err)
	default:
	}
	c.proceed()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the owned sender did not finish after release")
	}
	h.deliverNotes(t.Context(), it)
	if len(c.calls()) != 1 {
		t.Fatal("release/reconstruction duplicated the provider call")
	}
}
