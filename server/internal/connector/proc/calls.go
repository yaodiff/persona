package proc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/proto"
)

var errNotRunning = errors.New("not running")

// call runs one method on the running program.
func (c *Conn) call(ctx context.Context, method string, params, out any) error {
	if l := c.cfg.Link; l != nil {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		err := l.call(ctx, method, params, out)
		if errors.Is(err, errNotRunning) {
			return fmt.Errorf("%s: %w", c.cfg.Name, errNotRunning)
		}
		return err
	}
	c.mu.Lock()
	peer := c.peer
	c.mu.Unlock()
	if peer == nil {
		return fmt.Errorf("%s: %w", c.cfg.Name, errNotRunning)
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return peer.Call(ctx, method, params, out)
}

// LoginStep needs the program running: a sign-in keeps its state (Telegram's code request) in it between steps.
func (c *Conn) LoginStep(ctx context.Context, step string, values map[string]string) (connector.Prompt, error) {
	var p connector.Prompt
	if err := c.call(ctx, serve.MLoginStep, serve.LoginParams{Step: step, Values: values}, &p); err != nil {
		if errors.Is(err, errNotRunning) {
			return connector.Prompt{Step: step, Error: "先打开「同步 " + c.cfg.Name + "」再登录"}, nil
		}
		return p, err
	}
	c.refresh(ctx)
	return p, nil
}

// Logout signs out through the program; while it is not running (switched off), a run of it just for this.
func (c *Conn) Logout(ctx context.Context) error {
	err := c.call(ctx, serve.MLogout, struct{}{}, nil)
	if !errors.Is(err, errNotRunning) {
		c.refresh(ctx)
		return err
	}
	if l := c.cfg.Link; l != nil {
		release := l.Proc.Hold()
		defer release()
		wctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		if err := l.Proc.Wait(wctx); err != nil {
			return err
		}
		return c.call(ctx, serve.MLogout, struct{}{}, nil)
	}
	ch, err := c.spawn(ctx)
	if err != nil {
		return err
	}
	defer ch.stop()
	lctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	return ch.peer.Call(lctx, serve.MLogout, struct{}{}, nil)
}

func (c *Conn) Changes(ctx context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	var out serve.ChangesResult
	err := c.call(ctx, serve.MChanges, serve.ChangesParams{Cursor: cursor, Limit: limit}, &out)
	if err != nil {
		return nil, cursor, false, err
	}
	return out.Batches, out.Next, out.More, nil
}

func (c *Conn) Collect(ctx context.Context) ([]connector.InboxThing, error) {
	var out serve.CollectResult
	err := c.call(ctx, serve.MCollect, struct{}{}, &out)
	return out.Things, err
}

func (c *Conn) Reply(ctx context.Context, r connector.InboxReply) error {
	return c.call(ctx, serve.MReply, r, nil)
}

// GroupTool runs one of the program's tools for relating messages (group.tool); the answer is its JSON as it is.
func (c *Conn) GroupTool(ctx context.Context, call connector.GroupToolCall) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.call(ctx, serve.MGroupTool, call, &out)
	return out, err
}

func (c *Conn) GroupAppend(ctx context.Context, a connector.GroupAppend) (connector.InboxThing, error) {
	var out connector.InboxThing
	err := c.call(ctx, serve.MGroupAppend, a, &out)
	return out, err
}

func (c *Conn) GroupFallback(ctx context.Context, f connector.GroupFallback) (connector.GroupFallbackResult, error) {
	var out connector.GroupFallbackResult
	err := c.call(ctx, serve.MGroupFallback, f, &out)
	return out, err
}

// Tools answers from memory: what the program listed when it was last asked.
func (c *Conn) Tools() []proto.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.tools)
}

func (c *Conn) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	a, err := c.CallImages(ctx, name, args)
	return a.Text, err
}

// CallImages is Call with the pictures and out-of-band views the program answered (connector.ImageTools, #8484): a
// program that has none, or does not know the fields, answers the text alone.
func (c *Conn) CallImages(ctx context.Context, name string, args json.RawMessage) (connector.ToolAnswer, error) {
	var out serve.CallResult
	err := c.call(ctx, serve.MCall, serve.CallParams{Name: name, Args: args, CallFrom: serve.CallFromContext(ctx)}, &out)
	return connector.ToolAnswer{Text: out.Text, Images: out.Images, Views: out.Views}, err
}

// Actions answers from memory, as Tools.
func (c *Conn) Actions() []proto.Tool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.actions)
}

func (c *Conn) Describe(ctx context.Context, name string, args json.RawMessage) (connector.Action, string, error) {
	act, summary, _, err := c.DescribeWithApproval(ctx, name, args, "")
	return act, summary, err
}

// DescribeWithApproval calls actions.describe, optionally advertising approvalProtocol when the hub gate has a matching
// guarded auto-confirm policy for this call. Any unnegotiated auto_confirm grant is dropped.
func (c *Conn) DescribeWithApproval(ctx context.Context, name string, args json.RawMessage, approvalProtocol string) (connector.Action, string, *connector.AutoConfirmGrant, error) {
	var out serve.DescribeResult
	err := c.call(ctx, serve.MDescribe, serve.CallParams{
		Name:             name,
		Args:             args,
		ApprovalProtocol: approvalProtocol,
		CallFrom:         serve.CallFromContext(ctx),
	}, &out)
	if err != nil {
		return connector.Action{}, "", nil, err
	}
	if approvalProtocol == "" {
		return out.Action, out.Summary, nil, nil
	}
	return out.Action, out.Summary, out.AutoConfirm, nil
}

// Do sends the write once, with an id of its own: the program runs one id at most once, and Do never sends it again
// (a run that timed out may have happened; only the user asks again).
func (c *Conn) Do(ctx context.Context, name string, args json.RawMessage) (string, error) {
	done, err := c.DoRef(ctx, name, args)
	return done.Text, err
}

// DoRef is Do with the message_ref the program answered, when it did (connector.RefActions): a program that does not
// know the field answers the text alone, and the ref is empty.
func (c *Conn) DoRef(ctx context.Context, name string, args json.RawMessage) (connector.DoResult, error) {
	return c.DoWithApproval(ctx, name, args, connector.AutoConfirmApproval{})
}

// DoWithApproval sends actions.do with optional guarded auto-confirm approval fields (approval_mode and approval_token),
// preserving exact raw args JSON and a fresh hub-generated request_id.
func (c *Conn) DoWithApproval(ctx context.Context, name string, args json.RawMessage, approval connector.AutoConfirmApproval) (connector.DoResult, error) {
	if (approval.Mode == "") != (approval.Token == "") {
		return connector.DoResult{}, errors.New("actions.do requires approval_mode and approval_token together")
	}
	if approval.Mode != "" && approval.Mode != connector.ApprovalModeAutoConfirm {
		return connector.DoResult{}, fmt.Errorf("actions.do: unsupported approval_mode %q", approval.Mode)
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	var out serve.CallResult
	err := c.call(ctx, serve.MDo, serve.DoParams{
		Name:          name,
		Args:          args,
		RequestID:     hex.EncodeToString(id[:]),
		ApprovalMode:  approval.Mode,
		ApprovalToken: approval.Token,
		CallFrom:      serve.CallFromContext(ctx),
	}, &out)
	return connector.DoResult{Text: out.Text, MessageRef: out.MessageRef}, err
}

func (c *Conn) Thread(ctx context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	var out connector.ThreadPage
	err := c.call(ctx, serve.MThread, serve.ThreadParams{Thread: thread, Before: before, Limit: limit}, &out)
	return out, err
}

func (c *Conn) Media(ctx context.Context, ref string) (connector.Media, error) {
	var out connector.Media
	err := c.call(ctx, serve.MMedia, serve.MediaParams{Ref: ref}, &out)
	return out, err
}

// ItemSource is the program's knowledge-base items (items.list, items.doc_title); the hub asks only when it declared
// items.
func (c *Conn) ItemSource() kb.ItemSource { return connItems{c} }

type connItems struct{ c *Conn }

func (s connItems) Items(ctx context.Context) ([]kbwire.Item, error) {
	var out serve.ItemsResult
	err := s.c.call(ctx, serve.MItems, struct{}{}, &out)
	return out.Items, err
}

// DocTitle is "" when the program cannot tell (or fails): a title is a nicety.
func (s connItems) DocTitle(ctx context.Context, url string) string {
	var out serve.DocTitleResult
	if s.c.call(ctx, serve.MDocTitle, serve.DocTitleParams{URL: url}, &out) != nil {
		return ""
	}
	return out.Title
}

// ArchiveMedia is an archived message's attachment (archive.media); a program without it answers unknown_method and
// the line keeps the [attachment] marker (in Chinese).
func (c *Conn) ArchiveMedia(ctx context.Context, ref string) (connector.Media, error) {
	var out connector.Media
	err := c.call(ctx, serve.MArchiveMedia, serve.MediaParams{Ref: ref}, &out)
	return out, err
}

// HistoryChats are the conversations the program can import the history of (history.chats); the hub asks only a
// program declaring history. An error (with its rpc code) comes back as the program said it.
func (c *Conn) HistoryChats(ctx context.Context) ([]connectorwire.HistoryChat, error) {
	var out serve.HistoryChatsResult
	err := c.call(ctx, serve.MHistoryChats, struct{}{}, &out)
	return out.Chats, err
}

// History is one page of a conversation (archive.history). A call that timed out may still have been read by the
// program: it is not sent again here (docs/protocol/connector-protocol.md "History").
func (c *Conn) History(ctx context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	var out connectorwire.HistoryPage
	err := c.call(ctx, serve.MHistory, q, &out)
	return out, err
}

// HistoryAnchors are the owner's messages over a window (history.anchors), for a program declaring history_anchors.
func (c *Conn) HistoryAnchors(ctx context.Context, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	var out connectorwire.HistoryAnchors
	err := c.call(ctx, serve.MHistoryAnchors, q, &out)
	return out, err
}

// The capabilities Conn answers for: what the hub's pipes look for.
var (
	_ connector.Accounts            = (*Conn)(nil)
	_ connector.Login               = (*Conn)(nil)
	_ connector.Archive             = (*Conn)(nil)
	_ connector.Inbox               = (*Conn)(nil)
	_ connector.ReadTools           = (*Conn)(nil)
	_ connector.Actions             = (*Conn)(nil)
	_ connector.RefActions          = (*Conn)(nil)
	_ connector.ApprovalActions     = (*Conn)(nil)
	_ connector.IdentifiedConnector = (*Conn)(nil)
	_ connector.Threads             = (*Conn)(nil)
	_ connector.Events              = (*Conn)(nil)
	_ connector.Runner              = (*Conn)(nil)
	_ connector.Items               = (*Conn)(nil)
	_ connector.ArchiveMedia        = (*Conn)(nil)
	_ connector.Snapshot            = (*Conn)(nil)
	_ connector.Grouper             = (*Conn)(nil)
	_ connector.EventWriter         = (*Conn)(nil)
	_ connector.History             = (*Conn)(nil)
	_ connector.HistoryAnchors      = (*Conn)(nil)
)

// Snapshot is the program's live state (snapshot.get).
func (c *Conn) Snapshot(ctx context.Context, tz string) (connector.SnapshotData, error) {
	var out connector.SnapshotData
	err := c.call(ctx, serve.MSnapshot, serve.SnapshotParams{TZ: tz}, &out)
	return out, err
}
