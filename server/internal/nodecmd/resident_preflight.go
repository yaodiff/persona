package nodecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"time"

	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/nodeupdate"
)

type residentCheckOutput struct{ bytes.Buffer }

func (b *residentCheckOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("resident preflight output exceeds its budget")
	}
	return b.Buffer.Write(p)
}

func checkResidentReplacement(ctx context.Context, path, program string) error {
	check, err := residentReplacementInfo(ctx, path, program)
	if err != nil {
		return err
	}
	// Where there is no handover (Windows), the supervisor starts the replacement as a new process instead.
	if !check.ResidentHandover && node.ResidentHandoverSupported {
		return errors.New("resident replacement does not support listener handover")
	}
	return nil
}

func residentReplacementInfo(ctx context.Context, path, program string) (nodeupdate.Check, error) {
	var check nodeupdate.Check
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "node-update-check")
	var out residentCheckOutput
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return check, err
	}
	if err := json.Unmarshal(out.Bytes(), &check); err != nil {
		return check, err
	}
	if check.OS != runtime.GOOS || check.Arch != runtime.GOARCH || check.Version == "" ||
		(check.Program != "" && program != "" && check.Program != program) {
		return check, errors.New("resident replacement identity does not match this node")
	}
	return check, nil
}

// Startup rollback has not served a connection yet. Legacy images must receive neither the listener nor the lock;
// they reacquire both themselves. A capable image can use the normal descriptor handover.
func prepareResidentRecovery(transport *node.ResidentTransport, handover bool) (*node.ResidentFiles, error) {
	if !handover {
		transport.Close()
		return nil, nil
	}
	return transport.PrepareExec()
}
