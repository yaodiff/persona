package repositorywire

import (
	"strings"
	"time"
)

// Node RPC method names for managed bare repository mirrors under ~/.persona/repos/.
const (
	MethodRepoMirrorEnsure = "repo.mirror.ensure"
	MethodRepoMirrorStatus = "repo.mirror.status"
	MethodRepoMirrorRemove = "repo.mirror.remove"
)

// MirrorColdFetchTimeout is the Node's cold build budget.
const MirrorColdFetchTimeout = 10 * time.Minute

// MirrorEnsureTimeout leaves time beyond the cold fetch for the Node to return its result.
const MirrorEnsureTimeout = MirrorColdFetchTimeout + time.Minute

// Bounds and defaults for mirror TTL fetches and low-frequency periodic auto-sync.
const (
	DefaultMirrorFetchTTLSeconds     = 300
	MaxMirrorFetchTTLSeconds         = 86400
	DefaultMirrorSyncIntervalMinutes = 30
	MinMirrorSyncIntervalMinutes     = 15
	MaxMirrorSyncIntervalMinutes     = 1440
)

// ValidateMirrorRepoID validates a repository ID used as the filename stem under ~/.persona/repos/<repo_id>.git.
// It rejects empty IDs, path separators, traversal segments, control characters, and Windows reserved names.
func ValidateMirrorRepoID(repoID string) bool {
	if repoID == "" || len(repoID) > 80 || strings.HasSuffix(repoID, ".") || strings.Contains(repoID, "..") {
		return false
	}
	for i, c := range repoID {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok && (i == 0 || !strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return !IsWindowsReservedName(repoID)
}

// ValidateMirrorMachineID validates a machine ID for mirror configuration storage and APIs.
func ValidateMirrorMachineID(machineID string) bool {
	if machineID == "" || len(machineID) > 128 {
		return false
	}
	for _, c := range machineID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._-:", c)) {
			return false
		}
	}
	return true
}

// NormalizeMirrorTTLSeconds applies the default TTL when ttl is 0 and rejects negative or excessive values.
func NormalizeMirrorTTLSeconds(ttl int) (int, error) {
	if ttl < 0 || ttl > MaxMirrorFetchTTLSeconds {
		return 0, Problem("invalid")
	}
	if ttl == 0 {
		return DefaultMirrorFetchTTLSeconds, nil
	}
	return ttl, nil
}

// NormalizeMirrorSyncIntervalMinutes applies the default periodic sync interval when minutes is 0
// and validates explicit values against [MinMirrorSyncIntervalMinutes, MaxMirrorSyncIntervalMinutes].
func NormalizeMirrorSyncIntervalMinutes(minutes int) (int, error) {
	if minutes == 0 {
		return DefaultMirrorSyncIntervalMinutes, nil
	}
	if minutes < MinMirrorSyncIntervalMinutes || minutes > MaxMirrorSyncIntervalMinutes {
		return 0, Problem("invalid")
	}
	return minutes, nil
}

// MirrorEnsureRequest asks a Node to ensure ~/.persona/repos/<repo_id>.git exists and is fetched within TTL.
type MirrorEnsureRequest struct {
	RepoID     string   `json:"repo_id"`
	Identity   Identity `json:"identity"`
	CloneURL   string   `json:"clone_url"`
	TTLSeconds int      `json:"ttl_seconds,omitempty"`
	Force      bool     `json:"force,omitempty"`
}

// MirrorStatusRequest asks a Node for the status of one mirror (or all mirrors when RepoID is empty).
type MirrorStatusRequest struct {
	RepoID string `json:"repo_id,omitempty"`
}

// MirrorRemoveRequest asks a Node to safely delete ~/.persona/repos/<repo_id>.git and its metadata.
type MirrorRemoveRequest struct {
	RepoID string `json:"repo_id"`
}

// MirrorInfo describes a single managed bare mirror on a Node.
type MirrorInfo struct {
	RepoID        string     `json:"repo_id"`
	Path          string     `json:"path,omitempty"`
	Exists        bool       `json:"exists"`
	CloneURL      string     `json:"clone_url,omitempty"`
	DefaultRef    string     `json:"default_ref,omitempty"`
	HeadCommit    string     `json:"head_commit,omitempty"`
	LastSyncedAt  *time.Time `json:"last_synced_at,omitempty"`
	DiskBytes     int64      `json:"disk_bytes"`
	Fetched       bool       `json:"fetched,omitempty"`
	Coalesced     bool       `json:"coalesced,omitempty"`
	SkippedReason string     `json:"skipped_reason,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// MirrorStatusResponse is returned by repo.mirror.status.
type MirrorStatusResponse struct {
	Mirror         *MirrorInfo  `json:"mirror,omitempty"`
	Mirrors        []MirrorInfo `json:"mirrors,omitempty"`
	TotalDiskBytes int64        `json:"total_disk_bytes"`
}

// MirrorRemoveResponse is returned by repo.mirror.remove.
type MirrorRemoveResponse struct {
	RepoID     string `json:"repo_id"`
	Removed    bool   `json:"removed"`
	FreedBytes int64  `json:"freed_bytes"`
}

// RepoMirrorRecord is the Manager-persisted configuration and sync status for one (machine, repo_id) pair.
type RepoMirrorRecord struct {
	MachineID           string     `json:"machine_id"`
	RepoID              string     `json:"repo_id"`
	Enabled             bool       `json:"enabled"`
	AutoSync            bool       `json:"auto_sync"`
	SyncIntervalMinutes int        `json:"sync_interval_minutes"`
	LastSyncedAt        *time.Time `json:"last_synced_at,omitempty"`
	LastHeadCommit      string     `json:"last_head_commit,omitempty"`
	LastDefaultRef      string     `json:"last_default_ref,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	DiskBytes           int64      `json:"disk_bytes"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextSyncAt          *time.Time `json:"next_sync_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// RepoMirrorSyncUpdate carries the outcome of a mirror sync attempt to persist in Manager storage.
type RepoMirrorSyncUpdate struct {
	LastSyncedAt        *time.Time
	LastHeadCommit      string
	LastDefaultRef      string
	LastError           string
	DiskBytes           int64
	HasDiskBytes        bool
	ConsecutiveFailures int
	NextSyncAt          *time.Time
	UpdatedAt           time.Time
}

// RepoMirrorConfigInput is the request body for configuring a per-(machine, repo_id) managed bare mirror.
type RepoMirrorConfigInput struct {
	Enabled             bool `json:"enabled"`
	AutoSync            bool `json:"auto_sync"`
	SyncIntervalMinutes int  `json:"sync_interval_minutes,omitempty"`
}
