package node

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestLarkFileBodyAuthlessCannotAuthorizeStaleContent(t *testing.T) {
	cwd := t.TempDir()
	writeBody := func(name, content string) string {
		t.Helper()
		p := filepath.Join(cwd, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	writeBody("p2p.json", `{"type":"p2p"}`)
	writeBody("group.json", `{"type":"group"}`)
	absBody := writeBody("abs.json", `{"type":"p2p"}`)
	changedBody := writeBody("changed.json", `{"type":"p2p"}`)

	authlessCfg := olCfg()
	proxiedCfg := olCfg()
	proxiedCfg.Gates[0] = proxiedGate("http://127.0.0.1:1")

	bodyIndependentCfg := olCfg(proto.LarkRule{Method: "POST", Path: "/conversations", Action: "allow"})
	denyBeforeAllowCfg := proto.AssistantLark{Gates: []proto.CommandGate{func() proto.CommandGate {
		g := exampleGate("")
		g.Rules = []proto.LarkRule{
			{Method: "POST", Path: "/conversations", Body: map[string]any{"type": "group"}, Action: "deny"},
			{Method: "POST", Path: "/conversations", Action: "allow"},
		}
		return g
	}()}}
	allowBeforeDenyCfg := proto.AssistantLark{Gates: []proto.CommandGate{func() proto.CommandGate {
		g := exampleGate("")
		g.Rules = []proto.LarkRule{
			{Method: "POST", Path: "/conversations", Body: map[string]any{"type": "p2p"}, Action: "allow"},
			{Method: "POST", Path: "/conversations", Action: "deny"},
		}
		return g
	}()}}

	tests := []struct {
		name         string
		cfg          proto.AssistantLark
		command      string
		wantDecision string
		wantCards    int
		after        func()
	}{
		{
			name:         "lone relative @file on authless gate requires confirmation",
			cfg:          authlessCfg,
			command:      `ex POST /conversations @p2p.json`,
			wantDecision: "deny",
			wantCards:    1,
		},
		{
			name:         "absolute @file rewritten earlier in same command requires confirmation",
			cfg:          authlessCfg,
			command:      `echo '{"type":"group"}' > ` + absBody + `; ex POST /conversations @` + absBody,
			wantDecision: "deny",
			wantCards:    1,
		},
		{
			name:         "external mutation after check still requires confirmation",
			cfg:          authlessCfg,
			command:      `ex POST /conversations @changed.json`,
			wantDecision: "deny",
			wantCards:    1,
			after: func() {
				if err := os.WriteFile(changedBody, []byte(`{"type":"group"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:         "inline JSON body keeps body-scoped allow",
			cfg:          authlessCfg,
			command:      `ex POST /conversations '{"type":"p2p"}'`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "inline JSON plus -d @file cannot silently allow on authless gate",
			cfg:          authlessCfg,
			command:      `ex POST /conversations '{"type":"p2p"}' -d @group.json`,
			wantDecision: "deny",
			wantCards:    1,
		},
		{
			name:         "inline JSON plus -d @- cannot silently allow on authless gate",
			cfg:          authlessCfg,
			command:      `ex POST /conversations '{"type":"p2p"}' -d @-`,
			wantDecision: "deny",
			wantCards:    1,
		},
		{
			name:         "inline JSON plus --data-urlencode name@file cannot silently allow on authless gate",
			cfg:          authlessCfg,
			command:      `ex POST /conversations '{"type":"p2p"}' --data-urlencode payload@group.json`,
			wantDecision: "deny",
			wantCards:    1,
		},
		{
			name:         "inline JSON plus --data-urlencode a=b@c stays inline",
			cfg:          authlessCfg,
			command:      `ex POST /conversations '{"type":"p2p"}' --data-urlencode a=b@c`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "authless @file with -G and body-independent allow stays allowed",
			cfg:          bodyIndependentCfg,
			command:      `ex POST /conversations @p2p.json -G`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "authless @file with -G and body-scoped allow only is denied as unknown query",
			cfg:          authlessCfg,
			command:      `ex POST /conversations @p2p.json -G`,
			wantDecision: "deny",
			wantCards:    0,
		},
		{
			name:         "authenticated proxy gate keeps @file allow for proxy enforcement",
			cfg:          proxiedCfg,
			command:      `ex POST /conversations @p2p.json`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "body-independent allow rule still allows @file on authless gate",
			cfg:          bodyIndependentCfg,
			command:      `ex POST /conversations @p2p.json`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "body-scoped deny matched at check time still denies",
			cfg:          denyBeforeAllowCfg,
			command:      `ex POST /conversations @group.json`,
			wantDecision: "deny",
			wantCards:    0,
		},
		{
			name:         "body-scoped deny before body-independent allow confirms @file",
			cfg:          denyBeforeAllowCfg,
			command:      `ex POST /conversations @p2p.json`,
			wantDecision: "deny",
			wantCards:    1,
		},
		{
			name:         "body-scoped deny before body-independent allow still allows inline body",
			cfg:          denyBeforeAllowCfg,
			command:      `ex POST /conversations '{"type":"p2p"}'`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "body-scoped allow before unconditional deny denies @file on authless gate",
			cfg:          allowBeforeDenyCfg,
			command:      `ex POST /conversations @p2p.json`,
			wantDecision: "deny",
			wantCards:    0,
		},
		{
			name:         "body-scoped allow before unconditional deny allows inline body",
			cfg:          allowBeforeDenyCfg,
			command:      `ex POST /conversations '{"type":"p2p"}'`,
			wantDecision: "allow",
			wantCards:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			p, c, _ := newFakeCodex(t, n, cwd)
			p.id, c.threadID = testThread, testThread
			n.procs[testThread] = p
			n.larkCfg, n.larkCfgAt = tc.cfg, time.Now().Add(time.Hour)
			n.noteModes(testThread, proto.ModeDefault, "")
			fh.answer = func(proto.Interaction) proto.Resolution {
				no := false
				return proto.Resolution{Allow: &no}
			}
			decision, _, err := n.larkCheck(context.Background(), testThread, cwd, tc.command, proto.Interaction{
				SessionID: testThread,
				ExpiresAt: time.Now().Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.after != nil {
				tc.after()
			}
			if decision != tc.wantDecision || len(fh.interactions()) != tc.wantCards {
				t.Fatalf("decision=%q cards=%d, want decision=%q cards=%d", decision, len(fh.interactions()), tc.wantDecision, tc.wantCards)
			}
		})
	}
}

func TestLarkFileBodyBypassModeUnchanged(t *testing.T) {
	cwd := t.TempDir()
	absBody := filepath.Join(cwd, "abs.json")
	if err := os.WriteFile(filepath.Join(cwd, "p2p.json"), []byte(`{"type":"p2p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absBody, []byte(`{"type":"p2p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "msg.json"), []byte(`{"text":"hi"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		command      string
		wantDecision string
		wantCards    int
	}{
		{
			name:         "non-outbound relative file body stays allowed in bypass",
			command:      `ex POST /conversations @p2p.json`,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "non-outbound absolute file body after echo stays allowed in bypass",
			command:      `echo '{"type":"group"}' > ` + absBody + `; ex POST /conversations @` + absBody,
			wantDecision: "allow",
			wantCards:    0,
		},
		{
			name:         "outbound file body still confirms in bypass",
			command:      `ex POST /conversations/7/messages @msg.json`,
			wantDecision: "deny",
			wantCards:    1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			p, c, _ := newFakeCodex(t, n, cwd)
			p.id, c.threadID = testThread, testThread
			n.procs[testThread] = p
			n.larkCfg, n.larkCfgAt = olCfg(), time.Now().Add(time.Hour)
			n.noteModes(testThread, proto.ModeBypass, "")
			fh.answer = func(proto.Interaction) proto.Resolution {
				no := false
				return proto.Resolution{Allow: &no}
			}
			decision, _, err := n.larkCheck(context.Background(), testThread, cwd, tc.command, proto.Interaction{
				SessionID: testThread,
				ExpiresAt: time.Now().Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision != tc.wantDecision || len(fh.interactions()) != tc.wantCards {
				t.Fatalf("decision=%q cards=%d, want decision=%q cards=%d", decision, len(fh.interactions()), tc.wantDecision, tc.wantCards)
			}
		})
	}
}

func TestReadBodyFileRejectsUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realFile, []byte(`{"type":"p2p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file literally named "-" must not satisfy "@-" (stdin in curl).
	if err := os.WriteFile(filepath.Join(dir, "-"), []byte(`{"type":"p2p"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(dir, "subdir")
	if err := os.Mkdir(subDir, 0o700); err != nil {
		t.Fatal(err)
	}
	exactLimit := filepath.Join(dir, "exact.txt")
	if err := os.WriteFile(exactLimit, []byte(strings.Repeat("a", 1<<20)), 0o600); err != nil {
		t.Fatal(err)
	}
	overLimit := filepath.Join(dir, "over.txt")
	if err := os.WriteFile(overLimit, []byte(strings.Repeat("a", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := readBodyFile("real.json", dir); got != `{"type":"p2p"}` {
		t.Fatalf("regular file = %q, want JSON body", got)
	}
	if got := readBodyFile("-", dir); !strings.HasPrefix(got, "（读不到 ") {
		t.Fatalf("stdin sentinel = %q, want unreadable marker", got)
	}
	if got := readBodyFile("subdir", dir); !strings.HasPrefix(got, "（读不到 ") {
		t.Fatalf("directory = %q, want unreadable marker", got)
	}
	if got := readBodyFile(overLimit, dir); !strings.HasPrefix(got, "（读不到 ") {
		t.Fatalf("oversized file = %q, want unreadable marker", got)
	}
	if got := readBodyFile(exactLimit, dir); !strings.HasSuffix(got, "…") || utf8.RuneCountInString(got) != 4001 {
		t.Fatalf("1 MiB boundary file = len %d runes, ends %q; want 4001 runes ending with ellipsis", utf8.RuneCountInString(got), got[max(0, len(got)-6):])
	}

	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(realFile, link); err == nil {
		if got := readBodyFile(link, dir); !strings.HasPrefix(got, "（读不到 ") {
			t.Fatalf("symlink = %q, want unreadable marker", got)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatalf("symlink fixture failed: %v", err)
	}

	if runtime.GOOS != "windows" {
		for _, virtual := range []string{"/dev/null", "/proc/self/environ", "/sys/kernel/notes"} {
			if got := readBodyFile(virtual, dir); !strings.HasPrefix(got, "（读不到 ") {
				t.Fatalf("virtual path %q = %q, want unreadable marker", virtual, got)
			}
		}
	}

	// --url-query @file must enforce the same regular-file, size, symlink, and virtual-FS guards while
	// preserving os.DevNull as curl's empty-file sentinel.
	gates := olGate("")
	if v := classifyLark(`ex GET /health --url-query @`+os.DevNull, gates, dir, nil); v.deny != "" || len(v.calls) != 1 {
		t.Fatalf("--url-query @DevNull rejected: %+v", v)
	}
	for _, tc := range []struct {
		name string
		arg  string
	}{
		{"empty path", "@"},
		{"directory", "@subdir"},
		{"oversized file", "@" + overLimit},
	} {
		if v := classifyLark(`ex GET /health --url-query `+tc.arg, gates, dir, nil); v.deny == "" {
			t.Fatalf("--url-query %s (%q) was not denied: %+v", tc.name, tc.arg, v)
		}
	}
	if _, err := os.Lstat(link); err == nil {
		if v := classifyLark(`ex GET /health --url-query @`+link, gates, dir, nil); v.deny == "" {
			t.Fatalf("--url-query symlink was not denied: %+v", v)
		}
	}
	if runtime.GOOS != "windows" {
		for _, virtual := range []string{"/dev/zero", "/proc/self/environ", "/sys/kernel/notes"} {
			if v := classifyLark(`ex GET /health --url-query @`+virtual, gates, dir, nil); v.deny == "" {
				t.Fatalf("--url-query virtual %q was not denied: %+v", virtual, v)
			}
		}
	}
}

func TestLarkFiltersRejectWritingAndExecutionFlags(t *testing.T) {
	gates := olGate("")
	dir := t.TempDir()

	unsafeFilterCmds := []struct {
		name string
		cmd  string
	}{
		{"sort -o", `sort -o /tmp/out`},
		{"sort -o joined", `sort -o/tmp/out`},
		{"sort -uo", `sort -uo /tmp/out`},
		{"sort -ruo joined", `sort -ruo/tmp/out`},
		{"sort --output", `sort --output /tmp/out`},
		{"sort --output=", `sort --output=/tmp/out`},
		{"sort --out= abbrev", `sort --out=/tmp/out`},
		{"sort --compress-program", `sort --compress-program sh`},
		{"sort --compress-program=", `sort --compress-program=sh`},
		{"sort --comp= abbrev", `sort --comp=sh`},
		{"sort -T then -o", `sort -T /tmp -o /tmp/out`},
		{"date -s", `date -s '2026-01-01'`},
		{"date -s joined", `date -s2026-01-01`},
		{"date -us", `date -us '2026-01-01'`},
		{"date --set", `date --set '2026-01-01'`},
		{"date --set=", `date --set=2026-01-01`},
		{"date --se= abbrev", `date --se=2026-01-01`},
		{"date positional timestamp", `date 112612002026`},
		{"date end-of-options positional", `date -- -s`},
		{"uniq two operands", `uniq /tmp/in /tmp/out`},
		{"uniq flags then two operands", `uniq -c -f 1 /tmp/in /tmp/out`},
		{"uniq stdin to output file", `uniq - /tmp/out`},
		{"uniq end of options two operands", `uniq -- a b`},
	}

	for _, tc := range unsafeFilterCmds {
		t.Run("unsafe/"+tc.name, func(t *testing.T) {
			pipeline := `ex GET /health | ` + tc.cmd
			if v := classifyLark(pipeline, gates, dir, nil); !v.other {
				t.Fatalf("pipeline %q classified as filter-only: %+v", pipeline, v)
			}
			if larkLoneGateCall(pipeline, gates) {
				t.Fatalf("larkLoneGateCall accepted %q", pipeline)
			}
			if larkFilterSubstitution(tc.cmd) {
				t.Fatalf("larkFilterSubstitution accepted %q", tc.cmd)
			}
		})
	}

	benignFilterCmds := []struct {
		name string
		cmd  string
	}{
		{"sort -u", `sort -u`},
		{"sort -rn", `sort -rn`},
		{"sort -k -t", `sort -k 2,2 -t ,`},
		{"sort -t -o value", `sort -t -o`},
		{"sort -t-o value", `sort -t-o`},
		{"sort --key -o value", `sort --key -o`},
		{"sort end of options", `sort -- -o`},
		{"date plain", `date`},
		{"date -u format", `date -u +%Y-%m-%d`},
		{"date -I", `date -I`},
		{"date -Iseconds", `date -Iseconds`},
		{"date -uIseconds", `date -uIseconds`},
		{"date -d -s value", `date -d -s`},
		{"date -d-s value", `date -d-s`},
		{"date --date -s value", `date --date -s`},
		{"date end of options format", `date -- +%Y-%m-%d`},
		{"uniq stdin", `uniq`},
		{"uniq flags only", `uniq -c -i`},
		{"uniq single input file", `uniq -f 1 /tmp/in`},
		{"uniq joined -f single file", `uniq -f1 /tmp/in`},
		{"uniq single dash", `uniq -`},
		{"uniq end of options single file", `uniq -- -in`},
	}

	for _, tc := range benignFilterCmds {
		t.Run("benign/"+tc.name, func(t *testing.T) {
			pipeline := `ex GET /health | ` + tc.cmd
			if v := classifyLark(pipeline, gates, dir, nil); v.other || len(v.calls) != 1 {
				t.Fatalf("pipeline %q rejected benign filter: %+v", pipeline, v)
			}
			if !larkLoneGateCall(pipeline, gates) {
				t.Fatalf("larkLoneGateCall rejected benign filter %q", pipeline)
			}
			if !larkFilterSubstitution(tc.cmd) {
				t.Fatalf("larkFilterSubstitution rejected benign filter %q", tc.cmd)
			}
		})
	}

	t.Run("sort --compress-program with lark-cli is opaque", func(t *testing.T) {
		v := classifyLark(`lark-cli im +chat-messages-list --chat-id oc_a | sort --compress-program=/tmp/helper`, nil, dir, nil)
		if !v.other || !v.unknown {
			t.Fatalf("verdict = %+v, want other=true and unknown=true", v)
		}
	})

	t.Run("loop rejects unsafe sort and date flags", func(t *testing.T) {
		for _, cmd := range []string{
			`for id in 1111; do ex GET "/messages/$id/thread" | sort -o /tmp/out; done`,
			`for id in 1111; do ex GET "/messages/$id/thread" | sort --compress-program=sh; done`,
			`for id in 1111; do date -s '2026-01-01'; ex GET "/messages/$id/thread"; done`,
		} {
			if v := classifyLark(cmd, gates, dir, nil); !v.unknown {
				t.Fatalf("%q: verdict = %+v, want unknown=true", cmd, v)
			}
		}
	})
}
