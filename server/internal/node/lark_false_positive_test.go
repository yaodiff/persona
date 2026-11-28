package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestLarkLeadingGlobalOptions(t *testing.T) {
	needsLarkGate(t)
	for _, tc := range []struct {
		name, command, risk, path, actor, deny string
		calls                                  int
		other, write                           bool
	}{
		{"read", `lark-cli --as user wiki spaces get_node --params '{}' 2>&1 | head -c 1500`, "read", "/wiki/spaces/get_node", "以你的身份", "", 1, false, false},
		{"write", `lark-cli --as=user wiki spaces get_node --params '{}' 2>&1 | head -c 1500`, "write", "/wiki/spaces/get_node", "以你的身份", "", 1, false, true},
		{"bot", `lark-cli --as bot im +messages-send --text hi`, "write", "/im/+messages-send", "以机器人身份", "", 1, false, true},
		{"formats", `lark-cli --format=json -q '.data' --output=out wiki spaces get_node`, "read", "/wiki/spaces/get_node", "以你的身份", "", 1, false, false},
		{"unknown option", `lark-cli --weird x wiki spaces get_node`, "read", "", "", "", 0, true, true},
		{"profile", `lark-cli --as user --profile other wiki spaces get_node`, "read", "", "", "deny", 0, false, false},
		{"auth", `lark-cli --as user auth login`, "read", "", "", "deny", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyLark(tc.command, nil, "", func([]string) string { return tc.risk })
			if len(v.calls) != tc.calls || v.other != tc.other || v.cliWrite != tc.write || (v.deny != "") != (tc.deny != "") {
				t.Fatalf("verdict = %+v", v)
			}
			if tc.calls == 1 && (v.calls[0].Path != tc.path || larkCLIActor(v.calls[0].Args) != tc.actor) {
				t.Fatalf("call = %+v, actor = %q", v.calls[0], larkCLIActor(v.calls[0].Args))
			}
		})
	}
}

func TestLarkDefaultRiskLookup(t *testing.T) {
	for _, tc := range []struct{ name, response, want string }{{"read", "Risk: read", "read"}, {"write", "Risk: write", "write"}, {"unknown", "help", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "lark-cli"), []byte("#!/bin/sh\necho '"+tc.response+"'\n"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			n := &Node{}
			risk := n.larkCLIRisk(proto.AssistantLark{})
			if risk == nil {
				t.Fatal("risk lookup is nil")
			}
			if got := risk([]string{"wiki", "spaces", "get_node"}); got != tc.want {
				t.Fatalf("risk = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLarkInertHeredoc(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"cat source file", "cat > /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test ./...", 0},
		{"append source file", "cat >> /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test ./...", 0},
		{"tee append", "tee -a /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test ./...", 0},
		{"assignment heredoc substitution", "cat > /tmp/a.go <<'EOF'\n// lark-cli\nEOF\nA=1 <<EOF\n$(lark-cli im +messages-send)\nEOF", 1},
		{"redirect heredoc substitution", "cat > /tmp/a.go <<'EOF'\n// lark-cli\nEOF\n<<EOF\n$(lark-cli im +messages-send)\nEOF", 1},
		{"execute makefile", "cat > /tmp/Makefile <<'EOF'\nsend:\n\tlark-cli im +messages-send\nEOF\nmake -f /tmp/Makefile send", 1},
		{"execute go program", "cat > /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo run /tmp/a.go", 1},
		{"custom cat executable", "/tmp/cat > /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF", 1},
		{"PATH changes cat executable", "PATH=/tmp/tools:$PATH\ncat > /tmp/a.go <<'EOF'\nlark-cli im +messages-send --as user --text hi\nEOF", 1},
		{"test executable override", "cat > /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test -exec sh ./...", 1},
		{"test executes generated Go code", "cat > /tmp/a_test.go <<'EOF'\npackage x\nimport (\"testing\"; \"os/exec\")\nfunc TestSend(t *testing.T) { exec.Command(\"lark-cli\", \"im\", \"+messages-send\").Run() }\nEOF\ngo test ./...", 1},

		{"tee source file", "tee /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test ./...", 0},
		{"execute written file", "cat > /tmp/a.sh <<'EOF'\nlark-cli im +messages-send\nEOF\nsh /tmp/a.sh", 1},
		{"execute chmod file", "cat > /tmp/a.sh <<'EOF'\nlark-cli im +messages-send\nEOF\nchmod +x /tmp/a.sh; /tmp/a.sh", 1},
		{"shell stdin", "bash <<'EOF'\nlark-cli im +messages-send\nEOF", 1},
		{"python stdin", "python - <<'EOF'\nlark-cli im +messages-send\nEOF", 1},
		{"unquoted substitution", "cat > /tmp/a.go <<EOF\n$(lark-cli im +messages-send)\nEOF", 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

func TestLarkLiteralCommandVariable(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"literal", `A=/usr/bin/ab; timeout 30 $A scroll`, 0},
		{"braced literal", `A=/usr/bin/ab; timeout 30 ${A} scroll`, 0},
		{"literal after harmless command", `A=/usr/bin/ab; echo ok; timeout 30 $A scroll`, 0},
		{"literal after external command", `A=/usr/bin/ab; ls /tmp; timeout 30 $A scroll`, 0},
		{"repeated literal command", `A=/usr/bin/ab; $A status; timeout 30 $A scroll`, 0},
		{"repeated wrapped literal command", `A=/usr/bin/ab; timeout 30 $A status; timeout 30 $A scroll`, 0},
		{"unset after literal command", `A=/usr/bin/ab; $A status; unset A; timeout 30 $A scroll`, 1},
		{"dynamic argument after literal command", `A=/usr/bin/ab; $A "$ARG"; timeout 30 $A scroll`, 1},

		{"dynamic argument can assign", `A=/usr/bin/ab; echo ${A:=/x/lark-cli}; timeout 30 $A scroll`, 1},
		{"unset value", `A=/usr/bin/ab; unset A; timeout 30 $A scroll`, 1},
		{"substitution", `A=$(which x); $A`, 1},
		{"unassigned", `$A`, 1},
		{"dynamic value", `A=$B; $A`, 1},
		{"IFS splits literal command", `A=lark-cli/im; IFS=/; $A +messages-send --as user --text hi`, 1},
		{"arithmetic changes IFS", `A=lark-cli0im; B=$((IFS=0)); $A +messages-send --as user --text hi`, 1},
		{"wrapper expansion changes IFS", `A=lark-cli0im; timeout "$((IFS=0))" $A +messages-send --as user --text hi`, 1},
		{"nameref changes command", "builtin declare -n A=B\nA=/usr/bin/ab\nB=lark-cli\n$A im +messages-send --as user --text hi", 1},
		{"lark send", `A=/x/lark-cli; $A im +messages-send --text hi`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

func TestLarkLiteralGetWithShellVariables(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		calls         int
	}{
		{"single", `ex GET "/documents/read?url=https://example.test/docx/a"`, 1},
		{"for loop", `for x in a b; do ex GET "/documents/read?url=$x" > /tmp/o_$x.json; done`, 2},
		{"while loop", `x=a; while [ "$x" = a ]; do ex GET "/documents/read?url=$x" > /tmp/o_$x.json; x=b; done`, 1},
		{"incident", `for x in "docx a b" "wiki c d"; do set -- $x; ex GET "/documents/read?url=https://example.larkoffice.com/$2/$1" > /tmp/d_$3.json; python3 -I x.py; echo "$3 $(jq -r '.data.title' /tmp/d_$3.json | tr '\n' ' ') $(wc -c < /tmp/d_$3.json)"; done`, 1},
		{"output substitutions", `ex GET /health; echo "$(jq -r '.data.title' /tmp/o.json | tr '\n' ' ') $(wc -c < /tmp/o.json)"`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyLark(tc.command, olGate(""), t.TempDir(), nil)
			if v.deny != "" || v.unparsed || v.unknown || v.unsure || len(v.calls) != tc.calls {
				t.Fatalf("verdict = %+v", v)
			}
			for _, call := range v.calls {
				if call.Method != "GET" || larkDecision(olCfg(), call) != "allow" || !larkBypassSafe(call) {
					t.Fatalf("call = %+v, default decision = %q, bypass safe = %v", call, larkDecision(olCfg(), call), larkBypassSafe(call))
				}
			}
			assertAssistantBypassClassification(t, tc.command, 0)
		})
	}
}

func TestLarkLoopWritesStayBehindApproval(t *testing.T) {
	for _, tc := range []struct {
		name, command string
	}{
		{"post", `for x in a b; do ex POST "/documents/$x" '{}'; done`},
		{"put", `for x in a b; do ex PUT "/documents/$x" '{}'; done`},
		{"patch", `for x in a b; do ex PATCH "/documents/$x" '{}'; done`},
		{"delete", `for x in a b; do ex DELETE "/documents/$x"; done`},
		{"variable read method", `for method in GET; do ex "$method" /health; done`},
		{"variable write method", `for method in POST; do ex "$method" /documents '{}'; done`},
		{"variable command", `for x in a b; do $CLI GET "/documents/$x"; done`},
		{"dynamic route can hide deletion", `for action in delete; do ex GET "/documents/$action"; done`},
		{"loop shadows previous route", "action=read\nfor action in delete; do ex GET \"/documents/$action\"; done"},
		{"conditional assignment keeps previous route", `action=delete; false && action=read; ex GET "/documents/$action"`},
		{"binary builtin keeps previous route", "action=read\ntrue && printf -v action delete\nex GET \"/documents/$action\""},
		{"getopts keeps previous route", "action=read\ngetopts a action -a\nex GET \"/documents/$action\""},
		{"redirect arithmetic keeps previous route", "action=1\n: > \"/tmp/$((action=2))\"\nex GET \"/documents/$action\""},
		{"append assignment keeps previous route", "action=dele\naction+=te\nex GET \"/documents/$action\""},
		{"array assignment keeps previous route", "action=(read delete)\nex GET \"/documents/${action[1]}\""},
		{"indexed assignment keeps previous route", "action=read\naction[0]=delete\nex GET \"/documents/$action\""},
		{"declaration changes route case", "declare -l action=INITIAL\naction=DELETE\nex GET \"/documents/$action\""},
		{"unparsed redirect gate call", `{ ex</dev/null POST /documents; }`},
		{"unquoted route becomes delete", `for route in '/health -X DELETE'; do ex GET $route; done`},
		{"unquoted query becomes delete", `for query in 'q=a -X DELETE'; do ex GET /health --url-query $query; done`},
		{"command substitution becomes delete", `ex GET /health --url-query $(printf 'q=a -X DELETE')`},
		{"quoted positional expansion becomes delete", `set -- /health -X DELETE; ex GET "$@"`},
		{"brace expansion becomes delete", `for route in ''; do ex GET "$route"{/health,-X,DELETE}; done`},
		{"quoted brace expansion becomes delete", `for route in ''; do ex GET "$route"{/health,"-X",DELETE}; done`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyLark(tc.command, olGate(""), t.TempDir(), nil)
			if v.deny == "" && !v.unknown {
				for _, call := range v.calls {
					if larkDecision(olCfg(), call) == "allow" {
						t.Fatalf("write unexpectedly allowed: %+v", call)
					}
				}
			}
			assertAssistantBypassClassification(t, tc.command, 1)
		})
	}
}

func TestLarkHeredocUnsafeTargets(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Logf("FIFO case unavailable: %v", err)
		fifo = ""
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("/dev/null", link); err != nil {
		t.Logf("Symlink case unavailable: %v", err)
		link = ""
	}
	for _, tc := range []struct{ name, command string }{
		{"device", "cat > /dev/null <<'EOF'\n lark-cli im +messages-send\nEOF"},
		{"proc fd", "cat > /proc/self/fd/1 <<'EOF'\n lark-cli im +messages-send\nEOF"},
		{"tee fd", "tee /dev/fd/1 <<'EOF'\n lark-cli im +messages-send\nEOF"},
		{"multiple output redirections", "cat > /tmp/a.go > /dev/fd/1 <<'EOF'\n lark-cli im +messages-send\nEOF"},
		{"tee stdout redirect", "tee /tmp/a.go > /dev/fd/1 <<'EOF'\n lark-cli im +messages-send\nEOF"},

		{"fifo", "cat > " + fifo + " <<'EOF'\n lark-cli im +messages-send\nEOF"},
		{"symbolic link", "cat > " + link + " <<'EOF'\n lark-cli im +messages-send\nEOF"},
		{"long exec option", "cat > /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test --exec sh ./..."},
		{"long toolexec option", "cat > /tmp/a.go <<'EOF'\n// lark-cli im +messages-send\nEOF\ngo test --toolexec sh ./..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "fifo" && fifo == "" || tc.name == "symbolic link" && link == "" {
				t.Skip("platform fixture unavailable")
			}
			assertAssistantBypassClassification(t, tc.command, 1)
		})
	}
}

func TestLarkLiteralForLoops(t *testing.T) {
	gates := olGate("")
	alias := gates[0]
	alias.Command = "ol"
	gates = append(gates, alias)
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"message threads", `for id in 1111 2222 3333 4444; do echo "== $id"; timeout 60 ol GET "/messages/$id/thread" | jq -c '.items[]?' | head -20; done`, 0},
		{"meeting insights", `for h in 1111 2222 3333 4444; do timeout 60 ol GET "/meetings/$h/insights" > /tmp/ins_$h.json 2>&1; head -c 100 /tmp/ins_$h.json; done`, 0},
		{"binary versions", `for b in ~/.persona/bin/personad ~/.persona/bin/persona-node; do [ -x "$b" ] && { echo "== $b"; timeout 10 "$b" version 2>&1 | head -3; }; done`, 0},
		{"legacy binary versions", `for b in ~/.persona/bin/personad ~/.persona/bin/` + compat.DaemonBinary + `; do [ -x "$b" ] && { echo "== $b"; timeout 10 "$b" version 2>&1 | head -3; }; done`, 0},
		{"quoted list and braced route", `for id in '1111' "2222"; do ex GET "/messages/${id}/thread"; done`, 0},
		{"unquoted scalar route", `for id in 1111 2222; do ex GET /messages/$id/thread; done`, 0},
		{"ordinary command names", `for b in pwd ls; do "$b"; done`, 0},
		{"absolute binary", `for b in /opt/tools/personad /usr/local/bin/personad; do timeout 10 ${b} version; done`, 0},
		{"literal nested loops", `for id in 1111 2222; do for kind in thread replies; do ex GET "/messages/$id/$kind"; done; done`, 0},
		{"quoted space in route", `for id in 'item one' 'item two'; do ex GET "/messages/$id/thread"; done`, 0},
		{"deletion route", `for action in delete; do ex GET "/documents/$action"; done`, 1},
		{"later value is deletion", `for action in read delete; do ex GET "/documents/$action"; done`, 1},
		{"unquoted argument injection", `for route in '/health -X DELETE'; do ex GET $route; done`, 1},
		{"empty unquoted command", `for b in ''; do $b ex DELETE /documents; done`, 1},
		{"parameter list", `for id in $IDS; do ex GET "/messages/$id/thread"; done`, 1},
		{"command substitution list", `for id in $(cat /tmp/ids); do ex GET "/messages/$id/thread"; done`, 1},
		{"arithmetic list", `for id in $((1+1)); do ex GET "/messages/$id/thread"; done`, 1},
		{"glob list", `for id in /tmp/*; do ex GET "/messages/$id/thread"; done`, 1},
		{"quoted glob list", `for id in '*'; do ex GET "/messages/$id/thread"; done`, 1},
		{"brace list", `for id in {1111,2222}; do ex GET "/messages/$id/thread"; done`, 1},
		{"implicit list", `for id; do ex GET "/messages/$id/thread"; done`, 1},
		{"assignment in body", `for action in read; do action=delete; ex GET "/documents/$action"; done`, 1},
		{"read in body", `for action in read; do read action; ex GET "/documents/$action"; done`, 1},
		{"getopts in body", `for action in read; do getopts a action -a; ex GET "/documents/$action"; done`, 1},
		{"printf variable in body", `for action in read; do printf -v action delete; ex GET "/documents/$action"; done`, 1},
		{"wrapped printf variable", `for action in read; do builtin printf -v action delete; ex GET "/documents/$action"; done`, 1},
		{"assignment after call", `for action in read; do ex GET "/documents/$action"; action=delete; done`, 1},
		{"dynamic outer loop", `for x in $IDS; do for action in read; do ex GET "/documents/$action"; done; done`, 1},
		{"dynamic inner loop", `for action in read; do for action in $ACTIONS; do ex GET "/documents/$action"; done; done`, 1},
		{"IFS before loop", `IFS=/; for b in /opt/tools/personad; do $b version; done`, 1},
		{"PATH before loop", `PATH=/tmp/tools; for id in 1111; do ex GET "/messages/$id/thread"; done`, 1},
		{"IFS in body", `for b in /opt/tools/personad; do IFS=/; $b version; done`, 1},
		{"PATH in body", `for id in 1111; do PATH=/tmp/tools; ex GET "/messages/$id/thread"; done`, 1},
		{"IFS loop binding", `for IFS in /; do for b in /opt/tools/personad; do $b version; done; done`, 1},
		{"PATH loop binding", `for PATH in /tmp/tools; do for id in 1111; do ex GET "/messages/$id/thread"; done; done`, 1},
		{"environment wrapper", `for b in /opt/tools/personad; do env PATH=/tmp/tools timeout 10 "$b" version; done`, 1},
		{"CLI command binding", `for b in lark-cli; do "$b" docs +fetch; done`, 1},
		{"gate command binding", `for b in ol; do "$b" GET /health; done`, 1},
		{"shell command binding", `for b in sh; do "$b" -c 'echo ok'; done`, 1},
		{"eval command binding", `for b in eval; do "$b" 'echo ok'; done`, 1},
		{"source command binding", `for b in source; do "$b" /tmp/task.sh; done`, 1},
		{"dot command binding", `for b in .; do "$b" /tmp/task.sh; done`, 1},
		{"wrapper command binding", `for b in timeout; do "$b" 10 pwd; done`, 1},
		{"later command is shell", `for b in /opt/tools/personad sh; do "$b" version; done`, 1},
		{"arithmetic changes variable", `for action in read; do echo "$((action=1))"; ex GET "/documents/$action"; done`, 1},
		{"parameter assigns variable", `for action in read; do echo "${action:=delete}"; ex GET "/documents/$action"; done`, 1},
		{"nested loop shadows binding", `for action in read; do for action in delete; do echo "$action"; done; ex GET "/documents/$action"; done`, 1},
		{"read changes command", `for b in /opt/tools/personad; do read b; "$b" version; done`, 1},
		{"getopts changes command", `for b in /opt/tools/personad; do getopts a b -a; "$b" version; done`, 1},
		{"printf changes command", `for b in /opt/tools/personad; do printf -v b sh; "$b" version; done`, 1},
		{"function changes binding", `change() { action=delete; }; for action in read; do change; ex GET "/documents/$action"; done`, 1},
		{"existing literal CLI write policy", `for id in 1111; do lark-cli docs +create; done`, 0},
		{"existing literal gate write policy", `for id in 1111; do ex POST /documents '{}'; done`, 0},
		{"quote removal", `for id in i'tem' "i"tem; do ex GET "/messages/$id/thread"; done`, 0},
		{"escaped literal", `for id in item\ one; do ex GET "/messages/$id/thread"; done`, 0},
		{"glob beside bound route", `for id in 1111; do ex GET /messages/$id/*; done`, 1},
		{"brace beside bound route", `for id in 1111; do ex GET /messages/$id/{thread,replies}; done`, 1},
		{"conditional binary", `for b in ~/.persona/bin/personad; do if [ -x "$b" ]; then "$b" version; else echo missing; fi; done`, 0},
		{"conditional mutation", `for action in read; do if true; then action=delete; fi; ex GET "/documents/$action"; done`, 1},
		{"computed shell variable", `for RANDOM in 1111; do ex GET "/messages/$RANDOM/thread"; done`, 1},
		{"bounded list", `for id in ` + strings.Repeat("1111 ", 257) + `; do ex GET "/messages/$id/thread"; done`, 1},
		{"bounded Cartesian product", `for id in ` + strings.Repeat("1111 ", 17) + `; do for kind in ` + strings.Repeat("thread ", 17) + `; do ex GET "/messages/$id/$kind"; done; done`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards, gates...) })
	}
}

func TestLarkLiteralForLoopRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		paths         []string
		unknown       bool
	}{
		{"each message", `for id in 1111 2222; do ex GET "/messages/$id/thread"; done`, []string{"/messages/1111/thread", "/messages/2222/thread"}, false},
		{"each nested value", `for id in 1111 2222; do for kind in thread replies; do ex GET "/messages/$id/$kind"; done; done`, []string{"/messages/1111/thread", "/messages/1111/replies", "/messages/2222/thread", "/messages/2222/replies"}, false},
		{"deletion remains visible", `for action in read delete; do ex GET "/documents/$action"; done`, []string{"/documents/read", "/documents/delete"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := classifyLark(tc.command, olGate(""), t.TempDir(), nil)
			if len(v.calls) != len(tc.paths) || v.unknown != tc.unknown || v.deny != "" || v.unparsed || v.unsure {
				t.Fatalf("verdict = %+v", v)
			}
			for i, path := range tc.paths {
				if v.calls[i].Path != path {
					t.Fatalf("call %d = %+v, want route %q", i, v.calls[i], path)
				}
			}
		})
	}
}

func TestLarkLiteralForLoopCommandBoundaries(t *testing.T) {
	for name := range commandWrappers {
		t.Run(name, func(t *testing.T) {
			assertAssistantBypassClassification(t, `for b in `+name+`; do "$b" version; done`, 1)
		})
	}
	for name := range commandShells {
		t.Run(name, func(t *testing.T) {
			assertAssistantBypassClassification(t, `for b in /bin/`+name+`; do "$b" version; done`, 1)
		})
	}
}

// Reads stay reads when lark-cli cannot report its risk levels (#8389): the binary the node finds may be a wrapper that
// cannot reach the real lark-cli from the node's PATH, so the lookup returns nothing. The helper's binary is missing.
func TestLarkReadsWithoutRiskDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"single read", `timeout 30 /opt/x/lark-cli im +chat-messages-list --as user --chat-id oc_a --page-size 50 --no-reactions 2>&1 | head -c 30000`, 0},
		{"search", `lark-cli im +messages-search --query standup --format json | jq -r '.data.items[].chat_id' | sort -u`, 0},
		{"mget", `lark-cli im +messages-mget --message-ids om_a,om_b --format json > /tmp/m.json 2>&1`, 0},
		{"thread", `lark-cli im +threads-messages-list --thread omt_a | tee /tmp/t.json | jq '.data.items|length'`, 0},
		{"chat search", `lark-cli im +chat-search --query team --page-size 20`, 0},
		{"incident loop", `for c in oc_a oc_b; do echo "=== $c"; timeout 30 /opt/x/lark-cli im +chat-messages-list --as user --chat-id $c --start 2026-11-15T12:30:00+08:00 --end 2026-11-15T14:30:00+08:00 --page-size 50 --no-reactions 2>&1 | head -c 30000; done`, 0},
		{"loop with filters", `for c in oc_a oc_b; do lark-cli im +chat-messages-list --chat-id $c --page-size 20 | jq -r '.data.items[]|.body.content' | head -20; done`, 0},
		{"incident help", `cd /tmp; L=/opt/x/lark-cli; timeout 20 $L im +chat-messages-list --help 2>&1 | head -40`, 0},
		{"help after cd, then read", `cd /tmp; L=/opt/x/lark-cli; $L im +chat-messages-list --help; $L im +chat-messages-list --chat-id oc_a | head`, 0},

		// Writes and unknown risk stay behind approval.
		{"send", `lark-cli im +messages-send --chat-id oc_a --text hi`, 1},
		{"reply", `lark-cli im +messages-reply --message-id om_a --text hi`, 1},
		{"download writes", `lark-cli im +messages-resources-download --message-id om_a --file-key k`, 1},
		{"chat update", `lark-cli im +chat-update --chat-id oc_a --name x`, 1},
		{"service create", `lark-cli im messages create --params '{"receive_id_type":"chat_id"}' --data '{}'`, 1},
		{"api post", `lark-cli api POST /open-apis/im/v1/messages --data '{}'`, 1},
		{"api delete", `lark-cli api DELETE /open-apis/im/v1/messages/om_a`, 1},
		{"unknown shortcut", `lark-cli im +chat-messages-purge --chat-id oc_a`, 1},
		{"read shortcut as a prefix", `lark-cli im +chat-messages-list-and-send --chat-id oc_a`, 1},
		{"loop send", `for c in oc_a oc_b; do echo "== $c"; timeout 30 lark-cli im +messages-send --chat-id $c --text hi 2>&1 | head; done`, 1},
		{"loop mixes read and send", `for s in +chat-messages-list +messages-send; do lark-cli im $s --chat-id oc_a --text hi; done`, 1},
		{"send in substitution", `echo $(lark-cli im +messages-send --chat-id oc_a --text hi)`, 1},
		{"xargs send", `printf 'oc_a\n' | xargs -I{} lark-cli im +messages-send --chat-id {} --text hi`, 1},
		{"eval send", `eval "lark-cli im +messages-send --chat-id oc_a --text hi"`, 1},
		{"bash -c send", `timeout 20 bash -c 'lark-cli im +messages-send --chat-id oc_a --text hi'`, 1},
		{"variable subcommand", `s=+messages-send; lark-cli im $s --chat-id oc_a --text hi`, 1},
		{"help then send", `lark-cli im +chat-messages-list --help; lark-cli im +messages-send --chat-id oc_a --text hi`, 1},
		{"help is a text value", `lark-cli im +messages-send --chat-id oc_a --text --help`, 1},
		{"cd, help, then send", `cd /tmp; L=/opt/x/lark-cli; timeout 20 $L im +chat-messages-list --help 2>&1 | head; timeout 20 $L im +messages-send --chat-id oc_a --text hi`, 1},
		{"cd then relative command", `cd /tmp; L=./lark-cli; $L im +chat-messages-list --help`, 1},
		{"cd then bare command", `cd /tmp; L=lark-cli; $L im +chat-messages-list --help`, 1},
		{"dynamic cd", `cd "$D"; L=/opt/x/lark-cli; $L im +chat-messages-list --help`, 1},
		{"reassigned after cd", `cd /tmp; L=/opt/x/lark-cli; L=$(which x); $L im +chat-messages-list --help`, 1},
		{"read output as a command", `$(lark-cli im +chat-messages-list --chat-id oc_a | jq -r '.data.items[0].body.content')`, 1},
		{"read output into bash", `x=$(lark-cli im +chat-messages-list --chat-id oc_a); bash -c "$x"`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

// The fallback covers only an unknown risk: what lark-cli says wins.
func TestLarkKnownReadsDoNotOverrideReportedRisk(t *testing.T) {
	for _, tc := range []struct{ risk, want string }{{"", "read"}, {"read", "read"}, {"write", "write"}, {"high-risk-write", "high-risk-write"}} {
		v := classifyLark(`lark-cli im +chat-messages-list --chat-id oc_a`, nil, "", func([]string) string { return tc.risk })
		if len(v.calls) != 1 || v.calls[0].Risk != tc.want {
			t.Fatalf("risk %q: verdict = %+v", tc.risk, v)
		}
	}
}

// A gated call's @file body is read from the conversation's directory. Anything else in the command may run the call
// elsewhere (cd however spelled, env -C, sudo -D, a subshell), so a relative body file is known only for a lone call,
// at most under timeout and piped into filters (#8404). On an Auth-less gate in default mode, an @file body never
// selects a silent body-scoped allow because no proxy can verify the body at send time (#8413); inline bodies and
// authenticated proxy gates keep their body-scoped allow.
func TestLarkRelativeBodyFileAfterCd(t *testing.T) {
	cwd, other := t.TempDir(), t.TempDir()
	_ = os.WriteFile(filepath.Join(cwd, "body.json"), []byte(`{"type":"p2p"}`), 0o644)
	_ = os.WriteFile(filepath.Join(other, "body.json"), []byte(`{"type":"group"}`), 0o644)
	script := "/opt/skills/example/scripts/ex.sh"
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"no cd", `ex POST /conversations @body.json`, 1},
		{"inline body", `ex POST /conversations '{"type":"p2p"}'`, 0},
		{"no cd, timeout", `timeout 20 ex POST /conversations @body.json | jq .`, 1},
		{"absolute after cd", `cd ` + other + `; ex POST /conversations @` + cwd + `/body.json`, 1},
		{"absolute after cd, other body", `cd ` + other + `; ex POST /conversations @` + other + `/body.json`, 1},
		{"bare after cd", `cd ` + other + `; ex POST /conversations @body.json`, 1},
		{"variable after cd", `cd ` + other + `; G=` + script + `; $G POST /conversations @body.json`, 1},
		{"after cd in a loop", `for d in a; do cd ` + other + `; ex POST /conversations @body.json; done`, 1},
		{"cd later in a loop", `for d in a b; do ex POST /conversations @body.json; cd ` + other + `; done`, 1},
		{"pushd", `pushd ` + other + `; ex POST /conversations @body.json`, 1},
		{"cd in bash -c", `bash -c 'cd ` + other + `; ex POST /conversations @body.json'`, 1},
		{"cd and &&", `cd ` + other + ` && ex POST /conversations @./body.json`, 1},
		{"escaped cd, variable", `\cd ` + other + `; G=` + script + `; $G POST /conversations @body.json`, 1},
		{"escaped cd", `\cd ` + other + `; ex POST /conversations @body.json`, 1},
		{"empty double quotes in cd", `c""d ` + other + `; ex POST /conversations @body.json`, 1},
		{"empty single quotes in cd", `c''d ` + other + `; ex POST /conversations @body.json`, 1},
		{"quoted c", `'c'd ` + other + `; ex POST /conversations @body.json`, 1},
		{"escaped d", `c\d ` + other + `; ex POST /conversations @body.json`, 1},
		{"env -C", `env -C ` + other + ` ex POST /conversations @body.json`, 1},
		{"env --chdir", `env --chdir=` + other + ` ex POST /conversations @body.json`, 1},
		{"timeout env -C", `timeout 20 env -C ` + other + ` ex POST /conversations @body.json`, 1},
		{"sudo -D", `sudo -D ` + other + ` ex POST /conversations @body.json`, 1},
		{"subshell cd", `(cd ` + other + ` && ex POST /conversations @body.json)`, 1},
		{"redirected", `ex POST /conversations @body.json > /tmp/out.json`, 1},
		{"after another command", `true; ex POST /conversations @body.json`, 1},
		{"body written first", `echo '{"type":"group"}' > body.json; ex POST /conversations @body.json`, 1},
		{"absolute after escaped cd", `\cd ` + other + `; ex POST /conversations @` + cwd + `/body.json`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			p, c, _ := newFakeCodex(t, n, cwd)
			p.id, c.threadID = testThread, testThread
			n.procs[testThread] = p
			n.larkCfg, n.larkCfgAt = olCfg(), time.Now().Add(time.Hour)
			n.noteModes(testThread, "default", "")
			fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
			decision, _, err := n.larkCheck(context.Background(), testThread, cwd, tc.command, proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if got := len(fh.interactions()); got != tc.cards || tc.cards == 0 && decision != "allow" {
				t.Fatalf("decision=%q cards=%d; want %d", decision, got, tc.cards)
			}
		})
	}
	// Authenticated proxy gates keep #8404's lone-call relative @file boundary while allowing lone and absolute @file calls.
	// Non-standalone relative @file calls are marked v.other and refused directly for proxy-authenticated writes.
	proxiedCfg := olCfg()
	proxiedCfg.Gates[0] = proxiedGate("http://127.0.0.1:1")
	for _, tc := range []struct {
		name, command, wantDecision string
	}{
		{"proxied/no cd", `ex POST /conversations @body.json`, "allow"},
		{"proxied/no cd, timeout", `timeout 20 ex POST /conversations @body.json | jq .`, "allow"},
		{"proxied/absolute after cd", `cd ` + other + `; ex POST /conversations @` + cwd + `/body.json`, "allow"},
		{"proxied/absolute after escaped cd", `\cd ` + other + `; ex POST /conversations @` + cwd + `/body.json`, "allow"},
		{"proxied/bare after cd", `cd ` + other + `; ex POST /conversations @body.json`, "deny"},
		{"proxied/env -C", `env -C ` + other + ` ex POST /conversations @body.json`, "deny"},
		{"proxied/redirected", `ex POST /conversations @body.json > /tmp/out.json`, "deny"},
		{"proxied/body written first", `echo '{"type":"group"}' > body.json; ex POST /conversations @body.json`, "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			p, c, _ := newFakeCodex(t, n, cwd)
			p.id, c.threadID = testThread, testThread
			n.procs[testThread] = p
			n.larkCfg, n.larkCfgAt = proxiedCfg, time.Now().Add(time.Hour)
			n.noteModes(testThread, "default", "")
			fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
			decision, _, err := n.larkCheck(context.Background(), testThread, cwd, tc.command, proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if decision != tc.wantDecision || len(fh.interactions()) != 0 {
				t.Fatalf("decision=%q cards=%d; want decision=%q cards=0", decision, len(fh.interactions()), tc.wantDecision)
			}
		})
	}
}
