package proto

import (
	"errors"
	"fmt"
	"strings"
)

type ModelAliases map[string]string

var modelAliasEnv = map[string]string{
	"sonnet_model":   "ANTHROPIC_DEFAULT_SONNET_MODEL",
	"haiku_model":    "ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"opus_model":     "ANTHROPIC_DEFAULT_OPUS_MODEL",
	"fable_model":    "ANTHROPIC_DEFAULT_FABLE_MODEL",
	"subagent_model": "CLAUDE_CODE_SUBAGENT_MODEL",
}

func (a ModelAliases) Validate() error {
	for key, model := range a {
		if _, ok := modelAliasEnv[key]; !ok {
			return errors.New("model_aliases: only sonnet_model, haiku_model, opus_model, fable_model and subagent_model are supported")
		}
		name, _, _ := strings.Cut(model, "[")
		if len(model) > 200 || !ValidModelID(model) || strings.Contains(model, "~") || strings.EqualFold(name, "claude") ||
			strings.EqualFold(name, "sonnet") || strings.EqualFold(name, "haiku") ||
			strings.EqualFold(name, "opus") || strings.EqualFold(name, "fable") ||
			strings.EqualFold(name, "opusplan") || strings.EqualFold(name, "best") || strings.EqualFold(name, "default") || strings.EqualFold(name, "inherit") {
			return fmt.Errorf("model_aliases.%s: expected a concrete model ID", key)
		}
	}
	return nil
}

func (a ModelAliases) Env() map[string]string {
	env := make(map[string]string, len(a)+1)
	for key, model := range a {
		if name, ok := modelAliasEnv[key]; ok {
			env[name] = model
		}
	}
	if model := a["subagent_model"]; model != "" {
		env["CLAUDE_CODE_WORKFLOW_SUBAGENT_MODEL"] = model
	}
	return env
}
