package mail

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// conn is a cached, logged-in IMAP client.
type conn struct {
	c        *imapclient.Client
	lastUsed time.Time
}

// connProbe is when getConn checks a cached connection before handing it out: one unused for longer than after
// must answer a NOOP within wait, or it is dropped and dialed again. A NAT or firewall that silently forgot an idle
// flow would otherwise leave the next command hanging for the whole networkTimeout.
type connProbe struct {
	after, wait time.Duration
}

var defaultConnProbe = connProbe{after: time.Minute, wait: 5 * time.Second}

// probeLimits is s.probe with the defaults filled in (a zero Service has none).
func (s *Service) probeLimits() connProbe {
	p := s.probe
	if p.after <= 0 {
		p.after = defaultConnProbe.after
	}
	if p.wait <= 0 {
		p.wait = defaultConnProbe.wait
	}
	return p
}

// alive reports whether c answers a NOOP within wait. A connection that doesn't is closed, which also ends the NOOP.
func alive(ctx context.Context, c *imapclient.Client, wait time.Duration) bool {
	done := make(chan error, 1)
	go func() { done <- c.Noop().Wait() }()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case err := <-done:
		return err == nil
	case <-t.C:
	case <-ctx.Done():
	}
	c.Close()
	return false
}

// getConn returns a logged-in IMAP client for the account, dialing if needed. The caller holds the account's lock
// (withConnAt), so nothing else uses the connection while it is probed.
func (s *Service) getConn(ctx context.Context, a *MailAccount) (*imapclient.Client, error) {
	s.mu.Lock()
	cached := s.conns[a.ID]
	// A connection that ended while it sat in the cache (the server hung up, or a reply after the last command could
	// not be read) needs no NOOP, and no try of the operation is spent on it.
	gone := cached != nil && ended(cached.c)
	if cached != nil && !gone {
		if idle := s.now().Sub(cached.lastUsed); idle <= idleTimeout {
			cached.lastUsed = s.now()
			s.mu.Unlock()
			p := s.probeLimits()
			if idle <= p.after || alive(ctx, cached.c, p.wait) {
				return cached.c, nil
			}
			logf("mail: %s: the connection idle for %v did not answer a NOOP; reconnecting", a.ID, idle.Round(time.Second))
			if err := ctx.Err(); err != nil {
				s.dropConn(a.ID)
				return nil, err
			}
			s.mu.Lock()
		}
	}
	stale := s.conns[a.ID]
	delete(s.conns, a.ID)
	s.mu.Unlock()
	switch {
	case stale != nil && gone && stale == cached:
		logf("mail: %s: the cached connection had ended (%s); reconnecting", a.ID, whyEnded(stale.c))
	case stale != nil:
		stale.c.Close()
	}

	type result struct {
		c   *imapclient.Client
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := s.dialIMAP(context.Background(), a)
		ch <- result{c, err}
	}()
	var c *imapclient.Client
	select {
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		c = r.c
	}

	s.mu.Lock()
	// A concurrent caller may have installed a connection meanwhile; prefer it
	// and drop ours so we never leak.
	if existing, ok := s.conns[a.ID]; ok {
		s.mu.Unlock()
		c.Close()
		return existing.c, nil
	}
	s.conns[a.ID] = &conn{c: c, lastUsed: s.now()}
	s.mu.Unlock()
	return c, nil
}

// dropConn removes the cached connection for an account, closing it.
func (s *Service) dropConn(id string) {
	s.mu.Lock()
	c := s.conns[id]
	delete(s.conns, id)
	s.mu.Unlock()
	if c != nil {
		c.c.Close()
	}
}

// connTries bounds the logins one operation may use: the first try and one reconnect.
const connTries = 2

// withConn runs fn on the account's cached connection; op names the operation in the log. A connection that turns
// out to be gone is replaced and fn runs once more (withConnAt).
func (s *Service) withConn(ctx context.Context, a *MailAccount, op string, fn func(*imapclient.Client) error) error {
	return s.withConnAt(ctx, a, op, func(c *imapclient.Client, _ time.Time) error { return fn(c) })
}

// withConnAt is withConn handing fn the operation's deadline: networkTimeout after the account was free (connecting
// included), or the context's if that is sooner, and the same one when it retries. Work done in steps (a page of a
// slow mailbox) stops in time with it.
//
// A try that could not connect (a network error, not the server's answer) or whose connection ended under fn (the
// server hung up, a read or write failed, a reply could not be parsed) is tried once more on a new login, inside the
// same deadline; fn starts over then, or carries on from what it kept of the first try. Every failed try is logged
// with the account, op, the step and the raw error. When the last try could not reach the server either, the caller
// gets an UnreachableError, which says so in words; the raw error stays in the log and in its chain.
func (s *Service) withConnAt(ctx context.Context, a *MailAccount, op string, fn func(c *imapclient.Client, deadline time.Time) error) error {
	return s.withConnAtRetry(ctx, a, op, fn, nil)
}

// withConnAtRetry lets a write prevent the operation from being replayed once its first command may have reached
// the server. A broken connection is still discarded even when the operation cannot be retried.
func (s *Service) withConnAtRetry(ctx context.Context, a *MailAccount, op string, fn func(c *imapclient.Client, deadline time.Time) error, canRetry func() bool) error {
	// IMAP is stateful (SELECT), so a cached connection can only serve one
	// command sequence at a time. The wait for the turn is the caller's ctx's:
	// a caller that gave up leaves the queue (#2344).
	mu := s.accountLock(a.ID)
	if err := mu.acquire(ctx); err != nil {
		return err
	}
	defer mu.release()

	// The operation's own time starts once the account is free, on purpose: a
	// slow mailbox ahead in the queue must not eat it.
	deadline := time.Now().Add(networkTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	run := func(c *imapclient.Client) error {
		done := make(chan error, 1)
		go func() { done <- fn(c, deadline) }()
		select {
		case err := <-done:
			return err
		case <-time.After(time.Until(deadline)):
			s.dropConn(a.ID)
			return ErrTimedOut
		case <-ctx.Done():
			s.dropConn(a.ID)
			return ctx.Err()
		}
	}

	for try := 1; ; try++ {
		c, err := s.getConn(ctx, a)
		changed := s.noteLogin(a.ID, err)
		// lost: the server could not be reached, or the connection ended under fn; retryable: a new login might do
		// better (as before, any connect failure that is not the server's answer).
		step, why := "connect", ""
		var lost, retryable bool
		if err == nil {
			err = run(c)
			if err == nil {
				return nil
			}
			step = ""
			// A try past its time or given up on closed the connection itself: that is no news about the server.
			if !errors.Is(err, ErrTimedOut) && ctx.Err() == nil && connEnded(c, err) {
				why = s.discard(a.ID, c)
				lost, retryable = true, true
			}
		} else {
			retryable = isConnError(err)
			lost = retryable && networkError(err)
		}
		retry := retryable && try < connTries && ctx.Err() == nil && time.Now().Before(deadline) && (canRetry == nil || canRetry())
		// A refused login is logged when it starts or its answer changes, not again on every round.
		if !errors.Is(err, context.Canceled) && (step != "connect" || changed || !loginRefused(err)) {
			logTry(a.ID, op, step, try, retry, err, why)
		}
		switch {
		case retry:
			continue
		case lost:
			return &UnreachableError{Account: a.ID, err: err}
		}
		return err
	}
}

// logTry writes one line for a failed try: the account, the operation, the step that failed (the stepError in err,
// or connecting), what happens next, the raw error, and why the connection had ended when it had.
func logTry(account, op, step string, try int, retry bool, err error, why string) {
	cause := err
	var se *stepError
	if step == "" && errors.As(err, &se) {
		step, cause = se.step, se.err
	}
	at := ""
	if step != "" {
		at = " at " + step
	}
	next := "giving up"
	if retry {
		next = "reconnecting"
	}
	if why != "" {
		why = "; the connection had ended (" + why + ")"
	}
	logf("mail: %s: %s failed%s (try %d of %d, %s): %v%s", account, op, at, try, connTries, next, cause, why)
}

// stepError is an operation's step that failed (select INBOX, search, fetch…), so the log can say which. Its text is
// the step and the error, as fmt.Errorf("%s: %w") gives.
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

// failedAt is err, which happened at step.
func failedAt(step string, err error) error { return &stepError{step: step, err: err} }

// ended reports whether c's connection is over: go-imap's reader stopped (the server hung up, a read or write
// failed, a reply could not be parsed) and moved it to the logout state before failing the commands still waiting,
// so a command that failed on it sees this already.
func ended(c *imapclient.Client) bool { return c.State() == imap.ConnStateLogout }

// connEnded reports whether err, from a try on c, means c can't be used again.
func connEnded(c *imapclient.Client, err error) bool {
	return ended(c) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// whyEnded closes c and says why its connection had ended: go-imap's reader's error, or that there was none (the
// server closed the connection, or a write failed and closed it).
func whyEnded(c *imapclient.Client) string {
	if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return "read: " + err.Error()
	}
	return "no read error: the server closed it, or a write failed"
}

// discard takes c out of the cache (when it is still the account's) and closes it, saying why it had ended.
func (s *Service) discard(id string, c *imapclient.Client) string {
	s.mu.Lock()
	if cached := s.conns[id]; cached != nil && cached.c == c {
		delete(s.conns, id)
	}
	s.mu.Unlock()
	return whyEnded(c)
}

// networkError reports whether err is the network failing (no answer, refused, reset, cut off), as opposed to the
// server's answer, a proxy's, TLS or a sign-in token.
func networkError(err error) bool {
	var (
		ne net.Error
		pe *proxyError
	)
	if errors.As(err, &pe) {
		return false
	}
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}

// isConnError reports whether err means the connection is unusable, so a
// reconnect is worth trying.
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	var ie *imap.Error
	if errors.As(err, &ie) {
		// A tagged NO/BAD is a protocol answer, not a broken connection.
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}
