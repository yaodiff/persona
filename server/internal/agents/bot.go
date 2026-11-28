package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/rpc"
)

// BotSessionKey is the stable public key of every registered endpoint. It deliberately does not contain Orca's session ID.
const BotSessionKey = "endpoint"

// BotRegistration is a manager-owned explicit endpoint binding. Provider is the transport ("orca"). It binds either one
// exact Orca session (SessionID, phase 1) or one Orca bot (BotID, #16891), whose open Home Chat every read and send
// resolves again on the node, so a bot that started a new Home Chat is followed there. Exactly one of the two is set.
type BotRegistration struct {
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	Title     string `json:"title"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
}

// BotHistoryRequest and BotSendRequest are internal manager → node calls, never public HTTP chat endpoints. A request
// names an exact session, or a bot (BotID) whose open Home Chat the node resolves. A send that names a bot names the
// session too: the chat the manager resolved and fixed the operation for, which must still be the bot's Home Chat.
type BotHistoryRequest struct {
	SessionID string `json:"session_id,omitempty"`
	BotID     string `json:"bot_id,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type BotSendRequest struct {
	SessionID   string `json:"session_id"`
	BotID       string `json:"bot_id,omitempty"`
	OperationID string `json:"operation_id"`
	Text        string `json:"text"`
}

// BotPage is the node-projected directory/history page. Messages come only from Orca history, never a send receipt.
// SessionID is the Orca session the page was read from (a bot's Home Chat when the request named a bot).
type BotPage struct {
	CanSend    bool      `json:"can_send"`
	RankTimeMs int64     `json:"rank_time_ms"`
	Working    bool      `json:"working"`
	Messages   []Message `json:"messages"`
	HasMore    bool      `json:"has_more"`
	NextCursor string    `json:"next_cursor,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
}

// BotCaller reaches the exact machine through extmgr.Machine.call; the manager never opens an Orca socket.
type BotCaller interface {
	CallBot(ctx context.Context, machineID, method string, params, out any) error
}

type BotCallFunc func(ctx context.Context, machineID, method string, params, out any) error

func (f BotCallFunc) CallBot(ctx context.Context, machineID, method string, params, out any) error {
	return f(ctx, machineID, method, params, out)
}

// BotRegistry shares the operation book across endpoint sources so concurrent registrations cannot overwrite each other.
type BotRegistry struct {
	caller BotCaller
	ops    *botOperationBook
}

func NewBotRegistry(kv KV, caller BotCaller) *BotRegistry {
	return &BotRegistry{caller: caller, ops: &botOperationBook{kv: kv}}
}

var botEndpointID = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Source constructs one isolated agents.Source for one explicit registration.
func (r *BotRegistry) Source(reg BotRegistration) (*BotSource, error) {
	if !botEndpointID.MatchString(reg.ID) {
		return nil, fail(http.StatusBadRequest, "agents: bot endpoint id is invalid")
	}
	if reg.Provider != "orca" {
		return nil, fail(http.StatusBadRequest, "agents: unsupported bot provider")
	}
	if strings.TrimSpace(reg.Title) == "" || len(reg.Title) > 200 || reg.MachineID == "" || (reg.SessionID == "") == (reg.BotID == "") ||
		len(reg.SessionID) > 512 || len(reg.BotID) > 200 {
		return nil, fail(http.StatusBadRequest, "agents: incomplete bot registration")
	}
	return &BotSource{reg: reg, caller: r.caller, ops: r.ops}, nil
}

// DiscoveredBot is the registration of an Orca bot the manager found on a computer (agentbot.roster, #16891): bound
// to the bot, whose open Home Chat the node resolves on every call, so a new Home Chat is followed. Its public id comes
// from the computer and Orca's bot id: the same across restarts and Home Chat resets, and never an Orca session.
func DiscoveredBot(machineID, botID, name string) BotRegistration {
	sum := sha256.Sum256([]byte(machineID + "\x00" + botID))
	title := strings.TrimSpace(name)
	if title == "" {
		title = botID
	}
	if len(title) > 200 {
		title = strings.ToValidUTF8(title[:200], "")
	}
	return BotRegistration{ID: "orca-" + hex.EncodeToString(sum[:6]), Provider: "orca", Title: title, MachineID: machineID,
		BotID: botID}
}

// BotSource is one manager-registered endpoint as an existing agents.Source.
type BotSource struct {
	reg    BotRegistration
	caller BotCaller
	ops    *botOperationBook
}

func (s *BotSource) Name() string { return "bot_" + s.reg.ID }

func (s *BotSource) call(ctx context.Context, method string, params, out any) error {
	if s.caller == nil {
		return fail(http.StatusBadGateway, "agents: bot is unavailable")
	}
	if err := s.caller.CallBot(ctx, s.reg.MachineID, method, params, out); err != nil {
		return botCallError(err)
	}
	return nil
}

func (s *BotSource) page(ctx context.Context, method, cursor string, limit int) (*BotPage, error) {
	var page BotPage
	req := BotHistoryRequest{SessionID: s.reg.SessionID, BotID: s.reg.BotID, Cursor: cursor, Limit: limit}
	if err := s.call(ctx, method, req, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// binding is what a send is fixed to: the registration itself, or for one bound to a bot, the registration with the
// session of the bot's open Home Chat now. The operation book keeps that session, so the same clientId after the bot
// started a new Home Chat conflicts instead of following it there.
func (s *BotSource) binding(ctx context.Context) (BotRegistration, error) {
	if s.reg.BotID == "" {
		return s.reg, nil
	}
	page, err := s.page(ctx, "agentbot.describe", "", 1)
	if err != nil {
		return BotRegistration{}, err
	}
	if page.SessionID == "" {
		return BotRegistration{}, fail(http.StatusBadGateway, "agents: bot is unavailable")
	}
	reg := s.reg
	reg.SessionID = page.SessionID
	return reg, nil
}

func (s *BotSource) session(page *BotPage) Session {
	return Wrap(s.Name(), AgentSession{SessionKey: BotSessionKey, AccountID: s.reg.ID, Provider: "bot", Title: s.reg.Title,
		Kind: "bot", CanSend: page.CanSend, RankTimeMs: page.RankTimeMs}, "")
}

func (s *BotSource) Status(ctx context.Context) (Status, error) {
	st := Status{Source: s.Name(), Enabled: true,
		Providers: []AgentProvider{{Provider: "bot", Connection: "server", Available: true}},
		Accounts:  []AgentAccount{{AccountID: s.reg.ID, Provider: "bot", DisplayName: s.reg.Title, Status: "active", SessionCount: 1}}}
	if _, err := s.Sessions(ctx); err != nil {
		st.Error = err.Error()
		st.Providers[0].Available = false
		st.Accounts[0].Status = "unavailable"
		return st, err
	}
	return st, nil
}

func (s *BotSource) Sessions(ctx context.Context) ([]Session, error) {
	page, err := s.page(ctx, "agentbot.describe", "", 1)
	if err != nil {
		return nil, err
	}
	return []Session{s.session(page)}, nil
}

// Search is deliberately empty in phase 1: it cannot turn unregistered structured sessions into results.
func (s *BotSource) Search(context.Context, string) ([]SearchHit, error) { return []SearchHit{}, nil }

func (s *BotSource) Messages(ctx context.Context, key, cursor string, limit int) (*MessagePage, error) {
	if key != BotSessionKey {
		return nil, fail(http.StatusNotFound, "agents: unknown bot endpoint")
	}
	if limit == 0 {
		limit = 40
	}
	if limit < 1 || limit > 200 {
		return nil, fail(http.StatusBadRequest, "agents: invalid history limit")
	}
	page, err := s.page(ctx, "agentbot.history", cursor, limit)
	if err != nil {
		return nil, err
	}
	if page.Messages == nil {
		page.Messages = []Message{}
	}
	for i := range page.Messages {
		page.Messages[i].SessionKey = BotSessionKey
	}
	sess := s.session(page)
	return &MessagePage{Session: &sess, Messages: page.Messages, HasMore: page.HasMore, NextCursor: page.NextCursor}, nil
}

func (s *BotSource) prepareSend(ctx context.Context, clientID, text string) error {
	reg, err := s.binding(ctx)
	if err != nil {
		return err
	}
	_, err = s.ops.prepare(ctx, reg, clientID, text)
	return err
}

func (s *BotSource) Send(ctx context.Context, key, clientID, text string) (json.RawMessage, error) {
	if key != BotSessionKey {
		return nil, fail(http.StatusNotFound, "agents: unknown bot endpoint")
	}
	clientID = strings.ToLower(clientID)
	if !uuidRe.MatchString(clientID) {
		return nil, fail(http.StatusBadRequest, "agents: clientId must be a UUID")
	}
	if strings.TrimSpace(text) == "" || TextLen(text) > AgentMaxText {
		return nil, fail(http.StatusBadRequest, "agents: invalid bot message")
	}
	reg, err := s.binding(ctx)
	if err != nil {
		return nil, err
	}
	op, err := s.ops.prepare(ctx, reg, clientID, text)
	if err != nil {
		return nil, err
	}
	var receipt struct {
		Accepted bool `json:"accepted"`
	}
	req := BotSendRequest{SessionID: reg.SessionID, BotID: reg.BotID, OperationID: op, Text: text}
	if err := s.call(ctx, "agentbot.send", req, &receipt); err != nil {
		return nil, err
	}
	if !receipt.Accepted {
		return nil, fail(http.StatusConflict, "agents: bot did not accept the message")
	}
	// A receipt is only acceptance. No assistant message is synthesized here; replies arrive through Messages/history.
	return json.RawMessage(`{"accepted":true}`), nil
}

func (s *BotSource) DeleteMessage(_ context.Context, key, _ string) error {
	if key != BotSessionKey {
		return fail(http.StatusNotFound, "agents: unknown bot endpoint")
	}
	return fail(http.StatusNotImplemented, "agents: bot history cannot delete messages")
}

func (s *BotSource) MarkRead(_ context.Context, key string) error {
	if key != BotSessionKey {
		return fail(http.StatusNotFound, "agents: unknown bot endpoint")
	}
	return nil // Orca has no independent personal-Agent read marker in phase 1.
}

func botCallError(err error) error {
	var re *rpc.Error
	code := ""
	if errors.As(err, &re) {
		code = re.Code
	}
	switch code {
	case "not_found":
		return fail(http.StatusNotFound, "agents: bot endpoint is not linked to an active session")
	case "invalid":
		return fail(http.StatusBadRequest, "agents: invalid bot request")
	case "conflict", "refused":
		return fail(http.StatusConflict, "agents: bot refused the operation")
	case "unknown":
		return fail(http.StatusBadGateway, "agents: bot outcome is unknown; retry only with the same clientId")
	default:
		return fail(http.StatusBadGateway, "agents: bot is unavailable")
	}
}

type botOperation struct {
	ClientID    string    `json:"client_id"`
	EndpointID  string    `json:"endpoint_id"`
	MachineID   string    `json:"machine_id"`
	SessionID   string    `json:"session_id"`
	TextSHA256  string    `json:"text_sha256"`
	OperationID string    `json:"operation_id"`
	CreatedAt   time.Time `json:"created_at"`
}

type botOperationBook struct {
	mu     sync.Mutex
	kv     KV
	loaded bool
	rows   map[string]botOperation
	now    func() time.Time // tests; nil is time.Now
}

const botOperationsKey = "agents.bot.operations"

// botOperationsMax bounds the book, which is rewritten whole on every new send. It is four times outboxMax, so a
// clientId still outlives its outbox row; only a retry after that many newer sends gets a fresh operation ID.
var botOperationsMax = 4 * outboxMax

// prepare persists the exact binding, payload hash and Orca operation ID before any write. It contains no message text or transcript.
func (b *botOperationBook) prepare(ctx context.Context, reg BotRegistration, clientID, text string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		b.rows = map[string]botOperation{}
		if b.kv != nil {
			var saved map[string]botOperation
			ok, err := b.kv.Setting(ctx, botOperationsKey, &saved)
			if err != nil {
				return "", fail(http.StatusBadGateway, "agents: could not read bot send operations")
			}
			if ok && saved != nil {
				b.rows = saved
			}
		}
		b.loaded = true
	}
	fingerprint := textSHA256(text)
	if op, ok := b.rows[clientID]; ok {
		if op.EndpointID != reg.ID || op.MachineID != reg.MachineID || op.SessionID != reg.SessionID || op.TextSHA256 != fingerprint {
			return "", fail(http.StatusConflict, "agents: clientId was already used for a different payload")
		}
		return op.OperationID, nil
	}
	now := time.Now().UTC()
	if b.now != nil {
		now = b.now().UTC()
	}
	op := botOperation{ClientID: clientID, EndpointID: reg.ID, MachineID: reg.MachineID, SessionID: reg.SessionID,
		TextSHA256: fingerprint, OperationID: orcart.OperationID(now, reg.MachineID+"/"+reg.SessionID+"/"+clientID), CreatedAt: now}
	next := make(map[string]botOperation, len(b.rows)+1)
	maps.Copy(next, b.rows)
	next[clientID] = op
	pruneBotOperations(next, botOperationsMax)
	if b.kv != nil {
		if err := b.kv.PutSetting(ctx, botOperationsKey, next); err != nil {
			return "", fail(http.StatusBadGateway, "agents: could not persist bot send operation")
		}
	}
	b.rows = next
	return op.OperationID, nil
}

// pruneBotOperations drops the oldest rows beyond max (ties by client ID, so the result is deterministic).
func pruneBotOperations(rows map[string]botOperation, max int) {
	if len(rows) <= max {
		return
	}
	list := slices.Collect(maps.Values(rows))
	slices.SortFunc(list, func(a, b botOperation) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ClientID, b.ClientID)
	})
	for _, old := range list[max:] {
		delete(rows, old.ClientID)
	}
}
