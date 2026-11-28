package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
)

func TestChatSendsDefaultUserAgent(t *testing.T) {
	for _, tt := range []struct {
		name, api, path, response string
		bearer, retry             bool
	}{
		{"messages", "", "/v1/messages", `{"content":[{"type":"text","text":"ok"}]}`, false, false},
		{"messages bearer", "", "/v1/messages", `{"content":[{"type":"text","text":"ok"}]}`, true, false},
		{"responses", "responses", "/v1/responses", "data: {\"type\":\"response.completed\"}\n\n", false, false},
		{"chat completions retry", "chat", "/v1/chat/completions", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan string, 3)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tt.path {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				requests <- r.UserAgent()
				if tt.retry && len(requests) == 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"max_tokens is unsupported; use max_completion_tokens"}}`))
					return
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer srv.Close()
			_, err := New(Config{API: tt.api, BaseURL: srv.URL, APIKey: "k", Model: "m", Bearer: tt.bearer}).Chat(
				context.Background(), Request{Messages: []Message{{RoleUser, "hi"}}, MaxTokens: 42})
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if tt.retry {
				wantCount = 2
			}
			if len(requests) != wantCount {
				t.Fatalf("got %d requests, want %d", len(requests), wantCount)
			}
			for i := 0; i < wantCount; i++ {
				if got, want := <-requests, modelhttp.UserAgent(); got != want {
					t.Errorf("request %d User-Agent = %q, want %q", i+1, got, want)
				}
			}
		})
	}
}

type countingTransport struct {
	calls atomic.Int32
}

func (tr *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.calls.Add(1)
	return http.DefaultTransport.RoundTrip(req)
}

func TestRedirectUserAgent(t *testing.T) {
	const custom = "private-launcher-agent"
	for _, api := range []string{"messages", "responses", "chat"} {
		for _, crossOrigin := range []bool{false, true} {
			t.Run(api+"/cross="+fmt.Sprint(crossOrigin), func(t *testing.T) {
				var first, second *httptest.Server
				var got []string
				first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got = append(got, r.UserAgent())
					if r.Method != http.MethodPost {
						t.Errorf("method = %s, want POST", r.Method)
					}
					switch r.URL.Path {
					case "/v1/messages", "/v1/responses", "/v1/chat/completions":
						http.Redirect(w, r, "/same", http.StatusTemporaryRedirect)
					case "/same":
						if crossOrigin {
							http.Redirect(w, r, second.URL+"/first", http.StatusTemporaryRedirect)
							return
						}
						fallthrough
					case "/final":
						switch api {
						case "messages":
							fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
						case "responses":
							fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
						case "chat":
							fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						}
					default:
						t.Errorf("unexpected path %s", r.URL.Path)
					}
				}))
				defer first.Close()
				second = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got = append(got, r.UserAgent())
					if r.Method != http.MethodPost {
						t.Errorf("method = %s, want POST", r.Method)
					}
					if r.URL.Path == "/first" {
						http.Redirect(w, r, "/second", http.StatusTemporaryRedirect)
					} else {
						http.Redirect(w, r, first.URL+"/final", http.StatusTemporaryRedirect)
					}
				}))
				defer second.Close()
				transport := &countingTransport{}
				c := NewWith(Config{API: api, BaseURL: first.URL, APIKey: "k", Model: "m", UserAgent: strPtr(custom), Headers: map[string]string{"User-Agent": custom, "X-Custom-Header": "secret"}}, transport)
				reply, err := c.Complete(context.Background(), "system", "hello", 8)
				if err != nil || reply != "ok" {
					t.Fatalf("reply = %q, error = %v", reply, err)
				}
				want := []string{custom, custom}
				if crossOrigin {
					want = append(want, modelhttp.UserAgent(), modelhttp.UserAgent(), modelhttp.UserAgent())
				}
				if !reflect.DeepEqual(got, want) || int(transport.calls.Load()) != len(want) {
					t.Errorf("User-Agents = %q, transport calls = %d; want %q", got, transport.calls.Load(), want)
				}
			})
		}
	}
}

func TestRedirectStripsCredentials(t *testing.T) {
	const apiKey = "sk-secret-redirect-test"
	for _, tc := range []struct {
		name     string
		api      string
		bearer   bool
		wantKey  string
		wantAuth string
	}{
		{name: "messages/x-api-key", api: "messages", wantKey: apiKey},
		{name: "messages/bearer", api: "messages", bearer: true, wantAuth: "Bearer " + apiKey},
		{name: "responses", api: "responses", wantAuth: "Bearer " + apiKey},
		{name: "chat", api: "chat", wantAuth: "Bearer " + apiKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type hop struct {
				host string
				path string
				key  string
				auth string
			}
			var first, second *httptest.Server
			var hops []hop
			first = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hops = append(hops, hop{
					host: "first",
					path: r.URL.Path,
					key:  r.Header.Get("X-Api-Key"),
					auth: r.Header.Get("Authorization"),
				})
				switch r.URL.Path {
				case "/v1/messages", "/v1/responses", "/v1/chat/completions":
					http.Redirect(w, r, "/same", http.StatusTemporaryRedirect)
				case "/same":
					http.Redirect(w, r, second.URL+"/hop1", http.StatusTemporaryRedirect)
				case "/final":
					switch tc.api {
					case "messages":
						fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
					case "responses":
						fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
					case "chat":
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				default:
					t.Errorf("unexpected path on first: %s", r.URL.Path)
				}
			}))
			defer first.Close()
			second = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hops = append(hops, hop{
					host: "second",
					path: r.URL.Path,
					key:  r.Header.Get("X-Api-Key"),
					auth: r.Header.Get("Authorization"),
				})
				if r.URL.Path == "/hop1" {
					http.Redirect(w, r, "/hop2", http.StatusTemporaryRedirect)
				} else {
					http.Redirect(w, r, first.URL+"/final", http.StatusTemporaryRedirect)
				}
			}))
			defer second.Close()

			c := New(Config{API: tc.api, BaseURL: first.URL, APIKey: apiKey, Bearer: tc.bearer, Model: "m"})
			reply, err := c.Complete(context.Background(), "system", "hello", 8)
			if err != nil || reply != "ok" {
				t.Fatalf("reply = %q, error = %v", reply, err)
			}
			want := []hop{
				{host: "first", path: apiPathForTest(tc.api), key: tc.wantKey, auth: tc.wantAuth},
				{host: "first", path: "/same", key: tc.wantKey, auth: tc.wantAuth},
				{host: "second", path: "/hop1", key: "", auth: ""},
				{host: "second", path: "/hop2", key: "", auth: ""},
				{host: "first", path: "/final", key: "", auth: ""},
			}
			if !reflect.DeepEqual(hops, want) {
				t.Errorf("hops = %+v\nwant = %+v", hops, want)
			}
		})
	}

	t.Run("checkRedirectHeaders", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			originURL string
			nextURL   string
			keepAuth  bool
		}{
			{name: "same-origin-default-port", originURL: "https://api.example.com/v1/messages", nextURL: "https://api.example.com:443/v1/other", keepAuth: true},
			{name: "same-origin-ipv6-default-port", originURL: "https://[::1]:443/v1/messages", nextURL: "https://[::1]/v1/other", keepAuth: true},
			{name: "cross-domain", originURL: "https://api.example.com/v1/messages", nextURL: "https://evil.example.net/steal", keepAuth: false},
			{name: "subdomain", originURL: "https://api.example.com/v1/messages", nextURL: "https://sub.api.example.com/v1/messages", keepAuth: false},
			{name: "scheme-downgrade", originURL: "https://api.example.com/v1/messages", nextURL: "http://api.example.com/v1/messages", keepAuth: false},
			{name: "scheme-upgrade", originURL: "http://api.example.com/v1/messages", nextURL: "https://api.example.com/v1/messages", keepAuth: false},
			{name: "different-port", originURL: "https://api.example.com/v1/messages", nextURL: "https://api.example.com:8443/v1/messages", keepAuth: false},
			{name: "ipv6-different-port", originURL: "https://[::1]:443/v1/messages", nextURL: "https://[::1]:8443/v1/messages", keepAuth: false},
			{name: "userinfo-spoof", originURL: "https://api.example.com/v1/messages", nextURL: "https://api.example.com@evil.example.net/v1/messages", keepAuth: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				originReq := httptest.NewRequest(http.MethodPost, tc.originURL, nil)
				next := httptest.NewRequest(http.MethodPost, tc.nextURL, nil)
				next.Header.Set("X-Api-Key", apiKey)
				next.Header.Set("Authorization", "Bearer "+apiKey)
				next.Header.Set("Proxy-Authorization", "Basic secret")
				next.Header.Set("Cookie", "sid=secret")
				next.Header.Set("Cookie2", "$Version=1")
				if err := checkRedirectHeaders(next, []*http.Request{originReq}, nil); err != nil {
					t.Fatalf("checkRedirectHeaders error = %v", err)
				}
				for _, h := range []string{"X-Api-Key", "Authorization", "Proxy-Authorization", "Cookie", "Cookie2"} {
					got := next.Header.Get(h)
					if tc.keepAuth && got == "" {
						t.Errorf("header %s was cleared on same-origin redirect", h)
					}
					if !tc.keepAuth && got != "" {
						t.Errorf("header %s leaked across origin: %q", h, got)
					}
				}
			})
		}

		// Empty via slice and nil URLs must not panic.
		next := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
		if err := checkRedirectHeaders(next, nil, nil); err != nil {
			t.Fatalf("empty via error = %v", err)
		}
		if sameOrigin(nil, next.URL) || sameOrigin(next.URL, nil) || sameOrigin(nil, nil) {
			t.Fatal("sameOrigin with nil URL returned true")
		}
	})
}

func apiPathForTest(api string) string {
	switch api {
	case "responses":
		return "/v1/responses"
	case "chat":
		return "/v1/chat/completions"
	default:
		return "/v1/messages"
	}
}

func strPtr(s string) *string { return &s }

func TestRedirectLimit(t *testing.T) {
	for _, redirects := range []int{9, 10} {
		t.Run(fmt.Sprint(redirects), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls <= redirects {
					http.Redirect(w, r, "/", http.StatusFound)
				}
			}))
			defer s.Close()
			resp, err := New(Config{}).http.Get(s.URL)
			if resp != nil {
				resp.Body.Close()
			}
			if redirects == 9 && (err != nil || calls != 10) {
				t.Fatalf("calls = %d, error = %v", calls, err)
			}
			if redirects == 10 && (err == nil || !strings.Contains(err.Error(), "stopped after 10 redirects") || calls != 10) {
				t.Fatalf("calls = %d, error = %v", calls, err)
			}
		})
	}
}

func TestSameOrigin(t *testing.T) {
	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{"http://example.com", "http://EXAMPLE.com:80", true},
		{"https://example.com", "https://example.com:443", true},
		{"http://example.com", "https://example.com", false},
		{"http://example.com", "http://other.example.com", false},
		{"http://example.com", "http://example.com:81", false},
	} {
		a, err := url.Parse(tt.a)
		if err != nil {
			t.Fatal(err)
		}
		b, err := url.Parse(tt.b)
		if err != nil {
			t.Fatal(err)
		}
		if got := sameOrigin(a, b); got != tt.want {
			t.Errorf("sameOrigin(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestChatSendsTheTurnsAndReadsTheUsage(t *testing.T) {
	var got struct {
		System   string    `json:"system"`
		Messages []Message `json:"messages"`
		Max      int       `json:"max_tokens"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"model":"m-2025","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5}}`))
	}))
	defer srv.Close()
	turns := []Message{{RoleUser, "a"}, {RoleAssistant, "b"}, {RoleUser, "c"}}
	r, err := New(Config{BaseURL: srv.URL, APIKey: "k", Model: "m"}).Chat(context.Background(), Request{System: "sys", Messages: turns, MaxTokens: 99})
	if err != nil {
		t.Fatal(err)
	}
	if got.System != "sys" || got.Max != 99 || !reflect.DeepEqual(got.Messages, turns) {
		t.Fatalf("request %+v", got)
	}
	want := Reply{Text: "ok", Model: "m-2025", Usage: Usage{InputTokens: 12, OutputTokens: 3, CacheReadInputTokens: 4, CacheCreationInputTokens: 5}}
	if r != want {
		t.Fatalf("reply %+v", r)
	}
}

func TestChatOnTheResponsesAPIReadsTheUsage(t *testing.T) {
	var got struct {
		Input []Message `json:"input"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		for _, ev := range []string{
			`{"type":"response.output_text.delta","delta":"hi"}`,
			`{"type":"response.completed","response":{"model":"gpt-x","usage":{"input_tokens":10,"output_tokens":2,"input_tokens_details":{"cached_tokens":6}}}}`,
		} {
			_, _ = w.Write([]byte("data: " + ev + "\n\n"))
		}
	}))
	defer srv.Close()
	r, err := New(Config{API: "responses", BaseURL: srv.URL, APIKey: "k", Model: "m"}).Chat(context.Background(),
		Request{System: "sys", Messages: []Message{{RoleUser, "a"}, {RoleAssistant, "b"}, {RoleUser, "c"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Input) != 4 || got.Input[0].Role != "developer" || got.Input[2] != (Message{RoleAssistant, "b"}) {
		t.Fatalf("input %+v", got.Input)
	}
	if want := (Reply{Text: "hi", Model: "gpt-x", Usage: Usage{InputTokens: 4, OutputTokens: 2, CacheReadInputTokens: 6}}); r != want {
		t.Fatalf("reply %+v", r)
	}
}

// A deadline on the context is the bound, not the client's 60 seconds; the model asked for stands when the upstream
// names none.
func TestChatGoesByTheContextsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"late"}]}`))
	}))
	defer srv.Close()
	c := New(Config{BaseURL: srv.URL + "/slow", APIKey: "k", Model: "m"})
	c.http.Timeout = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := c.Chat(ctx, Request{Messages: []Message{{RoleUser, "a"}}, MaxTokens: 5})
	if err != nil || r.Text != "late" || r.Model != "m" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := c.Complete(context.Background(), "", "a", 5); err == nil {
		t.Fatal("without a deadline the client's timeout holds")
	}
}
