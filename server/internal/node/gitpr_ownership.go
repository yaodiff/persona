package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Ownership reads the actual checkout and the node's signed-in GitHub identity,
// not an agent's report or the PR URL it happened to mention.
func (n *Node) gitPROwnership(ctx context.Context, cwd string) (map[string]any, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	branch, err := git(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return nil, err
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	raw, err := ghRun(ctx, dir, 15*time.Second, "pr", "view", strings.TrimSpace(branch), "--json",
		"url,author,headRefName,headRefOid")
	if err != nil {
		return nil, err
	}
	var pr struct {
		URL    string `json:"url"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
		Head string `json:"headRefName"`
		OID  string `json:"headRefOid"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, err
	}
	address, err := url.Parse(pr.URL)
	if err != nil || address.Hostname() == "" {
		return nil, fmt.Errorf("GitHub did not return a valid PR URL")
	}
	viewer, err := ghRun(ctx, dir, 15*time.Second, "api", "user", "--hostname", address.Hostname(), "--jq", ".login")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"branch": strings.TrimSpace(branch), "head_oid": strings.TrimSpace(head), "viewer": strings.TrimSpace(string(viewer)),
		"pr": map[string]string{"url": pr.URL, "author": pr.Author.Login, "head": pr.Head, "head_oid": pr.OID},
	}, nil
}
