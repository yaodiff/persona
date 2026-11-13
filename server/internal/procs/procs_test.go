package procs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// childEnv runs the test binary as a process under test: "report" calls AtStart and prints what it found (state);
// "chain" calls AtStart, then runs two reports of its own, one with its environment as it is (what the node and the
// tools a slot or manager starts get) and one with ChildEnv (the slot's manager).
const childEnv = "PERSONA_TEST_PROCS_CHILD"

func TestMain(m *testing.M) {
	switch os.Getenv(childEnv) {
	case "report":
		os.Exit(printJSON(report()))
	case "chain":
		AtStart()
		plain, err := run(os.Environ(), "report")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		handed, err := run(ChildEnv(os.Environ()), "report")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(printJSON(chain{Plain: plain, Handed: handed}))
	}
	os.Exit(m.Run())
}

// state is one process under test: GOMAXPROCS and its variables before and after AtStart, and what ChildEnv then adds.
type state struct {
	Before, After       int
	EnvBefore, EnvAfter []string
	Child               []string
}

type chain struct{ Plain, Handed state }

func report() state {
	s := state{Before: runtime.GOMAXPROCS(0), EnvBefore: capVars(os.Environ())}
	AtStart()
	s.After, s.EnvAfter, s.Child = runtime.GOMAXPROCS(0), capVars(os.Environ()), ChildEnv(nil)
	return s
}

func printJSON(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("\nresult %s\n", b)
	return 0
}

// capVars are env's GOMAXPROCS and EnvCap entries.
func capVars(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, envMaxProcs+"=") || strings.HasPrefix(kv, EnvCap+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// run runs the test binary in mode with env (its own childEnv entry replaced) and decodes its result.
func run(env []string, mode string, prefix ...string) (s state, err error) {
	err = runInto(&s, env, mode, prefix...)
	return s, err
}

func runInto(v any, env []string, mode string, prefix ...string) error {
	env = slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, childEnv+"=") })
	args := append(slices.Clone(prefix), os.Args[0], "-test.run=^$")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(env, childEnv+"="+mode)
	out, err := cmd.Output()
	i := strings.LastIndex(string(out), "\nresult ")
	if err != nil || i < 0 {
		return fmt.Errorf("%s %s: %v\n%s", strings.Join(args, " "), mode, err, out)
	}
	return json.Unmarshal([]byte(strings.TrimSpace(string(out[i+len("\nresult "):]))), v)
}

// baseEnv is this process's environment without GOMAXPROCS or the marker, plus extra.
func baseEnv(extra ...string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return len(capVars([]string{kv})) > 0 })
	return append(env, extra...)
}

var handed = []string{"GOMAXPROCS=4", EnvCap + "=4"}

func TestAtStart(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
		// want is the expected state for a process whose runtime default (before) is what it reported.
		want func(before int) state
	}{
		{"nothing set caps above Max", nil, func(before int) state {
			if before <= Max { // a host with Max CPUs or fewer: the runtime's value stays, and nothing is handed on
				return state{Before: before, After: before}
			}
			return state{Before: before, After: Max, Child: handed}
		}},
		{"the user's GOMAXPROCS wins and stays", []string{"GOMAXPROCS=6"}, func(int) state {
			return state{Before: 6, After: 6, EnvBefore: []string{"GOMAXPROCS=6"}, EnvAfter: []string{"GOMAXPROCS=6"}}
		}},
		{"the hub's cap is kept, removed and handed on", handed, func(int) state {
			return state{Before: 4, After: 4, EnvBefore: handed, Child: handed}
		}},
		{"an unmatched marker leaves the user's GOMAXPROCS", []string{"GOMAXPROCS=6", EnvCap + "=4"}, func(int) state {
			return state{Before: 6, After: 6, EnvBefore: []string{"GOMAXPROCS=6", EnvCap + "=4"},
				EnvAfter: []string{"GOMAXPROCS=6"}}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := run(baseEnv(c.env...), "report")
			if err != nil {
				t.Fatal(err)
			}
			if want := c.want(got.Before); !equal(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		})
	}
}

// When the runtime's default is at or below the cap, AtStart leaves it (and the runtime's own updates) alone and hands
// nothing on. taskset asks for Max CPUs, but a container's cpuset can intersect that range (one that excludes CPUs 0
// and 1 leaves only two of 0-3), so the default the runtime reports may be below Max; the contract under test is the
// same at every value at or below Max.
func TestAtStartLeavesADefaultAtTheCap(t *testing.T) {
	taskset, err := exec.LookPath("taskset")
	if err != nil {
		t.Skip("no taskset:", err)
	}
	cpus := "0-" + strconv.Itoa(Max-1)
	got, err := run(baseEnv(), "report", taskset, "-c", cpus)
	if err != nil {
		t.Skip("taskset -c", cpus, "on this host:", err)
	}
	if got.Before > Max {
		t.Fatalf("taskset -c %s left %d Ps, want at most %d", cpus, got.Before, Max)
	}
	if want := (state{Before: got.Before, After: got.Before}); !equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A slot the edge started with the cap: what it starts as it is (the node, tools) sees neither variable; the manager it
// hands the cap to (ChildEnv) starts with Max Ps and takes both out again, for its own children.
func TestTheCapReachesOnlyTheHubsOwnChildren(t *testing.T) {
	var got chain
	if err := runInto(&got, baseEnv(handed...), "chain"); err != nil {
		t.Fatal(err)
	}
	if len(got.Plain.EnvBefore) != 0 {
		t.Errorf("a child started with the slot's environment sees %q", got.Plain.EnvBefore)
	}
	if want := (state{Before: 4, After: 4, EnvBefore: handed, Child: handed}); !equal(got.Handed, want) {
		t.Errorf("the manager: got %+v, want %+v", got.Handed, want)
	}
}

func TestChildEnv(t *testing.T) {
	defer func(c int) { capped = c }(capped)
	for _, c := range []struct {
		name   string
		capped int
		env    []string
		want   []string
	}{
		{"uncapped adds nothing", 0, []string{"A=1"}, []string{"A=1"}},
		{"capped adds the cap and its marker", 4, []string{"A=1"}, []string{"A=1", "GOMAXPROCS=4", EnvCap + "=4"}},
		{"a GOMAXPROCS already there wins", 4, []string{"GOMAXPROCS=2"}, []string{"GOMAXPROCS=2"}},
		{"a marker already there is left", 4, []string{EnvCap + "=4"}, []string{EnvCap + "=4"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			capped = c.capped
			if got := ChildEnv(slices.Clone(c.env)); !slices.Equal(got, c.want) {
				t.Fatalf("ChildEnv(%q) with cap %d = %q, want %q", c.env, c.capped, got, c.want)
			}
		})
	}
}

func equal(a, b state) bool {
	return a.Before == b.Before && a.After == b.After && slices.Equal(a.EnvBefore, b.EnvBefore) &&
		slices.Equal(a.EnvAfter, b.EnvAfter) && slices.Equal(a.Child, b.Child)
}
