//go:build unix

package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func TestBackendWorkspaceRemovesIncompleteBareAfterFailedFetch(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_partial", OperationID: "op_partial", Plan: workspacePlan(t, home,
		repository.LayoutContainer, workspaceRemote(t, "main"), workspaceRemote(t, "develop"))}
	storage := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID))
	partial := filepath.Join(storage, "git", taskworkspace.Hash(req.Plan.Repositories[1].RepoID)+".git")
	outside := workspaceTemp(t)
	keep := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(keep, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKSPACE_FETCH_PARTIAL", partial)
	t.Setenv("WORKSPACE_FETCH_OUTSIDE", outside)
	workspaceFetchShim(t, `
		if [ "$2" = "$WORKSPACE_FETCH_PARTIAL" ]; then
			printf 'partial pack' > "$2/objects/pack/incomplete"
			ln -s "$WORKSPACE_FETCH_OUTSIDE" "$2/objects/external"
			exit 128
		fi`)
	got := workspaceCall(t, n, "prepare", req)
	if got.Error == nil || got.Error.Code != "source_fetch_failed" || len(got.Repositories) != 2 ||
		got.Repositories[0].State != "ready" || got.Repositories[1].CommonDir != partial {
		t.Fatalf("prepare left an unexpected failure: %v", got.Error)
	}
	if _, err := os.Lstat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("incomplete private bare repository remains: %v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "outside" {
		t.Fatal("cleanup changed the symlink target", err)
	}
	receipt, err := os.ReadFile(filepath.Join(storage, "receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal workspaceJournal
	if err := json.Unmarshal(receipt, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.State != "failed" || journal.Error == nil || journal.Error.Code != "source_fetch_failed" ||
		journal.Identities[partial] != "" || journal.Identities[got.Repositories[0].CommonDir] == "" {
		t.Fatal("receipt lost the failure or the ownership of the ready source")
	}
	first := got.Repositories[0]
	marker := filepath.Join(first.Path, "keep.txt")
	if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKSPACE_FETCH_PARTIAL", "")
	retried := workspaceCall(t, n, "prepare", req)
	if retried.State != "ready" || retried.Repositories[0].ResolvedCommit != first.ResolvedCommit {
		t.Fatalf("retry failed: %v", retried.Error)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "ready" {
		t.Fatal("retry changed the ready worktree", err)
	}
}

func TestBackendWorkspaceFetchFailurePreservesPreexistingBare(t *testing.T) {
	for _, owned := range []bool{false, true} {
		name, code := "unowned", "path_conflict"
		if owned {
			name, code = "owned by a prior attempt", "source_fetch_failed"
		}
		t.Run(name, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			req := taskworkspace.Request{WorkspaceID: "ws_existing", OperationID: "op_existing",
				Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "main"))}
			storage := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID))
			common := filepath.Join(storage, "git", taskworkspace.Hash("web")+".git")
			if err := os.MkdirAll(common, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(common, "keep.txt")
			if err := os.WriteFile(marker, []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			if owned {
				id, err := workspaceFileID(common)
				if err != nil {
					t.Fatal(err)
				}
				journal := workspaceJournal{
					Record: taskworkspace.Record{SchemaVersion: 1, Plan: req.Plan, State: "preparing",
						WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, InputDigest: taskworkspace.InputKey(req.Plan)},
					Identities: map[string]string{common: id},
				}
				if err := workspaceWrite(filepath.Join(storage, "receipt.json"), &journal); err != nil {
					t.Fatal(err)
				}
			}
			workspaceFetchShim(t, "exit 128")
			if got := workspaceCall(t, n, "prepare", req); got.Error == nil || got.Error.Code != code {
				t.Fatalf("prepare error = %v; want %s", got.Error, code)
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "existing" {
				t.Fatal("cleanup changed the preexisting repository", err)
			}
		})
	}
}
