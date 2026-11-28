package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/nwverse/persona/server/internal/taskworkspace"
)

// Engine workspace deletion resolves native IDs first. Check those physical
// targets against Node receipts even when an old client knows only a native ID.
func (n *Node) guardBackendDeletion(path string) error {
	if n.dir == "" {
		return nil
	}
	control, err := n.workspaceControlDir("task-workspaces")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(control)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	target := realPath(path)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(control, entry.Name(), "receipt.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var j workspaceJournal
		if err = json.Unmarshal(raw, &j); err != nil {
			return err
		}
		for owned, identity := range j.Identities {
			if j.State == "removed" && (j.Cleanup == nil || !slices.Contains(j.Cleanup.RetainedPaths, owned)) {
				continue
			}
			ownedReal := realPath(owned)
			if !isInsideOrEqual(target, ownedReal) && !isInsideOrEqual(ownedReal, target) {
				continue
			}
			if id, err := workspaceFileID(owned); err == nil && id == identity {
				return taskworkspace.Failure("resource_conflict", "cleanup", j.OperationID, "", owned)
			}
		}
	}
	return nil
}
