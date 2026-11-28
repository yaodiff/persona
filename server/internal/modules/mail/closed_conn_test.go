package mail

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// hangupProxy is a TCP proxy in front of an IMAP server that ends a connection right after the server answered a
// command that after picks: the command succeeds, and the next one finds the connection closed, the way a server
// that hangs up after some command does. QQ Mail does it after a keyword search: two accounts' searches failed
// with "fetch: use of closed network connection" on every try, the retry on a new login included.
type hangupProxy struct {
	ln     net.Listener
	after  func(conn int, command string) bool // conn counts the connections from 1
	conns  atomic.Int32
	hung   atomic.Int32
	script imapProxyScript

	mu   sync.Mutex
	open []net.Conn
}

func newHangupProxy(t *testing.T, target string, after func(conn int, command string) bool) *hangupProxy {
	t.Helper()
	return newScriptedIMAPProxy(t, target, after, imapProxyScript{})
}

type imapProxyScript struct {
	// before can answer a command on the proxy so the real server does not see it.
	before func(line string) (answer string)
	// reply can rewrite or drop server lines. A selected tagged reply still ends the connection.
	reply func(line string, hangup bool) string
}

func newScriptedIMAPProxy(t *testing.T, target string, after func(int, string) bool, script imapProxyScript) *hangupProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &hangupProxy{ln: ln, after: after, script: script}
	t.Cleanup(func() { ln.Close(); p.endAll() })
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				client.Close()
				continue
			}
			p.mu.Lock()
			p.open = append(p.open, client, server)
			p.mu.Unlock()
			go p.serve(int(p.conns.Add(1)), client, server)
		}
	}()
	return p
}

// keywordSearch is a UID SEARCH for words, the command QQ Mail hangs up after.
func keywordSearch(_ int, command string) bool {
	f := strings.Fields(strings.ToUpper(command))
	return len(f) > 3 && f[1] == "UID" && f[2] == "SEARCH" && slices.Contains(f, "TEXT")
}

// selects reports whether command selects a mailbox (EXAMINE is a read-only SELECT).
func selects(command string) bool {
	f := strings.Fields(strings.ToUpper(command))
	return len(f) > 1 && (f[1] == "SELECT" || f[1] == "EXAMINE")
}

// serve relays one connection line by line, and ends it after the tagged answer to a command p.after picks.
func (p *hangupProxy) serve(n int, client, server net.Conn) {
	var mu sync.Mutex
	tag := ""
	var hungUp atomic.Bool
	go func() {
		r := bufio.NewReader(client)
		for {
			line, err := r.ReadString('\n')
			if hungUp.Load() {
				// Drain what the client sent after the hang-up: closing a socket with unread input sends a reset,
				// and the client would read "connection reset" instead of the server's clean close.
				if err != nil {
					client.Close()
					return
				}
				continue
			}
			if p.script.before != nil {
				if answer := p.script.before(line); answer != "" {
					if _, werr := client.Write([]byte(answer)); werr != nil {
						return
					}
					continue
				}
			}
			if f := strings.Fields(line); len(f) > 1 && p.after != nil && p.after(n, line) {
				mu.Lock()
				tag = f[0]
				mu.Unlock()
			}
			if _, werr := server.Write([]byte(line)); (werr != nil && !hungUp.Load()) || err != nil {
				return
			}
		}
	}()
	r := bufio.NewReader(server)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		mu.Lock()
		hangup := tag != "" && strings.HasPrefix(line, tag+" ")
		mu.Unlock()
		if p.script.reply != nil {
			line = p.script.reply(line, hangup)
		}
		if line != "" {
			if _, werr := client.Write([]byte(line)); werr != nil {
				return
			}
		}
		if hangup {
			p.hung.Add(1)
			hungUp.Store(true)
			server.Close()
			// End the client's side with a clean close (FIN); the reader above drains and then closes it.
			if tc, ok := client.(*net.TCPConn); ok {
				tc.CloseWrite()
			} else {
				client.Close()
			}
			return
		}
	}
}

// endAll ends every connection open so far.
func (p *hangupProxy) endAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.open {
		c.Close()
	}
}

// captureLog collects what logf writes for the rest of the test.
func captureLog(t *testing.T) func() []string {
	var (
		mu    sync.Mutex
		lines []string
	)
	old := logf
	logf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	t.Cleanup(func() { logf = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(lines)
	}
}

// proxiedService is a Service with one account whose connections go through a hangupProxy.
func proxiedService(t *testing.T, after func(conn int, command string) bool) (*Service, *testIMAP, *hangupProxy, *atomic.Int32) {
	t.Helper()
	srv := newTestIMAP(t)
	s := newServiceWithAccount(t, srv)
	proxy := newHangupProxy(t, fmt.Sprintf("%s:%d", srv.host, srv.port), after)
	return s, srv, proxy, countLogins(s, proxy.ln.Addr().String())
}

// A server that ends the connection after every keyword search still gets the search answered: the messages it
// found are fetched on a new login instead of searching again, which would end that connection too. The failed try
// is logged with the account, the operation, the step and the raw error.
func TestSearchThatEndsTheConnectionIsFetchedOnANewLogin(t *testing.T) {
	logs := captureLog(t)
	s, srv, proxy, logins := proxiedService(t, keywordSearch)
	srv.appendMessage(t, "INBOX", rawMessage("Invoice", "a@example.com", "the invoice for March"))
	srv.appendMessage(t, "INBOX", rawMessage("Lunch", "b@example.com", "see you at noon"))

	res, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Query: "invoice"})
	t.Logf("search: %d logins, %d connections ended by the server; log %q", logins.Load(), proxy.hung.Load(), logs())
	if err != nil {
		t.Fatalf("search on a server that ends the connection after it: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Subject != "Invoice" {
		t.Fatalf("messages = %+v, want the one invoice", res.Messages)
	}
	if n := logins.Load(); n != 2 {
		t.Fatalf("%d logins, want 2 (one reconnect)", n)
	}
	if n := proxy.hung.Load(); n != 1 {
		t.Fatalf("the server ended %d connections, want 1: the retry searched again", n)
	}
	if !hasLine(logs(), "mail: acc_test: search INBOX failed at fetch (try 1 of 2, reconnecting): ", "closed") {
		t.Fatalf("no line for the failed try in %q", logs())
	}

	// The new login is healthy: the next search uses it, and the server ends it after that search too.
	if _, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Query: "lunch"}); err != nil {
		t.Fatalf("second search: %v", err)
	}
}

// Reading a message on a connection that ends under it reconnects once and reads it on the new login.
func TestReadOnAConnectionThatEndsIsRetriedOnce(t *testing.T) {
	logs := captureLog(t)
	// The first connection ends once the server answered its SELECT, so the FETCH after it finds it closed.
	s, srv, _, logins := proxiedService(t, func(conn int, command string) bool { return conn == 1 && selects(command) })
	uid := srv.appendMessage(t, "INBOX", rawMessage("Hello", "a@example.com", "the body"))

	msg, err := s.GetMessage(context.Background(), "acc_test", uint32(uid), "INBOX")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if msg.Subject != "Hello" || !strings.Contains(msg.Text, "the body") {
		t.Fatalf("message = %+v", msg)
	}
	if n := logins.Load(); n != 2 {
		t.Fatalf("%d logins, want 2", n)
	}
	if !hasLine(logs(), "mail: acc_test: read INBOX failed at fetch (try 1 of 2, reconnecting): ", "") {
		t.Fatalf("no line for the failed try in %q", logs())
	}
}

// A cached connection that ended while it was not in use (the server hung up) is replaced before the operation
// runs: no NOOP and no failed try are spent on it, and the log says it had ended.
func TestEndedCachedConnectionIsReplacedWithoutATry(t *testing.T) {
	logs := captureLog(t)
	s, srv, proxy, logins := proxiedService(t, func(int, string) bool { return false })
	uid := srv.appendMessage(t, "INBOX", rawMessage("Hello", "a@example.com", "x"))
	ctx := context.Background()
	if _, err := s.ListMessages(ctx, ListOptions{AccountID: "acc_test"}); err != nil {
		t.Fatalf("first list: %v", err)
	}
	s.mu.Lock()
	cached := s.conns["acc_test"]
	s.mu.Unlock()
	proxy.endAll()
	// go-imap closes Closed() once its reader stopped, after it moved the client to the logout state.
	select {
	case <-cached.c.Closed():
	case <-time.After(5 * time.Second):
		t.Fatal("the client never saw its connection end")
	}
	if !ended(cached.c) {
		t.Fatal("the reader stopped but the client is not in the logout state")
	}

	if _, err := s.GetMessage(ctx, "acc_test", uint32(uid), "INBOX"); err != nil {
		t.Fatalf("read after the server hung up: %v", err)
	}
	if n := logins.Load(); n != 2 {
		t.Fatalf("%d logins, want 2", n)
	}
	lines := logs()
	if !hasLine(lines, "mail: acc_test: the cached connection had ended (", "reconnecting") || hasLine(lines, "", " (try ") {
		t.Fatalf("log %q, want only the ended cached connection", lines)
	}
}

// When the reconnect's connection ends too, the retries stop there (two logins), each try is logged, and the user
// is told in words, in their language, that the mailbox could not be reached; the raw error stays in the log and in
// the error's chain.
func TestConnectionThatKeepsEndingIsReportedInWords(t *testing.T) {
	logs := captureLog(t)
	// Every connection ends once the server answered its SELECT.
	s, srv, _, logins := proxiedService(t, func(_ int, command string) bool { return selects(command) })
	srv.appendMessage(t, "INBOX", rawMessage("Invoice", "a@example.com", "x"))

	_, err := s.ListMessages(context.Background(), ListOptions{AccountID: "acc_test", Query: "invoice"})
	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) || unreachable.Account != "acc_test" {
		t.Fatalf("err = %v, want UnreachableError", err)
	}
	var searchFailure *stepError
	if !errors.As(unreachable, &searchFailure) || !isHangupCause(searchFailure.err) {
		t.Fatalf("error chain contains %v, want the raw disconnect cause", errors.Unwrap(unreachable))
	}
	if n := logins.Load(); n != connTries {
		t.Fatalf("%d logins, want %d", n, connTries)
	}
	lines := logs()
	if !hasLine(lines, "mail: acc_test: search INBOX failed at search (try 1 of 2, reconnecting): ", "") ||
		!hasLine(lines, "mail: acc_test: search INBOX failed at search (try 2 of 2, giving up): ", searchFailure.err.Error()) {
		t.Fatalf("log %q, want both tries", lines)
	}

	for _, c := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.En, "Couldn't reach this mailbox, even after reconnecting once. Try again later."},
		{i18n.ZhHans, "连不上这个邮箱，已经重连过一次还是不行，请稍后再试。"},
		{i18n.Ja, "このメールボックスに接続できません。一度再接続しましたが、うまくいきませんでした。しばらくしてからもう一度お試しください。"},
	} {
		t.Run(string(c.lang), func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(mailLanguageWriter{w, c.lang},
				httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?account_id=acc_test&q=invoice", nil))
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadGateway || body.Code != "mail_unreachable" || body.Error != c.want ||
				strings.Contains(w.Body.String(), "closed") || strings.Contains(w.Body.String(), "127.0.0.1") {
				t.Fatalf("HTTP %d %s, want 502 and %q", w.Code, w.Body, c.want)
			}
			res := s.Call(i18n.With(context.Background(), c.lang), ToolSearch,
				json.RawMessage(`{"account_id":"acc_test","query":"invoice"}`), nil)
			if !res.IsError || len(res.Content) != 1 || res.Content[0].Text != c.want {
				t.Fatalf("mail_search = %+v, want %q", res, c.want)
			}
		})
	}
}

func isHangupCause(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	if err == nil {
		return false
	}
	// go-imap formats a read error while looking for the next response tag with %v.
	message := err.Error()
	return strings.HasPrefix(message, "in response: cannot read tag: read tcp ") &&
		strings.HasSuffix(message, ": "+syscall.ECONNRESET.Error())
}

func TestIsHangupCause(t *testing.T) {
	reset := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Addr: &net.TCPAddr{
			IP:   net.IPv4(127, 0, 0, 1),
			Port: 143,
		},
		Err: os.NewSyscallError("read", syscall.ECONNRESET),
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"closed", net.ErrClosed, true},
		{"EOF", io.EOF, true},
		{"unexpected EOF", io.ErrUnexpectedEOF, true},
		{"wrapped reset", reset, true},
		{"formatted reset", fmt.Errorf("in response: cannot read tag: %v", reset), true},
		{"unrelated response", errors.New("mailbox closed by policy"), false},
		{"unrelated reset text", errors.New("IMAP status: " + syscall.ECONNRESET.Error()), false},
		{"missing cause", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHangupCause(tc.err); got != tc.want {
				t.Fatalf("isHangupCause(%v) = %t, want %t", tc.err, got, tc.want)
			}
		})
	}
}

// hasLine reports whether a line starts with prefix and contains part.
func hasLine(lines []string, prefix, part string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, prefix) && strings.Contains(l, part) })
}
