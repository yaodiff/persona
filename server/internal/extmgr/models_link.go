package extmgr

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The model configuration is the assistant's (docs/assistant/model-config.md "Who owns models"), in this process: what the
// extensions have for it (their providers, their voice models, which of them need "message triage", their quota
// readers) it asks here (app.call plugins.*, answered in-process: AppCall), and what the extensions need of it (whether
// "message triage" can be called, which launchers have a gateway, requests to a launcher's gateway sent with its key)
// is asked of it directly (models.usable, models.launchers, models.launcher_http), as the hub asked it over the bus
// before the move.

// modelsAppCall answers the manager's plugins.presets / plugins.voice / plugins.quota / plugins.need_triage; false for
// the others.
func (h *Manager) modelsAppCall(ctx context.Context, p assistantwire.AppCall) (json.RawMessage, bool, error) {
	if p.AppID != assistantwire.AppPlugins {
		return nil, false, nil
	}
	var out any
	switch p.Method {
	case assistantwire.PluginsPresets:
		out = map[string]any{"presets": h.extensions.launcherPresets(ctx)}
	case assistantwire.PluginsVoice:
		list := []assistantwire.VoicePlugin{}
		if h.extensions != nil {
			for _, x := range h.extensions.list() {
				if x.Kind != "voice" || !x.Enabled { // a switched-off one has no models to pick (the built-in is off until set up)
					continue
				}
				models, err := h.extensionModelChoices(ctx, "plugin."+x.ID)
				v := assistantwire.VoicePlugin{ID: x.ID, Name: x.Name, Models: models.Models, Default: models.Default}
				if v.Models == nil {
					v.Models = []assistantwire.ModelChoice{}
				}
				if err != nil {
					v.Error = i18n.Localize(err, h.contextLang(ctx))
				}
				list = append(list, v)
			}
		}
		out = map[string]any{"plugins": list}
	case assistantwire.PluginsQuota:
		var a struct {
			Name    string `json:"name"`
			Machine string `json:"machine"`
			BaseURL string `json:"base_url"`
			TZ      string `json:"tz"`
		}
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, true, err
		}
		report, err := h.extensions.quota(ctx, a.BaseURL, a.TZ, h.launcherSender(a.Name, a.Machine))
		if err != nil {
			return nil, true, err
		}
		out = map[string]any{"report": report}
	case assistantwire.PluginsModels: // the extensions with a model purpose of their own (extension_model.go)
		out = map[string]any{"extensions": h.extensions.modelExtensions()}
	case assistantwire.PluginsNeedTriage:
		names := h.extensionsNeedingTriage()
		if names == nil {
			names = []string{}
		}
		out = map[string]any{"names": names}
	default:
		return nil, false, nil
	}
	b, err := json.Marshal(out)
	return b, true, err
}

// triageUsable says whether inbox.triage ("message triage") can be called now, as the assistant resolves it; an error
// while it can't be asked (not connected: assistant_offline).
func (h *Manager) triageUsable(ctx context.Context) (bool, error) {
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.ModelsUsableMethod,
		assistantwire.ModelsUsableParams{Purpose: "inbox.triage"})
	return res.Usable, err
}

// launcherCall is models.launcher_http: the assistant has the launchers and their keys. Its coded errors come back as
// the hub's (errNoLauncher, errOffline, errNoGateway).
func (h *Manager) launcherCall(ctx context.Context, params map[string]any, out any) error {
	err := h.callAssistant(ctx, assistantwire.MethodModelsLauncherHTTP, params, out)
	var re *rpc.Error
	if errors.As(err, &re) {
		switch re.Code {
		case assistantwire.CodeNoLauncher:
			return errNoLauncher
		case assistantwire.CodeMachineOffline:
			return errOffline
		case assistantwire.CodeNoGateway:
			return errNoGateway
		}
	}
	return err
}

// gatewayLaunchers is models.launchers: the launchers with a gateway address, as the assistant has them (names and
// addresses, no key), for the plugins' gateway.launchers. While the assistant is not connected the error is
// assistant_offline; a manager too old to list them is unsupported, not the plugin's unknown method.
func (h *Manager) gatewayLaunchers(ctx context.Context) ([]assistantwire.GatewayLauncher, error) {
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.ModelsLaunchersMethod, buswire.None{})
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == bus.CodeUnknownMethod {
		return nil, &rpc.Error{Code: extension.CodeUnsupported, Message: "the manager is too old to list the launchers; update it"}
	}
	return res.Launchers, err
}
