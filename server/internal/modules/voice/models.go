package voice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ModelChoice and VoiceChoice are what GET /models and GET /voices list (docs/extensions/extensions.md "Voice").
type ModelChoice struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type VoiceChoice struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Gender    string   `json:"gender,omitempty"`
	Languages []string `json:"languages,omitempty"`
}

type voiceList struct {
	Voices  []VoiceChoice `json:"voices"`
	Default string        `json:"default,omitempty"`
	// Stale: the service's list could not be read just now; this is the last one it gave, or the dialect's built-in
	// list when it never gave one (#2411). The app asks again later.
	Stale bool `json:"stale,omitempty"`
}

// The voices Codex offers gpt-live-1-codex (codex-rs RealtimeVoicesList v3), cove its default.
var codexVoices = []string{"juniper", "maple", "spruce", "ember", "vale", "breeze", "arbor", "sol", "cove", "aube", "bubbie",
	"haetsal", "hanul", "himari", "leher", "miko", "neer", "porto", "rindo", "rio", "rivage", "selva", "sonna", "tinta", "viola"}

// The built-in voices of the public GPT-Live API, marin its default.
var liveVoices = []string{"alloy", "ash", "ballad", "beacon", "bossa", "brise", "cedar", "cinder", "coral", "delta", "echo",
	"flitz", "gleam", "harema", "juni", "marin", "meridian", "nira", "noeul", "nuri", "quartz", "ripple", "sage", "shida",
	"shimmer", "sillage", "stone", "tempo", "verse", "vesper", "willow"}

// catalog lists models and voices across the providers; the services' own lists are kept 10 minutes, and the last
// good one stays as the answer while a service's list cannot be read (#2411).
type catalog struct {
	cfg  *Config
	now  func() time.Time
	logf func(string, ...any) // nil: say nothing

	mu      sync.Mutex
	cache   map[string]cached
	failing map[string]bool // keys whose last read failed: logged once, until one succeeds again
}

type cached struct {
	at    time.Time
	value any
}

const catalogTTL = 10 * time.Minute

func (c *catalog) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// getOrLast is key's value, loaded again once it is 10 minutes old. When loading fails it falls back to the last good
// value: (that value, true, the error); with none, the error alone. Only good values are kept, and a failure is logged
// once until a load succeeds again.
func (c *catalog) getOrLast(key string, load func() (any, error)) (v any, old bool, err error) {
	c.mu.Lock()
	hit, ok := c.cache[key]
	c.mu.Unlock()
	if ok && c.clock().Sub(hit.at) < catalogTTL {
		return hit.value, false, nil
	}
	v, err = load()
	c.mu.Lock()
	if err != nil {
		first := !c.failing[key]
		if first {
			if c.failing == nil {
				c.failing = map[string]bool{}
			}
			c.failing[key] = true
		}
		c.mu.Unlock()
		if first && c.logf != nil {
			instead := ""
			if ok {
				instead = "; the last good list stands in"
			}
			c.logf("voice catalog: reading %s failed: %s%s (logged once until it reads again)", key, fetchFailure(err), instead)
		}
		if ok {
			return hit.value, true, err
		}
		return nil, false, err
	}
	delete(c.failing, key)
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[key] = cached{at: c.clock(), value: v}
	c.mu.Unlock()
	return v, false, nil
}

// last is the last good value kept under key, however old.
func (c *catalog) last(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.cache[key]
	return hit.value, ok
}

func (c *catalog) provider(id string) *Provider {
	for i := range c.cfg.Providers {
		if c.cfg.Providers[i].ID == id {
			return &c.cfg.Providers[i]
		}
	}
	return nil
}

type providerModels struct {
	models []ModelChoice // ids without the provider prefix
	def    string
}

// providerModels: the configured models, else the service's own list (live dialect), else the dialect's default.
func (c *catalog) providerModels(ctx context.Context, p *Provider) (providerModels, error) {
	if len(p.Models) > 0 || p.Dialect == DialectCodex || p.Dialect == DialectRealtime || p.Dialect == DialectFake {
		ids := p.Models
		if len(ids) == 0 {
			if p.Dialect == DialectRealtime {
				ids = []string{"gpt-realtime", "gpt-realtime-mini"}
			} else {
				ids = []string{p.defaultModel()}
			}
		}
		out := providerModels{def: p.defaultModel()}
		for _, id := range ids {
			out.models = append(out.models, ModelChoice{ID: id})
		}
		return out, nil
	}
	// The last good list while the service's cannot be read: a call (and /voices) still finds its default model.
	v, _, err := c.getOrLast("models:"+p.ID, func() (any, error) {
		var res struct {
			Models []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
				DisplaySnek string `json:"display_name"`
			} `json:"models"`
		}
		if err := getJSON(ctx, p, "/models", nil, &res); err != nil {
			return nil, err
		}
		out := providerModels{def: p.defaultModel()}
		for _, m := range res.Models {
			if !modelPattern.MatchString(m.ID) {
				continue
			}
			name := firstNonEmpty(m.Name, m.DisplayName, m.DisplaySnek)
			if name == m.ID {
				name = ""
			}
			out.models = append(out.models, ModelChoice{ID: m.ID, Name: name})
		}
		if out.def == "" && len(out.models) > 0 {
			out.def = out.models[0].ID
		}
		return out, nil
	})
	if err != nil {
		return providerModels{}, err
	}
	return v.(providerModels), nil
}

// models is GET /models: every provider's models as "<provider>/<model>". A provider whose list cannot be read is
// left out (and logged) rather than failing the others.
func (c *catalog) models(ctx context.Context, logf func(string, ...any)) ([]ModelChoice, string, error) {
	out := []ModelChoice{} // none while no upstream is set up: an empty list, not null
	var firstErr error
	def := ""
	several := len(c.cfg.Providers) > 1
	for i := range c.cfg.Providers {
		p := &c.cfg.Providers[i]
		pm, err := c.providerModels(ctx, p)
		if err != nil {
			logf("models: %s: %s", p.ID, fetchFailure(err))
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p.name(), err)
			}
			continue
		}
		for _, m := range pm.models {
			name := firstNonEmpty(m.Name, m.ID)
			if several {
				name += " · " + p.name()
			}
			out = append(out, ModelChoice{ID: p.ID + "/" + m.ID, Name: name})
		}
		if def == "" && pm.def != "" {
			def = p.ID + "/" + pm.def
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, "", firstErr
	}
	if c.cfg.Default != "" {
		def = c.cfg.Default
	}
	return out, def, nil
}

// resolve turns the app's model ("<provider>/<model>", or empty for the default) into a provider and its model.
func (c *catalog) resolve(ctx context.Context, id string) (*Provider, string, error) {
	if len(c.cfg.Providers) == 0 {
		return nil, "", errNoUpstream
	}
	if id == "" {
		id = c.cfg.Default
	}
	if id == "" {
		p := &c.cfg.Providers[0]
		pm, err := c.providerModels(ctx, p)
		if err != nil {
			return nil, "", err
		}
		if pm.def == "" {
			return nil, "", fmt.Errorf("%s: no model to use (set default_model)", p.name())
		}
		return p, pm.def, nil
	}
	pid, model, ok := splitModel(id)
	if !ok {
		return nil, "", fmt.Errorf("model %q is not <provider>/<model>", id)
	}
	p := c.provider(pid)
	if p == nil {
		return nil, "", fmt.Errorf("%w: %s", errNoProvider, pid)
	}
	return p, model, nil
}

// voices is GET /voices?model=: when a streaming TTS endpoint is configured, its voice list (GET <voices URL>,
// #17843); otherwise the realtime service's list (live dialect, GET <url>/voices), else the dialect's built-in one.
// While the service's list cannot be read it answers with the last good one for that model, else the built-in one,
// marked Stale (#2411): a call can still pick a voice, and the app asks again later. Only a model id that names no
// provider, or no upstream at all, is an error.
func (c *catalog) voices(ctx context.Context, id string) (voiceList, error) {
	if tts := c.cfg.TTS(); tts.Configured() {
		return c.ttsVoices(ctx, tts), nil
	}
	p, model, err := c.resolve(ctx, id)
	if err != nil {
		if id != "" || errors.Is(err, errNoUpstream) || errors.Is(err, errNoProvider) {
			return voiceList{}, err
		}
		// The default model could not be found (the service's model list was never read): the first upstream's voices
		// all the same, the last good list of any of its models, else its dialect's.
		p = &c.cfg.Providers[0]
		if v, ok := c.last(lastVoicesKey(p.ID)); ok {
			return stale(v.(voiceList)), nil
		}
		return stale(builtinVoices(p.Dialect)), nil
	}
	switch p.Dialect {
	case DialectCodex, DialectRealtime, DialectFake:
		return builtinVoices(p.Dialect), nil
	}
	key := "voices:" + p.ID + "/" + model
	v, wasStale, err := c.getOrLast(key, func() (any, error) {
		var res struct {
			Voices       []VoiceChoice `json:"voices"`
			DefaultVoice string        `json:"defaultVoice"`
			Default      string        `json:"default_voice"`
		}
		if err := getJSON(ctx, p, "/voices", url.Values{"model": {model}}, &res); err != nil {
			var status *httpStatusError
			if errors.As(err, &status) && status.noSuchEndpoint() {
				// A service without a voices list: the public API's built-in voices, as good as any list it gives.
				return builtinVoices(p.Dialect), nil
			}
			return nil, err
		}
		out := voiceList{Default: firstNonEmpty(res.DefaultVoice, res.Default), Voices: []VoiceChoice{}}
		for _, v := range res.Voices {
			if voicePattern.MatchString(v.ID) {
				out.Voices = append(out.Voices, v)
			}
		}
		return out, nil
	})
	if err != nil && v == nil {
		return stale(builtinVoices(p.Dialect)), nil
	}
	list := v.(voiceList)
	if wasStale {
		return stale(list), nil
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[lastVoicesKey(p.ID)] = cached{at: c.clock(), value: list} // for when its default model cannot be found
	c.mu.Unlock()
	return list, nil
}

// lastVoicesKey keeps the latest list a provider gave for any of its models.
func lastVoicesKey(provider string) string { return "voices-latest:" + provider }

const ttsVoicesCacheKey = "voices:tts"

// ttsVoicesTimeout bounds GET <voices URL> to the TTS endpoint so a slow or unreachable endpoint does not stall the app.
var ttsVoicesTimeout = 5 * time.Second

// ttsVoices reads the configured TTS endpoint's optional GET .../v1/audio/voices list (#17843), cached for catalogTTL
// (10 minutes). A 404/405/501 means the endpoint has no voice list (default voice only, not stale); any other failure
// serves the last good list marked stale, or the default voice alone marked stale when no list was ever read.
func (c *catalog) ttsVoices(ctx context.Context, tts TTSEndpoint) voiceList {
	v, wasStale, err := c.getOrLast(ttsVoicesCacheKey, func() (any, error) {
		raw, err := getTTSVoices(ctx, tts)
		if err != nil {
			var status *httpStatusError
			if errors.As(err, &status) && status.noSuchEndpoint() {
				return ttsDefaultOnly(tts.DefaultVoice()), nil
			}
			return nil, err
		}
		return parseTTSVoices(raw, tts.DefaultVoice())
	})
	if err != nil && v == nil {
		return stale(ttsDefaultOnly(tts.DefaultVoice()))
	}
	list := v.(voiceList)
	if wasStale {
		return stale(list)
	}
	return list
}

func ttsDefaultOnly(defaultVoice string) voiceList {
	if defaultVoice == "" {
		return voiceList{Voices: []VoiceChoice{}}
	}
	return voiceList{Voices: []VoiceChoice{{ID: defaultVoice}}, Default: defaultVoice}
}

func getTTSVoices(ctx context.Context, tts TTSEndpoint) ([]byte, error) {
	u, err := url.Parse(tts.VoicesURL())
	if err != nil {
		return nil, errors.New("the TTS voices address does not parse")
	}
	ctx, cancel := context.WithTimeout(ctx, ttsVoicesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tts.VoicesURL(), nil)
	if err != nil {
		return nil, err
	}
	if key := tts.Key(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := directClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, &httpStatusError{path: u.Path, status: res.StatusCode, body: clipRunes(string(raw), 200)}
	}
	return raw, nil
}

// parseTTSVoices decodes either {"voices":[{"id","name","languages"}],"default":...} or {"voices":["a","b"]}.
// defaultVoice (tts_default_voice) is always included and marked default when set; otherwise the endpoint's default
// wins, else the first voice.
func parseTTSVoices(raw []byte, defaultVoice string) (voiceList, error) {
	var res struct {
		Voices       []json.RawMessage `json:"voices"`
		Default      string            `json:"default"`
		DefaultVoice string            `json:"defaultVoice"`
		DefaultSnek  string            `json:"default_voice"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return voiceList{}, err
	}
	out := voiceList{Voices: []VoiceChoice{}}
	seen := map[string]bool{}
	for _, item := range res.Voices {
		var v VoiceChoice
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			v.ID = strings.TrimSpace(s)
		} else if err := json.Unmarshal(item, &v); err == nil {
			v.ID = strings.TrimSpace(v.ID)
		} else {
			continue
		}
		if !voicePattern.MatchString(v.ID) || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out.Voices = append(out.Voices, v)
	}
	epDefault := strings.TrimSpace(firstNonEmpty(res.Default, res.DefaultVoice, res.DefaultSnek))
	if epDefault != "" && !voicePattern.MatchString(epDefault) {
		epDefault = ""
	}
	switch {
	case defaultVoice != "":
		if !seen[defaultVoice] {
			out.Voices = append([]VoiceChoice{{ID: defaultVoice}}, out.Voices...)
		}
		out.Default = defaultVoice
	case epDefault != "":
		if !seen[epDefault] {
			out.Voices = append([]VoiceChoice{{ID: epDefault}}, out.Voices...)
		}
		out.Default = epDefault
	case len(out.Voices) > 0:
		out.Default = out.Voices[0].ID
	}
	return out, nil
}

// builtinVoices is a dialect's own list: Codex's voices for gpt-live-1-codex (cove), none for the fake one (it speaks
// no audio), else the public GPT-Live API's (marin).
func builtinVoices(dialect string) voiceList {
	ids, def := liveVoices, "marin"
	switch dialect {
	case DialectCodex:
		ids, def = codexVoices, "cove"
	case DialectFake:
		return voiceList{Voices: []VoiceChoice{}}
	}
	out := voiceList{Default: def, Voices: make([]VoiceChoice, 0, len(ids))}
	for _, v := range ids {
		out.Voices = append(out.Voices, VoiceChoice{ID: v, Name: strings.ToUpper(v[:1]) + v[1:]})
	}
	return out
}

func stale(v voiceList) voiceList {
	v.Stale = true
	return v
}

// httpStatusError is a service's answer other than 200 to a GET.
type httpStatusError struct {
	path   string
	status int
	body   string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("GET %s: HTTP %d %s", e.path, e.status, e.body)
}

// noSuchEndpoint: the service has no such list (not a passing failure).
func (e *httpStatusError) noSuchEndpoint() bool {
	return e.status == http.StatusNotFound || e.status == http.StatusMethodNotAllowed || e.status == http.StatusNotImplemented
}

// errCredential: the provider's key_file or key_command gave no credential.
var errCredential = errors.New("no credential")

// fetchFailure says why a list could not be read, for the log: never the credential, the key command's output, the
// service's answer or its address (which may carry a key).
func fetchFailure(err error) string {
	var status *httpStatusError
	var uerr *url.Error
	switch {
	case errors.As(err, &status):
		return fmt.Sprintf("HTTP %d", status.status)
	case errors.Is(err, errCredential):
		return "its credential could not be read (key_file / key_command)"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.As(err, &uerr):
		return uerr.Op + ": " + clipRunes(uerr.Err.Error(), 200)
	}
	return clipRunes(err.Error(), 200)
}

// getJSON GETs <the provider's url>/<path> over HTTPS with its credential.
func getJSON(ctx context.Context, p *Provider, path string, q url.Values, out any) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return errors.New("the service address does not parse")
	}
	u.Scheme = strings.Replace(strings.Replace(u.Scheme, "wss", "https", 1), "ws", "http", 1)
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	key, err := p.credential(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errCredential, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := p.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return &httpStatusError{path: u.Path, status: res.StatusCode, body: clipRunes(string(raw), 200)}
	}
	return json.Unmarshal(raw, out)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
