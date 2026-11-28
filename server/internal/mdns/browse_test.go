package mdns

import (
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"
)

type browseFixture struct {
	typ, inst, host       name
	ptr, srv, txt, a4, ll record
}

func newFixture(t *testing.T) browseFixture {
	typ := mustType(t, "_persona-hub._tcp")
	inst := append(name{"Hub A"}, typ...)
	host := name{"persona-a", "local"}
	return browseFixture{
		typ:  typ,
		inst: inst,
		host: host,
		ptr:  record{name: typ, typ: typePTR, class: classIN, ttl: 120, target: inst},
		srv:  record{name: inst, typ: typeSRV, class: classIN | topBit, ttl: 120, port: 8443, target: host},
		txt:  record{name: inst, typ: typeTXT, class: classIN | topBit, ttl: 120, txt: []string{"hub_id=a"}},
		a4:   record{name: host, typ: typeA, class: classIN | topBit, ttl: 120, addr: netip.MustParseAddr("192.0.2.1")},
		ll:   record{name: host, typ: typeAAAA, class: classIN | topBit, ttl: 120, addr: netip.MustParseAddr("fe80::1")},
	}
}

func resp(rs ...record) *message {
	return &message{flags: flagResponse | flagAA, answers: rs}
}

func qtypes(qs []question) []uint16 {
	var out []uint16
	for _, q := range qs {
		out = append(out, q.typ)
	}
	return out
}

// Records arrive in pieces: a PTR alone, then SRV + TXT, then the addresses.
func TestBrowserAggregates(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	t0 := time.Unix(1_000_000, 0)

	found, follow := b.apply(resp(f.ptr), "eth0", t0)
	if len(found) != 0 {
		t.Fatalf("PTR alone reported %+v", found)
	}
	if !slices.Equal(qtypes(follow), []uint16{typeSRV, typeTXT}) || !follow[0].name.equal(f.inst) {
		t.Fatalf("follow-up after PTR = %+v", follow)
	}
	if _, follow = b.apply(resp(f.ptr), "eth0", t0.Add(time.Second)); len(follow) != 0 {
		t.Errorf("follow-up repeated within %v: %+v", followEvery, follow)
	}

	t1 := t0.Add(6 * time.Second)
	found, follow = b.apply(resp(f.srv, f.txt), "eth0", t1)
	if len(found) != 1 {
		t.Fatalf("SRV + TXT: found %+v", found)
	}
	e := found[0]
	if e.Instance != "Hub A" || e.Host != "persona-a.local." || e.Port != 8443 || e.Addrs != nil ||
		!maps.Equal(e.TXT, map[string]string{"hub_id": "a"}) || e.TTL != 120*time.Second || !e.Seen.Equal(t1) {
		t.Errorf("SRV + TXT: got %+v", e)
	}
	if !slices.Equal(qtypes(follow), []uint16{typeA, typeAAAA}) || !follow[0].name.equal(f.host) {
		t.Errorf("follow-up after SRV = %+v", follow)
	}

	found, _ = b.apply(resp(f.a4, f.ll), "eth0", t1.Add(time.Second))
	want := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("fe80::1%eth0")}
	if len(found) != 1 || !slices.Equal(found[0].Addrs, want) {
		t.Fatalf("addresses: got %+v, want %v", found, want)
	}

	// The same records again: nothing new to report.
	if found, follow = b.apply(resp(f.ptr, f.srv, f.txt, f.a4), "eth0", t1.Add(2*time.Second)); len(found)+len(follow) != 0 {
		t.Errorf("unchanged records reported %+v / asked %+v", found, follow)
	}

	// TXT changes: reported again.
	txt2 := f.txt
	txt2.txt = []string{"hub_id=a", "name=Lab"}
	found, _ = b.apply(resp(txt2), "eth0", t1.Add(3*time.Second))
	if len(found) != 1 || found[0].TXT["name"] != "Lab" || len(found[0].Addrs) != 2 {
		t.Errorf("TXT change: got %+v", found)
	}

	// An address goodbye removes just that address.
	gone := f.ll
	gone.ttl = 0
	found, _ = b.apply(resp(gone), "eth0", t1.Add(4*time.Second))
	if len(found) != 1 || !slices.Equal(found[0].Addrs, want[:1]) {
		t.Errorf("address goodbye: got %+v", found)
	}

	// The instance's goodbye.
	bye := f.ptr
	bye.ttl = 0
	found, _ = b.apply(resp(bye), "eth0", t1.Add(5*time.Second))
	if len(found) != 1 || found[0].TTL != 0 || found[0].Instance != "Hub A" {
		t.Fatalf("goodbye: got %+v", found)
	}
	if len(b.insts) != 0 {
		t.Errorf("goodbye left %d instances", len(b.insts))
	}
	if found, _ = b.apply(resp(bye), "eth0", t1.Add(6*time.Second)); len(found) != 0 {
		t.Errorf("second goodbye reported %+v", found)
	}
}

func TestBrowserGoodbyeBeforeReport(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	now := time.Unix(1_000_000, 0)
	b.apply(resp(f.ptr), "", now)
	bye := f.ptr
	bye.ttl = 0
	if found, _ := b.apply(resp(bye), "", now); len(found) != 0 {
		t.Errorf("never-reported instance said goodbye: %+v", found)
	}
}

func TestBrowserIgnoresSelf(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "HUB a")
	found, follow := b.apply(resp(f.ptr, f.srv, f.txt, f.a4), "", time.Now())
	if len(found)+len(follow)+len(b.insts) != 0 {
		t.Errorf("own records were used: %+v %+v", found, follow)
	}
}

func TestBrowserOtherTypeIgnored(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(mustType(t, "_other._tcp"), "")
	if found, follow := b.apply(resp(f.ptr, f.srv, f.txt, f.a4), "", time.Now()); len(found)+len(follow) != 0 {
		t.Errorf("other type: %+v %+v", found, follow)
	}
}

func TestBrowserExpiry(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	t0 := time.Unix(1_000_000, 0)
	short := f.a4
	short.ttl = 10
	found, _ := b.apply(resp(f.ptr, f.srv, f.txt, short, f.ll), "eth0", t0)
	if len(found) != 1 || len(found[0].Addrs) != 2 {
		t.Fatalf("got %+v", found)
	}
	if next := b.next(); !next.Equal(t0.Add(10 * time.Second)) {
		t.Errorf("next = %v, want +10s", next.Sub(t0))
	}
	if found = b.sweep(t0.Add(9 * time.Second)); len(found) != 0 {
		t.Errorf("early sweep reported %+v", found)
	}
	found = b.sweep(t0.Add(10 * time.Second))
	if len(found) != 1 || len(found[0].Addrs) != 1 || found[0].Addrs[0] != netip.MustParseAddr("fe80::1%eth0") {
		t.Errorf("address expiry: got %+v", found)
	}
	found = b.sweep(t0.Add(120 * time.Second))
	if len(found) != 1 || found[0].TTL != 0 {
		t.Errorf("instance expiry: got %+v", found)
	}
	if len(b.insts) != 0 || !b.next().IsZero() {
		t.Errorf("expired instance still cached")
	}
}

func TestBrowserHostMove(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	now := time.Unix(1_000_000, 0)
	b.apply(resp(f.ptr, f.srv, f.a4), "", now)
	moved := f.srv
	moved.target = name{"persona-b", "local"}
	found, _ := b.apply(resp(moved), "", now.Add(time.Second))
	if len(found) != 1 || found[0].Host != "persona-b.local." || len(found[0].Addrs) != 0 {
		t.Errorf("host move: got %+v", found)
	}
}

func TestBrowserLimits(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	var rs []record
	for i := range maxInstances + 50 {
		r := f.ptr
		r.target = append(name{"hub-" + strconv.Itoa(i)}, f.typ...)
		rs = append(rs, r)
	}
	b.apply(resp(rs...), "", time.Now())
	if len(b.insts) != maxInstances {
		t.Errorf("cached %d instances, want %d", len(b.insts), maxInstances)
	}

	b = newBrowser(f.typ, "")
	rs = []record{f.ptr, f.srv}
	for i := range maxHostAddrs + 10 {
		a := f.a4
		a.addr = netip.AddrFrom4([4]byte{192, 0, 2, byte(i + 1)})
		rs = append(rs, a)
	}
	found, _ := b.apply(resp(rs...), "", time.Now())
	if len(found) != 1 || len(found[0].Addrs) != maxHostAddrs {
		t.Errorf("addresses kept: %d", len(found[0].Addrs))
	}
}

func TestBrowserClampsTTL(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	long := f.ptr
	long.ttl = 0xffffffff
	srv := f.srv
	srv.ttl = 0xffffffff
	found, _ := b.apply(resp(long, srv), "", time.Now())
	if len(found) != 1 || found[0].TTL != maxTTL {
		t.Errorf("got %+v", found)
	}
}

// A TXT-only response for an unseen instance must keep a non-zero temporary expiry so the
// same loop turn's sweep does not immediately delete the cached TXT and break follow-up throttling.
// Once SRV arrives with a shorter authoritative TTL, it replaces the temporary TXT lifetime.
func TestBrowserTXTOnlySurvivesSweepAndDefersLifetimeToSRV(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	t0 := time.Unix(1_000_000, 0)

	txt := f.txt
	txt.ttl = 4500
	found, follow := b.apply(resp(txt), "eth0", t0)
	if len(found) != 0 {
		t.Fatalf("TXT alone reported %+v", found)
	}
	if !slices.Equal(qtypes(follow), []uint16{typeSRV, typeTXT}) || !follow[0].name.equal(f.inst) {
		t.Fatalf("follow-up after TXT = %+v", follow)
	}

	// runner.loop calls sweep(now) right after handle(p, now) on the same tick.
	if gone := b.sweep(t0); len(gone) != 0 {
		t.Fatalf("same-turn sweep reported %+v", gone)
	}
	if len(b.insts) != 1 {
		t.Fatalf("same-turn sweep dropped TXT-only instance; len(insts) = %d", len(b.insts))
	}
	if next := b.next(); !next.Equal(t0.Add(4500 * time.Second)) {
		t.Fatalf("next after TXT = +%v, want +4500s", next.Sub(t0))
	}

	// A repeated TXT within followEvery must remain throttled.
	if _, follow = b.apply(resp(txt), "eth0", t0.Add(time.Second)); len(follow) != 0 {
		t.Fatalf("follow-up repeated within %v after TXT: %+v", followEvery, follow)
	}

	// SRV arrives 2s later with a 120s TTL: TXT is preserved and SRV sets the authoritative expiry.
	t1 := t0.Add(2 * time.Second)
	found, _ = b.apply(resp(f.srv), "eth0", t1)
	if len(found) != 1 {
		t.Fatalf("SRV after TXT: found %+v, want 1", found)
	}
	if !maps.Equal(found[0].TXT, map[string]string{"hub_id": "a"}) || found[0].TTL != 120*time.Second {
		t.Fatalf("SRV after TXT: got %+v", found[0])
	}
	if next := b.next(); !next.Equal(t1.Add(120 * time.Second)) {
		t.Fatalf("next after SRV = +%v from t1, want +120s (not 4500s TXT TTL)", next.Sub(t1))
	}

	// A subsequent long-TTL TXT on an already-live instance must not extend its authoritative expiry.
	txtUpdate := f.txt
	txtUpdate.ttl = 4500
	txtUpdate.txt = []string{"hub_id=a", "v=2"}
	found, _ = b.apply(resp(txtUpdate), "eth0", t1.Add(time.Second))
	if len(found) != 1 || found[0].TXT["v"] != "2" {
		t.Fatalf("live TXT update: got %+v", found)
	}
	if next := b.next(); !next.Equal(t1.Add(120 * time.Second)) {
		t.Fatalf("long-TTL TXT extended live expiry to +%v from t1, want +120s", next.Sub(t1))
	}

	// Instance expires on SRV's TTL, not TXT's 4500s TTL.
	gone := b.sweep(t1.Add(120 * time.Second))
	if len(gone) != 1 || gone[0].TTL != 0 {
		t.Fatalf("expiry after SRV TTL: got %+v", gone)
	}
	if len(b.insts) != 0 || !b.next().IsZero() {
		t.Fatalf("instance remained cached after SRV expiry")
	}

	// TXT(4500s) -> PTR(120s) also hands liveness to PTR while preserving the cached TXT until SRV arrives.
	t2 := t0.Add(200 * time.Second)
	b.apply(resp(txt), "eth0", t2)
	t3 := t2.Add(2 * time.Second)
	b.apply(resp(f.ptr), "eth0", t3)
	if next := b.next(); !next.Equal(t3.Add(120 * time.Second)) {
		t.Fatalf("next after TXT->PTR = +%v from t3, want +120s", next.Sub(t3))
	}
	found, _ = b.apply(resp(f.srv), "eth0", t3.Add(time.Second))
	if len(found) != 1 || !maps.Equal(found[0].TXT, map[string]string{"hub_id": "a"}) {
		t.Fatalf("SRV after TXT->PTR lost TXT: %+v", found)
	}
}

func TestBrowserTXTOnlyExpiresQuietly(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	t0 := time.Unix(1_000_000, 0)

	txt := f.txt
	txt.ttl = 30
	b.apply(resp(txt), "", t0)
	if gone := b.sweep(t0.Add(29 * time.Second)); len(gone) != 0 || len(b.insts) != 1 {
		t.Fatalf("before TXT expiry: gone=%+v len=%d", gone, len(b.insts))
	}
	if gone := b.sweep(t0.Add(30 * time.Second)); len(gone) != 0 || len(b.insts) != 0 || !b.next().IsZero() {
		t.Fatalf("at TXT expiry: gone=%+v len=%d next=%v", gone, len(b.insts), b.next())
	}
}

func TestBrowserTXTGoodbyeHandling(t *testing.T) {
	f := newFixture(t)
	b := newBrowser(f.typ, "")
	t0 := time.Unix(1_000_000, 0)

	txtBye := f.txt
	txtBye.ttl = 0

	// Unknown instance TXT goodbye must neither cache a stub nor emit follow-up queries.
	found, follow := b.apply(resp(txtBye), "", t0)
	if len(found) != 0 || len(follow) != 0 || len(b.insts) != 0 {
		t.Fatalf("unknown TXT goodbye: found=%+v follow=%+v len=%d", found, follow, len(b.insts))
	}

	// Pending TXT-only instance receiving TXT goodbye is dropped cleanly without follow-ups.
	b.apply(resp(f.txt), "", t0)
	found, follow = b.apply(resp(txtBye), "", t0.Add(time.Second))
	if len(found) != 0 || len(follow) != 0 || len(b.insts) != 0 {
		t.Fatalf("pending TXT goodbye: found=%+v follow=%+v len=%d", found, follow, len(b.insts))
	}

	// Live instance (with PTR+SRV) receiving TXT goodbye clears TXT but stays alive.
	t1 := t0.Add(10 * time.Second)
	found, _ = b.apply(resp(f.ptr, f.srv, f.txt), "", t1)
	if len(found) != 1 || len(found[0].TXT) != 1 {
		t.Fatalf("live seed: %+v", found)
	}
	found, _ = b.apply(resp(txtBye), "", t1.Add(time.Second))
	if len(found) != 1 || len(found[0].TXT) != 0 || found[0].TTL == 0 {
		t.Fatalf("live TXT goodbye: got %+v", found)
	}
	if len(b.insts) != 1 {
		t.Fatalf("live instance dropped on TXT goodbye")
	}
}
