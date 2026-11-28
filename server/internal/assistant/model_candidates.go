package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Candidate models for model.complete (#10398, docs/extensions/extensions.md "Model calls"). A call may name models
// to try in order; one that fails, runs out of time, answers with nothing or does not match json_schema hands over
// to the next. The extension only names models: each runs on a provider the user configured in Settings › Model
// providers that offers it, and the user keeps the extension's lists, switches them off or replaces them with a list
// of their own on the extension's purpose in Settings › Model configuration (candidateSetting).

// Candidate modes of an extension's purpose.
const (
	candidatesExtension = "extension" // the lists the extension sends (the default)
	candidatesOff       = "off"       // ignore them: every call runs on the purpose's model
	candidatesCustom    = "custom"    // the user's own list, for every call
)

// candidatesRequestedMost is how many of an extension's lists the hub remembers for the user to see.
const candidatesRequestedMost = 8

// candidatesRequestedRefresh is how stale a remembered list's last use may get before a call writes it again.
const candidatesRequestedRefresh = time.Hour

// candidateSetting is what the user decided about an extension's candidates, stored on its purpose's record.
type candidateSetting struct {
	Mode   string         `json:"mode"`
	Custom []ModelBinding `json:"custom,omitempty"` // provider and model (empty: the provider's default)
}

// ModelCandidates is an extension purpose's candidates as Settings › Model configuration shows them.
type ModelCandidates struct {
	Mode      string           `json:"mode"`
	Custom    []CandidateView  `json:"custom"`    // the user's list (mode custom)
	Requested []RequestedChain `json:"requested"` // the lists the extension sent lately, newest first
	Modes     []string         `json:"modes"`     // the modes the hub takes
	Most      int              `json:"most"`      // how many candidates a list may have

	DirectProviderIDs []string `json:"direct_provider_ids"` // callable providers permitted for this purpose
}

// CandidateView is one candidate and where it would run now: Provider is empty and Available false when no provider
// the user configured offers it (or the user's pick cannot be called directly).
type CandidateView struct {
	Model        string `json:"model"`
	Provider     string `json:"provider,omitempty"`
	ProviderName string `json:"provider_name,omitempty"`
	Available    bool   `json:"available"`
	Problem      string `json:"problem,omitempty"`

	// Custom rows retain the exact setting. An empty setting uses the provider's current default model.
	ConfiguredModel *string `json:"configured_model,omitempty"`
}

// RequestedChain is one list the extension sent, with when it last did.
type RequestedChain struct {
	Models   []CandidateView `json:"models"`
	LastUsed time.Time       `json:"last_used"`
}

// requestedChain is how a list the extension sent is stored (model_candidates.requested.<id>).
type requestedChain struct {
	Models   []string  `json:"models"`
	LastUsed time.Time `json:"last_used"`
}

func requestedKey(purpose string) string { return "model_candidates.requested." + purpose }

// modelCandidate is one candidate of a call: what to call it, and the connection, or why there is none.
type modelCandidate struct {
	model    string // as the extension (or the user) named it
	provider string // the provider's name, for the log line
	id       string // the provider's id (launcher.<name>)
	config   ai.Config
	problem  string // not "": it cannot be called
}

// candidateSettingOf is purpose's stored setting; nil for every purpose but an extension's, and for none stored.
func (s *Service) candidateSettingOf(ctx context.Context, purpose string) *candidateSetting {
	if !isExtensionPurpose(purpose) {
		return nil
	}
	r, err := s.modelRecord(ctx, purpose)
	if err != nil {
		s.logf("assistant: candidates of %s: %v", purpose, err)
		return nil
	}
	return r.Candidates
}

// modelCandidates is the chain a call on purpose tries, models being what the call sent; nil runs it on the
// purpose's model exactly as without candidates.
func (s *Service) modelCandidates(ctx context.Context, purpose string, models []string) []modelCandidate {
	setting := s.candidateSettingOf(ctx, purpose)
	mode := candidatesExtension
	if setting != nil && setting.Mode != "" {
		mode = setting.Mode
	}
	if len(models) > 0 && isExtensionPurpose(purpose) {
		s.noteRequested(ctx, purpose, models) // seen in Settings whatever the mode
	}
	switch mode {
	case candidatesOff:
		return nil
	case candidatesCustom:
		chain := make([]modelCandidate, 0, len(setting.Custom))
		for _, b := range setting.Custom {
			chain = append(chain, s.pickedCandidate(ctx, b, ""))
		}
		return chain
	}
	if len(models) == 0 {
		return nil
	}
	var own ModelBinding
	if b, err := s.resolveModel(ctx, purpose); err == nil {
		own = b
	}
	chain := make([]modelCandidate, 0, len(models))
	for _, m := range models {
		chain = append(chain, s.offeredCandidate(ctx, own, m, ""))
	}
	return chain
}

// candidateProviders are the managed providers the hub can call directly, in the order a named model is looked
// for: first (the purpose's own provider) when it is one, then direct API providers, then keyed Messages launchers.
// Hidden ones and ones without an address or a key are left out.
func (s *Service) candidateProviders(ctx context.Context, first string) []storedLauncher {
	hidden := s.hiddenLaunchers(ctx)
	var api, launchers []storedLauncher
	var own *storedLauncher
	for _, l := range s.storedLaunchers(ctx) {
		if proto.IsExternalLauncher(l.Name) || slices.Contains(hidden, l.Name) || l.BaseURL == "" || l.KeySealed == "" {
			continue
		}
		if l.Kind != "api" && !messagesAPI(l.API) {
			continue
		}
		if "launcher."+l.Name == first {
			own = &l
			continue
		}
		if l.Kind == "api" {
			api = append(api, l)
		} else {
			launchers = append(launchers, l)
		}
	}
	out := make([]storedLauncher, 0, len(api)+len(launchers)+1)
	if own != nil {
		out = append(out, *own)
	}
	return append(append(out, api...), launchers...)
}

// providerOffers says whether l lists model: its default, a tier's, its small, subagent or custom model, or one of
// its model list.
func providerOffers(l storedLauncher, model string) bool {
	for _, m := range []string{l.Model, l.SmallModel, l.SubagentModel, l.CustomModel, l.OpusModel, l.SonnetModel, l.HaikuModel, l.FableModel} {
		if m == model {
			return true
		}
	}
	return slices.ContainsFunc(l.Models, func(m proto.ProviderModel) bool { return m.ID == model })
}

func providerLabel(l storedLauncher) string { return cmp.Or(l.DisplayName, l.Name) }

// offeredCandidate finds model on the providers the user configured, own being the purpose's binding (#10657):
//  1. a provider that offers it: the purpose's own when it lists it or the purpose is bound to it there, then the
//     others that list it (candidateProviders' order);
//  2. else the purpose's own provider when it declares no models list: it has not said what it serves, so the
//     gateway is asked, and one that does not serve it fails like any candidate does.
//
// Any other provider without a list is not tried: an extension's model never lands on a connection the user did not
// choose for it.
func (s *Service) offeredCandidate(ctx context.Context, own ModelBinding, model string, lang i18n.Lang) modelCandidate {
	var problem string
	providers := s.candidateProviders(ctx, own.Provider)
	try := func(l storedLauncher) (modelCandidate, bool) {
		config, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher." + l.Name, Model: model})
		if err != nil || config.RunLauncher != "" || !config.Configured() {
			if err != nil {
				problem = providerLabel(l) + ": " + i18n.Localize(err, lang.Or(s.lang(ctx)))
			}
			return modelCandidate{}, false
		}
		return modelCandidate{model: model, provider: providerLabel(l), id: "launcher." + l.Name, config: config}, true
	}
	isOwn := func(l storedLauncher) bool { return own.Provider != "" && "launcher."+l.Name == own.Provider }
	for _, l := range providers {
		if providerOffers(l, model) || isOwn(l) && model == own.Model {
			if c, ok := try(l); ok {
				return c
			}
		}
	}
	for _, l := range providers {
		if isOwn(l) && len(l.Models) == 0 {
			if c, ok := try(l); ok {
				return c
			}
		}
	}
	if problem == "" {
		problem = mc(lang.Or(i18n.En), "candidate_not_offered")
	}
	return modelCandidate{model: model, problem: problem}
}

// pickedCandidate is a candidate the user picked in Settings: their provider, and its default model when b has none.
func (s *Service) pickedCandidate(ctx context.Context, b ModelBinding, lang i18n.Lang) modelCandidate {
	name := strings.TrimPrefix(b.Provider, "launcher.")
	for _, l := range s.candidateProviders(ctx, "") {
		if l.Name != name {
			continue
		}
		config, err := s.apiConfig(ctx, b)
		model := cmp.Or(b.Model, config.Model, l.Model)
		switch {
		case err != nil:
			return modelCandidate{model: model, id: b.Provider, problem: providerLabel(l) + ": " + i18n.Localize(err, lang.Or(s.lang(ctx)))}
		case config.RunLauncher != "" || !config.Configured():
			return modelCandidate{model: model, id: b.Provider, problem: mc(lang.Or(i18n.En), "candidate_not_direct", i18n.Args{"provider": providerLabel(l)})}
		}
		return modelCandidate{model: model, provider: providerLabel(l), id: "launcher." + l.Name, config: config}
	}
	return modelCandidate{model: cmp.Or(b.Model, name), id: b.Provider, problem: mc(lang.Or(i18n.En), "candidate_provider_gone", i18n.Args{"provider": name})}
}

// runCandidates makes c on each of chain in turn until one answers with something (that matches the schema, with
// one). Each has c's timeout, the whole call at most modelCallTimeoutMost.
func (s *Service) runCandidates(ctx context.Context, purpose, who string, c checkedCall, chain []modelCandidate) (assistantwire.ModelCompleteResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, modelCallTimeoutMost)
	defer cancel()
	deadline, _ := callCtx.Deadline()
	var usage assistantwire.ModelUsage
	var attempts []assistantwire.ModelAttempt
	started := time.Now()
	for i, cand := range chain {
		attemptStarted := time.Now()
		before := usage
		var out assistantwire.ModelCompleteResult
		var outcome string
		var err error
		left := time.Until(deadline)
		switch {
		case cand.problem != "":
			outcome = "unavailable"
			data, _ := json.Marshal(assistantwire.ModelNotOffered{Model: cand.model})
			err = &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: " + cand.model + ": " + cand.problem, Data: data}
		case ctx.Err() != nil:
			outcome = "not tried"
			err = &rpc.Error{Code: assistantwire.CodeModelTimeout, Message: "model.complete: the call was cancelled"}
		case left <= 0:
			outcome = "not tried"
			err = &rpc.Error{Code: assistantwire.CodeModelTimeout, Message: "model.complete: no time left for it"}
		default:
			out, outcome, err = s.attemptModel(callCtx, s.countedClient(purpose, "", cand.id, cand.config), c, min(c.timeout, left), &usage)
			if err == nil && c.schema == nil && strings.TrimSpace(out.Text) == "" {
				outcome = "empty"
				err = &rpc.Error{Code: assistantwire.CodeModelFailed, Message: "model.complete: the reply was empty"}
			}
		}
		s.logf("assistant: %s model call%s candidate %d/%d %s via %s: %s in %s, %d request(s), %d input + %d output tokens",
			who, labelPart(c.p.Label), i+1, len(chain), cand.model, cmp.Or(cand.provider, "-"), outcome,
			time.Since(attemptStarted).Round(time.Millisecond), usage.Requests-before.Requests,
			usage.InputTokens-before.InputTokens, usage.OutputTokens-before.OutputTokens)
		if err == nil {
			out.Usage, out.Candidate, out.Attempts = usage, cand.model, attempts
			s.logf("assistant: %s model call%s answered by candidate %s (%s) after %d failed, in %s, %d request(s), %d input + %d output tokens (cache read %d, written %d)",
				who, labelPart(c.p.Label), cand.model, out.Model, len(attempts), time.Since(started).Round(time.Millisecond), usage.Requests,
				usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
			return out, nil
		}
		a := attemptOf(cand.model, err)
		a.Seconds = time.Since(attemptStarted).Round(time.Millisecond).Seconds()
		a.Usage = usageSince(before, usage)
		attempts = append(attempts, a)
		if len(chain) == 1 { // one candidate fails as a call without candidates would: its own code and data
			return assistantwire.ModelCompleteResult{}, err
		}
	}
	parts := make([]string, len(attempts))
	for i, a := range attempts {
		parts[i] = a.Model + ": " + a.Code + " (" + strings.TrimPrefix(a.Message, "model.complete: ") + ")"
	}
	s.logf("assistant: %s model call%s: every candidate failed in %s, %d request(s), %d input + %d output tokens: %s",
		who, labelPart(c.p.Label), time.Since(started).Round(time.Millisecond), usage.Requests, usage.InputTokens, usage.OutputTokens,
		strings.Join(parts, "; "))
	data, _ := json.Marshal(assistantwire.ModelCandidatesFailed{Attempts: attempts, Usage: usage})
	return assistantwire.ModelCompleteResult{}, &rpc.Error{Code: assistantwire.CodeModelCandidatesFailed, Message: "model.complete: every candidate failed: " + strings.Join(parts, "; "), Data: data}
}

// usageSince is what was spent from before to now.
func usageSince(before, now assistantwire.ModelUsage) assistantwire.ModelUsage {
	return assistantwire.ModelUsage{InputTokens: now.InputTokens - before.InputTokens, OutputTokens: now.OutputTokens - before.OutputTokens,
		CacheReadInputTokens:     now.CacheReadInputTokens - before.CacheReadInputTokens,
		CacheCreationInputTokens: now.CacheCreationInputTokens - before.CacheCreationInputTokens, Requests: now.Requests - before.Requests}
}

// attemptOf is the failed attempt of model with err (an *rpc.Error from attemptModel or runCandidates).
func attemptOf(model string, err error) assistantwire.ModelAttempt {
	a := assistantwire.ModelAttempt{Model: model, Code: assistantwire.CodeModelFailed, Message: err.Error()}
	var re *rpc.Error
	if errors.As(err, &re) {
		a.Code, a.Message = re.Code, re.Message
		if re.Code == assistantwire.CodeModelPaused {
			var pause assistantwire.ModelPause
			if json.Unmarshal(re.Data, &pause) == nil {
				a.Until = pause.Until
			}
		}
	}
	return a
}

// noteRequested remembers that the extension sent models, for Settings: written when the list is new or its last use
// is older than candidatesRequestedRefresh, the most recent candidatesRequestedMost kept. Failing to write never
// fails the call.
func (s *Service) noteRequested(ctx context.Context, purpose string, models []string) {
	st := s.store()
	if st == nil {
		return
	}
	s.models.requestedMu.Lock()
	defer s.models.requestedMu.Unlock()
	var list []requestedChain
	if _, err := st.Setting(ctx, requestedKey(purpose), &list); err != nil {
		s.logf("assistant: candidates of %s: %v", purpose, err)
		return
	}
	now := time.Now().UTC()
	i := slices.IndexFunc(list, func(r requestedChain) bool { return slices.Equal(r.Models, models) })
	if i >= 0 && now.Sub(list[i].LastUsed) < candidatesRequestedRefresh {
		return
	}
	if i >= 0 {
		list = slices.Delete(list, i, i+1)
	}
	list = append([]requestedChain{{Models: slices.Clone(models), LastUsed: now}}, list...)
	if len(list) > candidatesRequestedMost {
		list = list[:candidatesRequestedMost]
	}
	if err := st.PutSetting(ctx, requestedKey(purpose), list); err != nil {
		s.logf("assistant: candidates of %s: %v", purpose, err)
	}
}

// purposeCandidates is what Settings shows of purpose's candidates (an extension's purpose only).
func (s *Service) purposeCandidates(ctx context.Context, purpose string, setting *candidateSetting, effective ModelBinding) *ModelCandidates {
	lang := s.requestCtxLang(ctx)
	out := &ModelCandidates{Mode: candidatesExtension, DirectProviderIDs: []string{}, Custom: []CandidateView{}, Requested: []RequestedChain{},
		Modes: []string{candidatesExtension, candidatesOff, candidatesCustom}, Most: assistantwire.ModelCandidatesMost}
	if setting != nil {
		out.Mode = cmp.Or(setting.Mode, candidatesExtension)
		for _, b := range setting.Custom {
			view := candidateView(s.pickedCandidate(ctx, b, lang))
			view.ConfiguredModel = &b.Model
			out.Custom = append(out.Custom, view)
		}
	}
	if st := s.store(); st != nil {
		var list []requestedChain
		if _, err := memoSetting(ctx, st, requestedKey(purpose), &list); err == nil {
			for _, r := range list {
				chain := RequestedChain{Models: []CandidateView{}, LastUsed: r.LastUsed}
				for _, m := range r.Models {
					c := s.offeredCandidate(ctx, effective, m, lang)
					chain.Models = append(chain.Models, candidateView(c))
				}
				out.Requested = append(out.Requested, chain)
			}
		}
	}
	return out
}

func candidateView(c modelCandidate) CandidateView {
	return CandidateView{Model: c.model, Provider: c.id, ProviderName: c.provider, Available: c.problem == "", Problem: c.problem}
}

// patchCandidates reads PATCH's candidates: null goes back to the extension's lists, an object is a mode and, for
// custom, the user's list; each provider one of p's, each model a model id or empty for the provider's default.
func (s *Service) patchCandidates(ctx context.Context, p ModelPurpose, raw json.RawMessage) (*candidateSetting, error) {
	if !isExtensionPurpose(p.ID) {
		return nil, i18n.Err("assistant-models.err_candidates_extension_only")
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var in struct {
		Mode   string `json:"mode"`
		Custom []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"custom"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, i18n.Err("assistant-models.err_candidates_invalid")
	}
	switch in.Mode {
	case candidatesExtension, "":
		if len(in.Custom) > 0 {
			return nil, i18n.Err("assistant-models.err_candidates_invalid")
		}
		return nil, nil
	case candidatesOff:
		if len(in.Custom) > 0 {
			return nil, i18n.Err("assistant-models.err_candidates_invalid")
		}
		return &candidateSetting{Mode: candidatesOff}, nil
	case candidatesCustom:
	default:
		return nil, i18n.Err("assistant-models.err_candidates_invalid")
	}
	if len(in.Custom) == 0 || len(in.Custom) > assistantwire.ModelCandidatesMost {
		return nil, i18n.Err("assistant-models.err_candidates_count", i18n.Args{"most": assistantwire.ModelCandidatesMost})
	}
	out := &candidateSetting{Mode: candidatesCustom}
	for _, e := range in.Custom {
		if !slices.Contains(p.ProviderIDs, e.Provider) {
			return nil, i18n.Err("assistant-models.err_provider_unsupported")
		}
		if err := checkModelID(e.Model); err != nil {
			return nil, err
		}
		b := ModelBinding{Provider: e.Provider, Model: e.Model}
		if slices.Contains(out.Custom, b) {
			return nil, i18n.Err("assistant-models.err_candidates_invalid")
		}
		if c := s.pickedCandidate(ctx, b, ""); c.problem != "" {
			return nil, i18n.Err("assistant-models.err_candidates_not_callable", i18n.Args{"provider": e.Provider})
		}
		out.Custom = append(out.Custom, b)
	}
	return out, nil
}
