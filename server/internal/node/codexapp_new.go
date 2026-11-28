package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/proto"
)

// A new chat in the Codex App, started from the phone (docs/integrations/codex.md "New chats in the Codex App", #5726). The
// App takes no chat with a first message from outside: codex://new only fills its composer, and its codex_app MCP
// channel takes only the App's own processes. It does list any chat its Codex home has from an interactive source
// (vscode among them: what an app-server's threads are), opens one at codex://threads/<id>, and runs a turn in one it
// has loaded when a follower asks (thread-follower-start-turn, codex_desktop.go). So a start with codexAppLauncher:
//
//  1. starts the chat with the App's own codex (its bundled CLI, as `codex app-server` over stdio, client
//     codexAppClient): thread/start in the folder, then thread/name/set with the title, which writes the chat (Codex
//     writes one at its first turn otherwise); no model request; the app-server exits;
//  2. sends the first message as to any chat of the App (sendCodexDesktop): the App opens it (in front), and runs the
//     turn. Until then the App does not list it: a chat with no message is not in its sidebar.
//
// The chat is in the App's sidebar in the project its folder is in, or among the chats in no project, and the App runs
// it with its own settings. Persona lists it as one of the App's chats by its originator (codexOrigin.persona).

// codexAppLauncher is the launcher a computer whose Codex App runs offers (launchers.list), codexAppSource its Source;
// codexAppClient is the client name the chats are started with, which Codex writes as their originator.
const (
	codexAppLauncher = "codex-app"
	codexAppSource   = "codex_app"
	codexAppClient   = "persona_codex_app"
)

// codexAppModelLocked says why New Agent's model does not apply to the launcher; codexAppCheckMsg why Settings ›
// Models' Test has nothing to run for it.
const (
	codexAppModelLocked = "Codex App 用它自己选的模型运行这个对话。"
	codexAppCheckMsg    = "这个启动器在 Codex App 里新开对话，用的是 App 自己的设置，没有可以单独测试的命令。"
)

var (
	// codexAppSeedWait bounds the App's codex starting the chat.
	codexAppSeedWait = 30 * time.Second

	// codexAppCLI is the Codex CLI the App bundles (the same version as the App's own app-server), else one on PATH or
	// where Codex installs (a variable: tests use their own).
	codexAppCLI = func() (string, error) {
		if runtime.GOOS == "windows" {
			if p := bundledCodexAppCLI(); p != "" {
				return p, nil
			}
		}
		if p := bundledCodexCLI(); p != "" {
			return p, nil
		}
		return agentExecutable("codex")
	}

	// bundledCodexCLI is the Codex CLI the ChatGPT app bundles on a Mac, "" when there is none (a variable: tests use
	// their own). Its base name is codex, so herdr, codex-start and codexEffortInherited take it for plain codex.
	bundledCodexCLI = func() string {
		if runtime.GOOS != "darwin" {
			return ""
		}
		p := "/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex"
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode().Perm()&0111 != 0 {
			return p
		}
		return ""
	}

	// seedCodexAppChat starts a chat named title in dir with codex in home, and returns its thread id (a variable:
	// tests start their own).
	seedCodexAppChat          = startCodexAppChat
	seedCodexAppChatInProject = startCodexAppChatInProject
)

// isCodexAppLauncher: name is the Codex App's launcher, not a login shell's of that name.
func isCodexAppLauncher(name string) bool {
	return name == codexAppLauncher && !shellLauncherNames()[name]
}

// withCodexAppLauncher is list with the Codex App's launcher after it, on a computer whose App runs for this node's
// Codex home (its bus accepts connections) and where a Codex CLI to start chats with is found; added reports whether it was.
// A launcher of the login shell's with its name is left as it is: that one runs.
func (n *Node) withCodexAppLauncher(list []proto.Launcher) (out []proto.Launcher, added bool) {
	if n.sessionScope != nil || !codexDesktopAvailable(n.codexHome()) || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == codexAppLauncher }) {
		return list, false
	}
	if _, err := codexAppCLI(); err != nil {
		return list, false
	}
	return append(slices.Clone(list), proto.Launcher{Name: codexAppLauncher, Source: codexAppSource, Agent: "codex", DisplayName: "Codex App"}), true
}

// startInCodexApp starts a new chat in the Codex App for a start with its launcher; handled false for any other start
// (and a resume: a chat of the App is continued by a message, codex_desktop.go).
func (n *Node) startInCodexApp(ctx context.Context, p runtimeStartParams, nameHint string) (s *proto.Session, err error, handled bool) {
	if !isCodexAppLauncher(p.Launcher) || p.Resume != "" {
		return nil, nil, false
	}
	s, err = n.newCodexAppChat(ctx, p, nameHint)
	return s, err, true
}

func (n *Node) newCodexAppChat(ctx context.Context, p runtimeStartParams, nameHint string) (*proto.Session, error) {
	if n.sessionScope != nil {
		return nil, refusal("desktop_scope_unavailable", "Desktop sessions are unavailable in a scoped node.", nil)
	}
	home := n.codexHome()
	projectID, err := parseCodexDesktopProjectID(home, p.DesktopProjectID)
	if err != nil {
		return nil, err
	}
	if !codexDesktopAvailable(home) {
		return nil, codexDesktopUnavailable(ctx)
	}
	if p.PermissionMode == "plan" {
		// The App runs the chat in its own mode, and a plan waiting for approval there could not be answered here.
		return nil, readonlyStartUnsupported()
	}
	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		// The App lists a chat once it has a message: one with none would be nowhere to be seen.
		return nil, refusal("codex_needs_prompt", "在 Codex App 里新开对话需要第一条消息，这次没有发送。", nil)
	}
	model, effort := strings.TrimSpace(p.Model), strings.TrimSpace(p.Effort)
	if (model != "" || effort != "") && !codexDesktopBusAvailable(home) {
		return nil, codexDesktopUnavailable(ctx)
	}
	if model != "" && !proto.ValidModelID(model) {
		return nil, fmt.Errorf("invalid model %q", model)
	}
	cli, err := codexAppCLI()
	if err != nil {
		return nil, fmt.Errorf("没找到 Codex（codex 命令），不能在 Codex App 里新开对话：%v", err)
	}
	title := desktopTitle(cmp.Or(prompt, nameHint), "")
	dir, err := n.codexAppStartDir(p.Cwd, cmp.Or(prompt, nameHint))
	if err != nil {
		return nil, err
	}
	backend := backendLaunchFrom(ctx)
	if backend != nil {
		if err := backend.beforeDispatch(""); err != nil {
			return nil, err
		}
	}
	var thread string
	if projectID == "" {
		thread, err = seedCodexAppChat(ctx, cli, home, dir, title)
	} else {
		thread, err = seedCodexAppChatInProject(ctx, cli, home, dir, title, projectID)
	}
	if err != nil && !validID(thread) {
		if maybeDelivered(err) {
			return nil, err
		}
		return nil, fmt.Errorf("Codex 没能新开对话：%v", err)
	}
	if !validID(thread) {
		return nil, uncertain(errors.New("Codex returned no valid identifier for the new desktop chat"))
	}
	s := &proto.Session{SessionID: n.codexPublicID(home, thread), Cwd: dir, Title: title, Agent: "codex",
		Launcher: codexAppLauncher, Status: proto.StatusIdle, StartedByPersona: true, UpdatedAt: time.Now().UTC()}
	n.state.put(s.SessionID, sessionState{Launcher: codexAppLauncher, Cwd: dir, StartedAt: s.UpdatedAt,
		DesktopPendingModel: model, DesktopPendingEffort: effort})
	if backend != nil {
		if receiptErr := backend.executor.recordLaunchSession(s.SessionID); receiptErr != nil {
			return nil, receiptErr
		}
	}
	if err != nil {
		return s, desktopCreatedError(s, err, false)
	}
	log.Printf("new Codex App chat %s in %s", thread, dir)
	publicID := n.codexPublicID(home, thread)
	src := codexSource{home: home, thread: thread, path: rolloutPath(home, thread)}
	if err := n.finishCreatedCodexAppSettings(ctx, publicID, src); err != nil {
		log.Printf("new Codex App chat %s: initial settings (%q, %q) did not take: %v", thread, model, effort, err)
		return s, desktopCreatedError(s, err, false)
	}
	s.Model, s.Effort = model, effort
	if pick := n.codexAppPickOf(publicID); pick != nil {
		s.Model, s.Effort = cmp.Or(pick.model, s.Model), cmp.Or(pick.effort, s.Effort)
	}
	if err := n.sendCodexDesktop(ctx, src, p.Prompt, nil); err != nil {
		log.Printf("new Codex App chat %s: the first message did not go in: %v", thread, err)
		return s, desktopCreatedError(s, err, true)
	}
	n.notifyCodex()
	s.Status = proto.StatusWorking
	return s, nil
}

// A thread whose creation reached the desktop may have no rollout yet (for example, naming failed).
// Keep its first-message retry on the owning App even before the normal catalog can recognize it.
func (n *Node) sendCreatedCodexApp(ctx context.Context, id, text string, images []proto.Image) (bool, error) {
	if n.state == nil {
		return false, nil
	}
	if err := n.state.loaded(ctx); err != nil {
		return true, err
	}
	state, known := n.state.get(id)
	if !known || state.Launcher != codexAppLauncher {
		return false, nil
	}
	if n.sessionScope != nil || n.codexBlocked(id) {
		return true, refusal("codex_read_only", "The desktop session is outside this node's scope or has an identity conflict.", nil)
	}
	refs, err := assistantskills.References(text)
	if err != nil {
		return true, skillSelectionError(err)
	}
	if len(refs) > 0 {
		return true, skillSelectionError(assistantskills.ErrReference)
	}
	src, known := n.codexSource(id)
	if !known {
		return true, refusal("codex_read_only", "The desktop session's source is not available yet. Reopen it in Codex before sending.", nil)
	}
	if err := n.finishCreatedCodexAppSettings(ctx, id, src); err != nil {
		return true, err
	}
	return true, n.sendCodexDesktop(ctx, src, text, images)
}

// finishCreatedCodexAppSettings recovers initial setup on the original native thread. Requested values are kept
// separately from confirmed settings, so a failed readback never silently sends its first message on another model.
func (n *Node) finishCreatedCodexAppSettings(ctx context.Context, id string, src codexSource) error {
	state, _ := n.state.get(id)
	model, effort := state.DesktopPendingModel, state.DesktopPendingEffort
	if model == "" && effort == "" {
		return nil
	}
	if err := n.setCodexDesktopSettings(ctx, id, src, model, effort); err != nil {
		return err
	}
	n.state.update(id, func(cur *sessionState) bool {
		if cur.DesktopPendingModel != model || cur.DesktopPendingEffort != effort {
			return false
		}
		cur.DesktopPendingModel, cur.DesktopPendingEffort = "", ""
		return true
	})
	return nil
}

// codexAppStartDir is where a new chat of the App runs: dir as for the desktop app's (desktopStartDir: there, or a new
// folder in one that is); with none, a folder of its own where the App keeps a chat in no project,
// ~/Documents/Codex/<date>/<slug of its first message's first line>, made now.
func (n *Node) codexAppStartDir(dir, first string) (string, error) {
	if strings.TrimSpace(dir) != "" {
		return n.desktopStartDir(dir)
	}
	day := filepath.Join(n.home, "Documents", "Codex", time.Now().Format("2006-01-02"))
	line, _, _ := strings.Cut(strings.TrimSpace(first), "\n")
	base := codexAppSlug(line)
	for i := 1; ; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		p := filepath.Join(day, name)
		if err := os.MkdirAll(day, 0o755); err != nil {
			return "", fmt.Errorf("建不了文件夹 %s：%v", day, err)
		}
		switch err := os.Mkdir(p, 0o755); {
		case err == nil:
			return p, nil
		case errors.Is(err, os.ErrExist) && i < 100:
			continue // another chat's: one of its own
		default:
			return "", fmt.Errorf("建不了文件夹 %s：%v", p, err)
		}
	}
}

// codexAppSlug is a folder name from a line of text, as the App names one: its ASCII words, lower case, joined by "-",
// whole words up to 40 characters; "chat" when it has none.
func codexAppSlug(title string) string {
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	slug := ""
	for _, w := range words {
		next := w
		if slug != "" {
			next = slug + "-" + w
		}
		if len(next) > 40 {
			break
		}
		slug = next
	}
	return cmp.Or(slug, "chat")
}

// startCodexAppChat runs `codex app-server` (stdio, JSON lines) in dir for home, starts a thread there and names it
// title, which writes it, then ends the app-server. It never starts a turn.
func startCodexAppChat(ctx context.Context, cli, home, dir, title string) (string, error) {
	return startCodexAppChatInProject(ctx, cli, home, dir, title, "")
}

func startCodexAppChatInProject(ctx context.Context, cli, home, dir, title, projectID string) (string, error) {
	thread := ""
	err := withCodexAppServer(ctx, cli, home, dir, func(call codexAppCall) error {
		params := map[string]any{"cwd": dir}
		projects, err := listCodexDesktopProjects(call)
		if err != nil {
			var rpcErr *codexAppServerError
			if projectID != "" || !errors.As(err, &rpcErr) || rpcErr.Code != -32601 {
				return err
			}
		} else {
			project, err := selectCodexDesktopProject(projects, dir, projectID)
			if err != nil {
				return err
			}
			if project != "" {
				params["projectId"] = project
			}
		}
		var started struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := call("thread/start", params, &started); err != nil {
			var rejected *codexAppServerError
			if !errors.As(err, &rejected) {
				return uncertain(fmt.Errorf("the desktop may have created the chat before its response was lost: %w", err))
			}
			return err
		}
		if !validID(started.Thread.ID) {
			return errors.New("thread/start: no valid thread")
		}
		thread = started.Thread.ID
		return call("thread/name/set", map[string]any{"threadId": thread, "name": title}, nil)
	})
	return thread, err
}

// codexStdio is a short-lived, initialized app-server client used by both native metadata operations and model/list.
// Requests are sequential and bounded; it never resumes a desktop-owned thread or sends its model turns.
type codexStdio struct {
	ctx    context.Context
	cancel context.CancelFunc
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  *bufio.Scanner
	next   int
}

func startCodexStdio(ctx context.Context, cli, home, dir string) (*codexStdio, error) {
	return startCodexStdioWithEnv(ctx, cli, home, dir, nil)
}

func startCodexStdioWithEnv(ctx context.Context, cli, home, dir string, extraEnv []string) (*codexStdio, error) {
	ctx, cancel := context.WithTimeout(ctx, codexAppSeedWait)
	baseEnv := desktopSeedEnv(os.Environ())
	if len(extraEnv) > 0 {
		var clear []string
		for _, kv := range extraEnv {
			if k, _, ok := strings.Cut(kv, "="); ok && k != "" {
				clear = append(clear, k)
			}
		}
		baseEnv = append(withoutEnv(baseEnv, clear), extraEnv...)
	}
	program, env, err := codexStartProgram(cli, true, append(baseEnv, "CODEX_HOME="+home))
	if err != nil {
		cancel()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, program, "app-server")
	cmd.Dir, cmd.Env = dir, env
	if bin := codexAppNodeBin(cli); bin != "" {
		path := withDirsAfter(envValue(cmd.Env, "PATH"), []string{bin})
		cmd.Env = append(withoutEnv(cmd.Env, []string{"PATH"}), "PATH="+path)
	}
	killGroupOnCancel(cmd)
	prepareDesktopChild(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = in.Close()
		return nil, err
	}
	stopGroup := cmd.Cancel
	cmd.Cancel = sync.OnceValue(func() error {
		// A CLI launcher can fork a child that keeps both pipes open. Close our ends even if the group is gone.
		_ = in.Close()
		_ = out.Close()
		if err := stopGroup(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	})
	if err := cmd.Start(); err != nil {
		cancel()
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	lines := bufio.NewScanner(out)
	lines.Buffer(make([]byte, 64<<10), 64<<20)
	s := &codexStdio{ctx: ctx, cancel: cancel, cmd: cmd, in: in, lines: lines}
	if err := s.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": codexAppClient, "title": "Persona", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true}, // project/* requires this capability
	}, nil); err != nil {
		s.close()
		return nil, err
	}
	if _, err := in.Write([]byte("{\"method\":\"initialized\"}\n")); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// codexAppNodeBin is the interpreter directory for the official npm launcher when the service has no node on PATH.
// npm links bin/codex to the script, which uses /usr/bin/env node rather than an absolute interpreter path.
func codexAppNodeBin(cli string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if _, err := exec.LookPath("node"); err == nil {
		return ""
	}
	real, err := filepath.EvalSymlinks(cli)
	if err != nil {
		return ""
	}
	prefix, ok := strings.CutSuffix(real, "/lib/node_modules/@openai/codex/bin/codex.js")
	if !ok || !filepath.IsAbs(prefix) {
		return ""
	}
	bin := filepath.Join(prefix, "bin")
	if _, err := exec.LookPath(filepath.Join(bin, "node")); err != nil {
		return ""
	}
	return bin
}

func (s *codexStdio) close() {
	s.cancel()
	_ = s.cmd.Cancel()
	_ = s.cmd.Wait()
}

// call skips notifications and other responses until this request's answer. Canceling a call ends the short exchange.
func (s *codexStdio) call(ctx context.Context, method string, params, into any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	s.next++
	id := s.next
	b, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := s.in.Write(append(b, '\n')); err != nil {
		return err
	}
	for s.lines.Scan() {
		var m struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(s.lines.Bytes(), &m) != nil || m.ID == nil || *m.ID != id {
			continue
		}
		if m.Error != nil {
			return &codexAppServerError{Method: method, Code: m.Error.Code, Message: m.Error.Message}
		}
		if into != nil {
			if err := json.Unmarshal(m.Result, into); err != nil {
				return fmt.Errorf("%s: invalid response: %w", method, err)
			}
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := s.lines.Err(); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	return fmt.Errorf("%s: app-server closed before answering", method)
}
