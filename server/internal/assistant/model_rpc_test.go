package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestExtensionIdentityKeepsModelPurposeWithoutModelCalls(t *testing.T) {
	s, f, calls := extensionModelService(t, "unused")
	f.modelExts = []assistantwire.ModelExtension{{ID: "curator", Name: "Curator"}}
	if r := modelRequest(s, "PATCH", "extension.curator", `{"revision":0,"model":"stored-choice"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	f.modelExts = []assistantwire.ModelExtension{{ID: "renamed-curator", StorageID: "curator", Name: "Renamed Curator"}}
	p := purposeOf(t, s, "extension.curator")
	if p.Name != "Renamed Curator" || p.Override.Model != "stored-choice" {
		t.Fatalf("stored purpose lost: %+v", p)
	}
	if _, ok := s.extensionPurposeDef(context.Background(), "extension.renamed-curator"); ok {
		t.Fatal("second model purpose created")
	}
	if len(calls()) != 0 {
		t.Fatal("identity preference test called a model")
	}
	out, handled, err := s.modelsRequest(t.Context(), assistantwire.MethodExtensionIdentity, json.RawMessage(`{"future":true}`))
	if data, marshalErr := json.Marshal(out); !handled || err != nil || marshalErr != nil || string(data) != `{"supported":true}` {
		t.Fatalf("identity capability: %s, handled %v, error %v, marshal error %v", data, handled, err, marshalErr)
	}
}

// Model calls by extensions (model_rpc.go, #6102).

// upstreamCall is one request the fake Messages API got.
type upstreamCall struct {
	Model     string         `json:"model"`
	System    string         `json:"system"`
	MaxTokens int            `json:"max_tokens"`
	Messages  []ai.Message   `json:"messages"`
	Header    http.Header    `json:"-"`
	Raw       map[string]any `json:"-"`
}

// extensionModelService is an assistant whose hub.json "ai" (what "Knowledge base" follows) is a fake Messages API
// answering with replies in turn (the last one again after them), and an extension kb-curator granted models.
func extensionModelService(t *testing.T, replies ...string) (*Service, *fakeHub, func() []upstreamCall) {
	t.Helper()
	s, f, _ := modelService(t)
	var mu sync.Mutex
	var calls []upstreamCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		var c upstreamCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		c.Header = r.Header
		mu.Lock()
		calls = append(calls, c)
		reply := replies[min(len(calls), len(replies))-1]
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": c.Model + "-answered", "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": reply}},
			"usage":   map[string]any{"input_tokens": 100, "output_tokens": 10, "cache_read_input_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("upstream-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.hello.Models.AI = ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "kb-model"}
	f.modelExts = []assistantwire.ModelExtension{{ID: "kb-curator", Name: "知识库整理"}}
	return s, f, func() []upstreamCall { mu.Lock(); defer mu.Unlock(); return append([]upstreamCall(nil), calls...) }
}

func completeAs(s *Service, ext, params string) (assistantwire.ModelCompleteResult, error) {
	raw, _ := json.Marshal(assistantwire.ExtensionComplete{Extension: ext, Params: json.RawMessage(params)})
	out, ok, err := s.modelsRequest(context.Background(), assistantwire.MethodModelsExtensionComplete, raw)
	var res assistantwire.ModelCompleteResult
	if !ok {
		return res, errors.New("not answered")
	}
	if err != nil {
		return res, err
	}
	b, _ := json.Marshal(out)
	_ = json.Unmarshal(b, &res)
	return res, nil
}

func rpcCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestExtensionPurposeFollowsTheKnowledgeBaseUntilPicked(t *testing.T) {
	s, f, _ := extensionModelService(t, "ok")
	ctx := context.Background()
	p := purposeOf(t, s, "extension.kb-curator")
	if p.CategoryID != "kb" || p.Parent != "kb" || p.Kind != "api" || p.Name != "知识库整理" || p.Effective.Model != "kb-model" ||
		len(p.Fields) == 0 || p.InheritedFrom == "" || p.Uses != nil {
		t.Fatalf("purpose %+v", p)
	}
	var list struct {
		Purposes []ModelPurpose `json:"purposes"`
	}
	_ = json.Unmarshal(modelRequest(s, "GET", "", "").Body.Bytes(), &list)
	if last := list.Purposes[len(list.Purposes)-1]; last.ID != "extension.kb-curator" {
		t.Fatalf("the list ends with %s", last.ID)
	}
	// The knowledge base's pick carries over; the extension's own then wins, and leaves the knowledge base alone.
	if r := modelRequest(s, "PATCH", "kb", `{"revision":0,"model":"kb-picked"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if b, _ := s.resolveModel(ctx, "extension.kb-curator"); b.Model != "kb-picked" {
		t.Fatalf("does not follow the knowledge base: %+v", b)
	}
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":0,"model":"curator-model"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if b, _ := s.resolveModel(ctx, "extension.kb-curator"); b.Model != "curator-model" {
		t.Fatalf("its own pick: %+v", b)
	}
	if b, _ := s.resolveModel(ctx, "kb"); b.Model != "kb-picked" {
		t.Fatalf("the knowledge base changed: %+v", b)
	}
	if p := purposeOf(t, s, "extension.kb-curator"); p.Override.Model != "curator-model" || p.Revision != 1 {
		t.Fatalf("after the pick: %+v", p)
	}
	// Switched off or no longer granted: no purpose.
	f.modelExts = nil
	if r := modelRequest(s, "GET", "extension.kb-curator", ""); r.Code != 404 {
		t.Fatalf("%d", r.Code)
	}
}

func TestExtensionCompleteRunsOnItsPurposeAndReportsUsage(t *testing.T) {
	s, _, calls := extensionModelService(t, "整理好了")
	if r := modelRequest(s, "PATCH", "extension.kb-curator", `{"revision":0,"model":"curator-model"}`); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	res, err := completeAs(s, "kb-curator", `{"system":"你是整理员","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}],"max_tokens":300,"label":"daily"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "整理好了" || res.Model != "curator-model-answered" || res.StopReason != "end_turn" || res.JSON != nil ||
		res.Usage != (assistantwire.ModelUsage{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 1, Requests: 1}) {
		t.Fatalf("result %+v", res)
	}
	c := calls()
	if len(c) != 1 || c[0].Model != "curator-model" || c[0].System != "你是整理员" || c[0].MaxTokens != 300 || len(c[0].Messages) != 3 ||
		c[0].Messages[1] != (ai.Message{Role: "assistant", Content: "b"}) || c[0].Header.Get("x-api-key") != "upstream-test-key" {
		t.Fatalf("upstream got %+v", c)
	}
	// Defaults: 4096 tokens, the knowledge base's model when nothing is picked.
	if _, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"a"}]}`); err != nil {
		t.Fatal(err)
	}
	if c := calls(); c[1].MaxTokens != 4096 || c[1].System != "" {
		t.Fatalf("%+v", c[1])
	}
}

func TestExtensionCompleteChecksTheReplyAgainstTheSchema(t *testing.T) {
	schema := `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}},"score":{"type":"integer","minimum":0}},"required":["tags","score"],"additionalProperties":false}`
	s, _, calls := extensionModelService(t, `{"tags":["a"]}`, "```json\n{\"tags\":[\"a\",\"b\"],\"score\":3}\n```")
	res, err := completeAs(s, "kb-curator", `{"system":"给这段话打标","messages":[{"role":"user","content":"一段话"}],"json_schema":`+schema+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.JSON) != `{"score":3,"tags":["a","b"]}` || res.Usage.Requests != 2 || res.Usage.InputTokens != 200 || res.Usage.OutputTokens != 20 {
		t.Fatalf("result %+v %s", res, res.JSON)
	}
	c := calls()
	if len(c) != 2 || len(c[1].Messages) != 3 || c[1].Messages[1].Content != `{"tags":["a"]}` || c[0].System != c[1].System {
		t.Fatalf("%+v", c)
	}
	golden(t, "model_complete_schema.system", c[0].System)
	golden(t, "model_complete_schema.retry", c[1].Messages[2].Content)
}

func TestExtensionCompleteGivesUpOnAReplyThatStillDoesNotMatch(t *testing.T) {
	s, _, calls := extensionModelService(t, "不是 JSON", `{"tags":"a"}`)
	_, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"x"}],"json_schema":{"type":"object","properties":{"tags":{"type":"array"}}}}`)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeInvalidOutput {
		t.Fatalf("%v", err)
	}
	var data assistantwire.ModelInvalidOutput
	if json.Unmarshal(re.Data, &data) != nil || !strings.Contains(data.Problem, "$.tags: should be array") || data.Text != `{"tags":"a"}` || data.Usage.Requests != 2 {
		t.Fatalf("%+v", data)
	}
	if len(calls()) != 2 {
		t.Fatal("tried more than once more")
	}
}

func TestExtensionCompleteRefusesBadParams(t *testing.T) {
	s, _, calls := extensionModelService(t, "ok")
	for _, params := range []string{
		``,
		`{}`,
		`{"messages":[]}`,
		`{"messages":[{"role":"system","content":"x"}]}`,
		`{"messages":[{"role":"user","content":" "}]}`,
		`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`,
		`{"messages":[{"role":"assistant","content":"b"},{"role":"user","content":"a"}]}`,
		`{"messages":[{"role":"user","content":"a"},{"role":"user","content":"b"}]}`,
		`{"messages":[{"role":"user","content":"a"}],"max_tokens":64001}`,
		`{"messages":[{"role":"user","content":"a"}],"max_tokens":-1}`,
		`{"messages":[{"role":"user","content":"a"}],"timeout_seconds":601}`,
		`{"messages":[{"role":"user","content":"a"}],"json_schema":"string"}`,
		`{"messages":[{"role":"user","content":"a"}],"json_schema":{"$ref":"#/$defs/missing"}}`,
		`{"messages":[{"role":"user","content":"a"}],"json_schema":{"$ref":"https://example.com/s.json"}}`,
		`{"messages":[{"role":"user","content":"a"}],"label":"` + strings.Repeat("x", 65) + `"}`,
		`{"messages":[{"role":"user","content":"a"}],"temperature":0.2}`,
		`{"messages":[{"role":"user","content":"a"}],"model":"anything"}`,
	} {
		if _, err := completeAs(s, "kb-curator", params); rpcCode(err) != bus.CodeInvalidParams {
			t.Errorf("%s: %v", params, err)
		}
	}
	if len(calls()) != 0 {
		t.Fatal("a refused call went out")
	}
}

func TestModelRPCDecodingCompatibility(t *testing.T) {
	s, _, _ := extensionModelService(t, "ok")
	for _, tc := range []struct{ method, params, message string }{
		{assistantwire.MethodModelsExtensionComplete, `{"extension":1}`, `no extension`},
		{assistantwire.MethodModelsExtensionComplete, `{"extension":"kb-curator"}`, `params are required`},
		{assistantwire.MethodModelsExtensionComplete, `{"extension":"kb-curator","params":{"future":true}}`, `json: unknown field "future"`},
		{assistantwire.MethodModelsPurposeComplete, ``, `EOF`},
		{assistantwire.MethodModelsPurposeComplete, `{"purpose":"subscription.recipe","future":true}`, `json: unknown field "future"`},
	} {
		out, handled, err := s.modelsRequest(t.Context(), tc.method, json.RawMessage(tc.params))
		var wireErr *rpc.Error
		if !handled || out != nil || !errors.As(err, &wireErr) || wireErr.Code != bus.CodeInvalidParams || wireErr.Message != "model.complete: "+tc.message {
			t.Errorf("%s %s: reply %v, handled %v, error %v", tc.method, tc.params, out, handled, err)
		}
	}
	_, handled, err := s.modelsRequest(t.Context(), assistantwire.MethodModelsExtensionComplete,
		json.RawMessage(`{"extension":"kb-curator","future":true,"params":{"messages":[{"role":"user","content":"example"}]}}`))
	if !handled || err != nil {
		t.Fatalf("extension envelope with an unknown field: handled %v, error %v", handled, err)
	}
}

func TestModelsUsableRejectsMalformedParams(t *testing.T) {
	s, _, _ := modelService(t)
	out, handled, err := s.modelsRequest(t.Context(), assistantwire.MethodModelsUsable, json.RawMessage(`{"purpose":`))
	var wireErr *rpc.Error
	if !handled || out != nil || !errors.As(err, &wireErr) || wireErr.Code != bus.CodeInvalidParams ||
		wireErr.Message != "models.usable: unexpected end of JSON input" {
		t.Fatalf("models.usable: reply %v, handled %v, error %v", out, handled, err)
	}
}

func TestExtensionCompleteWithNothingToCall(t *testing.T) {
	s, f, calls := extensionModelService(t, "ok")
	if _, err := completeAs(s, "other", `{"messages":[{"role":"user","content":"a"}]}`); rpcCode(err) != assistantwire.CodeModelUnavailable {
		t.Fatalf("an extension without the purpose: %v", err)
	}
	f.hello.Models.AI = ai.Config{}
	if _, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"a"}]}`); rpcCode(err) != assistantwire.CodeModelUnavailable {
		t.Fatalf("no connection: %v", err)
	}
	if len(calls()) != 0 {
		t.Fatal("sent")
	}
}

// A connection out of quota holds: model_paused with when to try again, and nothing more is sent until then.
func TestExtensionCompleteRespectsAHeldConnection(t *testing.T) {
	s, f, _ := modelService(t)
	var mu sync.Mutex
	sent := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		sent++
		mu.Unlock()
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"account_quota_exhausted","message":"quota exhausted"}}`))
	}))
	defer srv.Close()
	f.hello.Models.AI = ai.Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	f.hello.Models.AI.APIKeyFile = key
	f.modelExts = []assistantwire.ModelExtension{{ID: "kb-curator", Name: "x"}}
	for range 2 {
		_, err := completeAs(s, "kb-curator", `{"messages":[{"role":"user","content":"a"}]}`)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != assistantwire.CodeModelPaused {
			t.Fatalf("%v", err)
		}
		var p assistantwire.ModelPause
		if json.Unmarshal(re.Data, &p) != nil || p.Reason != ai.ReasonQuota || p.Until == "" {
			t.Fatalf("%s", re.Data)
		}
	}
	if sent != 1 {
		t.Fatalf("sent %d times while held", sent)
	}
	// A later fake model may get this same loopback address after the listener closes.
	other := newFakeHub()
	other.fakeModel(t)
	next := other.hello.Models.AI
	next.BaseURL = srv.URL
	if ai.Held(next) != nil {
		t.Fatal("an independent model fixture inherited the previous account's quota hold")
	}
}

func TestReplySchemaSubset(t *testing.T) {
	schema, err := compileSchema(json.RawMessage(`{"$defs":{"kind":{"enum":["a","b"]}},"type":"object",
		"properties":{"k":{"$ref":"#/$defs/kind"},"n":{"type":"number","exclusiveMaximum":1},"s":{"type":"string","maxLength":2},
		"list":{"type":"array","minItems":1,"items":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"c":{"const":"x"}},
		"additionalProperties":{"type":"boolean"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for reply, want := range map[string]string{
		`{"k":"a","n":0.5,"s":"中文","list":[1,null],"c":"x","extra":true}`: "",
		`{"k":"c"}`:               `$.k: should be one of ["a","b"]`,
		`{"n":1}`:                 "$.n: should be less than 1",
		`{"s":"abc"}`:             "$.s: should be at most 2 characters",
		`{"list":[]}`:             "$.list: should have at least 1 items, has 0",
		`{"list":[1.5]}`:          "$.list[0]: matches none of anyOf",
		`{"c":"y"}`:               `$.c: should be "x"`,
		`{"extra":1}`:             "$.extra: should be boolean, is a number",
		`[1]`:                     "$: should be object, is an array",
		`好的：{"k":"b"} 就这样`:        "",
		"```\n{\"k\":\"a\"}\n```": "",
	} {
		v, err := decodeReply(reply)
		if err != nil {
			t.Fatalf("%s: %v", reply, err)
		}
		got := validateSchema(schema, schema, v, "$", 0)
		if (want == "") != (got == "") || !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q, want %q", reply, got, want)
		}
	}
	if _, err := decodeReply("没有 JSON"); err == nil {
		t.Fatal("no JSON decoded")
	}
}

// The manager's own calls (models.purpose_complete): only its built-in purposes, on Information processing's model,
// with model.complete's schema check and retry.
func TestPurposeCompleteRunsTheManagersPurposeOnInformationProcessing(t *testing.T) {
	s, f, calls := extensionModelService(t, `{"tool":"x"`, `{"tool":"list","args":{}}`)
	call := func(purpose string, params assistantwire.ModelCompleteParams) (assistantwire.ModelCompleteResult, error) {
		raw, _ := json.Marshal(assistantwire.PurposeComplete{Purpose: purpose, Params: params})
		out, ok, err := s.modelsRequest(context.Background(), assistantwire.MethodModelsPurposeComplete, raw)
		var res assistantwire.ModelCompleteResult
		if !ok {
			return res, errors.New("not answered")
		}
		if err != nil {
			return res, err
		}
		b, _ := json.Marshal(out)
		_ = json.Unmarshal(b, &res)
		return res, nil
	}
	params := assistantwire.ModelCompleteParams{System: "pick a tool", Messages: []assistantwire.ModelMessage{{Role: "user", Content: "tools"}},
		JSONSchema: json.RawMessage(`{"type":"object","properties":{"tool":{"type":"string"},"args":{"type":"object"}},"required":["tool","args"]}`),
		Label:      "subscription pick"}
	p := purposeOf(t, s, assistantwire.PurposeSubscriptionRecipe)
	if p.CategoryID != "kb" || p.Kind != "api" || p.Effective.Model != "kb-model" || p.Name != "订阅方式起草" {
		t.Fatalf("purpose %+v", p)
	}
	res, err := call(assistantwire.PurposeSubscriptionRecipe, params)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.JSON) != `{"args":{},"tool":"list"}` || res.Usage.Requests != 2 {
		t.Fatalf("result %+v %s", res, res.JSON)
	}
	if c := calls(); len(c) != 2 || c[0].Model != "kb-model" || !strings.Contains(c[0].System, "pick a tool") || len(c[1].Messages) != 3 {
		t.Fatalf("upstream got %+v", c)
	}
	for _, purpose := range []string{"kb", "assistant", "extension.kb-curator", ""} {
		if _, err := call(purpose, params); rpcCode(err) != bus.CodeInvalidParams {
			t.Errorf("purpose %q: %v", purpose, err)
		}
	}
	if _, err := call(assistantwire.PurposeSubscriptionRecipe, assistantwire.ModelCompleteParams{}); rpcCode(err) != bus.CodeInvalidParams {
		t.Fatalf("no messages: %v", err)
	}
	if len(calls()) != 2 {
		t.Fatal("a refused call went out")
	}
	// models.usable names what it runs on, for the consent before a sample goes there.
	usable := func() string {
		raw, _ := json.Marshal(map[string]string{"purpose": assistantwire.PurposeSubscriptionRecipe})
		out, ok, err := s.modelsRequest(context.Background(), assistantwire.MethodModelsUsable, raw)
		if !ok || err != nil {
			t.Fatalf("models.usable: %v %v", ok, err)
		}
		reply, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		return string(reply)
	}
	provider, _ := json.Marshal(mc(s.lang(context.Background()), "provider_legacy_kb"))
	if got, want := usable(), `{"usable":true,"provider":`+string(provider)+`,"model":"kb-model"}`; got != want {
		t.Fatalf("models.usable = %s, want %s", got, want)
	}
	f.hello.Models.AI = ai.Config{}
	if _, err := call(assistantwire.PurposeSubscriptionRecipe, params); rpcCode(err) != assistantwire.CodeModelUnavailable {
		t.Fatalf("nothing set up: %v", err)
	}
	if got := usable(); got != `{"usable":false}` {
		t.Fatalf("models.usable with nothing set up: %s", got)
	}
}
