package mdns

import (
	"maps"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const (
	maxInstances = 256            // instances cached per browser; more are ignored
	maxHostAddrs = 32             // addresses kept per instance
	maxTTL       = 24 * time.Hour // longer TTLs are cut, so a bogus record cannot pin an instance forever
	followEvery  = 5 * time.Second
)

// instRecords are the records one message carries for one instance of the browsed type.
type instRecords struct {
	label         string // the instance label as seen
	ptr, srv, txt *record
}

// group sorts the records of m (answers and additionals) by instance of typ, in first-seen order, and returns the
// address records separately.
func group(m *message, typ name) (insts []*instRecords, addrs []record) {
	byKey := map[string]*instRecords{}
	get := func(label string) *instRecords {
		k := asciiLower(label)
		g := byKey[k]
		if g == nil {
			g = &instRecords{label: label}
			byKey[k] = g
			insts = append(insts, g)
		}
		return g
	}
	for _, sec := range [][]record{m.answers, m.additionals} {
		for i := range sec {
			r := &sec[i]
			switch r.typ {
			case typePTR:
				if r.name.equal(typ) && len(r.target) == len(typ)+1 && r.target[1:].equal(typ) {
					get(r.target[0]).ptr = r
				}
			case typeSRV, typeTXT:
				if len(r.name) != len(typ)+1 || !r.name[1:].equal(typ) {
					continue
				}
				g := get(r.name[0])
				if r.typ == typeSRV {
					g.srv = r
				} else {
					g.txt = r
				}
			case typeA, typeAAAA:
				addrs = append(addrs, *r)
			}
		}
	}
	return insts, addrs
}

// gone reports whether the records say the instance left: its PTR or SRV came with TTL 0.
func (g *instRecords) gone() bool {
	return (g.ptr != nil && g.ptr.ttl == 0) || (g.srv != nil && g.srv.ttl == 0)
}

func clampTTL(ttl uint32) time.Duration {
	d := time.Duration(ttl) * time.Second
	return min(d, maxTTL)
}

// instance is what the browser knows about one instance, gathered over several messages.
type instance struct {
	label       string
	ttl         time.Duration
	expire      time.Time // the instance is dropped (and reported gone) after this
	hasLiveness bool      // true once PTR or SRV has set the authoritative instance lifetime
	host        name      // nil until an SRV arrives
	port        uint16
	txt         map[string]string
	addrs       map[netip.Addr]time.Time // address -> expiry
	seen        time.Time
	asked       time.Time // last follow-up query for missing records
	reported    *Entry    // what Found last got; nil if never reported
}

func (in *instance) setLiveness(exp time.Time) {
	if !in.hasLiveness {
		in.expire = exp
		in.hasLiveness = true
	} else {
		in.expire = later(in.expire, exp)
	}
}

func (in *instance) entry() Entry {
	e := Entry{
		Instance: in.label,
		Port:     int(in.port),
		TXT:      maps.Clone(in.txt),
		TTL:      in.ttl,
		Seen:     in.seen,
	}
	if in.host != nil {
		e.Host = in.host.String()
	}
	for a := range in.addrs {
		e.Addrs = append(e.Addrs, a)
	}
	slices.SortFunc(e.Addrs, netip.Addr.Compare)
	return e
}

func (in *instance) goodbye(now time.Time) Entry {
	e := in.entry()
	e.TTL = 0
	e.Seen = now
	return e
}

// sameContent compares what a caller cares about: not TTL or Seen.
func sameContent(a, b Entry) bool {
	return a.Instance == b.Instance && a.Host == b.Host && a.Port == b.Port &&
		slices.Equal(a.Addrs, b.Addrs) && maps.Equal(a.TXT, b.TXT)
}

// browser caches the instances of one service type across messages and decides when to report them.
// It is not safe for concurrent use; Run drives it from one goroutine.
type browser struct {
	typ   name
	self  string // our own instance label, lowercased; its records are ignored
	insts map[string]*instance
}

func newBrowser(typ name, self string) *browser {
	return &browser{typ: typ, self: asciiLower(self), insts: map[string]*instance{}}
}

// apply takes in one response. zone is the name of the interface it came in on (for IPv6 link-local addresses).
// It returns the entries to report (new, changed or gone) and the questions to ask for records still missing.
func (b *browser) apply(m *message, zone string, now time.Time) (found []Entry, follow []question) {
	groups, addrs := group(m, b.typ)
	touched := map[*instance]bool{}
	for _, g := range groups {
		key := asciiLower(g.label)
		if b.self != "" && key == b.self {
			continue
		}
		in := b.insts[key]
		if g.gone() {
			if in != nil {
				if in.reported != nil {
					found = append(found, in.goodbye(now))
				}
				delete(b.insts, key)
			}
			continue
		}
		if g.ptr == nil && g.srv == nil && g.txt != nil && g.txt.ttl == 0 && (in == nil || !in.hasLiveness) {
			if in != nil {
				delete(b.insts, key)
			}
			continue
		}
		if in == nil {
			if len(b.insts) >= maxInstances {
				continue
			}
			in = &instance{addrs: map[netip.Addr]time.Time{}}
			b.insts[key] = in
		}
		in.label = g.label
		in.seen = now
		if g.ptr != nil {
			in.ttl = clampTTL(g.ptr.ttl)
			in.setLiveness(now.Add(in.ttl))
		}
		if g.srv != nil {
			if in.host != nil && !in.host.equal(g.srv.target) {
				clear(in.addrs) // moved to another host: its old addresses no longer apply
			}
			in.host = g.srv.target
			in.port = g.srv.port
			ttl := clampTTL(g.srv.ttl)
			if g.ptr == nil {
				in.ttl = ttl
			}
			in.setLiveness(now.Add(ttl))
		}
		if g.txt != nil {
			if g.txt.ttl == 0 {
				in.txt = nil
			} else {
				in.txt = decodeTXT(g.txt.txt)
				if !in.hasLiveness {
					in.expire = later(in.expire, now.Add(clampTTL(g.txt.ttl)))
				}
			}
		}
		touched[in] = true
	}
	for _, a := range addrs {
		addr := a.addr
		if addr.Is6() && addr.IsLinkLocalUnicast() && zone != "" {
			addr = addr.WithZone(zone)
		}
		for _, in := range b.insts {
			if in.host == nil || !in.host.equal(a.name) {
				continue
			}
			if a.ttl == 0 {
				delete(in.addrs, addr)
			} else if _, ok := in.addrs[addr]; ok || len(in.addrs) < maxHostAddrs {
				in.addrs[addr] = now.Add(clampTTL(a.ttl))
			}
			touched[in] = true
		}
	}
	for in := range touched {
		if in.host == nil || len(in.addrs) == 0 {
			if now.Sub(in.asked) >= followEvery {
				in.asked = now
				if in.host == nil {
					n := append(name{in.label}, b.typ...)
					follow = append(follow, question{name: n, typ: typeSRV, class: classIN}, question{name: n, typ: typeTXT, class: classIN})
				} else {
					follow = append(follow, question{name: in.host, typ: typeA, class: classIN}, question{name: in.host, typ: typeAAAA, class: classIN})
				}
			}
		}
		if in.host == nil {
			continue // not reportable until the SRV is known
		}
		if e := in.entry(); in.reported == nil || !sameContent(*in.reported, e) {
			in.reported = &e
			found = append(found, e)
		}
	}
	sortEntries(found)
	return found, follow
}

// sweep drops expired instances (reporting them gone) and expired addresses (reporting the change).
func (b *browser) sweep(now time.Time) (found []Entry) {
	for key, in := range b.insts {
		if !now.Before(in.expire) {
			if in.reported != nil {
				found = append(found, in.goodbye(now))
			}
			delete(b.insts, key)
			continue
		}
		changed := false
		for a, exp := range in.addrs {
			if !now.Before(exp) {
				delete(in.addrs, a)
				changed = true
			}
		}
		if changed && in.reported != nil {
			if e := in.entry(); !sameContent(*in.reported, e) {
				in.reported = &e
				found = append(found, e)
			}
		}
	}
	sortEntries(found)
	return found
}

// next is when sweep has something to do; zero if nothing is cached.
func (b *browser) next() time.Time {
	var t time.Time
	for _, in := range b.insts {
		t = earlier(t, in.expire)
		for _, exp := range in.addrs {
			t = earlier(t, exp)
		}
	}
	return t
}

func later(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// earlier returns the earlier of a and b, treating zero as "none".
func earlier(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

func sortEntries(es []Entry) {
	slices.SortFunc(es, func(a, b Entry) int { return strings.Compare(a.Instance, b.Instance) })
}
