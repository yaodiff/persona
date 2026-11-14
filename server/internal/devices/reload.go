package devices

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func restartingError(err error) bool {
	var coded *rpc.Error
	return errors.As(err, &coded) && coded.Code == nodeproto.CodeRestarting
}

func handshakeInterrupted(err error) bool {
	return restartingError(err) || errors.Is(err, rpc.ErrClosed) || errors.Is(err, io.EOF)
}

// residentRestartBarrier is an optional state.load flag, so older managers still answer a harmless read.
// New managers stop outgoing calls and acknowledge only after all pre-suspension replies were received.
func (m *Machine) residentRestartBarrier(ctx context.Context, peer *rpc.Peer, prepare bool) (any, error) {
	m.mu.Lock()
	if m.peer != peer {
		m.mu.Unlock()
		return nil, rpc.ErrClosed
	}
	if !prepare {
		peer.ResumeCalls()
		m.reloadUntil = time.Time{}
		m.mu.Unlock()
		return map[string]bool{"restart_ready": false}, nil
	}
	m.reloadUntil = time.Now().Add(m.reconnectGrace)
	peer.SuspendCalls()
	m.mu.Unlock()
	if err := peer.WaitPending(ctx); err != nil {
		m.mu.Lock()
		if m.peer == peer {
			m.reloadUntil = time.Time{}
			peer.ResumeCalls()
		}
		m.mu.Unlock()
		return nil, err
	}
	return map[string]bool{"restart_ready": true}, nil
}

func (m *Machine) expectedReload() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return time.Now().Before(m.reloadUntil) || m.warmLocked(time.Now())
}

// call retries only an explicit not-started refusal or a close before writing. An ambiguous dropped write is never
// replayed. The retry budget bounds waiting, not the operation's original execution deadline after the node is ready.
// Each call leaves a line in the log when it fails (calllog.go).
func (m *Machine) call(ctx context.Context, method string, params any, out any) (err error) {
	start, tries := time.Now(), 0
	defer func() { m.logCall(method, err, time.Since(start), tries) }()
	var deadline time.Time
	for {
		tries++
		err := m.callOnce(ctx, method, params, out)
		notSent := errors.Is(err, errOffline) || errors.Is(err, rpc.ErrSuspended) || errors.Is(err, rpc.ErrClosed) && !errors.Is(err, rpc.ErrDropped)
		if !restartingError(err) && !(notSent && m.expectedReload()) {
			return err
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(20 * time.Second)
		}
		if time.Until(deadline) <= 0 {
			return context.DeadlineExceeded
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(100*time.Millisecond, time.Until(deadline))):
		}
	}
}
