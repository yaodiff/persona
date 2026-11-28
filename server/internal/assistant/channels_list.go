package assistant

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the channels as the hub has them ----

// channelsNow is the hub's list of the channels (channels.list), kept a few seconds: for what looks often (a run's
// sink every second, every card and turn's end).
func (s *Service) channelsNow(ctx context.Context) ([]channel.View, error) {
	c := &s.chans
	c.mu.Lock()
	if c.list != nil && time.Since(c.listAt) < 3*time.Second {
		out := c.list
		c.mu.Unlock()
		return out, nil
	}
	c.mu.Unlock()
	return s.channelsFresh(ctx)
}

// channelsFresh is the hub's list of the channels now (the app's pages: a switch just changed shows at once).
func (s *Service) channelsFresh(ctx context.Context) ([]channel.View, error) {
	c := &s.chans
	var res struct {
		Channels        []channel.View                     `json:"channels"`
		TargetOverrides map[string]*channel.TargetOverride `json:"target_overrides"`
	}
	if err := s.appCall(ctx, AppChannels, ChannelsList, map[string]any{}, &res); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.list, c.listAt = res.Channels, time.Now()
	c.overrides = res.TargetOverrides
	c.hasOverrides = res.TargetOverrides != nil
	c.mu.Unlock()
	return res.Channels, nil
}

// forgetChannelList: a switch or binding changed: the next look asks the hub again.
func (s *Service) forgetChannelList() {
	s.chans.mu.Lock()
	s.chans.list = nil
	s.chans.overrides = nil
	s.chans.hasOverrides = false
	s.chans.mu.Unlock()
}

// pluginChannel is channel id when a plugin contributes it (false when there is no such channel now, or the hub
// could not be asked).
func (s *Service) pluginChannel(ctx context.Context, id string) (channel.View, bool) {
	v, ok, _ := s.pluginChannelNow(ctx, id)
	return v, ok
}

// pluginChannelNow is pluginChannel telling the hub not answering (an error) from no such channel (false).
func (s *Service) pluginChannelNow(ctx context.Context, id string) (channel.View, bool, error) {
	list, err := s.channelsNow(ctx)
	if err != nil {
		return channel.View{}, false, err
	}
	for _, v := range list {
		if v.ID == id && v.Source == channel.SourcePlugin && (v.Kind == channel.KindIM || v.Kind == channel.KindMention) {
			return v, true, nil
		}
	}
	return channel.View{}, false, nil
}

// botLang is the language of what the bots and the channels write: the request's when the context carries one (the
// app's request, withRequestLang), else the user's (docs/protocol/i18n.md "Protocol"), zh-Hans while no client has said one.
func (s *Service) botLang(ctx context.Context) i18n.Lang {
	if l := i18n.From(ctx); l != "" {
		return l
	}
	return i18n.Lang(s.hello(ctx).Locale).Or(i18n.Default)
}

// withRequestLang is r with its Accept-Language (the hub relays the app's headers) in its context, for botLang.
func withRequestLang(r *http.Request) *http.Request {
	if l := i18n.FromRequest(r); l != "" {
		return r.WithContext(i18n.With(r.Context(), l))
	}
	return r
}

// channelList is every channel as the app sees it: the hub's list with what is the assistant's filled in.
func (s *Service) channelList(ctx context.Context, v2 bool) ([]channel.View, error) {
	list, err := s.channelsFresh(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]channel.View, len(list))
	for i, v := range list {
		out[i] = s.filledChannel(ctx, v, v2)
	}
	return out, nil
}

// filledChannel is v with its target, and a plugin channel's features.
func (s *Service) filledChannel(ctx context.Context, v channel.View, v2 bool) channel.View {
	switch {
	case v.ID == channel.Feishu:
		t := s.feishuChannelTarget(ctx)
		v.Target = &t
	case v.Source == channel.SourcePlugin && (v.Kind == channel.KindIM || v.Kind == channel.KindMention) && v.Installed:
		c := &s.chans
		c.mu.Lock()
		s.loadChannelsLocked(ctx)
		pref := s.channelPrefLocked(v.ID)
		t := channel.Target{Kind: channel.TargetMain}
		if v.Kind == channel.KindMention {
			t.Kind = channel.TargetNew
		}
		if pref.Target != nil {
			t = *pref.Target
		}
		threadMode := channelRoutingMode(v, pref.ThreadMode, v2)
		threadIdleHours := pref.ThreadIdleHours
		features := channel.PluginFeatures(s.botLang(ctx), v.Capabilities, pref.Features)
		c.mu.Unlock()
		if t.Kind == channel.TargetConversation && t.Title == "" {
			t.Title = s.conversationTitle(ctx, t.MachineID, t.SessionID)
		}
		v.Target = &t
		v.ThreadMode = threadMode
		v.ThreadIdleHours = threadIdleHours
		if v.Kind == channel.KindIM {
			v.Features = features
		}
	}
	return v
}

// conversationTitle is how a conversation is called in the channel's settings.
func (s *Service) conversationTitle(ctx context.Context, machine, session string) string {
	c, ok := s.session(ctx, machine, session)
	if !ok {
		return ""
	}
	return conversationName(c, s.settings(ctx).Main)
}

func (s *Service) channelView(ctx context.Context, id string, v2 bool) (channel.View, bool, error) {
	list, err := s.channelList(ctx, v2)
	if err != nil {
		return channel.View{}, false, err
	}
	for _, v := range list {
		if v.ID == id {
			return v, true, nil
		}
	}
	return channel.View{}, false, nil
}

func (s *Service) channelDetail(ctx context.Context, id string) (channel.Detail, bool, error) {
	v2 := s.integrationsV2On(ctx)
	v, ok, err := s.channelView(ctx, id, v2)
	if err != nil || !ok {
		return channel.Detail{}, ok, err
	}
	d := channel.Detail{View: v}
	switch id {
	case channel.Feishu:
		d.SettingsURL = "settings/connectors/feishu-bot"
		d.Waiting = s.channelWaiting(id)
		d.Waiting.Asks = s.larkModule().CardsWaiting()
	case channel.App:
	default:
		if v.Source == channel.SourcePlugin && v.Plugin != "" {
			d.SettingsURL = "settings/plugins/" + v.Plugin
		}
		d.Waiting = s.channelWaiting(id)
	}
	c := &s.chans
	c.mu.Lock()
	s.loadChannelsLocked(ctx)
	if p := c.prefs[id]; p != nil {
		d.Conversations = slices.Clone(p.Recent)
	}
	ov, hasOverrides := c.overrides[id], c.hasOverrides
	c.mu.Unlock()
	// A reported map without an override for this channel explicitly suppresses the hint.
	if !d.ReadOnly && d.Source != channel.SourceMarket && d.Target != nil && (!hasOverrides || ov != nil) {
		if v.Source == channel.SourcePlugin && v.Installed && v2 {
			switch d.ThreadMode {
			case channel.ThreadModePerGroupAndTopic:
				d.TargetOverride = &channel.TargetOverride{Mode: channel.TargetOverrideGroupsAndTopics}
			case channel.ThreadModePerTopic:
				d.TargetOverride = &channel.TargetOverride{Mode: channel.TargetOverrideTopics}
			}
		} else {
			switch {
			case ov != nil:
				cp := *ov
				d.TargetOverride = &cp
			case v.Source == channel.SourcePlugin && v.Installed && v.Capabilities.Threads:
				d.TargetOverride = &channel.TargetOverride{Mode: channel.TargetOverrideThreads}
			}
		}
	}
	for i := range d.Conversations {
		if t := s.conversationTitle(ctx, d.Conversations[i].MachineID, d.Conversations[i].SessionID); t != "" {
			d.Conversations[i].Title = t
		}
	}
	return d, true, nil
}

// isAssistantConversation: session on machine is one of the assistant's conversations, as the hub lists it.
func (s *Service) isAssistantConversation(ctx context.Context, machine, session string) bool {
	list, err := s.sessions(ctx, machine)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(list, func(c proto.Session) bool { return c.SessionID == session && c.Assistant })
}
