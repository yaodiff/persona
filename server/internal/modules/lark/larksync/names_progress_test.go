package larksync

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// namesReq asks for the names of senders of one group, none of them visible to contact/v3.
func namesReq(f *fakeOpen, chatID string, senders ...string) larksyncwire.NameResolveRequest {
	req := larksyncwire.NameResolveRequest{ChatSenders: map[string][]string{chatID: senders}}
	for _, id := range senders {
		req.OpenIDs = append(req.OpenIDs, larksyncwire.OpenID(id))
		f.handle("/open-apis/contact/v3/users/"+id, func(w http.ResponseWriter, _ *http.Request) {
			writeCode(w, http.StatusBadRequest, 41001, "external user")
		})
	}
	return req
}

// The production timeout, reproduced: the limiter paces the IM family at 5 requests a second, so a round can read
// only about five member pages a second of its budget. A sender on page 280 of a large group is out of reach of one
// round. Before rounds, the resolver returned context.DeadlineExceeded (a "timeout" the binding's health showed as
// failing) and the next run started again at page 0, so it never got there. Now the round ends partial without an
// error, saves where it stopped, and the next round resumes there: every page is read once, contact/v3 is asked once.
func TestResolveNamesResumesALargeGroupAcrossRounds(t *testing.T) {
	clk := &fakeClock{t: testNow}
	f := newFakeOpen(t, clk)
	s := newTestSyncer(t, clk)
	s.lim.rates[famIM] = familyRates[famIM] // the real pace: the budget, not the page count, is what runs out
	b := binding(f, "primary~a1", larksyncwire.TierFull)
	b.Budget = 30 * time.Second
	pagedMembers(t, f, "oc_big", 300, map[string][2]int{"ou_far": {280, 3}})
	req := namesReq(f, "oc_big", "ou_far")

	first, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
	if err != nil {
		t.Fatalf("round 1: err = %v, want none (a round that runs out of time is partial, not failed)", err)
	}
	if !first.Partial || first.UserNames["ou_far"] != "" || first.Progress == "" {
		t.Fatalf("round 1: partial = %v, names = %v, progress = %q", first.Partial, first.UserNames, first.Progress)
	}
	pages1 := f.count("/open-apis/im/v1/chats/oc_big/members")
	if pages1 < 100 || pages1 > 200 {
		t.Fatalf("round 1 read %d pages, want about 5 a second of a 30 s budget", pages1)
	}

	rounds := 1
	res := first
	for res.Partial {
		if rounds++; rounds > 4 {
			t.Fatal("the scan never finished")
		}
		req.Progress = res.Progress
		res, err = s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
		if err != nil {
			t.Fatalf("round %d: %v", rounds, err)
		}
	}
	if res.UserNames["ou_far"] != "Named ou_far" {
		t.Fatalf("names = %v", res.UserNames)
	}
	if n := f.count("/open-apis/im/v1/chats/oc_big/members"); n != 281 {
		t.Errorf("member pages read over %d rounds = %d, want 281 (pages 0-280, none twice)", rounds, n)
	}
	if n := f.count("/open-apis/contact/v3/users/ou_far"); n != 1 {
		t.Errorf("contact/v3 asked %d times, want once (its miss is remembered)", n)
	}
	if book := nameBookNames(res.NameBook); book["ou_far"] != "Named ou_far" {
		t.Errorf("name book = %v, want the learned sender", book)
	}
}

// A sender the stored scan was never checked for restarts it from the first page; a finished scan that found
// nothing is not read again for the senders it was checked for.
func TestResolveNamesRestartsAScanForANewSender(t *testing.T) {
	clk := &fakeClock{t: testNow}
	f := newFakeOpen(t, clk)
	s := newTestSyncer(t, clk)
	b := binding(f, "primary~a1", larksyncwire.TierFull)
	pagedMembers(t, f, "oc_grp", 5, map[string][2]int{"ou_new": {1, 0}})
	req := namesReq(f, "oc_grp", "ou_gone")

	first, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
	if err != nil || first.Partial || first.UserNames["ou_gone"] != "" {
		t.Fatalf("round 1: %v %v %v", err, first.Partial, first.UserNames)
	}
	if n := f.count("/open-apis/im/v1/chats/oc_grp/members"); n != 5 {
		t.Fatalf("round 1 read %d pages, want the whole list (5)", n)
	}

	req.Progress = first.Progress
	again, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
	if err != nil || again.Partial {
		t.Fatalf("round 2: %v %v", err, again.Partial)
	}
	if n := f.count("/open-apis/im/v1/chats/oc_grp/members"); n != 5 {
		t.Fatalf("round 2 read the finished list again: %d pages", n)
	}
	if n := f.count("/open-apis/contact/v3/users/ou_gone"); n != 1 {
		t.Errorf("contact/v3 asked %d times for a remembered miss", n)
	}

	req = namesReq(f, "oc_grp", "ou_gone", "ou_new")
	req.Progress = again.Progress
	third, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
	if err != nil || third.UserNames["ou_new"] != "Named ou_new" {
		t.Fatalf("round 3: %v %v", err, third.UserNames)
	}
	if n := f.count("/open-apis/im/v1/chats/oc_grp/members"); n != 10 {
		t.Fatalf("round 3 pages = %d in all, want 10 (the scan restarted at page 0 and read the list once more)", n)
	}

	// A week later every remembered miss is tried again.
	clk.advance(namesRetryAfter + time.Hour)
	req = namesReq(f, "oc_grp", "ou_gone")
	req.Progress = third.Progress
	if _, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req); err != nil {
		t.Fatal(err)
	}
	if n := f.count("/open-apis/contact/v3/users/ou_gone"); n != 2 {
		t.Errorf("contact/v3 asked %d times after a week, want 2", n)
	}
}

// A page that keeps failing for a passing reason stalls the list (not done): the error comes back for the manager
// to note, and the saved progress resumes at the same page next round.
func TestResolveNamesStalledPageResumesThere(t *testing.T) {
	clk := &fakeClock{t: testNow}
	f := newFakeOpen(t, clk)
	s := newTestSyncer(t, clk)
	b := binding(f, "primary~a1", larksyncwire.TierFull)
	failing := true
	pagedMembers(t, f, "oc_grp", 3, map[string][2]int{"ou_x": {2, 0}})
	members := f.handlers["/open-apis/im/v1/chats/oc_grp/members"]
	f.handle("/open-apis/im/v1/chats/oc_grp/members", func(w http.ResponseWriter, r *http.Request) {
		if failing && r.URL.Query().Get("page_token") == "p1" {
			writeCode(w, http.StatusBadGateway, 0, "bad gateway")
			return
		}
		members(w, r)
	})
	req := namesReq(f, "oc_grp", "ou_x")
	first, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
	if err == nil || first.Progress == "" {
		t.Fatalf("round 1: err = %v, progress = %q; want the passing failure and the position kept", err, first.Progress)
	}
	failing = false
	req.Progress = first.Progress
	second, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
	if err != nil || second.UserNames["ou_x"] != "Named ou_x" {
		t.Fatalf("round 2: %v %v", err, second.UserNames)
	}
	// Round 1: page 0, then page 1 three times (the first try and two retries); round 2: pages 1 and 2.
	if n := f.count("/open-apis/im/v1/chats/oc_grp/members"); n != 6 {
		t.Errorf("member requests = %d, want 6 (page 0 not read again)", n)
	}
}

// The shape that timed out in production, reproduced with the real pace: two large groups (400 member pages each)
// whose unnamed senders left the group (no member list names them) and contact/v3 cannot see. Before rounds, every
// run spent its 2 minutes re-reading the first group and part of the second and failed with context.DeadlineExceeded,
// forever. Now each round is partial at worst, the lists are each read once in all, and the backfill completes.
func TestResolveNamesCompletesTwoLargeGroupsOfLeftSenders(t *testing.T) {
	clk := &fakeClock{t: testNow}
	f := newFakeOpen(t, clk)
	s := newTestSyncer(t, clk)
	s.lim.rates[famIM] = familyRates[famIM]
	b := binding(f, "primary~a1", larksyncwire.TierFull)
	b.Budget = 2 * time.Minute
	req := larksyncwire.NameResolveRequest{ChatSenders: map[string][]string{}}
	for _, chat := range []string{"oc_a", "oc_b"} {
		pagedMembers(t, f, chat, 400, nil)
		var senders []string
		for i := range 40 {
			senders = append(senders, fmt.Sprintf("ou_%s_left%02d", chat, i))
		}
		r := namesReq(f, chat, senders...)
		req.OpenIDs = append(req.OpenIDs, r.OpenIDs...)
		req.ChatSenders[chat] = senders
	}
	rounds := 0
	for {
		rounds++
		res, err := s.ResolveNamesWithFallback(t.Context(), b, staticToken("tok"), req)
		if err != nil {
			t.Fatalf("round %d: %v", rounds, err)
		}
		req.Progress = res.Progress
		if !res.Partial {
			break
		}
		if rounds > 5 {
			t.Fatal("the backfill never completed")
		}
	}
	if rounds < 2 {
		t.Fatalf("finished in %d round; the case needs more than one 2-minute budget", rounds)
	}
	for _, chat := range []string{"oc_a", "oc_b"} {
		if n := f.count("/open-apis/im/v1/chats/" + chat + "/members"); n != 400 {
			t.Errorf("%s member pages = %d, want 400 (each read once over %d rounds)", chat, n, rounds)
		}
	}
}
