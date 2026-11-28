package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Model calls by extensions (#6102, docs/extensions/extensions.md "Model calls"). The manager checks the models scope and hands
// the call here with the extension's id; it runs on the extension's purpose extension.<id>, which inherits
// "Knowledge base"'s model until the user picks another in Settings › Models. Keys stay here. No budget and no cap
// (the user's decision): each call's usage goes back to the extension and into one log line. The manager's own calls
// (models.purpose_complete, managerPurposes) go the same way on a built-in purpose.

const (
	modelCallMaxTokens   = 4096
	modelCallTokensLimit = 64000
	modelCallTimeout     = 120 * time.Second
	modelCallTimeoutMost = 600 * time.Second
	modelCallSchemaMost  = 64 << 10
	modelCallLabelMost   = 64
)

// modelExtensions are the extensions with a model purpose of their own, as the manager has them (plugins.models).
func (s *Service) modelExtensions(ctx context.Context) []assistantwire.ModelExtension {
	var res struct {
		Extensions []assistantwire.ModelExtension `json:"extensions"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsModels, map[string]any{}, &res); err != nil {
		s.logf("assistant: model extensions: %v", err)
	}
	return res.Extensions
}

// extensionPurposeDef is extension.<id>'s purpose: an API use of its own under "Knowledge base", inheriting it.
func (s *Service) extensionPurposeDef(ctx context.Context, id string) (ModelPurpose, bool) {
	ext, ok := strings.CutPrefix(id, assistantwire.ExtensionPurposePrefix)
	if !ok {
		return ModelPurpose{}, false
	}
	for _, x := range s.modelExtensions(ctx) {
		if x.StorageKey() == ext {
			l := s.requestCtxLang(ctx)
			return ModelPurpose{ID: id, CategoryID: "kb", Name: x.Name, Group: mc(l, "group_extensions"), Kind: "api", Parent: "kb"}, true
		}
	}
	return ModelPurpose{}, false
}

func isExtensionPurpose(id string) bool {
	return strings.HasPrefix(id, assistantwire.ExtensionPurposePrefix)
}

func invalidModelParams(format string, args ...any) error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: "model.complete: " + fmt.Sprintf(format, args...)}
}

func strictModelParams(raw json.RawMessage, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return invalidModelParams("%v", err)
	}
	return nil
}

// extensionComplete answers models.extension_complete.
func (s *Service) extensionComplete(ctx context.Context, call assistantwire.ExtensionComplete) (assistantwire.ModelCompleteResult, error) {
	if call.Extension == "" {
		return assistantwire.ModelCompleteResult{}, invalidModelParams("no extension")
	}
	if len(call.Params) == 0 {
		return assistantwire.ModelCompleteResult{}, invalidModelParams("params are required")
	}
	var p assistantwire.ModelCompleteParams
	if err := strictModelParams(call.Params, &p); err != nil {
		return assistantwire.ModelCompleteResult{}, err
	}
	c, err := checkModelCall(p)
	if err != nil {
		return assistantwire.ModelCompleteResult{}, err
	}
	purpose := assistantwire.ExtensionPurposePrefix + call.StorageKey()
	if _, ok := s.extensionPurposeDef(ctx, purpose); !ok {
		return assistantwire.ModelCompleteResult{}, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: the extension has no model purpose (is it on and granted models?)"}
	}
	return s.runModelCall(ctx, purpose, "extension "+call.Extension, "this extension", c)
}

// managerPurposes are the built-in purposes the manager may make calls of its own on (models.purpose_complete).
var managerPurposes = map[string]bool{assistantwire.PurposeSubscriptionRecipe: true}

// purposeComplete answers models.purpose_complete: a call the manager makes itself, such as drafting a subscription's
// recipe (#2040), on one of managerPurposes.
func (s *Service) purposeComplete(ctx context.Context, call assistantwire.PurposeComplete) (assistantwire.ModelCompleteResult, error) {
	if !managerPurposes[call.Purpose] {
		return assistantwire.ModelCompleteResult{}, invalidModelParams("purpose %q is not one the manager calls", call.Purpose)
	}
	c, err := checkModelCall(call.Params)
	if err != nil {
		return assistantwire.ModelCompleteResult{}, err
	}
	return s.runModelCall(ctx, call.Purpose, call.Purpose, "purpose "+call.Purpose, c)
}

// checkedCall is a model.complete whose params passed checkModelCall: what is sent, the schema the reply must match
// and how long the whole call may take.
type checkedCall struct {
	p       assistantwire.ModelCompleteParams
	system  string
	turns   []ai.Message
	schema  map[string]any
	timeout time.Duration
}

// checkModelCall holds model.complete's params to their rules, before anything is resolved or sent.
func checkModelCall(p assistantwire.ModelCompleteParams) (checkedCall, error) {
	if len(p.Messages) == 0 {
		return checkedCall{}, invalidModelParams("messages are required")
	}
	turns := make([]ai.Message, 0, len(p.Messages)+2)
	for i, m := range p.Messages {
		if m.Role != ai.RoleUser && m.Role != ai.RoleAssistant {
			return checkedCall{}, invalidModelParams("messages[%d].role must be user or assistant", i)
		}
		if strings.TrimSpace(m.Content) == "" {
			return checkedCall{}, invalidModelParams("messages[%d].content is empty", i)
		}
		if i > 0 && m.Role == p.Messages[i-1].Role {
			return checkedCall{}, invalidModelParams("messages[%d]: user and assistant turns alternate", i)
		}
		turns = append(turns, ai.Message{Role: m.Role, Content: m.Content})
	}
	if p.Messages[0].Role != ai.RoleUser || p.Messages[len(p.Messages)-1].Role != ai.RoleUser {
		return checkedCall{}, invalidModelParams("messages start and end with the user's")
	}
	switch {
	case p.MaxTokens == 0:
		p.MaxTokens = modelCallMaxTokens
	case p.MaxTokens < 1 || p.MaxTokens > modelCallTokensLimit:
		return checkedCall{}, invalidModelParams("max_tokens is 1 to %d", modelCallTokensLimit)
	}
	timeout := modelCallTimeout
	switch {
	case p.TimeoutSeconds == 0:
	case p.TimeoutSeconds < 1 || time.Duration(p.TimeoutSeconds)*time.Second > modelCallTimeoutMost:
		return checkedCall{}, invalidModelParams("timeout_seconds is 1 to %d", int(modelCallTimeoutMost/time.Second))
	default:
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	if len(p.Label) > modelCallLabelMost {
		return checkedCall{}, invalidModelParams("label is at most %d bytes", modelCallLabelMost)
	}
	if len(p.Models) > assistantwire.ModelCandidatesMost {
		return checkedCall{}, invalidModelParams("models has at most %d entries", assistantwire.ModelCandidatesMost)
	}
	for i, m := range p.Models {
		if !proto.ValidModelID(m) {
			return checkedCall{}, invalidModelParams("models[%d] is not a model id", i)
		}
		if slices.Contains(p.Models[:i], m) {
			return checkedCall{}, invalidModelParams("models[%d] repeats %s", i, m)
		}
	}
	c := checkedCall{p: p, system: p.System, turns: turns, timeout: timeout}
	if len(p.JSONSchema) > 0 && string(p.JSONSchema) != "null" {
		if len(p.JSONSchema) > modelCallSchemaMost {
			return checkedCall{}, invalidModelParams("json_schema is at most %d bytes", modelCallSchemaMost)
		}
		var err error
		if c.schema, err = compileSchema(p.JSONSchema); err != nil {
			return checkedCall{}, invalidModelParams("%v", err)
		}
		var indented bytes.Buffer
		_ = json.Indent(&indented, p.JSONSchema, "", "  ")
		c.system = prompts.RenderJSONSchema(prompts.JSONSchema{System: p.System, Schema: indented.String()})
	}
	return c, nil
}

// runModelCall makes c on purpose's model: with a schema, a reply that does not match it is asked for once more
// with what did not match. who names the caller in the log line, what in the error when nothing can be called.
// With candidate models (c.p.Models, or the user's own list for an extension's purpose) it tries them in order
// instead (model_candidates.go).
func (s *Service) runModelCall(ctx context.Context, purpose, who, what string, c checkedCall) (assistantwire.ModelCompleteResult, error) {
	if chain := s.modelCandidates(ctx, purpose, c.p.Models); chain != nil {
		return s.runCandidates(ctx, purpose, who, c, chain)
	}
	b, err := s.resolveModel(ctx, purpose)
	if err != nil {
		return assistantwire.ModelCompleteResult{}, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: i18n.Localize(err, s.lang(ctx))}
	}
	config, err := s.apiConfig(ctx, b)
	if err != nil {
		return assistantwire.ModelCompleteResult{}, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: i18n.Localize(err, s.lang(ctx))}
	}
	if !config.Configured() {
		return assistantwire.ModelCompleteResult{}, &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: nothing to call for " + what + "; pick a model for it in Settings › Model configuration"}
	}
	var usage assistantwire.ModelUsage
	started := time.Now()
	out, outcome, err := s.attemptModel(ctx, s.countedClient(purpose, "", b.Provider, config), c, c.timeout, &usage)
	s.logf("assistant: %s model call%s on %s: %s in %s, %d request(s), %d input + %d output tokens (cache read %d, written %d)",
		who, labelPart(c.p.Label), config.Model, outcome, time.Since(started).Round(time.Millisecond), usage.Requests,
		usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens)
	return out, err
}

// attemptModel makes c once on client within timeout, adding what it spends to usage: the answer, or the coded
// error (*rpc.Error) model.complete returns; outcome is what the log line says (done, failed, refused, invalid output).
func (s *Service) attemptModel(ctx context.Context, client *ai.Client, c checkedCall, timeout time.Duration, usage *assistantwire.ModelUsage) (assistantwire.ModelCompleteResult, string, error) {
	p := c.p
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	turns := c.turns
	add := func(u ai.Usage) {
		usage.InputTokens += u.InputTokens
		usage.OutputTokens += u.OutputTokens
		usage.CacheReadInputTokens += u.CacheReadInputTokens
		usage.CacheCreationInputTokens += u.CacheCreationInputTokens
		usage.Requests++
	}
	var reply ai.Reply
	var value any
	var err error
	for attempt := 0; ; attempt++ {
		reply, err = client.Chat(cctx, ai.Request{System: c.system, Messages: turns, MaxTokens: p.MaxTokens})
		if err != nil {
			var refusal *ai.RefusalError
			if errors.As(err, &refusal) { // the declined call still spent tokens: its usage goes back with the code
				add(reply.Usage)
				data, _ := json.Marshal(assistantwire.ModelRefusal{StopReason: refusal.StopReason, Usage: *usage})
				return assistantwire.ModelCompleteResult{}, "refused (" + refusal.StopReason + ")",
					&rpc.Error{Code: assistantwire.CodeModelRefused, Message: "model.complete: " + err.Error(), Data: data}
			}
			return assistantwire.ModelCompleteResult{}, "failed", modelCallError(cctx, s.requestCtxLang(cctx), err)
		}
		add(reply.Usage)
		if c.schema == nil {
			break
		}
		problem := ""
		if value, err = decodeReply(reply.Text); err != nil {
			problem = err.Error()
		} else {
			problem = validateSchema(c.schema, c.schema, value, "$", 0)
		}
		if problem == "" {
			break
		}
		if reply.Truncated {
			problem += " (the reply was cut off at max_tokens)"
		}
		if attempt > 0 {
			data, _ := json.Marshal(assistantwire.ModelInvalidOutput{Problem: problem, Text: reply.Text, Usage: *usage})
			return assistantwire.ModelCompleteResult{}, "invalid output",
				&rpc.Error{Code: assistantwire.CodeInvalidOutput, Message: "model.complete: the reply does not match json_schema: " + problem, Data: data}
		}
		turns = append(turns, ai.Message{Role: ai.RoleAssistant, Content: cmpOr(reply.Text, "(empty)")},
			ai.Message{Role: ai.RoleUser, Content: prompts.RenderJSONSchemaRetry(problem)})
	}
	out := assistantwire.ModelCompleteResult{Text: reply.Text, Model: reply.Model, StopReason: "end_turn", Usage: *usage}
	if reply.Truncated {
		out.StopReason = "max_tokens"
	}
	if c.schema != nil {
		out.JSON, _ = json.Marshal(value)
	}
	return out, "done", nil
}

func labelPart(label string) string {
	if label == "" {
		return ""
	}
	return " (" + label + ")"
}

func cmpOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// directCallFailureExplanation is the plain-language cause shared by manager model calls and saved direct-API checks.
// The caller appends the upstream's original detail; an unclassified error keeps its established message.
func directCallFailureExplanation(l i18n.Lang, err error) string {
	switch {
	case ai.IsModelUnavailable(err):
		return pt(l, "status_model_not_found")
	case ai.IsContextTooLong(err):
		return pt(l, "context_too_long")
	default:
		return ""
	}
}

// modelCallError codes a failed request for the extension: held connection, nothing set up, out of time, or the
// upstream's failure. l is the manager request's language, or the user's language for a background call. A declined
// request (ai.RefusalError) is model_refused, coded by the caller with its usage.
func modelCallError(ctx context.Context, l i18n.Lang, err error) error {
	var paused *ai.PausedError
	switch {
	case errors.As(err, &paused):
		data, _ := json.Marshal(assistantwire.ModelPause{Reason: paused.Reason, Until: paused.Until.UTC().Format(time.RFC3339)})
		return &rpc.Error{Code: assistantwire.CodeModelPaused, Message: "model.complete: " + paused.Text(), Data: data}
	case ai.IsNotConfigured(err):
		return &rpc.Error{Code: assistantwire.CodeModelUnavailable, Message: "model.complete: " + err.Error()}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &rpc.Error{Code: assistantwire.CodeModelTimeout, Message: "model.complete: timed out"}
	}
	if explanation := directCallFailureExplanation(l, err); explanation != "" {
		return &rpc.Error{Code: assistantwire.CodeModelFailed, Message: "model.complete: " + explanation + ": " + err.Error()}
	}
	return &rpc.Error{Code: assistantwire.CodeModelFailed, Message: "model.complete: " + err.Error()}
}
