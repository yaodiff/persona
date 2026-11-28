package settle

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// A `tool` call goes on without its folder; secret_store, which reads files named relative to it, and every other
// command do not.
func TestGoesOn(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"persona-node", "tool", "a2a_read", `{"machine_id":"m","session_id":"s"}`}, true},
		{[]string{"personad", "tool", "a2a_send", "-"}, true},
		{[]string{"personad", "tool", "--cli", "example", "list"}, true},
		{[]string{"personad", "tool"}, true}, // a usage mistake, answered without the folder
		{[]string{"personad", "tool", "secret_store", "--from-file", "manifest.json"}, false},
		{[]string{"personad", "hook"}, false},
		{[]string{"personad", "mcp"}, false},
		{[]string{"personad", "launch", "work"}, false},
		{[]string{"personad", "ask", "tool"}, false},
		{[]string{"personad"}, false},
		{nil, false},
	} {
		if got := goesOn(c.args); got != c.want {
			t.Errorf("goesOn(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// calls records what settle does, and fails a change of directory with the errors given first.
type calls struct {
	done []string
	errs []error
}

func (c *calls) chdir(dir string) error {
	c.done = append(c.done, "chdir "+dir)
	if len(c.errs) == 0 {
		return nil
	}
	err := c.errs[0]
	c.errs = c.errs[1:]
	return err
}

func (c *calls) setenv(key, value string) error {
	c.done = append(c.done, "setenv "+key+"="+value)
	return nil
}

func TestSettle(t *testing.T) {
	const left = "/work/project"
	eintr := &fs.PathError{Op: "chdir", Path: left, Err: syscall.EINTR}
	for _, c := range []struct {
		name string
		args []string
		left string
		errs []error
		want []string
	}{
		{"the folder answered", []string{"personad", "hook"}, "", nil, nil},
		{"a tool call goes on from /", []string{"personad", "tool", "a2a_read", "{}"}, left, nil, nil},
		{"anything else goes back", []string{"personad", "hook"}, left, nil, []string{"chdir " + left, "setenv PWD=" + left}},
		{"secret_store goes back", []string{"personad", "tool", "secret_store", "--from-file", "m.json"}, left, nil,
			[]string{"chdir " + left, "setenv PWD=" + left}},
		// Going back waits for the folder, through every EINTR, as os's start did.
		{"going back waits", []string{"persona-node", "launch", "work"}, left, []error{eintr, eintr},
			[]string{"chdir " + left, "chdir " + left, "chdir " + left, "setenv PWD=" + left}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := &calls{errs: c.errs}
			if err := settle(c.args, c.left, got.chdir, got.setenv); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got.done, c.want) {
				t.Fatalf("calls %q, want %q", got.done, c.want)
			}
		})
	}
	// A folder that answers again but cannot be opened (it was removed meanwhile) fails the command, naming the
	// folder, rather than running it from "/".
	gone := &fs.PathError{Op: "chdir", Path: left, Err: syscall.ENOENT}
	got := &calls{errs: []error{gone}}
	err := settle([]string{"/opt/persona/bin/personad", "launch", "work"}, left, got.chdir, got.setenv)
	if err == nil || !strings.HasPrefix(err.Error(), "personad: the folder it was started in ("+left+")") ||
		!strings.HasSuffix(err.Error(), gone.Error()) {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(got.done, []string{"chdir " + left}) {
		t.Fatalf("calls %q", got.done)
	}
	for args, want := range map[string]string{"personad": "personad", `C:\persona\persona-node.exe`: "persona-node.exe", "": ""} {
		if got := program([]string{args}); got != want {
			t.Errorf("program(%q) = %q, want %q", args, got, want)
		}
	}
	if got := program(nil); got != "persona" {
		t.Errorf("program(nil) = %q", got)
	}
}

// Every program with the device's commands has the two packages: personad, persona-node and persona-manager all run
// `tool` (internal/nodecmd, which imports this package).
func TestDeviceProgramsHaveIt(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	const self = "github.com/nwverse/persona/server/internal/startdir/settle"
	cmd := exec.Command(goBin, "list", "-f", "{{.ImportPath}} {{join .Deps \" \"}}", "../../../cmd/personad",
		"../../../cmd/persona-node", "../../../cmd/persona-manager")
	cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		t.Fatalf("go list printed %d programs:\n%s", len(lines), out)
	}
	for _, line := range lines {
		program, deps, _ := strings.Cut(line, " ")
		if !slices.Contains(strings.Fields(deps), self) {
			t.Errorf("%s does not link %s: started in a folder that does not answer, its `tool` calls would wait for it",
				filepath.Base(program), self)
		}
	}
}
