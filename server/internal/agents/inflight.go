package agents

import (
	"context"
	"sync"
)

// listFlight is one ask of a source for its session list that callers missing the cache at the same time share.
// The Agents page asks for its sections, groups and counts at once when a change drops the cache, and every open
// app does too: each would otherwise make its own call to the source (a plugin's agentsrc.sessions round trip).
type listFlight struct {
	done    chan struct{}
	list    []Session
	err     error
	waiters int // callers who joined it rather than asking; guarded by listFlights.mu
	// askerGone is whether the asker's own context had ended (cancelled or past its deadline) when the ask failed:
	// that failure is the asker's, not the source's, so it is not handed to those waiting.
	askerGone bool
}

// listFlights are the asks under way, keyed by source and cache generation: an ask that began before an invalidate
// or forget is not shared with a caller who came after it (cache.go's gen).
type listFlights struct {
	mu sync.Mutex
	m  map[listFlightKey]*listFlight
}

type listFlightKey struct {
	source string
	gen    int
}

// join is the ask of source at gen under way, and whether the caller has to make it (and then land it).
func (fs *listFlights) join(source string, gen int) (*listFlight, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	k := listFlightKey{source, gen}
	if f := fs.m[k]; f != nil {
		f.waiters++
		return f, false
	}
	if fs.m == nil {
		fs.m = map[listFlightKey]*listFlight{}
	}
	f := &listFlight{done: make(chan struct{})}
	fs.m[k] = f
	return f, true
}

func (fs *listFlights) land(source string, gen int, f *listFlight) {
	fs.mu.Lock()
	delete(fs.m, listFlightKey{source, gen})
	fs.mu.Unlock()
	close(f.done)
}

// listShared is src's session list from the cache, or from one ask shared with whoever else misses it now. The ask
// runs under the context of the caller who made it; when that caller went away (cancelled, or its deadline passed),
// those waiting ask again themselves rather than answer with someone else's cancellation or timeout. A failure of the
// source itself, its own timeout included, is shared: asking again would only fail again.
func (s *Service) listShared(ctx context.Context, src Source) ([]Session, error) {
	name := src.Name()
	for {
		if list, ok := s.cache.getSessions(name); ok {
			return list, nil
		}
		gen := s.cache.generation(name)
		f, lead := s.flights.join(name, gen)
		if lead {
			f.list, f.err = s.listNow(ctx, src, gen)
			f.askerGone = f.err != nil && ctx.Err() != nil
			s.flights.land(name, gen, f)
			return f.list, f.err
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if f.askerGone && ctx.Err() == nil {
			continue // the one asking went away, not the source
		}
		return f.list, f.err
	}
}
