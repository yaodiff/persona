package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGitPROwnershipReadsNodeIdentityAndActualCheckout(t *testing.T) {
	f := newPullFixture(t)
	runPullGit(t, f.local, "switch", "-q", "-c", "fix/login")
	head := strings.TrimSpace(runPullGit(t, f.local, "rev-parse", "HEAD"))
	calls := fakePRGh(t, "", fmt.Sprintf(`{"url":"https://github.com/acme/web/pull/42",
		"author":{"login":"contributor"},"headRefName":"fix/login","headRefOid":%q}`, head))
	args, _ := json.Marshal(map[string]any{"cwd": f.local, "ownership": true})
	raw, err := f.node.gitWrite(context.Background(), methodGitPR, args)
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	pr, ok := out["pr"].(map[string]string)
	if !ok || out["viewer"] != "contributor" || out["branch"] != "fix/login" || out["head_oid"] != head ||
		pr["author"] != "contributor" || pr["head"] != "fix/login" || pr["head_oid"] != head ||
		pr["url"] != "https://github.com/acme/web/pull/42" {
		t.Fatal("ownership did not come from node Git/GitHub reads", out)
	}
	if got := calls(); len(got) != 2 || !strings.Contains(got[0], "pr view fix/login") || got[1] != "api user --hostname github.com --jq .login" {
		t.Fatal("ownership read unrelated reviews or skipped identity verification", got)
	}
}

func TestGitPROwnershipFailsWithoutAuthoritativeNodeSources(t *testing.T) {
	for _, kind := range []string{"signed out", "detached"} {
		t.Run(kind, func(t *testing.T) {
			f := newPullFixture(t)
			auth := ""
			if kind == "signed out" {
				auth = "out"
			} else {
				runPullGit(t, f.local, "checkout", "--detach")
			}
			fakePRGh(t, auth)
			if _, err := f.node.gitPROwnership(context.Background(), f.local); err == nil {
				t.Fatal("missing ownership sources were accepted")
			}
		})
	}
}
