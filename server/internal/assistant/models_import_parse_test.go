package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
)

// Fake keys and hosts only: nothing here is a real gateway, key or header.
const (
	pasteKeyA = "sk-example-paste-a"
	pasteKeyB = "sk-example-paste-b"
)

// gateway is a Claude Code gateway draft's provider.
func gateway(base, auth, key string) pastedProvider {
	return pastedProvider{Kind: "launcher", Agent: "claude", API: "messages", BaseURL: base, Auth: auth, APIKey: key}
}

// withModels is p with models (the first is its model).
func withModels(p pastedProvider, ids ...string) pastedProvider {
	for _, id := range ids {
		p.Models = append(p.Models, proto.ProviderModel{ID: id})
	}
	if len(ids) > 0 {
		p.Model = ids[0]
	}
	return p
}

func checkPaste(t *testing.T, text string, want pasteResult) {
	t.Helper()
	got := parsePaste(text)
	if !reflect.DeepEqual(got, want) {
		gj, _ := json.MarshalIndent(got, "", " ")
		wj, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("got  %s\nwant %s", gj, wj)
	}
}

func notes(n ...pasteNote) []pasteNote { return n }

func TestParsePasteShell(t *testing.T) {
	kimi := gateway("https://gw.example.com", "bearer", pasteKeyA)
	kimi.Name, kimi.Model, kimi.Args = "kimi", "kimi-k3", []string{"--dangerously-skip-permissions"}
	kimi.Models = []proto.ProviderModel{{ID: "kimi-k3", ContextWindow: 1000000}}
	cases := []struct {
		name, text string
		want       pasteResult
	}{
		{
			name: "a single-quoted alias with a /v1 address",
			text: `alias kimi='ANTHROPIC_BASE_URL=https://gw.example.com/v1/ ANTHROPIC_AUTH_TOKEN="Bearer ` + pasteKeyA + `" ANTHROPIC_MODEL=kimi-k3[1m] claude --dangerously-skip-permissions'`,
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{{Label: "kimi", Aliases: []string{"kimi"}, Provider: kimi}}},
		},
		{
			name: "double quotes, no quotes, an alias without a key",
			text: "alias a=\"ANTHROPIC_BASE_URL=https://a.example.com ANTHROPIC_API_KEY=" + pasteKeyA + " claude\"\n" +
				`alias b=ANTHROPIC_BASE_URL=https://b.example.com\ claude`,
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{
				{Label: "a", Aliases: []string{"a"}, Provider: pastedProvider{Kind: "launcher", Agent: "claude", API: "messages", Name: "a",
					BaseURL: "https://a.example.com", Auth: "api_key", APIKey: pasteKeyA, Args: []string{}}},
				{Label: "b", Aliases: []string{"b"}, Provider: pastedProvider{Kind: "launcher", Agent: "claude", API: "messages", Name: "b",
					BaseURL: "https://b.example.com", Auth: "api_key", Args: []string{}}, Notes: notes(pasteNote{Code: noteNoKey})},
			}},
		},
		{
			name: "exports and a command line with its own variables",
			text: "export ANTHROPIC_BASE_URL=https://gw.example.com\nexport ANTHROPIC_AUTH_TOKEN=\"" + pasteKeyA + "\" DISABLE_TELEMETRY=1\n" +
				"ANTHROPIC_MODEL=glm-5 claude -c",
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "bearer", pasteKeyA), "glm-5")
					p.Args, p.Env = []string{"-c"}, "DISABLE_TELEMETRY=1"
					return p
				}(),
				EnvNames: []string{"DISABLE_TELEMETRY"},
			}}},
		},
		{
			name: ".env lines with quotes and comments",
			text: "# the team's gateway\nANTHROPIC_BASE_URL=\"https://gw.example.com/anthropic\"  # it's ours\n" +
				"export ANTHROPIC_API_KEY='" + pasteKeyA + "'\nAPI_TIMEOUT_MS=600000\nGATEWAY_LABEL=\"team blue\"\nAWS_SECRET_ACCESS_KEY=sk-example-aws\n",
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: func() pastedProvider {
					p := gateway("https://gw.example.com/anthropic", "api_key", pasteKeyA)
					p.TimeoutMS, p.Env = 600000, `GATEWAY_LABEL="team blue"`
					return p
				}(),
				EnvNames: []string{"GATEWAY_LABEL"}, SkippedEnv: []skippedEnv{{Name: "AWS_SECRET_ACCESS_KEY", Reason: "credential"}},
			}}},
		},
		{
			name: "line continuations, CRLF",
			text: "ANTHROPIC_BASE_URL=https://gw.example.com \\\r\n  ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + " \\\r\n  claude --model x\r\nfi\r\n",
			want: pasteResult{Format: "shell", Notes: notes(pasteNote{Code: noteNotClaude, Line: 4}), Drafts: []pasteDraft{{
				Provider: func() pastedProvider {
					p := gateway("https://gw.example.com", "bearer", pasteKeyA)
					p.Args = []string{"--model", "x"}
					return p
				}(),
			}}},
		},
		{
			name: "aliases on one gateway and key are one draft; another key another",
			text: "alias k1='ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + " ANTHROPIC_MODEL=m1 claude'\n" +
				"alias k2='ANTHROPIC_BASE_URL=https://gw.example.com/ ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + " ANTHROPIC_MODEL=m2 claude'\n" +
				"alias k3='ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=" + pasteKeyB + " ANTHROPIC_MODEL=m1 claude'\n",
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{
				{Label: "k1, k2", Aliases: []string{"k1", "k2"}, Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "bearer", pasteKeyA), "m1", "m2")
					p.Name, p.Args = "k1", []string{}
					return p
				}()},
				{Label: "k3", Aliases: []string{"k3"}, Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "bearer", pasteKeyB), "m1")
					p.Name, p.Args = "k3", []string{}
					return p
				}()},
			}},
		},
		{
			name: "aliases that differ in more than the model are a draft each",
			text: "export ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + "\n" +
				"alias d1='ANTHROPIC_MODEL=m1 claude'\nalias d2='CLAUDE_CODE_EFFORT_LEVEL=high ANTHROPIC_MODEL=m2 claude'\n",
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{
				{Label: "d1", Aliases: []string{"d1"}, Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "bearer", pasteKeyA), "m1")
					p.Name, p.Args = "d1", []string{}
					return p
				}()},
				{Label: "d2", Aliases: []string{"d2"}, Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "bearer", pasteKeyA), "m2")
					p.Name, p.Args, p.Effort = "d2", []string{}, "high"
					return p
				}()},
			}},
		},
		{
			name: "other aliases and commands, Claude Code's own sign-in, shell expansions, a user in the address",
			text: "alias ll='ls -la'\nalias cx='OPENAI_API_KEY=sk-example-codex codex'\nalias plain='claude'\n" +
				"alias v='ANTHROPIC_BASE_URL=$GATEWAY claude'\nalias u='ANTHROPIC_BASE_URL=https://user:pw-example@gw.example.com claude'\n" +
				"alias w='ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=$(cat ~/.key) claude'\n" +
				"claude\nf() { claude; }\nalias -g\nsource ~/.zshrc.local\n",
			want: pasteResult{Format: "shell", Notes: notes( // in text order
				pasteNote{Code: noteNotClaude, Name: "ll"}, pasteNote{Code: noteNotClaude, Name: "cx"},
				pasteNote{Code: noteNoGateway, Name: "plain"}, pasteNote{Code: noteURLShell, Name: "v"}, pasteNote{Code: noteURLUser, Name: "u"},
				pasteNote{Code: noteNoGateway, Line: 7}, pasteNote{Code: noteLineUnused, Line: 8}, pasteNote{Code: noteLineUnused, Line: 9},
				pasteNote{Code: noteNotClaude, Line: 10},
			), Drafts: []pasteDraft{{Label: "w", Aliases: []string{"w"}, Provider: func() pastedProvider {
				p := gateway("https://gw.example.com", "bearer", "")
				p.Name, p.Args = "w", []string{}
				return p
			}(), Notes: notes(pasteNote{Code: noteKeyShell})}}},
		},
		{
			name: "aliases see every export, a command line only the ones before it",
			text: "alias late='claude'\nclaude\nexport ANTHROPIC_BASE_URL=https://gw.example.com\n",
			want: pasteResult{Format: "shell", Notes: notes(pasteNote{Code: noteNoGateway, Line: 2}), Drafts: []pasteDraft{{
				Label: "late", Aliases: []string{"late"}, Provider: func() pastedProvider {
					p := gateway("https://gw.example.com", "api_key", "")
					p.Name, p.Args = "late", []string{}
					return p
				}(), Notes: notes(pasteNote{Code: noteNoKey})}}},
		},
		{
			name: "an alias defined again counts where its last definition is",
			text: "alias k='ANTHROPIC_BASE_URL=https://old.example.com claude'\nalias j='ANTHROPIC_BASE_URL=https://j.example.com claude'\n" +
				"alias k='ANTHROPIC_BASE_URL=https://new.example.com claude'\n",
			want: pasteResult{Format: "shell", Notes: []pasteNote{}, Drafts: []pasteDraft{
				{Label: "j", Aliases: []string{"j"}, Provider: func() pastedProvider {
					p := gateway("https://j.example.com", "api_key", "")
					p.Name, p.Args = "j", []string{}
					return p
				}(), Notes: notes(pasteNote{Code: noteNoKey})},
				{Label: "k", Aliases: []string{"k"}, Provider: func() pastedProvider {
					p := gateway("https://new.example.com", "api_key", "")
					p.Name, p.Args = "k", []string{}
					return p
				}(), Notes: notes(pasteNote{Code: noteNoKey})},
			}},
		},
		{
			name: "an alias defined again as something else is no longer a claude alias",
			text: "alias k='ANTHROPIC_BASE_URL=https://gw.example.com claude'\nalias k='ls -la'\n",
			want: pasteResult{Format: "shell", Notes: notes(pasteNote{Code: noteNotClaude, Name: "k"}), Drafts: []pasteDraft{}},
		},
		{
			name: "variables without a gateway",
			text: "export ANTHROPIC_MODEL=opus\n",
			want: pasteResult{Format: "shell", Notes: notes(pasteNote{Code: noteNoGateway}), Drafts: []pasteDraft{}},
		},
		{
			name: "an address set to an expansion",
			text: "export ANTHROPIC_BASE_URL=\"${GATEWAY}\"\n",
			want: pasteResult{Format: "shell", Notes: notes(pasteNote{Code: noteURLShell, Line: 1}), Drafts: []pasteDraft{}},
		},
		{name: "variables of something else", text: "DATABASE_URL=postgres://db.example.com\n", want: pasteResult{Notes: notes(pasteNote{Code: noteUnrecognized}), Drafts: []pasteDraft{}}},
		{name: "prose", text: "hello there\nit's a key\n", want: pasteResult{Notes: notes(pasteNote{Code: noteUnrecognized}), Drafts: []pasteDraft{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkPaste(t, c.text, c.want) })
	}
}

func TestParsePasteSettings(t *testing.T) {
	cases := []struct {
		name, text string
		want       pasteResult
	}{
		{
			name: "env, model, headers, values that are no strings; with a key in env, apiKeyHelper says nothing",
			text: `{"env": {"ANTHROPIC_BASE_URL": "https://gw.example.com/", "ANTHROPIC_AUTH_TOKEN": "` + pasteKeyA + `",
				"ANTHROPIC_CUSTOM_HEADERS": "X-Team: blue\nX-Api-Key: sk-example-hdr\nAuthorization: Basic sk-example-basic",
				"API_TIMEOUT_MS": 600000, "DISABLE_TELEMETRY": "1", "ENABLE_X": true},
				"model": "glm-5", "apiKeyHelper": "~/bin/key.sh", "permissions": {"allow": []}}`,
			want: pasteResult{Format: "settings", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "bearer", pasteKeyA), "glm-5")
					p.Headers, p.Env = "X-Team: blue", "DISABLE_TELEMETRY=1"
					return p
				}(),
				EnvNames:       []string{"DISABLE_TELEMETRY"},
				SkippedHeaders: []skippedHeader{{Name: "X-Api-Key", Reason: "credential"}, {Name: "Authorization", Reason: "credential"}},
			}}},
		},
		{
			name: "no key in env: apiKeyHelper says why",
			text: `{"env": {"ANTHROPIC_BASE_URL": "https://gw.example.com"}, "apiKeyHelper": "~/bin/key.sh"}`,
			want: pasteResult{Format: "settings", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: gateway("https://gw.example.com", "api_key", ""), Notes: notes(pasteNote{Code: noteKeyHelper}),
			}}},
		},
		{
			name: "env's model wins; no key",
			text: `{"env": {"ANTHROPIC_BASE_URL": "https://gw.example.com", "ANTHROPIC_MODEL": "m-env"}, "model": "m-top"}`,
			want: pasteResult{Format: "settings", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: withModels(gateway("https://gw.example.com", "api_key", ""), "m-env"), Notes: notes(pasteNote{Code: noteNoKey}),
			}}},
		},
		{name: "Claude Code's own sign-in", text: `{"model": "opus"}`, want: pasteResult{Format: "settings", Notes: notes(pasteNote{Code: noteNoGateway}), Drafts: []pasteDraft{}}},
		{name: "invalid JSON", text: `{"env": {"ANTHROPIC_BASE_URL": "https://gw.example.com",`, want: pasteResult{Notes: notes(pasteNote{Code: noteInvalidJSON}), Drafts: []pasteDraft{}}},
		{name: "other JSON", text: `[{"env": {}}]`, want: pasteResult{Notes: notes(pasteNote{Code: noteUnrecognized}), Drafts: []pasteDraft{}}},
		{name: "an object of something else", text: `{"name": "x"}`, want: pasteResult{Notes: notes(pasteNote{Code: noteUnrecognized}), Drafts: []pasteDraft{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkPaste(t, c.text, c.want) })
	}
}

func TestParsePasteCurl(t *testing.T) {
	cases := []struct {
		name, text string
		want       pasteResult
	}{
		{
			name: "Messages: x-api-key, headers, a proxy, a JSON body over several lines",
			text: "curl https://gw.example.com/v1/messages \\\n  -H \"x-api-key: " + pasteKeyA + "\" \\\n" +
				"  -H 'anthropic-version: 2023-06-01' -H \"content-type: application/json\" \\\n" +
				"  -H \"X-Team: blue\" -H \"X-Org-Token: sk-example-org\" -x http://proxy.example.com:8080 --noproxy localhost \\\n" +
				"  -u user:pw-example -k -sS -X POST --cert /tmp/c.pem \\\n  -d '{\n    \"model\": \"claude-x\",\n    \"max_tokens\": 10\n  }'\n",
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: func() pastedProvider {
					p := withModels(gateway("https://gw.example.com", "api_key", pasteKeyA), "claude-x")
					p.Headers, p.Proxy, p.NoProxy = "X-Team: blue", "http://proxy.example.com:8080", "localhost"
					return p
				}(),
				SkippedHeaders: []skippedHeader{{Name: "anthropic-version", Reason: "protocol"}, {Name: "content-type", Reason: "protocol"},
					{Name: "X-Org-Token", Reason: "credential"}},
				Notes: notes(pasteNote{Code: noteCurlOption, Name: "-u"}, pasteNote{Code: noteCurlOption, Name: "-k"},
					pasteNote{Code: noteCurlOption, Name: "--cert"}),
			}}},
		},
		{
			name: "Chat Completions: Bearer and custom headers, proxy options noted",
			text: "KEY=1 curl -sSL https://api.example.com/v1/chat/completions -H \"Authorization: Bearer " + pasteKeyB + "\" -H \"X-Team: blue\" " +
				"-x http://proxy.example.com:1 --noproxy localhost --json '{\"model\":\"gpt-x\"}'\necho done\n",
			want: pasteResult{Format: "curl", Notes: notes(pasteNote{Code: noteLineUnused, Line: 2}), Drafts: []pasteDraft{{
				Provider: func() pastedProvider {
					p := withModels(pastedProvider{Kind: "api", API: "chat", BaseURL: "https://api.example.com/v1", Auth: "bearer", APIKey: pasteKeyB}, "gpt-x")
					p.Headers = "X-Team: blue"
					return p
				}(),
				Notes: notes(pasteNote{Code: noteCurlOption, Name: "-x"}, pasteNote{Code: noteCurlOption, Name: "--noproxy"}),
			}}},
		},
		{
			name: "Responses, the key a variable",
			text: `curl https://api.example.com/prefix/v1/responses/ -H 'Authorization: Bearer $OPENAI_API_KEY'`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: pastedProvider{Kind: "api", API: "responses", BaseURL: "https://api.example.com/prefix/v1", Auth: "bearer"},
				Notes:    notes(pasteNote{Code: noteKeyShell}),
			}}},
		},
		{
			name: "Chat Completions without /v1",
			text: `curl https://api.example.com/chat/completions -H 'Authorization: Bearer ` + pasteKeyB + `' -d '{"model":"chat-x"}'`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: withModels(pastedProvider{Kind: "api", API: "chat", BaseURL: "https://api.example.com", Auth: "bearer", APIKey: pasteKeyB}, "chat-x"),
			}}},
		},
		{
			name: "Chat Completions under another version prefix",
			text: `curl "https://api.example.com/v1beta/openai/chat/completions" -H "Authorization: Bearer ` + pasteKeyB + `"`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: pastedProvider{Kind: "api", API: "chat", BaseURL: "https://api.example.com/v1beta/openai", Auth: "bearer", APIKey: pasteKeyB},
			}}},
		},
		{
			name: "Responses without /v1",
			text: `curl https://gw.example.com/openai/responses -H "Authorization: Bearer ` + pasteKeyB + `"`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: pastedProvider{Kind: "api", API: "responses", BaseURL: "https://gw.example.com/openai", Auth: "bearer", APIKey: pasteKeyB},
			}}},
		},
		{
			name: "models, -H run together",
			text: `curl "https://gw.example.com/anthropic/v1/models" -H"x-api-key: ` + pasteKeyA + `"`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: gateway("https://gw.example.com/anthropic", "api_key", pasteKeyA),
			}}},
		},
		{
			name: "messages without /v1, no key",
			text: `curl --url https://gw.example.com/api/messages -d @body.json`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: gateway("https://gw.example.com/api", "", ""), Notes: notes(pasteNote{Code: noteNoKey}),
			}}},
		},
		{
			name: "another path",
			text: `curl "https://gw.example.com/proxy/run/?debug=1#x" -H "x-api-key: ` + pasteKeyA + `"`,
			want: pasteResult{Format: "curl", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Provider: gateway("https://gw.example.com/proxy/run", "api_key", pasteKeyA),
				Notes:    notes(pasteNote{Code: noteCurlPath, Name: "/proxy/run/"}),
			}}},
		},
		{name: "the URL a variable", text: "# call it\ncurl $GATEWAY/v1/messages -H \"x-api-key: " + pasteKeyA + "\"",
			want: pasteResult{Format: "curl", Notes: notes(pasteNote{Code: noteURLShell, Line: 2}), Drafts: []pasteDraft{}}},
		{name: "a user in the URL", text: "curl https://user:pw-example@gw.example.com/v1/messages",
			want: pasteResult{Format: "curl", Notes: notes(pasteNote{Code: noteURLUser, Line: 1}), Drafts: []pasteDraft{}}},
		{name: "no URL", text: "curl -sS", want: pasteResult{Notes: notes(pasteNote{Code: noteUnrecognized}), Drafts: []pasteDraft{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkPaste(t, c.text, c.want) })
	}
}

func TestParsePasteCindy(t *testing.T) {
	cases := []struct {
		name, text string
		want       pasteResult
	}{
		{
			name: "one provider: its Messages endpoint, models as strings and objects, the other endpoint noted",
			text: `{"kind": "custom", "name": "Example AI", "auth": {"method": "apiKey", "apiKey": "` + pasteKeyA + `"},
				"endpoints": [
					{"protocol": "openai-responses", "baseUrl": "https://example.invalid/v1", "apiKey": "sk-example-codex"},
					{"protocol": "anthropic-messages", "baseUrl": "https://example.invalid/anthropic/v1", "headers": {"X-Tenant": "example"},
						"models": ["m1", {"id": "m2", "name": "M Two", "contextWindow": 1000000, "reasoningEfforts": ["low", "high", "turbo"],
							"reasoningDefaultEffort": "high"}, {"id": "m3", "reasoningEfforts": ["low"], "reasoningDefaultEffort": "max"}, "m1"]}
				]}`,
			want: pasteResult{Format: "cindy", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Label: "Example AI",
				Provider: pastedProvider{Kind: "launcher", Agent: "claude", API: "messages", DisplayName: "Example AI",
					BaseURL: "https://example.invalid/anthropic", APIKey: pasteKeyA, Model: "m1", Headers: "X-Tenant: example",
					Models: []proto.ProviderModel{{ID: "m1"}, {ID: "m2", Name: "M Two", ContextWindow: 1000000, Efforts: []string{"low", "high"},
						DefaultEffort: "high"}, {ID: "m3", Efforts: []string{"low"}}}},
				Notes: notes(pasteNote{Code: noteCindyEndpoint, Name: "openai-responses"}),
			}}},
		},
		{
			name: "Responses only: a direct API, the provider's models",
			text: `{"name": "R", "auth": {"apiKey": "` + pasteKeyB + `"}, "models": ["r1"],
				"endpoints": [{"protocol": "openai-responses", "baseUrl": "https://example.invalid/v1/"}]}`,
			want: pasteResult{Format: "cindy", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Label:    "R",
				Provider: withModels(pastedProvider{Kind: "api", API: "responses", DisplayName: "R", BaseURL: "https://example.invalid/v1", Auth: "bearer", APIKey: pasteKeyB}, "r1"),
			}}},
		},
		{
			name: "Chat with headers, Google noted, no auth",
			text: `{"kind": "custom", "name": "C", "auth": {"method": "none"}, "endpoints": [
				{"protocol": "google-generative-ai", "baseUrl": "https://example.invalid/google"},
				{"protocol": "openai-chat", "baseUrl": "http://127.0.0.1:4000/v1", "headers": {"X-Tenant": "example", "X-Api-Key": "sk-example-h",
					"Content-Type": "application/json", "X-Count": 3}}]}`,
			want: pasteResult{Format: "cindy", Notes: []pasteNote{}, Drafts: []pasteDraft{{
				Label:          "C",
				Provider:       pastedProvider{Kind: "api", API: "chat", DisplayName: "C", BaseURL: "http://127.0.0.1:4000/v1", Headers: "X-Tenant: example"},
				SkippedHeaders: []skippedHeader{{Name: "Content-Type", Reason: "protocol"}, {Name: "X-Api-Key", Reason: "credential"}},
				Notes:          notes(pasteNote{Code: noteCindyAuth, Name: "none"}, pasteNote{Code: noteCindyEndpoint, Name: "google-generative-ai"}),
			}}},
		},
		{
			name: "a batch: Google only, presets, built-in keys, OAuth, a user in the address, a good one",
			text: `{"providers": [
				{"kind": "custom", "name": "G", "auth": {"method": "apiKey", "apiKey": "sk-example-g"},
					"endpoints": [{"protocol": "google-generative-ai", "baseUrl": "https://example.invalid/g"}]},
				{"kind": "preset", "preset": "openrouter", "apiKey": "sk-example-p"},
				{"kind": "builtin", "provider": "gemini", "apiKey": "sk-example-b"},
				{"kind": "custom", "name": "O", "auth": {"method": "oauth", "tokenUrl": "https://example.invalid/token"},
					"endpoints": [{"protocol": "anthropic-messages", "baseUrl": "https://example.invalid/o"}]},
				{"kind": "custom", "name": "U", "auth": {"apiKey": "sk-example-u"},
					"endpoints": [{"protocol": "anthropic-messages", "baseUrl": "https://user:pw-example@example.invalid/u"}]},
				{"kind": "custom", "name": "Good", "auth": {"apiKey": "` + pasteKeyA + `"},
					"endpoints": [{"protocol": "anthropic-messages", "baseUrl": "https://example.invalid/good", "apiKey": "` + pasteKeyB + `"}]}
			]}`,
			want: pasteResult{Format: "cindy", Notes: notes(pasteNote{Code: noteCindyEndpoint, Name: "google-generative-ai"},
				pasteNote{Code: noteCindyKind, Name: "preset"}, pasteNote{Code: noteCindyKind, Name: "builtin"},
				pasteNote{Code: noteCindyAuth, Name: "oauth"}, pasteNote{Code: noteURLUser, Name: "U"}),
				Drafts: []pasteDraft{{Label: "Good", Provider: pastedProvider{Kind: "launcher", Agent: "claude", API: "messages", DisplayName: "Good",
					BaseURL: "https://example.invalid/good", APIKey: pasteKeyB}}}},
		},
		{name: "invalid JSON", text: `{"kind": "custom", "endpoints": [}`, want: pasteResult{Notes: notes(pasteNote{Code: noteInvalidJSON}), Drafts: []pasteDraft{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkPaste(t, c.text, c.want) })
	}
}

// More than pasteMost drafts: the first ones, and too_many; what comes after the one past them is not read.
func TestParsePasteTooMany(t *testing.T) {
	var b strings.Builder
	var providers []string
	for i := range pasteMost + 1 {
		fmt.Fprintf(&b, "alias a%d='ANTHROPIC_BASE_URL=https://gw%d.example.com claude'\n", i, i)
		providers = append(providers, fmt.Sprintf(`{"name": "p%d", "endpoints": [{"protocol": "openai-chat", "baseUrl": "https://p%d.example.com/v1"}]}`, i, i))
	}
	providers = append(providers, `{"kind": "preset"}`) // past the one past pasteMost drafts, not read: no cindy_kind
	for _, text := range []string{b.String(), `{"providers": [` + strings.Join(providers, ",") + `]}`} {
		res := parsePaste(text)
		if len(res.Drafts) != pasteMost || res.Drafts[pasteMost-1].Provider.BaseURL == "" ||
			!reflect.DeepEqual(res.Notes, notes(pasteNote{Code: noteTooMany})) {
			t.Fatalf("%d drafts, notes %+v", len(res.Drafts), res.Notes)
		}
	}
}

// Nothing of the text but the key comes back: each case's secret is in no part of the answer, and the answer says
// what was left out (the fragments).
func TestParsePasteLeavesSecretsOut(t *testing.T) {
	const gw = "export ANTHROPIC_BASE_URL=https://gw.example.com\nexport ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + "\n"
	const curl = "curl https://gw.example.com/v1/messages -H 'x-api-key: " + pasteKeyA + "' "
	cindy := func(endpoint string) string {
		return `{"kind": "custom", "name": "Ex", "auth": {"apiKey": "` + pasteKeyA + `"}, "endpoints": [` + endpoint + `]}`
	}
	const s = "sk-example-hidden"
	cases := []struct {
		name, text string
		want       []string // fragments of the answer
	}{
		// A header with a line break, a backslash or a $ in its value is unsafe (with a Bearer one in it, a credential);
		// one whose name is no plain token is not even named.
		{"cindy header value with a line break", cindy(`{"protocol": "anthropic-messages", "baseUrl": "https://gw.example.com",
			"headers": {"X-Org": "a\nAuthorization: Bearer ` + s + `", "X-Note": "a\nX-Other: ` + s + `", "X-Team": "blue"}}`),
			[]string{`"headers":"X-Team: blue"`, `{"name":"X-Note","reason":"unsafe"}`, `{"name":"X-Org","reason":"credential"}`}},
		{"cindy header name with a line break", cindy(`{"protocol": "anthropic-messages", "baseUrl": "https://gw.example.com",
			"headers": {"X-Org: a\nAuthorization": "Bearer ` + s + `", "X Org": "` + s + `"}}`),
			[]string{`"notes":[{"code":"header_unreadable"}]`}},
		{"curl header names that are no token", curl + `-H 'X Api Key: ` + s + `' -H '$X-Org: a\nAuthorization: Bearer ` + s + `' -H 'x.api.key: ` + s + `'`,
			[]string{`{"code":"header_unreadable"}`}},
		// ANSI-C quoting ($'…') is not read at all (shellcmd fails closed on it since #17598): the command makes no draft.
		{"curl with ANSI-C quoting", curl + `-H $'X-Org: a\nAuthorization: Bearer ` + s + `'`,
			[]string{`"drafts":[]`, `"notes":[{"code":"unrecognized"}]`}},
		{"curl header over two lines", curl + "-H 'X-Org: a\nAuthorization: Bearer " + s + "' -H 'X-Note: a\nX-Other: " + s + "'",
			[]string{`{"name":"X-Org","reason":"credential"}`, `{"name":"X-Note","reason":"unsafe"}`}},
		{"a literal backslash-n or a $ in ANTHROPIC_CUSTOM_HEADERS", gw + `export ANTHROPIC_CUSTOM_HEADERS="X-Org: a\nX-Other: ` + s + `"` + "\n" +
			"claude\nexport ANTHROPIC_CUSTOM_HEADERS='X-Team: $" + s + "'\nclaude\n",
			[]string{`{"name":"X-Org","reason":"unsafe"}`, `{"name":"X-Team","reason":"unsafe"}`}},
		// --opt=value names only the option.
		{"curl --opt=value", curl + "--header=Authorization:Bearer-" + s + " --user=me:" + s + " --oauth2-bearer=" + s + " --data-urlencode=key=" + s,
			[]string{`{"code":"curl_option","name":"--header"}`, `{"code":"curl_option","name":"--user"}`,
				`{"code":"curl_option","name":"--oauth2-bearer"}`, `{"code":"curl_option","name":"--data-urlencode"}`}},
		// Header names that hold a secret's word, however they are written.
		{"credential header names", curl + "-H 'XApiKey: " + s + "' -H 'X-AccessToken: " + s + "' -H 'X-Authentication: " + s +
			"' -H 'Authorisation: Bearer " + s + "' -H 'X-Session: " + s + "'",
			[]string{`{"name":"XApiKey","reason":"credential"}`, `{"name":"X-AccessToken","reason":"credential"}`,
				`{"name":"X-Authentication","reason":"credential"}`, `{"name":"Authorisation","reason":"credential"}`,
				`{"name":"X-Session","reason":"credential"}`}},
		{"cindy camelCase header names", cindy(`{"protocol": "anthropic-messages", "baseUrl": "https://gw.example.com",
			"headers": {"accessToken": "` + s + `", "authToken": "` + s + `"}}`),
			[]string{`{"name":"accessToken","reason":"credential"}`, `{"name":"authToken","reason":"credential"}`}},
		// Arguments that may hold a secret or a setting's text: none kept.
		{"args with a setting's JSON", `alias kimi='ANTHROPIC_BASE_URL=https://gw.example.com claude --settings "{\"env\":{\"OTHER\":\"` + s + `\"}}"'`,
			[]string{`{"code":"args_left_out"}`}},
		{"args with a key", gw + "claude --api-key " + s + " --mcp-config '{\"headers\":{\"Authorization\":\"Bearer " + s + "\"}}'",
			[]string{`{"code":"args_left_out"}`}},
		{"args with =", gw + "claude --append=" + s, []string{`{"code":"args_left_out"}`}},
		{"args after a flag named like a key", gw + "claude --gateway-key " + s, []string{`{"code":"args_left_out"}`}},
		// An address with a query or fragment makes no draft; a proxy with one is left out.
		{"shell address with a query", "alias q='ANTHROPIC_BASE_URL=https://gw.example.com/?key=" + s + " claude'\n" +
			"export ANTHROPIC_BASE_URL='https://gw.example.com/#" + s + "'\nclaude\n",
			[]string{`{"code":"url_query","name":"q"}`, `{"code":"url_query","line":3}`}},
		{"settings address with a query", `{"env": {"ANTHROPIC_BASE_URL": "https://gw.example.com/?key=` + s + `"}}`,
			[]string{`"notes":[{"code":"url_query"}]`}},
		{"cindy address with a query", cindy(`{"protocol": "openai-chat", "baseUrl": "https://gw.example.com/v1?key=` + s + `#` + s + `"}`),
			[]string{`"drafts":[]`, `{"code":"url_query","name":"Ex"}`}},
		{"proxy with a query", gw + "export HTTPS_PROXY='http://proxy.example.com:8080/?token=" + s + "' HTTP_PROXY=http://plain.example.com:1\n",
			[]string{`{"name":"HTTPS_PROXY","reason":"credential"}`, `"auth":"bearer","api_key":"` + pasteKeyA + `"}`}},
		{"curl proxy with a query", curl + "-x 'http://proxy.example.com:1/?token=" + s + "'", []string{`{"code":"curl_option","name":"-x"}`}},
		// Variables named like secrets, beyond the import's words.
		{"variable names", gw + "export GITHUB_PAT=" + s + " OPENAIKEY=" + s + " ACCESSTOKEN=" + s + " RELAY_AUTH=" + s + " X_AUTHORIZATION=" + s +
			" DB_PASS=" + s + " JWT=" + s + " BEARER=" + s + " SESSIONID=" + s + " ANTHROPIC_APIKEY2=" + s + " MY_PWD=" + s +
			" CLAUDE_CODE_MAX_OUTPUT_TOKENS=64000\n",
			[]string{`"env":"CLAUDE_CODE_MAX_OUTPUT_TOKENS=64000"`, `{"name":"ACCESSTOKEN","reason":"credential"}`,
				`{"name":"ANTHROPIC_APIKEY2","reason":"credential"}`, `{"name":"BEARER","reason":"credential"}`,
				`{"name":"DB_PASS","reason":"credential"}`, `{"name":"GITHUB_PAT","reason":"credential"}`,
				`{"name":"JWT","reason":"credential"}`, `{"name":"MY_PWD","reason":"credential"}`,
				`{"name":"OPENAIKEY","reason":"credential"}`, `{"name":"RELAY_AUTH","reason":"credential"}`,
				`{"name":"SESSIONID","reason":"credential"}`, `{"name":"X_AUTHORIZATION","reason":"credential"}`}},
		// Values shaped like a secret, whatever they are named, and variables of headers.
		{"variable values", gw + "export OPENROUTER=" + s + " GH=ghp_" + s + " RELAY=\"Bearer " + s + "\" OTEL_EXPORTER_OTLP_HEADERS='x-honeycomb-team=" + s +
			"' NOTE=plain\n",
			[]string{`"env":"NOTE=plain"`, `{"name":"GH","reason":"credential"}`, `{"name":"OPENROUTER","reason":"credential"}`,
				`{"name":"OTEL_EXPORTER_OTLP_HEADERS","reason":"credential"}`, `{"name":"RELAY","reason":"credential"}`}},
		{"header names and values", gw + "export ANTHROPIC_CUSTOM_HEADERS='X-Pwd: v" + s + "\nX-Hmac: v" + s + "\nX-Sig: v" + s + "\nX-Access: v" + s +
			"\nX-Sid: v" + s + "\nX-Otp: v" + s + "\nX-Api: v" + s + "\nX-Org: bearer " + s + "\nX-Ref: " + s + "\nX-Org-Id: org-1\nX-Title: My App'\n",
			[]string{`"headers":"X-Org-Id: org-1\nX-Title: My App"`, `{"name":"X-Pwd","reason":"credential"}`, `{"name":"X-Hmac","reason":"credential"}`,
				`{"name":"X-Sig","reason":"credential"}`, `{"name":"X-Access","reason":"credential"}`, `{"name":"X-Sid","reason":"credential"}`,
				`{"name":"X-Otp","reason":"credential"}`, `{"name":"X-Api","reason":"credential"}`, `{"name":"X-Org","reason":"credential"}`,
				`{"name":"X-Ref","reason":"credential"}`}},
		// A key with a line break in it is no key.
		{"a key running on", "curl https://gw.example.com/v1/messages -H 'x-api-key: " + pasteKeyB + "\nAuthorization: Bearer " + s + "'",
			[]string{`{"code":"no_key"}`}},
		// cindy names what it defines only.
		{"cindy values it does not define", `{"providers": [{"kind": "` + s + `"}, {"kind": "custom", "endpoints": [{"protocol": "` + s +
			`", "baseUrl": "https://a.example.com"}]}, {"kind": "custom", "endpoints": [{"protocol": "anthropic-messages",
			"baseUrl": "https://b.example.com"}, {"protocol": "` + s + `"}]}]}`,
			[]string{`"notes":[{"code":"cindy_kind"},{"code":"cindy_endpoint"}]`, `"notes":[{"code":"no_key"},{"code":"cindy_endpoint"}]`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := json.Marshal(parsePaste(c.text))
			if strings.Contains(string(b), s) {
				t.Fatalf("the answer carries the secret: %s", b)
			}
			for _, w := range c.want {
				if !strings.Contains(string(b), w) {
					t.Fatalf("no %s in %s", w, b)
				}
			}
		})
	}
}

// A paste of the body's size stays a small answer: reading stops past pasteMost launches, and notes and the
// variables left out of a draft are capped.
func TestParsePasteIsBounded(t *testing.T) {
	const gw = "export ANTHROPIC_BASE_URL=https://gw.example.com\n"
	var cmds, opts, headers, aliases, vars strings.Builder
	cmds.WriteString(gw)
	vars.WriteString(gw)
	for i := range 3000 {
		fmt.Fprintf(&cmds, "V%d=1\n", i)
		fmt.Fprintf(&opts, " --o%d", i)
		fmt.Fprintf(&headers, " -H 'X-Key-%d: v'", i)
		fmt.Fprintf(&aliases, "alias a%d=x\n", i)
		if i < 400 {
			fmt.Fprintf(&vars, "export V%d=1\n", i)
		}
	}
	cmds.WriteString(strings.Repeat("claude\n", 3000) + "fi\n") // past pasteEnvMost variables nothing is read
	vars.WriteString("claude\n")
	for _, c := range []struct {
		name, text   string
		drafts       int
		top          []pasteNote // nil: any, pasteNotesMost at most
		skipped      int         // variables and headers each draft names as left out
		skippedFirst string
	}{
		{"variables past the bound", cmds.String(), 1, notes(pasteNote{Code: noteTooMany}), pasteSkippedMost, "V143"},
		{"launches past the bound", gw + strings.Repeat("claude\n", pasteLaunchMost+1) + "fi\n", pasteMost, notes(pasteNote{Code: noteTooMany}), 0, ""},
		{"curl options", "curl https://gw.example.com/v1/messages" + opts.String(), 1, []pasteNote{}, 0, ""},
		{"curl headers", "curl https://gw.example.com/v1/messages" + headers.String(), 1, []pasteNote{}, pasteSkippedMost, "X-Key-0"},
		{"aliases of something else", aliases.String(), 0, nil, 0, ""},
		{"notes, then too many", aliases.String() + "export ANTHROPIC_BASE_URL=https://gw.example.com\n" + strings.Repeat("claude\n", pasteMost+1),
			pasteMost, nil, 0, ""},
		{"variables", vars.String(), 1, []pasteNote{}, pasteSkippedMost, "V143"},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := parsePaste(c.text)
			all := len(res.Notes)
			for _, d := range res.Drafts {
				all += len(d.Notes)
				skipped, first := len(d.SkippedEnv)+len(d.SkippedHeaders), ""
				if len(d.SkippedEnv) > 0 {
					first = d.SkippedEnv[0].Name // by name, after the 50 the env takes
				} else if len(d.SkippedHeaders) > 0 {
					first = d.SkippedHeaders[0].Name
				}
				if skipped != c.skipped || first != c.skippedFirst {
					t.Fatalf("%d named as left out, the first %q", skipped, first)
				}
			}
			tooMany := len(res.Drafts) == pasteMost && res.Notes[len(res.Notes)-1] != pasteNote{Code: noteTooMany} // kept past the cap
			if tooMany || len(res.Drafts) != c.drafts || all > pasteNotesMost || c.top != nil && !reflect.DeepEqual(res.Notes, c.top) {
				t.Fatalf("%d drafts, %d notes, top %v", len(res.Drafts), all, res.Notes[:min(len(res.Notes), 3)])
			}
		})
	}
}

// Arguments are left out for an option named for a secret or a value shaped like one, not for words that only
// contain one (bypassPermissions, --fork-session).
func TestParsePasteArgs(t *testing.T) {
	for _, c := range []struct {
		args string
		kept []string
	}{
		{"--permission-mode bypassPermissions --fork-session --continue", []string{"--permission-mode", "bypassPermissions", "--fork-session", "--continue"}},
		{`--model opus[1m] --add-dir ~/work --allowedTools "Bash(git:*)" --append-system-prompt "Be brief"`,
			[]string{"--model", "opus[1m]", "--add-dir", "~/work", "--allowedTools", "Bash(git:*)", "--append-system-prompt", "Be brief"}},
		{"--dangerously-skip-permissions --verbose", []string{"--dangerously-skip-permissions", "--verbose"}},
		{"--api-key hidden", nil},
		{"--apiKey hidden", nil},
		{"--token=hidden", nil},
		{"--auth hidden", nil},
		{"--client-secret hidden", nil},
		{"--resume sk-ant-hidden", nil},
		{"--header 'Authorization: Bearer hidden'", nil},
		{`--settings '{"a":1}'`, nil},
		{"--settings={}", nil},
		{"--append=plain --x=a=b", nil}, // a value with an = (KEY=value) may set anything
		{"--append=plain", []string{"--append=plain"}},
	} {
		res := parsePaste("export ANTHROPIC_BASE_URL=https://gw.example.com\nclaude " + c.args)
		d := res.Drafts[0]
		if !slices.Equal(d.Provider.Args, c.kept) || (c.kept == nil) != slices.Contains(d.Notes, pasteNote{Code: noteArgsLeftOut}) {
			t.Errorf("%s: args %q, notes %v", c.args, d.Provider.Args, d.Notes)
		}
	}
}

func TestNameWords(t *testing.T) {
	for name, want := range map[string][]string{
		"XApiKey": {"X", "API", "KEY"}, "bypassPermissions": {"BYPASS", "PERMISSIONS"}, "X-Org-Id": {"X", "ORG", "ID"},
		"HTTPReferer": {"HTTP", "REFERER"}, "sha256Sig": {"SHA256", "SIG"}, "x.api key": {"X", "API", "KEY"}, "--": nil,
	} {
		if got := nameWords(name); !slices.Equal(got, want) {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// Aliases are read however many there are (up to pasteLaunchMost): sixty on one gateway and key that differ only in
// the model are one draft with sixty models, and exports below them still count.
func TestParsePasteManyAliases(t *testing.T) {
	var b strings.Builder
	for i := range 60 {
		fmt.Fprintf(&b, "alias m%d='ANTHROPIC_MODEL=model-%d claude'\n", i, i)
	}
	b.WriteString("export ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + "\n")
	res := parsePaste(b.String())
	if len(res.Drafts) != 1 || len(res.Drafts[0].Provider.Models) != 60 || res.Drafts[0].Provider.APIKey != pasteKeyA || len(res.Notes) != 0 {
		t.Fatalf("%d drafts, notes %v", len(res.Drafts), res.Notes)
	}
}

func pasteRequest(s *Service, body string) *httptest.ResponseRecorder {
	return request(s, "POST", "/api/v1/launchers/import/parse", body)
}

func TestParsePasteRouteRefuses(t *testing.T) {
	s, _, _ := modelService(t)
	missing := pasteRequest(s, `{}`)
	for _, c := range []struct {
		body string
		code int
	}{
		{`{}`, 400},
		{`{"text": " \n\t "}`, 400},
		{`{"text": 5}`, 400},
		{`{"text": "` + strings.Repeat("x", 300<<10) + `"}`, 413},
	} {
		r := pasteRequest(s, c.body)
		// A refused text says what is missing, the same way each time, never what the decoder read.
		if r.Code != c.code || c.code == 400 && r.Body.String() != missing.Body.String() {
			t.Fatalf("%.40s: %d %s", c.body, r.Code, r.Body.String())
		}
	}
	if r := pasteRequest(s, `{"text": "hello"}`); r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"format":"","drafts":[],"notes":[{"code":"unrecognized"}]}` {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

// Matches: every stored provider on the draft's base URL (stored with a /v1 or not), the ones with its key first; a
// key that is a reference never matches. A shell alias's suggested name avoids every name Persona lists.
func TestParsePasteRouteMatchesAndNames(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	seal := func(key string) string {
		sealed, err := s.sealKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return sealed
	}
	ref := extension.SealedReference("op://vault/item/key")
	stored := []storedLauncher{
		{KeySealed: seal(pasteKeyB), ManagedLauncher: proto.ManagedLauncher{Name: "other_key", BaseURL: "https://gw.example.com", Model: "x"}},
		{KeySealed: seal(ref), ManagedLauncher: proto.ManagedLauncher{Name: "by_ref", BaseURL: "https://gw.example.com", Model: "x"}},
		{KeySealed: seal(pasteKeyA), DisplayName: "Team gateway", ManagedLauncher: proto.ManagedLauncher{Name: "kimi", BaseURL: "https://gw.example.com/v1", Model: "x"}},
		{KeySealed: seal(pasteKeyA), ManagedLauncher: proto.ManagedLauncher{Name: "elsewhere", BaseURL: "https://other.example.com", Model: "x"}},
		{ManagedLauncher: proto.ManagedLauncher{Name: "keyless", BaseURL: "https://gw.example.com", Model: "x"}},
	}
	if err := s.putSetting(ctx, launchersManagedKey, stored); err != nil {
		t.Fatal(err)
	}
	text := "alias kimi='ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + " claude'\n" +
		"alias kimi_2='ANTHROPIC_BASE_URL=https://gw.example.com claude'\n"
	cindy := `{"name": "R", "auth": {"apiKey": "` + strings.ReplaceAll(ref, "\x00", `\u0000`) + `"},
		"endpoints": [{"protocol": "anthropic-messages", "baseUrl": "https://gw.example.com"}]}`
	for _, c := range []struct {
		text  string
		names []string
		want  [][]pasteMatch
	}{
		{text, []string{"kimi-2", "kimi_2"}, [][]pasteMatch{
			{{Name: "kimi", DisplayName: "Team gateway", SameKey: true}, {Name: "other_key"}, {Name: "by_ref"}, {Name: "keyless"}},
			{{Name: "other_key"}, {Name: "by_ref"}, {Name: "kimi", DisplayName: "Team gateway"}, {Name: "keyless"}},
		}},
		{cindy, []string{""}, [][]pasteMatch{{{Name: "other_key"}, {Name: "by_ref"}, {Name: "kimi", DisplayName: "Team gateway"}, {Name: "keyless"}}}},
	} {
		body, _ := json.Marshal(map[string]string{"text": c.text})
		r := pasteRequest(s, string(body))
		var out pasteResult
		if err := json.Unmarshal(r.Body.Bytes(), &out); r.Code != 200 || err != nil || len(out.Drafts) != len(c.want) {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		for i, d := range out.Drafts {
			if d.Provider.Name != c.names[i] || !reflect.DeepEqual(d.Matches, c.want[i]) {
				t.Fatalf("draft %d: %q %+v", i, d.Provider.Name, d.Matches)
			}
		}
		if strings.Contains(r.Body.String(), pasteKeyB) || strings.Contains(r.Body.String(), "op://") && !strings.Contains(c.text, "op://") {
			t.Fatalf("a stored key left the manager: %s", r.Body.String())
		}
	}
}

// A draft meets only the stored providers that call its API at its address (a Codex launcher is no direct Responses
// API), and only their keys are opened, only for a draft with a key.
func TestMatchPasteByAPI(t *testing.T) {
	const base, v1 = "https://gw.example.com", "https://gw.example.com/v1"
	stored := []storedLauncher{
		{KeySealed: "claude", ManagedLauncher: proto.ManagedLauncher{Name: "claude_gw", BaseURL: v1}}, // a Messages base: /v1 dropped
		{KeySealed: "omp", ManagedLauncher: proto.ManagedLauncher{Name: "omp_gw", Agent: "omp", BaseURL: base}},
		{KeySealed: "msg", ManagedLauncher: proto.ManagedLauncher{Name: "msg_api", Kind: "api", API: "messages", BaseURL: base}},
		{ManagedLauncher: proto.ManagedLauncher{Name: "keyless", BaseURL: base}},
		{KeySealed: "codex", ManagedLauncher: proto.ManagedLauncher{Name: "codex_gw", Agent: "codex", API: "responses", BaseURL: v1}},
		{KeySealed: "resp", ManagedLauncher: proto.ManagedLauncher{Name: "resp_api", Kind: "api", API: "responses", BaseURL: v1}},
		{KeySealed: "chat", ManagedLauncher: proto.ManagedLauncher{Name: "chat_api", Kind: "api", API: "chat", BaseURL: v1}},
		{KeySealed: "far", ManagedLauncher: proto.ManagedLauncher{Name: "far", BaseURL: "https://far.example.com"}},
	}
	keys := map[string]string{"claude": pasteKeyA, "omp": pasteKeyB, "msg": pasteKeyA, "codex": pasteKeyB, "resp": pasteKeyB,
		"chat": pasteKeyA, "far": pasteKeyA}
	var opened []string
	open := func(sealed string) (string, error) {
		opened = append(opened, sealed)
		return keys[sealed], nil
	}
	drafts := []pasteDraft{
		{Provider: pastedProvider{Kind: "launcher", Agent: "claude", API: "messages", BaseURL: base, APIKey: pasteKeyA}},
		{Provider: pastedProvider{Kind: "api", API: "responses", BaseURL: v1, APIKey: pasteKeyB}},
		{Provider: pastedProvider{Kind: "api", API: "chat", BaseURL: v1, APIKey: pasteKeyA}},
		{Provider: pastedProvider{Kind: "api", API: "chat", BaseURL: v1}}, // no key: nothing opened for it
	}
	if err := matchPaste(drafts, stored, open); err != nil {
		t.Fatal(err)
	}
	want := [][]pasteMatch{
		{{Name: "claude_gw", SameKey: true}, {Name: "msg_api", SameKey: true}, {Name: "omp_gw"}, {Name: "keyless"}},
		{{Name: "resp_api", SameKey: true}},
		{{Name: "chat_api", SameKey: true}},
		{{Name: "chat_api"}},
	}
	for i, d := range drafts {
		if !reflect.DeepEqual(d.Matches, want[i]) {
			t.Errorf("draft %d: %+v, want %+v", i, d.Matches, want[i])
		}
	}
	if want := []string{"claude", "omp", "msg", "resp", "chat"}; !slices.Equal(opened, want) {
		t.Fatalf("opened %v, want %v", opened, want)
	}
	failing := func(string) (string, error) { return "", errors.New("sealed with another key") }
	if err := matchPaste(drafts[:1], stored, failing); err == nil {
		t.Fatal("a key that does not open matched")
	}
}

// The answer carries the pasted key and no other secret of the text: not a credential variable, header, a proxy's or
// address's password, or curl's -u.
func TestParsePasteRouteKeepsSecrets(t *testing.T) {
	s, _, _ := modelService(t)
	const secret = "pw-example-secret"
	for _, text := range []string{
		"alias s='ANTHROPIC_BASE_URL=https://gw.example.com ANTHROPIC_AUTH_TOKEN=" + pasteKeyA + " AWS_SECRET_ACCESS_KEY=" + secret +
			" HTTPS_PROXY=http://u:" + secret + "@proxy.example.com:1 ANTHROPIC_CUSTOM_HEADERS=\"X-Org-Token: " + secret + "\" claude'\n" +
			"alias t='ANTHROPIC_BASE_URL=https://u:" + secret + "@gw.example.com claude'\n",
		`{"env": {"ANTHROPIC_BASE_URL": "https://gw.example.com", "ANTHROPIC_API_KEY": "` + pasteKeyA + `", "MY_PASSWORD": "` + secret + `",
			"ANTHROPIC_CUSTOM_HEADERS": "Authorization: Bearer ` + secret + `"}}`,
		"curl https://gw.example.com/v1/messages -H 'x-api-key: " + pasteKeyA + "' -H 'X-Session-Cookie: " + secret + "' -u u:" + secret +
			" -x http://u:" + secret + "@proxy.example.com:1 -H 'Authorization: Bearer " + secret + "'",
		"curl https://u:" + secret + "@gw.example.com/v1/messages",
		`{"providers": [{"name": "A", "auth": {"apiKey": "` + pasteKeyA + `"}, "endpoints": [{"protocol": "anthropic-messages",
			"baseUrl": "https://gw.example.com", "headers": {"X-Auth-Token": "` + secret + `"}}]},
			{"name": "B", "endpoints": [{"protocol": "anthropic-messages", "baseUrl": "https://u:` + secret + `@gw.example.com"}]}]}`,
	} {
		body, _ := json.Marshal(map[string]string{"text": text})
		r := pasteRequest(s, string(body))
		if r.Code != 200 || strings.Contains(r.Body.String(), secret) {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		if strings.Contains(text, pasteKeyA) && !strings.Contains(r.Body.String(), pasteKeyA) {
			t.Fatalf("the pasted key is not in the answer: %s", r.Body.String())
		}
	}
}

// alias on the import reads only that alias of the machine: one it does not have previews nothing and imports nothing.
func TestLauncherImportOneAlias(t *testing.T) {
	s, mux := importTestHub(t, importAliases, nil)
	get := func(alias string) []importedProvider {
		t.Helper()
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, httptest.NewRequest("GET", "/api/v1/launchers/import?machine=m1&alias="+alias, nil))
		var out struct {
			Providers []importedProvider `json:"providers"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &out); r.Code != 200 || err != nil || out.Providers == nil {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
		return out.Providers
	}
	if got := get("gcc2"); len(got) != 1 || !reflect.DeepEqual(got[0].Aliases, []string{"gcc2"}) || got[0].Variants != nil {
		t.Fatalf("%+v", got)
	}
	if got := get("nope"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if got := get(""); len(got) != 2 {
		t.Fatalf("without alias: %+v", got)
	}
	if r := importRequest(mux, "POST", `{"machine":"m1","alias":"kcc","providers":["gcc"]}`); r.Code != 400 {
		t.Fatalf("an alias not asked for imported: %d %s", r.Code, r.Body.String())
	}
	r := importRequest(mux, "POST", `{"machine":"m1","alias":"gcc2","providers":["gcc2"]}`)
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"created":["gcc2"],"updated":[],"hidden":[],"kept":[]}` {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if list := s.storedLaunchers(context.Background()); len(list) != 1 || len(list[0].Models) != 1 || list[0].Models[0].ID != "glm-air" {
		t.Fatalf("%+v", list)
	}
}

func TestParsePasteShellEnvironmentReset(t *testing.T) {
	for _, launch := range []string{
		"env -i ANTHROPIC_BASE_URL=https://explicit.example.com claude",
		"alias clean='env --ignore-environment ANTHROPIC_BASE_URL=https://explicit.example.com claude'",
		"alias clean='export ANTHROPIC_MODEL=old; env - ANTHROPIC_BASE_URL=https://explicit.example.com claude'",
	} {
		got := parsePaste("export ANTHROPIC_BASE_URL=https://old.example.com ANTHROPIC_API_KEY=" + pasteKeyA + " ANTHROPIC_MODEL=old\n" + launch)
		if len(got.Drafts) != 1 {
			t.Fatalf("%s: got %d drafts", launch, len(got.Drafts))
		}
		p := got.Drafts[0].Provider
		if p.BaseURL != "https://explicit.example.com" || p.APIKey != "" || p.Model != "" {
			t.Errorf("%s: incorrect provider after environment reset", launch)
		}
	}
}
