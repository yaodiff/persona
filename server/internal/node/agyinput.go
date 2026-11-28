package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/procinfo"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Commands waiting for input in Antigravity (agy) panes (#14527, docs/integrations/engines.md "Commands waiting for
// input"). agy runs a long command as a background task of its conversation. When that command stops at a prompt (a
// password, a pasted token, a sign-in code), nothing in the pane can answer it: what is typed there goes to agy's own
// input box, and agy holds every message while a non-daemon background task runs, so a message that carries the answer
// does not even start a turn until the task ends. The node reads the pane's conversation instead: a task whose output
// stops at a prompt is a command-input card in the node's card list (codexask.go), and the owner's line goes straight
// into the task through agy's language server, as agy's own manage_task send_input does, never through the model.
//
// What agy keeps of a conversation, under <agy data>/brain/<conversation>/.system_generated/:
//
//	tasks/task-N.log     the task's output as its pseudo-terminal wrote it, a prompt last and no newline after it
//	messages/<id>.json   messages to the conversation: "Task id \"<conversation>/task-N\" finished with result: ..."
//	                     from <conversation>/task-N when a task ends, a notice from "system" when a restart stopped them
//	logs/transcript.jsonl the step that started a task ("... background task with task id: <conversation>/task-N",
//	                     then "Task Description: <command>")

const (
	// agyInputQuiet: a prompt is asked once it has been the output's last line this long, so progress output that
	// passes through a line ending in ':' is never taken for one.
	agyInputQuiet = 2 * time.Second
	// agyInputMaxAge: task logs not written for longer are not looked at.
	agyInputMaxAge = 24 * time.Hour
	// agyInputTail is how much of a task log's end is read: the prompt and the lines before it.
	agyInputTail = 8 << 10
	// agyInputOutputLines: at most this many lines before the prompt go on the card (blank lines not counted: dropped).
	agyInputOutputLines = 20
	// agyInputMessageRead: a message file is read up to this many bytes (its sender and first words are at the start).
	agyInputMessageRead = 4 << 10
	// agyInputTranscriptTail: the transcript's end searched for a task's description.
	agyInputTranscriptTail = 2 << 20
	// agyInputStartKeep: how long a pane's agy start time is kept before it is read again.
	agyInputStartKeep = 30 * time.Second
	// agyInputSendWait bounds finding agy's language server and its answer to the line.
	agyInputSendWait = 10 * time.Second
	// agyInputMaxProcs: at most this many processes under a pane are looked at for agy's language server.
	agyInputMaxProcs = 256
)

// Codes of a refusal to type an answer; the manager says each in the answering app's language (fleet_ix_command.go).
const (
	codeCommandInputLine        = "command_input_line"        // the line cannot be typed (empty, a line break, too long)
	codeCommandInputUnreachable = "command_input_unreachable" // agy's language server could not be found or reached
	codeCommandInputRefused     = "command_input_refused"     // agy answered the line with an error: it was not typed
	codeCommandInputUnconfirmed = "command_input_unconfirmed" // the line went out and agy did not answer in time
)

// agyInputEnv names the variables agy puts in the environment of every command it runs: its language server's
// address and the CSRF token that server asks for (agy's built-in customization skill calls the server the same way).
const (
	agyLSAddressEnv = "ANTIGRAVITY_LS_ADDRESS"
	agyLSTokenEnv   = "ANTIGRAVITY_CSRF_TOKEN"
)

// agyInputs is what the node remembers between two readings of the agy panes' tasks.
type agyInputs struct {
	mu     sync.Mutex
	logs   map[string]agyLogSeen     // task log path → how it looked
	convs  map[string]*agyConvSeen   // conversation → the messages read so far
	starts map[string]agyPaneStarted // pane → when its agy started
	cards  map[string]agyInputCardAt // card id → the card as first made (CreatedAt and the title stay)
}

// agyLogSeen is a task log as the last reading saw it.
type agyLogSeen struct {
	size        int64
	mod         time.Time
	since, seen time.Time // since: when it first looked like this; seen: the last reading that looked
}

// agyConvSeen is what a conversation's messages say of its tasks.
type agyConvSeen struct {
	read    map[string]bool // message files read (they do not change once written)
	ended   map[string]bool // tasks with an end message
	restart time.Time       // the newest restart notice: no task started before it runs
	seen    time.Time
}

// agyPaneStarted is when a pane's agy started (zero when the system does not tell), and when that was read.
type agyPaneStarted struct {
	start, at time.Time
}

// agyInputCardAt is a card as first made, and the last reading that listed it.
type agyInputCardAt struct {
	ix   proto.Interaction
	seen time.Time
}

// agyInputPane is an agy pane whose conversation is known.
type agyInputPane struct {
	id, pane, cwd, conversation string
}

// agyWaitingTask is a task stopped at a prompt.
type agyWaitingTask struct {
	conversation, task string
	size               int64 // the log's size at the prompt: a later prompt in the same task is another card
	prompt, output     string
	secret             bool
}

// commandInputID is a waiting task's card id: its conversation, its task and where in its output the prompt is.
func commandInputID(w agyWaitingTask) string {
	return proto.CommandInputPrefix + shortHash(w.conversation+"\x00"+w.task+"\x00"+strconv.FormatInt(w.size, 10))
}

// commandInputCards are the cards of the commands waiting for input in agy panes, for the node's card list
// (syncCodexCards): one per task stopped at a prompt for agyInputQuiet. Cheap when nothing waits: a directory listing
// and a stat per recent task log.
func (n *Node) commandInputCards(ctx context.Context) []proto.Interaction {
	if runtime.GOOS == "windows" { // agy's environment cannot be read there, so no answer could reach a task
		return nil
	}
	now := time.Now()
	var cards []proto.Interaction
	for _, p := range n.agyInputPanes() {
		for _, w := range n.agyWaiting(ctx, p, now, true) {
			cards = append(cards, n.agyInputCard(p, w, now))
		}
	}
	n.cx.input.prune(now)
	return cards
}

// agyInputPanes are the live agy panes whose conversation the node knows, in id order.
func (n *Node) agyInputPanes() []agyInputPane {
	var rows []agyInputPane
	n.mu.Lock()
	for id, a := range n.agents {
		if a.Agent == agyAgent && a.PaneID != "" && a.AgentStatus != proto.StatusOffline && a.AgentStatus != proto.StatusInterrupted {
			rows = append(rows, agyInputPane{id: id, pane: a.PaneID, cwd: a.Cwd})
		}
	}
	n.mu.Unlock()
	out := rows[:0]
	for _, r := range rows {
		if r.conversation = n.agyConversation(r.id, r.pane); r.conversation != "" {
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b agyInputPane) int { return strings.Compare(a.id, b.id) })
	return out
}

// agyWaiting are the tasks of p's conversation stopped at a prompt: a log written in the last day, by the agy that
// runs the pane now, with no end message and no restart notice after it, whose output ends in a prompt (quiet: one that
// has not changed for agyInputQuiet). Without quiet (an answer checking its card), the prompt only has to be there.
func (n *Node) agyWaiting(ctx context.Context, p agyInputPane, now time.Time, quiet bool) []agyWaitingTask {
	dir := filepath.Join(n.agyRootFor(p.conversation), "brain", p.conversation, ".system_generated")
	entries, err := os.ReadDir(filepath.Join(dir, "tasks"))
	if err != nil {
		return nil
	}
	type candidate struct {
		task, path string
		info       os.FileInfo
	}
	var found []candidate
	for _, e := range entries {
		task, ok := strings.CutSuffix(e.Name(), ".log")
		if !ok || !agyTaskRe.MatchString(task) || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Size() == 0 || now.Sub(info.ModTime()) > agyInputMaxAge {
			continue
		}
		found = append(found, candidate{task, filepath.Join(dir, "tasks", e.Name()), info})
	}
	if len(found) == 0 {
		return nil
	}
	conv := n.cx.input.messages(p.conversation, filepath.Join(dir, "messages"), now)
	var started time.Time
	var out []agyWaitingTask
	for _, c := range found {
		if conv.ended[c.task] || !conv.restart.IsZero() && !c.info.ModTime().After(conv.restart) {
			continue
		}
		if quiet && !n.cx.input.quietFor(c.path, c.info, now) {
			continue
		}
		prompt, output, secret, ok := agyLogPrompt(c.path, c.info.Size())
		if !ok {
			continue
		}
		if started.IsZero() {
			started = n.agyPaneStart(ctx, p.pane, now)
		}
		// A task the pane's agy did not start is one an earlier agy left at its prompt: it died with that agy.
		if !started.IsZero() && c.info.ModTime().Before(started) {
			continue
		}
		out = append(out, agyWaitingTask{conversation: p.conversation, task: c.task, size: c.info.Size(),
			prompt: prompt, output: output, secret: secret})
	}
	return out
}

// agyTaskRe is a background task's name as agy gives it.
var agyTaskRe = lazyre.New(`^task-[0-9]+$`)

// agyLogPrompt reads the end of a task log of size bytes: the prompt its output stops at (the last line, no newline
// after it, proto.CommandPrompt), the lines before it (blank ones dropped, at most agyInputOutputLines and
// proto.CommandInputMaxOutput bytes), and whether the prompt reads as secret. Escapes and control characters are removed.
func agyLogPrompt(path string, size int64) (prompt, output string, secret, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false, false
	}
	defer f.Close()
	from := max(size-agyInputTail, 0)
	buf := make([]byte, size-from)
	if _, err := f.ReadAt(buf, from); err != nil && !errors.Is(err, io.EOF) {
		return "", "", false, false
	}
	text := textfmt.StripTerminalEscapes(string(buf))
	if from > 0 { // the first line was cut: not shown
		if _, rest, cut := strings.Cut(text, "\n"); cut {
			text = rest
		}
	}
	if strings.HasSuffix(text, "\n") {
		return "", "", false, false // a newline after the last line: the command is not at a prompt
	}
	before, last, _ := cutLast(text, "\n")
	if prompt, secret, ok = proto.CommandPrompt(last); !ok {
		return "", "", false, false
	}
	return prompt, agyLastLines(before, agyInputOutputLines, proto.CommandInputMaxOutput), secret, true
}

// cutLast is strings.Cut at sep's last occurrence: before is "" and after is s when there is none.
func cutLast(s, sep string) (before, after string, found bool) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):], true
	}
	return "", s, false
}

// agyLastLines is the end of s without its blank lines: at most lines lines and limit bytes, cut at a line. A card has
// little room, and a command's blank lines only space out what it says.
func agyLastLines(s string, lines, limit int) string {
	rows := slices.DeleteFunc(strings.Split(s, "\n"), func(r string) bool { return strings.TrimSpace(r) == "" })
	if len(rows) > lines {
		rows = rows[len(rows)-lines:]
	}
	for len(rows) > 0 && len(strings.Join(rows, "\n")) > limit {
		rows = rows[1:]
	}
	return strings.Join(rows, "\n")
}

// agyPaneStart is when the agy running pane started, zero when that cannot be told (kept agyInputStartKeep).
func (n *Node) agyPaneStart(ctx context.Context, pane string, now time.Time) time.Time {
	if s, ok := n.cx.input.started(pane, now); ok {
		return s
	}
	var start time.Time
	if pids, err := n.paneProcesses(ctx, pane); err == nil {
		for _, pid := range pids {
			if t, ok := agyProcessStart(pid); ok && (start.IsZero() || t.Before(start)) {
				start = t
			}
		}
	}
	n.cx.input.setStarted(pane, start, now)
	return start
}

// agyProcessStart is when pid started, from the system's own record of it (procinfo.StartMark): seconds on macOS,
// clock ticks since boot on Linux.
func agyProcessStart(pid int) (time.Time, bool) {
	mark := procinfo.StartMark(pid)
	switch {
	case strings.HasPrefix(mark, "darwin:"):
		sec, usec, _ := strings.Cut(strings.TrimPrefix(mark, "darwin:"), ".")
		s, err1 := strconv.ParseInt(sec, 10, 64)
		u, err2 := strconv.ParseInt(usec, 10, 64)
		if err1 != nil || err2 != nil {
			return time.Time{}, false
		}
		return time.Unix(s, u*1000), true
	case strings.HasPrefix(mark, "linux:"):
		ticks, err := strconv.ParseInt(strings.TrimPrefix(mark, "linux:"), 10, 64)
		boot, ok := agyBootTime()
		if err != nil || !ok {
			return time.Time{}, false
		}
		return boot.Add(time.Duration(ticks) * time.Second / agyUserHZ), true
	}
	return time.Time{}, false
}

// agyUserHZ is USER_HZ, the unit of /proc/<pid>/stat's start time: 100 on every Linux Persona runs on.
const agyUserHZ = 100

// agyBootTime is when the system booted (/proc/stat's btime).
func agyBootTime() (time.Time, bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
				return time.Unix(s, 0), true
			}
		}
	}
	return time.Time{}, false
}

// agyInputCard is w's card, made once per id: the same CreatedAt and title on every list.
func (n *Node) agyInputCard(p agyInputPane, w agyWaitingTask, now time.Time) proto.Interaction {
	id := commandInputID(w)
	if ix, ok := n.cx.input.card(id, now); ok {
		return ix
	}
	title := proto.Session{Cwd: p.cwd}
	n.agyPaneTitle(&title, w.conversation)
	if title.Title == "" && p.cwd != "" {
		title.Title = filepath.Base(p.cwd)
	}
	ix := newCommandInputCard(id, p.id, title.Title, p.cwd, agyAgent, agyLabel, now, proto.CommandInput{
		Command: n.agyTaskCommand(w.conversation, w.task), Task: w.task, Prompt: w.prompt, Output: w.output, Masked: w.secret,
	})
	n.cx.input.keepCard(id, ix, now)
	return ix
}

func newCommandInputCard(id, session, title, cwd, agent, label string, now time.Time, in proto.CommandInput) proto.Interaction {
	input, _ := json.Marshal(in)
	return proto.Interaction{
		ID: id, SessionID: session, SessionTitle: title, Cwd: cwd, Kind: proto.IxPermission,
		CreatedAt: now.UTC(), ExpiresAt: now.Add(agyInputMaxAge).UTC(), ToolName: proto.CommandInputTool, ToolInput: input,
		Summary: i18n.T(i18n.Default, "command-input.summary", i18n.Args{"prompt": in.Prompt}),
		Fields: []proto.Field{{Key: proto.CommandInputField, Label: in.Prompt, Secret: true,
			Placeholder: i18n.T(i18n.Default, "command-input.placeholder")}},
		Agent: agent, AgentLabel: label,
	}
}

// agyLabel is what the apps call agy on a card that is not Claude Code's.
const agyLabel = "Antigravity"

// agyTaskCommand is the command a task runs, as its start step describes it ("Task Description: …"), "" when the end
// of the transcript does not say.
func (n *Node) agyTaskCommand(conversation, task string) string {
	path := filepath.Join(n.agyRootFor(conversation), "brain", conversation, agyTranscriptPath)
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	from := max(info.Size()-agyInputTranscriptTail, 0)
	buf := make([]byte, info.Size()-from)
	if _, err := f.ReadAt(buf, from); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	mark := []byte("task id: " + conversation + "/" + task)
	for _, line := range bytes.Split(buf, []byte("\n")) {
		if !bytes.Contains(line, mark) {
			continue
		}
		var step struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(line, &step) != nil {
			continue
		}
		if !strings.Contains(step.Content, string(mark)+"\n") { // task-7, not task-70
			continue
		}
		if m := agyTaskDescRe.FindStringSubmatch(step.Content); m != nil {
			return agyClip(strings.TrimSpace(m[1]), 300)
		}
	}
	return ""
}

// agyTaskDescRe is the description line of a task's start step.
var agyTaskDescRe = lazyre.New(`(?m)^Task Description: (.+)$`)

// agyClip is s cut to at most n characters, an ellipsis marking the cut.
func agyClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// commandInputAnswer types the owner's answer to a command-input card into its task (codexAnswer hands it over).
// Not now and a hand-back type nothing, and the same prompt is not asked again; a line goes only into the prompt the
// card was made from, or nothing is typed (prompt_changed). After the line went in, the same prompt is not asked again
// either: a command that echoes nothing still has it last until it prints more.
func (n *Node) commandInputAnswer(ctx context.Context, session, id string, r proto.Resolution) error {
	switch {
	case r.Cancel:
		return fmt.Errorf("a command's prompt cannot be closed without an answer")
	case r.Dismiss || r.Allow == nil || !*r.Allow:
		n.cx.dismiss(session, id)
		return nil
	}
	line := r.Values[proto.CommandInputField]
	if problem := proto.CommandInputLineProblem(line); problem != "" {
		return refusal(codeCommandInputLine, i18n.T(i18n.Default, "command-input.line_"+problem), map[string]any{"problem": problem})
	}
	p, w, ok := n.agyWaitingFor(ctx, session, id)
	if !ok {
		return refusal(screen.PromptChanged, i18n.T(i18n.Default, "command-input.prompt_changed"), nil)
	}
	ctx, cancel := context.WithTimeout(ctx, agyInputSendWait)
	defer cancel()
	ls, err := n.agyLanguageServer(ctx, p.pane)
	if err != nil {
		n.logf("command input %s: %v", id, err)
		return refusal(codeCommandInputUnreachable, i18n.T(i18n.Default, "command-input.unreachable"), nil)
	}
	if err := ls.sendTaskInput(ctx, w.conversation, w.task, line); err != nil {
		n.logf("command input %s: %v", id, err) // the error never holds the line or the token
		if errors.Is(err, errAgyUnconfirmed) {
			// The request went out: agy may have typed the line. Not "nothing typed", so sending it again is the
			// owner's call after a look at the command (the card stays, saying so).
			data, _ := json.Marshal(map[string]any{"delivered": "unknown"})
			return &rpc.Error{Code: codeCommandInputUnconfirmed, Message: i18n.T(i18n.Default, "command-input.unconfirmed"), Data: data}
		}
		return refusal(codeCommandInputRefused, i18n.T(i18n.Default, "command-input.refused", i18n.Args{"reason": err.Error()}), nil)
	}
	n.logf("command input %s: typed a line into %s", id, w.task)
	n.cx.dismiss(session, id)
	return nil
}

// agyWaitingFor is the pane and the task of card id of session when its prompt is still the one the card was made from.
func (n *Node) agyWaitingFor(ctx context.Context, session, id string) (agyInputPane, agyWaitingTask, bool) {
	now := time.Now()
	for _, p := range n.agyInputPanes() {
		if p.id != session {
			continue
		}
		for _, w := range n.agyWaiting(ctx, p, now, false) {
			if commandInputID(w) == id {
				return p, w, true
			}
		}
	}
	return agyInputPane{}, agyWaitingTask{}, false
}

// agyLS is an agy language server: where it listens, and the CSRF token it asks for.
type agyLS struct {
	addr, token string
}

// agyProcEnv reads the language server's two variables from a process's environment (nil when it has neither); tests
// stand in for it.
var agyProcEnv = readAgyProcEnv

// agyLanguageServer is the language server of the agy running pane. agy gives its address and token to every command
// it runs, so they are read from the environment of a process under the pane's agy: the waiting command's own, or
// another one's. Only a loopback address is taken: the line and the token never leave the computer.
func (n *Node) agyLanguageServer(ctx context.Context, pane string) (agyLS, error) {
	roots, err := n.paneProcesses(ctx, pane)
	if err != nil {
		return agyLS{}, fmt.Errorf("the pane's processes: %w", err)
	}
	procs, err := listProcs(ctx)
	if err != nil {
		return agyLS{}, fmt.Errorf("the process table: %w", err)
	}
	children := procChildren(procs)
	seen := map[int]bool{}
	queue := slices.Clone(roots)
	for len(queue) > 0 && len(seen) < agyInputMaxProcs {
		if ctx.Err() != nil {
			return agyLS{}, ctx.Err()
		}
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] || pid <= 0 {
			continue
		}
		seen[pid] = true
		queue = append(queue, children[pid]...)
		env := agyProcEnv(pid)
		addr, token := env[agyLSAddressEnv], env[agyLSTokenEnv]
		if addr == "" || token == "" {
			continue
		}
		if !loopbackAddress(addr) {
			return agyLS{}, fmt.Errorf("agy's language server is not on this computer")
		}
		return agyLS{addr: addr, token: token}, nil
	}
	return agyLS{}, fmt.Errorf("no command under the pane's agy names its language server")
}

// readAgyProcEnv reads the two variables from pid's environment as it was started: /proc/<pid>/environ on Linux,
// ps -E on macOS (as readPaneEnv does).
func readAgyProcEnv(pid int) map[string]string {
	var env []byte
	if runtime.GOOS == "linux" {
		env, _ = os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		env, _ = exec.CommandContext(ctx, "ps", "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		cancel()
	}
	if len(env) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, name := range []string{agyLSAddressEnv, agyLSTokenEnv} {
		if m := envVarRe(name).FindSubmatch(env); m != nil {
			out[name] = string(m[1])
		}
	}
	return out
}

// loopbackAddress: addr is host:port with a loopback host (localhost, 127.0.0.0/8, ::1) and a port.
func loopbackAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if p, err := strconv.Atoi(port); err != nil || p <= 0 || p > 65535 {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// agyLSMethod is the path of the language server's method that sends a message to an agent or a task.
const agyLSMethod = "/exa.language_server_pb.LanguageServerService/SendAgentMessage"

// sendTaskInput types line into task of conversation: a message to the task with the line and a newline, the way
// manage_task send_input sends it (sender: the conversation). The request goes to a loopback address with no proxy and
// no redirect; its body is cleared once sent. Errors say what failed, never the line or the token.
func (ls agyLS) sendTaskInput(ctx context.Context, conversation, task, line string) error {
	body, err := json.Marshal(struct {
		Recipient string `json:"recipient"`
		SenderID  string `json:"senderId"`
		Content   string `json:"content"`
	}{conversation + "/" + task, conversation, line + "\n"})
	if err != nil {
		return err
	}
	defer clear(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ls.addr+agyLSMethod, bytes.NewReader(body))
	if err != nil {
		return errors.New("the language server's address")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("X-Codeium-Csrf-Token", ls.token)
	req.Header.Set("X-Antigravity-Csrf-Token", ls.token)
	client := &http.Client{
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return errors.New("the language server is not reachable") // nothing went out
		}
		return errAgyUnconfirmed
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	var e struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
	if code := agyLSCodeRe.FindString(e.Code); code != "" {
		return fmt.Errorf("HTTP %d, %s", resp.StatusCode, code)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}

// errAgyUnconfirmed: the line went to agy's language server and no answer came back (a timeout, a dropped connection),
// so whether agy typed it is not known.
var errAgyUnconfirmed = errors.New("the language server did not confirm the line")

// agyLSCodeRe is a Connect error code (not_found, permission_denied…): nothing else of the language server's answer is
// repeated.
var agyLSCodeRe = lazyre.New(`^[a-z_]{1,40}$`)

// messages is what conversation's messages directory says of its tasks now: the files not read yet are read once.
func (s *agyInputs) messages(conversation, dir string, now time.Time) agyConvSeen {
	entries, _ := os.ReadDir(dir)
	s.mu.Lock()
	if s.convs == nil {
		s.convs = map[string]*agyConvSeen{}
	}
	c := s.convs[conversation]
	if c == nil {
		c = &agyConvSeen{read: map[string]bool{}, ended: map[string]bool{}}
		s.convs[conversation] = c
	}
	c.seen = now
	var unread []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && e.Type().IsRegular() && !c.read[e.Name()] {
			unread = append(unread, e.Name())
		}
	}
	s.mu.Unlock()
	notes := map[string]agyMessageNote{}
	for _, name := range unread {
		notes[name] = readAgyMessage(filepath.Join(dir, name), conversation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, m := range notes {
		c.read[name] = true
		if m.task != "" {
			c.ended[m.task] = true
		}
		if m.restart.After(c.restart) {
			c.restart = m.restart
		}
	}
	return agyConvSeen{ended: maps.Clone(c.ended), restart: c.restart}
}

// agyMessageNote is what one message file says of the conversation's tasks: the task whose end it reports, or when a
// restart stopped them all.
type agyMessageNote struct {
	task    string
	restart time.Time
}

// readAgyMessage reads one message file of conversation: the task whose end it reports (from <conversation>/task-N,
// "Task id … finished"), or the time of a restart notice (from system, "… stopped due to server restart").
func readAgyMessage(path, conversation string) (m agyMessageNote) {
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	head := make([]byte, agyInputMessageRead)
	k, _ := io.ReadFull(f, head)
	head = head[:k]
	sender := agyMsgField(head, "sender")
	content := agyMsgField(head, "content")
	switch {
	case strings.HasPrefix(sender, conversation+"/") && strings.HasPrefix(content, "Task id ") && strings.Contains(content, " finished"):
		if task := strings.TrimPrefix(sender, conversation+"/"); agyTaskRe.MatchString(task) {
			m.task = task
		}
	case sender == "system" && strings.Contains(content, "stopped due to server restart"):
		if t, err := time.Parse(time.RFC3339Nano, agyMsgField(head, "timestamp")); err == nil {
			m.restart = t
		}
	}
	return m
}

// agyMsgField is a string field of a message file's first bytes, read up to the cut when the file is longer: its
// sender, timestamp and the start of its content come before the rest of a long output.
func agyMsgField(head []byte, name string) string {
	key := []byte(`"` + name + `":`)
	i := bytes.Index(head, key)
	if i < 0 {
		return ""
	}
	rest := bytes.TrimLeft(head[i+len(key):], " ")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	var b strings.Builder
	for j := 1; j < len(rest); j++ {
		c := rest[j]
		switch {
		case c == '"':
			return b.String()
		case c == '\\' && j+1 < len(rest):
			j++
			switch rest[j] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(rest[j]) // \" \\ \/ as themselves; \u escapes are kept as written, never needed here
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String() // cut at the read's end: what came is enough
}

// quietFor says whether the log at path has looked as it does (size, modification time) for agyInputQuiet.
func (s *agyInputs) quietFor(path string, info os.FileInfo, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logs == nil {
		s.logs = map[string]agyLogSeen{}
	}
	l, ok := s.logs[path]
	if !ok || l.size != info.Size() || !l.mod.Equal(info.ModTime()) {
		l = agyLogSeen{size: info.Size(), mod: info.ModTime(), since: now}
	}
	l.seen = now
	s.logs[path] = l
	return now.Sub(l.since) >= agyInputQuiet
}

func (s *agyInputs) started(pane string, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.starts[pane]
	return st.start, ok && now.Sub(st.at) < agyInputStartKeep
}

func (s *agyInputs) setStarted(pane string, start, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.starts == nil {
		s.starts = map[string]agyPaneStarted{}
	}
	s.starts[pane] = agyPaneStarted{start: start, at: now}
}

func (s *agyInputs) card(id string, now time.Time) (proto.Interaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cards[id]
	if ok {
		c.seen = now
		s.cards[id] = c
	}
	return c.ix, ok
}

func (s *agyInputs) keepCard(id string, ix proto.Interaction, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cards == nil {
		s.cards = map[string]agyInputCardAt{}
	}
	s.cards[id] = agyInputCardAt{ix: ix, seen: now}
}

// prune forgets what no reading looked at for a minute: logs, conversations, panes and cards that went away.
func (s *agyInputs) prune(now time.Time) {
	const keep = time.Minute
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, l := range s.logs {
		if now.Sub(l.seen) > keep {
			delete(s.logs, k)
		}
	}
	for k, c := range s.convs {
		if now.Sub(c.seen) > keep {
			delete(s.convs, k)
		}
	}
	for k, st := range s.starts {
		if now.Sub(st.at) > keep {
			delete(s.starts, k)
		}
	}
	for k, c := range s.cards {
		if now.Sub(c.seen) > keep {
			delete(s.cards, k)
		}
	}
}
