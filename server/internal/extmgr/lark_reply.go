package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
)

func (h *Manager) observeLarkReply(ctx context.Context, raw json.RawMessage) (any, error) {
	var watch connectorwire.LarkReplyWatch
	if err := json.Unmarshal(raw, &watch); err != nil {
		return nil, err
	}
	if h.connectors == nil || !h.connectors.Enabled(cliwire.ConnectorID) {
		return nil, errors.New("Lark CLI connector is unavailable")
	}
	reader, ok := h.connectors.Get(cliwire.ConnectorID).(cliport.Connector)
	if !ok || reader == nil {
		return nil, errors.New("Lark CLI connector is unavailable")
	}
	readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return reader.ObserveReply(readCtx, watch)
}

func (h *Manager) observeLarkTopic(ctx context.Context, raw json.RawMessage) (any, error) {
	var watch connectorwire.LarkTopicWatch
	if err := json.Unmarshal(raw, &watch); err != nil {
		return nil, err
	}
	if h.connectors == nil || !h.connectors.Enabled(cliwire.ConnectorID) {
		return nil, errors.New("Lark CLI connector is unavailable")
	}
	reader, ok := h.connectors.Get(cliwire.ConnectorID).(cliport.Connector)
	if !ok || reader == nil {
		return nil, errors.New("Lark CLI connector is unavailable")
	}
	readCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	return reader.ObserveTopic(readCtx, watch)
}
