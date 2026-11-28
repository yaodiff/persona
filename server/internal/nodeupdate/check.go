package nodeupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodesign"
)

// CheckLimits bound one run of an executable's node-update-check.
//
// FirstRun applies to a file this process has not seen pass a check yet. Endpoint protection on some Macs holds a new
// executable's first start until its scan ends, about 30 s, and caches the verdict by content, so later starts take
// milliseconds (#12528). Repeat applies once the same file (path, file identity, size and modification time) passed:
// a run that slow then is a hang, not a scan. Each check is one run, so FirstRun (plus the second the process gets to
// exit) is the most any check waits.
type CheckLimits struct {
	FirstRun time.Duration
	Repeat   time.Duration
}

// checkLimits are the update's own: staging, signing and rollback checks.
var checkLimits = CheckLimits{FirstRun: 60 * time.Second, Repeat: 20 * time.Second}

// CommitWait is the most Manager.Commit takes before it answers: the upload's node-update-check and, with a Signer, up
// to two signed copies (with the certificate, then ad hoc), each signed and verified (nodesign.SignLimit) and then
// checked. Each copy is a new file, so each check may take FirstRun while endpoint protection scans it; copying,
// hashing and each process's second to exit get the margin. Whoever waits for node.update.commit (the hub's deploy,
// devices/deploy_rpc.go, and its relay) waits longer than this, or it takes a slow answer for a lost one.
func CommitWait() time.Duration {
	return 3*checkLimits.FirstRun + 2*nodesign.SignLimit + 30*time.Second
}

// passedChecks remembers files whose check passed, so their next runs get the Repeat limit. A file replaced or
// rewritten at the same path no longer matches its entry and gets FirstRun again.
var passedChecks = struct {
	sync.Mutex
	files map[string]os.FileInfo
}{files: map[string]os.FileInfo{}}

// passedChecksMax bounds the memo: staged files have fresh names, and a resident runs for months.
const passedChecksMax = 32

func checkedBefore(path string, info os.FileInfo) bool {
	passedChecks.Lock()
	defer passedChecks.Unlock()
	seen, ok := passedChecks.files[path]
	return ok && os.SameFile(seen, info) && seen.Size() == info.Size() && seen.ModTime().Equal(info.ModTime())
}

func rememberCheck(path string, info os.FileInfo) {
	passedChecks.Lock()
	defer passedChecks.Unlock()
	if len(passedChecks.files) >= passedChecksMax {
		clear(passedChecks.files)
	}
	passedChecks.files[path] = info
}

// RunCheck runs path's node-update-check and returns what it printed, unvalidated. The run is limited by
// limits.FirstRun the first time this process checks the file and by limits.Repeat after it passed once. When the
// limit expires the error says "node-update-check timed out after …", so a slow first start reads differently from a
// version, platform or program mismatch.
func RunCheck(ctx context.Context, path string, limits CheckLimits) (Check, error) {
	limit, run := limits.FirstRun, "first run of this file"
	info, statErr := os.Stat(path)
	if statErr == nil && checkedBefore(path, info) {
		limit, run = limits.Repeat, "this file passed before"
	}
	cause := fmt.Errorf("node-update-check timed out after %s (%s): %w", limit, run, context.DeadlineExceeded)
	ctx, cancel := context.WithTimeoutCause(ctx, limit, cause)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "node-update-check")
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w: %w", context.Cause(ctx), err)
		}
		return Check{}, err
	}
	var check Check
	if err := json.Unmarshal(output.Bytes(), &check); err != nil {
		return Check{}, fmt.Errorf("node-update-check output: %w", err)
	}
	if statErr == nil {
		rememberCheck(path, info)
	}
	return check, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("self-check output exceeds limit")
	}
	return b.Buffer.Write(p)
}
