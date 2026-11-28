package proto

import (
	"strings"
	"testing"
)

func TestModelAliasesValidate(t *testing.T) {
	valid := ModelAliases{
		"sonnet_model":   "vendor/standard[1m]",
		"haiku_model":    "vendor/fast",
		"opus_model":     "claude-opus-5-5",
		"fable_model":    "vendor/fable",
		"subagent_model": "vendor/helper",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid aliases: %v", err)
	}

	for _, tc := range []struct {
		name    string
		aliases ModelAliases
	}{
		{"unknown key", ModelAliases{"tiny_model": "vendor/tiny"}},
		{"empty value", ModelAliases{"sonnet_model": ""}},
		{"tier alias", ModelAliases{"sonnet_model": "sonnet"}},
		{"plan alias", ModelAliases{"opus_model": "opusplan[1m]"}},
		{"default alias", ModelAliases{"haiku_model": "default"}},
		{"inherit alias", ModelAliases{"fable_model": "inherit"}},
		{"native alias", ModelAliases{"subagent_model": "claude"}},
		{"space", ModelAliases{"sonnet_model": "vendor/standard model"}},
		{"too long", ModelAliases{"sonnet_model": strings.Repeat("a", 201)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.aliases.Validate(); err == nil {
				t.Fatalf("%v validated", tc.aliases)
			}
		})
	}
}
