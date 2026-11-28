package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/teamdocs"
)

func fixtureMemberToken() string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
}

func fixtureMemberRevision(content string) teamdocs.Revision {
	hash := sha256.Sum256([]byte(content))
	return teamdocs.Revision{Project: "p", Document: "doc", Number: 1, Author: "writer", Content: content, SHA256: hex.EncodeToString(hash[:])}
}

func TestProjectMemberDestinations(t *testing.T) {
	token := fixtureMemberToken()
	for _, base := range []string{"https://projects.test", "http://127.0.0.1:8080", "http://[::1]:82", "http://localhost/teams"} {
		if _, err := newMemberClient(base, token); err != nil {
			t.Errorf("service URL %q: %v", base, err)
		}
	}
	for _, base := range []string{"http://projects.test", "https://user:pass@projects.test", "https://projects.test?token=private", "https://projects.test#private", "/docs"} {
		if _, err := newMemberClient(base, token); err == nil {
			t.Errorf("accepted service URL %q", base)
		}
	}
	for _, bad := range []string{"", token + "\n", token[:40], token[:42] + "?", token[:42] + "x"} {
		if _, err := newMemberClient("https://projects.test", bad); err == nil {
			t.Error("accepted malformed token")
		}
	}
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/private", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c, err := newMemberClient(redirect.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.read(t.Context(), "p", "doc", 1); err == nil || followed.Load() {
		t.Fatal("followed redirect or accepted redirect as a revision")
	}
}

func TestProjectMemberChecksPublishReceipt(t *testing.T) {
	const content = " title \r\n\nCafe\u0301 <ok>\u0001 "
	published := fixtureMemberRevision(content)
	for _, tc := range []struct {
		name   string
		change func(*teamdocs.Revision)
	}{
		{"exact", nil},
		{"foreign project", func(r *teamdocs.Revision) { r.Project = "other" }},
		{"foreign document", func(r *teamdocs.Revision) { r.Document = "other" }},
		{"wrong head", func(r *teamdocs.Revision) { r.Number++ }},
		{"invalid author", func(r *teamdocs.Revision) { r.Author = "writer/other" }},
		{"changed bytes", func(r *teamdocs.Revision) { *r = fixtureMemberRevision(strings.TrimSpace(content)) }},
		{"modified digest", func(r *teamdocs.Revision) { r.SHA256 = strings.Repeat("0", 64) }},
		{"partial approval", func(r *teamdocs.Revision) { r.ApprovedBy = "lead" }},
		{"unexpected approval", func(r *teamdocs.Revision) { now := time.Now(); r.ApprovedBy = "lead"; r.ApprovedAt = &now }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/mount/v1/projects/p/documents/doc/revisions" || r.Header.Get("Authorization") != "Bearer "+fixtureMemberToken() {
					t.Error("wrong publish request target, method or credential")
				}
				var body struct {
					Expected *int64 `json:"expected_revision"`
					Content  string `json:"content"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Expected == nil || *body.Expected != 0 || body.Content != content {
					t.Error("publish did not preserve the submitted revision and content")
				}
				rev := published
				if tc.change != nil {
					tc.change(&rev)
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(rev)
			}))
			defer s.Close()
			c, err := newMemberClient(s.URL+"/mount", fixtureMemberToken())
			if err != nil {
				t.Fatal(err)
			}
			rev, err := c.publish(t.Context(), "p", "doc", 0, content)
			if tc.change == nil {
				if err != nil || rev != published {
					t.Fatalf("unexpected receipt: %v", err)
				}
			} else if !errors.Is(err, errMemberReceipt) {
				t.Fatalf("accepted modified receipt: %v", err)
			}
		})
	}
}

func TestProjectMemberApprovalRechecksReviewedRevision(t *testing.T) {
	base := fixtureMemberRevision("reviewed text")
	approvedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		digest        string
		prior         bool
		status        int
		alterApprover bool
		wantPosts     int32
		wantError     string
	}{
		{"approved", base.SHA256, false, 200, false, 1, ""},
		{"repeated approval", base.SHA256, true, 200, false, 1, ""},
		{"preserve first approval", base.SHA256, true, 200, true, 1, "invalid revision"},
		{"different digest", strings.Repeat("9", 64), false, 200, false, 0, "differs"},
		{"new head after review", base.SHA256, false, 409, false, 1, "409"},
		{"role still required on retry", base.SHA256, true, 403, false, 1, "403"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads, posts atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rev := base
				if tc.prior {
					rev.ApprovedBy, rev.ApprovedAt = "lead", &approvedAt
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /v1/projects/p/documents/doc/revisions/1":
					reads.Add(1)
				case "POST /v1/projects/p/documents/doc/approvals":
					posts.Add(1)
					var body struct {
						Revision int64 `json:"revision"`
					}
					if reads.Load() != 1 || json.NewDecoder(r.Body).Decode(&body) != nil || body.Revision != 1 {
						t.Error("attempted approval before verifying the exact revision")
					}
					if tc.status != 200 {
						http.Error(w, "sensitive body "+fixtureMemberToken(), tc.status)
						return
					}
					rev.ApprovedBy, rev.ApprovedAt = "lead", &approvedAt
					if tc.alterApprover {
						rev.ApprovedBy = "replacement"
					}
				default:
					t.Error("unexpected project route")
					w.WriteHeader(404)
					return
				}
				_ = json.NewEncoder(w).Encode(rev)
			}))
			defer s.Close()
			c, err := newMemberClient(s.URL, fixtureMemberToken())
			if err != nil {
				t.Fatal(err)
			}
			rev, err := c.approve(t.Context(), "p", "doc", 1, tc.digest)
			if tc.wantError == "" {
				if err != nil || rev.ApprovedBy != "lead" || !rev.ApprovedAt.Equal(approvedAt) {
					t.Fatalf("incorrect approval: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || strings.Contains(err.Error(), fixtureMemberToken()) || strings.Contains(err.Error(), "sensitive body") {
				t.Errorf("incorrect approval error: %v", err)
			}
			if reads.Load() != 1 || posts.Load() != tc.wantPosts {
				t.Errorf("got %d reads, %d writes", reads.Load(), posts.Load())
			}
		})
	}
}

func TestProjectMemberBoundedResponse(t *testing.T) {
	rev := fixtureMemberRevision("private text")
	raw, _ := json.Marshal(rev)
	for _, reply := range []string{string(raw) + " {}", strings.Replace(string(raw), `"project":"p"`, `"project":"p","project":"p"`, 1), strings.Repeat(" ", memberReplyBytes) + string(raw)} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(reply)) }))
		c, err := newMemberClient(s.URL, fixtureMemberToken())
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.read(t.Context(), "p", "doc", 1)
		s.Close()
		if !errors.Is(err, errMemberReceipt) {
			t.Fatalf("accepted invalid response: %v", err)
		}
	}
}
