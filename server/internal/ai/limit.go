package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Upstream limits (docs/assistant/model-config.md "Upstream rate limits"). A relay answering "the account's quota is used up" (HTTP 429
// account_quota_exhausted) or "the account is busy" (HTTP 503 account_concurrency_full) used to be tried again item
// after item: 429 requests and as many log lines in 15 minutes. Now such an answer holds the connection (address +
// key, whichever use calls it): requests are put off without being sent until the pause is over, then one goes
// first to find out. Callers take a PausedError as "not now", leave the work queued and log nothing; the gate logs
// one line when it holds and one when it lets go.

const (
	ReasonQuota = "quota" // the account's quota is used up
	ReasonBusy  = "busy"  // the account is at its concurrency limit, or the upstream is overloaded
)

var (
	quotaPause = 30 * time.Minute // without a Retry-After
	busyFirst  = 5 * time.Second  // the first busy pause; each busy answer in a row doubles it
	busyMost   = 5 * time.Minute
	afterMost  = 24 * time.Hour // a Retry-After longer than this is taken as this
	growAfter  = 10             // successes under a cap before it is raised by one
	capLift    = 8              // a cap raised past this is lifted
	now        = time.Now
	logf       = log.Printf
)

// SetBusyPauseForTest makes a busy answer's first pause d instead of five seconds until t ends, for another package's
// test of what follows a pause.
func SetBusyPauseForTest(t interface{ Cleanup(func()) }, d time.Duration) {
	old := busyFirst
	busyFirst = d
	t.Cleanup(func() { busyFirst = old })
}

// backgroundSlotWait is how long a background request (Background) waits for a slot under the cap before it is put off.
const backgroundSlotWait = 30 * time.Second

// slotWaitKey carries how long a request may wait for a slot under the cap (Background); none: until its deadline.
type slotWaitKey struct{}

// Background marks ctx's requests as background work (the knowledge base's loop): queued under a connection's cap,
// such a request waits at most 30 seconds for a slot and is then put off (PausedError, nothing sent), so it gives its
// turn back instead of spending its deadline (minutes) in the queue. Other requests on the same connection (a reply
// someone waits for, triage, translation) wait for a slot until their own deadline, as before.
func Background(ctx context.Context) context.Context { return withSlotWait(ctx, backgroundSlotWait) }

func withSlotWait(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, slotWaitKey{}, d)
}

// Pause is a connection held back: nothing is sent to it until Until (then one request goes first).
type Pause struct {
	Reason string    `json:"reason"` // quota | busy
	Until  time.Time `json:"until"`
}

// Text is the pause the way the app shows it, e.g. "quota used up, retry in 23 minutes".
func (p Pause) Text() string {
	what := "并发已满"
	if p.Reason == ReasonQuota {
		what = "额度用完"
	}
	d := p.Until.Sub(now())
	switch {
	case d <= 0:
		return what + "，稍后重试"
	case d < time.Minute:
		return fmt.Sprintf("%s，%d 秒后重试", what, int((d+time.Second-1)/time.Second))
	default:
		return fmt.Sprintf("%s，%d 分钟后重试", what, int((d+time.Minute-1)/time.Minute))
	}
}

// PausedError: the request was not sent because its connection is held (or it is the answer that held it). The
// work is put off, not failed: keep it queued and do not log it.
type PausedError struct{ Pause }

func (e *PausedError) Error() string { return e.Text() }

// IsPaused tells a put-off request from a failed one.
func IsPaused(err error) bool {
	var p *PausedError
	return errors.As(err, &p)
}

// Held is the pause on cfg's connection or CLI launcher gate, nil when requests go out.
func Held(cfg Config) *Pause {
	if cfg.RunLauncher != "" {
		return held(runKey(cfg))
	}
	if !cfg.Configured() {
		return nil
	}
	key, err := New(cfg).key()
	if err != nil {
		return nil
	}
	return held(gateID{httpKey: gateKeyFor(cfg.API, cfg.BaseURL, key)})
}

func held(key gateID) *Pause {
	v, ok := gates.Load(key)
	if !ok {
		return nil
	}
	g := v.(*gate)
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held {
		return nil
	}
	p := g.pause
	return &p
}

// gate is shared by clients using the same HTTP credentials and address, or the same CLI tuple.
type gate struct {
	host string // for the log

	mu          sync.Mutex
	held        bool      // from the answer that paused it to the next success
	pause       Pause     // while held
	since       time.Time // when it was held
	putOff      int       // requests not sent while held
	probing     bool      // the pause is over and one request is finding out, until the upstream answers it
	strikes     int       // busy answers in a row
	runTimedOut bool      // the previous eligible CLI run timed out
	cap         int       // requests in flight at most, 0 for no cap
	inflight    int
	out         []time.Time   // when each request in flight went out, the longest out first, for the log (noTurn)
	fine        int           // successes since the cap last changed
	wake        chan struct{} // closed on every change, for requests waiting under the cap

	// The requests put off for want of a turn since the last log line about them (noTurn): how many, since when,
	// and when that line was written.
	noTurns      int
	noTurnSince  time.Time
	noTurnLogged time.Time
}

// turn is a request the gate let out: probe says it is the one finding out whether a pause is over, at when it went.
type turn struct {
	probe bool
	at    time.Time
}

// Only HTTP gates set httpKey. CLI gates use separate fields so delimiters in names are unambiguous.
type gateID struct{ httpKey, machine, launcher, model string }

func runKey(cfg Config) gateID {
	return gateID{machine: cfg.ViaMachine, launcher: cfg.RunLauncher, model: cfg.Model}
}

var gates sync.Map // gateID → *gate

func gateForRun(cfg Config) *gate {
	return sharedGate(runKey(cfg), cfg.RunLauncher)
}

func normalizeGateBase(api, baseURL string) string {
	switch api {
	case "chat":
		baseURL = trimSingleEndpointPath(baseURL, "/chat/completions")
	case "responses":
		baseURL = trimVersionedEndpointPath(baseURL, "/responses")
	default:
		baseURL = trimVersionedEndpointPath(baseURL, "/messages")
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func gateKeyFor(api, baseURL, key string) string {
	sum := sha256.Sum256([]byte(key))
	return normalizeGateBase(api, baseURL) + "#" + hex.EncodeToString(sum[:8])
}

func gateKey(baseURL, key string) string {
	return gateKeyFor("", baseURL, key)
}

func gateForAPI(api, baseURL, key string) *gate {
	return sharedGate(gateID{httpKey: gateKeyFor(api, baseURL, key)}, baseURL)
}

func sharedGate(k gateID, host string) *gate {
	if g, ok := gates.Load(k); ok {
		return g.(*gate)
	}
	if k.httpKey != "" {
		if u, err := url.Parse(host); err == nil && u.Host != "" {
			host = u.Host
		}
	} else {
		host = runGateHost(k.machine, k.launcher, k.model)
	}
	g, _ := gates.LoadOrStore(k, &gate{host: host, wake: make(chan struct{})})
	return g.(*gate)
}

func runGateHost(machine, launcher, model string) string {
	host := launcher
	if machine != "" {
		host = machine + "/" + host
	}
	if model != "" {
		host += ":" + model
	}
	return host
}

func gateFor(baseURL, key string) *gate {
	return gateForAPI("", baseURL, key)
}

// admit lets a request go out, waits its turn, or puts it off while the connection is held. Its turn: a slot under
// the cap, and, once a pause is over, the answer to the one request finding out whether it is (probe: this request
// is that one). The others used to be put off at once while it was out, so a round of Inbox triage that came right
// after a pause sorted one item and reported every other as held, with nothing in flight, until a later round (#6698).
func (g *gate) admit(ctx context.Context) (t turn, err error) {
	var giveUp <-chan time.Time // a background request's, set on its first wait (nil: none)
	waited := false
	for {
		g.mu.Lock()
		if g.held && now().Before(g.pause.Until) {
			g.putOff++
			p := g.pause
			g.mu.Unlock()
			return turn{}, &PausedError{p}
		}
		if g.held && g.probing || g.cap > 0 && g.inflight >= g.cap {
			wake := g.wake
			g.mu.Unlock()
			if !waited {
				waited = true
				if d, ok := ctx.Value(slotWaitKey{}).(time.Duration); ok {
					t := time.NewTimer(d)
					defer t.Stop()
					giveUp = t.C
				}
			}
			select {
			case <-ctx.Done():
				// Out of time waiting for its turn: nothing was sent, so it is put off like a paused one.
				return turn{}, g.notSent()
			case <-giveUp:
				// A background request waited its while: put off the same way, so the caller's loop moves on and asks
				// again later.
				return turn{}, g.notSent()
			case <-wake:
			}
			continue
		}
		t = turn{probe: g.held, at: now()}
		g.probing = g.probing || t.probe
		g.inflight++
		g.out = append(g.out, t.at)
		g.mu.Unlock()
		return t, nil
	}
}

// notSent is the error of a request that gave up waiting for its turn: the pause while the connection is held (it
// counts as put off), else busy as of now (no slot under the cap).
func (g *gate) notSent() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.noTurn()
	if g.held {
		g.putOff++
		return &PausedError{g.pause}
	}
	return &PausedError{Pause{Reason: ReasonBusy, Until: now()}}
}

// noTurnEvery is the least time between two log lines about requests put off for want of a turn (noTurn).
const noTurnEvery = 10 * time.Minute

// noTurn counts a request put off for want of its turn and says so in the log, at most once every noTurnEvery: how
// many were, how many requests are out, under what cap, and for how long the longest of them has been. Nothing said
// it before: a request put off for want of a slot left no line at all, so a connection whose every request was put
// off looked the same in the log whether the upstream was at its limit or a slot had been lost (#6698). The time
// tells them apart: no request stays out past its caller's deadline (minutes), so one out for hours is not out.
// Under g.mu.
func (g *gate) noTurn() {
	at := now()
	if g.noTurns == 0 {
		g.noTurnSince = at
	}
	g.noTurns++
	if !g.noTurnLogged.IsZero() && at.Sub(g.noTurnLogged) < noTurnEvery {
		return
	}
	what := "none out"
	if len(g.out) > 0 {
		what = fmt.Sprintf("%d out", len(g.out))
		if g.cap > 0 {
			what += fmt.Sprintf(" under a cap of %d", g.cap)
		}
		// g.out is in the order they went out (admit), so its first is the one out longest.
		what += fmt.Sprintf(", the longest for %s", at.Sub(g.out[0]).Round(time.Second))
		if g.held && g.probing {
			what += "; one is finding out whether the pause is over"
		}
	}
	logf("ai: %s: %d request(s) put off for want of a turn since %s: %s", g.host, g.noTurns, clockText(g.noTurnSince), what)
	g.noTurns, g.noTurnLogged = 0, at
}

// clockText is a moment for the log: its time, with its day when that is not today (a Retry-After of hours runs
// past midnight, and "until 14:03:22" read as a pause already over).
func clockText(t time.Time) string {
	if t.Format(time.DateOnly) != now().In(t.Location()).Format(time.DateOnly) {
		return t.Format(time.DateTime)
	}
	return t.Format(time.TimeOnly)
}

// answers lets the connection go once the upstream said 200 to the request finding out, rather than when its answer
// has been read to the end: every call streams, so a long answer (a knowledge base call takes minutes) kept every
// other request on the connection put off for as long as it lasted. It says whether it let go: that request is an
// ordinary one from then on (done). The busy answers in a row are not forgotten yet: a limit the stream reports
// after its 200 holds the connection again, for longer than the last time.
func (g *gate) answers() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held || !g.probing {
		return false
	}
	g.probing = false
	g.letGo()
	g.changed()
	return true
}

// letGo ends the hold, under g.mu.
func (g *gate) letGo() {
	logf("ai: %s answers again after %s; %d request(s) were put off", g.host, now().Sub(g.since).Round(time.Second), g.putOff)
	g.held, g.putOff = false, 0
}

// probeAnswer is the transport of the request finding out whether a pause is over: it lets the gate go at the
// upstream's 200 (gate.answers); let says it did.
type probeAnswer struct {
	next http.RoundTripper
	g    *gate
	let  atomic.Bool
}

func (p *probeAnswer) RoundTrip(r *http.Request) (*http.Response, error) {
	next := p.next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(r)
	if err == nil && resp.StatusCode == http.StatusOK && p.g.answers() {
		p.let.Store(true)
	}
	return resp, err
}

// A best-effort call (e.g. three-second tagging) timing out says little about launcher
// capacity. Sample the remaining execution budget after any wait for a slot. Ten seconds
// leaves room for setup on normal callers with deadlines of thirty seconds or more.
const minRunTimeoutBudget = 10 * time.Second

func (g *gate) run(ctx context.Context, r Request, run func(context.Context, Request) (Reply, error)) (reply Reply, err error) {
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	t, err := g.admit(ctx)
	if err != nil {
		if ctx.Err() == context.Canceled {
			return Reply{}, context.Canceled
		}
		return Reply{}, err
	}
	deadline, bounded := ctx.Deadline()
	normalBudget := !bounded || time.Until(deadline) >= minRunTimeoutBudget
	err = errUnwound // also return the slot and release probe ownership if the callback panics
	defer func() { err = g.doneRun(t, normalBudget, ctx.Err() == context.Canceled, err) }()
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	return run(ctx, r)
}

// Two consecutive full-budget timeouts hold the launcher. After that, a timed-out
// probe renews the hold immediately. Only the probe can change an existing hold;
// a timeout already in flight when another run holds it is put off with that hold.
func (g *gate) doneRun(t turn, normalBudget, canceled bool, err error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	reason, detail := "", ""
	if g.held && !t.probe && normalBudget && !canceled && errors.Is(err, context.DeadlineExceeded) {
		return g.finish(t, false, false, err, ReasonBusy, 0, "repeated launcher timeouts")
	}
	if !g.held || t.probe {
		switch {
		case canceled || errors.Is(err, context.Canceled):
			// No observation of launcher capacity.
		case errors.Is(err, context.DeadlineExceeded):
			if normalBudget {
				if g.runTimedOut || t.probe {
					reason, detail = ReasonBusy, "repeated launcher timeouts"
				}
				g.runTimedOut = true
			}
		default:
			g.runTimedOut = false
		}
	}
	return g.finish(t, t.probe, err == nil && !canceled, err, reason, 0, detail)
}

// done records how a request that went out ended (answered: the upstream said 200) and returns the error for the
// caller: an answer that holds the connection becomes a PausedError. probe: it is still the one finding out (the
// gate did not go at its 200, answers).
func (g *gate) done(t turn, probe, answered bool, err error) error {
	var reason, detail string
	var after time.Duration
	if !answered {
		reason, after, detail = classify(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.finish(t, probe, answered, err, reason, after, detail)
}

// finish returns the slot and updates the shared backoff and concurrency cap under g.mu.
func (g *gate) finish(t turn, probe, answered bool, err error, reason string, after time.Duration, detail string) error {
	g.inflight--
	for i, at := range g.out {
		if at.Equal(t.at) {
			g.out = append(g.out[:i], g.out[i+1:]...)
			break
		}
	}
	defer g.changed()
	if probe {
		g.probing = false
	}
	if answered {
		switch {
		case g.held && probe:
			g.letGo()
			g.strikes = 0
		case !g.held:
			g.strikes = 0
			if g.cap > 0 {
				if g.fine++; g.fine >= growAfter {
					g.cap, g.fine = g.cap+1, 0
					if g.cap > capLift {
						g.cap = 0
					}
				}
			}
		}
		return err
	}
	if reason == "" {
		return err
	}
	// A request sent before the hold that comes back with the same answer changes nothing: only the one that holds
	// the connection and the probes count.
	counts := !g.held || probe
	pause := after
	if reason == ReasonQuota && pause <= 0 {
		pause = quotaPause
	}
	if reason == ReasonBusy {
		if counts {
			g.strikes++
			if g.cap == 0 {
				g.cap = max(1, (g.inflight+1)/2) // the requests in flight when it said busy, this one included
			} else {
				g.cap = max(1, g.cap/2)
			}
			g.fine = 0
		}
		pause = max(pause, min(busyFirst<<min(max(g.strikes-1, 0), 16), busyMost))
	}
	until := now().Add(pause)
	if !g.held {
		g.held, g.since, g.putOff = true, now(), 0
		g.pause = Pause{Reason: reason, Until: until}
		what := "is busy"
		if reason == ReasonQuota {
			what = "is out of quota"
		}
		logf("ai: %s %s (%s); requests are put off until %s", g.host, what, detail, clockText(until))
	} else {
		if until.After(g.pause.Until) {
			g.pause.Until = until
		}
		if reason == ReasonQuota {
			g.pause.Reason = ReasonQuota
		}
	}
	return &PausedError{g.pause}
}

func (g *gate) changed() {
	close(g.wake)
	g.wake = make(chan struct{})
}

// httpError is an answer other than 200. body keeps the established clipped text; the full parsed envelope is retained
// for failure classification and gate logs.
type httpError struct {
	status     int
	body       string
	retryAfter time.Duration
	code       string
	message    string
	errorType  string
	envelope   bool
	noRetry    bool
}

type redactedError struct {
	error
	text string
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.error }

func redactValues(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "***")
		}
	}
	return text
}

func (c *Client) secretValues(key string) []string {
	secrets := []string{key}
	for _, value := range c.cfg.Headers {
		secrets = append(secrets, value)
	}
	return secrets
}

// redactError covers failures reported inside a 200 stream as well as HTTP errors. Typed errors keep their unwrap
// chain and limit/model classification; the streamLimit's own message is scrubbed too because the gate logs it directly.
func (c *Client) redactError(err error, key string) error {
	if err == nil {
		return nil
	}
	secrets := c.secretValues(key)
	var limit *streamLimit
	if errors.As(err, &limit) {
		limit.msg = redactValues(limit.msg, secrets)
	}
	var stream *streamFailure
	if errors.As(err, &stream) {
		stream.msg = redactValues(stream.msg, secrets)
	}
	var httpFailure *httpError
	if errors.As(err, &httpFailure) {
		httpFailure.body = redactValues(httpFailure.body, secrets)
		httpFailure.message = redactValues(httpFailure.message, secrets)
	}
	text := redactValues(err.Error(), secrets)
	if text == err.Error() {
		return err
	}
	return &redactedError{error: err, text: text}
}

func (c *Client) newHTTPError(resp *http.Response, raw []byte, key string) *httpError {
	return newHTTPError(resp, raw, c.secretValues(key)...)
}

func newHTTPError(resp *http.Response, raw []byte, secrets ...string) *httpError {
	raw = []byte(redactValues(string(raw), secrets))
	code, message, errorType, envelope := gatewayErrorDetailsWithType(raw)
	after := parseRetryAfter(resp.Header.Get("Retry-After"))
	return &httpError{status: resp.StatusCode, body: strings.TrimSpace(string(raw[:min(len(raw), 300)])),
		retryAfter: after, code: code, message: message, errorType: errorType,
		envelope: envelope, noRetry: noRetryHeader(resp.Header)}
}

// noRetryHeader reports whether the gateway explicitly forbids retrying this response.
func noRetryHeader(header http.Header) bool {
	return strings.EqualFold(strings.TrimSpace(header.Get("X-Should-Retry")), "false")
}

func (e *httpError) Error() string { return fmt.Sprintf("ai: HTTP %d: %s", e.status, e.body) }
func (e *httpError) failureKind() FailureKind {
	return failureKindFor(e.status, e.code, e.message, e.errorType)
}

// Transient: the same request tried again may get an answer: the call ran out of time, the connection failed (before
// the answer, or partway through it: a body shorter than it said, a stream that stopped before its end, a reset), or
// an upstream answered 5xx or 408 (often a gateway in front of the model), also as a Messages error after the 200
// (streamFailure). Recognized model/context/endpoint codes and an exact X-Should-Retry: false (case-insensitive,
// surrounded by optional space) override that 5xx/408 default. A request turned down (other 4xx), a model that isn't
// set up, a pause (IsPaused: wait for the gate, don't hurry it) and a reply's own problems (a refusal, a reply that
// doesn't decode) are not.
func Transient(err error) bool {
	if err == nil || IsPaused(err) {
		return false
	}
	var h *httpError
	if errors.As(err, &h) {
		if _, typed := failureKindForCode(h.code); typed {
			return false
		}
		return !h.noRetry && (h.status >= 500 || h.status == http.StatusRequestTimeout)
	}
	var f *streamFailure
	if errors.As(err, &f) {
		if _, typed := failureKindForCode(f.code); typed {
			return false
		}
		return f.status >= 500 || f.status == http.StatusRequestTimeout
	}
	// A *url.Error is a request that got no answer at all (a deadline, a refused or reset connection); a *net.OpError
	// is a connection that failed while the answer was being read. Both are net.Errors. A reset or broken pipe counts
	// also when it comes without one around it.
	var n net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.As(err, &n)
}

// parseRetryAfter reads digits-only Retry-After seconds or an HTTP date. Malformed and absent values are not a
// cooldown; callers require a positive duration for the 429 quota-reset exception. Large values cap before the
// duration multiplication can overflow.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	digits := true
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			digits = false
			break
		}
	}
	if digits {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0
		}
		if n >= uint64(afterMost/time.Second) {
			return afterMost
		}
		return time.Duration(n) * time.Second
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	return min(max(t.Sub(now()), 0), afterMost)
}

// retryAfter reads Retry-After: seconds or an HTTP date, returning zero when it is absent or malformed.
func retryAfter(v string) time.Duration {
	return parseRetryAfter(v)
}

var quotaWords = lazyre.New(`quota|usage limit|exhaust|insufficient|credit|billing`)

// streamLimit is a stream that answered 200 and then reported a limit: a Responses stream's response.failed or error
// event, as relays and Codex-style backends send, or a Messages stream's error event (overloaded_error,
// rate_limit_error). It holds the connection like the HTTP answer; its text is the event's as before.
type streamLimit struct{ reason, event, code, msg string }

func (e *streamLimit) Error() string { return fmt.Sprintf("ai: %s: %s", e.event, e.msg) }

// streamFailure is any other failure a Messages error reported after the 200 (an error event, or an error body a
// gateway sent with 200). status is the HTTP status its type stands for, so it is Transient exactly when that HTTP
// answer would be: an api_error (500) or timeout_error (504) is tried again as before streaming, when it came as the
// status itself; an unknown type is not.
type streamFailure struct {
	status          int
	kind, code, msg string
}

func (e *streamFailure) Error() string { return fmt.Sprintf("ai: error: %s: %s", e.kind, e.msg) }

// messageErrorStatus is the HTTP status of each Messages API error type (limits are in streamCodes).
var messageErrorStatus = map[string]int{
	"invalid_request_error": http.StatusBadRequest, "authentication_error": http.StatusUnauthorized,
	"billing_error": http.StatusPaymentRequired, "permission_error": http.StatusForbidden,
	"not_found_error": http.StatusNotFound, "request_too_large": http.StatusRequestEntityTooLarge,
	"api_error": http.StatusInternalServerError, "timeout_error": http.StatusGatewayTimeout,
}

// streamCodes are explicit error codes that report a limit, and which one. HTTP classification uses the same machine
// semantics; the Messages API's overloaded_error and rate_limit_error entries preserve its established type behavior.
var streamCodes = map[string]string{
	"rate_limit_exceeded": ReasonBusy, "slow_down": ReasonBusy, "server_is_overloaded": ReasonBusy,
	"rpm_limit": ReasonBusy, "tpm_limit": ReasonBusy, "concurrency_limit": ReasonBusy, "account_concurrency_full": ReasonBusy,
	"insufficient_quota": ReasonQuota, "credit_balance_exhausted": ReasonQuota,
	"organization_spend_limit_exceeded": ReasonQuota, "project_spend_limit_exceeded": ReasonQuota,
	"account_quota_exhausted": ReasonQuota, "budget_exhausted": ReasonQuota, "weekly_budget_exhausted": ReasonQuota,
	"api_key_model_limit_exhausted": ReasonQuota, "quota_exhausted": ReasonQuota,
	// The Messages API's error types for HTTP 529 and 429, which a stream reports in an error event after the 200.
	"overloaded_error": ReasonBusy, "rate_limit_error": ReasonBusy,
}

// limitReasonForCode reads only an explicit code, never error.type. Recognized rate/concurrency codes stay busy even
// when their message speaks of quota; rate_limit_error keeps its pre-existing quota-wording exception.
func limitReasonForCode(code, msg string) (string, bool) {
	code = strings.TrimSpace(code)
	reason, ok := streamCodes[strings.ToLower(code)]
	if !ok {
		return "", false
	}
	if strings.EqualFold(code, "rate_limit_error") && quotaWords.MatchString(strings.ToLower(msg)) {
		reason = ReasonQuota // as an HTTP 429 whose body speaks of quota (classify)
	}
	return reason, true
}

// streamLimitFor is the limit an in-stream failure's code reports, nil for any other failure.
func streamLimitFor(event, code, msg string) *streamLimit {
	reason, ok := limitReasonForCode(code, msg)
	if !ok {
		return nil
	}
	return &streamLimit{reason: reason, event: event, code: strings.TrimSpace(code), msg: msg}
}

var (
	// tryAgainIn finds the wait in "try again in ...": one or more number-and-unit parts ("1.5s", "250ms", "20 seconds",
	// "6m0s", "1m30s", "2 minutes", "1h"). A longer unit name comes before its prefix: the first that fits is taken.
	tryAgainIn = lazyre.New(`(?i)try again in\s*((?:\d+(?:\.\d+)?\s*(?:ms|milliseconds?|seconds?|secs?|s|minutes?|mins?|m|hours?|hrs?|h)\s*)+)\b`)
	// tryAgainPart is one number-and-unit part of tryAgainIn's match.
	tryAgainPart = lazyre.New(`(\d+(?:\.\d+)?)\s*([A-Za-z]+)`)
)

// tryAgain is the wait a stream's limit message asks for ("Please try again in 1.5s", "Please try again in 6m0s"),
// which a stream has instead of Retry-After: the sum of its parts, at most afterMost; 0 when it names none.
func tryAgain(msg string) time.Duration {
	m := tryAgainIn.FindStringSubmatch(msg)
	if m == nil {
		return 0
	}
	total := 0.0
	for _, p := range tryAgainPart.FindAllStringSubmatch(m[1], -1) {
		n, err := strconv.ParseFloat(p[1], 64)
		if err != nil {
			return 0
		}
		total += n * float64(tryAgainUnit(p[2]))
	}
	if total < float64(afterMost) {
		return time.Duration(total)
	}
	return afterMost
}

// tryAgainUnit is the duration of one of tryAgainIn's unit names.
func tryAgainUnit(name string) time.Duration {
	switch name = strings.ToLower(name); {
	case name == "ms" || strings.HasPrefix(name, "milli"):
		return time.Millisecond
	case strings.HasPrefix(name, "h"):
		return time.Hour
	case strings.HasPrefix(name, "m"):
		return time.Minute
	default:
		return time.Second
	}
}

// classify: whether err is an answer that holds the connection, why, for how long the upstream asked, and what it
// said (for the log line).
func classify(err error) (reason string, after time.Duration, detail string) {
	var s *streamLimit
	if errors.As(err, &s) {
		return s.reason, tryAgain(s.msg), "stream " + s.code + ": " + s.msg[:min(len(s.msg), 120)]
	}
	var h *httpError
	if !errors.As(err, &h) {
		return "", 0, ""
	}
	code, message, errorType, envelope := h.code, h.message, h.errorType, h.envelope
	if code == "" && message == "" && errorType == "" && !envelope { // also support direct httpError test fixtures
		code, message, errorType, envelope = gatewayErrorDetailsWithType([]byte(h.body))
	}
	body := strings.ToLower(h.body)
	codedReason, coded := limitReasonForCode(code, message)
	if _, typed := failureKindForCode(code); typed {
		// Model, context and endpoint semantics are more specific than any status-based gate action.
		return "", 0, ""
	}
	switch {
	case coded && codedReason == ReasonQuota:
		// Explicit quota exhaustion wins on any status, including an otherwise non-retryable response.
		reason = ReasonQuota
	case h.status == http.StatusPaymentRequired:
		reason = ReasonQuota
	case h.status == http.StatusTooManyRequests && h.noRetry && h.retryAfter > 0:
		// Only a valid positive 429 cooldown is a quota reset instead of an ordinary busy answer. Other no-retry
		// statuses retain their raw behavior; busy statuses without one still use the gate.
		reason = ReasonQuota
	case h.status != http.StatusTooManyRequests && h.status != http.StatusServiceUnavailable && h.status != 529:
		return "", 0, ""
	case coded:
		reason = codedReason
	case strings.Contains(body, "concurrency"):
		reason = ReasonBusy
	case h.status == http.StatusTooManyRequests && quotaWords.MatchString(body):
		reason = ReasonQuota
	default: // a plain rate limit, 503 or 529 (overloaded)
		reason = ReasonBusy
	}
	detail = fmt.Sprintf("HTTP %d", h.status)
	if envelope && (code != "" || message != "" || errorType != "") {
		label := code
		if label == "" {
			label = errorType
		}
		detail += " " + strings.TrimSpace(label+": "+message)
	} else if h.body != "" {
		detail += " " + h.body[:min(len(h.body), 120)]
	}
	return reason, h.retryAfter, detail
}
