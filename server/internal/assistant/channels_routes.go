package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- the app's interface (relayed by the hub, assistantwire.Routes) ----

func (s *Service) channelRoutes() {
	handle := func(pattern string, fn http.HandlerFunc) {
		s.handle(pattern, func(w http.ResponseWriter, r *http.Request) { fn(w, withRequestLang(r)) })
	}
	handle("GET /api/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		list, err := s.channelList(r.Context(), s.integrationsV2On(r.Context()))
		if err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"channels": list})
	})
	handle("GET /api/v1/channels/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok, err := s.channelDetail(r.Context(), r.PathValue("id"))
		switch {
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		case !ok:
			apierr.WriteCode(w, http.StatusNotFound, "not_found", errors.New("no such channel"))
		default:
			apierr.WriteJSON(w, d)
		}
	})
	full := func(f func(w http.ResponseWriter, r *http.Request, v channel.View)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, errors.New("this device can only read and answer"))
				return
			}
			v, ok, err := s.channelView(r.Context(), r.PathValue("id"), s.integrationsV2On(r.Context()))
			switch {
			case err != nil:
				apierr.Write(w, http.StatusBadGateway, err)
				return
			case !ok:
				apierr.WriteCode(w, http.StatusNotFound, "not_found", errors.New("no such channel"))
				return
			case v.ReadOnly || v.Source == channel.SourceMarket:
				apierr.WriteCode(w, http.StatusConflict, "read_only", errors.New(i18n.T(s.botLang(r.Context()), "assistant-channels.not_set_here", i18n.Args{"name": v.Name})))
				return
			}
			f(w, r, v)
		}
	}
	detail := func(w http.ResponseWriter, r *http.Request, id string) {
		s.forgetChannelList()
		d, _, err := s.channelDetail(r.Context(), id)
		if err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		apierr.WriteJSON(w, d)
	}
	handle("PUT /api/v1/channels/{id}", full(func(w http.ResponseWriter, r *http.Request, v channel.View) {
		var in struct {
			Enabled         *bool               `json:"enabled"`
			Target          *channel.Target     `json:"target"`
			ThreadMode      *channel.ThreadMode `json:"thread_mode"`
			ThreadIdleHours *int                `json:"thread_idle_hours"`
			Features        map[string]bool     `json:"features"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.updateChannelFields(r.Context(), v, channelUpdate{
			Enabled:         in.Enabled,
			Target:          in.Target,
			ThreadMode:      in.ThreadMode,
			ThreadIdleHours: in.ThreadIdleHours,
			Features:        in.Features,
		}); err != nil {
			if errors.Is(err, errChannelSettingsWrite) {
				apierr.Write(w, http.StatusServiceUnavailable, errors.New(http.StatusText(http.StatusServiceUnavailable)))
				return
			}
			if errors.Is(err, errChannelsUnread) {
				apierr.Write(w, http.StatusServiceUnavailable, err)
				return
			}
			var re *rpc.Error
			if errors.As(err, &re) && re.Code == assistantwire.CodeNeedsTriageModel {
				apierr.WriteCode(w, http.StatusConflict, re.Code, errors.New(re.Message))
				return
			}
			apierr.WriteCode(w, http.StatusBadRequest, "invalid", err)
			return
		}
		detail(w, r, v.ID)
	}))
	handle("DELETE /api/v1/channels/{id}/binding", full(func(w http.ResponseWriter, r *http.Request, v channel.View) {
		if err := s.appCall(r.Context(), AppChannels, ChannelsUnbind, map[string]any{"channel": v.ID}, nil); err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		s.forgetChannelTurns(v.ID)
		if v.ID == channel.Feishu {
			s.larkModule().Unpaired()
		}
		detail(w, r, v.ID)
	}))
	handle("POST /api/v1/channels/{id}/test", full(func(w http.ResponseWriter, r *http.Request, v channel.View) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		res, err := s.testChannel(ctx, v)
		if err != nil {
			var nc notConnectedError
			if errors.As(err, &nc) {
				apierr.WriteCode(w, http.StatusConflict, "not_connected", errors.New(nc.Error()))
				return
			}
			if msg, ok := cannotSendMessage(err); ok {
				if msg == "" {
					msg = i18n.T(s.botLang(r.Context()), "assistant-channels.cannot_send", i18n.Args{"name": v.Name})
				}
				apierr.WriteCode(w, http.StatusConflict, channel.CodeCannotSend, errors.New(msg))
				return
			}
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		apierr.WriteJSON(w, res)
	}))
}

type channelUpdate struct {
	Enabled         *bool
	Target          *channel.Target
	ThreadMode      *channel.ThreadMode
	ThreadIdleHours *int
	Features        map[string]bool
}

// updateChannel applies the app's changes: only what is given.
func (s *Service) updateChannel(ctx context.Context, v channel.View, enabled *bool, target *channel.Target, features map[string]bool) error {
	return s.updateChannelFields(ctx, v, channelUpdate{Enabled: enabled, Target: target, Features: features})
}

func (s *Service) updateChannelFields(ctx context.Context, v channel.View, in channelUpdate) error {
	enabled, target, threadMode, threadIdleHours, features := in.Enabled, in.Target, in.ThreadMode, in.ThreadIdleHours, in.Features
	l := s.botLang(ctx)
	if target != nil {
		if err := target.Check(); err != nil {
			return err
		}
		if target.Kind == channel.TargetConversation && !s.isAssistantConversation(ctx, target.MachineID, target.SessionID) {
			return errors.New(i18n.T(l, "assistant-channels.assistant_chats_only"))
		}
	}
	if threadMode != nil {
		if err := threadMode.Check(); err != nil {
			return err
		}
	}
	if threadIdleHours != nil {
		if err := channel.CheckThreadIdleHours(*threadIdleHours); err != nil {
			return err
		}
	}
	known := map[string]bool{}
	for _, f := range v.Features {
		known[f.ID] = true
	}
	for id := range features {
		if !known[id] {
			return errors.New(i18n.T(l, "assistant-channels.no_feature", i18n.Args{"name": v.Name, "feature": id}))
		}
	}
	if v.ID == channel.Feishu {
		if threadMode != nil || threadIdleHours != nil {
			return errors.New("thread_mode and thread_idle_hours are only supported for extension channels")
		}
		return s.updateFeishuChannel(ctx, enabled, target, features)
	}
	if v.Source != channel.SourcePlugin {
		return errors.New(i18n.T(l, "assistant-channels.not_set_here", i18n.Args{"name": v.Name}))
	}
	c := &s.chans
	c.mu.Lock()
	s.loadChannelsLocked(ctx)
	if !c.loaded {
		c.mu.Unlock()
		return errChannelsUnread
	}
	if enabled != nil {
		c.mu.Unlock()
		if err := s.appCall(ctx, AppChannels, ChannelsEnable, map[string]any{"channel": v.ID, "on": *enabled}, nil); err != nil {
			return err
		}
		c.mu.Lock()
	}
	defer c.mu.Unlock()
	if target == nil && threadMode == nil && threadIdleHours == nil && len(features) == 0 {
		return nil
	}
	p := c.prefsForUpdate(v.ID)
	if target != nil {
		t := *target
		t.Title = ""
		p.Target = &t
	}
	if threadMode != nil {
		p.ThreadMode = *threadMode
	}
	if threadIdleHours != nil {
		p.ThreadIdleHours = *threadIdleHours
	}
	if len(features) > 0 {
		if p.Features == nil {
			p.Features = maps.Clone(channel.DefaultFeatures)
		} else {
			p.Features = maps.Clone(p.Features)
		}
		maps.Copy(p.Features, features)
		if features[channel.FeatureAllAsks] {
			p.Features[channel.FeatureAsks] = true
		}
	}
	return s.saveChannelsLocked(v.ID, p)
}

type channelTestResult struct {
	OK        bool   `json:"ok"`
	ReplyOnly bool   `json:"reply_only,omitempty"`
	Note      string `json:"note,omitempty"`
}

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

type notConnectedError struct {
	msg string
}

func (e notConnectedError) Error() string { return e.msg }

func cannotSendMessage(err error) (string, bool) {
	var cs cannotSendError
	if errors.As(err, &cs) {
		return sanitizeCannotSendReason(cs.msg), true
	}
	if errors.Is(err, errCannotSend) || errors.Is(err, channel.ErrCannotSend) {
		return sanitizeCannotSendReason(err.Error()), true
	}
	var re *rpc.Error
	if errors.As(err, &re) && (re.Code == CodeCannotSend || re.Code == channel.CodeCannotSend) {
		return sanitizeCannotSendReason(re.Message), true
	}
	return "", false
}

func sanitizeCannotSendReason(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" || msg == errCannotSend.Error() || msg == channel.ErrCannotSend.Error() {
		return ""
	}
	return msg
}

// testChannel sends the owner (only them) a line, to see the channel reaches them; for reply-only channels, it verifies
// connectivity/readiness without invoking proactive channel.send.
func (s *Service) testChannel(ctx context.Context, v channel.View) (channelTestResult, error) {
	l := s.botLang(ctx)
	if !v.Enabled || v.State != channel.StateConnected {
		if v.State == channel.StateError && strings.TrimSpace(v.Error) != "" {
			return channelTestResult{}, notConnectedError{msg: strings.TrimSpace(v.Error)}
		}
		return channelTestResult{}, notConnectedError{msg: i18n.T(l, "assistant-channels.not_connected", i18n.Args{"name": v.Name})}
	}
	if v.Capabilities.ReplyOnly {
		return channelTestResult{
			OK:        true,
			ReplyOnly: true,
			Note:      i18n.T(l, "assistant-channels.reply_only_test_ok", i18n.Args{"name": v.Name}),
		}, nil
	}
	if v.ID == channel.Feishu {
		if err := s.appCall(ctx, AppFeishu, FeishuTest, map[string]any{}, nil); err != nil {
			return channelTestResult{}, err
		}
		return channelTestResult{OK: true}, nil
	}
	if err := s.channelTell(ctx, v.ID, "", i18n.T(l, "assistant-channels.test_message"), channel.OutNote); err != nil {
		return channelTestResult{}, err
	}
	return channelTestResult{OK: true}, nil
}
