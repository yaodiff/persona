package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

const (
	pairLifetime = 10 * time.Minute
	pairGrace    = 2 * time.Minute
)

type pairCode struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	URL       string    `json:"url"`
	HubID     string    `json:"hub_id"`
	FP        string    `json:"fp"`
	TLSPort   int       `json:"tls_port"`
	Alt       []string  `json:"alt"`
	// Machine: made for that machine's persona-link (`personad hub pair --machine`); the URL is then persona://link?….
	Machine string `json:"machine,omitempty"`
	PairURL string `json:"pair_url"`
}

func (p pairCode) pairURL() string {
	values := url.Values{"url": {p.URL}, "token": {p.Token}}
	if p.Machine != "" {
		values.Set("machine", p.Machine)
	}
	if p.HubID != "" {
		values.Set("hub", p.HubID)
	}
	if p.FP != "" {
		values.Set("fp", p.FP)
	}
	if p.TLSPort > 0 {
		values.Set("tls", strconv.Itoa(p.TLSPort))
	}
	for _, alt := range p.Alt {
		values.Add("alt", alt)
	}
	if p.Machine != "" {
		return "persona://link?" + values.Encode()
	}
	return "persona://pair?" + values.Encode()
}

// pairCodeReply is a pairing code with what a signed-in device shows for it: the QR code of the app's pairing link
// and, asked with {"web": the page's address}, the link that signs a browser in and its QR code (#2731).
type pairCodeReply struct {
	*pairCode
	QR     *qrImage `json:"qr,omitempty"`
	WebURL string   `json:"web_url,omitempty"`
	WebQR  *qrImage `json:"web_qr,omitempty"`
	// A computer's code (docs/assistant/onboarding.md 5.3): its name, whether it waits to be confirmed (pending / confirmed),
	// whether it was added now, the command that pairs its Persona Link and the line that installs and pairs a node
	// by system (only linux, when the hub knows the site it was published on).
	Name    string            `json:"name,omitempty"`
	Trust   string            `json:"trust,omitempty"`
	Added   bool              `json:"added,omitempty"`
	Command string            `json:"command,omitempty"`
	Install map[string]string `json:"install,omitempty"`
	// Manager is the manager the computer belongs to now (entryOwner): the one add.manager named, unless it could not
	// be. A hub from before add.manager leaves it out (and adds every computer to the carrier).
	Manager string `json:"manager,omitempty"`
}

type qrImage struct {
	Size int      `json:"size"`
	Rows []string `json:"rows"`
}

func newQRImage(text string) (*qrImage, error) {
	rows, size, err := qrRows(text)
	if err != nil {
		return nil, err
	}
	return &qrImage{Size: size, Rows: rows}, nil
}

// webSignInBase checks the page address a device sends for the browser sign-in link: an absolute http(s) URL
// without a fragment, since the token goes in one (`#token=`, which never reaches a server; web/src/lib/auth.ts).
func webSignInBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("web must be the page's http(s) address, without a #fragment")
	}
	return raw, nil
}

// pairRoutes are the pairing endpoints: short-lived pairing tokens for `personad hub pair`
// (docs/archive/redesign/hub-changes.md 1.3), and for a signed-in device pairing another one (#2731).
// Also a computer's code (machines_add.go): for a link computer there is, or one added now, from the master token over
// loopback or a signed-in full device.
func (h *Hub) pairRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/auth/pair-codes", func(w http.ResponseWriter, r *http.Request) {
		who := principalOf(r.Context())
		fromDevice := who.ID != ""
		source := h.sourceOf(r)
		if fromDevice {
			// A device with full access can already add devices (POST /api/v1/auth/tokens); this code is safer to
			// hand over: it expires in ten minutes and is traded once. Not from a pairing code itself (no chain of
			// codes), the watch, a link, a peer or an extension.
			if !mayTrust(who) {
				writeErr(w, http.StatusForbidden, errors.New("this device may not pair other devices"))
				return
			}
		} else if source != sourceLoopback {
			writeErr(w, http.StatusForbidden, errors.New("pairing codes require the master token over loopback"))
			return
		}
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		// {"machine": id} makes a code for that link machine's persona-link (link.go), {"add": {…}} adds the computer
		// first (machines_add.go); no body, one for an app.
		var req struct {
			Machine string             `json:"machine"`
			Add     *machineAddRequest `json:"add"`
			Web     string             `json:"web"`
		}
		b, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if readErr != nil {
			writeBodyErr(w, readErr, readErr)
			return
		}
		if len(bytes.TrimSpace(b)) > 0 {
			if err := json.Unmarshal(b, &req); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		if req.Machine != "" || req.Add != nil {
			if req.Web != "" {
				writeErr(w, http.StatusBadRequest, errors.New("web is for a code that pairs an app or a browser"))
				return
			}
			if req.Machine != "" && req.Add != nil {
				writeErr(w, http.StatusBadRequest, errors.New("machine or add, not both (add.id names the computer to add)"))
				return
			}
			h.serveComputerCode(w, r, who, fromDevice, req.Machine, req.Add)
			return
		}
		web := ""
		if req.Web != "" {
			var err error
			if web, err = webSignInBase(req.Web); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		var p *pairCode
		var err error
		member := h.isMember()
		if member {
			// An app pairs with the center: devices are the center's (plan-hub-center.md 3).
			p = &pairCode{}
			err = h.meshCall(r.Context(), h.centerID(), "pair.code", map[string]any{}, p)
		} else {
			p, err = h.newPairCode(r.Context(), "")
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !member && source == sourceLoopback {
			if appURL := h.appPairURL(); appURL != p.URL {
				p.URL = appURL
				p.PairURL = p.pairURL()
			}
		}
		reply := pairCodeReply{pairCode: p}
		if reply.QR, err = newQRImage(p.PairURL); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if web != "" {
			reply.WebURL = web + "#token=" + url.QueryEscape(p.Token)
			if reply.WebQR, err = newQRImage(reply.WebURL); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		if fromDevice {
			h.logf("pairing code made by device %q (%s)", who.Name, who.ID)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, reply)
	})
}

// appPairURL replaces the default bare hostname for local app pairing on an IPv6 LAN.
// Use only addresses the Hub already advertises on its HTTP listener.
func (h *Hub) appPairURL() string {
	c := h.cfg
	publicURL := c.PublicURL
	if c.Public.TLSListen != "" || c.Security.PlaintextLoopbackOnly || c.Security.ProxyProtocol != nil ||
		len(c.Public.Advertise) > 0 || identity.Getenv("PERSONA_PUBLIC_URL") != "" {
		return publicURL
	}
	hostname := os.Hostname
	if h.hostname != nil {
		hostname = h.hostname
	}
	host, hostErr := hostname()
	bind, port, err := net.SplitHostPort(c.Listen)
	if err != nil || hostErr != nil || host == "" || strings.ContainsAny(host, ".:") || strings.EqualFold(host, "localhost") ||
		publicURL != "http://"+net.JoinHostPort(host, port) || h.adminContainer() {
		return publicURL
	}
	if bind != "" {
		addr, err := netip.ParseAddr(bind)
		if err != nil || !addr.Unmap().Is6() {
			return publicURL
		}
	}

	var candidate string
	var subnet netip.Prefix
	for _, endpoint := range h.endpointList() {
		if endpoint.Kind != "lan" || endpoint.Scheme != "http" {
			continue
		}
		u, err := url.Parse(endpoint.URL)
		if err != nil || u.Port() != port {
			continue
		}
		addr, err := netip.ParseAddr(u.Hostname())
		if err != nil || !addr.Is6() {
			return publicURL
		}
		// Several addresses on one /64 are normal, for example after IPv6 autoconfiguration.
		prefix := netip.PrefixFrom(addr, 64).Masked()
		if candidate == "" {
			candidate, subnet = endpoint.URL, prefix
		} else if prefix != subnet {
			return publicURL
		}
	}
	if candidate != "" {
		return candidate
	}
	return publicURL
}

// newPairCode makes a one-time pairing code: for an app (machine ""), or for that link machine's persona-link.
func (h *Hub) newPairCode(ctx context.Context, machine string) (*pairCode, error) {
	if h.store == nil {
		return nil, errors.New("no database")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, errors.New("could not generate pairing token")
	}
	p := pairCode{Token: "jp_" + hex.EncodeToString(secret[:]), ExpiresAt: time.Now().UTC().Add(pairLifetime),
		URL: h.cfg.PublicURL, HubID: h.hubID(), Alt: []string{}, Machine: machine}
	p.TLSPort, p.FP = h.tlsPin()
	if machine != "" && p.FP != "" && len(h.cfg.Public.LinkAdvertise) > 0 {
		endpoints := h.linkEndpointList()
		if len(endpoints) == 0 {
			return nil, errors.New("public.link_advertise has no valid HTTPS endpoints")
		}
		p.URL, p.TLSPort = endpoints[0].URL, endpoints[0].Port
		for _, e := range endpoints[1:min(len(endpoints), 3)] {
			p.Alt = append(p.Alt, e.URL)
		}
	} else {
		p.Alt = append(p.Alt, h.pairCandidates()...)
	}
	if err := h.store.CreatePairToken(ctx, hashToken(p.Token), p.ExpiresAt, p.Machine); err != nil {
		return nil, err
	}
	p.PairURL = p.pairURL()
	return &p, nil
}

func pairExpires(p *db.PairToken) time.Time {
	until := p.ExpiresAt
	if p.UsedAt != nil && p.UsedAt.Add(pairGrace).Before(until) {
		until = p.UsedAt.Add(pairGrace)
	}
	return until
}

// expiringContext returns a context canceled when until is reached (or immediately when already elapsed).
// A zero until returns a plain cancelable context with no deadline timer.
func expiringContext(parent context.Context, until time.Time) (context.Context, context.CancelFunc) {
	if until.IsZero() {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, until)
}

// An open WS must not keep a pairing credential alive indefinitely. Recheck redemption while it is open,
// then cancel exactly at the earlier of its original expiry and the redemption grace deadline.
func (h *Hub) pairContext(parent context.Context, p principal) (context.Context, context.CancelFunc) {
	ctx, cancel := expiringContext(parent, p.PairExpires)
	go func() {
		defer cancel()
		until := p.PairExpires
		for {
			wait := min(time.Until(until), 15*time.Second)
			if wait <= 0 {
				return
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if !time.Now().Before(until) {
				return
			}
			lookup, stop := context.WithTimeout(ctx, 5*time.Second)
			pt, err := h.store.PairTokenByHash(lookup, p.PairHash)
			stop()
			if err != nil || pt == nil {
				return
			}
			until = pairExpires(pt)
		}
	}()
	return ctx, cancel
}

// notifyPairing tells the devices a new credential was made; credential is its id (what "Revoke" revokes).
func (h *Hub) notifyPairing(name, credential string) {
	if !h.push.ready() {
		h.logf("new device paired: %q (push unavailable)", name)
		return
	}
	h.push.sendLocalized(func(l i18n.Lang) map[string]any {
		return devicePairedPayload(l, name, credential)
	}, "", nil)
}

// serveComputerCode answers pair-codes for a computer: machine (one there is) or add (one to add, or to use again).
func (h *Hub) serveComputerCode(w http.ResponseWriter, r *http.Request, who principal, fromDevice bool, machine string, add *machineAddRequest) {
	if fromDevice && h.isMember() {
		// The center's devices and computers: what a member does with a computer a device adds is not settled.
		writeErrCode(w, http.StatusConflict, "mesh_member", i18n.Err("hub-pairing.mesh_member"))
		return
	}
	if !h.linkPairable() {
		writeAddMachineErr(w, &addMachineError{status: http.StatusConflict, code: "tls_required",
			err: i18n.Err("hub-pairing.tls_required"), extra: map[string]any{"action": "enable_tls"}})
		return
	}
	if add != nil {
		if err := h.addTarget(r.Context(), strings.TrimSpace(add.Manager)); err != nil {
			writeAddMachineErr(w, err) // nothing written
			return
		}
	}
	actor := trustActor(who)
	h.machineAdd.Lock()
	id, added, err := h.computerFor(r.Context(), machine, add, fromDevice, actor)
	h.machineAdd.Unlock()
	if err != nil {
		writeAddMachineErr(w, err)
		return
	}
	p, err := h.newPairCode(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	reply := pairCodeReply{pairCode: p, Trust: db.TrustConfirmed, Added: added, Manager: h.entryOwner(id),
		Command: "persona-link pair '" + p.PairURL + "'", Install: h.computerInstall(p.PairURL)}
	if h.machinePending(id) {
		reply.Trust = trustPending
	}
	if mc := h.knownEntry(id); mc != nil {
		reply.Name = mc.Name
	}
	h.machineAudit(r.Context(), id, "code", actor, nil)
	if fromDevice {
		h.logf("[%s] pairing code for its Persona Link made by device %q (%s)", id, who.Name, who.ID)
		h.notifyComputerCode(who, id)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, reply)
}
