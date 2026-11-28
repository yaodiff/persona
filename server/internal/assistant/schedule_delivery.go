package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// TaskDelivery names an optional channel thread where a scheduled task's finished result is posted
// instead of the owner's private DM mirror, so replies in that thread continue the task's execution conversation.
type TaskDelivery = assistantwire.TaskDelivery

func cloneTaskDelivery(d *TaskDelivery) *TaskDelivery {
	if d == nil {
		return nil
	}
	cp := *d
	return &cp
}

func validDeliveryToken(s string) bool {
	return s != "" && len(s) <= 100 && !strings.ContainsFunc(s, unicode.IsSpace)
}

// cleanTaskDelivery validates and normalizes an optional delivery anchor.
// A nil or all-empty anchor clears delivery; otherwise channel must be "feishu" with non-empty chat_id and thread_id.
func cleanTaskDelivery(d *TaskDelivery) (*TaskDelivery, error) {
	if d == nil {
		return nil, nil
	}
	ch := strings.ToLower(strings.TrimSpace(d.Channel))
	chat := strings.TrimSpace(string(d.ChatID))
	thread := strings.TrimSpace(string(d.ThreadID))
	if ch == "" && chat == "" && thread == "" {
		return nil, nil
	}
	if ch != channel.Feishu || !validDeliveryToken(chat) || !validDeliveryToken(thread) {
		return nil, i18n.Err("assistant-schedule.bad_delivery")
	}
	return &TaskDelivery{
		Channel:  ch,
		ChatID:   assistantwire.FeishuChatID(chat),
		ThreadID: assistantwire.FeishuThreadID(thread),
	}, nil
}

// deliveryRaw parses an optional delivery JSON field (from HTTP bodies or scheduling tools).
// Missing leaves it untouched (has=false); null or {} clears it (has=true, d=nil).
func deliveryRaw(raw json.RawMessage) (d *TaskDelivery, has bool, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, true, nil
	}
	var in TaskDelivery
	if err := json.Unmarshal(trimmed, &in); err != nil {
		return nil, true, i18n.Err("assistant-schedule.bad_delivery")
	}
	cleaned, err := cleanTaskDelivery(&in)
	return cleaned, true, err
}

func deliveryIn(raw map[string]json.RawMessage) (d *TaskDelivery, has bool, err error) {
	v, ok := raw["delivery"]
	if !ok {
		return nil, false, nil
	}
	return deliveryRaw(v)
}

func isFeishuThreadDelivery(d *TaskDelivery) bool {
	return d != nil && d.Channel == channel.Feishu && d.ChatID != "" && d.ThreadID != ""
}

// taskDeliveryForConversation looks up the scheduled task that owns or just ran in machine/session,
// returning its display label and optional thread delivery anchor.
// Tasks without a delivery anchor still match only their own RunSessionID/RunMachineID (matching taskOfConversation).
func (s *Service) taskDeliveryForConversation(ctx context.Context, machine, session string) (string, *TaskDelivery) {
	if session == "" {
		return "", nil
	}
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return "", nil
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.RunSessionID == session && t.RunMachineID == machine {
			return t.label(), cloneTaskDelivery(t.Delivery)
		}
	}
	for _, t := range sc.tasks {
		if !isFeishuThreadDelivery(t.Delivery) {
			continue
		}
		if t.SessionID == session && (t.MachineID == "" || t.MachineID == machine) {
			return t.label(), cloneTaskDelivery(t.Delivery)
		}
		if t.LastSessionID == session {
			return t.label(), cloneTaskDelivery(t.Delivery)
		}
	}
	return "", nil
}
