package githubwire

import "strings"

// ID is the GitHub connector's id: its key in manager.json and the prefix of its tools.
const ID = "github"

// Config is manager.json's "github".
type Config struct {
	Auth AuthConfig `json:"auth"`
	// Inbox: the defaults of what comes into the inbox until the user chooses in the app.
	Inbox InboxOptions `json:"inbox,omitempty"`
	// Proxy is github.proxy: an http:// proxy for everything sent to GitHub (the device flow on its web host, the REST
	// API, CI logs), for networks that cannot reach github.com directly; empty follows the process's proxy environment
	// (direct when it names no proxy). The connector builds its client with it as netproxy.Egress does, and a malformed
	// one fails every request rather than going direct.
	Proxy string `json:"proxy,omitempty"`
	// APIRoot is where the REST requests go (no trailing slash) instead of the host's own API, read when the connector is
	// made: a fake GitHub for a test in another package. It is not a setting (never read from hub.json).
	APIRoot string `json:"-"`
}

// AuthConfig: whose gh login the hub reads, and for which host. Host is github.com when empty; a GitHub Enterprise
// host goes here and only here (not built yet: only github.com is tried).
type AuthConfig struct {
	FromMachine string `json:"from_machine,omitempty"`
	Host        string `json:"host,omitempty"`
}

// Configured says whether the hub should run the connector at all.
func (c *Config) Configured() bool { return c != nil && c.Auth.FromMachine != "" }

// Host is the GitHub host the connector reads: github.com when Auth.Host is empty.
func (c *Config) Host() string {
	if c.Auth.Host == "" {
		return "github.com"
	}
	return strings.ToLower(c.Auth.Host)
}

// InboxOptions are the user's choices for GitHub's inbox.
type InboxOptions struct {
	CI         string   `json:"ci,omitempty"`
	MutedRepos []string `json:"muted_repos,omitempty"`
	// IgnoreBranches are path.Match patterns of branches whose CI never comes in; nil is the connector's default.
	IgnoreBranches []string `json:"ignore_branches,omitempty"`
}
