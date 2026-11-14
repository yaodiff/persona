package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// The built-in manager's own service in `personad hub setup` (docs/assistant/managers.md "The built-in manager as its
// own service"). A new install's hub.json says assistant.mode service, so the hub never starts the built-in manager;
// an existing hub's stays as it is unless --manager-service. Setup then makes the one-time code (as `persona-hub
// manager-code` does) and runs `<root>/bin/personad manager install --builtin --join <the hub on loopback>` with the
// code in its environment: the manager's program keeps a binding that works or trades the code, and writes its
// manager.json and its own unit (persona-manager-assistant.service, or the launchd agent com.persona.manager.assistant)
// itself; the hub writes neither. Last, setup waits until the hub has it on the bus. A switch of an existing hub
// (--manager-service) is checked with the manager's program first, and undone when it cannot be finished: the hub
// starts its child again, so the built-in manager is never left with nothing to run it.

// canManagerService: setup installs services here, and so the built-in manager's own service too: a Mac, or Linux
// with a systemd user instance (without one the service step stops and says to rerun with --no-service).
func (e *setupEnv) canManagerService(ctx context.Context) bool {
	if e.o.noService || e.isInsideContainer() {
		return false
	}
	switch e.goos {
	case "darwin":
		return true
	case "linux":
		return e.userSystemd(ctx)
	}
	return false
}

// userSystemd: systemctl --user works here.
func (e *setupEnv) userSystemd(ctx context.Context) bool {
	if _, err := e.lookPath("systemctl"); err != nil {
		return false
	}
	return e.systemctl(ctx, "show-environment") == nil
}

// noManagerServiceWhy says why canManagerService is false.
func (e *setupEnv) noManagerServiceWhy(ctx context.Context) string {
	switch {
	case e.o.noService:
		return "--no-service"
	case e.isInsideContainer():
		return i18n.T(e.l, "hub-setup.why_container")
	case e.goos == "linux" && !e.userSystemd(ctx):
		return i18n.T(e.l, "hub-setup.why_no_systemd")
	}
	return i18n.T(e.l, "hub-setup.why_no_service", i18n.Args{"os": e.goos})
}

// freshManagerMode is a new install's assistant.mode: service when setup installs the built-in manager's service
// (service true), else the default (child). A mode already in the file stays, but for --manager-service (force),
// which asks for service over a child written there. assistant.external is left alone: the built-in manager runs
// elsewhere then.
func freshManagerMode(o *jsonObject, service, force bool) error {
	a, err := o.Object("assistant")
	if err != nil {
		return err
	}
	if a == nil {
		a = &jsonObject{}
	}
	var external bool
	if raw, ok := a.Get("external"); ok && json.Unmarshal(raw, &external) == nil && external {
		return nil
	}
	var mode string
	if raw, ok := a.Get("mode"); ok {
		_ = json.Unmarshal(raw, &mode)
	}
	if !service || mode == AssistantModeService || (mode != "" && !force) {
		return nil
	}
	if err := a.Set("mode", AssistantModeService); err != nil {
		return err
	}
	o.SetObject("assistant", a)
	return nil
}

// toManagerService sets an existing hub's assistant.mode to service (--manager-service); changed false when it was.
// Before it changes anything, the manager's program that will run the service says it can be installed here (an
// older one does not know how: the hub would be left with no built-in manager).
func (e *setupEnv) toManagerService(ctx context.Context) (changed bool, err error) {
	var mode string
	if raw, err := readHubJSON(e.root); err == nil {
		if a, err := raw.Object("assistant"); err == nil && a != nil {
			if m, ok := a.Get("mode"); ok {
				_ = json.Unmarshal(m, &mode)
			}
		}
	}
	// The binary the service step will run: personad, or an older layout's compat.DaemonBinary it links personad to.
	bin := filepath.Join(e.root, "bin", "personad")
	if _, err := os.Stat(bin); err != nil {
		bin = filepath.Join(e.root, "bin", compat.DaemonBinary)
	}
	if _, statErr := os.Stat(bin); statErr == nil && mode != AssistantModeService {
		if out, err := e.run(ctx, []string{"PERSONA_HOME=" + e.root}, bin, managerconf.BuiltinCheckArgs()...); err != nil {
			why := lastLine(string(out))
			if why == "" {
				why = err.Error()
			}
			return false, e.fail("config", i18n.T(e.l, "hub-setup.manager_check_failed", i18n.Args{"error": why}),
				i18n.T(e.l, "hub-setup.manager_check_failed_next", i18n.Args{"bin": bin}))
		}
	}
	err = editHubJSON(e.root, func(o *jsonObject) error {
		a, err := o.Object("assistant")
		if err != nil {
			return err
		}
		if a == nil {
			a = &jsonObject{}
		}
		var external bool
		if raw, ok := a.Get("external"); ok && json.Unmarshal(raw, &external) == nil && external {
			return errManagerExternal
		}
		var mode string
		if raw, ok := a.Get("mode"); ok {
			_ = json.Unmarshal(raw, &mode)
		}
		if mode == AssistantModeService {
			return nil
		}
		changed, e.prevMode = true, mode
		if err := a.Set("mode", AssistantModeService); err != nil {
			return err
		}
		o.SetObject("assistant", a)
		return nil
	})
	switch {
	case errors.Is(err, errManagerExternal):
		return false, e.fail("config", i18n.T(e.l, "hub-setup.manager_external"),
			i18n.T(e.l, "hub-setup.manager_external_next", i18n.Args{"path": e.hubJSON()}))
	case err != nil:
		return false, e.fail("config", e.writeFailed(e.hubJSON(), err), i18n.T(e.l, "hub-setup.check_permissions"))
	}
	e.switched = changed
	return changed, nil
}

// undoSwitch puts assistant.mode back as it was before this run's --manager-service, takes away the built-in
// manager's service as far as it got (its binding stays; the hub's child gets a token of its own), and restarts the
// hub so that it starts its child again. It says what it did.
func (e *setupEnv) undoSwitch(ctx context.Context, bin string) string {
	err := editHubJSON(e.root, func(o *jsonObject) error {
		a, err := o.Object("assistant")
		if err != nil || a == nil {
			return err
		}
		if e.prevMode == "" {
			a.Del("mode")
		} else if err := a.Set("mode", e.prevMode); err != nil {
			return err
		}
		o.SetObject("assistant", a)
		return nil
	})
	if err != nil {
		return i18n.T(e.l, "hub-setup.undo_failed", i18n.Args{"error": err.Error()})
	}
	e.switched = false
	_, _ = e.run(ctx, []string{"PERSONA_HOME=" + e.root}, bin, "manager", "uninstall", "--builtin")
	if e.hubRestart == nil {
		return i18n.T(e.l, "hub-setup.undo_done")
	}
	if err := e.hubRestart(ctx); err != nil {
		return i18n.T(e.l, "hub-setup.undo_restart_failed", i18n.Args{"error": err.Error()})
	}
	return i18n.T(e.l, "hub-setup.undo_restarted")
}

var errManagerExternal = errors.New("assistant.external")

// managerLog is where the built-in manager's service logs, for the lines setup prints.
func (e *setupEnv) managerLog() string {
	if e.goos == "darwin" {
		return i18n.T(e.l, "hub-setup.manager_log_file", i18n.Args{"path": filepath.Join(managerconf.DefaultDir(e.root), "manager.log")})
	}
	return i18n.T(e.l, "hub-setup.manager_log_journal", i18n.Args{"service": managerconf.BuiltinService})
}

// builtinManager is setup's manager step.
func (e *setupEnv) builtinManager(ctx context.Context, cfg *setupConfig) error {
	a := cfg.Assistant
	bin := filepath.Join(e.root, "bin", "personad")
	switch {
	case a.External:
		e.report("manager", true, i18n.T(e.l, "hub-setup.manager_external_ok"), "")
		return nil
	case a.StartsBuiltin():
		next := ""
		if e.canManagerService(ctx) {
			next = i18n.T(e.l, "hub-setup.manager_child_next")
		}
		e.report("manager", true, i18n.T(e.l, "hub-setup.manager_child"), next)
		return nil
	case !a.ServiceBound():
		e.report("manager", false, i18n.T(e.l, "hub-setup.manager_mode_unknown", i18n.Args{"mode": a.Mode}),
			i18n.T(e.l, "hub-setup.manager_mode_unknown_next", i18n.Args{"path": e.hubJSON()}))
		return nil
	case !e.canManagerService(ctx) && e.builtinOnline(ctx, cfg.Config):
		e.report("manager", true, i18n.T(e.l, "hub-setup.manager_service_running", i18n.Args{"why": e.noManagerServiceWhy(ctx)}), "")
		return nil
	case !e.canManagerService(ctx):
		// Nothing runs the built-in manager here unless the user does: a problem, with both ways out.
		e.report("manager", false, i18n.T(e.l, "hub-setup.manager_service_unattended", i18n.Args{"why": e.noManagerServiceWhy(ctx)}),
			i18n.T(e.l, "hub-setup.manager_service_unattended_next", i18n.Args{"url": managerconf.LocalHubURL(cfg.Listen), "bin": bin}))
		return nil
	}
	retry := i18n.T(e.l, "hub-setup.manager_retry_switched")
	if !e.switched {
		// (a switch an earlier run made is not known here: the way back is said every time)
		retry = i18n.T(e.l, "hub-setup.manager_retry")
	}
	code, err := e.builtinCode(ctx, cfg.Config)
	if err != nil {
		detail := i18n.T(e.l, "hub-setup.manager_code_failed", i18n.Args{"error": err.Error()})
		if e.switched {
			detail += e.sep() + e.undoSwitch(ctx, bin)
		}
		return e.fail("manager", detail, retry)
	}
	// The code goes in the environment, never on the command line: any user here can read that while it runs, and
	// the hub trades this code from its own machine without another credential. One the program did not trade (it
	// kept a binding that works, or stopped first) is voided after.
	programBefore, wasActive := e.builtinProgram(), e.builtinActive(ctx)
	out, err := e.run(ctx, []string{"PERSONA_HOME=" + e.root, managerconf.BuiltinCodeEnv + "=" + code},
		bin, managerconf.BuiltinInstallArgs(managerconf.LocalHubURL(cfg.Listen))...)
	_ = e.voidCode(ctx, cfg.Config, code)
	if err != nil {
		why := lastLine(string(out))
		if why == "" {
			why = err.Error()
		}
		detail := i18n.T(e.l, "hub-setup.manager_install_failed", i18n.Args{"error": why})
		if e.switched {
			detail += e.sep() + e.undoSwitch(ctx, bin)
		}
		return e.fail("manager", detail, retry)
	}
	unit := managerconf.BuiltinService
	if e.goos == "darwin" {
		unit = managerconf.BuiltinLabel
	}
	switched := ""
	if after := e.builtinProgram(); programBefore != "" && after != "" && resolvedPath(programBefore) != resolvedPath(after) {
		if wasActive {
			switched = e.sep() + i18n.T(e.l, "hub-setup.switched_restarted_it", i18n.Args{"program": filepath.Base(after)})
		} else {
			switched = e.sep() + i18n.T(e.l, "hub-setup.switched", i18n.Args{"program": filepath.Base(after)})
		}
	}
	wait := e.managerWait
	deadline := time.Now().Add(wait)
	for {
		if e.builtinOnline(ctx, cfg.Config) {
			e.report("manager", true, i18n.T(e.l, "hub-setup.manager_online", i18n.Args{"unit": unit})+switched, "")
			return nil
		}
		if !time.Now().Before(deadline) || ctx.Err() != nil {
			break
		}
		e.sleep(time.Second)
	}
	// The hub runs and the phone can pair: say what is wrong and go on (its service keeps trying).
	// (said every time: a switch an earlier run made is not known here)
	next := e.managerLog() + e.sep() + i18n.T(e.l, "hub-setup.manager_back_to_child")
	e.report("manager", false, i18n.T(e.l, "hub-setup.manager_offline", i18n.Args{"unit": unit, "count": int(wait.Seconds())})+switched, next)
	return nil
}

// builtinProgram is the program the built-in manager's service runs, under its name or its former one: its unit as
// the user's systemd reads it, or its launchd agent; "" for none.
func (e *setupEnv) builtinProgram() string {
	id := assistantwire.AssistantID
	if e.goos == "darwin" {
		for _, label := range []string{managerconf.BuiltinLabel, compat.LegacyManagerLabel(id)} {
			if job, ok := readLaunchdJob(filepath.Join(e.home, "Library", "LaunchAgents", label+".plist")); ok && len(job.args) > 0 {
				return homePath(job.args[0], e.home)
			}
		}
		return ""
	}
	for _, unit := range []string{managerconf.BuiltinService + ".service", compat.LegacyManagerUnit(id)} {
		if p := e.unitProgram(unit); p != "" {
			return p
		}
	}
	return ""
}

// builtinActive says the built-in manager's service runs, under its name or its former one (on a Mac: launchd has the
// agent).
func (e *setupEnv) builtinActive(ctx context.Context) bool {
	id := assistantwire.AssistantID
	if e.goos == "darwin" {
		for _, label := range []string{managerconf.BuiltinLabel, compat.LegacyManagerLabel(id)} {
			if _, err := e.run(ctx, nil, "launchctl", "print", "gui/"+strconv.Itoa(e.uid)+"/"+label); err == nil {
				return true
			}
		}
		return false
	}
	for _, unit := range []string{managerconf.BuiltinService + ".service", compat.LegacyManagerUnit(id)} {
		if _, err := e.run(ctx, nil, "systemctl", "--user", "is-active", "--quiet", unit); err == nil {
			return true
		}
	}
	return false
}

// builtinOnline: the hub has the built-in manager on its bus as a service of its own (GET /api/v1/managers on loopback).
func (e *setupEnv) builtinOnline(ctx context.Context, cfg *Config) bool {
	client, done := loopbackClient()
	defer done()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", portOf(cfg.Listen))+"/api/v1/managers", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		Managers []managerInfo `json:"managers"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body) != nil {
		return false
	}
	for _, m := range body.Managers {
		if m.ID == assistantwire.AssistantID { // its hello's deploy is "service" then (PERSONA_ASSISTANT_DEPLOY)
			return m.Online && m.Deploy == AssistantModeService
		}
	}
	return false
}

// builtinCode makes a one-time code (10 minutes) that binds the built-in manager's own service to the hub of cfg, in
// its database: `persona-hub manager-code` and setup.
func builtinCode(ctx context.Context, cfg *Config) (string, time.Time, error) {
	if cfg.DatabaseURL == "" {
		return "", time.Time{}, errors.New("hub.json has no database_url")
	}
	database, err := db.Open(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return "", time.Time{}, err
	}
	defer database.Close()
	h := &Hub{cfg: cfg, store: database, logger: log.New(io.Discard, "", 0)}
	return h.newInstallCode(ctx, assistantwire.AssistantID, "助手") // i18n-ignore: its identity's name, as assistant-token gives it
}

// voidBuiltinCode voids the built-in manager's code when it is still good (not traded): ExpireManagerInstallCode.
func voidBuiltinCode(ctx context.Context, cfg *Config, code string) error {
	if cfg.DatabaseURL == "" || code == "" {
		return nil
	}
	database, err := db.Open(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.ExpireManagerInstallCode(ctx, hashToken(code))
	return err
}

// lastLine is the last line of s with text.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
