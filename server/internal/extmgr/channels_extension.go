package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The channels plugins contribute (the channel extension point, docs/extensions/channels.md "Contributing a channel"),
// the hub's part: the program talks to its chat app — it hands the hub the owner's messages (channel.receive), which go
// to the assistant (channels.receive), and sends what the assistant gives it (channels.send → channel.send). Commands,
// where a message goes, the turn, what is asked in text, pushes and the sync mirror are the assistant's.

// channelSendTries: a send that fails (not cannot_send) goes again after 1, 2, 4 s, with the same ref.
var channelSendTries = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// errCannotSend: the channel has nowhere to send to yet (WeChat before the owner's first message).
var errCannotSend = errors.New("cannot send yet")

type cannotSendError struct {
	msg string
}

func (e cannotSendError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return errCannotSend.Error()
}

func (e cannotSendError) Unwrap() error { return errCannotSend }

// extensionCall calls channel id's program.
func (h *Manager) extensionCall(ctx context.Context, id, method string, params, out any) error {
	if call := h.channels.call; call != nil { // tests: a program of their own
		err := call(ctx, id, method, params, out)
		if errors.Is(err, channel.ErrCannotSend) {
			msg := strings.TrimSpace(err.Error())
			if msg == channel.ErrCannotSend.Error() {
				msg = ""
			}
			return cannotSendError{msg: msg}
		}
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == channel.CodeCannotSend {
			return cannotSendError{msg: strings.TrimSpace(re.Message)}
		}
		return err
	}
	pc, ok := h.extensionChannel(id)
	if !ok || h.extensions == nil {
		return fmt.Errorf("no channel %s", id)
	}
	hp := h.extensions.get(pc.extension.Record.ID)
	if hp == nil || hp.proc == nil {
		return errors.New(i18n.T(h.botLang(ctx), "hub-channels_extension.not_running", i18n.Args{"name": pc.item.Name}))
	}
	p, err := extension.Targeted(params, id)
	if err != nil {
		return err
	}
	err = hp.proc.Call(ctx, channel.InPlugin(method), p, out)
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == channel.CodeCannotSend {
		return cannotSendError{msg: strings.TrimSpace(re.Message)}
	}
	return err
}

// channelSendPiece sends one piece to channel id's owner (channels.send): again on a failure, with the same ref.
func (h *Manager) channelSendPiece(ctx context.Context, id string, p channel.SendParams) (channel.SendResult, error) {
	pc, ok := h.extensionChannel(id)
	if !ok && h.channels.call == nil {
		return channel.SendResult{}, fmt.Errorf("no channel %s", id)
	}
	if ok && !pc.item.Capabilities.ImagesOut {
		p.Images = nil // only a channel that says it sends pictures gets them (#11986)
	}
	p.Contribution = ""
	var out channel.SendResult
	var err error
	for attempt := 0; ; attempt++ {
		out = channel.SendResult{}
		if err = h.extensionCall(ctx, id, channel.MSend, p, &out); err == nil || errors.Is(err, errCannotSend) || attempt >= len(channelSendTries) {
			break
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(channelSendTries[attempt]):
		}
	}
	if errors.Is(err, errCannotSend) {
		return out, &rpc.Error{Code: assistantwire.CodeCannotSend, Message: err.Error()}
	}
	return out, err
}

// channelTell sends a line to channel id's owner, logging a failure (never the text): what the hub says itself.
func (h *Manager) channelTell(ctx context.Context, id, replyTo, text, kind string) {
	if _, err := h.channelSendPiece(ctx, id, channel.SendParams{Ref: "hub-" + randomHex(8), Text: text, Kind: kind, ReplyTo: replyTo}); err != nil {
		h.logf("channels: %s: send: %v", id, err)
	}
}

// extensionChannelCall answers a plugin's calls in the channel namespace (extensions.go): method without it.
func (h *Manager) extensionChannelCall(extensionID, method string, params json.RawMessage) (any, error) {
	id := contributionOf(params)
	p := h.extensions.reg.Get(extensionID)
	if p == nil {
		return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no plugin " + extensionID}
	}
	items := p.Manifest.Channels()
	if id == "" && len(items) == 1 {
		id = items[0].ID
	}
	i := slices.IndexFunc(items, func(it channel.Item) bool { return it.ID == id })
	if i < 0 {
		return nil, &rpc.Error{Code: extension.CodeNoContribution, Message: "no channel " + id}
	}
	switch method {
	case channel.MReceive:
		var in channel.ReceiveParams
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Message.ID) == "" {
			return nil, errors.New("a message without an id")
		}
		in.Message.ChatKind = in.Message.ChatKind.Known()
		go h.channelReceive(id, in.Message)
		return struct{}{}, nil
	case channel.MStatus:
		var in channel.StatusParams
		if err := json.Unmarshal(params, &in); err != nil {
			return nil, err
		}
		c := &h.channels
		c.mu.Lock()
		if c.statuses == nil {
			c.statuses = map[string]*channel.StatusParams{}
		}
		c.statuses[id] = &in
		c.mu.Unlock()
		return struct{}{}, nil
	}
	return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method channel." + method}
}

// channelStatus is what a channel without a connector last said of itself (nil before it said anything).
func (h *Manager) channelStatus(id string) *channel.StatusParams {
	c := &h.channels
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statuses[id]
}

// channelMessageMost is the most a channel's message brings for the assistant, its pictures and files together: the
// bus carries a message of at most 64 MiB, the data as base64 in it.
const channelMessageMost = 40 << 20

// channelReceive hands one of the owner's messages to the assistant (channels.receive), which takes it from there:
// it waits while the assistant is not connected (toAssistant), and only then is the owner told it can't be reached,
// once per message.
func (h *Manager) channelReceive(id string, m channel.Message) {
	total := 0
	keep := func(kind string) func(channel.Blob) bool {
		return func(b channel.Blob) bool {
			if total+len(b.Data) <= channelMessageMost {
				total += len(b.Data)
				return false
			}
			m.Unsupported = append(m.Unsupported, kind+"「"+firstNonEmpty(b.Name, "未命名")+"」（太大）")
			return true
		}
	}
	m.Images = slices.DeleteFunc(m.Images, keep("图片"))
	m.Files = slices.DeleteFunc(m.Files, keep("文件"))
	raw, err := json.Marshal(m)
	if err != nil {
		return
	}
	// The owner wrote it (the channel's program only hands over the owner's): what it answers, it answers once, in
	// this channel, and only a card the channel takes a typed answer for.
	action := h.issueMessageAction(id, channel.TypedAnswerKinds...)
	err = h.toAssistant(assistantwire.MethodChannelReceive, assistantwire.ChannelReceive{Channel: id, Message: raw, Action: action})
	if err == nil {
		return
	}
	if !errors.Is(err, errAssistantOffline) {
		h.logf("channels: %s: hand the message to the assistant: %v", id, err)
	}
	if !h.claimOfflineNote(id + "-" + m.ID) {
		return // told once: the chat app delivered it again
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	h.channelTell(ctx, id, m.ID, i18n.T(h.userLang(), "hub-channels_extension.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}), channel.OutNote)
}

// claimOfflineNote: the first time the hub tells the owner about this message (a chat app delivers some again).
func (h *Manager) claimOfflineNote(key string) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if slices.Contains(b.seen, key) {
		return false
	}
	b.seen = append(b.seen, key)
	if len(b.seen) > botSeenKeep {
		b.seen = b.seen[len(b.seen)-botSeenKeep:]
	}
	return true
}
