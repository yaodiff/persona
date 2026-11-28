package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// A write runs once by its request id: asked again (at once or later), the program answers what the first run did.
func TestWritesRunOnce(t *testing.T) {
	var w writes
	var runs atomic.Int32
	run := func() (CallResult, error) {
		n := runs.Add(1)
		if n > 1 {
			return CallResult{}, errors.New("ran twice")
		}
		return CallResult{Text: "sent"}, nil
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := w.do("r1", run); err != nil || res.Text != "sent" {
				t.Errorf("%+v %v", res, err)
			}
		}()
	}
	wg.Wait()
	if res, err := w.do("r1", run); err != nil || res.Text != "sent" || runs.Load() != 1 {
		t.Fatalf("again later: %+v %v, %d run(s)", res, err, runs.Load())
	}
	if _, err := w.do("r2", run); err == nil {
		t.Fatal("another id did not run")
	}
	for i := range doneMost + 1 {
		_, _ = w.do(string(rune('a'+i%26))+string(rune(i)), func() (CallResult, error) { return CallResult{}, nil })
	}
	if len(w.runs) > doneMost || len(w.order) > doneMost {
		t.Fatalf("remembers %d writes", len(w.runs))
	}
}

// The same request id again answers what the first run did, the message ref it said included.
func TestWritesReplayTheMessageRef(t *testing.T) {
	var w writes
	runs := 0
	run := func() (CallResult, error) {
		runs++
		return CallResult{Text: "sent", MessageRef: "msg-7"}, nil
	}
	for range 2 {
		if res, err := w.do("r1", run); err != nil || res.Text != "sent" || res.MessageRef != "msg-7" {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if runs != 1 {
		t.Fatalf("%d runs", runs)
	}
}

// A write still running keeps its request id however many other writes finish meanwhile: the same id again waits
// for it and answers what it did, instead of running the action a second time.
func TestWritesKeepRunningWrite(t *testing.T) {
	var w writes
	started, release := make(chan struct{}), make(chan struct{})
	first := make(chan CallResult, 1)
	go func() {
		res, _ := w.do("r1", func() (CallResult, error) {
			close(started)
			<-release
			return CallResult{Text: "sent"}, nil
		})
		first <- res
	}()
	<-started
	for i := range doneMost {
		_, _ = w.do(fmt.Sprintf("other-%d", i), func() (CallResult, error) { return CallResult{}, nil })
	}
	var again atomic.Int32
	second := make(chan CallResult, 1)
	go func() {
		res, _ := w.do("r1", func() (CallResult, error) {
			again.Add(1)
			return CallResult{Text: "sent again"}, nil
		})
		second <- res
	}()
	close(release)
	if res1, res2 := <-first, <-second; res1.Text != "sent" || res2.Text != "sent" {
		t.Fatalf("first %+v, second %+v", res1, res2)
	}
	if n := again.Load(); n != 0 {
		t.Fatalf("the same request id ran %d more time(s)", n)
	}
	if len(w.runs) > doneMost || len(w.order) > doneMost {
		t.Fatalf("remembers %d writes", len(w.runs))
	}
}

// The hub sends a ref again when it timed out waiting: while the first send is still running, the second waits for
// it and answers what it did, instead of sending the message again.
func TestSentRefsWaitForRunningSend(t *testing.T) {
	var r sentRefs
	var sends atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	send := func() (channel.SendResult, error) {
		if sends.Add(1) == 1 {
			close(started)
			<-release
		}
		return channel.SendResult{MessageID: "m1"}, nil
	}
	results := make(chan channel.SendResult, 2)
	errs := make(chan error, 2)
	go func() { res, err := r.do("ref1", send); results <- res; errs <- err }()
	<-started
	go func() { res, err := r.do("ref1", send); results <- res; errs <- err }()
	// Give the retry time to reach the running send: it must wait for it, not send again or answer early.
	time.Sleep(50 * time.Millisecond)
	if n := sends.Load(); n != 1 {
		t.Fatalf("the retry sent again while the first send ran: %d send(s)", n)
	}
	select {
	case res := <-results:
		t.Fatalf("answered %+v before the first send finished", res)
	default:
	}
	close(release)
	for range 2 {
		if res, err := <-results, <-errs; err != nil || res.MessageID != "m1" {
			t.Fatalf("%+v %v", res, err)
		}
	}
	if n := sends.Load(); n != 1 {
		t.Fatalf("sent %d times", n)
	}
	if res, err := r.do("ref1", send); err != nil || res.MessageID != "m1" || sends.Load() != 1 {
		t.Fatalf("again later: %+v %v, %d send(s)", res, err, sends.Load())
	}
}

// A failed send is forgotten: the same ref may be sent again, and once it went it is not sent a third time.
func TestSentRefsRetryAfterFailure(t *testing.T) {
	var r sentRefs
	var sends atomic.Int32
	send := func() (channel.SendResult, error) {
		if sends.Add(1) == 1 {
			return channel.SendResult{}, errors.New("upstream timed out")
		}
		return channel.SendResult{MessageID: "m2"}, nil
	}
	if _, err := r.do("ref2", send); err == nil {
		t.Fatal("the first send did not fail")
	}
	if res, err := r.do("ref2", send); err != nil || res.MessageID != "m2" {
		t.Fatalf("retry: %+v %v", res, err)
	}
	if res, err := r.do("ref2", send); err != nil || res.MessageID != "m2" || sends.Load() != 2 {
		t.Fatalf("again later: %+v %v, %d send(s)", res, err, sends.Load())
	}
	for i := range doneMost + 1 {
		_, _ = r.do(string(rune('a'+i%26))+string(rune(i)), func() (channel.SendResult, error) { return channel.SendResult{}, nil })
	}
	if len(r.sent) > doneMost || len(r.order) > doneMost {
		t.Fatalf("remembers %d sends", len(r.sent))
	}
}

type serveApprovalConn struct {
	gotProto    string
	gotApproval connector.AutoConfirmApproval
	grant       *connector.AutoConfirmGrant
}

func (*serveApprovalConn) ID() string   { return "example" }
func (*serveApprovalConn) Name() string { return "Example" }
func (*serveApprovalConn) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapActions}}
}
func (*serveApprovalConn) Actions() []proto.Tool { return []proto.Tool{{Name: "example_do"}} }
func (*serveApprovalConn) Describe(context.Context, string, json.RawMessage) (connector.Action, string, error) {
	return connector.Action{Name: "example_do", Visible: "example"}, "summary", nil
}
func (c *serveApprovalConn) DescribeWithApproval(_ context.Context, name string, _ json.RawMessage, protocol string) (connector.Action, string, *connector.AutoConfirmGrant, error) {
	c.gotProto = protocol
	return connector.Action{Name: name, Visible: "example"}, "summary", c.grant, nil
}
func (*serveApprovalConn) Do(context.Context, string, json.RawMessage) (string, error) {
	return "manual", nil
}
func (c *serveApprovalConn) DoWithApproval(_ context.Context, _ string, _ json.RawMessage, approval connector.AutoConfirmApproval) (connector.DoResult, error) {
	c.gotApproval = approval
	return connector.DoResult{Text: "auto", MessageRef: "m1"}, nil
}

func TestHandleGuardedAutoConfirmWire(t *testing.T) {
	exp := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
	c := &serveApprovalConn{grant: &connector.AutoConfirmGrant{Token: "tok_1", ExpiresAt: exp}}
	s := &server{c: c}

	// Describe without approval_protocol omits auto_confirm even if connector returns one.
	out, err := s.handle(context.Background(), InPlugin(MDescribe), json.RawMessage(`{"contribution":"example","name":"example_do","args":{"a":1}}`))
	if err != nil {
		t.Fatalf("unnegotiated describe: %v", err)
	}
	if dr := out.(DescribeResult); dr.AutoConfirm != nil || c.gotProto != "" {
		t.Fatalf("unnegotiated describe leaked auto_confirm: %+v proto=%q", dr, c.gotProto)
	}

	// Describe with approval_protocol forwards protocol and returns auto_confirm.
	out, err = s.handle(context.Background(), InPlugin(MDescribe), json.RawMessage(`{"contribution":"example","name":"example_do","args":{"a":1},"approval_protocol":"guarded-auto-confirm-v1"}`))
	if err != nil {
		t.Fatalf("negotiated describe: %v", err)
	}
	if dr := out.(DescribeResult); dr.AutoConfirm == nil || dr.AutoConfirm.Token != "tok_1" || !dr.AutoConfirm.ExpiresAt.Equal(exp) || c.gotProto != connector.ApprovalProtocolGuardedAutoConfirmV1 {
		t.Fatalf("negotiated describe: %+v proto=%q", dr, c.gotProto)
	}

	// Do rejects unpaired approval_mode / approval_token and unsupported approval_mode.
	for _, raw := range []string{
		`{"contribution":"example","name":"example_do","request_id":"r1","approval_mode":"auto_confirm"}`,
		`{"contribution":"example","name":"example_do","request_id":"r2","approval_token":"tok_1"}`,
		`{"contribution":"example","name":"example_do","request_id":"r3","approval_mode":"other","approval_token":"tok_1"}`,
	} {
		if _, err := s.handle(context.Background(), InPlugin(MDo), json.RawMessage(raw)); err == nil {
			t.Errorf("expected error for invalid actions.do params: %s", raw)
		}
	}

	// Do with valid paired approval_mode + approval_token routes to DoWithApproval.
	out, err = s.handle(context.Background(), InPlugin(MDo), json.RawMessage(`{"contribution":"example","name":"example_do","request_id":"r4","approval_mode":"auto_confirm","approval_token":"tok_1"}`))
	if err != nil {
		t.Fatalf("approved do: %v", err)
	}
	if cr := out.(CallResult); cr.Text != "auto" || cr.MessageRef != "m1" || c.gotApproval.Mode != connector.ApprovalModeAutoConfirm || c.gotApproval.Token != "tok_1" {
		t.Fatalf("approved do: %+v approval=%+v", cr, c.gotApproval)
	}
}
