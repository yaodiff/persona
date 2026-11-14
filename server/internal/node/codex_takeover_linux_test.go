//go:build linux

package node

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCodexTakeoverUsesOnlyTheSelectedWritersTerminalBridge(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same incarnation", true: "changed incarnation"}[changed], func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("ORCA_USER_DATA_PATH", dir)
			marker := filepath.Join(dir, "input")
			counter := filepath.Join(dir, "counter")
			cli := filepath.Join(dir, "terminal-cli")
			script := `#!/bin/sh
set -eu
if [ "$2" = show ]; then
  incarnation=first
  if [ -f "$COUNTER" ] && [ "$CHANGE" = true ]; then incarnation=second; fi
  touch "$COUNTER"
  printf '{"ok":true,"result":{"terminal":{"handle":"test-handle","incarnationId":"%s","tabId":"test-tab","leafId":"test-leaf","writable":true}}}\n' "$incarnation"
elif [ "$2" = read ]; then
  printf '{"ok":true,"result":{"terminal":{"tail":["\\u203a Ask Codex to do anything","","  gpt-6 low \\u00b7 /work/example","  ? for shortcuts"]}}}\n'
else
  test "$1" = terminal && test "$2" = send
  test "$3" = --terminal && test "$4" = test-handle && test "$5" = --text
  printf '%s' "$6" > "$MARKER"
fi
`
			if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			env := append(withoutEnv(os.Environ(), []string{"ORCA_CLI_COMMAND", "ORCA_TERMINAL_HANDLE", orcaPaneEnv}),
				"ORCA_CLI_COMMAND="+cli, "ORCA_TERMINAL_HANDLE=test-handle", orcaPaneEnv+"=test-tab:test-leaf",
				"MARKER="+marker, "COUNTER="+counter, "CHANGE="+map[bool]string{true: "true", false: "false"}[changed])
			writer := startTakeoverWriter(t, env)
			// The node's own terminal must never be used as a fallback.
			t.Setenv("ORCA_CLI_COMMAND", "/does-not-exist")
			t.Setenv("ORCA_TERMINAL_HANDLE", "unrelated")
			send, err := codexQuitTerminal(context.Background(), writer, "test-tab:test-leaf")
			if err != nil {
				t.Fatal(err)
			}
			err = send()
			got, readErr := os.ReadFile(marker)
			if changed {
				if refusalCode(err) != "codex_takeover_changed" || readErr == nil {
					t.Fatalf("changed terminal received input: err=%v bytes=%q", err, got)
				}
			} else if err != nil || readErr != nil || !slices.Equal(got, []byte{4}) {
				t.Fatalf("expected one shutdown key, not a signal/prompt: err=%v read=%v bytes=%q", err, readErr, got)
			}
		})
	}
}

func TestCodexTakeoverNeverQuitsAnUnknownViewOrDraft(t *testing.T) {
	for _, text := range []string{
		"",
		"ordinary shell $",
		"\u203a Keep this unsent draft\n  ? for shortcuts",
		"Approval needed\n  Enter to confirm",
	} {
		if codexQuitComposer(text) {
			t.Errorf("unsafe composer accepted: %q", text)
		}
	}
	if !codexQuitComposer("\u203a Ask Codex to do anything\n\n  gpt-6 low \u00b7 /work/example\n  ? for shortcuts") {
		t.Fatal("empty Codex composer not recognized")
	}
	footer := "\n  GPT-6.1 Sol max \u00b7 Context 99% left \u00b7 475K window\n  \u2190 for agents \u00b7 ? for shortcuts"
	if !codexQuitComposer("\u203a Ask Codex to do anything" + footer) {
		t.Fatal("context-status footer was mistaken for a draft")
	}
	if codexQuitComposer("\u203a Keep this draft" + footer) {
		t.Fatal("context-status footer erased a real draft")
	}
}

func TestCodexTakeoverRejectsBridgeForADifferentPane(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("ORCA_USER_DATA_PATH", dir)
	w := startTakeoverWriter(t, append(withoutEnv(os.Environ(), []string{orcaPaneEnv}), orcaPaneEnv+"=other:pane"))
	_, err := codexQuitTerminal(context.Background(), w, "expected:pane")
	if err == nil || !strings.Contains(refusalCode(err), "codex_takeover_") {
		t.Fatalf("an unrelated terminal was accepted: %v", err)
	}
}

// startTakeoverWriter starts a stand-in Codex writer with env and returns once it runs its own code.
// exec.Cmd.Start returns as soon as the new program's close-on-exec has run, before the kernel has laid out that
// program's environment, so /proc/<pid>/environ can still read empty and the takeover rightly refuses a writer it
// cannot verify (codex_takeover_unavailable). A real writer has long been running when it is taken over. read is a
// shell builtin blocked on the open stdin pipe: the shell never execs again and ends when the test closes that pipe.
func startTakeoverWriter(t *testing.T, env []string) codexWriter {
	t.Helper()
	child := exec.Command("/bin/sh", "-c", "echo ready; read -r line")
	child.Env = env
	if _, err := child.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatalf("stand-in writer did not start: %v", err)
	}
	return codexWriter{child.Process.Pid, procStartMark(child.Process.Pid)}
}
