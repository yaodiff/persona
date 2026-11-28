//go:build unix

package node

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func workspaceFetchShim(t *testing.T, fetchBody string) {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKSPACE_FETCH_REAL_GIT", git)
	bin := workspaceTemp(t)
	script := `#!/bin/sh
set -eu
case " $* " in
	*" ls-remote "*)
		if [ -n "${WORKSPACE_FETCH_REMOTE_REFS:-}" ]; then
			printf '%s\n' "$WORKSPACE_FETCH_REMOTE_REFS"
			exit 0
		fi
		;;
	*" fetch "*)
		` + fetchBody + `
		;;
esac
exec "$WORKSPACE_FETCH_REAL_GIT" "$@"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestBackendWorkspaceFetchFailureCodes(t *testing.T) {
	missingCommit := repository.RequestedRef{Kind: "commit", Value: strings.Repeat("a", 40)}
	for _, tc := range []struct {
		name, fetchBody, code string
		ref                   repository.RequestedRef
	}{
		{"fetch failure", "exit 128", "source_fetch_failed", repository.RequestedRef{Kind: "default"}},
		{"missing commit", ":", "ref_not_found", missingCommit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			plan := workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "main"))
			plan.Source.Repositories.Repositories[0].RequestedRef = tc.ref
			plan = *workspacenaming.Resolve(plan.Source, plan.Node).Plan
			req := taskworkspace.Request{WorkspaceID: "ws_fetch", OperationID: "op_fetch", Plan: plan}
			workspaceFetchShim(t, tc.fetchBody)
			got := workspaceCall(t, n, "prepare", req)
			if got.State != "failed" || got.Error == nil || got.Error.Code != tc.code ||
				len(got.Repositories) != 1 || got.Repositories[0].Error == nil || got.Repositories[0].Error.Code != tc.code {
				t.Fatalf("prepare error = %v; want %s", got.Error, tc.code)
			}
			if _, err := os.Lstat(got.Repositories[0].CommonDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed fetch left the incomplete bare directory: %v", err)
			}
		})
	}
}

func TestBackendWorkspaceSourceFetchTimeoutTiers(t *testing.T) {
	const upstream = "https://github.com/example/web.git"
	e := &workspaceExecutor{}
	if got := e.sourceFetchTimeout(upstream, true); got != 10*time.Minute {
		t.Fatalf("cold network fetch budget = %v", got)
	}
	e.coldFetchTimeout = 100 * time.Millisecond
	for _, tc := range []struct {
		name, transport string
		fresh           bool
		timeout         time.Duration
	}{
		{"cold network", upstream, true, e.coldFetchTimeout},
		{"incremental network", upstream, false, 2 * time.Minute},
		{"local mirror path", "/example/mirror.git", true, 2 * time.Minute},
		{"local file URL", "file:///example/source.git", true, 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.sourceFetchTimeout(tc.transport, tc.fresh); got != tc.timeout {
				t.Fatalf("fetch budget = %v; want %v", got, tc.timeout)
			}
		})
	}
}

func TestBackendWorkspaceColdCommitFetchDeadline(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	plan := workspacePlan(t, home, repository.LayoutRoot, "https://github.com/example/web.git")
	commit := strings.Repeat("a", 40)
	plan.Source.Repositories.Repositories[0].RequestedRef = repository.RequestedRef{Kind: "commit", Value: commit}
	plan = *workspacenaming.Resolve(plan.Source, plan.Node).Plan
	t.Setenv("WORKSPACE_FETCH_REMOTE_REFS", commit+"\tHEAD")
	workspaceFetchShim(t, "exec sleep 30")
	e := &workspaceExecutor{
		n: n, path: filepath.Join(workspaceTemp(t), "receipt.json"), coldFetchTimeout: 100 * time.Millisecond,
		journal: workspaceJournal{
			Record:     taskworkspace.Record{Plan: plan, OperationID: "op_timeout"},
			Identities: make(map[string]string),
		},
	}
	got := e.prepare(t.Context())
	if got.State != "failed" || got.Error == nil || got.Error.Code != "source_fetch_timeout" ||
		len(got.Repositories) != 1 || got.Repositories[0].Error == nil || got.Repositories[0].Error.Code != "source_fetch_timeout" {
		t.Fatalf("prepare error = %v; want source_fetch_timeout", got.Error)
	}
	if _, err := os.Lstat(got.Repositories[0].CommonDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out fetch left the incomplete bare directory: %v", err)
	}
}
