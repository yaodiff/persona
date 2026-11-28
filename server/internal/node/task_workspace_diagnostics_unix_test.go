//go:build unix

package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func TestBackendWorkspaceReceiptRedactsGitStderr(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_stderr", OperationID: "op_stderr",
		Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "main"))}
	secrets := []string{
		"fixture-user", "fixture-pass%2Dword", "example.invalid/private/repo.git", "fixture-query",
		"fixture-bearer-value", "fixture-basic-value", "fixture-token-value", "opaque-helper-output",
		"fixture-ssh-user", "example.invalid:private/repo.git", "fixture-header-value",
	}
	stderr := strings.Repeat("Receiving objects from the source\r", 2048) +
		"fatal: \tAuthentication  failed for 'https://fixture-user:fixture-pass%2Dword@example.invalid/private/repo.git?access_token=fixture-query'\n" +
		"Authorization: Bearer fixture-bearer-value\nProxy-Authorization: Basic fixture-basic-value\n" +
		"username=fixture-user password=fixture-pass%2Dword token=fixture-token-value\n" +
		"remote: opaque-helper-output\nX-Auth-Token: fixture-header-value\n" +
		"remote: fixture-ssh-user@example.invalid:private/repo.git\n"
	t.Setenv("WORKSPACE_FETCH_STDERR", stderr)
	workspaceFetchShim(t, `printf '%s' "$WORKSPACE_FETCH_STDERR" >&2
		exit 128`)
	got := workspaceCall(t, n, "prepare", req)
	if got.Error == nil || got.Error.Code != "source_fetch_failed" {
		t.Fatalf("prepare error = %v; want source_fetch_failed", got.Error)
	}
	path := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID), "receipt.json")
	receipt, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics struct {
		Summary string `json:"last_git_stderr_summary"`
	}
	if err := json.Unmarshal(receipt, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.Summary) > 384 || !strings.Contains(diagnostics.Summary, "authentication failed") ||
		diagnostics.Summary != strings.Join(strings.Fields(diagnostics.Summary), " ") {
		t.Fatal("receipt lacks a bounded, whitespace-normalized authentication diagnostic")
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "last_git_stderr_summary") || strings.Contains(string(wire), diagnostics.Summary) {
		t.Fatal("workspace response exposed local Git diagnostics")
	}
	for _, secret := range secrets {
		if strings.Contains(string(receipt), secret) || strings.Contains(string(wire), secret) || strings.Contains(got.Error.Error(), secret) {
			t.Fatal("workspace receipt or response exposed Git stderr data outside the summary")
		}
	}
}
