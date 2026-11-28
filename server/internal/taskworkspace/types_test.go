package taskworkspace

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestBackendWorkspaceCapabilityFieldsAreAdditive(t *testing.T) {
	legacy := []byte(`{"capability":"task_workspace_v1","start":true}`)
	var caps Capabilities
	if err := json.Unmarshal(legacy, &caps); err != nil || caps.Capability != Capability || !caps.Start ||
		caps.EmptyRepositories || caps.LocalGit {
		t.Fatal(caps, err)
	}
	encoded, err := json.Marshal(caps)
	if err != nil || string(encoded) != string(legacy) {
		t.Fatalf("legacy capability shape: %s, %v", encoded, err)
	}
}

func TestReadyLocalRepositoryMustMatchFrozenSource(t *testing.T) {
	local := &repositorywire.LocalCheckout{CommonDir: "/example/repository/.git", Commit: strings.Repeat("a", 40)}
	repo := repositorywire.PreparationRepository{RepoID: "local", Local: local}
	record := Record{
		State: "ready",
		Plan: workspacenaming.Plan{
			Source:       workspacenaming.Source{Repositories: repositorywire.PreparationSnapshot{Repositories: []repositorywire.PreparationRepository{repo}}},
			Repositories: []workspacenaming.RepositoryPlan{{RepoID: repo.RepoID, RelativePath: ".", AbsolutePath: "/example/workspace", TaskBranch: "task/example"}},
		},
		Repositories: []Instance{{
			RepoID: repo.RepoID, Input: repo, RelativePath: ".", Path: "/example/workspace", TaskBranch: "task/example",
			State: "ready", ResolvedCommit: local.Commit, CommonDir: local.CommonDir,
		}},
	}
	if err := ValidateReady(record); err != nil {
		t.Fatal(err)
	}
	record.Repositories[0].ResolvedCommit = strings.Repeat("b", 40)
	if err := ValidateReady(record); err == nil {
		t.Fatal("accepted an unrelated commit")
	}
	record.Repositories[0].ResolvedCommit = local.Commit
	record.Repositories[0].CommonDir = "/example/replacement/.git"
	if err := ValidateReady(record); err == nil {
		t.Fatal("accepted a replacement Git directory")
	}
}
