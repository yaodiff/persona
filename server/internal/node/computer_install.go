package node

// Installing Cua Driver and asking for its permissions from the app (computer.install, computer.grant, computer.cancel;
// docs/extensions/computer-use.md "One-tap installation"): one background job at a time, whose state comes back with computer.status.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// cuaInstallBase is where Cua publishes its installer (tests point it elsewhere). install.sh hands over to
// _install-rust.sh, which sources _install-common.sh: all three are fetched first and run from disk, so nothing else
// is fetched as code while it runs (the release archive is, and the installer checks it against the release's
// SHA256SUMS).
var cuaInstallBase = "https://cua.ai/driver/"

var cuaScripts = []string{"install.sh", "_install-rust.sh", "_install-common.sh"}

// cuaScriptMost: the installer is about 1,800 lines; anything much bigger is not it.
const cuaScriptMost = 2 << 20

// installOS is the OS the node installs on (tests say darwin). installArch is its architecture (tests say arm64).
var (
	installOS   = runtime.GOOS
	installArch = runtime.GOARCH
)

var jobTimeout = map[string]time.Duration{"install": 15 * time.Minute, "grant": 10 * time.Minute, "open": time.Minute}

// jobVerbs say a job's kind in Chinese, for "…in progress, wait for it to finish".
var jobVerbs = map[string]string{"install": "安装", "grant": "请求授权", "open": "打开"}

// jobSlot is one background job at a time (an install, a grant…): the running one, or the last. Cua Driver and ego
// lite each have their own (cuaJob, egoJob).
type jobSlot struct {
	mu     sync.Mutex
	job    *proto.ComputerJob
	cancel context.CancelFunc
}

// cuaJob is the node's install or grant of Cua Driver.
var cuaJob jobSlot

func (s *jobSlot) current() *proto.ComputerJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil {
		return nil
	}
	j := *s.job
	return &j
}

// start runs run in the background as the slot's job of kind; one at a time. then, when set, runs after a success.
func (s *jobSlot) start(kind string, run func(ctx context.Context, step func(string)) error, then func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.job; j != nil && j.State == "running" {
		return fmt.Errorf("正在%s，等它做完", jobVerbs[j.Kind])
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout[kind])
	job := &proto.ComputerJob{Kind: kind, State: "running", Started: time.Now().UTC()}
	s.job, s.cancel = job, cancel
	go func() {
		defer cancel()
		err := run(ctx, func(step string) {
			s.mu.Lock()
			job.Step = step
			s.mu.Unlock()
		})
		s.mu.Lock()
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			job.State, job.Step = "cancelled", ""
		case err != nil:
			job.State, job.Error = "failed", err.Error()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				job.Error = "超时（" + jobTimeout[kind].String() + "）"
			}
		default:
			job.State, job.Step = "done", ""
		}
		s.mu.Unlock()
		if err == nil && then != nil {
			then()
		}
	}()
	return nil
}

// cancelInstall stops a running install (a grant or an open waits on the Mac's own windows: closing those is the
// user's).
func (s *jobSlot) cancelInstall() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.job; j == nil || j.State != "running" || j.Kind != "install" {
		return false
	}
	s.cancel()
	return true
}

func currentJob() *proto.ComputerJob { return cuaJob.current() }

// startJob runs a Cua Driver job; an install that worked asks for the permissions straight away.
func startJob(kind string, run func(ctx context.Context, step func(string)) error) error {
	var then func()
	if kind == "install" {
		then = func() { _ = startJob("grant", grantCua) }
	}
	return cuaJob.start(kind, run, then)
}

func cancelJob() bool { return cuaJob.cancelInstall() }

// installCua downloads Cua's installer, checks it, and runs it: into /Applications and ~/.local/bin, the shell's rc
// files left alone, with its usage statistics off (also for the install itself).
func installCua(ctx context.Context, step func(string)) error {
	if installOS != "darwin" {
		return errors.New("只能装在 Mac 上")
	}
	if cuaBin() != "" {
		return errors.New("已经装好了")
	}
	step("下载安装器")
	dir, err := os.MkdirTemp("", "persona-cua-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, name := range cuaScripts {
		data, err := fetchScript(ctx, cuaInstallBase+name)
		if err != nil {
			return fmt.Errorf("下载 %s：%w", name, err)
		}
		if err := checkScript(name, data); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o700); err != nil {
			return err
		}
	}
	step("下载并校验 Cua Driver，装进「应用程序」")
	args := []string{filepath.Join(dir, "install.sh"), "--no-modify-path"}
	if _, err := exec.LookPath("cosign"); err == nil {
		args = append(args, "--require-signature") // the release's Sigstore bundle too
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CUA_DRIVER_RS_TELEMETRY_ENABLED=0", "CUA_DRIVER_NO_MODIFY_PATH=1")
	killGroupOnCancel(cmd)
	var out tailBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("安装器失败（%v）：%s", err, oneLine(out.String(), 4))
	}
	bin := cuaBin()
	if bin == "" {
		return fmt.Errorf("装完找不到 cua-driver：%s", oneLine(out.String(), 2))
	}
	step("关闭 Cua 的使用统计")
	if o, err := cuaRun(ctx, bin, "telemetry", "disable"); err != nil {
		return fmt.Errorf("装好了，但没能关闭使用统计：%s", firstLine(o))
	}
	return nil
}

// checkScript: what is fetched is Cua's installer, as far as can be told before running it. The two it runs are bash
// scripts, the one they source a shell file, and the one that downloads the release still checks it against the
// release's SHA256SUMS.
func checkScript(name string, data []byte) error {
	bash := bytes.HasPrefix(data, []byte("#!/bin/bash")) || bytes.HasPrefix(data, []byte("#!/usr/bin/env bash"))
	if name == "_install-common.sh" {
		bash = bytes.HasPrefix(data, []byte("#")) && !bytes.HasPrefix(bytes.TrimSpace(data), []byte("<"))
	}
	if !bash {
		return fmt.Errorf("%s 不像 Cua 的安装脚本", name)
	}
	if name == "_install-rust.sh" && !bytes.Contains(data, []byte("SHA256SUMS")) {
		return errors.New("安装器不再对照 SHA256SUMS 校验发布包，没有运行")
	}
	return nil
}

// grantCua asks macOS for Cua Driver's two permissions: `cua-driver permissions grant` opens CuaDriver.app, whose
// dialogs are answered on the Mac's own screen (or over its live screen, #1184).
func grantCua(ctx context.Context, step func(string)) error {
	bin := cuaBin()
	if bin == "" {
		return errors.New("这台 Mac 上没有装 Cua Driver")
	}
	step("等你在那台 Mac 上点「允许」")
	out, err := exec.CommandContext(ctx, bin, "permissions", "grant").CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("没能请求授权：%s", firstLine(strings.TrimSpace(string(out))))
	}
	return nil
}

func fetchScript(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, cuaScriptMost+1))
	if err != nil {
		return nil, err
	}
	if len(data) > cuaScriptMost {
		return nil, errors.New("太大")
	}
	return data, nil
}

// oneLine is the last n lines of a process's output on one line, for the app.
func oneLine(out string, n int) string {
	return strings.ReplaceAll(lastLines(strings.TrimSpace(out), n), "\n", " / ")
}
