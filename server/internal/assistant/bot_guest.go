package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/larkhost"
)

// Group guests (docs/extensions/bots.md "Group access for teammates"). The run itself is the lark module's
// (modules/lark/guest.go): this file is only what that module asks of the core. A guest never enters the owner's
// conversations, so it never reaches handToAssistant, StartRun, feishuMessage or openFromElsewhere.

// StartGroupGuest starts the guest that answers one chat and thread, registered before the node starts it. The tools
// are the reply tool plus the owner-listed connector tools that still pass the guest check; one that fails is dropped
// and logged, so the guest starts with fewer tools, never more. Without the lark module no guest starts.
func (s *Service) StartGroupGuest(ctx context.Context, group, text, launcher string, tools []string) (string, string, error) {
	names := make([]string, 0, 1+len(tools))
	for _, tool := range s.larkModule().HandoffTools() {
		if tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	if len(names) == 0 {
		return "", "", larkhost.ErrNoChannel
	}
	for _, name := range tools {
		if err := s.checkGuest(ctx, &GuestSpec{Tools: []string{name}}); err != nil {
			s.logf("assistant: group guest drops %s: %v", name, err)
			continue
		}
		names = append(names, name)
	}
	id := newGuestSessionID()
	machine := s.settings(ctx).MachineID
	if err := s.guestSetGroup(ctx, machine, id, group, names); err != nil {
		return "", "", err
	}
	sess, err := s.Start(ctx, StartParams{Text: text, Launcher: launcher, GuestTools: names, GuestGroup: group,
		NewSessionID: id, Origin: OriginChannel, OriginRef: channel.Feishu})
	if err != nil {
		_ = s.guestDrop(context.WithoutCancel(ctx), id) // a start that never ran leaves no guest behind
		return "", "", err
	}
	return sess.MachineID, sess.SessionID, nil
}

// SendGroupGuest sends one later question into the conversation that already answers group. A session the node does
// not have returns larkhost.ErrGuestGone, so the caller starts a fresh one.
func (s *Service) SendGroupGuest(ctx context.Context, machine, session, turn, text string) error {
	err := s.sendOnce(ctx, machine, map[string]any{"session_id": session, "text": text, "client_msg_id": "bg:" + turn})
	if err != nil && sessionNotFound(err) {
		return larkhost.ErrGuestGone
	}
	return err
}

// The wire type the hub sends, named here so a change of it fails this file's build.
var _ = assistantwire.FeishuGuestTurn{}

// A guest's question is another person's words; the caller keeps it out of the owner's store.
var _ = strings.TrimSpace
