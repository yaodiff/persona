package node

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func sendRawHTTP(t *testing.T, baseURL, method, rawTarget string) int {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse baseURL %q: %v", baseURL, err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", u.Host, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", method, rawTarget, u.Host)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write raw request %q: %v", rawTarget, err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatalf("read response for %q: %v", rawTarget, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestGateProxyRejectsTraversalAndAmbiguousPaths(t *testing.T) {
	api := newFakeAPI(t)
	n, _, _ := ledgerNode(t, api.URL)
	n.larkCfg.Gates[0].PathPrefix = "/agent/v1"
	n.larkCfg.Gates[0].Rules = []proto.LarkRule{
		{Method: http.MethodGet, Path: "/secrets/**", Action: "deny"},
		{Method: http.MethodGet, Path: "**", Action: "allow"},
	}
	p := n.gates()
	base, release, err := p.open("ex", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	keyPrefix := strings.TrimSuffix(u.Path, "/")

	badPaths := []struct {
		name string
		path string
	}{
		{"duplicate_slash_write_get", "/agent/v1//maintenance/export"},
		{"dot_segment_write_get", "/agent/v1/./maintenance/export"},
		{"dotdot_write_get", "/agent/v1/chats/../maintenance/export"},
		{"encoded_dotdot", "/agent/v1/chats/%2e%2e/maintenance/export"},
		{"encoded_dot_mixed", "/agent/v1/chats/.%2E/maintenance/export"},
		{"encoded_dot_single", "/agent/v1/%2e/maintenance/export"},
		{"encoded_slash", "/agent/v1/maintenance%2fexport"},
		{"encoded_slash_upper", "/agent/v1/maintenance%2Fexport"},
		{"encoded_hash", "/agent/v1/maintenance/export%23/safe"},
		{"encoded_query", "/agent/v1/maintenance/export%3f/safe"},
		{"encoded_backslash", "/agent/v1/maintenance%5cexport"},
		{"encoded_nul", "/agent/v1/maintenance/export%00"},
		{"leading_dotdot_escape", "/../admin/secret"},
		{"leading_duplicate_slash_deny", "//secrets/key"},
		{"traversal_to_denied_route", "/agent/v1/public/../secrets/key"},
		{"trailing_double_slash", "/agent/v1/me//"},
	}
	for _, tc := range badPaths {
		t.Run(tc.name, func(t *testing.T) {
			code := sendRawHTTP(t, base, http.MethodGet, keyPrefix+tc.path)
			if code != http.StatusBadRequest {
				t.Fatalf("GET %s = %d, want 400 Bad Request", tc.path, code)
			}
		})
	}
	if got := api.requests(); len(got) != 0 {
		t.Fatalf("malformed/traversal paths reached upstream: %+v", got)
	}

	goodPaths := []string{
		"/agent/v1/me",
		"/agent/v1/conversations/a%20b/messages",
		"/agent/v1/conversations/caf%C3%A9/messages",
		"/agent/v1/conversations/feed-1/messages/",
	}
	for _, gp := range goodPaths {
		if code := sendRawHTTP(t, base, http.MethodGet, keyPrefix+gp); code != http.StatusOK {
			t.Fatalf("legitimate GET %s = %d, want 200 OK", gp, code)
		}
	}
	if got := api.requests(); len(got) != len(goodPaths) {
		t.Fatalf("upstream received %d legitimate requests, want %d", len(got), len(goodPaths))
	}
}

func TestParseGateCallRejectsTraversalAndAmbiguousPaths(t *testing.T) {
	g := proxiedGate("http://example.invalid")
	g.PathPrefix = "/agent/v1"
	bad := []string{
		"/agent/v1//maintenance/export",
		"/agent/v1/./maintenance/export",
		"/agent/v1/chats/../maintenance/export",
		"/agent/v1/chats/%2e%2e/maintenance/export",
		"/agent/v1/maintenance%2fexport",
		"/agent/v1/maintenance%5cexport",
		"/agent/v1/maintenance/export%23/safe",
		"/agent/v1/maintenance/export%3f/safe",
		"//secrets/key",
	}
	for _, raw := range bad {
		if _, _, ok := parseGateCall(g, []string{http.MethodGet, raw}, nil, nil, t.TempDir()); ok {
			t.Errorf("parseGateCall accepted ambiguous/traversal path %q", raw)
		}
	}
	for _, good := range []string{
		"/agent/v1/me",
		"/agent/v1/conversations/a%20b/messages?limit=10",
		"conversations/feed-1/messages/",
	} {
		if _, _, ok := parseGateCall(g, []string{http.MethodGet, good}, nil, nil, t.TempDir()); !ok {
			t.Errorf("parseGateCall rejected legitimate path %q", good)
		}
	}
}
