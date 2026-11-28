package node

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// For a command it does not have, lark-cli prints the help of the group above it, with no Risk: line (#10845). The
// gate lets such a command run, so lark-cli tells the assistant "unknown subcommand", instead of carding a write that
// cannot happen. The fixtures are lark-cli 1.0.98's own output.

func larkHelp(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "larkcli", "1.0.98-"+name+"-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// noRiskHelp is a method's own help without its Risk: line: a command that exists but gives no risk level.
func noRiskHelp(t *testing.T) string {
	t.Helper()
	read := larkHelp(t, "im-messages-read_status")
	help := strings.Replace(read, "Risk: read\n", "", 1)
	if help == read {
		t.Fatal("the read_status fixture has no Risk: line")
	}
	return help
}

func TestLarkRiskOfHelp(t *testing.T) {
	get := larkHelp(t, "im-messages-get") // `lark-cli im messages get --help`: the help of `im messages`
	im := larkHelp(t, "im")               // `lark-cli im --help`, which is also what `im +chat-serch --help` prints
	for _, tc := range []struct{ name, help, words, want string }{
		{"a method the group does not have", get, "im messages get", larkCLIMissing},
		{"a shortcut the domain does not have", im, "im +chat-serch", larkCLIMissing},
		{"a real method", larkHelp(t, "im-messages-read_status"), "im messages read_status", "read"},
		{"a real method without a Risk line", noRiskHelp(t), "im messages read_status", ""},
		// Each condition on its own keeps a help from saying "no such command".
		{"Usage names a shorter command", get, "im messages get om_x", ""},
		{"Usage names a longer command", get, "im messages", ""},
		{"Usage takes an argument", strings.Replace(get, "  lark-cli im messages [flags]", "  lark-cli im messages <message_id> [flags]", 1), "im messages get", ""},
		{"no Usage", strings.Replace(get, "Usage:", "Use:", 1), "im messages get", ""},
		{"no Available Commands", noRiskHelp(t), "im messages read_status om_x", ""},
		{"the word is an Available Command", get, "im messages delete", ""},
		{"no output", "", "im messages get", ""},
	} {
		if got := larkRiskOf([]byte(tc.help), strings.Fields(tc.words)); got != tc.want {
			t.Errorf("%s (%s): got %q, want %q", tc.name, tc.words, got, tc.want)
		}
	}
}

// fakeLarkCLI is a lark-cli that answers `<words…> --help` with helps[words] (nothing for the rest), exits with exit,
// and notes each call in the returned file.
func fakeLarkCLI(t *testing.T, helps map[string]string, exit int) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + shellQuote(calls) + "\ncase \"$*\" in\n"
	i := 0
	for words, help := range helps {
		file := filepath.Join(dir, "help"+strconv.Itoa(i))
		i++
		if err := os.WriteFile(file, []byte(help), 0o600); err != nil {
			t.Fatal(err)
		}
		script += shellQuote(words+" --help") + ") cat " + shellQuote(file) + " ;;\n"
	}
	script += "esac\nexit " + strconv.Itoa(exit) + "\n"
	bin = filepath.Join(dir, "lark-cli")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, calls
}

func larkCLICalls(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// "No such command" is cached like a risk level; a lark-cli that cannot run, or a help run that fails, is asked again.
func TestLarkCLIRiskMissing(t *testing.T) {
	helps := map[string]string{"im messages get": larkHelp(t, "im-messages-get")}
	n := codexTestNode(t)
	bin, calls := fakeLarkCLI(t, helps, 0)
	risk := n.larkCLIRisk(proto.AssistantLark{CLI: &proto.LarkCLI{Bin: bin}})
	for range 2 {
		if r := risk([]string{"im", "messages", "get"}); r != larkCLIMissing {
			t.Fatalf("im messages get: %q", r)
		}
	}
	if got := larkCLICalls(t, calls); len(got) != 1 {
		t.Fatalf("lark-cli asked %d times: %q", len(got), got)
	}

	n = codexTestNode(t)
	bin, calls = fakeLarkCLI(t, helps, 1)
	risk = n.larkCLIRisk(proto.AssistantLark{CLI: &proto.LarkCLI{Bin: bin}})
	for range 2 {
		if r := risk([]string{"im", "messages", "get"}); r != "" {
			t.Fatalf("a failed help run: %q", r)
		}
	}
	if got := larkCLICalls(t, calls); len(got) != 2 || len(n.larkRisks) != 0 {
		t.Fatalf("a failed help run is cached: calls %q, cache %v", got, n.larkRisks)
	}

	n = codexTestNode(t)
	risk = n.larkCLIRisk(proto.AssistantLark{CLI: &proto.LarkCLI{Bin: filepath.Join(t.TempDir(), "lark-cli")}})
	if r := risk([]string{"im", "messages", "get"}); r != "" || len(n.larkRisks) != 0 {
		t.Fatalf("no lark-cli: %q, cache %v", r, n.larkRisks)
	}
}

// In the assistant's conversation, bypass or not, a method lark-cli does not have runs without a card; a real
// method goes by its risk level, and one without a level, a help that only looks like a group's, or a lark-cli that
// cannot run still shows the card. Each command is checked twice: only a level or "no such command" is cached.
func TestLarkCLIMissingCommandHasNoCard(t *testing.T) {
	needsLarkGate(t)
	get := larkHelp(t, "im-messages-get")
	helps := map[string]string{
		"im messages get":         get,
		"im messages get om_x":    get, // lark-cli prints the group's help for any words below it
		"im messages read_status": larkHelp(t, "im-messages-read_status"),
		"im messages read_users":  strings.ReplaceAll(noRiskHelp(t), "read_status", "read_users"),
	}
	const missing = `lark-cli im messages get --params '{"message_id":"om_x"}' --as user`
	for _, mode := range []string{proto.ModeBypass, "default"} {
		for _, tc := range []struct {
			name, command string
			cards, asked  int // cards per check; lark-cli --help runs over both checks
			noCLI         bool
		}{
			{"missing method", missing, 0, 1, false},
			{"missing method piped", missing + ` | jq -r '.error.message'`, 0, 1, false},
			{"real read", `lark-cli im messages read_status --params '{"message_id":"om_x"}' --as user`, 0, 1, false},
			{"real method without a risk level", `lark-cli im messages read_users --params '{"message_id":"om_x"}' --as user`, 1, 2, false},
			{"Usage that names another command", `lark-cli im messages get om_x --as user`, 1, 2, false},
			{"lark-cli cannot run", `lark-cli im messages read_users --params '{"message_id":"om_x"}' --as user`, 1, 0, true},
			// Named as a read, a method still runs without a card when lark-cli cannot be asked (#11193).
			{"read-named method when lark-cli cannot run", missing, 0, 0, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				bin, calls := fakeLarkCLI(t, helps, 0)
				if tc.noCLI {
					bin = filepath.Join(t.TempDir(), "lark-cli")
				}
				cfg := olCfg()
				cfg.CLI = &proto.LarkCLI{Bin: bin}
				n, fh := readCardBypassNode(t, cfg)
				n.persistModes(testThread, mode, "")
				want := "allow"
				if tc.cards > 0 {
					want = "deny" // the fake owner says no
				}
				for i := 1; i <= 2; i++ {
					decision, reason, err := n.larkCheck(context.Background(), testThread, t.TempDir(), tc.command,
						proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
					if cards := fh.interactions(); err != nil || decision != want || len(cards) != i*tc.cards {
						t.Fatalf("check %d: decision=%q reason=%q err=%v cards=%d, want %s/%d", i, decision, reason, err, len(cards), want, i*tc.cards)
					}
				}
				if got := larkCLICalls(t, calls); len(got) != tc.asked {
					t.Fatalf("lark-cli asked %d times, want %d: %q", len(got), tc.asked, got)
				}
				if tc.cards > 0 && !strings.Contains(fh.interactions()[0].Summary, "（lark-cli 没给出风险等级）") {
					t.Fatalf("card summary = %q", fh.interactions()[0].Summary)
				}
			})
		}
	}
}
