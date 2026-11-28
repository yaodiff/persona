package node

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// The agy CLI reads its account once when a process starts, keeps it in memory and writes that account's token back at
// every hourly refresh, so a sign-in made after the warm model.run worker started never reaches it (#10368, #17860).
// The worker's key stamps where the CLI keeps its sign-in: antigravity-oauth-token on Linux (agyWarmKey.oauthStat) and,
// on macOS, the login-keychain item. Only the item's attributes are read: `security find-generic-password` without -g
// or -w prints no secret.

const (
	agyKeychainProgram = "/usr/bin/security"
	agyKeychainService = "gemini"
	agyKeychainAccount = "antigravity"
	agyKeychainMissing = 44 // `security` exits 44 when the item does not exist
	agyKeychainWait    = 5 * time.Second
)

// agyKeychainStampFn is replaced in tests.
var agyKeychainStampFn = agyKeychainStamp

var agyKeychainLast struct {
	sync.Mutex
	stamp string
}

var agyKeychainDate = lazyre.New(`^\s*"(cdat|mdat)"<timedate>=.*?"([0-9]{14}Z)`)

// agyKeychainStamp answers the keychain item's dates ("keychain:<created>:<modified>"), "keychain:none" without an
// item, or "" off macOS. A failed read keeps the last answer, so a transient failure never looks like a new sign-in.
func agyKeychainStamp() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), agyKeychainWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, agyKeychainProgram, "find-generic-password", "-s", agyKeychainService, "-a", agyKeychainAccount).Output()
	stamp := ""
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		stamp = parseAgyKeychainStamp(out)
	case errors.As(err, &exitErr) && exitErr.ExitCode() == agyKeychainMissing:
		stamp = "keychain:none"
	}
	agyKeychainLast.Lock()
	defer agyKeychainLast.Unlock()
	if stamp == "" {
		return agyKeychainLast.stamp
	}
	agyKeychainLast.stamp = stamp
	return stamp
}

func parseAgyKeychainStamp(out []byte) string {
	dates := map[string]string{}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if m := agyKeychainDate.FindSubmatch(line); m != nil {
			dates[string(m[1])] = string(m[2])
		}
	}
	if dates["mdat"] == "" {
		return ""
	}
	return "keychain:" + dates["cdat"] + ":" + dates["mdat"]
}

// agy streams an account error as its whole answer, one line, and ends the turn normally (the same shape the
// Antigravity runtime reads in integrations/antigravity/lib/errors.js).
var agyStreamedErrorLine = lazyre.New(`^⚠ ([A-Z][A-Z_]*) \(code (\d{3})\): ([^\n]+)$`)

// agyAccountTrouble reports whether a turn's whole answer is agy's quota or sign-in error line: the worker that gave it
// may hold an account the CLI has since left, so it is not kept for the next request.
func agyAccountTrouble(text string) bool {
	m := agyStreamedErrorLine.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return false
	}
	switch {
	case m[1] == "RESOURCE_EXHAUSTED", m[1] == "UNAUTHENTICATED", m[2] == "429", m[2] == "401":
		return true
	}
	return strings.Contains(strings.ToLower(m[3]), "quota")
}
