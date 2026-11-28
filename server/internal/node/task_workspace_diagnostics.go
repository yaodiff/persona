package node

import "strings"

const (
	workspaceGitStderrTail         = 8 << 10
	workspaceGitStderrSummaryLimit = 384
)

// Git, remotes and credential helpers can write arbitrary text to stderr.
// Persist only recognized diagnostic phrases so URLs, headers, and unlabeled
// secrets cannot flow from the subprocess into the receipt.
func workspaceGitStderrSummary(stderr string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(stderr), " "))
	if normalized == "" {
		return ""
	}
	var summary strings.Builder
	for _, phrase := range [...]string{
		"authentication failed",
		"could not read username",
		"terminal prompts disabled",
		"permission denied",
		"repository not found",
		"could not resolve host",
		"failed to connect",
		"connection refused",
		"connection reset",
		"connection timed out",
		"operation timed out",
		"certificate verification failed",
		"ssl certificate problem",
		"the requested url returned error",
		"couldn't find remote ref",
		"not our ref",
		"unadvertised object",
		"early eof",
		"remote end hung up unexpectedly",
		"no space left on device",
		"disk quota exceeded",
	} {
		if !strings.Contains(normalized, phrase) {
			continue
		}
		if summary.Len()+len(", ")+len(phrase) > workspaceGitStderrSummaryLimit {
			break
		}
		if summary.Len() != 0 {
			summary.WriteString(", ")
		}
		summary.WriteString(phrase)
	}
	if summary.Len() == 0 {
		return "unrecognized git diagnostic"
	}
	return summary.String()
}
