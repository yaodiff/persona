package hub

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
)

// `personad hub setup` (docs/assistant/onboarding.md 4.1, 5.5): one command that leaves a working hub on this machine. It writes
// hub.json (a whole new file only when there is none yet), makes sure there is a database (the same Postgres container
// scripts/deploy/db.sh makes, or the one hub.json names), installs the service that keeps the hub running (the same
// systemd units as scripts/deploy/hub-systemd.sh; a launchd agent on a Mac), waits for the hub, has the built-in manager
// bound and installed as a service of its own (setup_manager.go), checks it, and prints what to do next: a QR code for
// the phone and the admin URL with the first-run setup code in its fragment.
//
// Every step prints one line; a step that fails says what to do and stops. Running it again is safe: values already
// in hub.json are never changed, and a service that is already right is left running.

// ErrSetupStopped: a step failed and said why; the command exits non-zero without printing anything more.
var ErrSetupStopped = errors.New("hub setup stopped")

type setupOptions struct {
	yes       bool
	listen    string
	tls       string
	listenSet bool
	tlsSet    bool
	outside   bool
	noService bool
	json      bool
	// managerService: an existing hub's built-in manager becomes a service of its own (a new install's is anyway).
	managerService bool
	// lang: the language the installer ran in (--lang, else PERSONA_INSTALL_LANG), for a new hub.json's initial_locale.
	lang i18n.Lang
}

// The database `personad hub setup` creates: scripts/deploy/db.sh's container, so deploy.sh keeps working with it.
const (
	setupDBContainer = "persona-postgres"
	setupDBVolume    = "persona-pgdata"
	setupDBPort      = 18661
	hubLaunchdLabel  = "com.persona.hub" // like com.persona.node (persona-link's nodesvc.go)
)

// SetupCommand is `personad hub setup [--yes] [--listen :18660] [--tls :18662] [--outside] [--no-service]
// [--manager-service] [--json] [--lang en|zh-Hans|ja]`.
func SetupCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("hub setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	// The usage is written before --lang is read: in the terminal's language.
	tl := i18n.Terminal()
	var o setupOptions
	flags.BoolVar(&o.yes, "yes", false, i18n.T(tl, "hub-setup.flag_yes"))
	flags.StringVar(&o.listen, "listen", ":18660", i18n.T(tl, "hub-setup.flag_listen"))
	flags.StringVar(&o.tls, "tls", ":18662", i18n.T(tl, "hub-setup.flag_tls"))
	flags.BoolVar(&o.outside, "outside", false, i18n.T(tl, "hub-setup.flag_outside"))
	flags.BoolVar(&o.noService, "no-service", false, i18n.T(tl, "hub-setup.flag_no_service"))
	flags.BoolVar(&o.managerService, "manager-service", false, i18n.T(tl, "hub-setup.flag_manager_service"))
	flags.BoolVar(&o.json, "json", false, i18n.T(tl, "hub-setup.flag_json"))
	lang := flags.String("lang", "", i18n.T(tl, "hub-setup.flag_lang"))
	if err := flags.Parse(args); err != nil {
		return err
	}
	// From here on, what setup says is in the language it prints in (setupLang); an unknown --lang is not one.
	if o.lang = i18n.Lang(*lang); o.lang != "" && !i18n.Valid(o.lang) {
		return errors.New(i18n.T(setupLang("", identity.Getenv), "hub-setup.lang_invalid"))
	}
	l := setupLang(o.lang, identity.Getenv)
	if flags.NArg() != 0 {
		return errors.New(i18n.T(l, "hub-setup.no_arguments"))
	}
	if o.noService && o.managerService {
		return errors.New(i18n.T(l, "hub-setup.manager_service_conflict"))
	}
	flags.Visit(func(f *flag.Flag) {
		o.listenSet = o.listenSet || f.Name == "listen"
		o.tlsSet = o.tlsSet || f.Name == "tls"
	})
	for _, a := range []string{o.listen, o.tls} {
		if _, err := doctorPort(a); err != nil {
			return errors.New(i18n.T(l, "hub-setup.listen_flag_invalid", i18n.Args{"address": strconv.Quote(a)}))
		}
	}
	e, err := newSetupEnv(stdout, o)
	if err != nil {
		return err
	}
	return e.setup(ctx)
}

// setupEnv is everything setup touches outside its own files, so that tests swap them out.
type setupEnv struct {
	home, root string
	goos       string
	uid        int
	user       string
	out        io.Writer
	o          setupOptions
	// l is the language setup prints in (setupLang); --json's detail and next are worded in it too.
	l i18n.Lang

	run      func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
	runTTY   func(ctx context.Context, name string, args ...string) error // sudo: the password prompt needs the terminal
	lookPath func(string) (string, error)
	ask      func(question string) (yes, asked bool)
	lanIPv4  func() string
	portFree func(port int) bool
	exe      func() (string, error)
	pingDB   func(ctx context.Context, url string) error
	doctor   func(ctx context.Context, dir string, outside bool, l i18n.Lang) []string
	getenv   func(string) string
	sleep    func(time.Duration)

	isContainer func() bool
	// restartHub: config changed what a running hub reads only at start, so service restarts it; restartWhy says what.
	restartHub bool
	restartWhy []string
	// hubRestart restarts the hub's service again (set by service when the hub runs as it): undoing a switch to the
	// built-in manager's own service that could not be finished. switched: this run set assistant.mode service on a
	// hub.json a hub had run with (--manager-service); prevMode is what it was ("" for none).
	hubRestart func(ctx context.Context) error
	switched   bool
	prevMode   string
	// builtinCode makes a one-time code that binds the built-in manager's own service (ManagerCode's); voidCode voids
	// it when the manager's program did not trade it.
	builtinCode func(ctx context.Context, cfg *Config) (string, error)
	voidCode    func(ctx context.Context, cfg *Config, code string) error
	// keptMachines: machine → the manager that keeps it, as the hub's database has it (keptMachines).
	keptMachines func(ctx context.Context, cfg *Config) (map[string]string, error)

	healthWait  time.Duration
	dbWait      time.Duration
	managerWait time.Duration // for the built-in manager's service to come online
	// The database setup creates; another name only for tests and trial runs (_PERSONA_SETUP_DB_CONTAINER…).
	dbContainer, dbVolume string
	dbPort                int
}

func newSetupEnv(out io.Writer, o setupOptions) (*setupEnv, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	root, err := identity.PrepareHome(home)
	if err != nil {
		return nil, err
	}
	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	e := &setupEnv{
		home: home, root: root, goos: runtime.GOOS, uid: os.Getuid(), user: name, out: out, o: o,
		l:           setupLang(o.lang, identity.Getenv),
		run:         runCommand,
		runTTY:      runOnTerminal,
		lookPath:    exec.LookPath,
		ask:         askTerminal,
		lanIPv4:     defaultLANIPv4,
		portFree:    tcpPortFree,
		exe:         ServerBinary, // persona-hub setup installs the personad beside it (the service: splitHub)
		pingDB:      pingPostgres,
		doctor:      setupDoctor,
		getenv:      identity.Getenv,
		sleep:       time.Sleep,
		isContainer: inContainer,
		builtinCode: func(ctx context.Context, cfg *Config) (string, error) {
			code, _, err := builtinCode(ctx, cfg)
			return code, err
		},
		voidCode:     voidBuiltinCode,
		keptMachines: keptMachines,

		healthWait:  90 * time.Second,
		dbWait:      30 * time.Second,
		managerWait: 90 * time.Second,
		dbContainer: setupDBContainer, dbVolume: setupDBVolume, dbPort: setupDBPort,
	}
	if o.json {
		e.ask = func(string) (bool, bool) { return false, false } // nobody to ask: the caller is a program
	}
	// Hidden, for trying setup out next to a real hub on the same machine (never in docs for users).
	if v := os.Getenv("_PERSONA_SETUP_DB_CONTAINER"); v != "" {
		e.dbContainer, e.dbVolume = v, v+"-data"
	}
	if v, err := strconv.Atoi(os.Getenv("_PERSONA_SETUP_DB_PORT")); err == nil && v > 0 && v < 65536 {
		e.dbPort = v
	}
	return e, nil
}

// setupLang is the language setup prints in (#5520): the installer's (--lang, else PERSONA_INSTALL_LANG, which
// install.sh sets from the language it ran in), else the terminal's.
func setupLang(flag i18n.Lang, getenv func(string) string) i18n.Lang {
	if flag != "" {
		return flag
	}
	if l := i18n.Lang(getenv("PERSONA_INSTALL_LANG")); i18n.Valid(l) {
		return l
	}
	return i18n.Terminal()
}

// --- output ---------------------------------------------------------------------------------------------------

// setupStepNames are the catalog keys of the steps' names, by the step's machine name (--json's "step").
var setupStepNames = map[string]string{
	"config": "hub-setup.step_config", "database": "hub-setup.step_database", "service": "hub-setup.step_service",
	"health": "hub-setup.step_health", "manager": "hub-setup.step_manager", "doctor": "hub-setup.step_doctor",
}

// report prints a step's line; detail and next are worded in e.l already. --json keeps step and ok as they are, for
// programs, and carries detail and next as they read.
func (e *setupEnv) report(step string, ok bool, detail, next string) {
	if e.o.json {
		b, _ := marshalNoEscape(map[string]any{"step": step, "ok": ok, "detail": detail, "next": next})
		_, _ = e.out.Write(append(b, '\n'))
		return
	}
	args := i18n.Args{"step": i18n.T(e.l, setupStepNames[step]), "detail": detail}
	if ok {
		fmt.Fprintln(e.out, i18n.T(e.l, "hub-setup.line_ok", args))
	} else {
		fmt.Fprintln(e.out, i18n.T(e.l, "hub-setup.line_problem", args))
	}
	if next != "" {
		fmt.Fprintln(e.out, "  "+i18n.T(e.l, "hub-setup.line_next", i18n.Args{"next": next}))
	}
}

// sep joins further clauses onto a step's detail or next ("；" in zh-Hans).
func (e *setupEnv) sep() string { return i18n.T(e.l, "hub-setup.sep") }

// errText is err's text for a step's line: a catalog error (i18n.Err) reads in e.l, any other as it is.
func (e *setupEnv) errText(err error) string {
	var m *i18n.Message
	if errors.As(err, &m) && err.Error() == m.Error() {
		return m.Text(e.l)
	}
	return err.Error()
}

// fail reports a step that stops setup.
func (e *setupEnv) fail(step, detail, next string) error {
	e.report(step, false, detail, next)
	return ErrSetupStopped
}

// --- the steps ------------------------------------------------------------------------------------------------

func (e *setupEnv) setup(ctx context.Context) error {
	if !e.o.json {
		fmt.Fprintln(e.out, i18n.T(e.l, "hub-setup.title"))
	}
	cfg, err := e.config()
	if err != nil {
		return err
	}
	if err := e.database(ctx, cfg); err != nil {
		return err
	}
	if err := e.localMachine(ctx, cfg); err != nil {
		return err
	}
	if err := e.service(ctx, cfg); err != nil {
		return err
	}
	if err := e.health(ctx, cfg); err != nil {
		return err
	}
	if err := e.builtinManager(ctx, cfg); err != nil {
		return err
	}
	problems := e.doctor(ctx, e.root, e.o.outside, e.l)
	for _, p := range problems {
		e.report("doctor", false, p, "")
	}
	if len(problems) == 0 {
		e.report("doctor", true, i18n.T(e.l, "hub-setup.doctor_ok"), "")
	}
	return e.finish(ctx, cfg)
}
