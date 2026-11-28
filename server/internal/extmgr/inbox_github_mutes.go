package extmgr

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
)

// ---- GitHub's muted repositories ----
//
// GitHub's settings page (and older apps) keep muted repositories in its options ("muted_repos"). They are the
// inbox's mutes of source github, tag repo (the manager's): listed in its options from there, set through them into
// there, and the ones kept in its options before are moved over once (githubMutes, when the manager connects).

const githubMutedRepos = "muted_repos"

// githubMutes moves the repositories GitHub's options still mute into the inbox's mutes and empties them there.
func (h *Manager) githubMutes(ctx context.Context) {
	o, ok := connector.As[connector.Options](h.connectorOf("github"))
	if !ok {
		return
	}
	var opts map[string]json.RawMessage
	var repos []string
	if json.Unmarshal(o.Options(), &opts) != nil || json.Unmarshal(opts[githubMutedRepos], &repos) != nil || len(repos) == 0 {
		return
	}
	params := assistantwire.InboxRepoMutes{Source: "github", Set: true, Add: true, Repos: repos}
	if _, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.InboxRepoMutesMethod, params); err != nil {
		h.logf("inbox: github's muted repositories: %v", err)
		return
	}
	opts[githubMutedRepos] = json.RawMessage(`[]`)
	raw, _ := json.Marshal(opts)
	if err := h.applyConnectorOptions(ctx, "github", raw); err != nil {
		h.logf("inbox: github's muted repositories moved, its options kept them: %v", err)
	}
}

// withGitHubMutes is GitHub's options as listed: the muted repositories are the inbox's mutes. While the manager
// can't say there are none (nil: the apps draw no inbox section for GitHub): options without the mutes would show
// nothing muted, and saving them (another setting changed) would unmute every repository.
func (h *Manager) withGitHubMutes(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var opts map[string]json.RawMessage
	if json.Unmarshal(raw, &opts) != nil {
		return raw
	}
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.InboxRepoMutesMethod,
		assistantwire.InboxRepoMutes{Source: "github"})
	if err != nil {
		h.logf("inbox: github's muted repositories: %v", err)
		return nil
	}
	if res.Repos == nil {
		res.Repos = []string{}
	}
	opts[githubMutedRepos], _ = json.Marshal(res.Repos)
	out, _ := json.Marshal(opts)
	return out
}

// setGitHubMutes takes the muted repositories out of options GitHub's page sets: they become the inbox's mutes (the
// ones not listed any more are unmuted). It returns the options without them. Options that do not list them (no
// muted_repos, or null) leave the mutes as they are, the inbox's and those still in GitHub's options: only a list,
// even an empty one, sets them.
func (h *Manager) setGitHubMutes(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var opts map[string]json.RawMessage
	if json.Unmarshal(raw, &opts) != nil {
		return raw, nil // the connector says what is wrong
	}
	field, ok := opts[githubMutedRepos]
	var repos []string
	if !ok || json.Unmarshal(field, &repos) != nil || repos == nil {
		delete(opts, githubMutedRepos)
		if o, ok := connector.As[connector.Options](h.connectorOf("github")); ok {
			var cur map[string]json.RawMessage
			if json.Unmarshal(o.Options(), &cur) == nil && cur[githubMutedRepos] != nil {
				opts[githubMutedRepos] = cur[githubMutedRepos] // not moved to the inbox yet
			}
		}
		out, _ := json.Marshal(opts)
		return out, nil
	}
	params := assistantwire.InboxRepoMutes{Source: "github", Set: true, Repos: repos}
	if _, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.InboxRepoMutesMethod, params); err != nil {
		return nil, err
	}
	opts[githubMutedRepos] = json.RawMessage(`[]`)
	out, _ := json.Marshal(opts)
	return out, nil
}
