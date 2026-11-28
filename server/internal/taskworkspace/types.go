// Package taskworkspace defines the opt-in backend preparation contract. Legacy
// execution workspaces and engine workspace IDs retain their existing meanings.
package taskworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

const Capability = nodeproto.TaskWorkspace
const MethodPrefix = "task.workspace."

// The preparation budgets live in the repository contract, so contract packages can read them too.
const (
	GitTimeout       = repository.TaskWorkspaceGitTimeout
	ColdFetchTimeout = repository.TaskWorkspaceColdFetchTimeout
	RPCTimeout       = repository.TaskWorkspaceRPCTimeout
)

// PrepareTimeout budgets ref discovery and a cold fetch for each sequential
// remote source, with time for local setup and the reply.
func PrepareTimeout(remoteRepos int) time.Duration {
	return repository.TaskWorkspacePrepareTimeout(remoteRepos)
}

// Capabilities is the response to the read-only task.workspace.capabilities probe.
type Capabilities = nodeproto.TaskWorkspaceCapabilities

// LocalSourceMethod inspects a source directory without consulting a terminal host.
const LocalSourceMethod = "workspace.naming.local-source"

type LocalSourceRequest = repository.LocalSourceRequest
type LocalSource = repository.LocalSource

type Problem struct {
	Code        string   `json:"code"`
	Stage       string   `json:"stage"`
	RepoID      string   `json:"repo_id,omitempty"`
	Path        string   `json:"path,omitempty"`
	OperationID string   `json:"operation_id"`
	Actions     []string `json:"actions"`
}

func (p *Problem) Error() string { return "task_workspace_" + p.Code }
func Failure(code, stage, operation, repo, path string) *Problem {
	actions := []string{"inspect", "retry", "cancel"}
	if code == "operation_unknown" || code == "operation_busy" {
		actions = []string{"inspect"}
	}
	if code == "operation_conflict" || code == "resource_conflict" {
		actions = []string{"inspect", "cancel", "cleanup", "prepare_new"}
	}
	if code == "ref_not_found" || code == "ref_invalid" || code == "invalid_path" || code == "path_symlink" || code == "base_scope_denied" || code == "path_conflict" || code == "naming_incomplete" {
		actions = []string{"inspect", "cancel", "edit_inputs", "prepare_new"}
	}
	if stage == "start" && (code == "unsupported" || code == "engine_start_failed") {
		actions = []string{"inspect", "retry_start", "switch_engine"}
	}
	if stage == "start" && code == "result_mismatch" {
		actions = []string{"inspect", "stop_session"}
	}
	return &Problem{code, stage, repo, path, operation, actions}
}

type Instance struct {
	RepoID         string                           `json:"repo_id"`
	Input          repository.PreparationRepository `json:"input"`
	RelativePath   string                           `json:"relative_path"`
	Path           string                           `json:"path"`
	CommonDir      string                           `json:"common_dir"`
	TaskBranch     string                           `json:"task_branch"`
	ResolvedRef    string                           `json:"resolved_ref,omitempty"`
	ResolvedCommit string                           `json:"resolved_commit,omitempty"`
	State          string                           `json:"state"`
	CurrentHead    string                           `json:"current_head,omitempty"`
	Dirty          bool                             `json:"dirty"`
	Removed        bool                             `json:"removed"`
	BranchDeleted  bool                             `json:"branch_deleted"`
	Error          *Problem                         `json:"error,omitempty"`
}
type CleanupResult struct {
	Removed       bool     `json:"removed"`
	RetainedPaths []string `json:"retained_paths"`
}

// Launch is the Node receipt for the one session using a backend workspace.
type Launch struct {
	ExpectedSessionID string `json:"expected_session_id,omitempty"`
	UseID             string `json:"use_id"`
	SessionID         string `json:"session_id,omitempty"`
	TaskDispatch      bool   `json:"task_dispatch,omitempty"`
}
type Record struct {
	Launch              *Launch              `json:"launch,omitempty"`
	SchemaVersion       int                  `json:"schema_version"`
	WorkspaceID         string               `json:"workspace_id"`
	OperationID         string               `json:"operation_id"`
	NodeOperationID     string               `json:"node_operation_id,omitempty"`
	PreviousOperationID string               `json:"previous_operation_id,omitempty"`
	Plan                workspacenaming.Plan `json:"plan"`
	InputDigest         string               `json:"input_digest"`
	State               string               `json:"state"`
	Repositories        []Instance           `json:"repositories"`
	Revision            int64                `json:"revision"`
	Error               *Problem             `json:"error,omitempty"`
	Cleanup             *CleanupResult       `json:"cleanup,omitempty"`
	Uses                []string             `json:"uses"`
}
type Request struct {
	WorkspaceID         string               `json:"workspace_id"`
	OperationID         string               `json:"operation_id"`
	PreviousOperationID string               `json:"previous_operation_id,omitempty"`
	Plan                workspacenaming.Plan `json:"plan"`
	Force               bool                 `json:"force,omitempty"`
	DeleteBranch        bool                 `json:"delete_branch,omitempty"`
	UseID               string               `json:"use_id,omitempty"`
	// TaskDispatch preserves ordinary task launcher and terminal placement.
	// It is sent only on start and is excluded from preparation identity.
	TaskDispatch bool `json:"task_dispatch,omitempty"`
	// MirrorRepos names repo IDs in Plan that have an opt-in managed bare mirror enabled on this Node.
	// Transient per-call hint only; excluded from InputKey and backward-compatible with older Nodes.
	MirrorRepos      []string `json:"mirror_repos,omitempty"`
	MirrorTTLSeconds int      `json:"mirror_ttl_seconds,omitempty"`
}

func (r Record) Request() Request {
	return Request{WorkspaceID: r.WorkspaceID, OperationID: r.OperationID, PreviousOperationID: r.PreviousOperationID, Plan: r.Plan}
}
func Hash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// InputKey excludes presentation and the planning clock; physical and source
// choices remain part of the identity. A retry always keeps the original Plan.
func InputKey(p workspacenaming.Plan) string {
	return Hash(struct {
		Manager, Todo, Machine, Root string
		Layout                       repository.Layout
		Repositories                 []workspacenaming.RepositoryPlan
		Sources                      []repository.PreparationRepository
	}{p.Source.Manager, p.Source.TodoID, p.Source.MachineID, p.RootPath, p.Source.Layout, p.Repositories, p.Source.Repositories.Repositories})
}
func ValidRequest(r Request) bool {
	if !workspacenaming.ValidOperationID(r.WorkspaceID) || !workspacenaming.ValidOperationID(r.OperationID) {
		return false
	}
	if r.PreviousOperationID != "" && !workspacenaming.ValidOperationID(r.PreviousOperationID) {
		return false
	}
	return workspacenaming.CheckPlan(r.Plan) == nil && r.Plan.Source.Manager != "" && r.Plan.Source.MachineID != "" &&
		len(r.Plan.Repositories) <= repository.MaxTaskRepositories
}

// GitEnv keeps the Node's credential helpers and SSH configuration, but removes
// repository locators which could redirect a command into an unrelated checkout.
func GitEnv(env []string) []string {
	out := make([]string, 0, len(env)+8)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "GIT_CONFIG") || strings.HasPrefix(key, "GIT_TRACE") {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SHALLOW_FILE", "GIT_CEILING_DIRECTORIES", "GIT_OPTIONAL_LOCKS", "GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "LC_ALL", "SSH_ASKPASS", "GCM_INTERACTIVE":
			continue
		}
		out = append(out, item)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GCM_INTERACTIVE=never", "LC_ALL=C")
}

// ValidateReady rejects incomplete or mismatched successful results at the
// Manager persistence boundary. Node inspection owns physical Git attestation.
func ValidateReady(r Record) error {
	if r.State != "ready" {
		return nil
	}
	if len(r.Repositories) != len(r.Plan.Repositories) {
		return Failure("result_mismatch", "manager", r.OperationID, "", r.Plan.RootPath)
	}
	seen := map[string]bool{}
	for _, instance := range r.Repositories {
		if seen[instance.RepoID] {
			return Failure("result_mismatch", "manager", r.OperationID, instance.RepoID, instance.Path)
		}
		seen[instance.RepoID] = true
		matched := false
		for i, p := range r.Plan.Repositories {
			if instance.RepoID != p.RepoID {
				continue
			}
			_, err := repository.NormalizeRef(repository.RequestedRef{Kind: "commit", Value: instance.ResolvedCommit})
			matched = err == nil && instance.State == "ready" && !instance.Removed && instance.Path == p.AbsolutePath && instance.RelativePath == p.RelativePath && instance.TaskBranch == p.TaskBranch && instance.CommonDir != "" && Hash(instance.Input) == Hash(r.Plan.Source.Repositories.Repositories[i])
			if local := instance.Input.Local; local != nil {
				matched = matched && instance.CommonDir == local.CommonDir && instance.ResolvedCommit == local.Commit
			}
		}
		if !matched {
			return Failure("result_mismatch", "manager", r.OperationID, instance.RepoID, instance.Path)
		}
	}
	return nil
}
