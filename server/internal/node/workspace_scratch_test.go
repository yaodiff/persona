package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

func callScratchPrepare(t *testing.T, n *Node, req nodeproto.ScratchWorkspaceRequest) (nodeproto.ScratchWorkspaceResult, error) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(context.Background(), nodeproto.MethodWorkspaceScratchPrepare, raw)
	if err != nil {
		return nodeproto.ScratchWorkspaceResult{}, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var res nodeproto.ScratchWorkspaceResult
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	return res, nil
}

func TestScratchWorkspaceDryRunCreateAndSafeReuse(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".persona")
	n := &Node{home: home, dir: state}

	// DryRun previews exact target path without creating anything on disk.
	preview, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_alpha123", DryRun: true})
	if err != nil {
		t.Fatalf("dry_run prepare: %v", err)
	}
	if preview.Created || preview.Reused {
		t.Fatalf("dry_run must not report created/reused: %+v", preview)
	}
	if _, err := os.Stat(preview.Path); !os.IsNotExist(err) {
		t.Fatalf("dry_run created directory %s: %v", preview.Path, err)
	}
	if _, err := os.Stat(filepath.Join(state, scratchDirName)); !os.IsNotExist(err) {
		t.Fatalf("dry_run created scratch root: %v", err)
	}

	// Actual prepare creates private 0700 dir and 0600 ownership receipt.
	created, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_alpha123"})
	if err != nil {
		t.Fatalf("prepare create: %v", err)
	}
	if !created.Created || created.Reused || created.Path != preview.Path {
		t.Fatalf("created=%+v, want path=%s created=true", created, preview.Path)
	}
	st, err := os.Stat(created.Path)
	if err != nil || !st.IsDir() {
		t.Fatalf("created dir stat: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Fatalf("created dir mode = %o, want 0700", perm)
	}
	// Directory itself stays empty (metadata lives beside it under .persona-managed).
	if entries, err := os.ReadDir(created.Path); err != nil || len(entries) != 0 {
		t.Fatalf("new scratch dir should be empty, got entries=%v err=%v", entries, err)
	}
	receiptPath := filepath.Join(state, scratchDirName, scratchMetaDirName, "todo_alpha123.json")
	if rst, err := os.Stat(receiptPath); err != nil || rst.Mode().Perm() != 0o600 {
		t.Fatalf("receipt stat=%v err=%v, want 0600 file", rst, err)
	}

	// Write task artifacts inside the scratch dir; same todo_id safely reuses it without losing files.
	notePath := filepath.Join(created.Path, "notes.md")
	if err := os.WriteFile(notePath, []byte("research findings"), 0o600); err != nil {
		t.Fatal(err)
	}
	reused, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_alpha123"})
	if err != nil {
		t.Fatalf("prepare reuse: %v", err)
	}
	if reused.Created || !reused.Reused || reused.Path != created.Path {
		t.Fatalf("reused=%+v, want reused=true path=%s", reused, created.Path)
	}
	if raw, err := os.ReadFile(notePath); err != nil || string(raw) != "research findings" {
		t.Fatalf("existing task note disturbed: %q %v", raw, err)
	}

	// A different todo_id gets a separate isolated directory.
	other, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_beta456"})
	if err != nil {
		t.Fatalf("second todo prepare: %v", err)
	}
	if !other.Created || other.Path == created.Path {
		t.Fatalf("second todo not isolated: other=%+v first=%+v", other, created)
	}

	// The created scratch directory passes guardAgentDirectory; assistant workspace stays refused.
	if err := n.guardAgentDirectory(context.Background(), created.Path); err != nil {
		t.Fatalf("guardAgentDirectory rejected managed scratch dir: %v", err)
	}
	assistantDir := filepath.Join(state, "assistant")
	if err := os.MkdirAll(assistantDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := n.guardAgentDirectory(context.Background(), assistantDir); rpcCode(err) != "agent_assistant_workspace" {
		t.Fatalf("guardAgentDirectory must keep refusing assistant workspace, got %v", err)
	}
}

func TestScratchWorkspaceRejectsTraversalAndInvalidIDs(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".persona")
	n := &Node{home: home, dir: state}

	badIDs := []string{
		"",
		".",
		"..",
		"../assistant",
		"../../etc",
		"todo/nested",
		"todo\\nested",
		".persona-managed",
		"-dash",
		"todo with spaces",
		"todo\x00bad",
		strings.Repeat("x", 97),
	}
	for _, id := range badIDs {
		_, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: nodeproto.ScratchID(id)})
		if rpcCode(err) != "invalid_scratch_id" {
			t.Errorf("id=%q: got err=%v (code=%q), want invalid_scratch_id", id, err, rpcCode(err))
		}
	}
	if _, err := os.Stat(filepath.Join(state, scratchDirName)); !os.IsNotExist(err) {
		t.Fatalf("invalid IDs created scratch root: %v", err)
	}
}

func TestScratchWorkspaceRefusesUnmanagedNonEmptyDirAndIdentityTamper(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, ".persona")
	n := &Node{home: home, dir: state}

	// 1. Pre-existing non-empty unmanaged directory must be refused and untouched.
	unmanaged := filepath.Join(state, scratchDirName, "todo_existing1")
	if err := os.MkdirAll(unmanaged, 0o700); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(unmanaged, "keep.txt")
	if err := os.WriteFile(userFile, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{true, false} {
		_, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_existing1", DryRun: dryRun})
		if rpcCode(err) != "scratch_path_conflict" {
			t.Fatalf("dryRun=%v unmanaged non-empty dir: got %v (code=%q), want scratch_path_conflict", dryRun, err, rpcCode(err))
		}
	}
	if raw, err := os.ReadFile(userFile); err != nil || string(raw) != "precious" {
		t.Fatalf("unmanaged dir file modified: %q %v", raw, err)
	}

	// 2. Pre-existing regular file at target path is refused.
	fileConflict := filepath.Join(state, scratchDirName, "todo_file1")
	if err := os.WriteFile(fileConflict, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_file1"}); rpcCode(err) != "scratch_path_conflict" {
		t.Fatalf("regular file conflict: got %v (code=%q), want scratch_path_conflict", err, rpcCode(err))
	}

	// 3. Managed directory replaced out-of-band (different inode) is refused with scratch_identity_mismatch.
	created, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_owned1"})
	if err != nil {
		t.Fatal(err)
	}
	replaced := filepath.Join(t.TempDir(), "replacement")
	if err := os.MkdirAll(replaced, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(created.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replaced, created.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_owned1"}); rpcCode(err) != "scratch_identity_mismatch" {
		t.Fatalf("replaced dir: got %v (code=%q), want scratch_identity_mismatch", err, rpcCode(err))
	}
}

func TestScratchWorkspaceRefusesSymlinksAndOutOfScope(t *testing.T) {
	// 1. Symlink at target pointing to assistant workspace must be refused.
	t.Run("target_symlink", func(t *testing.T) {
		home := t.TempDir()
		state := filepath.Join(home, ".persona")
		assistantDir := filepath.Join(state, "assistant")
		scratchRoot := filepath.Join(state, scratchDirName)
		if err := os.MkdirAll(assistantDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(scratchRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(assistantDir, filepath.Join(scratchRoot, "todo_sym1")); err != nil {
			t.Fatal(err)
		}
		n := &Node{home: home, dir: state}
		_, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_sym1"})
		code := rpcCode(err)
		if code != "scratch_path_unsafe" && code != "agent_assistant_workspace" {
			t.Fatalf("target symlink: got %v (code=%q)", err, code)
		}
	})

	// 2. Symlink at scratch root must be refused.
	t.Run("root_symlink", func(t *testing.T) {
		home := t.TempDir()
		state := filepath.Join(home, ".persona")
		external := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(external, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(external, filepath.Join(state, scratchDirName)); err != nil {
			t.Fatal(err)
		}
		n := &Node{home: home, dir: state}
		if _, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_sym2"}); rpcCode(err) != "scratch_path_unsafe" {
			t.Fatalf("root symlink: got %v (code=%q), want scratch_path_unsafe", err, rpcCode(err))
		}
	})

	// 3. Symlinked $HOME ancestor (like /home -> /data/home) works cleanly and preserves visible path.
	t.Run("symlinked_home_ancestor", func(t *testing.T) {
		base := t.TempDir()
		realHome := filepath.Join(base, "real-home")
		if err := os.MkdirAll(filepath.Join(realHome, ".persona"), 0o700); err != nil {
			t.Fatal(err)
		}
		linkHome := filepath.Join(base, "link-home")
		if err := os.Symlink(realHome, linkHome); err != nil {
			t.Fatal(err)
		}
		n := &Node{home: linkHome, dir: filepath.Join(linkHome, ".persona")}
		preview, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_home1", DryRun: true})
		if err != nil {
			t.Fatalf("symlinked home preview: %v", err)
		}
		created, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_home1"})
		if err != nil {
			t.Fatalf("symlinked home create: %v", err)
		}
		if !created.Created || created.Path != preview.Path {
			t.Fatalf("preview=%+v created=%+v; want identical visible path", preview, created)
		}
		wantPrefix := filepath.Join(linkHome, ".persona", scratchDirName) + string(filepath.Separator)
		if !strings.HasPrefix(created.Path, wantPrefix) {
			t.Fatalf("visible path = %q, want prefix %q", created.Path, wantPrefix)
		}
	})

	// 4. Scoped node rejects scratch directory outside sessionScope.
	t.Run("session_scope", func(t *testing.T) {
		home := t.TempDir()
		state := filepath.Join(home, ".persona")
		scopeDir := filepath.Join(home, "allowed-only")
		if err := os.MkdirAll(scopeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		n := &Node{home: home, dir: state, sessionScope: &nodeSessionScope{dir: scopeDir}}
		if _, err := callScratchPrepare(t, n, nodeproto.ScratchWorkspaceRequest{TodoID: "todo_scope1"}); rpcCode(err) != "scratch_out_of_scope" {
			t.Fatalf("out of scope: got %v (code=%q), want scratch_out_of_scope", err, rpcCode(err))
		}
	})
}
