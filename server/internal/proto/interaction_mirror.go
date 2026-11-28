package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// ApprovalMirrorIDPrefix is reserved for display copies. An authoritative
// interaction with this prefix is rejected at manager and hub ingestion.
const ApprovalMirrorIDPrefix = "assistant-main-mirror-"

// ApprovalMirrorID is the only display ID the hub accepts for an authoritative
// interaction's main-conversation copy. A nonzero occurrence timestamp is part
// of both the ID and its hash: a delayed click on an earlier occurrence cannot
// alias a later card whose runtime reused the original ID. A runtime-chosen ID
// cannot alias an unrelated pending operation into that interaction's decision
// path. The optional zero/omitted form retains the legacy zero-time identity.
func ApprovalMirrorID(id string, occurrence ...time.Time) string {
	salt, prefix := "", ApprovalMirrorIDPrefix
	if len(occurrence) > 0 && !occurrence[0].IsZero() {
		ns := occurrence[0].UTC().UnixNano()
		salt = strconv.FormatInt(ns, 10)
		prefix += "v1-" + strconv.FormatInt(ns, 36) + "-"
	}
	sum := sha256.Sum256([]byte("assistant-main-approval-mirror\x00" + id + "\x00" + salt))
	return prefix + hex.EncodeToString(sum[:])
}

// ApprovalMirrorReceiptID validates a display ID after its live cards are gone.
// The occurrence embedded in a versioned ID is only a decoding aid: the whole
// deterministic ID still has to match this exact authoritative ID. An operation
// receipt is additionally required before this normalization can relay a retry.
func ApprovalMirrorReceiptID(originID, mirrorID string) bool {
	versioned := ApprovalMirrorIDPrefix + "v1-"
	if !strings.HasPrefix(mirrorID, versioned) {
		return mirrorID == ApprovalMirrorID(originID)
	}
	rest := strings.TrimPrefix(mirrorID, versioned)
	// The suffix is '-' plus the 64 hexadecimal hash bytes. A base-36 timestamp
	// before it may itself have a leading minus for a pre-epoch test fixture.
	if len(rest) < 65 || rest[len(rest)-65] != '-' {
		return false
	}
	ns, err := strconv.ParseInt(rest[:len(rest)-65], 36, 64)
	if err != nil {
		return false
	}
	return mirrorID == ApprovalMirrorID(originID, time.Unix(0, ns).UTC())
}
