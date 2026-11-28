package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/nwverse/persona/server/internal/i18n"
)

func moveServer(t *testing.T, capabilities ...imap.Cap) (*testIMAP, *Service) {
	t.Helper()
	caps := imap.CapSet{imap.CapIMAP4rev1: {}}
	for _, capability := range capabilities {
		caps[capability] = struct{}{}
	}
	srv := newTestIMAPCaps(t, caps)
	return srv, newServiceWithAccount(t, srv)
}

func testMoveRequest(t *testing.T, srv *testIMAP, mailbox string) (uint32, MoveRequest) {
	t.Helper()
	uid := srv.appendMessage(t, mailbox, rawMessage("Move this", "a@example.com", "body"))
	status, err := srv.user.Status(mailbox, &imap.StatusOptions{UIDValidity: true})
	if err != nil {
		t.Fatal(err)
	}
	return uint32(uid), MoveRequest{Mailbox: mailbox, UIDValidity: status.UIDValidity, To: RoleArchive}
}

func moveBoxCount(t *testing.T, srv *testIMAP, mailbox string) uint32 {
	t.Helper()
	st, err := srv.user.Status(mailbox, &imap.StatusOptions{NumMessages: true})
	if err != nil {
		t.Fatal(err)
	}
	return *st.NumMessages
}

func postMove(t *testing.T, srv *Service, uid uint32, req MoveRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/mail/messages/acc_test/%d/move", uid), bytes.NewReader(raw))
	srv.Handler().ServeHTTP(w, r)
	return w
}

func requireMoveCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || moveResponseError(err).Code() != want {
		t.Fatalf("move error = %v, want %s", err, want)
	}
}

func requireMoveHTTPCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct{ Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != status || body.Code != code {
		t.Fatalf("HTTP %d %s, want %d %s", rec.Code, rec.Body, status, code)
	}
}

func moveCommand(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	if fields[1] == "UID" && len(fields) >= 3 {
		return "UID " + fields[2]
	}
	return fields[1]
}

func observeMove(t *testing.T, srv *testIMAP, s *Service, after func(int, string) bool, script imapProxyScript) (func() []string, *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	var commands []string
	before := script.before
	script.before = func(line string) string {
		cmd := moveCommand(line)
		switch cmd {
		case "UID MOVE", "UID COPY", "UID STORE", "UID EXPUNGE", "EXPUNGE":
			mu.Lock()
			commands = append(commands, cmd)
			mu.Unlock()
		}
		if before != nil {
			return before(line)
		}
		return ""
	}
	proxy := newScriptedIMAPProxy(t, serverAddr(srv.host, srv.port), after, script)
	logins := countLogins(s, proxy.ln.Addr().String())
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(commands)
	}, logins
}

func TestMailMoveProtocolAndUndo(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []imap.Cap
		want []string
	}{
		{"native", []imap.Cap{imap.CapMove, imap.CapUIDPlus}, []string{"UID MOVE"}},
		{"rev2", []imap.Cap{imap.CapIMAP4rev2}, []string{"UID MOVE"}},
		{"UIDPLUS", []imap.Cap{imap.CapUIDPlus}, []string{"UID COPY", "UID STORE", "UID EXPUNGE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := moveServer(t, tc.caps...)
			uid, req := testMoveRequest(t, srv, "INBOX")
			req.To = RoleTrash
			srv.appendMessage(t, "INBOX", rawMessage("Keep in inbox", "a@example.com", "body"), imap.FlagDeleted)
			srv.appendMessage(t, "Trash", rawMessage("Keep in trash", "a@example.com", "body"), imap.FlagDeleted)
			var extended atomic.Bool
			commands, _ := observeMove(t, srv, s, nil, imapProxyScript{before: func(line string) string {
				if isListReturn([]byte(line)) {
					extended.Store(true)
				}
				return ""
			}})
			r := postMove(t, s, uid, req)
			var moved MoveResult
			if err := json.Unmarshal(r.Body.Bytes(), &moved); r.Code != http.StatusOK || err != nil || moved.Mailbox != "Trash" || moved.UID == 0 || moved.UIDValidity == 0 {
				t.Fatalf("move: HTTP %d %s (%v)", r.Code, r.Body, err)
			}
			if got := commands(); !slices.Equal(got, tc.want) {
				t.Fatalf("move commands = %v, want %v", got, tc.want)
			}
			if got := moveBoxCount(t, srv, "INBOX"); got != 1 {
				t.Fatalf("INBOX count = %d, want the other deleted message", got)
			}
			r = postMove(t, s, moved.UID, MoveRequest{Mailbox: moved.Mailbox, UIDValidity: moved.UIDValidity, ToMailbox: req.Mailbox})
			if r.Code != http.StatusOK || moveBoxCount(t, srv, "INBOX") != 2 || moveBoxCount(t, srv, "Trash") != 1 {
				t.Fatalf("undo: HTTP %d %s; INBOX %d, Trash %d", r.Code, r.Body, moveBoxCount(t, srv, "INBOX"), moveBoxCount(t, srv, "Trash"))
			}
			if got := commands(); !slices.Equal(got, append(slices.Clone(tc.want), tc.want...)) {
				t.Fatalf("undo commands = %v", got)
			}
			if tc.name == "UIDPLUS" && extended.Load() {
				t.Fatal("sent LIST RETURN without LIST-EXTENDED")
			}
		})
	}
}

func TestMailMoveRefusals(t *testing.T) {
	t.Run("UID validity changed and the UID was reused", func(t *testing.T) {
		srv, s := moveServer(t, imap.CapUIDPlus)
		uid, req := testMoveRequest(t, srv, "Drafts")
		if err := srv.user.Delete("Drafts"); err != nil {
			t.Fatal(err)
		}
		if err := srv.user.Create("Drafts", nil); err != nil {
			t.Fatal(err)
		}
		if got := srv.appendMessage(t, "Drafts", rawMessage("Replacement", "a@example.com", "body")); uint32(got) != uid {
			t.Fatalf("recreated mailbox's UID = %d, want %d", got, uid)
		}
		commands, _ := observeMove(t, srv, s, nil, imapProxyScript{})
		requireMoveHTTPCode(t, postMove(t, s, uid, req), http.StatusConflict, "mail_uid_validity_changed")
		if got := commands(); len(got) != 0 {
			t.Fatalf("changed UID validity issued writes: %v", got)
		}
	})
	for _, tc := range []struct {
		name   string
		change func(*MoveRequest)
		caps   []imap.Cap
		status int
		code   string
	}{
		{"no capabilities", func(*MoveRequest) {}, nil, 422, "unsupported"},
		{"same explicit source", func(r *MoveRequest) { r.To, r.ToMailbox = "", "inbox" }, []imap.Cap{imap.CapMove}, 400, "invalid_params"},
		{"same role source", func(r *MoveRequest) { r.To = RoleInbox }, []imap.Cap{imap.CapMove}, 400, "invalid_params"},
		{"bad role", func(r *MoveRequest) { r.To = RoleDrafts }, []imap.Cap{imap.CapMove}, 400, "invalid_params"},
		{"two destinations", func(r *MoveRequest) { r.ToMailbox = "Trash" }, []imap.Cap{imap.CapMove}, 400, "invalid_params"},
		{"no destination", func(r *MoveRequest) { r.To = "" }, []imap.Cap{imap.CapMove}, 400, "invalid_params"},
		{"no validity", func(r *MoveRequest) { r.UIDValidity = 0 }, []imap.Cap{imap.CapMove}, 400, "invalid_params"},
		{"unknown destination", func(r *MoveRequest) { r.To, r.ToMailbox = "", "Missing" }, []imap.Cap{imap.CapMove}, 404, "mailbox_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := moveServer(t, tc.caps...)
			uid, req := testMoveRequest(t, srv, "INBOX")
			tc.change(&req)
			commands, _ := observeMove(t, srv, s, nil, imapProxyScript{})
			requireMoveHTTPCode(t, postMove(t, s, uid, req), tc.status, tc.code)
			if got := commands(); len(got) != 0 {
				t.Fatalf("refused move issued writes: %v", got)
			}
		})
	}
}

func TestMailMoveDestination(t *testing.T) {
	srv, s := moveServer(t, imap.CapUIDPlus, imap.CapListExtended, imap.CapSpecialUse)
	if err := srv.user.Create("Filtered", nil); err != nil {
		t.Fatal(err)
	}
	uid, req := testMoveRequest(t, srv, "INBOX")
	req.To = RoleJunk
	observeMove(t, srv, s, nil, imapProxyScript{reply: func(line string, _ bool) string {
		if strings.HasPrefix(line, "* LIST ") && strings.Contains(line, "Filtered") {
			if strings.Contains(line, "()") {
				return strings.Replace(line, "()", `(\Junk)`, 1)
			}
			return strings.Replace(line, "(", `(\Junk `, 1)
		}
		return line
	}})
	result, err := s.moveMessage(context.Background(), "acc_test", uid, req)
	if err != nil || result.Mailbox != "Filtered" || moveBoxCount(t, srv, "Junk") != 0 {
		t.Fatalf("special-use destination = %+v, err = %v", result, err)
	}
}

func TestMailMoveWithoutMatchingCopyUID(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp string
	}{
		{"missing", ""}, {"different source", "[COPYUID 5 99 1] "}, {"ambiguous destination", "[COPYUID 5 1 1:4] "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := moveServer(t, imap.CapUIDPlus)
			uid, req := testMoveRequest(t, srv, "INBOX")
			observeMove(t, srv, s, nil, imapProxyScript{reply: func(line string, _ bool) string {
				if start := strings.Index(line, "[COPYUID "); start >= 0 {
					return line[:start] + tc.resp + line[start+strings.Index(line[start:], "]")+2:]
				}
				return line
			}})
			r := postMove(t, s, uid, req)
			if r.Code != http.StatusOK || strings.TrimSpace(r.Body.String()) != `{"mailbox":"Archive"}` || moveBoxCount(t, srv, "Archive") != 1 {
				t.Fatalf("HTTP %d %s, want the destination without a mapped identity", r.Code, r.Body)
			}
		})
	}
}

func TestMailMoveGmailArchive(t *testing.T) {
	for _, tc := range []struct {
		name, provider, auth string
	}{
		{"OAuth", ProviderGmail, AuthOAuth2}, {"app password", ProviderGmail, AuthPassword}, {"custom password setup", ProviderIMAP, AuthPassword},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := moveServer(t, imap.CapUIDPlus)
			a := s.accts["acc_test"]
			a.Provider, a.Auth = tc.provider, tc.auth
			if tc.provider == ProviderIMAP {
				a.IMAPHost = "imap.gmail.com"
			}
			uid, req := testMoveRequest(t, srv, "INBOX")
			commands, logins := observeMove(t, srv, s, nil, imapProxyScript{})
			requireMoveHTTPCode(t, postMove(t, s, uid, req), http.StatusUnprocessableEntity, "unsupported")
			if logins.Load() != 0 || len(commands()) != 0 {
				t.Fatal("Gmail archive reached IMAP")
			}
			for _, box := range []string{RoleJunk, RoleTrash, RoleInbox, "explicit"} {
				source := "INBOX"
				if box == RoleInbox {
					source = "Drafts"
				}
				uid, req := testMoveRequest(t, srv, source)
				req.To = box
				if box == "explicit" {
					req.To, req.ToMailbox = "", "Archive"
				}
				if r := postMove(t, s, uid, req); r.Code != http.StatusOK {
					t.Fatalf("%s: HTTP %d %s", box, r.Code, r.Body)
				}
			}
		})
	}
}

func TestMailMoveFallbackStopsOnFailure(t *testing.T) {
	steps := []string{"UID COPY", "UID STORE", "UID EXPUNGE"}
	for i, stop := range steps {
		t.Run(stop, func(t *testing.T) {
			srv, s := moveServer(t, imap.CapUIDPlus)
			uid, req := testMoveRequest(t, srv, "INBOX")
			commands, logins := observeMove(t, srv, s, nil, imapProxyScript{before: func(line string) string {
				if moveCommand(line) == stop {
					tag, _, _ := strings.Cut(line, " ")
					return tag + " NO rejected\r\n"
				}
				return ""
			}})
			_, err := s.moveMessage(context.Background(), "acc_test", uid, req)
			if i == 0 {
				requireMoveCode(t, err, "mail_move_failed")
			} else {
				requireMoveCode(t, err, "mail_move_partial")
			}
			if got := commands(); !slices.Equal(got, steps[:i+1]) || logins.Load() != 1 {
				t.Fatalf("commands = %v, logins = %d", got, logins.Load())
			}
		})
	}
}

func TestMailMoveNativeTaggedErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		apply  bool
		status int
		code   string
	}{
		{"BAD before applying", false, http.StatusBadGateway, "mail_move_failed"},
		{"NO after applying", true, http.StatusConflict, "mail_move_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := moveServer(t, imap.CapMove)
			uid, req := testMoveRequest(t, srv, "INBOX")
			var moveTag atomic.Pointer[string]
			commands, logins := observeMove(t, srv, s, nil, imapProxyScript{
				before: func(line string) string {
					if moveCommand(line) != "UID MOVE" {
						return ""
					}
					tag, _, _ := strings.Cut(line, " ")
					if !tc.apply {
						return tag + " BAD unsupported arguments\r\n"
					}
					moveTag.Store(&tag)
					return ""
				},
				reply: func(line string, _ bool) string {
					// RFC 6851 §3.3 permits a tagged NO even if a message moved.
					if tag := moveTag.Load(); tag != nil && strings.HasPrefix(line, *tag+" OK ") {
						return *tag + " NO move failed\r\n"
					}
					return line
				},
			})
			requireMoveHTTPCode(t, postMove(t, s, uid, req), tc.status, tc.code)
			if got := commands(); !slices.Equal(got, []string{"UID MOVE"}) || logins.Load() != 1 {
				t.Fatalf("commands = %v, logins = %d; want one UID MOVE without replay", got, logins.Load())
			}
			wantArchive := uint32(0)
			if tc.apply {
				wantArchive = 1
			}
			if archive, inbox := moveBoxCount(t, srv, "Archive"), moveBoxCount(t, srv, "INBOX"); archive != wantArchive || inbox != 1-wantArchive {
				t.Fatalf("archive = %d, inbox = %d; want %d and %d", archive, inbox, wantArchive, 1-wantArchive)
			}
		})
	}
}

func TestMailMoveConnectionLoss(t *testing.T) {
	for _, tc := range []struct {
		name, command, code string
		cap                 imap.Cap
		drop                bool
		logins              int32
	}{
		{"native reply lost", "UID MOVE", "mail_move_unknown", imap.CapMove, true, 1},
		{"copy reply lost", "UID COPY", "mail_move_unknown", imap.CapUIDPlus, true, 1},
		{"copy acknowledged", "UID COPY", "mail_move_partial", imap.CapUIDPlus, false, 1},
		{"safe reconnect before write", "UID FETCH", "", imap.CapUIDPlus, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, s := moveServer(t, tc.cap)
			uid, req := testMoveRequest(t, srv, "INBOX")
			commands, logins := observeMove(t, srv, s, func(conn int, line string) bool {
				return conn == 1 && moveCommand(line) == tc.command
			}, imapProxyScript{reply: func(line string, hangup bool) string {
				if tc.drop && hangup {
					return ""
				}
				return line
			}})
			w := postMove(t, s, uid, req)
			if tc.code == "" {
				if w.Code != http.StatusOK {
					t.Fatalf("pre-write reconnect: HTTP %d %s", w.Code, w.Body)
				}
			} else {
				requireMoveHTTPCode(t, w, http.StatusConflict, tc.code)
			}
			write := "UID COPY"
			if tc.cap == imap.CapMove {
				write = "UID MOVE"
			}
			var writes int
			for _, cmd := range commands() {
				if cmd == write {
					writes++
				}
			}
			if writes != 1 || logins.Load() != tc.logins || moveBoxCount(t, srv, "Archive") != 1 {
				t.Fatalf("commands = %v, logins = %d, copies = %d", commands(), logins.Load(), moveBoxCount(t, srv, "Archive"))
			}
		})
	}
}

func TestMailMoveCancelAndTimeoutAfterCopy(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", canceled), func(t *testing.T) {
			srv, s := moveServer(t, imap.CapUIDPlus)
			uid, req := testMoveRequest(t, srv, "INBOX")
			copyReply := make(chan struct{})
			commands, logins := observeMove(t, srv, s, nil, imapProxyScript{reply: func(line string, _ bool) string {
				if strings.Contains(line, "[COPYUID ") {
					close(copyReply)
					return ""
				}
				return line
			}})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if canceled {
				go func() {
					select {
					case <-copyReply:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			_, err := s.moveMessage(ctx, "acc_test", uid, req)
			requireMoveCode(t, err, "mail_move_unknown")
			if got := commands(); !slices.Equal(got, []string{"UID COPY"}) || logins.Load() != 1 || moveBoxCount(t, srv, "Archive") != 1 {
				t.Fatalf("commands = %v, logins = %d, copies = %d", got, logins.Load(), moveBoxCount(t, srv, "Archive"))
			}
		})
	}
}

func TestMailMoveDirectToolNeedsApproval(t *testing.T) {
	srv, s := moveServer(t, imap.CapUIDPlus)
	uid, req := testMoveRequest(t, srv, "INBOX")
	ctx := i18n.With(context.Background(), i18n.En)
	args := json.RawMessage(fmt.Sprintf(`{"account_id":"acc_test","mailbox":"INBOX","uid":%d,"uid_validity":%d,"to":"archive"}`, uid, req.UIDValidity))
	commands, _ := observeMove(t, srv, s, nil, imapProxyScript{})
	if out, _ := s.CallRef(ctx, ToolMove, args, nil); !out.IsError || len(commands()) != 0 {
		t.Fatalf("ungated tool = %+v, wrote %v", out, commands())
	}
	for _, allow := range []bool{false, true} {
		out, ref := s.CallRef(ctx, ToolMove, args, func(_ context.Context, summary string, got json.RawMessage) bool {
			if summary != "Move an email in \"me@example.com\" from \"INBOX\" to \"Archive\"\nSubject: \"Move this\"" || !bytes.Equal(got, args) || len(commands()) != 0 {
				t.Fatalf("approval summary %q, args %s, premature commands %v", summary, got, commands())
			}
			return allow
		})
		if out.IsError == allow || ref != "" {
			t.Fatalf("approval %v: %+v, ref %q", allow, out, ref)
		}
	}
	if got := commands(); !slices.Equal(got, []string{"UID COPY", "UID STORE", "UID EXPUNGE"}) || moveBoxCount(t, srv, "Archive") != 1 {
		t.Fatalf("commands = %v, copies = %d", got, moveBoxCount(t, srv, "Archive"))
	}
}

func TestMailMoveApprovalBindsDestinationAndRechecksIdentity(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprint("recreate=", recreate), func(t *testing.T) {
			srv, s := moveServer(t, imap.CapMove, imap.CapListExtended, imap.CapSpecialUse)
			if err := srv.user.Create("Other", nil); err != nil {
				t.Fatal(err)
			}
			uid, req := testMoveRequest(t, srv, "Drafts")
			var changed atomic.Bool
			commands, _ := observeMove(t, srv, s, nil, imapProxyScript{reply: func(line string, _ bool) string {
				if changed.Load() && strings.HasPrefix(line, "* LIST ") && strings.Contains(line, "Other") {
					if strings.Contains(line, "()") {
						return strings.Replace(line, "()", `(\Archive)`, 1)
					}
					return strings.Replace(line, "(", `(\Archive `, 1)
				}
				return line
			}})
			ctx := i18n.With(context.Background(), i18n.En)
			args := json.RawMessage(fmt.Sprintf(`{"account_id":"acc_test","mailbox":"Drafts","uid":%d,"uid_validity":%d,"to":"archive"}`, uid, req.UIDValidity))
			summary, apply, err := s.PrepareMove(ctx, args)
			if err != nil || !strings.Contains(summary, `to "Archive"`) || len(commands()) != 0 {
				t.Fatalf("preview: summary = %q, err = %v, commands = %v", summary, err, commands())
			}
			changed.Store(true)
			if recreate {
				if err := srv.user.Delete("Drafts"); err != nil {
					t.Fatal(err)
				}
				if err := srv.user.Create("Drafts", nil); err != nil {
					t.Fatal(err)
				}
				srv.appendMessage(t, "Drafts", rawMessage("Replacement", "a@example.com", "body"))
			}
			result, err := apply(ctx)
			if recreate {
				requireMoveCode(t, err, "mail_uid_validity_changed")
				if len(commands()) != 0 {
					t.Fatalf("stale approval issued a write: %v", commands())
				}
				return
			}
			if err != nil || result.Mailbox != "Archive" || moveBoxCount(t, srv, "Other") != 0 {
				t.Fatalf("after the role changed, result = %+v, err = %v", result, err)
			}
			_, err = apply(ctx)
			requireMoveCode(t, err, "approval_required")
			if got := commands(); !slices.Equal(got, []string{"UID MOVE"}) {
				t.Fatalf("approved move sent %v", got)
			}
		})
	}
}
