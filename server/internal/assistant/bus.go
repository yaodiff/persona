package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/memtrim"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The assistant on the hub's bus (docs/protocol/protocol.md "Bus"): BusHub is Hub over a bus.Client connection, and serves
// the hub's calls and events with a Service. The connection stays up while Run runs: it dials with the assistant's
// token, says manager.hello, keeps the answer (manager.updated replaces it), reports the whole snapshot, and dials
// again when it drops.

// BusOptions are where the hub is and how to reach it.
type BusOptions struct {
	// ID is the manager it connects as, the one its token is for (manager.hello); "" is the assistant the hub starts.
	ID         string
	Hub        string // the hub's base URL (http(s)://host:port)
	Token      string // the assistant's bus token (jb_…)
	HTTPClient *http.Client
	Version    string // this build's, for the hub's roster
	Deploy     string // how it was started: "child" (the hub's), DeployService, "external", "docker"
	// MachineUID is the host's local machine UID (~/.persona/machine-uid) for manager.hello; when empty and
	// HostDataDir is set, manager.hello reads <HostDataDir>/machine-uid without creating it.
	MachineUID  string
	HostDataDir string
	Backoff     time.Duration // before dialing again, doubling to a minute (zero: the protocol's 1 s; tests shorten it)
	Logf        func(format string, args ...any)
	// Devices is the manager's own connections to the computers (devices.go); nil: the hub has them (tests).
	Devices Devices
	// Extensions is the manager's extensions, connectors and bots (extensions.go); nil: none here (tests).
	Extensions Extensions
	// StopWhenRevoked: Run returns bus.ErrRevoked once the hub refuses the token (bus.Client.StopWhenRevoked);
	// StopWhenRejected: any refusal counts (bus.Client.StopWhenRejected).
	StopWhenRevoked, StopWhenRejected bool
	// Outbox keeps the pushes and the apps' live updates made while the hub is away, for when it is back
	// (outbox.go); nil: they are lost then (tests).
	Outbox OutboxStore
	// Features is the built-in modules' wire feature flags to announce in manager.hello; nil: no module features.
	Features func() []string
	// ConfigFile is the manager's manager.json: its sections go over the hub's hello (ownconfig.go); "": the
	// hello as it is (tests).
	ConfigFile string
}

// BusHub is Hub over the bus.
type BusHub struct {
	client *bus.Client
	opts   BusOptions

	mu    sync.Mutex
	hello Hello
	own   ownConfig // manager.json's sections as they last read (setHello)
	// legacy is the hub's hello-era sections (the handshake's legacy_config) not taken over yet (takeLegacy), under
	// legacyMu with legacySaid (its failure was said); one setHello takes it at a time.
	legacyMu   sync.Mutex
	legacy     json.RawMessage
	legacySaid bool
	svc        *Service
	ctx        context.Context // Serve's
	// httpH serves the hub's http.request for httpOf (httpHandler), under mu.
	httpH  http.Handler
	httpOf *Service

	// helloed is closed once the first hello's answer is kept (setHello): the hub's calls and events can come in on
	// their own goroutines before that answer is, and what they need of it (the hub's kb settings, its machines)
	// would be missing — they wait for it (waitHello).
	helloed   chan struct{}
	helloOnce sync.Once
	// keptConn is the connection (conns as onHello counted it) whose hello's answer was kept last, and keptNext is
	// closed when a later one's is (waitHelloAfter); both guarded by mu.
	keptConn uint64
	keptNext chan struct{}
	// lossWaited, when not 0, is one more than the connection after which no hello was kept within helloWait of
	// events lost on it: the line does not wait for it again (eventsLost). takeEvents' own.
	lossWaited uint64

	events  *eventQueue   // the hub's events, taken in the order they came (bus_events.go)
	conns   atomic.Uint64 // the connections whose hello was answered: the current one's number (busEvent.conn)
	pending atomic.Int64  // events received and not yet taken
	// taking, when set, sees each event as it is taken out of the line, before it is handled (tests).
	taking func(busEvent)
	// answering: the hub's requests being answered now (a tool call, an app's call): idle (idle.go) waits for them.
	answering atomic.Int64
	// pendingRestart: true while this manager has detected a replaced binary on disk and is waiting for idle before
	// restarting (carried on manager.hello and live manager.status events).
	pendingRestart atomic.Bool

	// deltas are the text a followed conversation writes (session.delta), the latest one per conversation: one waits
	// in events for each at most, so they never crowd out the others (bus_events.go).
	deltaMu sync.Mutex
	deltas  map[string]json.RawMessage

	out      *outbox // nil: none (outbox.go)
	reported *report // the current connection's report of the manager's state (outbox.go); guarded by mu
	// speakerTurnsImport is kb.speaker_turns_import read once at manager start (ownconfig.go); never updated by setHello.
	speakerTurnsImport bool
}

type busEvent struct {
	name   string
	params json.RawMessage
	conn   uint64 // the connection it came on (BusHub.conns as it was read)
	key    string // of one of which only the newest matters and that came while the line was long (eventKey); else ""
	lost   int    // not an event of the hub's: the note of this many that the full line did not keep (eventsLost)
}

// hubConnKey holds, in a ctx, the connection to the hub an event came on (busEvent.conn).
type hubConnKey struct{}

// ctx is ctx for handling ev: it says which connection ev came on (hubConnOf).
func (ev busEvent) ctx(ctx context.Context) context.Context {
	return context.WithValue(ctx, hubConnKey{}, ev.conn)
}

// hubConnOf is the connection to the hub the event ctx handles came on: 0 for one that did not come over the bus.
func hubConnOf(ctx context.Context) uint64 {
	conn, _ := ctx.Value(hubConnKey{}).(uint64)
	return conn
}

var _ Hub = (*BusHub)(nil)

// NewBusHub is the hub as the assistant reaches it over the bus; Run connects it.
func NewBusHub(o BusOptions) *BusHub {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	b := &BusHub{opts: o, events: newEventQueue(), helloed: make(chan struct{}), out: newOutbox(o.Outbox)}
	if own, err := readOwnConfig(o.ConfigFile); err == nil { // it read as the manager started (moveConfig)
		b.own = own
	}
	b.speakerTurnsImport = b.own.KB != nil && b.own.KB.SpeakerTurnsImport
	reviewEnabled := b.own.ProjectReview.Enabled
	reviewAppInputs := reviewEnabled && b.own.ProjectReview.AppInputs
	host, _ := os.Hostname()
	id := o.ID
	if id == "" {
		id = assistantwire.AssistantID
	}
	b.client = &bus.Client{
		// The manager's extensions stop waiting for a card when their caller does (extmgr's interactions.wait). A switch
		// to Persona Link that gave up on the hub's begin (its time ran out) has the hub give it up too, so it changes
		// nothing the manager no longer waits for (internal/devices link_switch.go).
		// Retirement requests also end with their caller, so renewed activity can cancel the guarded stop.
		Cancels: map[string]bool{
			assistantwire.MethodApp:          true,
			assistantwire.MethodDeviceRetire: true,
			devicewire.MethodLinkSwitchBegin: true,
		},
		Hub: o.Hub, Token: o.Token, HTTPClient: o.HTTPClient, Logf: o.Logf, Backoff: o.Backoff,
		StopWhenRevoked: o.StopWhenRevoked, StopWhenRejected: o.StopWhenRejected,
		// The hub's own manager (its child, or the built-in one's service dialing the hub's local address) is on the
		// hub's machine: compressing its messages saves no network, and costs both ends the compressor's state.
		Uncompressed: o.Deploy == "child" || o.Deploy == DeployService && id == assistantwire.AssistantID && loopbackHub(o.Hub),
		HelloMethod:  assistantwire.MethodHello,
		HelloParams: func() any {
			capabilities := []string{assistantwire.CapabilityConfigOwn, assistantwire.CapabilityAgentSessionArchives,
				assistantwire.CapabilityAgentSessionArchiveClose, assistantwire.CapabilityAgentSessionIdleArchive, assistantwire.CapabilityAgentNaming, assistantwire.CapabilityAgentAutoname, assistantwire.CapabilityMachinesOwn,
				assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingPhotos, assistantwire.CapabilityMeetingLiveView,
				assistantwire.CapabilityMeetingHighlights, assistantwire.CapabilityMeetingNotes, assistantwire.CapabilityMeetingEcho, assistantwire.CapabilityMeetingTranscription, assistantwire.CapabilityMeetingSpeakers,
				assistantwire.CapabilityTaskInput, assistantwire.CapabilityRecordingDay, assistantwire.CapabilityAssistantSearch, assistantwire.CapabilityHealthIntake, assistantwire.CapabilityHealthItems, assistantwire.CapabilityHealthStrength, assistantwire.CapabilityHealthTell, assistantwire.CapabilityAssistantSegments, assistantwire.CapabilityDirectionSessions, assistantwire.CapabilityHTTPBodyStream, assistantwire.CapabilityAssistantStartServiceTier,
				assistantwire.CapabilityMCPWait, assistantwire.CapabilitySessionsSideFold, assistantwire.CapabilityCountsView, assistantwire.CapabilityRemoteImages, assistantwire.CapabilityTodoView,
				assistantwire.CapabilityTodoRevision, assistantwire.CapabilityTodoPlaces, assistantwire.CapabilityTodoAsks, assistantwire.CapabilityTodoTimeline, assistantwire.CapabilityTodoImages, devicewire.SharingCapability, devicewire.DevelopmentPackagesCapability, devicewire.PublishedPackagesCapability,
				assistantwire.CapabilityExtensionInfoAccessScoped}
			if reviewEnabled {
				capabilities = append(capabilities, assistantwire.CapabilityProjectReview)
			}
			capabilities = append(capabilities, assistantwire.CapabilityIntegrationsProjection)
			b.mu.Lock()
			svc := b.svc
			b.mu.Unlock()
			params := map[string]any{"id": id, "host": host, "os": runtime.GOOS, "arch": runtime.GOARCH,
				"deploy": o.Deploy, "version": o.Version, "capabilities": withPages(withHealth(withRecording(capabilities, svc), svc), id, svc), "project_review_app_inputs": reviewAppInputs}
			if uid := b.machineUID(); uid != "" {
				params["machine_uid"] = uid
			}
			if b.pendingRestart.Load() {
				params["pending_restart"] = true
			}
			if o.Features != nil {
				if features := o.Features(); len(features) > 0 {
					params["features"] = features
				}
			}
			return params
		},
		OnHello: b.onHello,
		// A node's tool call that its caller stopped waiting for ends at the hub too (fleet_tools.go).
		OnRequest: b.answer,
		OnEvent: func(event string, params json.RawMessage) {
			// Credential revocation goes to both managers and can write to their stores.
			if event == bus.EventCredentialRevoked {
				b.queueEvent(event, params)
				return
			}
			if o.Devices != nil && o.Devices.Event(event, params) {
				return // the computers' (devices.go), in the order they came
			}
			if o.Extensions != nil && o.Extensions.Event(event, params) {
				return // the extensions' (extensions.go)
			}
			b.queueEvent(event, params)
		},
	}
	return b
}

// loopbackHub says the hub's URL is this machine's loopback address.
func loopbackHub(hub string) bool {
	u, err := url.Parse(hub)
	if err != nil {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// Run keeps the connection to the hub up and serves it with svc, and runs svc's own work (its schedule) from when it
// first connected (it needs the hub's hello: where it runs, the user's time zone), until ctx ends.
func (b *BusHub) Run(ctx context.Context, svc *Service) error {
	go func() {
		if b.client.Wait(ctx) == nil {
			svc.Run(ctx)
		}
	}()
	return b.Serve(ctx, svc)
}

// Serve is Run without svc's own work: the connection alone. It lets svc's knowledge base go for good when it ends.
func (b *BusHub) Serve(ctx context.Context, svc *Service) error {
	if svc.devs != nil {
		b.opts.Devices = svc.devs
	}
	if b.speakerTurnsImport {
		svc.speakerTurnsImport.Store(true)
	}
	if config := b.own.ProjectReview; config.Enabled {
		store, ok := b.opts.Outbox.(*db.DB)
		if !ok {
			return errors.New("project_review requires the manager's Postgres store")
		}
		svc.projectReview = &projectReview{projectReviewConfig: config, db: store, wake: make(chan struct{}, 1)}
	}
	b.mu.Lock()
	b.svc, b.ctx = svc, ctx
	b.mu.Unlock()
	defer svc.kbEnd()
	defer svc.larkWait(2 * time.Second) // the Feishu agent messages' workers end with the connection's last calls
	go b.takeEvents(ctx)
	go b.runOutbox(ctx) // what was made while the hub was away, once it is back (outbox.go)
	return b.client.Run(ctx)
}

// Connected: the connection to the hub is up now (the knowledge base closes while it is not, kb.go).
func (b *BusHub) Connected() bool { return b.client.Peer() != nil }

// machineUID returns the host's local machine UID for manager.hello ("" when unavailable).
func (b *BusHub) machineUID() string {
	if uid := strings.TrimSpace(b.opts.MachineUID); uid != "" {
		return uid
	}
	if b.opts.HostDataDir != "" {
		return identity.ReadMachineUID(b.opts.HostDataDir)
	}
	return ""
}

// SetPendingRestart records whether this manager is waiting for idle to restart onto a replaced binary and notifies
// the hub immediately when the value changes. The same flag is also sent on manager.hello across reconnects.
func (b *BusHub) SetPendingRestart(pending bool) {
	if b.pendingRestart.Swap(pending) == pending {
		return
	}
	_ = b.client.Notify(assistantwire.EventManagerStatus, assistantwire.ManagerStatus{PendingRestart: pending})
}

// CredentialRevoked checks the hub's credential status for a kept message's sender.
func (b *BusHub) CredentialRevoked(ctx context.Context, id string) (bool, error) {
	return bus.CredentialRevoked(ctx, b.client.Call, id)
}

// onHello keeps the hub's answer to manager.hello. A new connection: the hub shows nothing of the assistant until it
// reports the whole of it again.
func (b *BusHub) onHello(res json.RawMessage) error {
	conn := b.conns.Add(1) // what the hub's events say of the hub is that connection's (busEvent.conn)
	var h Hello
	if err := json.Unmarshal(res, &h); err != nil {
		return err
	}
	if own, err := readOwnConfig(b.opts.ConfigFile); err == nil {
		if err := own.validatePlacement(h); err != nil {
			// Existing node connections survive a failed hub handshake. Keep its new
			// restrictions before rejecting placement, so they cannot serve stale access.
			b.setHello(h)
			b.helloKept(conn)
			return err
		}
	}
	h = b.setHello(h)
	b.helloKept(conn)
	b.mu.Lock()
	svc, ctx := b.svc, b.ctx
	b.mu.Unlock()
	// The outbox waits until what follows has reported the manager's state on this connection (its extensions, its
	// cards): a card's push needs the card, an extension's live update the extension (outbox.go).
	reported := b.newReport()
	if e := b.opts.Extensions; e != nil && ctx != nil {
		mine, err := json.Marshal(h) // with manager.json's sections over the hub's
		if err != nil {
			return err
		}
		e.Hello(mine)
		reported.Add(1)
		go func() {
			defer reported.Done()
			if b.client.Wait(ctx) == nil {
				e.BusUp()
			}
		}()
	}
	if d := b.opts.Devices; d != nil && ctx != nil {
		d.Hello(res)
		if svc != nil {
			svc.forgetPendingRead() // the hand-over is in: read again for the connections BusUp sets up (machine_trust.go)
		}
		reported.Add(1)
		go func() { // once the connection counts as up (after this returns)
			defer reported.Done()
			if b.client.Wait(ctx) == nil {
				d.BusUp()
				if svc != nil {
					svc.FleetUp(ctx) // the interactions, the lists and the fleet (fleet_events.go)
				}
			}
		}()
	}
	reported.seal()
	if b.pendingRestart.Load() && ctx != nil {
		go func() {
			if b.client.Wait(ctx) == nil {
				_ = b.client.Notify(assistantwire.EventManagerStatus, assistantwire.ManagerStatus{PendingRestart: b.pendingRestart.Load()})
			}
		}()
	}
	if svc != nil && h.Mutes != nil {
		svc.setMutes(*h.Mutes) // the hub's, before anything is pushed (fleet_mutes.go)
	}
	if svc != nil {
		svc.workReconnected() // turns that ended while it was away are looked for at the next tick (agents_work.go)
		// The whole snapshot goes once the connection counts as up (after this returns): before that it is offline,
		// and the hub would show nothing of the assistant until its next change.
		go func() {
			if b.client.Wait(ctx) == nil {
				svc.Resync(context.WithoutCancel(ctx))
			}
		}()
		// The modules' handshake hooks (kernel/seam: the Inbox's "Needs you" count, which the hub keeps of this
		// connection only), once the connection counts as up.
		go func() {
			if b.client.Wait(ctx) == nil {
				svc.kernel.Connected(context.WithoutCancel(ctx))
			}
		}()
		// The conversations the channels follow (channels_handler.go).
		go svc.channelsReconnected(context.WithoutCancel(ctx))
		// Turns the hub sent as it connected can be taken before this answer is kept (events come on their own
		// goroutine): without the hub's kb settings they could not be filed and wait — now they can.
		go svc.kbFileWaiting(context.WithoutCancel(ctx))
	}
	return nil
}

// Wait returns once the connection is up and its handshake went through.
func (b *BusHub) Wait(ctx context.Context) error { return b.client.Wait(ctx) }

// Client is the bus connection (for the devices: their calls and events to the hub).
func (b *BusHub) Client() *bus.Client { return b.client }

// setHello keeps the hub's hello with the manager's own sections over it (manager.json, read again each time: a
// hand edit is taken up at the next hello) and returns what it kept. A manager.json that no longer reads keeps the
// sections it last read (the manager started with them), not the hello's bare defaults.
func (b *BusHub) setHello(h Hello) Hello {
	b.legacyMu.Lock()
	if len(h.LegacyConfig) > 0 && b.opts.ConfigFile != "" {
		b.legacy, b.legacySaid = h.LegacyConfig, false // kept until it is taken over (manager.updated does not carry it)
	}
	h.LegacyConfig = nil
	if b.legacy != nil {
		if err := b.takeLegacy(b.legacy); err != nil {
			if !b.legacySaid {
				b.opts.Logf("configuration: could not take the hub's sections over into %s: %v (gone by as they came "+
					"until they are)", b.opts.ConfigFile, err)
				b.legacySaid = true
			}
			var lo ownConfig
			if json.Unmarshal(b.legacy, &lo) == nil {
				h = lo.over(h) // under manager.json's, which a manager that did not start from them lacks
			}
		} else {
			b.legacy = nil
		}
	}
	b.legacyMu.Unlock()
	own, err := readOwnConfig(b.opts.ConfigFile)
	if err == nil {
		err = own.validatePlacement(h)
	}
	b.mu.Lock()
	if err != nil {
		own = b.own
	} else {
		b.own = own
	}
	b.mu.Unlock()
	if err != nil {
		b.opts.Logf("assistant: %s: %v (the sections it last read are kept until it is fixed)", b.opts.ConfigFile, err)
	}
	h = own.over(h)
	b.mu.Lock()
	b.hello = h
	b.mu.Unlock()
	b.helloOnce.Do(func() { close(b.helloed) })
	return h
}

// takeLegacy takes the hub's hello-era sections (the handshake's legacy_config) over into manager.json, once each
// (managerconf.MoveFrom), and says what it did.
func (b *BusHub) takeLegacy(legacy json.RawMessage) error {
	file := b.opts.ConfigFile
	rep, err := managerconf.MoveFrom(legacy, file)
	if err != nil {
		return err
	}
	if len(rep.Moved) > 0 {
		b.opts.Logf("configuration: took %s over from the hub's hub.json into %s (once; as the hub had them: %s)",
			strings.Join(rep.Moved, ", "), file, rep.Backup)
	}
	if len(rep.Ignored) > 0 {
		b.opts.Logf("configuration: %s: the hub's hub.json has another value than %s, which is the one read (once)",
			strings.Join(rep.Ignored, ", "), file)
	}
	return nil
}

// helloWait bounds how long a call or event of the hub's waits for the first hello's answer to be kept.
var helloWait = 10 * time.Second

// waitHello returns once the first hello's answer is kept (at once after that), or after helloWait or ctx.
func (b *BusHub) waitHello(ctx context.Context) {
	select {
	case <-b.helloed:
		return
	default:
	}
	t := time.NewTimer(helloWait)
	defer t.Stop()
	select {
	case <-b.helloed:
	case <-t.C:
	case <-ctx.Done():
	}
}

// helloKept says connection conn's hello answer is kept: what waited for one later than another's goes on.
func (b *BusHub) helloKept(conn uint64) {
	b.mu.Lock()
	b.keptConn = conn
	next := b.keptNext
	b.keptNext = nil
	b.mu.Unlock()
	if next != nil {
		close(next)
	}
}

// waitHelloAfter returns true once the hello's answer of a connection made after conn is kept (at once when one
// is), false after helloWait or ctx.
func (b *BusHub) waitHelloAfter(ctx context.Context, conn uint64) bool {
	var t *time.Timer
	for {
		b.mu.Lock()
		if b.keptConn > conn {
			b.mu.Unlock()
			return true
		}
		if b.keptNext == nil {
			b.keptNext = make(chan struct{})
		}
		next := b.keptNext
		b.mu.Unlock()
		if t == nil {
			t = time.NewTimer(helloWait)
			defer t.Stop()
		}
		select {
		case <-next:
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// ---- what the assistant asks of the hub ----

func (b *BusHub) Hello(context.Context) (Hello, error) { return b.helloNow(), nil }

// helloNow is the hello's answer as last kept.
func (b *BusHub) helloNow() Hello {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hello
}

func (b *BusHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if out, ok, err := b.localDeviceCall(ctx, p); ok { // the manager's own machines (devices.go)
		return out, err
	}
	method := assistantwire.DeviceMethod
	if p.Method == nodeproto.StopIfIdle {
		method = assistantwire.DeviceRetireMethod
	}
	out, err := buswire.Call(ctx, b.client, method, p)
	return out, busError(err)
}

func (b *BusHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if d, ok := ctx.Deadline(); ok { // the hub stops when the caller stops waiting (a model call it would go on with)
		p.TimeoutMS = max(time.Until(d).Milliseconds(), 1)
	}
	if out, ok, err := b.localAppCall(ctx, p); ok { // the manager's extensions, connectors and bots (extensions.go)
		return out, err
	}
	out, err := buswire.Call(ctx, b.client, assistantwire.AppMethod, p)
	return out, busError(err)
}

func (b *BusHub) NotifyRequest(ctx context.Context, p NotifyRequest) (NotifyResult, error) {
	return b.sendPush(ctx, p)
}

func (b *BusHub) Tell(event string, params any) error { return b.client.Notify(event, params) }

func (b *BusHub) Broadcast(ctx context.Context, m Broadcast) {
	if err := b.sendBroadcast(ctx, m); err != nil {
		b.opts.Logf("assistant: %s: %v", assistantwire.EventBroadcast, err)
	}
}

func (b *BusHub) Snapshot(ctx context.Context, s Snapshot) (SnapshotResult, error) {
	b.keepFromSnapshot(s) // what the nodes keep listing is the manager's to tell them (devices.go)
	out, err := buswire.Call(ctx, b.client, assistantwire.SnapshotMethod, s)
	return out, busError(err)
}

// codedError is an error that came over the bus with a code: its message as the hub wrote it, its kind as the
// assistant's code checks it (errors.Is).
type codedError struct {
	msg  string
	code string // the hub's (assistantwire.Code…)
	kind error
}

func (e *codedError) Error() string { return e.msg }
func (e *codedError) Unwrap() error { return e.kind }

// busError gives the hub's coded errors back their kind: a machine the hub does not have, one that is offline, a
// node link that dropped, a stale snapshot. Other errors (a node's own coded refusal among
// them) stay *rpc.Error, as they were on the hub.
func busError(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return err
	}
	var kind error
	switch re.Code {
	case assistantwire.CodeNoDevice:
		kind = ErrNoDevice
	case assistantwire.CodeMachineOffline:
		kind = apierr.ErrOffline
	case assistantwire.CodeNodeClosed:
		kind = rpc.ErrClosed
	case assistantwire.CodeStaleSnapshot:
		kind = ErrStaleSnapshot
	default:
		return err
	}
	return &codedError{msg: re.Message, code: re.Code, kind: kind}
}

// answer is request with the hub's coded errors given back their code: an answer the assistant could not give because
// of the hub (a machine that is offline, a node link that dropped) reaches the app as the hub's own error would.
func (b *BusHub) answer(ctx context.Context, method string, params json.RawMessage) (any, error) {
	b.answering.Add(1)
	defer b.answering.Add(-1)
	out, err := b.request(ctx, method, params)
	var ce *codedError
	if errors.As(err, &ce) {
		return nil, &rpc.Error{Code: ce.code, Message: ce.msg}
	}
	return out, err
}

// ---- what the hub asks of the assistant ----

func (b *BusHub) service() *Service {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.svc
}

// httpHandler serves the hub's http.request for s: s's routes, with the machines' and the nodes' routes that are the
// manager's in front (devices.go, fleet_routes.go). Built once per service, not per request: the two ServeMuxes and
// their twenty routes were 0.1 ms and 33 KB of every relayed request. Their handlers read everything when they run.
func (b *BusHub) httpHandler(s *Service) http.Handler {
	d := b.opts.Devices
	if d == nil {
		return s
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.httpOf != s || b.httpH == nil {
		b.httpH, b.httpOf = s.fleetHandler(d.Handler(s)), s
	}
	return b.httpH
}

// handleBusRequest binds a handler to its descriptor and uses the bus's existing decoder.
func handleBusRequest[P, R any](ctx context.Context, _ buswire.Method[P, R], decode func(any) error, run func(context.Context, P) (R, error)) (any, error) {
	var p P
	if err := decode(&p); err != nil {
		return nil, err
	}
	return run(ctx, p)
}

func (b *BusHub) request(ctx context.Context, method string, params json.RawMessage) (reply any, requestErr error) {
	b.waitHello(ctx)
	s := b.service()
	decode := func(v any) error {
		if err := json.Unmarshal(params, v); err != nil {
			return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + err.Error()}
		}
		return nil
	}
	switch method {
	case assistantwire.MethodHTTP:
		return handleBusRequest(ctx, assistantwire.HTTPMethod, decode, func(ctx context.Context, p assistantwire.HTTPRequest) (assistantwire.HTTPResponse, error) {
			defer memtrim.AfterLarge(len(p.Body)) // a large upload's buffers go back to the system after it
			return serveHTTP(ctx, b.httpHandler(s), p, s.lang)
		})
	case assistantwire.MethodHTTPStreamed: // http.request with its body on a relay of its own (bus_body.go)
		return handleBusRequest(ctx, assistantwire.HTTPStreamedMethod, decode, func(ctx context.Context, p assistantwire.HTTPRequest) (assistantwire.HTTPResponse, error) {
			body, err := b.streamedBody(ctx, p.BodyStream)
			if err != nil {
				return assistantwire.HTTPResponse{}, &rpc.Error{Code: assistantwire.CodeBodyIncomplete, Message: method + ": " + err.Error()}
			}
			p.Body, p.BodyStream = body, nil
			defer memtrim.AfterLarge(len(p.Body))
			return serveHTTP(ctx, b.httpHandler(s), p, s.lang)
		})
	case assistantwire.MethodTools:
		return handleBusRequest(ctx, assistantwire.ToolsMethod, decode, func(ctx context.Context, target assistantwire.SessionRef) (assistantwire.ToolsResult, error) {
			return assistantwire.ToolsResult{Tools: s.ToolsFor(ctx, target.MachineID, target.SessionID)}, nil
		})
	case assistantwire.MethodTool:
		return handleBusRequest(ctx, assistantwire.ToolMethod, decode, func(ctx context.Context, p assistantwire.ToolCall) (proto.ToolResult, error) {
			return s.Tool(ctx, p), nil
		})
	case assistantwire.MethodContext:
		return handleBusRequest(ctx, assistantwire.ContextMethod, decode, func(ctx context.Context, p assistantwire.ContextParams) (assistantwire.ContextResult, error) {
			return s.Context(ctx, p), nil
		})
	case assistantwire.MethodAction:
		return handleBusRequest(ctx, assistantwire.ActionMethod, decode, s.Action)
	case assistantwire.MethodTodoHandoff:
		return handleBusRequest(ctx, assistantwire.TodoHandoffMethod, decode, s.TodoHandoff)
	case assistantwire.MethodTodoLink:
		return handleBusRequest(ctx, assistantwire.TodoLinkMethod, decode, s.TodoLink)
	case assistantwire.MethodStart:
		return handleBusRequest(ctx, assistantwire.StartMethod, decode, func(ctx context.Context, p assistantwire.StartParams) (assistantwire.StartResult, error) {
			sess, err := s.Start(ctx, p)
			return assistantwire.StartResult{Session: sess}, err
		})
	case assistantwire.MethodProjectReviewOwnerInput:
		return handleBusRequest(ctx, assistantwire.ProjectReviewOwnerInputMethod, decode, func(ctx context.Context, p assistantwire.ProjectReviewOwnerInput) (buswire.None, error) {
			return buswire.None{}, s.projectReviewOwnerInput(ctx, p)
		})
	case assistantwire.MethodSendParams:
		var p SendParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		if s.keepsBehind(ctx, p) { // older owner messages to it are kept: this one goes after them (undelivered.go)
			return nil, &rpc.Error{Code: assistantwire.CodeUndeliveredAhead, Message: "older messages to this conversation wait to go in"}
		}
		ctx, note := withLauncherNote(ctx)
		// The owner's message, which the hub sends and reports: its parameters may move a direction's notes block
		// (direction_notes.go), settled by the hub's notice, or now when they are refused and nothing is sent. A
		// message they were already given for is the hub's retry, which moves nothing: it is tied to its first attempt
		// while that one waits for its notice (notesOwnerCarry).
		msg, _ := p.Params["client_msg_id"].(string)
		ctx, carry := s.notesOwnerCarry(ctx, p.SessionID, msg)
		params, err := s.SendParams(ctx, p)
		if err != nil {
			s.notesOwnerRefused(ctx, carry)
			s.appSendRefused(p.MachineID, p.SessionID, err) // the hub sends nothing: no ConversationSent (north_star.go)
			return nil, err
		}
		if target, _ := params[ownerApprovalHandledParam].(string); target != "" {
			s.notesOwnerRefused(ctx, carry) // an approval is not a model turn or a conversation message
			s.forgetUnusedOwnerRelayLater(ctx, p.SessionID, msg)
			return map[string]any{"params": params}, nil
		}
		if _, err := s.withOwnerRelayLater(ctx, p.MachineID, p.SessionID, msg, params, true); err != nil {
			s.notesOwnerRefused(ctx, carry)
			s.appSendRefused(p.MachineID, p.SessionID, err)
			return nil, err
		}
		s.notesOwnerSent(ctx, carry, msg)
		if *note != "" {
			return map[string]any{"params": params, "launcher_note": *note}, nil
		}
		return map[string]any{"params": params}, nil
	case assistantwire.MethodKeepUndelivered:
		return handleBusRequest(ctx, assistantwire.KeepUndeliveredMethod, decode, func(ctx context.Context, p assistantwire.KeepUndeliveredParams) (assistantwire.KeepUndeliveredResult, error) {
			kept, err := s.KeepUndelivered(ctx, p)
			return assistantwire.KeepUndeliveredResult{Undelivered: &kept}, err
		})
	case assistantwire.MethodRename:
		var p RenameParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.Rename(ctx, p)
	case assistantwire.MethodAgentStartParams, assistantwire.MethodAgentResumeParams, assistantwire.MethodAgentTabName:
		return b.agentRequest(ctx, s, method, decode) // agents_bus.go
	// The channels' (channels.go, feishu.go).
	case assistantwire.MethodChannelUnbound:
		var p ChannelUnbound
		if err := decode(&p); err != nil {
			return nil, err
		}
		return map[string]any{}, s.ChannelUnbound(ctx, p)
	case assistantwire.MethodChannelReceive:
		var p ChannelReceive
		if err := decode(&p); err != nil {
			return nil, err
		}
		return map[string]any{}, s.ChannelReceive(ctx, p)
	case assistantwire.MethodFeishuMessage:
		var p FeishuMessage
		if err := decode(&p); err != nil {
			return nil, err
		}
		return map[string]any{}, s.FeishuMessage(ctx, p)
	case assistantwire.MethodFeishuAction:
		var p FeishuAction
		if err := decode(&p); err != nil {
			return nil, err
		}
		return s.FeishuAction(ctx, p)
	case assistantwire.MethodFeishuHandoff:
		var p assistantwire.FeishuHandoff
		if err := decode(&p); err != nil {
			return nil, err
		}
		return map[string]any{}, s.larkModule().Handoff(ctx, p)
	case assistantwire.MethodFeishuGuest:
		var p assistantwire.FeishuGuestTurn
		if err := decode(&p); err != nil {
			return nil, err
		}
		return map[string]any{}, s.larkModule().Guest(ctx, p)
	case assistantwire.MethodFeishuView:
		return s.FeishuView(ctx), nil
	case assistantwire.MethodFleetAsk:
		return handleBusRequest(ctx, assistantwire.FleetAskMethod, decode, func(ctx context.Context, p assistantwire.FleetAskParams) (proto.Resolution, error) {
			return s.askHub(ctx, p.Interaction)
		})
	case assistantwire.MethodFleetAskPrepared:
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
			Replacement string            `json:"replacement"`
		}
		if err := decode(&p); err != nil {
			return nil, err
		}
		return b.askPreparedHub(ctx, p.Interaction, p.Replacement)
	}
	if out, ok, err := s.kbRequest(ctx, method, params); ok { // the knowledge base's (kb_bus.go)
		return out, err
	}
	if out, ok, err := s.conversationsRequest(ctx, method, params); ok { // extensions' assistant.read (conversations_rpc.go)
		return out, err
	}
	if out, ok, err := s.extensionEventRequest(ctx, method, params); ok { // extensions' assistant.event (extension_event.go)
		return out, err
	}
	if out, ok, err := s.kernel.Answer(ctx, method, params); ok { // the modules' (kernel/seam: the Inbox's)
		return out, err
	}
	if e := b.opts.Extensions; e != nil {
		if out, ok, err := e.Request(ctx, method, params); ok { // extensions.* (extensions.go)
			if method == extwire.MethodAuthorize && err == nil {
				if a, ok := out.(extwire.Authorized); ok && a.Extension != "" {
					a.Owner = extwire.OwnerThirdParty
					if s.extensionIsOwner(ctx, a.Extension) {
						a.Owner = extwire.OwnerConfirmed
					}
					return a, nil
				}
			}
			return out, err
		}
	}
	if out, ok, err := s.modelsRequest(ctx, method, params); ok { // the hub's plugins' (models_hub.go)
		return out, err
	}
	if out, ok, err := s.transcriptionRequest(ctx, method, params); ok { // extensions' transcription (extension_transcribe.go)
		return out, err
	}
	if out, ok, err := s.meetingChoiceRequest(ctx, method, params); ok { // the Volcengine extension's switch (meeting_transcription_choice.go)
		return out, err
	}
	if d := b.opts.Devices; d != nil {
		if method == devicewire.MethodCall {
			var p devicewire.NodeCall
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, err
			}
			if conversationWrite(p.Method) {
				s.conversationWrites.RLock()
				defer s.conversationWrites.RUnlock()
			}
			if p.Method != "session.unwatch" {
				if err := s.conversationParamsAvailable(ctx, d.Canonical(p.MachineID), p.Params); err != nil {
					return nil, err
				}
			}
			if devicewire.IsSessionStart(p.Method) {
				finish := s.beginPlacementBind(ctx, d.Canonical(p.MachineID), p.Params)
				defer func() { raw, _ := reply.(json.RawMessage); finish(raw) }()
			}
			if out, handled, err := s.logicalCall(context.WithValue(ctx, logicalTransportKey{}, p), d.Canonical(p.MachineID), p.Method, p.Params); handled {
				return out, err
			}
			if out, handled, err := s.historyCall(ctx, d.Canonical(p.MachineID), p.Method, p.Params); handled {
				return out, err
			}
		}
		if out, ok, err := d.Request(ctx, method, params); ok { // devices.* (devices.go)
			if err == nil && method == devicewire.MethodCall {
				var p devicewire.NodeCall
				_ = json.Unmarshal(params, &p)
				if p.Method != "session.unwatch" {
					if err := s.conversationParamsAvailable(ctx, d.Canonical(p.MachineID), p.Params); err != nil {
						return nil, err
					}
				}
			}
			if err == nil && method == devicewire.MethodCall {
				s.nodeCallDone(ctx, params) // an effort set on a conversation with a model pick (model_pick.go)
			}
			return out, err
		}
	}
	return nil, &rpc.Error{Code: bus.CodeUnknownMethod, Message: "unknown method " + method}
}

// serveHTTP answers an app's request the hub relayed (http.request) with s's routes. An error that is an i18n.Message
// reads in the request's language (its Accept-Language, relayed as it came), else in fallback's (the user's).
func serveHTTP(ctx context.Context, s http.Handler, p assistantwire.HTTPRequest, fallback func(context.Context) i18n.Lang) (assistantwire.HTTPResponse, error) {
	target := p.Path
	if p.Query != "" {
		target += "?" + p.Query
	}
	if p.Caller != nil {
		ctx = devicewire.WithCaller(ctx, *p.Caller)
	}
	if p.ShareAuthority != nil {
		ctx = devicewire.WithShareAuthority(ctx, *p.ShareAuthority)
	}
	if p.ModeActor != nil {
		ctx = devicewire.WithModeActor(ctx, p.ModeActor)
	}
	if p.OwnerAnswer {
		ctx = devicewire.WithOwnerAnswer(ctx)
	}
	if p.ClarificationProof != "" {
		ctx = assistantwire.WithClarificationProof(ctx, p.ClarificationProof)
	}
	if p.HandoffApprover != nil { // set by the hub only, never from a header (secret_handoff.go there)
		ctx = assistantwire.WithHandoffApprover(ctx, *p.HandoffApprover)
	}
	if p.Human { // the same: a new conversation a person's own app opens (start.go's route)
		ctx = assistantwire.WithHumanStart(ctx)
	}
	if p.ReplyInput {
		ctx = assistantwire.WithReplyInput(ctx)
	}
	r, err := http.NewRequestWithContext(ctx, p.Method, target, bytes.NewReader(p.Body))
	if err != nil {
		return assistantwire.HTTPResponse{}, &rpc.Error{Code: bus.CodeInvalidParams, Message: err.Error()}
	}
	r.Header = http.Header(p.Header)
	if r.Header == nil {
		r.Header = http.Header{}
	}
	r.ContentLength = int64(len(p.Body))
	if len(p.Body) > 0 { // in memory already: the large routes decode it where it is (decodeLargeBody)
		r.Body = newRelayedBody(p.Body)
	}
	r = withRequestLang(r) // what the routes write for people reads in it too (i18n.From)
	w := &recorder{header: http.Header{}, lang: func() i18n.Lang {
		if l := i18n.FromRequest(r); l != "" {
			return l
		}
		if fallback != nil {
			return fallback(ctx)
		}
		return ""
	}}
	s.ServeHTTP(w, r)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return assistantwire.HTTPResponse{ShareAuthority: w.shareAuthority, Status: w.status, Header: w.header, Body: w.body.Bytes()}, nil
}

// recorder keeps a response to send back whole.
type recorder struct {
	shareAuthority *devicewire.ShareAuthority
	header         http.Header
	status         int
	body           bytes.Buffer
	lang           func() i18n.Lang // the request's language, read only when an error is written (i18n.Carrier)
}

// Lang is the language apierr writes an i18n.Message in.
func (w *recorder) Lang() i18n.Lang {
	if w.lang == nil {
		return ""
	}
	return w.lang()
}

func (w *recorder) SetShareAuthority(a devicewire.ShareAuthority) { w.shareAuthority = &a }
func (w *recorder) Header() http.Header                           { return w.header }

// Grow makes room for n more bytes of body: a route that knows its answer's size and writes it in pieces
// (writeInboxItems) then fills one buffer instead of growing it step by step.
func (w *recorder) Grow(n int) { w.body.Grow(n) }

func (w *recorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}
func (w *recorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

// Settle returns once the events received so far are taken (a turn's end may still be running: TurnEnded is its
// own). Tests call it after a round trip to the hub, which comes after the events it sent before.
func (b *BusHub) Settle() {
	for b.pending.Load() > 0 {
		time.Sleep(5 * time.Millisecond)
	}
}

// takeEvents takes the hub's events one at a time, in order, until ctx ends: nothing else waits on the line, so
// nothing is left waiting then. A turn's end runs on its own: naming a conversation asks a model and a task waits
// for its agents to settle, and the events after it must not wait for that.
func (b *BusHub) takeEvents(ctx context.Context) {
	for ctx.Err() == nil {
		ev, ok := b.events.take()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-b.events.wake:
			}
			continue
		}
		if f := b.taking; f != nil {
			f(ev)
		}
		b.waitHello(ctx)
		if ev.lost > 0 {
			b.eventsLost(ctx, ev)
			b.pending.Add(-1)
			continue
		}
		if ev.name == assistantwire.EventSessionDelta {
			ev.params = b.takeDelta(ev.params)
		}
		if err := b.event(ctx, ev); err != nil {
			b.opts.Logf("assistant: %s: %v", ev.name, err)
		}
		b.pending.Add(-1)
	}
}

func (b *BusHub) event(ctx context.Context, ev busEvent) error {
	s := b.service()
	decode := func(v any) error { return json.Unmarshal(ev.params, v) }
	switch ev.name {
	case bus.EventCredentialRevoked:
		var p bus.DeviceCredential
		if err := decode(&p); err != nil {
			return err
		}
		if p.ID != "" {
			s.undeliveredCredentialRevoked(ctx, p.ID)
			if b.opts.Devices != nil {
				b.opts.Devices.Event(ev.name, ev.params)
			}
		}
	case assistantwire.EventUserSpoke:
		var p assistantwire.UserSpokeParams
		if err := decode(&p); err != nil {
			return err
		}
		if p.Source != "" {
			s.noteOwnerInstruction(ctx, p.SessionRef, p.Text, p.Source, p.ClientMsgID.String(), p.At)
		}
		s.userSpokeMessage(ctx, p.SessionRef, p.Text, p.ClientMsgID.String())
	case assistantwire.EventUserStopped:
		var p SessionRef
		if err := decode(&p); err != nil {
			return err
		}
		go s.UserStopped(context.WithoutCancel(ctx), p)
	case assistantwire.EventTurnStarted:
		var p SessionRef
		if err := decode(&p); err != nil {
			return err
		}
		s.TurnStarted(ctx, p)
	case assistantwire.EventTurnEnded:
		var p SessionRef
		if err := decode(&p); err != nil {
			return err
		}
		go s.TurnEnded(ctx, p)
	case assistantwire.EventSessionsChanged:
		var p DeviceSessions
		if err := decode(&p); err != nil {
			return err
		}
		s.SessionsChanged(ctx, p)
	case assistantwire.EventStartEnded:
		var p StartEnded
		if err := decode(&p); err != nil {
			return err
		}
		go s.StartEnded(context.WithoutCancel(ctx), p)
	case assistantwire.EventAgentTabNameStarted:
		var p assistantwire.AgentTabNameStart
		if err := decode(&p); err != nil {
			return err
		}
		s.nameAgentTabLater(p)
	case assistantwire.EventConversationSent:
		var p ConversationSent
		if err := decode(&p); err != nil {
			return err
		}
		go s.ConversationSent(ev.ctx(context.WithoutCancel(ctx)), p) // with the connection it came on (direction_notes.go)
	case assistantwire.EventInteractionResolved:
		var p InteractionResolved
		if err := decode(&p); err != nil {
			return err
		}
		s.InteractionResolved(ctx, p)
	case assistantwire.EventInteractionRaised:
		var p InteractionRaised
		if err := decode(&p); err != nil {
			return err
		}
		s.InteractionRaised(ctx, p)
	case assistantwire.EventUpdated:
		var h Hello
		if err := decode(&h); err != nil {
			return err
		}
		before := b.helloNow().Assistant.Machines
		h = b.setHello(h)
		if e := b.opts.Extensions; e != nil { // the extensions go by the same sections (kb off, the owner's name)
			if mine, err := json.Marshal(h); err == nil {
				e.Hello(mine)
			}
		}
		// A computer just confirmed gets its keys (machine_trust.go).
		s.trustChanged(ctx, before, h.Assistant.Machines)
		s.ProfileChanged(ctx) // the live context and the settings are read again
		s.Resync(ctx)         // and the hub gets the profile in effect now (the default machine may be another)
	case assistantwire.EventResync:
		go s.Resync(ctx)
	case assistantwire.EventSessionSeen:
		var p SessionSeen
		if err := decode(&p); err != nil {
			return err
		}
		s.SessionSeen(ctx, p)
	case assistantwire.EventModelUsed: // a typed model id ran (models_custom.go); may ask the node for its list
		var p assistantwire.ModelUsed
		if err := decode(&p); err != nil {
			return err
		}
		go s.ModelUsed(context.WithoutCancel(ctx), p)
	case assistantwire.EventTodoSpaceRenamed:
		var p TodoSpaceRenamed
		if err := decode(&p); err != nil {
			return err
		}
		s.TodoSpaceRenamed(ctx, p)
	// The channels' (channels.go, feishu.go).
	case assistantwire.EventInteractionAnnounced:
		var p InteractionAnnounced
		if err := decode(&p); err != nil {
			return err
		}
		s.InteractionAnnounced(ctx, p)
	case assistantwire.EventInteractionEnded:
		var p InteractionEnded
		if err := decode(&p); err != nil {
			return err
		}
		s.InteractionEnded(ctx, p)
	case assistantwire.EventSessionItems: // an item the hub read whole for an app (the manager follows the rest itself)
		var p SessionItems
		if err := decode(&p); err != nil {
			return err
		}
		s.followedItems(p.MachineID, ev.params)
	case assistantwire.EventInlineViewPage:
		var p SessionItems
		if err := decode(&p); err != nil {
			return err
		}
		go s.repairInlineViewPage(context.WithoutCancel(ctx), p)
	case assistantwire.EventSessionDelta:
		var p SessionDelta
		if err := decode(&p); err != nil {
			return err
		}
		s.SessionDelta(ctx, p)
	case assistantwire.EventFeishuOwner:
		var p FeishuOwner
		if err := decode(&p); err != nil {
			return err
		}
		s.FeishuOwner(ctx, p)
	case assistantwire.EventFeishuRecalled:
		var p FeishuRecalled
		if err := decode(&p); err != nil {
			return err
		}
		s.FeishuRecalled(p)
	case assistantwire.EventChannelNoted:
		var p ChannelNoted
		if err := decode(&p); err != nil {
			return err
		}
		s.ChannelNoted(ctx, p)
	case "managers": // the bus's roster: nothing the assistant needs
	default:
		if s.FleetEvent(ctx, ev.name, ev.params) { // the hub's fleet.* (fleet_events.go)
			return nil
		}
		if ok, err := s.kbEvent(ctx, ev.name, ev.params); ok { // the knowledge base's (kb_bus.go)
			return err
		}
		return fmt.Errorf("unknown event")
	}
	return nil
}

// HubBuild is what the running hub says it is: its version and the commit it was built from (GET /api/v1/health, from
// where the assistant reaches it; a public address does not say either, which reads as "").
func (b *BusHub) HubBuild(ctx context.Context) (version, commit string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.opts.Hub, "/")+"/api/v1/health", nil)
	if err != nil {
		return "", "", err
	}
	c := b.opts.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("health answered %d", resp.StatusCode)
	}
	var h struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&h); err != nil {
		return "", "", err
	}
	return h.Version, h.Commit, nil
}
