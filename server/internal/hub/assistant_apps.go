package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
)

// The hub's plain capabilities the assistant asks for its directory and its settings (app.call, assistantwire): the
// where the knowledge base keeps its files and the hub's own settings for persona_settings (the connectors' and the
// extensions' parts are the manager's own: internal/extmgr). Nothing here knows what the
// assistant does with them.

// assistantAppCall answers the app.call methods of this file; false for the others.
func (h *Hub) assistantAppCall(ctx context.Context, from string, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	var out any
	var err error
	switch p.AppID + " " + p.Method {
	case assistantwire.AppSettings + " " + assistantwire.SettingsList:
		list := []assistantwire.Setting{}
		for _, s := range h.selfSettings() {
			list = append(list, assistantwire.Setting{Key: s.key, Title: s.title, About: s.about, Value: s.value, Actions: s.actions()})
		}
		for _, s := range h.managerSettings(ctx, from) { // the mail accounts, the sources' own rules (the caller's)
			list = append(list, assistantwire.Setting{Key: s.Key, Title: s.Title, About: s.About, Value: s.Value, Actions: s.Actions})
		}
		out = map[string]any{"settings": list}
	case assistantwire.AppNotify + " " + assistantwire.NotifyQuietGet:
		out = map[string]bool{"quiet": h.push.quietNow()}
	case assistantwire.AppSettings + " " + assistantwire.AgentsDisplayGet:
		owner := h.defaultOwner(ctx)
		if from == owner {
			return json.RawMessage(`{}`), true, nil // the caller owns the settings
		}
		req := assistantwire.HTTPRequest{Method: http.MethodGet, Path: "/api/v1/agents/display-settings"}
		res, err := buswire.Call(ctx, h.managerCaller(owner), assistantwire.HTTPMethod, req)
		if err != nil {
			return nil, true, err
		}
		if res.Status != http.StatusOK {
			return nil, true, fmt.Errorf("Agents display settings returned HTTP %d", res.Status)
		}
		return res.Body, true, nil
	default:
		return nil, false, nil
	}
	if err != nil {
		return nil, true, busCodedError(err)
	}
	raw, err := json.Marshal(out)
	return raw, true, err
}

// noDevice is device.call's and app.call's error for a machine the hub does not have.
func noDevice(id string) error { return fmt.Errorf("%w %q", assistantwire.ErrNoDevice, id) }
