package nodeupdate

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodesign"
)

// The slowest Commit with a Signer: the upload's first check, then the certificate copy and the ad-hoc copy, each
// signed, verified and checked for as long as their limits allow, every process taking its extra second to exit.
// CommitWait has to cover it, or the hub's deploy and relay take the slow answer for a lost one (#12670).
func TestCommitWaitCoversTheCertificateToAdHocFallback(t *testing.T) {
	const exit = time.Second // cmd.WaitDelay of each check and each codesign run
	check := checkLimits.FirstRun + exit
	signedCopy := nodesign.SignLimit + 2*exit + check // codesign --sign, codesign --verify, node-update-check
	slowest := check + 2*signedCopy
	if CommitWait() <= slowest {
		t.Fatalf("CommitWait is %s; the slowest signed commit takes %s", CommitWait(), slowest)
	}
}
