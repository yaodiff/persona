package extmgr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// An extension's sign-in (manifest.auth, docs/extensions/extensions.md "Sign-in (auth)", #4507): its program answers auth.status,
// auth.step and auth.logout; the hub keeps the last status it heard, shows it on the extension, tells the apps when
// it changes (extension_auth) and passes the apps' sign-in steps on. What signing in yields stays with the program, in
// its data directory: the values of a step go to it only, never into a log, an audit or an answer.

// The states of ExtensionAuth.
const (
	authUnknown   = "unknown" // the program does not run (switched off, waiting for its settings), or was not asked yet
	authSignedOut = "signed_out"
	authPending   = "pending"
	authSignedIn  = "signed_in"
	authExpired   = "expired"
	authError     = "error"
)

const (
	authStatusWait = 10 * time.Second
	authStepWait   = time.Minute
)

// ExtensionAuth is an extension's sign-in as the apps see it.
type ExtensionAuth struct {
	Title         string     `json:"title"`
	Description   string     `json:"description,omitempty"`
	Page          string     `json:"page,omitempty"`
	State         string     `json:"state"`
	Account       string     `json:"account,omitempty"`
	Message       string     `json:"message,omitempty"`
	MessageDetail string     `json:"message_detail,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CheckedAt     *time.Time `json:"checked_at,omitempty"`
}

// authStatus is the program's answer to auth.status and auth.logout.
type authStatus struct {
	// hubMessage distinguishes hub-owned failures from the program's authored message.
	hubMessage bool
	State      string     `json:"state"`
	Account    string     `json:"account,omitempty"`
	Message    string     `json:"message,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

func authStatusFromWire(status wire.AuthStatus) authStatus {
	st := authStatus{State: status.State, Account: status.Account, Message: status.Message}
	if !status.ExpiresAt.IsZero() {
		expiresAt := status.ExpiresAt
		st.ExpiresAt = &expiresAt
	}
	return st
}

// authPrompt is a sign-in step: the connectors' Prompt, and when its code or link stops working.
type authPrompt struct {
	connector.Prompt
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// authCache is the last status of each extension that signs in.
type authCache struct {
	mu         sync.Mutex
	byID       map[string]authStatus
	at         map[string]time.Time
	epoch      map[string]uint64
	refreshing map[string]bool
	again      map[string]bool
}

func (c *authCache) get(id string) (authStatus, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.byID[id]
	return st, c.at[id], ok
}

func (c *authCache) epochOf(id string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch[id]
}

// put keeps st for id and says whether it changed.
func (c *authCache) put(id string, st authStatus) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.putLocked(id, st)
}

// putIfEpoch keeps st for id only if no drop or superseding write occurred since epoch was observed.
// When bump is true (e.g. after an explicit logout), the epoch advances so older in-flight probes
// cannot overwrite the newer state.
func (c *authCache) putIfEpoch(id string, epoch uint64, st authStatus, bump bool) (stored, changed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch[id] != epoch {
		return false, false
	}
	if bump {
		if c.epoch == nil {
			c.epoch = map[string]uint64{}
		}
		c.epoch[id]++
	}
	return true, c.putLocked(id, st)
}

func (c *authCache) putLocked(id string, st authStatus) bool {
	if c.byID == nil {
		c.byID, c.at = map[string]authStatus{}, map[string]time.Time{}
	}
	old, had := c.byID[id]
	c.byID[id], c.at[id] = st, time.Now()
	return !had || old.State != st.State || old.Account != st.Account || old.Message != st.Message || old.hubMessage != st.hubMessage || !sameTime(old.ExpiresAt, st.ExpiresAt)
}

// drop invalidates id's cached status, advances its epoch to fence in-flight probes,
// and reports whether a cached status was present. Queued follow-up refreshes (again)
// are preserved so an auth.changed emitted by a newly started replacement process is not lost.
func (c *authCache) drop(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.epoch == nil {
		c.epoch = map[string]uint64{}
	}
	c.epoch[id]++
	_, had := c.byID[id]
	delete(c.byID, id)
	delete(c.at, id)
	return had
}

// beginRefresh claims the single background refresh slot for id. If one is already running,
// it marks a follow-up refresh due so auth.changed events are never lost.
func (c *authCache) beginRefresh(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing == nil {
		c.refreshing = map[string]bool{}
	}
	if c.refreshing[id] {
		if c.again == nil {
			c.again = map[string]bool{}
		}
		c.again[id] = true
		return false
	}
	c.refreshing[id] = true
	return true
}

// continueRefresh consumes a pending follow-up refresh if requested while the active pass ran,
// or releases the refresh slot atomically when none is pending.
func (c *authCache) continueRefresh(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.again[id] {
		delete(c.again, id)
		return true
	}
	delete(c.refreshing, id)
	return false
}

func (c *authCache) endRefresh(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.refreshing, id)
	delete(c.again, id)
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// validAuthState is a state a program may answer.
func validAuthState(s string) bool {
	switch s {
	case authSignedOut, authPending, authSignedIn, authExpired, authError:
		return true
	}
	return false
}

// cachedAuthView reads p's sign-in status from the cache without spawning background refreshes.
// ready is false only when p is running and its auth status has not been cached yet.
func (ps *extensions) cachedAuthView(p *extension.Extension, langs ...i18n.Lang) (v *ExtensionAuth, ready bool) {
	if p == nil || p.Full == nil || p.Full.Auth == nil {
		return nil, false
	}
	spec := p.Full.Auth
	v = &ExtensionAuth{Title: spec.Title, Description: spec.Description, Page: spec.Page, State: authUnknown}
	if !ps.authRuns(p) {
		return v, true
	}
	st, at, ok := ps.auth.get(p.Record.ID)
	if !ok {
		return v, false
	}
	v.State, v.Account, v.Message, v.ExpiresAt, v.CheckedAt = st.State, st.Account, st.Message, st.ExpiresAt, &at
	if st.hubMessage && len(langs) > 0 {
		v.Message, v.MessageDetail = i18n.ExtensionFailureText(langs[0], v.Message, "extension-status.auth_failed")
	}
	return v, true
}

// authView is p's sign-in from the cache (nil when it does not sign in): unknown while its program does not run or
// was not asked yet. It asks in the background when it was not.
func (ps *extensions) authView(p *extension.Extension, langs ...i18n.Lang) *ExtensionAuth {
	v, ready := ps.cachedAuthView(p, langs...)
	if v != nil && !ready {
		go ps.refreshAuth(context.Background(), p.Record.ID)
	}
	return v
}

// authProc returns p's runnable host process when p is enabled, healthy, and attached to a Proc.
func (ps *extensions) authProc(p *extension.Extension) *host.Proc {
	if p == nil || !p.Record.Enabled || p.Problem != "" {
		return nil
	}
	hp := ps.get(p.Record.ID)
	if hp == nil || hp.proc == nil {
		return nil
	}
	return hp.proc
}

// authRuns says whether p's program can be asked about its sign-in now: on, set up, and started (or started by
// being called).
func (ps *extensions) authRuns(p *extension.Extension) bool {
	return ps.authProc(p) != nil
}

// refreshAuth asks extension id for its status, keeps it and tells the apps when it changed. Background callers
// (an auth.changed event, a view that had none) run one at a time per extension; notifications arriving while
// a refresh is in flight trigger one coalesced follow-up pass.
func (ps *extensions) refreshAuth(ctx context.Context, id string) {
	if !ps.auth.beginRefresh(id) {
		return
	}
	released := false
	defer func() {
		if !released {
			ps.auth.endRefresh(id)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		_, _ = ps.askAuth(ctx, id)
		if !ps.auth.continueRefresh(id) {
			released = true
			return
		}
	}
}

// askAuth asks extension id's program for its sign-in status now.
func (ps *extensions) askAuth(ctx context.Context, id string) (*ExtensionAuth, error) {
	p := ps.reg.Snapshot(id)
	if p == nil || p.Full == nil || p.Full.Auth == nil {
		return nil, errNoAuth
	}
	id = p.Record.ID
	epoch := ps.auth.epochOf(id)
	proc := ps.authProc(p)
	if proc == nil {
		v, _ := ps.cachedAuthView(p)
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctx, authStatusWait)
	defer cancel()
	status, err := buswire.Call(ctx, proc, extwire.AuthStatusMethod, buswire.None{})
	st := authStatusFromWire(status)
	if err != nil {
		var remote *rpc.Error
		st = authStatus{State: authError, Message: err.Error(), hubMessage: !errors.As(err, &remote) || errors.As(err, new(*i18n.Message))}
	} else if !validAuthState(st.State) {
		st = authStatus{State: authError, Message: i18n.T(i18n.En, "extension-status.auth_unknown_state", i18n.Args{"state": st.State}), hubMessage: true}
	}
	cur := ps.reg.Snapshot(id)
	if cur == nil || cur.Full == nil || cur.Full.Auth == nil {
		return nil, errNoAuth
	}
	var langs []i18n.Lang
	if ps.h != nil {
		langs = []i18n.Lang{ps.h.contextLang(ctx)}
	}
	if ps.authProc(cur) != proc {
		v, _ := ps.cachedAuthView(cur, langs...)
		return v, nil
	}
	ps.keepAuthAt(cur, epoch, st, false)
	v, _ := ps.cachedAuthView(cur, langs...)
	return v, nil
}

func (ps *extensions) keepAuthAt(p *extension.Extension, epoch uint64, st authStatus, bump bool) {
	stored, changed := ps.auth.putIfEpoch(p.Record.ID, epoch, st, bump)
	if !stored || !changed || ps.h == nil {
		return
	}
	v, _ := ps.cachedAuthView(p)
	ps.h.broadcast(map[string]any{"type": "extension_auth", "extension": p.Record.ID, "auth": v, "hub_auth_message": st.hubMessage})
}

// forgetAuth drops what is known of id's sign-in (its program stopped, it was removed) and tells the apps.
func (ps *extensions) forgetAuth(id string) {
	if !ps.auth.drop(id) {
		return
	}
	if ps.h == nil {
		return
	}
	if p := ps.reg.Snapshot(id); p != nil && p.Full != nil && p.Full.Auth != nil {
		ps.h.broadcast(map[string]any{"type": "extension_auth", "extension": id, "auth": ps.authView(p)})
	}
}

// needsSignIn says whether extension id signs in and is not signed in (an install's next step is to).
func (h *Manager) needsSignIn(ctx context.Context, id string) bool {
	ps := h.extensions
	v, err := ps.askAuth(ctx, id)
	if err != nil || v == nil {
		return false
	}
	return v.State == authSignedOut || v.State == authExpired
}

// authWarning is the line old apps show for an extension that is not signed in.
func authWarning(l i18n.Lang, v *ExtensionAuth) string {
	switch {
	case v == nil:
		return ""
	case v.State == authSignedOut:
		return i18n.T(l, "hub-extension_auth.warn_signed_out", i18n.Args{"title": v.Title})
	case v.State == authExpired:
		return i18n.T(l, "hub-extension_auth.warn_expired", i18n.Args{"title": v.Title})
	}
	return ""
}

var errNoAuth = i18n.ErrEnglish("extension-status.no_auth")

// authQR draws the QR code of a step's URL: a qr step's (to scan with another app) and a device code's (its link,
// for another phone).
func authQR(p *connector.Prompt) error {
	p.QR = nil
	if p.Kind != connector.PromptQRCode && p.Kind != connector.PromptDeviceCode {
		return nil
	}
	if p.URL == "" {
		if p.Kind == connector.PromptQRCode {
			return i18n.ErrEnglish("extension-status.qr_missing_url")
		}
		return nil
	}
	rows, size, err := qrRows(p.URL)
	if err != nil {
		return err
	}
	p.QR = &connector.QRCode{Size: size, Rows: rows}
	return nil
}

func (h *Manager) authRoutes(api *http.ServeMux, full func(f func(w http.ResponseWriter, r *http.Request, p *extension.Extension)) http.HandlerFunc) {
	ps := func() *extensions { return h.extensions }
	noAuth := func(w http.ResponseWriter, r *http.Request, p *extension.Extension) bool {
		if p != nil && p.Full != nil && p.Full.Auth != nil {
			return false
		}
		writeExtensionResponseError(w, r, http.StatusNotFound, "no_auth", i18n.Err("hub-extension_auth.no_auth"))
		return true
	}
	// running returns p's program to talk to about signing in, or writes why there is none.
	running := func(w http.ResponseWriter, r *http.Request, p *extension.Extension) *host.Proc {
		if noAuth(w, r, p) {
			return nil
		}
		snap := ps().reg.Snapshot(p.Record.ID)
		if noAuth(w, r, snap) {
			return nil
		}
		if !snap.Record.Enabled {
			writeExtensionResponseError(w, r, http.StatusConflict, "disabled", i18n.Err("hub-extension_auth.disabled"))
			return nil
		}
		proc := ps().authProc(snap)
		if proc == nil {
			writeExtensionResponseError(w, r, http.StatusConflict, "not_running", i18n.Err("hub-extension_auth.not_running"))
			return nil
		}
		return proc
	}
	api.HandleFunc("GET /api/v1/extensions/{id}/-/auth", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		if noAuth(w, r, p) {
			return
		}
		v, err := ps().askAuth(r.Context(), p.Record.ID)
		if err != nil {
			writeExtensionResponseErr(w, r, http.StatusBadGateway, err)
			return
		}
		writeExtensionJSON(w, r, v)
	}))
	api.HandleFunc("POST /api/v1/extensions/{id}/-/auth/step", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		if running(w, r, p) == nil {
			return
		}
		var in struct {
			Step   string            `json:"step"`
			Values map[string]string `json:"values"`
		}
		if err := decodeJSONBody(w, r, &in, 16<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if in.Values == nil {
			in.Values = map[string]string{}
		}
		cur := ps().reg.Snapshot(p.Record.ID)
		if noAuth(w, r, cur) {
			return
		}
		proc := ps().authProc(cur)
		if proc == nil {
			writeExtensionResponseError(w, r, http.StatusConflict, "not_running", i18n.Err("hub-extension_auth.not_running"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), authStepWait)
		defer cancel()
		var pr authPrompt
		if err := proc.Call(ctx, "auth.step", map[string]any{"step": in.Step, "values": in.Values}, &pr); err != nil {
			writeExtensionResponseErr(w, r, http.StatusBadGateway, err)
			return
		}
		if err := authQR(&pr.Prompt); err != nil {
			writeExtensionResponseErr(w, r, http.StatusBadGateway, err)
			return
		}
		if in.Step == "" {
			ps().reg.Audit(r.Context(), p.Record.ID, "auth_start", actor(r), nil)
		}
		if pr.Done {
			ps().reg.Audit(r.Context(), p.Record.ID, "auth_signed_in", actor(r), nil)
			_, _ = ps().askAuth(context.WithoutCancel(r.Context()), p.Record.ID)
		} else if in.Step == "" {
			_, _ = ps().askAuth(context.WithoutCancel(r.Context()), p.Record.ID) // now pending
		}
		writeExtensionJSON(w, r, pr)
	}))
	api.HandleFunc("POST /api/v1/extensions/{id}/-/auth/logout", full(func(w http.ResponseWriter, r *http.Request, p *extension.Extension) {
		proc := running(w, r, p)
		if proc == nil {
			return
		}
		epoch := ps().auth.epochOf(p.Record.ID)
		ctx, cancel := context.WithTimeout(r.Context(), authStepWait)
		defer cancel()
		status, err := buswire.Call(ctx, proc, extwire.AuthLogoutMethod, buswire.None{})
		if err != nil {
			writeExtensionResponseErr(w, r, http.StatusBadGateway, err)
			return
		}
		st := authStatusFromWire(status)
		ps().reg.Audit(r.Context(), p.Record.ID, "auth_logout", actor(r), nil)
		cur := ps().reg.Snapshot(p.Record.ID)
		if cur != nil && cur.Full != nil && cur.Full.Auth != nil {
			if validAuthState(st.State) {
				if ps().authProc(cur) == proc {
					ps().keepAuthAt(cur, epoch, st, true)
				}
			} else {
				_, _ = ps().askAuth(context.WithoutCancel(r.Context()), p.Record.ID)
			}
			v, _ := ps().cachedAuthView(cur, h.requestLang(r))
			writeExtensionJSON(w, r, v)
			return
		}
		v, _ := ps().cachedAuthView(p, h.requestLang(r))
		writeExtensionJSON(w, r, v)
	}))
}

// authChanged is a program's auth.changed event: ask it again.
func (ps *extensions) authChanged(id string, _ json.RawMessage) {
	go ps.refreshAuth(context.Background(), id)
}

// extensionAuthCommand is `personad extension auth <id> [login|logout]` (extension_cli.go).
func extensionAuthCommand(ctx context.Context, c *extensionClient, args []string, stdin io.Reader, stdout io.Writer) error {
	base := "/api/v1/extensions/" + url.PathEscape(args[0]) + "/-/auth"
	l := c.l()
	say := func(v ExtensionAuth) {
		key := map[string]string{authUnknown: "extmgr-cli.auth_unknown", authSignedOut: "extmgr-cli.auth_signed_out",
			authPending: "extmgr-cli.auth_pending", authSignedIn: "extmgr-cli.auth_signed_in",
			authExpired: "extmgr-cli.auth_expired", authError: "extmgr-cli.auth_error"}[v.State]
		line := v.State
		if key != "" {
			line = i18n.T(l, key)
		}
		if v.Account != "" {
			line = i18n.T(l, "extmgr-cli.auth_account", i18n.Args{"state": line, "account": v.Account})
		}
		if v.Message != "" {
			line += i18n.T(l, "extmgr-cli.paren", i18n.Args{"text": v.Message})
		}
		fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.label_value", i18n.Args{"label": v.Title, "value": line}))
	}
	what := ""
	if len(args) > 1 {
		what = args[1]
	}
	switch what {
	case "":
		var v ExtensionAuth
		if err := c.do(ctx, "GET", base, nil, &v); err != nil {
			return err
		}
		say(v)
		return nil
	case "logout":
		var v ExtensionAuth
		if err := c.do(ctx, "POST", base+"/logout", map[string]string{}, &v); err != nil {
			return err
		}
		say(v)
		return nil
	case "login":
	default:
		return errors.New("personad extension auth <id> [login|logout]")
	}
	in := bufio.NewReader(stdin)
	step, values := "", map[string]string{}
	shown := ""
	for {
		var p authPrompt
		if err := c.do(ctx, "POST", base+"/step", map[string]any{"step": step, "values": values}, &p); err != nil {
			return err
		}
		if p.Done {
			if p.Account != "" {
				fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.auth_done_account", i18n.Args{"account": p.Account}))
			} else {
				fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.auth_done"))
			}
			return nil
		}
		if p.Error != "" {
			fmt.Fprintln(stdout, p.Error)
		}
		step, values = p.Step, map[string]string{}
		switch p.Kind {
		case connector.PromptDeviceCode, connector.PromptQRCode:
			if key := p.URL + "\n" + p.UserCode; key != shown {
				shown = key
				if p.QR != nil {
					printQR(stdout, p.QR.Rows)
				}
				if p.UserCode != "" {
					fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.auth_open_code", i18n.Args{"url": p.URL, "code": p.UserCode}))
				} else {
					fmt.Fprintln(stdout, i18n.T(l, "extmgr-cli.auth_open", i18n.Args{"url": p.URL}))
				}
				if p.Hint != "" {
					fmt.Fprintln(stdout, p.Hint)
				}
			}
			wait := time.Duration(max(p.Interval, 2)) * time.Second
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		default:
			if p.Hint != "" {
				fmt.Fprintln(stdout, p.Hint)
			}
			for _, f := range p.Fields {
				fmt.Fprint(stdout, i18n.T(l, "extmgr-cli.field_prompt", i18n.Args{"label": f.Label})+" ")
				var line string
				if f.Secret {
					if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
						b, err := term.ReadPassword(int(file.Fd()))
						fmt.Fprintln(stdout)
						if err != nil {
							return err
						}
						line = string(b)
					}
				}
				if line == "" {
					read, err := in.ReadString('\n')
					if err != nil && read == "" {
						return err
					}
					line = read
				}
				values[f.Name] = strings.TrimSpace(line)
			}
		}
	}
}

// printQR draws a QR code's rows ("0" / "1") in a terminal, two rows a line, with its quiet zone.
func printQR(w io.Writer, rows []string) {
	if len(rows) == 0 {
		return
	}
	size := len(rows[0])
	at := func(y, x int) bool { return y >= 0 && y < len(rows) && x >= 0 && x < size && rows[y][x] == '1' }
	for y := -2; y < len(rows)+2; y += 2 {
		var b strings.Builder
		for x := -2; x < size+2; x++ {
			top, bottom := at(y, x), at(y+1, x)
			switch { // light on dark terminals: a dark module is a space
			case top && bottom:
				b.WriteString(" ")
			case top:
				b.WriteString("▄")
			case bottom:
				b.WriteString("▀")
			default:
				b.WriteString("█")
			}
		}
		fmt.Fprintln(w, b.String())
	}
}
