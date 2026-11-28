package larkcli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

func writeHardeningScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-cli.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

// A subprocess that spawns a grandchild holding stdout open must not hang Run() past timeout,
// and the grandchild process group must be terminated cleanly.
func TestExecuteKillsProcessGroupAndDoesNotHangOnOrphanChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process-group test")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	bin := writeHardeningScript(t, `
sleep 30 &
echo $! > "`+pidFile+`"
wait
`)
	r := New(Config{Bin: bin, Timeout: 150 * time.Millisecond})
	start := time.Now()
	_, err := r.Run(context.Background(), "im", "+chat-list")
	elapsed := time.Since(start)
	if KindOf(err) != KindTimeout {
		t.Fatalf("err = %v (kind %q), want timeout", err, KindOf(err))
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run hung for %s waiting for grandchild holding stdout", elapsed)
	}
	if raw, err := os.ReadFile(pidFile); err == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
			if osproc.AliveMine(pid) {
				_ = osproc.Kill(pid)
				t.Fatalf("grandchild pid %d survived Run timeout", pid)
			}
		}
	}
}

// If the leader exits cleanly while a background grandchild still holds stdout open,
// Run must not hang past WaitDelay and must clean up the orphaned grandchild.
func TestExecuteWaitDelayCleansOrphanGrandchild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process-group test")
	}
	pidFile := filepath.Join(t.TempDir(), "orphan.pid")
	bin := writeHardeningScript(t, `
sleep 30 &
echo $! > "`+pidFile+`"
printf '{"ok":true}\n'
exit 0
`)
	r := New(Config{Bin: bin, Timeout: 5 * time.Second})
	out, err := r.Run(context.Background(), "im", "+chat-list")
	if err != nil {
		t.Fatalf("Run returned error on clean exit with orphan grandchild: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"ok":true}` {
		t.Fatalf("out = %q, want {\"ok\":true}", string(out))
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read orphan pid: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil || pid <= 0 {
		t.Fatalf("invalid orphan pid %q: %v", string(raw), convErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for osproc.AliveMine(pid) && time.Now().Before(deadline) {
		<-time.After(10 * time.Millisecond)
	}
	if osproc.AliveMine(pid) {
		_ = osproc.Kill(pid)
		t.Fatalf("orphan grandchild pid %d survived ErrWaitDelay cleanup", pid)
	}
}

// Runaway stdout must be capped in memory without deadlocking the child and must fail closed.
func TestExecuteCapsStdoutMemoryAndFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	oldStdout, oldStderr := maxCommandStdout, maxCommandStderr
	maxCommandStdout, maxCommandStderr = 1024, 512
	defer func() { maxCommandStdout, maxCommandStderr = oldStdout, oldStderr }()

	bin := writeHardeningScript(t, `
dd if=/dev/zero bs=4096 count=8 2>/dev/null | tr '\0' 'a'
dd if=/dev/zero bs=2048 count=2 2>/dev/null | tr '\0' 'e' >&2
`)
	r := New(Config{Bin: bin, Timeout: 5 * time.Second})
	out, err := r.Run(context.Background(), "im", "+chat-list")
	if err == nil || len(out) != 0 {
		t.Fatalf("expected error on oversized stdout, got out=%d bytes err=%v", len(out), err)
	}
	if KindOf(err) != KindOther {
		t.Fatalf("kind = %q, want %q (err=%v)", KindOf(err), KindOther, err)
	}
}

func TestCappedBufferBoundariesAndConcurrency(t *testing.T) {
	b := &cappedBuffer{limit: 4}
	if n, err := b.Write([]byte("abcd")); err != nil || n != 4 {
		t.Fatalf("Write exact limit = (%d, %v)", n, err)
	}
	if got, trunc := b.bytes(); string(got) != "abcd" || trunc {
		t.Fatalf("exact limit = (%q, %v), want (\"abcd\", false)", got, trunc)
	}
	// A zero-byte write when full must not mark truncated.
	if n, err := b.Write(nil); err != nil || n != 0 {
		t.Fatalf("Write(nil) = (%d, %v)", n, err)
	}
	if _, trunc := b.bytes(); trunc {
		t.Fatal("zero-byte write at full capacity marked truncated")
	}
	// Writing one byte past limit marks truncated while preserving first limit bytes.
	if n, err := b.Write([]byte("e")); err != nil || n != 1 {
		t.Fatalf("Write(+1) = (%d, %v)", n, err)
	}
	if got, trunc := b.bytes(); string(got) != "abcd" || !trunc {
		t.Fatalf("limit+1 = (%q, %v), want (\"abcd\", true)", got, trunc)
	}

	// Zero limit ignores empty writes and truncates non-empty writes.
	z := &cappedBuffer{limit: 0}
	_, _ = z.Write(nil)
	if _, trunc := z.bytes(); trunc {
		t.Fatal("zero-limit buffer marked truncated on empty write")
	}
	_, _ = z.Write([]byte("x"))
	if got, trunc := z.bytes(); len(got) != 0 || !trunc {
		t.Fatalf("zero-limit non-empty = (%q, %v), want (\"\", true)", got, trunc)
	}

	// Concurrent writers and readers must stay race-free and bounded.
	cb := &cappedBuffer{limit: 64}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				_, _ = cb.Write([]byte("0123456789"))
				_, _ = cb.bytes()
			}
		}()
	}
	wg.Wait()
	if got, trunc := cb.bytes(); len(got) != 64 || !trunc {
		t.Fatalf("concurrent cappedBuffer = (len=%d, trunc=%v), want (64, true)", len(got), trunc)
	}
}

func TestLoginLineWriterEdgeCases(t *testing.T) {
	newProc := func() (*LoginProcess, *loginLineWriter) {
		p := &LoginProcess{ready: make(chan struct{}), done: make(chan struct{})}
		return p, &loginLineWriter{p: p}
	}

	t.Run("split URL and split CRLF across writes", func(t *testing.T) {
		p, w := newProc()
		_, _ = w.Write([]byte("Open https://accounts.feishu.cn/device?user_"))
		_, _ = w.Write([]byte("code=SPLIT-1\r"))
		_, _ = w.Write([]byte("\n"))
		if got := p.Info(); got.UserCode != "SPLIT-1" {
			t.Fatalf("info = %+v, want SPLIT-1", got)
		}
	})

	t.Run("exact 64KiB line accepted", func(t *testing.T) {
		p, w := newProc()
		suffix := " https://accounts.feishu.cn/device?user_code=EXACT-64K"
		line := strings.Repeat("a", loginLineLimit-len(suffix)) + suffix
		_, _ = w.Write([]byte(line + "\n"))
		if got := p.Info(); got.UserCode != "EXACT-64K" {
			t.Fatalf("info = %+v, want EXACT-64K", got)
		}
	})

	t.Run("64KiB+1 line skipped without merging next line", func(t *testing.T) {
		p, w := newProc()
		_, _ = w.Write(bytes.Repeat([]byte("a"), loginLineLimit+1))
		_, _ = w.Write([]byte("\nhttps://accounts.feishu.cn/device?user_code=AFTER-BIG\n"))
		if got := p.Info(); got.UserCode != "AFTER-BIG" {
			t.Fatalf("info = %+v, want AFTER-BIG", got)
		}
	})

	t.Run("mid-line truncation at 1MiB scan cap discards partial URL and frees buffer", func(t *testing.T) {
		p, w := newProc()
		// Fill just shy of loginScanLimit so the URL is cut in half across the cap.
		prefixURL := "https://accounts.feishu.cn/device?user_code=HALF-CUT"
		cutAt := len(prefixURL) - 4 // leaves "HALF" before cap, "-CUT" after cap
		padLen := loginScanLimit - cutAt
		for padLen > 0 {
			chunk := min(padLen, 4096)
			buf := bytes.Repeat([]byte("z"), chunk)
			buf[len(buf)-1] = '\n'
			_, _ = w.Write(buf)
			padLen -= chunk
		}
		_, _ = w.Write([]byte(prefixURL + "\n"))
		w.Flush()
		if got := p.Info(); got.URL != "" || got.UserCode != "" {
			t.Fatalf("partial URL across 1MiB cap was parsed: %+v", got)
		}
		if w.buf != nil {
			t.Fatal("loginLineWriter retained line buffer after reaching scan limit")
		}
	})

	t.Run("unterminated final line parsed on Flush", func(t *testing.T) {
		p, w := newProc()
		_, _ = w.Write([]byte("https://accounts.feishu.cn/device?user_code=NO-NEWLINE"))
		if got := p.Info(); got.UserCode != "" {
			t.Fatalf("parsed before newline or Flush: %+v", got)
		}
		w.Flush()
		if got := p.Info(); got.UserCode != "NO-NEWLINE" {
			t.Fatalf("Flush did not parse unterminated line: %+v", got)
		}
	})
}

// StartLogin must honor caller context cancellation promptly and terminate the spawned login process.
func TestStartLoginHonorsCallerContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	pidFile := filepath.Join(t.TempDir(), "login.pid")
	started := filepath.Join(t.TempDir(), "started")
	bin := writeHardeningScript(t, `
echo $$ > "`+pidFile+`"
touch "`+started+`"
sleep 30
`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(started); err == nil {
				cancel()
				return
			}
			<-time.After(10 * time.Millisecond)
		}
		cancel()
	}()
	start := time.Now()
	p, err := New(Config{Bin: bin, Timeout: 5 * time.Second}).StartLogin(ctx)
	elapsed := time.Since(start)
	if p != nil || KindOf(err) != KindTimeout || !errors.Is(err, context.Canceled) {
		t.Fatalf("StartLogin = (%v, %v), want canceled timeout error", p, err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("StartLogin ignored caller cancel and waited %s", elapsed)
	}
	if raw, err := os.ReadFile(pidFile); err == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
			if osproc.AliveMine(pid) {
				_ = osproc.Kill(pid)
				t.Fatalf("login process %d survived caller context cancellation", pid)
			}
		}
	}
}

// StartLogin must prioritize p.ready over p.done on fast-exiting CLI runs, time out cleanly
// when no URL arrives before loginReadyTimeout, and tolerate repeated Cancel calls.
func TestStartLoginFastExitReadyTimeoutAndIdempotentCancel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	fastBin := writeHardeningScript(t, `printf 'https://accounts.feishu.cn/device?user_code=FAST-1\n'; exit 0`)
	rFast := New(Config{Bin: fastBin, Timeout: 5 * time.Second})
	for i := range 5 {
		p, err := rFast.StartLogin(context.Background())
		if err != nil {
			t.Fatalf("iter %d: fast-exit StartLogin failed: %v", i, err)
		}
		if got := p.Info(); got.UserCode != "FAST-1" {
			t.Fatalf("iter %d: info = %+v, want FAST-1", i, got)
		}
		p.Cancel()
		p.Cancel()
		<-p.done
	}

	oldReady := loginReadyTimeout
	loginReadyTimeout = 120 * time.Millisecond
	defer func() { loginReadyTimeout = oldReady }()

	pidFile := filepath.Join(t.TempDir(), "hang.pid")
	hangBin := writeHardeningScript(t, `
echo $$ > "`+pidFile+`"
sleep 30
`)
	p, err := New(Config{Bin: hangBin, Timeout: 5 * time.Second}).StartLogin(context.Background())
	if p != nil || KindOf(err) != KindTimeout {
		t.Fatalf("StartLogin timeout = (%v, %v), want (nil, KindTimeout)", p, err)
	}
	if raw, readErr := os.ReadFile(pidFile); readErr == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); convErr == nil && pid > 0 {
			if osproc.AliveMine(pid) {
				_ = osproc.Kill(pid)
				t.Fatalf("hanging login process %d survived loginReadyTimeout", pid)
			}
		}
	}
}

// StartLogin must not deadlock when lark-cli emits a >64 KiB line or >1 MiB of output before exiting.
func TestStartLoginDrainsOversizedLinesAndOutputWithoutDeadlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script test")
	}
	bin := writeHardeningScript(t, `
dd if=/dev/zero bs=70000 count=1 2>/dev/null | tr '\0' 'x'
printf '\nhttps://accounts.feishu.cn/device?user_code=LONG-9999\n'
dd if=/dev/zero bs=65536 count=20 2>/dev/null | tr '\0' 'y'
`)
	p, err := New(Config{Bin: bin, Timeout: 5 * time.Second}).StartLogin(context.Background())
	if err != nil {
		t.Fatalf("StartLogin: %v", err)
	}
	if got := p.Info(); got.UserCode != "LONG-9999" {
		p.Cancel()
		t.Fatalf("info = %+v, want LONG-9999", got)
	}
	select {
	case <-p.done:
		if done, err := p.Poll(); !done || err != nil {
			t.Fatalf("Poll = (%v, %v), want (true, nil)", done, err)
		}
	case <-time.After(5 * time.Second):
		p.Cancel()
		t.Fatal("StartLogin deadlocked on >1 MiB output")
	}
}

func TestParseLoginInfoRejectsPhishingAndMalformedHosts(t *testing.T) {
	for _, bad := range []string{
		"Open https://accounts.feishu.cn@evil.invalid/device?user_code=PHISH-1",
		"Open https://:443/device?user_code=EMPTY-HOST",
		`Open https://accounts.feishu.cn\evil.invalid/device?user_code=BACKSLASH`,
		"Open https://accounts.feishu.cn/device?user_code=CTRL\x01-1",
	} {
		if info, ok := parseLoginInfo(bad); ok {
			t.Errorf("parseLoginInfo(%q) accepted: %+v", bad, info)
		}
	}
	if info, ok := parseLoginInfo("Open https://accounts.feishu.cn/device?user_code=GOOD-2"); !ok || info.UserCode != "GOOD-2" {
		t.Fatalf("parseLoginInfo rejected valid URL: (%+v, %v)", info, ok)
	}
}

func TestShellQuoteSpecialChars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell quoting test")
	}
	words := []string{"", "plain", "it's", `back\slash`, `\\`, `\'`, "a!b", "$HOME $(id) `id` ; & |"}
	for _, sh := range []string{"sh", "bash", "zsh"} {
		shellPath, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		for _, w := range words {
			out, err := exec.Command(shellPath, "-c", "printf '%s|' "+shellQuote(w)).CombinedOutput()
			if err != nil || string(out) != w+"|" {
				t.Errorf("%s: %q read back as %q (%v)", sh, w, out, err)
			}
		}
	}
}

func TestBinaryValidationAndCliEnvHardening(t *testing.T) {
	for _, bad := range []string{"lark-cli\nid", "lark-cli\x00x", "lark-cli\x1b[31m", "lark-cli\x7f"} {
		r := New(Config{Bin: bad})
		if _, err := r.binary(context.Background()); KindOf(err) != KindNotInstalled {
			t.Fatalf("binary(%q) = %v, want KindNotInstalled", bad, err)
		}
	}
	// Directory path must not be accepted as executable.
	if _, err := New(Config{Bin: t.TempDir()}).binary(context.Background()); KindOf(err) != KindNotInstalled {
		t.Fatalf("directory binary = %v, want KindNotInstalled", err)
	}

	if runtime.GOOS != "windows" {
		// Login-shell output that fails, returns a non-existent path, returns a directory,
		// or overflows the output cap must not poison r.path.
		dir := t.TempDir()
		realBin := filepath.Join(dir, "real-lark-cli")
		if err := os.WriteFile(realBin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", "/usr/bin:/bin")

		for _, tc := range []struct {
			name   string
			script string
			want   string
		}{
			{name: "nonzero exit", script: "echo " + realBin + "; exit 1", want: ""},
			{name: "nonexistent path", script: "echo " + filepath.Join(dir, "missing") + "; exit 0", want: ""},
			{name: "directory path", script: "echo " + dir + "; exit 0", want: ""},
			{name: "truncated output", script: "dd if=/dev/zero bs=70000 count=1 2>/dev/null | tr '\\0' 'x'; printf '\\n" + realBin + "\\n'; exit 0", want: ""},
			{name: "valid path", script: "echo " + realBin + "; exit 0", want: realBin},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fakeShell := writeHardeningScript(t, tc.script+"\n")
				t.Setenv("SHELL", fakeShell)
				r := New(Config{Bin: "missing-lark-cli-for-test", Timeout: 2 * time.Second})
				got, err := r.binary(context.Background())
				if tc.want == "" {
					if err == nil || got != "" {
						t.Fatalf("binary() = (%q, %v), want failure", got, err)
					}
					if r.path != "" {
						t.Fatalf("r.path poisoned with %q", r.path)
					}
				} else {
					if err != nil || got != tc.want {
						t.Fatalf("binary() = (%q, %v), want %q", got, err, tc.want)
					}
				}
			})
		}
	}

	// Empty PATH must not produce a trailing separator (POSIX current-directory lookup).
	t.Setenv("PATH", "")
	envEmpty := cliEnv("/usr/local/bin/lark-cli")
	pathEntries := []string{}
	for _, kv := range envEmpty {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			pathEntries = append(pathEntries, kv)
		}
	}
	if len(pathEntries) != 1 || pathEntries[0] != "PATH="+filepath.Dir("/usr/local/bin/lark-cli") {
		t.Fatalf("cliEnv with empty PATH = %q, want single PATH without trailing separator", pathEntries)
	}

	t.Setenv("PATH", "/usr/bin")
	envNonEmpty := cliEnv("/usr/local/bin/lark-cli")
	pathCount := 0
	for _, kv := range envNonEmpty {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			pathCount++
		}
	}
	if pathCount != 1 {
		t.Fatalf("cliEnv produced %d PATH entries, want 1", pathCount)
	}
}
