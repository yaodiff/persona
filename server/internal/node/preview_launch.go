package node

// The dev servers a preview can show (docs/web/preview.md "Servers"): the entries of the project's .claude/launch.json, in
// the shape Claude Desktop reads (configurations[]: name, runtimeExecutable, runtimeArgs, port, optional url, cwd and
// env), each with whether something listens on its port now. preview.start runs one of them in a new terminal tab.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// launchFile is where a project keeps its dev servers, under its folder.
const launchFile = ".claude/launch.json"

// launchFileMax: a launch.json bigger than this is not one.
const launchFileMax = 256 << 10

// previewServer is one entry of launch.json as the panel shows it.
type previewServer struct {
	Name string `json:"name"`
	Port int    `json:"port,omitempty"`
	// URL is the entry's own address (a page of its server), "" for http://localhost:<port>/.
	URL string `json:"url,omitempty"`
	// Command is what Start runs, the program first; empty for an entry that only attaches to a running server.
	Command []string `json:"command,omitempty"`
	// Env are the entry's variables, shown with the command.
	Env map[string]string `json:"env,omitempty"`
	// Cwd is where it runs.
	Cwd       string `json:"cwd,omitempty"`
	Listening bool   `json:"listening"`
	Title     string `json:"title,omitempty"`
	// Problem says why the entry cannot be used (a code, docs/web/preview.md); the others are empty then.
	Problem string `json:"problem,omitempty"`
}

// previewServers is preview.servers' answer.
type previewServers struct {
	// File is the launch.json read, "" when the folder and the ones above it up to the repository's root have none.
	File    string          `json:"file,omitempty"`
	Servers []previewServer `json:"servers"`
	// Error is why File could not be used (its JSON, its size).
	Error string `json:"error,omitempty"`
	// Browser is the browser previews open in, "" when this computer has none (docs/web/preview.md "The browser").
	Browser string `json:"browser,omitempty"`
}

// Problems of a launch.json entry.
const (
	problemNoName   = "no_name"
	problemNoPort   = "no_port"   // neither a port nor a local url with one
	problemNotLocal = "not_local" // its url is another computer's: a preview shows this one's
	problemNoRun    = "no_run"    // nothing to run and no url to attach to
	problemBadEnv   = "bad_env"   // an env name that is not one
)

var launchEnvName = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]*$`)

// findLaunchFile is the launch.json for a session in dir: dir's own, else the nearest folder above it up to the
// repository's root (or the home folder) that has one.
func findLaunchFile(dir string) string {
	home, _ := os.UserHomeDir()
	for i := 0; i < 12 && dir != ""; i++ {
		p := filepath.Join(dir, launchFile)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil || dir == home {
			return ""
		}
		up := filepath.Dir(dir)
		if up == dir {
			return ""
		}
		dir = up
	}
	return ""
}

// launchEntry is a configuration as written.
type launchEntry struct {
	Name              string            `json:"name"`
	RuntimeExecutable string            `json:"runtimeExecutable"`
	RuntimeArgs       []string          `json:"runtimeArgs"`
	Port              json.Number       `json:"port"`
	URL               string            `json:"url"`
	Cwd               string            `json:"cwd"`
	Env               map[string]string `json:"env"`
}

// readLaunchFile reads the entries of a launch.json at path (its folder's parent is the project's root). The entries
// are as written, a Problem on those it cannot use; nothing is probed.
func readLaunchFile(path string) ([]previewServer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, launchFileMax+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > launchFileMax {
		return nil, fmt.Errorf("%s is over %d KB", launchFile, launchFileMax>>10)
	}
	var doc struct {
		Configurations []json.RawMessage `json:"configurations"`
	}
	dec := json.NewDecoder(bytes.NewReader(jsonc(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %v", launchFile, err)
	}
	root := filepath.Dir(filepath.Dir(path))
	out := []previewServer{}
	for _, c := range doc.Configurations {
		var e launchEntry
		d := json.NewDecoder(bytes.NewReader(c))
		d.UseNumber()
		if err := d.Decode(&e); err != nil {
			s := previewServer{Name: entryName(c), Problem: problemNoRun}
			if s.Name == "" {
				s.Problem = problemNoName
			}
			out = append(out, s)
			continue
		}
		out = append(out, launchServer(root, e))
	}
	return out, nil
}

// entryName is an entry's name when the rest of it is not what launch.json takes.
func entryName(raw json.RawMessage) string {
	var e struct {
		Name any `json:"name"`
	}
	_ = json.Unmarshal(raw, &e)
	s, _ := e.Name.(string)
	return strings.TrimSpace(s)
}

func launchServer(root string, e launchEntry) previewServer {
	s := previewServer{Name: strings.TrimSpace(e.Name)}
	if s.Name == "" {
		s.Problem = problemNoName
		return s
	}
	if p, err := strconv.Atoi(e.Port.String()); err == nil {
		s.Port = p
	}
	if u := strings.TrimSpace(e.URL); u != "" {
		pu, err := url.Parse(u)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || !loopbackHost(pu.Hostname()) {
			s.Problem = problemNotLocal
			return s
		}
		if p, err := strconv.Atoi(pu.Port()); err == nil && s.Port == 0 {
			s.Port = p
		}
		s.URL = pu.String()
	}
	if s.Port <= 0 || s.Port > 65535 {
		s.Problem, s.Port = problemNoPort, 0
		return s
	}
	if exe := strings.TrimSpace(e.RuntimeExecutable); exe != "" {
		for k := range e.Env {
			if !launchEnvName.MatchString(k) {
				s.Problem = problemBadEnv
				return s
			}
		}
		s.Command = append([]string{exe}, e.RuntimeArgs...)
		s.Env = e.Env
		s.Cwd = root
		if c := strings.TrimSpace(e.Cwd); c != "" {
			c = strings.ReplaceAll(c, "${workspaceFolder}", root)
			if !filepath.IsAbs(c) {
				c = filepath.Join(root, c)
			}
			s.Cwd = filepath.Clean(c)
		}
	} else if s.URL == "" {
		s.Problem = problemNoRun
	}
	return s
}

var (
	jsoncComment = lazyre.New(`(?s)"(?:[^"\\]|\\.)*"|//[^\n]*|/\*.*?\*/`)
	jsoncComma   = lazyre.New(`(?s)"(?:[^"\\]|\\.)*"|,(\s*[\]}])`)
)

// jsonc is a JSON-with-comments text (what VS Code writes launch.json as) as JSON: comments and trailing commas out,
// strings untouched.
func jsonc(b []byte) []byte {
	b = jsoncComment.ReplaceAllFunc(b, func(m []byte) []byte {
		if m[0] == '"' {
			return m
		}
		return nil
	})
	return jsoncComma.ReplaceAllFunc(b, func(m []byte) []byte {
		if m[0] == '"' {
			return m
		}
		return m[1:]
	})
}

// loopbackHost: an address of this computer only (localhost, *.localhost, 127.0.0.0/8, ::1).
func loopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// previewServersRPC is preview.servers: launch.json's entries for a session's folder, probed.
func (n *Node) previewServersRPC(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Cwd string `json:"cwd"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Cwd == "" || !filepath.IsAbs(p.Cwd) {
		return nil, errors.New("preview.servers: need an absolute cwd")
	}
	res := previewServers{Servers: []previewServer{}}
	if b, err := findBrowser(); err == nil {
		res.Browser = b.name
	}
	res.File = findLaunchFile(filepath.Clean(p.Cwd))
	if res.File == "" {
		return res, nil
	}
	servers, err := readLaunchFile(res.File)
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	pctx, cancel := context.WithTimeout(ctx, portProbeWait+time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range servers {
		if servers[i].Problem != "" {
			continue
		}
		wg.Add(1)
		go func(s *previewServer) {
			defer wg.Done()
			pr := probePort(pctx, s.Port, "")
			s.Listening, s.Title = pr.http || pr.title != "", pr.title
			if !s.Listening { // something that is not HTTP (yet): it listens all the same
				if c, err := dialPort(pctx, s.Port, ""); err == nil {
					c.Close()
					s.Listening = true
				}
			}
		}(&servers[i])
	}
	wg.Wait()
	sort.SliceStable(servers, func(i, j int) bool { return servers[i].Problem == "" && servers[j].Problem != "" })
	res.Servers = servers
	return res, nil
}

// previewStartRPC is preview.start: run a launch.json entry in a new terminal tab of the session's workspace, named
// after it, so its output is there to read and Ctrl-C stops it. The panel showed the command and the user confirmed
// it; the node runs the entry as the file says now, the program and its arguments each quoted (no shell string).
func (n *Node) previewStartRPC(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Cwd       string `json:"cwd"`
		Name      string `json:"name"`
		Workspace string `json:"workspace"`
		Kind      string `json:"kind"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Cwd == "" || p.Name == "" || p.Workspace == "" {
		return nil, errors.New("preview.start: need cwd, name and workspace")
	}
	runtimeSpace := n.isRuntimeWorkspace(p.Workspace)
	file := findLaunchFile(filepath.Clean(p.Cwd))
	if file == "" {
		return nil, fmt.Errorf("no %s for %s", launchFile, p.Cwd)
	}
	servers, err := readLaunchFile(file)
	if err != nil {
		return nil, err
	}
	var s *previewServer
	for i := range servers {
		if servers[i].Name == p.Name {
			s = &servers[i]
			break
		}
	}
	switch {
	case s == nil:
		return nil, fmt.Errorf("%s has no entry %q", launchFile, p.Name)
	case s.Problem != "":
		return nil, fmt.Errorf("entry %q cannot be used: %s", p.Name, s.Problem)
	case len(s.Command) == 0:
		return nil, fmt.Errorf("entry %q only opens %s: start its server yourself", p.Name, s.URL)
	}
	if runtimeSpace {
		// A runtime's workspace (a Paseo session's workspace_id) has no terminal: the server runs in the terminal
		// engine's workspace that holds its folder, else in a new workspace there; never in one that only happens to be
		// first.
		if p.Kind, p.Workspace = n.launchSpaceHolding(ctx, s.Cwd); p.Workspace == "" {
			launch, created, err := n.launchSpace(ctx, "", s.Cwd, s.Name)
			switch {
			case errors.Is(err, errNoLauncher):
				return nil, refusal(codePreviewNoTerminal, i18n.T(i18n.Default, "rpc-errors."+codePreviewNoTerminal), nil)
			case err != nil:
				return nil, fmt.Errorf("could not open a terminal tab: %w", err)
			}
			return n.runPreview(ctx, launch, created, s)
		}
	}
	var launch host.Launcher
	for _, h := range n.hosts.All() {
		if l, ok := h.(host.Launcher); ok && (p.Kind == "" || h.Kind() == p.Kind) {
			if _, has := n.spaceOf(h.Kind(), p.Workspace); has {
				launch = l
				break
			}
		}
	}
	if launch == nil {
		return nil, fmt.Errorf("workspace %s is not on a host that can open tabs", p.Workspace)
	}
	created, err := launch.CreateSlot(ctx, p.Workspace, s.Cwd, tabLabel(s.Name, n.tabLabels(ctx, launch, p.Workspace)))
	if err != nil {
		return nil, fmt.Errorf("could not open a terminal tab: %w", err)
	}
	return n.runPreview(ctx, launch, created, s)
}

// codePreviewNoTerminal: a preview's server has nowhere to run, the machine enabling no terminal engine (its words
// are the hub's, in the request's language: rpc-errors.preview_no_terminal).
const codePreviewNoTerminal = "preview_no_terminal"

// runPreview runs entry s in the new tab created and answers with its pane and port.
func (n *Node) runPreview(ctx context.Context, launch host.Launcher, created *host.Created, s *previewServer) (any, error) {
	if created == nil || created.Pane.ID == "" {
		return nil, errors.New("could not open a terminal tab: the host returned no pane")
	}
	time.Sleep(300 * time.Millisecond) // the fresh shell loads its rc files; typed input is buffered either way
	if err := launch.Run(ctx, created.Pane, launchCommand(s)); err != nil {
		return nil, fmt.Errorf("could not run %s: %w", s.Command[0], err)
	}
	_ = n.refreshHosts(ctx, launch.(host.Host))
	pane := created.Pane.String()
	return map[string]any{"pane": pane, "terminal": panePrefix + pane, "port": s.Port}, nil
}

// launchCommand is the line typed to run an entry: `env VAR=val …` in front when it has variables (env, so every
// shell takes it), each word quoted.
func launchCommand(s *previewServer) string {
	var words []string
	if len(s.Env) > 0 {
		keys := make([]string, 0, len(s.Env))
		for k := range s.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		words = append(words, "env")
		for _, k := range keys {
			words = append(words, shellQuote(k+"="+s.Env[k]))
		}
	}
	for _, w := range s.Command {
		words = append(words, shellQuote(w))
	}
	return strings.Join(words, " ")
}
