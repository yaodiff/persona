package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Answering a start's startup question from the app (#10365). An async start whose new tab stopped on a question
// (shell_waiting) keeps its tab and lists the question with the answers it may be given (questionChoices). The owner
// picks one on a full device; start.continue types it into that tab, only while the same question is still on its
// last line, and starts the session again in the same tab under the same id: the record goes back to starting
// (startLog.restart), so a message sent meanwhile, or sent again, waits for this start instead of getting the old
// error. A question still there then (or a new one) ends that start the same way, with its answers, and the tab is
// kept again. A question no longer on the tab's last line (answered at the computer, or gone by itself) gets nothing
// typed, and the start runs again there all the same: it goes on once the shell is at rest, or ends at once with the
// question the tab shows now and its answers.

// The refusals of start.continue; each typed nothing.
var (
	// errStartNotWaiting: the session has no failed start waiting on a startup question (it started, expired, or
	// another answer already started it again).
	errStartNotWaiting = refusal("start_not_waiting", "this session's start is not waiting on a question any more", nil)
	// errChoiceNotOffered: the answer is not one the question was offered (questionChoices).
	errChoiceNotOffered = refusal("choice_not_offered", "that answer is not offered for this question", nil)
)

// echoWait is how long start.continue watches the question's line after typing an answer: a question that reads a
// whole line shows the answer after it and waits for Enter.
const echoWait = 600 * time.Millisecond

// startContinue answers id's startup question with choice (an id questionChoices offered) and starts id again in its
// kept tab. It returns the session as it starts.
func (n *Node) startContinue(ctx context.Context, id, choice string) (*proto.Session, error) {
	failed, listed := n.starts.failed(id)
	if failed == nil || failed.spec == nil || listed.StartErrorCode != proto.CodeShellWaiting {
		return nil, errStartNotWaiting
	}
	if failed.spec.execution.SecretTicket != "" { // its values were for one start, and that one took them (secretenv.go)
		return nil, refusal(proto.CodeSecretEnvGone, "this start's secret values were used up; start it again", nil)
	}
	pane, line := listed.StartErrorArgs["pane"], listed.StartErrorArgs["line"]
	ask, c, err := offeredAnswer(line, choice)
	if err != nil {
		return nil, err
	}
	// One answer at a time per tab, and none while another request takes the tab for a start of its own (start.keys).
	unlock := n.typing.lock(workAgentPaneLockPrefix + pane)
	defer unlock()
	if again, _ := n.starts.failed(id); again != failed { // an answer before this one started it again
		return nil, errStartNotWaiting
	}
	typed, err := n.typeAnswer(ctx, pane, line, ask, c)
	if err != nil {
		return nil, err
	}
	if typed {
		n.logf("start %s: answered its tab's question %q with %s", id, line, c.Label)
	} else {
		n.logf("start %s: its tab's question %q is gone; starting again there without typing", id, line)
	}
	spec := *failed.spec
	spec.execution.InPane = pane // the kept tab, not a new one (claimKeptTab)
	r, sctx := n.newStartRec(id, &spec, time.Now().UTC())
	if err := n.starts.restart(id, failed, r); err != nil {
		r.cancel()
		return nil, err
	}
	if typed {
		sctx = context.WithValue(sctx, answeredKey{}, true)
	}
	return n.runAsyncStart(sctx, id, r), nil
}

// startAnswer answers line, the startup question the kept tab on pane stopped on, with choice, as startContinue does,
// for a start the hub runs again itself: a planned task's (POST /api/v1/todos/{id}/start/answer), whose start was
// not the node's own async one and has no record here to restart. The hub then starts it in that tab (session.start's
// in_pane); the tab is marked typed so that start waits out the question's line as startContinue's does. A question
// no longer on the tab's last line gets nothing typed and nothing marked (typed false), so the hub records no answer
// of the owner's, and the hub's start there decides as startContinue's does.
func (n *Node) startAnswer(ctx context.Context, pane, line, choice string) (typed bool, _ error) {
	if pane == "" || line == "" {
		return false, errStartNotWaiting
	}
	ask, c, err := offeredAnswer(line, choice)
	if err != nil {
		return false, err
	}
	unlock := n.typing.lock(workAgentPaneLockPrefix + pane)
	defer unlock()
	typed, err = n.typeAnswer(ctx, pane, line, ask, c)
	if err != nil {
		return false, err
	}
	if !typed {
		n.logf("kept tab %s: its question %q is gone; nothing typed", pane, line)
		return false, nil
	}
	n.markKeptTyped(pane)
	n.logf("kept tab %s: answered its question %q with %s", pane, line, c.Label)
	return true, nil
}

// markKeptTyped notes that something was typed into the kept tab on pane (keptStart.typed).
func (n *Node) markKeptTyped(pane string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if k, ok := n.keptStarts[pane]; ok {
		k.typed = true
		n.keptStarts[pane] = k
	}
}

// withoutAnswers is err, a start's refusal, with no answers listed for a startup question it stopped on
// (shell_waiting's choices): for a start that cannot run again on an answer (one that took a hand-off's values,
// secret_env_gone), so no app offers answers it would refuse. Its question, screen and kept tab stay listed.
func withoutAnswers(err error) error {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != proto.CodeShellWaiting || len(re.Data) == 0 {
		return err
	}
	data := map[string]any{}
	if json.Unmarshal(re.Data, &data) != nil {
		return err
	}
	delete(data, proto.ShellAskChoices)
	b, _ := json.Marshal(data)
	return &rpc.Error{Code: re.Code, Message: re.Message, Data: b}
}

// offeredAnswer is the answer choice names among those line, a startup question, is offered (questionChoices), and
// the question's ask: errChoiceNotOffered for any other, and for anything but Ctrl-C to a password prompt.
func offeredAnswer(line, choice string) (shellAsk, shellChoice, error) {
	ask := questionChoices(line)
	c, ok := ask.choice(choice)
	if !ok || ask.Secret && c.ID != choiceCancel.ID { // a password prompt never gets anything typed from the app
		return ask, shellChoice{}, errChoiceNotOffered
	}
	return ask, c, nil
}

// typeAnswer types c, an answer to line (ask), into the kept tab on pane, and says whether it did: only while it is
// still a failed start's kept tab (errStartTabGone). One whose last line is not line any more (answered at the
// computer, gone by itself, or another question since) gets nothing (false): an answer now could run as a command at
// the prompt, or answer another question. The caller holds the pane's typing lock.
func (n *Node) typeAnswer(ctx context.Context, pane, line string, ask shellAsk, c shellChoice) (bool, error) {
	if pane == "" {
		return false, errStartTabGone
	}
	if _, ok := n.keptStartTab(pane); !ok {
		return false, errStartTabGone
	}
	shown, err := n.readPane(ctx, pane, "visible", 60)
	if err != nil {
		return false, err
	}
	if shellQuestion(shown) != line {
		return false, nil
	}
	ctx = withStartTyping(ctx, pane) // the shell, not an agent, is what reads the answer (Orca: typeGate)
	if err := n.sendKeys(ctx, pane, c.Keys...); err != nil {
		return false, err
	}
	if !ask.SingleKey {
		n.enterAfterEcho(ctx, pane, line, c, ask.LineMode)
	}
	return true, nil
}

// enterAfterEcho presses Enter after c, a one-key answer, when the question reads a whole line: its line then shows
// the key after the question and waits. A question known to read one key (shellAsk.SingleKey: compinit's read -q,
// which echoes it) is not asked here: it has moved on, and an Enter would go to whatever reads next. One that read the
// key alone without echoing it has moved on too (oh-my-zsh's read -k), and gets nothing more. A line-reading question
// (lineMode) whose screen did not change within echoWait gets Enter too.
func (n *Node) enterAfterEcho(ctx context.Context, pane, line string, c shellChoice, lineMode bool) {
	if len(c.Keys) != 1 || len([]rune(c.Keys[0])) != 1 {
		return // a named key, or an answer that ends with its own Enter
	}
	key := c.Keys[0]
	deadline := time.Now().Add(echoWait)
	for {
		shown, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			return
		}
		last := lastLine(shown)
		if last != line {
			if rest, ok := strings.CutPrefix(last, line); ok && strings.EqualFold(strings.TrimSpace(rest), key) {
				_ = n.sendKeys(ctx, pane, "enter")
			}
			return
		}
		if !time.Now().Before(deadline) {
			if lineMode {
				_ = n.sendKeys(ctx, pane, "enter")
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(shellSettlePoll / 2):
		}
	}
}

// answeredKey marks the start startContinue runs after typing an answer (answeredOf).
type answeredKey struct{}

// answeredOf: ctx is the start startContinue runs right after answering its tab's question. Its question stays the
// last line written until the shell prints something more (oh-my-zsh only moves the cursor down after the key), however
// long the rest of the startup files take, so it is waited out as in a new tab, not refused at once (keptQuestionWait).
// A kept tab startAnswer answered, or start.keys typed into, is the same for the start that claims it next
// (keptStart.typed).
func answeredOf(ctx context.Context) bool {
	answered, _ := ctx.Value(answeredKey{}).(bool)
	return answered
}
