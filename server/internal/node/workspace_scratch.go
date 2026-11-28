package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

func init() {
	nodeRPC(nodeproto.MethodWorkspaceScratchPrepare, (*Node).prepareScratchWorkspaceRPC)
}

const (
	scratchDirName     = "scratch"
	scratchMetaDirName = ".persona-managed"
)

type scratchWorkspaceReceipt struct {
	SchemaVersion int       `json:"schema_version"`
	TodoID        string    `json:"todo_id"`
	Path          string    `json:"path"`
	FileID        string    `json:"file_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func (n *Node) prepareScratchWorkspaceRPC(ctx context.Context, raw json.RawMessage) (any, error) {
	var req nodeproto.ScratchWorkspaceRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, refusal("invalid_scratch_request", fmt.Sprintf("invalid scratch workspace request: %v", err), nil)
		}
	}
	return n.prepareScratchWorkspace(ctx, req)
}

// prepareScratchWorkspace validates, previews (when DryRun is true), or creates/reuses a
// node-managed isolated scratch directory under <persona-data>/scratch/<todo_id>.
//
// Security invariants:
//   - Only strict single-segment ASCII identifiers (nodeproto.ValidScratchID) are accepted.
//   - Symlinks at the managed root, metadata directory, receipt, or target directory are refused.
//   - Pre-existing non-empty unmanaged directories and mismatched directory identities are refused.
//   - agent_assistant_workspace, pathguard offLimits, and node sessionScope checks always run.
//   - Retries and continuations for the same todo_id safely reuse their verified directory.
func (n *Node) prepareScratchWorkspace(ctx context.Context, req nodeproto.ScratchWorkspaceRequest) (nodeproto.ScratchWorkspaceResult, error) {
	todoID := string(req.TodoID)
	if !nodeproto.ValidScratchID(todoID) {
		return nodeproto.ScratchWorkspaceResult{}, refusal("invalid_scratch_id", "scratch workspace todo_id must be 1-96 ASCII letters, digits, hyphens, or underscores starting with a letter or digit", nil)
	}
	baseDir := strings.TrimSpace(n.dir)
	if baseDir == "" {
		baseDir = identity.DataDir(n.home)
	}
	if baseDir == "" || !filepath.IsAbs(baseDir) {
		return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_unavailable", "node data directory is unavailable for managed scratch workspaces", nil)
	}
	state, err := n.realPath(filepath.Clean(baseDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_unsafe", fmt.Sprintf("managed scratch base directory is unsafe: %v", err), nil)
	}
	scratchRoot := filepath.Join(state, scratchDirName)
	metaDir := filepath.Join(scratchRoot, scratchMetaDirName)
	target := filepath.Join(scratchRoot, todoID)
	receiptPath := filepath.Join(metaDir, todoID+".json")
	if filepath.Dir(target) != scratchRoot || filepath.Base(target) != todoID {
		return nodeproto.ScratchWorkspaceResult{}, refusal("invalid_scratch_id", "scratch workspace path escapes managed scratch root", nil)
	}

	n.scratchMu.Lock()
	defer n.scratchMu.Unlock()

	if err := checkManagedScratchContainer(scratchRoot, "managed scratch root"); err != nil {
		return nodeproto.ScratchWorkspaceResult{}, err
	}
	if err := checkManagedScratchContainer(metaDir, "managed scratch metadata directory"); err != nil {
		return nodeproto.ScratchWorkspaceResult{}, err
	}
	if !n.sessionScopeAllows(target) {
		return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_out_of_scope", "scratch workspace is outside this node's scope", nil)
	}
	if err := n.offLimitsResolved(target); err != nil {
		return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_unsafe", err.Error(), nil)
	}
	if err := n.guardAgentDirectory(ctx, target); err != nil {
		return nodeproto.ScratchWorkspaceResult{}, err
	}

	visible := n.visibleScratchPath(state, scratchRoot, target)
	st, statErr := os.Lstat(target)
	switch {
	case statErr == nil:
		if st.Mode()&os.ModeSymlink != 0 {
			return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_unsafe", "scratch workspace path is a symbolic link", nil)
		}
		if !st.IsDir() {
			return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_conflict", "scratch workspace path already exists and is not a directory", nil)
		}
		fileID, err := workspaceFileID(target)
		if err != nil {
			return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_unsafe", fmt.Sprintf("cannot verify scratch directory identity: %v", err), nil)
		}
		owned, hasReceipt, err := verifyScratchReceipt(receiptPath, todoID, fileID)
		if err != nil {
			return nodeproto.ScratchWorkspaceResult{}, err
		}
		if owned {
			return nodeproto.ScratchWorkspaceResult{Path: visible, Created: false, Reused: true}, nil
		}
		if hasReceipt {
			return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_identity_mismatch", "existing scratch directory does not match managed ownership record", nil)
		}
		entries, err := os.ReadDir(target)
		if err != nil {
			return nodeproto.ScratchWorkspaceResult{}, fmt.Errorf("inspect existing scratch directory: %w", err)
		}
		if len(entries) > 0 {
			return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_conflict", "existing scratch directory is non-empty and unmanaged; refusing to reuse", nil)
		}
		if req.DryRun {
			return nodeproto.ScratchWorkspaceResult{Path: visible}, nil
		}
		if err := n.ensureScratchMetaDir(metaDir); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, err
		}
		if err := writeScratchReceipt(receiptPath, todoID, target, fileID); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, fmt.Errorf("record scratch workspace ownership: %w", err)
		}
		return nodeproto.ScratchWorkspaceResult{Path: n.visibleScratchPath(state, scratchRoot, target), Created: false, Reused: true}, nil

	case errors.Is(statErr, os.ErrNotExist):
		if req.DryRun {
			return nodeproto.ScratchWorkspaceResult{Path: visible}, nil
		}
		if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, fmt.Errorf("create managed scratch root: %w", err)
		}
		if err := checkManagedScratchContainer(scratchRoot, "managed scratch root"); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, err
		}
		if err := n.ensureScratchMetaDir(metaDir); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, err
		}
		if err := os.Mkdir(target, 0o700); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, fmt.Errorf("create managed scratch directory: %w", err)
		}
		fileID, err := workspaceFileID(target)
		if err != nil {
			return nodeproto.ScratchWorkspaceResult{}, refusal("scratch_path_unsafe", fmt.Sprintf("cannot verify created scratch directory identity: %v", err), nil)
		}
		if err := n.guardAgentDirectory(ctx, target); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, err
		}
		if err := writeScratchReceipt(receiptPath, todoID, target, fileID); err != nil {
			return nodeproto.ScratchWorkspaceResult{}, fmt.Errorf("record scratch workspace ownership: %w", err)
		}
		return nodeproto.ScratchWorkspaceResult{Path: n.visibleScratchPath(state, scratchRoot, target), Created: true, Reused: false}, nil

	default:
		return nodeproto.ScratchWorkspaceResult{}, fmt.Errorf("inspect scratch workspace path: %w", statErr)
	}
}

func checkManagedScratchContainer(path, label string) error {
	if err := workspaceNoLinks(path); err != nil {
		return refusal("scratch_path_unsafe", fmt.Sprintf("%s contains a symbolic link or invalid path", label), nil)
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return refusal("scratch_path_unsafe", fmt.Sprintf("%s is not a real directory", label), nil)
	}
	return nil
}

func (n *Node) ensureScratchMetaDir(metaDir string) error {
	if err := os.MkdirAll(metaDir, 0o700); err != nil {
		return fmt.Errorf("create managed scratch metadata directory: %w", err)
	}
	return checkManagedScratchContainer(metaDir, "managed scratch metadata directory")
}

func verifyScratchReceipt(receiptPath, todoID, fileID string) (owned, exists bool, err error) {
	st, lerr := os.Lstat(receiptPath)
	if errors.Is(lerr, os.ErrNotExist) {
		return false, false, nil
	}
	if lerr != nil {
		return false, false, fmt.Errorf("inspect scratch ownership receipt: %w", lerr)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return false, true, refusal("scratch_path_unsafe", "scratch ownership receipt is not a regular file", nil)
	}
	raw, rerr := os.ReadFile(receiptPath)
	if rerr != nil {
		return false, true, fmt.Errorf("read scratch ownership receipt: %w", rerr)
	}
	var receipt scratchWorkspaceReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return false, true, refusal("scratch_identity_mismatch", "scratch ownership receipt is corrupt", nil)
	}
	if receipt.TodoID == todoID && receipt.FileID != "" && receipt.FileID == fileID {
		return true, true, nil
	}
	return false, true, nil
}

func writeScratchReceipt(receiptPath, todoID, target, fileID string) error {
	if st, err := os.Lstat(receiptPath); err == nil && (st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular()) {
		return refusal("scratch_path_unsafe", "scratch ownership receipt is not a regular file", nil)
	}
	return workspaceWrite(receiptPath, scratchWorkspaceReceipt{
		SchemaVersion: 1,
		TodoID:        todoID,
		Path:          target,
		FileID:        fileID,
		CreatedAt:     time.Now().UTC(),
	})
}

func (n *Node) visibleScratchPath(state, scratchRoot, target string) string {
	if _, err := os.Stat(target); err == nil {
		return n.visiblePath(target)
	}
	if _, err := os.Stat(scratchRoot); err == nil {
		return filepath.Join(n.visiblePath(scratchRoot), filepath.Base(target))
	}
	if _, err := os.Stat(state); err == nil {
		return filepath.Join(n.visiblePath(state), scratchDirName, filepath.Base(target))
	}
	if h := homeSpelling(target, n.home); h != "" {
		return h
	}
	return target
}
