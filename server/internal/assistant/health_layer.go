package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/deeplink"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/healthhost"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// The Health module (internal/modules/health, docs/architecture/core-and-modules.md "Health"): Health intake, the water
// reminders, the intake page, the legacy water import and the health_intake tool. The core reaches it only through
// internal/healthhost; s.healthMod is nil when the module is left out of the build (persona_without_health,
// persona_core_only) or switched off (the modules row), and then the core serves no intake route, announces no
// health_intake or health_items capability, offers no health_intake tool and sends no water reminder. The Health space (health.go)
// stays in the core.

// healthRoutes makes the module's layer, unless the build has no module or the modules row switches it off (the port
// asks the switch, moduleswitch, as every provider seam does), and registers its routes in the core's order.
func (s *Service) healthRoutes() {
	if l, ok := healthhost.New(s.moduleSwitch, healthHost{s}); ok {
		s.healthMod = l
		l.Routes(s.handle)
	}
}

// healthOn says whether the module is loaded.
func (s *Service) healthOn() bool { return s.healthMod != nil }

// healthReminders runs the module's water reminders until ctx ends (none without the module).
func (s *Service) healthReminders(ctx context.Context) {
	if s.healthMod != nil {
		s.healthMod.ReminderLoop(ctx)
	}
}

// healthTool runs the health_intake tool.
func (s *Service) healthTool(ctx context.Context, call healthhost.ToolCall) (string, error) {
	if s.healthMod == nil {
		return "", errors.New("unknown tool " + call.Name)
	}
	return s.healthMod.Tool(ctx, call)
}

// withHealth removes the intake and items capabilities when svc has no Health layer (or, before construction,
// when the build has no Health module), so the hub announces neither feature.
func withHealth(caps []string, svc *Service) []string {
	on := healthhost.InBuild(moduleswitch.Set{})
	if svc != nil {
		on = svc.healthOn()
	}
	if on {
		return caps
	}
	return slices.DeleteFunc(caps, func(c string) bool {
		return c == assistantwire.CapabilityHealthIntake || c == assistantwire.CapabilityHealthItems || c == assistantwire.CapabilityHealthStrength
	})
}

// healthHost is the core as the Health module reaches it (healthhost.Host).
type healthHost struct{ s *Service }

func (h healthHost) Logf(format string, args ...any) { h.s.logf(format, args...) }

func (h healthHost) Settings() healthhost.Settings {
	if st := h.s.store(); st != nil {
		return st
	}
	return nil
}

func (h healthHost) IntakeStore() healthhost.IntakeStore {
	st, _ := h.s.store().(healthhost.IntakeStore)
	return st
}

func (h healthHost) ImportStore() healthhost.ImportStore {
	st, _ := h.s.store().(healthhost.ImportStore)
	return st
}

func (h healthHost) WaterExtensions() healthhost.WaterExtensions {
	e, _ := h.s.hub.(waterExtensions)
	return e
}

func (h healthHost) Event(ctx context.Context, ev map[string]any) { h.s.event(ctx, ev) }

func (h healthHost) Lang(ctx context.Context) i18n.Lang { return h.s.lang(ctx) }

func (h healthHost) Timezone(ctx context.Context) string { return h.s.settings(ctx).Timezone }

func (h healthHost) Location(ctx context.Context) *time.Location { return h.s.location(ctx) }

func (h healthHost) Standby(ctx context.Context) bool { return h.s.hello(ctx).Assistant.Standby }

func (h healthHost) QuietPush(ctx context.Context) bool { return h.s.hello(ctx).QuietPush }

func (h healthHost) Notify(ctx context.Context, n assistantwire.Notification) (assistantwire.NotifyResult, error) {
	res, err := h.s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n})
	if err == nil {
		h.s.noteCardResult(ctx, cardHealthWater, n, res)
	}
	return res, err
}

func (h healthHost) FitPage(view *wire.View, version int, l i18n.Lang) error {
	slot, _ := extension.Slot("extension_page")
	_, err := extension.Fit(view, slot, version, l)
	return err
}

func (h healthHost) UIVersion() int { return extension.UIVersion }

func (h healthHost) StrengthStore() healthhost.StrengthStore {
	st, _ := h.s.store().(healthhost.StrengthStore)
	return st
}
func (h healthHost) CardShown(ctx context.Context, c assistantwire.StrengthCard) {
	row := db.AssistantViewRow{ID: c.ID, Conversation: c.Conversation, MachineID: c.MachineID, View: c.View, Version: c.Version, CreatedAt: c.At, UpdatedAt: c.At}
	out, err := storedView(row)
	if err != nil {
		h.s.logf("Health card: %v", err)
		return
	}
	body, err := json.Marshal(map[string]any{"view": out})
	if err != nil {
		h.s.logf("Health card: %v", err)
		return
	}
	h.s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": c.Conversation, "item": FeedItem{ID: c.FeedID, Conversation: c.Conversation, At: c.At, Kind: feedView, Ref: c.ID, Body: body}})
}
func (h healthHost) CardChanged(ctx context.Context, c assistantwire.StrengthCard) {
	if c.CreatedAt.IsZero() {
		if st, ok := h.s.store().(viewStore); ok {
			if row, err := st.AssistantView(ctx, c.Conversation, c.ID); err == nil {
				c.CreatedAt = row.CreatedAt
			} else {
				h.s.logf("Health card timestamp: %v", err)
			}
		}
	}

	v, err := wire.ParseAssistantView(c.View)
	if err != nil {
		h.s.logf("Health card: %v", err)
		return
	}
	h.s.event(ctx, map[string]any{"type": "assistant_view", "conversation": c.Conversation, "view": wire.AssistantView{ID: c.ID, Conversation: c.Conversation, Version: c.Version, View: *v, CreatedAt: c.CreatedAt, UpdatedAt: c.At}})
}
func (h healthHost) PageLink(ctx context.Context, ui, state string) string {
	base := h.s.webBase(ctx)
	if base == "" {
		return ""
	}
	return deeplink.Web(base, deeplink.Extension("-health-intake", ui, state))
}

// healthCardAction never enters the generic action outbox or starts an assistant turn.
func (s *Service) healthCardAction(w http.ResponseWriter, r *http.Request, st viewActionStore, conversation, key string, in viewActionInput) {
	if s.healthMod == nil {
		apierr.WriteBody(w, http.StatusNotFound, apierr.Body(http.StatusNotFound, "not_found", i18n.Err("assistant-health_strength.unavailable")))
		return
	}
	if in.ExpectedVersion < 1 || in.Page != "" || len(in.Values) > 0 || (in.Action != "save" && in.Action != "discard") {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_strength.invalid_action"))
		return
	}
	// Settlement receipts must be checked before rejecting the old version carried by a retry.
	row, v, ok := storedCard(w, r, st, conversation, key, 0)
	if !ok {
		return
	}
	if c, known := s.session(r.Context(), row.MachineID, conversation); known && runSpace(c) != SpaceHealth {
		apierr.WriteBody(w, http.StatusForbidden, apierr.Body(http.StatusForbidden, "permission_denied", i18n.Err("assistant-health_strength.not_health")))
		return
	}
	declared := false
	for _, a := range wire.Actions(v) {
		if a.ID == in.Action && a.Reply != "" {
			declared = true
		}
	}
	res, err := s.healthMod.CardAction(r.Context(), healthhost.CardAction{Conversation: conversation, Key: key, Action: in.Action, ClientMsgID: in.ClientMsgID, Version: in.ExpectedVersion, Declared: declared})
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return
	}
	a := db.AssistantViewActionRow{Conversation: conversation, Key: key, Action: res.Action, ClientMsgID: res.ClientMsgID, Values: json.RawMessage(`{}`), Status: "sent", At: res.At, UpdatedAt: res.At, Version: res.Version}
	switch res.Outcome {
	case "done":
		apierr.WriteJSON(w, map[string]any{"action": wireViewAction(a)})
	case "answered":
		writeAlreadyAnswered(w, a)
	case "changed":
		viewChangedNow(w, r, st, conversation, key)
	case "invalid_draft":
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_strength.invalid_draft"))
	case "not_found":
		apierr.WriteBody(w, http.StatusNotFound, apierr.Body(http.StatusNotFound, "not_found", i18n.Err("assistant-health_strength.not_found")))
	default:
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-health_strength.invalid_action"))
	}
}
