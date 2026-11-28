package repository

import "github.com/nwverse/persona/server/internal/repositorywire"

const (
	MethodRepoMirrorEnsure           = repositorywire.MethodRepoMirrorEnsure
	MethodRepoMirrorStatus           = repositorywire.MethodRepoMirrorStatus
	MethodRepoMirrorRemove           = repositorywire.MethodRepoMirrorRemove
	MirrorColdFetchTimeout           = repositorywire.MirrorColdFetchTimeout
	DefaultMirrorFetchTTLSeconds     = repositorywire.DefaultMirrorFetchTTLSeconds
	MaxMirrorFetchTTLSeconds         = repositorywire.MaxMirrorFetchTTLSeconds
	DefaultMirrorSyncIntervalMinutes = repositorywire.DefaultMirrorSyncIntervalMinutes
	MinMirrorSyncIntervalMinutes     = repositorywire.MinMirrorSyncIntervalMinutes
	MaxMirrorSyncIntervalMinutes     = repositorywire.MaxMirrorSyncIntervalMinutes
)

type MirrorEnsureRequest = repositorywire.MirrorEnsureRequest
type MirrorStatusRequest = repositorywire.MirrorStatusRequest
type MirrorRemoveRequest = repositorywire.MirrorRemoveRequest
type MirrorInfo = repositorywire.MirrorInfo
type MirrorStatusResponse = repositorywire.MirrorStatusResponse
type MirrorRemoveResponse = repositorywire.MirrorRemoveResponse
type RepoMirrorRecord = repositorywire.RepoMirrorRecord
type RepoMirrorSyncUpdate = repositorywire.RepoMirrorSyncUpdate
type RepoMirrorConfigInput = repositorywire.RepoMirrorConfigInput

func ValidateMirrorRepoID(repoID string) bool { return repositorywire.ValidateMirrorRepoID(repoID) }
func ValidateMirrorMachineID(machineID string) bool {
	return repositorywire.ValidateMirrorMachineID(machineID)
}
func NormalizeMirrorTTLSeconds(ttl int) (int, error) {
	return repositorywire.NormalizeMirrorTTLSeconds(ttl)
}
func NormalizeMirrorSyncIntervalMinutes(minutes int) (int, error) {
	return repositorywire.NormalizeMirrorSyncIntervalMinutes(minutes)
}
