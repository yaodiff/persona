package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A model picked for one of the assistant's conversations (#5033, docs/assistant/model-config.md "A conversation's own
// model"): the user switches a conversation to another launcher, model or reasoning effort in the chat's model panel,
// and its next messages run on that until they switch again or go back to the default. The pick lives with the
// conversation on the hub side (settings assistant.model_picks), so every device sees the same one (model_pick on the
// conversation) and a message carries none of it. With no pick, the main conversation and a scheduled task's follow
// Model configuration › Main chat and the others keep what they run, as before (sendModel). A launcher or model picked
// with a message (an older app) still wins for that message.
//
// Unlike the default, a pick is never replaced behind the user's back: a message to a conversation whose picked
// launcher is gone from the computer, out of its provider's device scope or unable to run the assistant is refused with
// why (modelPickRefusal), and the user picks again.

const (
	modelPicksKey = "assistant.model_picks"
	// maxModelPicks bounds the book (conversations come and go): the oldest picks go first.
	maxModelPicks = 512
)

// modelPick is a conversation's pick as kept.
type modelPick struct {
	MachineID string    `json:"machine_id"`
	Launcher  string    `json:"launcher"`
	Model     string    `json:"model,omitempty"`
	Effort    string    `json:"effort,omitempty"`
	At        time.Time `json:"at"`
	// Runtime and Channel are what the pick was asked for in (docs/assistant/execution-spec.md), when it was: the
	// launcher is the alias that runs them.
	Runtime string `json:"runtime,omitempty"`
	// Channel: see Runtime.
	Channel string `json:"channel,omitempty"`
}

// wire is the pick as the apps get it.
func (p modelPick) wire() *proto.ModelPick {
	return &proto.ModelPick{Launcher: p.Launcher, Model: p.Model, Effort: p.Effort, Runtime: p.Runtime, Channel: p.Channel}
}

// modelPickBook is the book in memory. m is replaced, never changed in place, so a reader keeps the one it took; mu
// guards m and loaded, write makes changes one at a time and is held over the database write, which readers (every
// snapshot, every message) never wait on.
type modelPickBook struct {
	mu     sync.Mutex
	write  sync.Mutex
	loaded bool
	m      map[string]modelPick // by session id
}

// pickEfforts are the reasoning efforts a pick may carry: "auto" (the model's own default), Claude Code's levels and
// Codex's (node/codex_effort.go). Whether the model takes one is the node's to say when it runs.
var pickEfforts = append([]string{"auto", "none", "minimal", "ultra"}, proto.EffortLevels...)

// modelPicksLocked reads the book once; b.mu is held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) modelPicksLocked(ctx context.Context) bool {
	b := &s.modelPicks
	if b.loaded {
		return true
	}
	m := map[string]modelPick{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, modelPicksKey, &m); err != nil {
			s.logf("load conversation model picks: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]modelPick{}
	}
	b.m, b.loaded = m, true
	return true
}

// modelPicksSet is every pick by session id; false while the book could not be read.
func (s *Service) modelPicksSet() (map[string]modelPick, bool) {
	b := &s.modelPicks
	b.mu.Lock()
	if !s.modelPicksLocked(context.Background()) {
		b.mu.Unlock()
		return nil, false
	}
	out := maps.Clone(b.m)
	b.mu.Unlock()
	if s.storeFn.Load() != nil {
		rows, _, err := s.logicalIndex()
		if err != nil {
			return out, false
		}
		for _, c := range rows {
			if a, ok := c.Active(); ok && a.Ordinal > 1 {
				out[c.SessionID] = modelPick{MachineID: c.MachineID, Launcher: a.Launcher, Model: a.Model, Effort: a.Effort}
			}
		}
	}
	return out, true
}

// modelPickOf is conversation sid's pick on machine; false when it has none (or one kept for another computer), or when
// the book can't be read (a message then runs as if there were none: a pick that is not known can't be honored or
// refused). Every assistant conversation may have one (#9248; #6911 had limited it to the main conversation so that a
// pick nobody could see never ran a leader or the supervisor): what the apps show as the conversation's model_pick is
// exactly what its next message runs on.
func (s *Service) modelPickOf(ctx context.Context, machine, sid string) (modelPick, bool) {
	p, ok, _ := s.readModelPick(ctx, machine, sid)
	return p, ok
}

// readModelPick distinguishes an absent selection from an unreadable one at delivery boundaries.
// Store read errors (logical conversation lookup or model-picks book load) surface as the localized
// assistant-model_pick.err_unreadable error rather than a raw database error.
func (s *Service) readModelPick(ctx context.Context, machine, sid string) (modelPick, bool, error) {
	if c, err := s.logicalPickConversation(ctx, machine, sid); err != nil {
		s.logf("load conversation model pick (%s/%s): %v", machine, short(sid), err)
		return modelPick{}, false, i18n.Err("assistant-model_pick.err_unreadable")
	} else if c != nil {
		a, _ := c.Active()
		return modelPick{MachineID: c.MachineID, Launcher: a.Launcher, Model: a.Model, Effort: a.Effort}, true, nil
	}
	b := &s.modelPicks
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.modelPicksLocked(ctx) {
		return modelPick{}, false, i18n.Err("assistant-model_pick.err_unreadable")
	}
	p, ok := b.m[sid]
	return p, ok && p.MachineID == machine, nil
}

// setModelPick keeps p as conversation sid's pick (nil: drops it) and tells the apps. Dropping a pick that isn't there
// is no error.
func (s *Service) setModelPick(ctx context.Context, sid string, p *modelPick) error {
	return s.updateModelPick(ctx, sid, func(_ modelPick, had bool) (*modelPick, bool) {
		if p == nil {
			return nil, had
		}
		return p, true
	})
}

// updateModelPick changes conversation sid's pick to what change makes of it (its pick, and whether it had one): the
// next pick (nil: none) and whether anything changed. A change is saved, then the apps are told.
func (s *Service) updateModelPick(ctx context.Context, sid string, change func(p modelPick, had bool) (*modelPick, bool)) error {
	if handled, err := s.logicalUpdatePick(ctx, sid, change); handled {
		return err
	}
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-models.err_no_database")
	}
	b := &s.modelPicks
	b.write.Lock()
	b.mu.Lock()
	if !s.modelPicksLocked(ctx) {
		b.mu.Unlock()
		b.write.Unlock()
		return i18n.Err("assistant-model_pick.err_unreadable")
	}
	cur := b.m
	b.mu.Unlock()
	old, had := cur[sid]
	p, changed := change(old, had)
	if !changed {
		b.write.Unlock()
		return nil
	}
	if p != nil && proto.IsExternalLauncher(p.Launcher) {
		resolved, err := s.resolveExternalPick(ctx, sid, *p)
		if err != nil {
			b.write.Unlock()
			return refusePick(http.StatusBadRequest, err)
		}
		p = &resolved
	}
	next := maps.Clone(cur)
	if p == nil {
		delete(next, sid)
	} else {
		next[sid] = *p
		s.evictModelPicks(ctx, next, sid)
	}
	if err := st.PutSetting(ctx, modelPicksKey, next); err != nil {
		b.write.Unlock()
		return err
	}
	b.mu.Lock()
	b.m = next
	b.mu.Unlock()
	b.write.Unlock() // before publishing: the snapshot reads the book, and the next change need not wait for it
	s.publish(ctx)   // the apps get the conversation with its model_pick now
	return nil
}

// evictModelPicks bounds picks to maxModelPicks: the oldest go, but never the main conversation's, however long it has
// kept it, nor kept's (the one just set).
func (s *Service) evictModelPicks(ctx context.Context, picks map[string]modelPick, kept string) {
	if len(picks) <= maxModelPicks {
		return
	}
	main := s.settings(ctx).Main
	for len(picks) > maxModelPicks {
		oldest := ""
		for id, p := range picks {
			if id != main && id != kept && (oldest == "" || p.At.Before(picks[oldest].At)) {
				oldest = id
			}
		}
		if oldest == "" {
			return
		}
		delete(picks, oldest)
	}
}

// applyPick puts conversation pick into p, a message's (or warm's) params, as a start with it runs: its launcher, its
// model, its effort unless p has one, checked and fingerprinted. A pick without a model runs what the start ran: Model
// configuration › Main chat's model when the launcher is the one configured there (applyModel, later changes there
// too), else the launcher's own default.
func (s *Service) applyPick(ctx context.Context, p map[string]any, pick modelPick) error {
	return s.applyPickFor(ctx, "assistant", p, pick)
}

func (s *Service) applyPickFor(ctx context.Context, purpose string, p map[string]any, pick modelPick) error {
	if proto.IsExternalLauncher(pick.Launcher) {
		scope, ok := ctx.Value(externalPickKey{}).(externalPickScope)
		if !ok || scope.pick != pick {
			return i18n.Err("external-adapter.scope_required")
		}
	}
	p["launcher"] = pick.Launcher
	if e, _ := p["effort"].(string); e == "" && pick.Effort != "" {
		p["effort"] = pick.Effort
	}
	if pick.Model == "" {
		delete(p, "model")
		return s.applyModel(ctx, purpose, p)
	}
	p["model"] = pick.Model
	return s.applyPickedModel(ctx, p)
}

// relayOnPick puts conversation sid's pick into params, a message the hub relays to it (supervision, notices, another
// conversation's): it runs on the pick like one the user types, so the model the apps show is the next one's.
// Ordinary legacy picks keep the previous fallback behavior; an external refusal never reaches the old connection.
func (s *Service) relayOnPick(ctx context.Context, machine, sid string, params map[string]any, pick modelPick) error {
	p := maps.Clone(params)
	if err := s.sendOnPick(ctx, machine, sid, p, pick, s.lang(ctx)); err != nil {
		if proto.IsExternalLauncher(pick.Launcher) {
			return err
		}
		s.logf("[%s] %s: its pick can't run a relayed message: %v; it keeps what it ran last", machine, short(sid), err)
		return nil
	}
	maps.Copy(params, p)
	return nil
}

// relayMainModel puts Main chat's configured model into an unpicked main conversation's relayed message when it stays
// on the conversation's current execution path, so an idle reload woken by a relay keeps the configured model.
func (s *Service) relayMainModel(ctx context.Context, machine, sid string, params map[string]any) {
	c, known := s.session(ctx, machine, sid)
	if !known {
		return
	}
	resolved, err := s.resolveModel(ctx, purposeMain)
	if err != nil {
		s.logf("[%s] %s: main chat relay model: %v; it keeps what it ran last", machine, short(sid), err)
		return
	}
	targetLauncher, ok := strings.CutPrefix(resolved.Provider, "launcher.")
	if !ok || targetLauncher == "" || proto.IsExternalLauncher(targetLauncher) {
		return
	}
	if c.SourceKind == proto.SourceRuntime {
		if c.Launcher == "" || c.Launcher != targetLauncher || (resolved.Model == "" && resolved.Effort == "") {
			return
		}
	} else {
		if err := s.otherKindPick(ctx, machine, sid, targetLauncher); err != nil {
			return
		}
		if err := s.logicalAdapterPick(ctx, machine, c, targetLauncher); err != nil {
			return
		}
	}
	p := maps.Clone(params)
	if err := preservePermissionMode(p, func() error { return s.applyModel(ctx, purposeMain, p) }); err != nil {
		s.logf("[%s] %s: main chat relay model: %v; it keeps what it ran last", machine, short(sid), err)
		return
	}
	maps.Copy(params, p)
}

// pickEffortSet makes effort the pick's effort of conversation sid on machine, when it has a pick: the apps set it
// through POST …/modes (#2416), and the next message would otherwise put the pick's back. A conversation without a
// pick keeps its modes on the node, so they change nothing here.
func (s *Service) pickEffortSet(ctx context.Context, machine, sid, effort string) {
	if !slices.Contains(pickEfforts, effort) {
		return
	}
	err := s.updateModelPick(ctx, sid, func(p modelPick, had bool) (*modelPick, bool) {
		if !had || p.MachineID != machine || p.Effort == effort {
			return nil, false
		}
		p.Effort, p.At = effort, time.Now().UTC()
		return &p, true
	})
	if err != nil {
		s.logf("[%s] %s: the pick's effort: %v", machine, short(sid), err)
	}
}

// nodeCallDone sees a node call the hub made through this manager's devices (devices.call, params) once it went
// through: an effort set on a conversation with a pick (session.modes) becomes the pick's (pickEffortSet). Anything else
// is left alone, without being decoded.
func (s *Service) nodeCallDone(ctx context.Context, params json.RawMessage) {
	if !bytes.Contains(params, []byte(`"session.modes"`)) {
		return
	}
	var c devicewire.NodeCall
	if json.Unmarshal(params, &c) != nil || c.Method != "session.modes" {
		return
	}
	var m struct {
		SessionID string `json:"session_id"`
		Effort    string `json:"effort"`
	}
	if json.Unmarshal(c.Params, &m) != nil || m.SessionID == "" || m.Effort == "" {
		return
	}
	s.pickEffortSet(ctx, c.MachineID, m.SessionID, m.Effort)
}

// conversationMachine is where conversation sid runs: machine when the app named one, the main conversation's
// machine for the main one, else the computer that lists it as the assistant's, else the assistant's own.
func (s *Service) conversationMachine(ctx context.Context, sid, machine string) string {
	if machine != "" {
		return machine
	}
	cfg := s.settings(ctx)
	if cfg.Main != "" && cfg.Main == sid {
		return cfg.mainRouteMachine()
	}
	for _, m := range s.fleetMachines() {
		if sess, ok := s.devs.Session(m.ID, sid); ok && m.isAssistant(sess) {
			return m.ID
		}
	}
	return cfg.MachineID
}

// pickError is a refused pick with the status the route answers.
type pickError struct {
	status int
	err    error
}

func (e *pickError) Error() string { return e.err.Error() }
func (e *pickError) Unwrap() error { return e.err }

func refusePick(status int, err error) error { return &pickError{status: status, err: err} }

// pickModel checks a pick for conversation sid (on machine, "" to find it) and keeps it: the launcher can run the
// assistant there (modelPickRefusal) and runs the conversation's agent kind (otherKindPick), the model is a model id
// the launcher takes, the effort one a pick may carry.
// lang is the app's, for the refusals that are worded here.
func (s *Service) pickModel(ctx context.Context, sid, machine string, in proto.ModelPick, lang i18n.Lang) (modelPick, error) {
	in.Launcher, in.Model, in.Effort = strings.TrimSpace(in.Launcher), strings.TrimSpace(in.Model), strings.TrimSpace(in.Effort)
	in.Runtime, in.Channel = strings.TrimSpace(in.Runtime), strings.TrimSpace(in.Channel)
	if in.Runtime != "" || in.Channel != "" {
		var err error
		if in, err = s.projectPick(ctx, sid, machine, in); err != nil {
			return modelPick{}, refusePick(http.StatusBadRequest, err)
		}
	}
	switch {
	case in.Launcher == "":
		return modelPick{}, refusePick(http.StatusBadRequest, i18n.Err("assistant-model_pick.err_launcher_required"))
	case in.Effort != "" && !slices.Contains(pickEfforts, in.Effort):
		return modelPick{}, refusePick(http.StatusBadRequest, i18n.Err("assistant-model_pick.err_effort", i18n.Args{"effort": in.Effort}))
	}
	if err := checkModelID(in.Model); err != nil {
		return modelPick{}, refusePick(http.StatusBadRequest, err)
	}
	machine = s.conversationMachine(ctx, sid, machine)
	if _, ok := s.device(ctx, machine); !ok {
		return modelPick{}, refusePick(http.StatusNotFound, errUnknownMachine)
	}
	// Any assistant conversation may keep a pick (#9248), an Agent or an unknown id may not. The main conversation may
	// not have run yet, so it is told by the settings and its route machine; the others by being an assistant
	// conversation on machine.
	if !s.keepsPickOn(ctx, sid, machine) {
		return modelPick{}, refusePick(http.StatusBadRequest, i18n.Err("assistant-model_pick.err_not_assistant"))
	}
	launcherMachine := s.logicalExecutionMachine(ctx, machine, sid)
	external := proto.IsExternalLauncher(in.Launcher)
	if external {
		pick, err := s.resolveExternalPick(ctx, sid, modelPick{MachineID: machine, Launcher: in.Launcher, Model: in.Model})
		if err != nil {
			return modelPick{}, refusePick(http.StatusBadRequest, err)
		}
		in.Model = pick.Model
	} else if err := s.modelPickRefusal(ctx, launcherMachine, in.Launcher, lang, false); err != nil {
		return modelPick{}, err
	}
	var other *launcherOtherKind
	if err := s.otherKindPick(ctx, machine, sid, in.Launcher); err != nil {
		if errors.As(err, &other) {
			return modelPick{}, other.rpcError(lang) // Claude Code can't resume a Codex thread, nor Codex a Claude transcript
		}
		var refusal *rpc.Error
		if errors.As(err, &refusal) && refusal.Code == CodeBadRequest {
			return modelPick{}, err // Keep the logical refusal's non-delivery receipt.
		}
		return modelPick{}, refusePick(http.StatusBadRequest, err)
	}
	if in.Model != "" && !external {
		if err := s.pickedModelRefusal(ctx, launcherMachine, in.Launcher, in.Model); err != nil {
			return modelPick{}, refusePick(http.StatusBadRequest, err)
		}
	}
	p := modelPick{MachineID: machine, Launcher: in.Launcher, Model: in.Model, Effort: in.Effort, At: time.Now().UTC(),
		Runtime: in.Runtime, Channel: in.Channel}
	if err := s.setModelPick(ctx, sid, &p); err != nil {
		return modelPick{}, err
	}
	return p, nil
}

// keepsPickOn: sid on machine is a conversation that may keep a pick: the main one on its route machine, or any other
// assistant conversation the computer has.
func (s *Service) keepsPickOn(ctx context.Context, sid, machine string) bool {
	if cfg := s.settings(ctx); cfg.Main != "" && cfg.Main == sid {
		return cfg.mainRouteMachine() == machine
	}
	c, ok := s.session(ctx, machine, sid)
	return ok && c.Assistant
}

// modelPickRefusal is why launcher name can't be a conversation's pick on machine, nil when it can: it can't run the
// assistant there, or it is not there (a Persona-managed provider out of the computer's device scope, or skipping a
// computer with its own sign-in; any other launcher missing from the computer's list). sending: the refusal of a
// message that would have run on it (the words say the message was not sent). As mainChatFit, a list that can't be
// read refuses nothing.
func (s *Service) modelPickRefusal(ctx context.Context, machine, name string, lang i18n.Lang, sending bool) error {
	if proto.IsExternalLauncher(name) {
		_, err := s.externalTarget(ctx, machine, name)
		if err != nil && !sending {
			return refusePick(http.StatusBadRequest, err)
		}
		return err
	}
	fit, refusal := s.mainChatFit(ctx, machine, name)
	switch fit {
	case mainChatFits:
		return nil
	case mainChatRefused:
		if sending {
			return refusal
		}
		d, _ := s.device(ctx, machine)
		args := i18n.Args{"launcher": name, "machine": firstNonEmpty(d.Name, machine)}
		if picker, err := s.pickerLaunchers(ctx, machine); err == nil {
			if i := slices.IndexFunc(picker, func(l ConfiguredLauncher) bool { return l.Name == name }); i >= 0 {
				args["launcher"] = launcherLabel(picker[i])
			}
			if fits, err := s.assistantLaunchersOf(ctx, machine, picker); err == nil {
				if f, ok := assistantFor(fits, name); ok { // the one set up to run the assistant for it
					args["instead"] = launcherLabel(f)
					return refusePick(http.StatusBadRequest, i18n.Err("assistant-model_pick.err_not_main_chat_instead", args))
				}
			}
		}
		return refusePick(http.StatusBadRequest, i18n.Err("assistant-model_pick.err_not_main_chat", args))
	}
	if l, managed := s.managedNamed(ctx, name); managed {
		if key := s.scopeReason(l, machine); key != "" {
			return &rpc.Error{Code: proto.CodeLauncherOutOfScope, Message: s.scopeText(ctx, lang, key, l, machine)}
		}
		if l.SkipsOwnService() {
			return s.ownServiceErr(ctx, lang, l, machine)
		}
	}
	d, _ := s.device(ctx, machine)
	data, _ := json.Marshal(map[string]string{"launcher": name, "machine_name": d.Name}) // the hub words it (apierr.Sentence)
	return &rpc.Error{Code: proto.CodeLauncherMissing, Message: fmt.Sprintf("launcher %s is not on %s", name, machine), Data: data}
}

// pickedModelRefusal: model can't be picked with launcher on machine because the launcher would not pass it to its
// agent (it sets its own --model, or its agent takes no model). A model its list lacks is a typed one and is kept, as a
// message's pick (#1644); a list that can't be read refuses nothing.
func (s *Service) pickedModelRefusal(ctx context.Context, machine, launcher, model string) error {
	ctx = withLauncherPurpose(ctx, "assistant")
	if proto.IsExternalLauncher(launcher) {
		e, err := s.externalTarget(ctx, machine, launcher)
		if err != nil {
			return err
		}
		_, err = externalModel(e, model)
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	res, err := s.askLauncherModels(cctx, machine, launcher, false) // the node keeps its list a while
	switch {
	case err != nil:
		return nil
	case res.Locked != "":
		return i18n.Err("assistant-model_pick.err_locked", i18n.Args{"launcher": launcher})
	case !takesTypedModel(res) && !listsModel(res, model):
		return i18n.Err("assistant-model_pick.err_no_model", i18n.Args{"launcher": launcher})
	}
	return nil
}

// keepStartPick keeps the launcher / model / effort a new main conversation was started with (POST
// /api/v1/assistant/conversations, Main: true) as its pick, so its later messages keep them, and shows it on sess.
// Only the main conversation keeps a pick (#6911): main is passed explicitly because a carried start
// (startMain) calls this before settings name the new conversation as the main one. Another conversation's start
// runs on what it named but keeps nothing. Nothing is kept when neither a launcher nor a model was named, or when
// the start fell back to another launcher than the one named (or than the one the model was for); an effort a pick
// can't carry is left out.
func (s *Service) keepStartPick(ctx context.Context, sess *proto.Session, launcher, model, effort string, main bool) {
	launcher, effort = strings.TrimSpace(launcher), strings.TrimSpace(effort)
	switch {
	case !main, launcher == "" && model == "", sess.Launcher == "":
		return // an effort alone (iOS sends its last one with every start) is no pick: Model configuration still holds
	case launcher != "" && launcher != sess.Launcher, model != "" && model != sess.Model:
		return // it fell back: the pick is not what runs
	}
	if !slices.Contains(pickEfforts, effort) {
		effort = ""
	}
	p := modelPick{MachineID: sess.MachineID, Launcher: sess.Launcher, Model: model, Effort: effort, At: time.Now().UTC()}
	if err := s.setModelPick(ctx, sess.SessionID, &p); err != nil {
		s.logf("[%s] %s: keep the model it started on: %v", sess.MachineID, short(sess.SessionID), err)
		return
	}
	sess.ModelPick = p.wire()
}

// storedPickRefused: err refuses a pick, or a message on its conversation's pick, and carries a code the hub words and
// the apps act on (launcher_out_of_scope, launcher_own_service, launcher_missing, launcher_other_kind): it goes on as it
// is (409 with the code and the logical non-delivery receipt).
func storedPickRefused(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && slices.Contains([]string{proto.CodeLauncherOutOfScope, proto.CodeLauncherOwnService,
		proto.CodeLauncherMissing, assistantwire.CodeLauncherOtherKind, CodeBadRequest}, re.Code)
}

// modelPickRoutes: PUT /api/v1/assistant/conversations/{id}/model {launcher | runtime?+channel?, model?, effort?,
// machine_id?} keeps the pick
// ({ok, model_pick}); DELETE there drops it (the conversation follows the default again; {ok}).
func (s *Service) modelPickRoutes() {
	s.handle("PUT /api/v1/assistant/conversations/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		id := r.PathValue("id")
		if !validSessionID(id) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-model_pick.err_invalid_id"))
			return
		}
		var in struct {
			proto.ModelPick
			MachineID string `json:"machine_id"` // "": the conversation's machine, found (conversationMachine)
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		p, err := s.pickModel(ctx, id, in.MachineID, in.ModelPick, s.requestLang(r))
		var pe *pickError
		switch {
		case errors.As(err, &pe):
			apierr.Write(w, pe.status, pe.err)
		case storedPickRefused(err):
			apierr.Call(w, err) // 409 with its code; launcher_missing worded by apierr in the app's language
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
		default:
			apierr.WriteJSON(w, map[string]any{"ok": true, "model_pick": p.wire()})
		}
	})
	s.handle("DELETE /api/v1/assistant/conversations/{id}/model", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		id := r.PathValue("id")
		if !validSessionID(id) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-model_pick.err_invalid_id"))
			return
		}
		if err := s.setModelPick(r.Context(), id, nil); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
