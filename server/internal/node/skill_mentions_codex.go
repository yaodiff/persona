package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/osproc"
)

type codexSkillList struct {
	Data []struct {
		Cwd    string `json:"cwd"`
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Path        string `json:"path"`
			Scope       string `json:"scope"`
			Enabled     bool   `json:"enabled"`
		} `json:"skills"`
		Errors []json.RawMessage `json:"errors"`
	} `json:"data"`
}

func (n *Node) codexSkillCatalog(ctx context.Context, in skillCatalogRequest) ([]assistantskills.Entry, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out codexSkillList
	n.mu.Lock()
	p := n.procs[in.SessionID]
	n.mu.Unlock()
	cwd := n.realDir(in.Cwd)
	params := map[string]any{"cwds": []string{cwd}, "forceReload": true}
	if p != nil && p.alive() && p.cx != nil && p.launcher == in.Launcher && p.cwd == in.Cwd {
		if err := p.cx.call(ctx, "skills/list", params, &out); err != nil {
			return nil, false, err
		}
	} else if err := n.probeCodexSkills(ctx, in.Launcher, cwd, params, &out); err != nil {
		return nil, false, err
	}
	return n.codexSkillEntries(cwd, out)
}

func (n *Node) codexSkillEntries(cwd string, out codexSkillList) ([]assistantskills.Entry, bool, error) {
	entries := []assistantskills.Entry{}
	found, complete := false, true
	for _, data := range out.Data {
		if filepath.Clean(data.Cwd) != filepath.Clean(cwd) {
			continue
		}
		found = true
		complete = complete && len(data.Errors) == 0
		for _, s := range data.Skills {
			if s.Enabled && s.Name != "" && filepath.IsAbs(s.Path) && filepath.Base(s.Path) == "SKILL.md" {
				st, err := os.Stat(s.Path)
				if err != nil || !st.Mode().IsRegular() {
					complete = false
					continue
				}
				path, err := filepath.EvalSymlinks(s.Path)
				if err != nil {
					complete = false
					continue
				}
				root := filepath.Dir(filepath.Dir(s.Path))
				if rel, err := filepath.Rel(n.home, root); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					root = "~/" + filepath.ToSlash(rel)
				}
				entries = append(entries, assistantskills.Entry{Name: s.Name, Description: s.Description, Path: path,
					Source: s.Scope + " · " + root})
			}
		}
	}
	if !found {
		return nil, false, errors.New("runtime returned no skill inventory for this directory")
	}
	return entries, complete, nil
}

// probeCodexSkills initializes the selected launcher's app-server without opening a thread or running a model.
// This preserves wrapper CODEX_HOME/config/profile flags instead of guessing roots from the node's HOME.
func (n *Node) probeCodexSkills(ctx context.Context, launcher, cwd string, params map[string]any, out *codexSkillList) error {
	shell, err := interactiveShell()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, shell, shellFlag(shell), codexShell(n.launcherCommand(launcher)), "persona")
	cmd.Dir, cmd.Env = cwd, os.Environ()
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	cmd.Cancel = func() error { return osproc.StopGroup(cmd.Process.Pid, true) }
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard // a shell environment can contain private configuration; never publish its diagnostics
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		stdin.Close()
		_ = osproc.StopGroup(cmd.Process.Pid, true)
		_ = cmd.Wait()
	}()
	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 4096), 4<<20)
	encoder := json.NewEncoder(stdin)
	call := func(id int, method string, params any, target any) error {
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			return err
		}
		for reader.Scan() {
			var reply struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(reader.Bytes(), &reply) != nil || reply.ID != id {
				continue
			}
			if len(reply.Error) != 0 && string(reply.Error) != "null" {
				return errors.New("runtime could not provide the skill catalog")
			}
			if target != nil {
				return json.Unmarshal(reply.Result, target)
			}
			return nil
		}
		return errors.New("runtime skill catalog connection ended")
	}
	if err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "persona", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true}}, nil); err != nil {
		return err
	}
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized"}); err != nil {
		return err
	}
	root := filepath.Join(cwd, ".claude", "skills")
	if st, err := os.Stat(root); err == nil && st.IsDir() {
		// Match codexConn.start: Persona supplies the assistant's installed connector/built-in skills here.
		if err := call(2, "skills/extraRoots/set", map[string]any{"extraRoots": []string{root}}, nil); err != nil {
			return err
		}
	}
	return call(3, "skills/list", params, out)
}
