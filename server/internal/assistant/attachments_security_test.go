package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCleanAttachmentsRejectsRootDotTraversalAndNul(t *testing.T) {
	for _, bad := range []string{
		"/",
		"/.",
		"~",
		"~/",
		"~/.",
		"/tmp/",
		"/tmp/.",
		"/tmp/./a.txt",
		"/tmp/../etc/passwd",
		"~/docs/../../etc/passwd",
		"/tmp/bad\x00file.txt",
		`~/a\..\b`,
		`~/a\.\b`,
	} {
		if _, err := cleanAttachments([]string{bad}); err == nil {
			t.Errorf("cleanAttachments accepted unsafe path %q", bad)
		}
	}
	// Legitimate dotfiles remain allowed.
	if got, err := cleanAttachments([]string{"/home/me/.env", "~/.bashrc"}); err != nil || len(got) != 2 {
		t.Fatalf("cleanAttachments rejected valid dotfiles: got=%v err=%v", got, err)
	}
}

func TestCheckAttachmentsBoundariesCJKHomeAndSavedValidation(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	f.sessions["server"] = nil
	f.setItems(proto.Item{
		Role: "user",
		Kind: proto.KindText,
		Text: "Compare /home/me/.ssh/id_rsa.pub and /code/app/.env.example (yes/no) " +
			"my/tmp/a /tmp/a/b /tmp/a-1 /Users/张三/etc/passwd /home/me/.ssh/id_rsa备份 好的 ~ " +
			"See /tmp/exact.txt. Check /tmp/colon.txt: next. " +
			"请看【/tmp/cjk1.txt】「/tmp/cjk2.txt」『/tmp/cjk3.txt』《/tmp/cjk4.txt》/tmp/cjk5.txt：说明 请看！/tmp/cjk6.txt " +
			"and /tmp/report-20261109-120000.txt.bak",
	})

	// Substring/prefix/dot/Han-adjacent matches must NEVER authorize exfiltrating a different file.
	for _, bad := range []string{
		"/home/me/.ssh/id_rsa",
		"/code/app/.env",
		"/code/app",
		"/tmp/a",
		"/etc/passwd",
		"/",
		"/.",
		"~",
		"~/.",
		"/tmp/.",
		"/tmp/report-20261109-120000.txt",
		"/tmp/claude-1000/-home-me/conv1/images/",
		"/tmp/claude-1000/-home-me/conv1/images/../../secret.txt",
	} {
		if _, err := s.checkAttachments(ctx, "server", "conv1", []string{bad}); err == nil {
			t.Errorf("checkAttachments allowed exfiltration of %q via substring/boundary/traversal bypass", bad)
		}
	}

	// Exact mentioned paths, sentence punctuation, and CJK punctuation delimiters still work.
	allowed := []string{
		"/home/me/.ssh/id_rsa.pub",
		"/code/app/.env.example",
		"/tmp/exact.txt",
		"/tmp/colon.txt",
		"/tmp/cjk1.txt",
		"/tmp/cjk2.txt",
		"/tmp/cjk3.txt",
		"/tmp/cjk4.txt",
		"/tmp/cjk5.txt",
		"/tmp/cjk6.txt",
	}
	for _, p := range allowed {
		if ok, err := s.checkAttachments(ctx, "server", "conv1", []string{p}); err != nil || len(ok) != 1 {
			t.Errorf("checkAttachments rejected exact user path %q: ok=%v err=%v", p, ok, err)
		}
	}

	// Bidirectional ~/ <-> r.home matching and empty-home safety.
	rHome := attachmentRules{
		home:    "/home/me",
		uploads: "/home/me/.persona/uploads/",
		self:    "conv1",
		said:    func() string { return "请看 ~/docs/a.pdf 和 /home/me/docs/b.pdf" },
	}
	if !rHome.given("/home/me/docs/a.pdf") {
		t.Error("expected /home/me/docs/a.pdf to match user's ~/docs/a.pdf")
	}
	if !rHome.given("~/docs/b.pdf") {
		t.Error("expected ~/docs/b.pdf to match user's /home/me/docs/b.pdf")
	}
	if rHome.given("/home/me/docs/a") || rHome.given("~/docs/b") {
		t.Error("prefix path should not match via ~/ <-> r.home")
	}

	rNoHome := attachmentRules{
		self: "conv1",
		said: func() string { return "好的 ~" },
	}
	if rNoHome.given("~/.") || rNoHome.given("~") {
		t.Error("empty home with ~/./~ must not match '好的 ~'")
	}

	// saved() must reject invalid r.self and directory-only paths.
	for _, badSelf := range []string{"", ".", "..", "a/b", `a\b`} {
		rBad := attachmentRules{self: badSelf, said: func() string { return "" }}
		if rBad.saved("/tmp/" + badSelf + "/images/1.png") {
			t.Errorf("saved() accepted invalid self=%q", badSelf)
		}
	}
	if rHome.saved("/home/me/.persona/uploads/") || rHome.saved("/tmp/conv1/images/") {
		t.Error("saved() accepted bare directory path")
	}
}

func TestReadAttachmentBoundsSanitizationAndEdgeCases(t *testing.T) {
	t.Run("negative head size returns error instead of panicking", func(t *testing.T) {
		s, f := newTaskService(t)
		f.sessions["server"] = nil
		f.answerNode("file.chunk", func(string, json.RawMessage) (any, error) {
			return proto.FileChunk{Path: "/tmp/a.txt", Name: "a.txt", Size: -10, Revision: "r1"}, nil
		})
		if _, _, _, err := s.readAttachment(context.Background(), "server", "/tmp/a.txt"); err == nil {
			t.Fatal("expected error for negative head.Size")
		}
	})

	t.Run("zero-byte file succeeds without extra chunk calls", func(t *testing.T) {
		s, f := newTaskService(t)
		f.sessions["server"] = nil
		calls := 0
		f.answerNode("file.chunk", func(_ string, raw json.RawMessage) (any, error) {
			calls++
			var req proto.FileChunkRequest
			_ = json.Unmarshal(raw, &req)
			return proto.FileChunk{Path: req.Path, Name: "empty.txt", Size: 0, Revision: "r1"}, nil
		})
		name, _, data, err := s.readAttachment(context.Background(), "server", "/tmp/empty.txt")
		if err != nil || name != "empty.txt" || len(data) != 0 || calls != 1 {
			t.Fatalf("zero-byte readAttachment = name=%q len=%d calls=%d err=%v", name, len(data), calls, err)
		}
	})

	t.Run("multi-chunk read clamps final Length and rejects cumulative overflow", func(t *testing.T) {
		s, f := newTaskService(t)
		f.sessions["server"] = nil
		var lengths []int
		f.answerNode("file.chunk", func(_ string, raw json.RawMessage) (any, error) {
			var req proto.FileChunkRequest
			_ = json.Unmarshal(raw, &req)
			if req.Length == 0 {
				return proto.FileChunk{Path: req.Path, Name: "multi.bin", Size: 7, Revision: "r1"}, nil
			}
			lengths = append(lengths, req.Length)
			if req.Offset == 0 {
				return proto.FileChunk{Path: req.Path, Data: []byte("abcd")}, nil
			}
			return proto.FileChunk{Path: req.Path, Data: []byte("efg")}, nil
		})
		name, _, data, err := s.readAttachment(context.Background(), "server", "/tmp/multi.bin")
		if err != nil || name != "multi.bin" || string(data) != "abcdefg" {
			t.Fatalf("multi-chunk = name=%q data=%q err=%v", name, data, err)
		}
		if len(lengths) != 2 || lengths[0] != 7 || lengths[1] != 3 {
			t.Fatalf("chunk request lengths = %v, want [7 3]", lengths)
		}

		// Second chunk overflows cumulative head.Size.
		f.answerNode("file.chunk", func(_ string, raw json.RawMessage) (any, error) {
			var req proto.FileChunkRequest
			_ = json.Unmarshal(raw, &req)
			if req.Length == 0 {
				return proto.FileChunk{Path: req.Path, Name: "over.bin", Size: 5, Revision: "r1"}, nil
			}
			if req.Offset == 0 {
				return proto.FileChunk{Path: req.Path, Data: []byte("abc")}, nil
			}
			return proto.FileChunk{Path: req.Path, Data: []byte("defg")}, nil
		})
		if _, _, _, err := s.readAttachment(context.Background(), "server", "/tmp/over.bin"); err == nil {
			t.Fatal("expected error on multi-chunk cumulative overflow")
		}
	})

	t.Run("truncated stream returns early EOF error", func(t *testing.T) {
		s, f := newTaskService(t)
		f.sessions["server"] = nil
		f.answerNode("file.chunk", func(_ string, raw json.RawMessage) (any, error) {
			var req proto.FileChunkRequest
			_ = json.Unmarshal(raw, &req)
			if req.Length == 0 {
				return proto.FileChunk{Path: req.Path, Name: "trunc.bin", Size: 10, Revision: "r1"}, nil
			}
			if req.Offset == 0 {
				return proto.FileChunk{Path: req.Path, Data: []byte("abc")}, nil
			}
			return proto.FileChunk{Path: req.Path, Data: nil}, nil
		})
		if _, _, _, err := s.readAttachment(context.Background(), "server", "/tmp/trunc.bin"); err == nil {
			t.Fatal("expected error for truncated stream")
		}
	})

	t.Run("sanitizeAttachmentName strips controls, leading spaces, traversal, dots and falls back safely", func(t *testing.T) {
		cases := []struct {
			headName string
			path     string
			want     string
		}{
			{"../../evil.txt", "/tmp/a.txt", "evil.txt"},
			{"dir/ evil.txt", "/tmp/a.txt", "evil.txt"},
			{"dir/ ..", "/tmp/fallback.txt", "fallback.txt"},
			{"dir/...", "/tmp/fallback.txt", "fallback.txt"},
			{"bad\x00\r\n\t\x1bname.txt", "/tmp/a.txt", "badname.txt"},
			{"\x00..\x00", "/tmp/fallback.txt", "fallback.txt"},
			{".env", "/tmp/a.txt", ".env"},
			{"dir/ ..", "/..", "file"},
		}
		for _, tc := range cases {
			s, f := newTaskService(t)
			f.sessions["server"] = nil
			headName := tc.headName
			f.answerNode("file.chunk", func(_ string, raw json.RawMessage) (any, error) {
				var req proto.FileChunkRequest
				_ = json.Unmarshal(raw, &req)
				return proto.FileChunk{Path: req.Path, Name: headName, Size: 0, Revision: "r1"}, nil
			})
			got, _, _, err := s.readAttachment(context.Background(), "server", tc.path)
			if err != nil || got != tc.want {
				t.Errorf("readAttachment(head.Name=%q, path=%q) = %q (err=%v), want %q", tc.headName, tc.path, got, err, tc.want)
			}
		}
	})
}

func TestWithAttachmentsDeterministicSinglePassAndCJKDelimiters(t *testing.T) {
	h := handedAttachments{
		Copied: true,
		Files:  []string{"/dest/a", "/dest/a-bak"},
		moved: map[string]string{
			"":           "/ignored",
			"/tmp/a":     "/dest/a",
			"/tmp/a.bak": "/dest/a-bak",
		},
	}
	in := "Check /tmp/a and /tmp/a.bak and /tmp/a.other. 请看【/tmp/a】和「/tmp/a」以及：/tmp/a：说明 /Users/张三/tmp/a /tmp/a备份"
	got := withAttachments(in, h)
	wantPrefix := "Check /dest/a and /dest/a-bak and /tmp/a.other. 请看【/dest/a】和「/dest/a」以及：/dest/a：说明 /Users/张三/tmp/a /tmp/a备份"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("withAttachments corrupted boundary or CJK delimiters:\ngot:  %q\nwant: %q", got, wantPrefix)
	}

	// Single-pass guarantee: chained mappings A->B and B->C must NOT double-replace A into C.
	chained := handedAttachments{
		Copied: true,
		Files:  []string{"/tmp/b", "/tmp/c"},
		moved: map[string]string{
			"/tmp/a": "/tmp/b",
			"/tmp/b": "/tmp/c",
		},
	}
	gotChained := withAttachments("First /tmp/a then /tmp/b.", chained)
	wantChained := "First /tmp/b then /tmp/c."
	if !strings.HasPrefix(gotChained, wantChained) {
		t.Fatalf("withAttachments double-replaced chained path:\ngot:  %q\nwant: %q", gotChained, wantChained)
	}
}
