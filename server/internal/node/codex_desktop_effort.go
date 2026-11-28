package node

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/nwverse/persona/server/internal/proto"
)

// The model and effort a chat of the Codex App runs its next turn with, changed from the phone (#5575) as with the
// App's own picker: the window that owns the chat is asked over the App's bus (codex_desktop.go) to update the thread's
// settings for its next turn (thread-follower-update-thread-settings), so a running turn goes on as it was, and the
// App's picker shows the new model and effort too. The change counts once the window's read-model-settings shows it.
//
// The levels offered are what the chat's model takes on the App's own codex (model/list), as for a thread on an
// app-server (codex_effort.go); the phone's choice is shown until a turn starts after it, whose record then tells.

// codexAppPick is the model and/or effort the phone set for a chat of the Codex App's next turn, and when.
type codexAppPick struct {
	model     string
	modelAt   time.Time
	effort    string
	hasEffort bool
	effortAt  time.Time
}

// codexAppModelsTTL: the App's model list is asked again after this long, in the background (the rows meanwhile offer
// the list kept).
const codexAppModelsTTL = 10 * time.Minute

var codexAppModelLists = struct {
	sync.Mutex
	at     map[string]time.Time
	got    map[string]map[string]codexModelEfforts // home → model → its efforts
	failed map[string]string                       // home → the failure last logged, not logged again on each retry (#9845)
	flight singleflight.Group
}{at: map[string]time.Time{}, got: map[string]map[string]codexModelEfforts{}, failed: map[string]string{}}

// readCodexAppModels asks the codex the App bundles (codexAppCLI) for the models it lists for home, hidden ones too (a
// chat may run one), falling back to <home>/models_cache.json when the CLI is unavailable or empty. A variable: tests
// answer their own.
var readCodexAppModels = func(ctx context.Context, home string) (map[string]codexModelEfforts, error) {
	cli, err := codexAppCLI()
	if err == nil {
		if s, startErr := startCodexStdio(ctx, cli, home, os.TempDir()); startErr == nil {
			defer s.close()
			if got, callErr := readCodexModels(ctx, s.call); callErr == nil && len(got) > 0 {
				return got, nil
			} else if callErr != nil {
				err = callErr
			}
		} else {
			err = startErr
		}
	}
	if cached, ok := readCodexHomeModelEfforts(home); ok {
		return cached, nil
	}
	return nil, err
}

func (n *Node) codexAppModelMeta(home, model string) (codexModelEfforts, bool) {
	codexAppModelLists.Lock()
	at, ok := codexAppModelLists.at[home]
	models := codexAppModelLists.got[home]
	codexAppModelLists.Unlock()
	if !ok || time.Since(at) >= codexAppModelsTTL {
		codexAppModelLists.flight.DoChan(home, func() (any, error) {
			ctx, cancel := context.WithTimeout(context.Background(), codexAppSeedWait)
			defer cancel()
			got, err := readCodexAppModels(ctx, home)
			codexAppModelLists.Lock()
			repeated := err != nil && codexAppModelLists.failed[home] == err.Error()
			if err != nil {
				got = models // the list kept, if any; asked again after the TTL
				codexAppModelLists.failed[home] = err.Error()
			} else {
				delete(codexAppModelLists.failed, home)
			}
			codexAppModelLists.Unlock()
			if err != nil && !repeated {
				n.logf("codex app %s: model/list: %v; its chats offer every effort level", home, err)
			}
			codexAppModelLists.Lock()
			codexAppModelLists.at[home], codexAppModelLists.got[home] = time.Now(), got
			codexAppModelLists.Unlock()
			if err == nil && !ok {
				n.notifyCodex() // the rows can offer their models' own levels
			}
			return nil, nil
		})
	}
	if model == "" {
		return codexModelEfforts{}, false
	}
	e, found := models[model]
	return e, found
}

// codexAppEfforts are the levels a chat of the Codex App running model may be set to: the model's on the App's codex,
// else (a model it does not list, a list not read yet or that could not be read) Codex's whole list, as on an
// app-server (codex_effort.go). Empty: the model takes no effort. The list is read in the background, never here: the
// rows are listed again once it comes.
func (n *Node) codexAppEfforts(home, model string) []string {
	if e, found := n.codexAppModelMeta(home, model); found {
		return e.levels
	}
	return codexEffortLevels
}

// codexDesktopEffortRow lets the phone change the model and effort of a chat of the Codex App (setCodexDesktopSettings)
// as on a thread of an app-server (codexEffortRow): capabilities.model, the levels its model takes, which the apps take
// for effort without capabilities.modes (that would offer the permission mode too), and the model / effort its next turn
// runs with — the phone's choice until a turn starts after it, else what the rollout's last turn ran with (m: its meta),
// else what the App's database keeps for the chat (#6501).
func (n *Node) codexDesktopEffortRow(s *proto.Session, src codexSource, m quickMeta) {
	if s.Capabilities != nil {
		s.Capabilities.Model = proto.Bool(true)
	}
	app := codexAppThreads(src.home)[src.thread]
	if s.Model == "" {
		s.Model = app.Model
	}
	p := n.codexAppPickOf(s.SessionID)
	if p != nil && p.model != "" && p.modelAt.After(m.modelAt) {
		s.Model = p.model
	}
	model := firstNonEmpty(s.Model, m.model)
	levels := n.codexAppEfforts(src.home, model)
	if len(levels) == 0 {
		s.Efforts, s.Effort = nil, ""
		return
	}
	effort := firstNonEmpty(m.effort, app.Effort)
	if p != nil && p.hasEffort && p.effortAt.After(m.effortAt) {
		effort = p.effort
	}
	s.Efforts, s.Effort = levels, effort
}

func (n *Node) codexAppPickOf(id string) *codexAppPick {
	n.mu.Lock()
	defer n.mu.Unlock()
	if p := n.modes[id].App; p != nil {
		cp := *p
		return &cp
	}
	return nil
}

func (n *Node) setCodexAppPick(id, model string, setModel bool, effort string, setEffort bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.modes == nil {
		n.modes = map[string]sessionModes{}
	}
	now := time.Now()
	m := n.modes[id]
	var next codexAppPick
	if m.App != nil {
		next = *m.App
	}
	if setModel {
		next.model, next.modelAt = model, now
	}
	if setEffort {
		next.effort, next.hasEffort, next.effortAt = effort, true, now
	}
	m.App = &next
	n.modes[id] = m
}

// codexDesktopModes is session.modes for a chat of the Codex App: its model and effort. Permission modes remain the
// App's to change.
func (n *Node) codexDesktopModes(ctx context.Context, id string, src codexSource, model, mode, effort string) error {
	if mode != "" {
		return fmt.Errorf("Codex 的会话在手机上只能改思考强度和模型，权限模式要在电脑上改")
	}
	model, effort = strings.TrimSpace(model), strings.TrimSpace(effort)
	if model == "" && effort == "" {
		return nil
	}
	if _, _, err := n.codexOwner(id); err == nil {
		return refusal("codex_read_only", "这个会话没有终端或启动器控制能力，请在原 app-server 中操作", nil)
	} else if !errors.Is(err, errCodexNoOwner) {
		return refusal("codex_read_only", err.Error(), nil)
	}
	var pending sessionState
	if n.state != nil {
		if err := n.state.loaded(ctx); err != nil {
			return err
		}
		pending, _ = n.state.get(id)
	}
	wantedEffort := cmp.Or(effort, pending.DesktopPendingEffort)
	if model != "" && effort == "" && pending.DesktopPendingEffort != "" {
		if meta, known := n.codexAppModelMeta(src.home, model); known && !slices.Contains(meta.levels, pending.DesktopPendingEffort) {
			// Changing the model supersedes an initial effort that the new model cannot use, just as a normal model
			// change reconciles the current effort. The UI may choose its new effort in a subsequent request.
			wantedEffort = reconcileCodexEffort(meta)
		}
	}
	// A partial change also resolves the rest of the original setup before acknowledging it. Otherwise the first
	// message could replay older settings or reconcile its effort against a model the change never read back.
	if err := n.setCodexDesktopSettings(ctx, id, src,
		cmp.Or(model, pending.DesktopPendingModel), wantedEffort); err != nil {
		return err
	}
	if n.state != nil && (pending.DesktopPendingModel != "" || pending.DesktopPendingEffort != "") {
		n.state.update(id, func(cur *sessionState) bool {
			if cur.DesktopPendingModel != pending.DesktopPendingModel || cur.DesktopPendingEffort != pending.DesktopPendingEffort {
				return false
			}
			cur.DesktopPendingModel, cur.DesktopPendingEffort = "", ""
			return true
		})
	}
	return nil
}

func (n *Node) setCodexDesktopEffort(ctx context.Context, id string, src codexSource, effort string) error {
	return n.setCodexDesktopSettings(ctx, id, src, "", effort)
}

// setCodexDesktopSettings sets the model and/or effort of the next turn of a chat of the Codex App, in the App window
// that owns it (the App is asked to open it first, as for a message). It is set once the window reads it back; the
// running turn, if any, goes on with its own.
func (n *Node) setCodexDesktopSettings(ctx context.Context, id string, src codexSource, model, effort string) error {
	model, effort = strings.TrimSpace(model), strings.TrimSpace(effort)
	if model == "" && effort == "" {
		return nil
	}
	if !codexDesktopBusAvailable(src.home) {
		return codexDesktopUnavailable(ctx)
	}
	if model != "" && !proto.ValidModelID(model) {
		return fmt.Errorf("invalid model %q", model)
	}
	m := n.metaOf(id)
	app := codexAppThreads(src.home)[src.thread]
	prev := n.codexAppPickOf(id)
	curModel := firstNonEmpty(m.model, app.Model)
	if prev != nil && prev.model != "" && prev.modelAt.After(m.modelAt) {
		curModel = prev.model
	}
	curEffort := firstNonEmpty(m.effort, app.Effort)
	if prev != nil && prev.hasEffort && prev.effortAt.After(m.effortAt) {
		curEffort = prev.effort
	}
	targetModel := cmp.Or(model, curModel)

	sendEffort := effort
	hasSendEffort := effort != ""
	clearPickedEffort := false
	if effort != "" {
		levels := n.codexAppEfforts(src.home, targetModel)
		if len(levels) == 0 {
			return fmt.Errorf("这个会话的模型不支持设置思考强度")
		}
		if !slices.Contains(levels, effort) {
			return fmt.Errorf("这个会话的模型不支持思考强度 %s（可选：%s）", effort, strings.Join(levels, " / "))
		}
	} else if meta, known := n.codexAppModelMeta(src.home, model); known {
		if len(meta.levels) == 0 {
			clearPickedEffort = true
		} else if curEffort != "" && !slices.Contains(meta.levels, curEffort) {
			sendEffort = reconcileCodexEffort(meta)
			hasSendEffort = sendEffort != ""
		}
	}

	ts := map[string]any{}
	if model != "" {
		ts["model"] = model
	}
	if hasSendEffort {
		ts["effort"] = sendEffort
	}

	what := "思考强度"
	if model != "" {
		what = "模型"
	}
	ctx, cancel := context.WithTimeout(ctx, codexDesktopTimeout)
	defer cancel()
	c, err := dialCodexDesktop(ctx, src.home)
	if err != nil {
		return codexDesktopUnavailable(ctx)
	}
	defer c.close()
	owner, err := c.loadedOwner(ctx, src.thread)
	if err != nil {
		return err
	}
	f, err := c.request("thread-follower-update-thread-settings", map[string]any{"conversationId": src.thread, "threadSettings": ts}, owner.HandledByClient)
	if err != nil {
		if f.Type == "response" { // the window answered: it did not take it
			return refusal("codex_refused", what+"没有改成：Codex App 回复「"+f.Error+"」", nil)
		}
		return fmt.Errorf("%s可能没有改成：和电脑上的 Codex App 断开了（%v），会话里显示的是它现在的设置", what, err)
	}
	var updated struct {
		Applied bool `json:"applied"`
	}
	if json.Unmarshal(f.Result, &updated) != nil || !updated.Applied {
		return refusal("codex_refused", "Codex App 没有接受这个"+what, nil)
	}
	f, err = c.request("thread-follower-read-model-settings", map[string]any{"conversationId": src.thread}, owner.HandledByClient)
	var read struct {
		Settings *struct {
			Model           *string `json:"model"`
			ReasoningEffort *string `json:"reasoningEffort"`
		} `json:"settings"`
	}
	if err != nil || json.Unmarshal(f.Result, &read) != nil || read.Settings == nil {
		return fmt.Errorf("%s可能没有改成：Codex App 没有读回设置", what)
	}
	if model != "" && (read.Settings.Model == nil || *read.Settings.Model != model) {
		return fmt.Errorf("模型可能没有改成：Codex App 读回的模型不是 %s", model)
	}
	if hasSendEffort && (read.Settings.ReasoningEffort == nil || *read.Settings.ReasoningEffort != sendEffort) {
		return fmt.Errorf("思考强度可能没有改成：Codex App 读回的设置不是 %s", sendEffort)
	}
	if clearPickedEffort {
		n.setCodexAppPick(id, model, model != "", "", true)
	} else {
		n.setCodexAppPick(id, model, model != "", sendEffort, hasSendEffort)
	}
	n.notifyCodex()
	return nil
}
