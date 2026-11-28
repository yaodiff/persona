package extmgr

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

func (h *Manager) pageRemoveLocked(s *pageSession) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.uiPages.sessions, s.id)
}
func (h *Manager) pageDispose(s *pageSession) {
	s.disposeOnce.Do(func() {
		if s.release != nil {
			defer s.release()
		}
		ps := &h.uiPages
		ps.mu.Lock()
		mountCancel := s.mountCancel
		mountDone := s.mountDone
		ps.mu.Unlock()
		// Cancel an awaiting mount before waiting for its RPC dispatch to finish;
		// its guest factory can remain unresolved until dispose reaches the bridge.
		if mountCancel != nil {
			mountCancel()
		}
		if mountDone != nil {
			<-mountDone
		}
		ps.mu.Lock()
		run := s.run
		ps.mu.Unlock()
		if run == 0 || s.process.Generation() != run {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.process.Call(ctx, "ui.page.dispose", map[string]any{"session": s.id}, nil)
	})
}
func (h *Manager) pageDropExtension(ext string) {
	ps := &h.uiPages
	ps.mu.Lock()
	var gone []*pageSession
	for _, s := range ps.sessions {
		if ext == "" || s.extension == ext {
			h.pageRemoveLocked(s)
			gone = append(gone, s)
		}
	}
	ps.mu.Unlock()
	h.pageDisposeAll(gone)
}
func pageBatch(sessions []*pageSession, before, work func(*pageSession)) {
	jobs := make(chan *pageSession)
	var workers sync.WaitGroup
	for i := 0; i < min(8, len(sessions)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for s := range jobs {
				work(s)
			}
		}()
	}
	for _, s := range sessions {
		if before != nil {
			before(s)
		}
		jobs <- s
	}
	close(jobs)
	workers.Wait()
}

type pagePauseJob struct {
	s    *pageSession
	done chan struct{}
}

func (h *Manager) pageDisposeAll(gone []*pageSession) {
	pageBatch(gone, func(s *pageSession) {
		h.uiPages.mu.Lock()
		who := s.who
		h.uiPages.mu.Unlock()
		h.pageEmit(who, streamFrame("closed", "", s.id))
	}, h.pageDispose)
}

func (h *Manager) pageDisconnect(who extwire.UIPrincipal) {
	ps := &h.uiPages
	ps.mu.Lock()
	var paused []pagePauseJob
	for _, s := range ps.sessions {
		if samePageOwner(s.who, who) && s.who.Connection == who.Connection {
			s.who.Connection = ""
			s.visibility++
			s.recovering = false
			s.suspended = true
			s.expires = time.Now().Add(5 * time.Minute)
			if s.pauseDone == nil {
				done := make(chan struct{})
				s.pauseDone = done
				paused = append(paused, pagePauseJob{s: s, done: done})
			}
		}
	}
	ps.mu.Unlock()
	h.pagePauseAll(paused)
}

func (h *Manager) pagePause(s *pageSession, done chan struct{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.process.Call(ctx, "ui.page.suspend", map[string]any{"session": s.id}, nil)
	ps := &h.uiPages
	ps.mu.Lock()
	if done != nil && s.pauseDone == done {
		s.pauseDone = nil
	}
	ps.mu.Unlock()
	if done != nil {
		close(done)
	}
	return err
}
func (h *Manager) pagePauseAll(paused []pagePauseJob) {
	if len(paused) == 0 {
		return
	}
	jobs := make(chan pagePauseJob)
	var workers sync.WaitGroup
	for i := 0; i < min(8, len(paused)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				_ = h.pagePause(job.s, job.done)
			}
		}()
	}
	for _, job := range paused {
		jobs <- job
	}
	close(jobs)
	workers.Wait()
}
func (h *Manager) pageSweep(now time.Time) {
	ps := &h.uiPages
	ps.mu.Lock()
	var gone []*pageSession
	for _, s := range ps.sessions {
		if (!s.expires.IsZero() && !now.Before(s.expires)) || s.run != 0 && s.process.Generation() != s.run {
			h.pageRemoveLocked(s)
			gone = append(gone, s)
		}
	}
	ps.mu.Unlock()
	h.pageDisposeAll(gone)
}
func (h *Manager) pageBusReset() {
	ps := &h.uiPages
	ps.mu.Lock()
	var paused []pagePauseJob
	for _, s := range ps.sessions {
		if !s.legacy && !s.suspended {
			s.who.Connection = ""
			s.visibility++
			s.recovering = false
			s.suspended = true
			s.expires = time.Now().Add(5 * time.Minute)
			if s.pauseDone == nil {
				done := make(chan struct{})
				s.pauseDone = done
				paused = append(paused, pagePauseJob{s: s, done: done})
			}
		}
	}
	ps.mu.Unlock()
	if len(paused) > 0 {
		go h.pagePauseAll(paused)
	}
}

func (h *Manager) pageLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.pageDropExtension("")
			return
		case now := <-ticker.C:
			h.pageSweep(now)
		}
	}
}
