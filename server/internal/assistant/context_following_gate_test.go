package assistant

import (
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func followingGateSnapshot(t *testing.T, h *inboxHub) string {
	t.Helper()
	done := make(chan struct{})
	h.Service.takeSnapshot(done)
	<-done
	if h.Service.live.snap == nil {
		t.Fatal("takeSnapshot did not store a snapshot")
	}
	return fitSnapshot(*h.Service.live.snap)
}

func TestSnapshotFollowingEnabledEmptyRendersExistingEmptyState(t *testing.T) {
	h := newInboxHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })

	text := followingGateSnapshot(t, h)
	const want = "- 在跟的事：没有"
	var sectionLines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "- 在跟的事") {
			sectionLines = append(sectionLines, line)
		}
	}
	if len(sectionLines) != 1 || sectionLines[0] != want {
		t.Fatalf("Following up section lines = %q, want exactly %q:\n%s", sectionLines, want, text)
	}
}

func TestSnapshotFollowingDisabledOmitsSectionAndItemTitles(t *testing.T) {
	h := newInboxHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	waiting := followingReviewItem("gate-disabled", &InboxFollow{Who: "对方", Next: "再问一次"},
		&InboxHandling{State: HandlingWaiting}, TierAction)
	h.inbox.mu.Lock()
	h.inbox.items[waiting.ID] = waiting
	h.inbox.mu.Unlock()

	text := followingGateSnapshot(t, h)
	if strings.Contains(text, "- 在跟的事") {
		t.Errorf("a disabled inbox still has a Following up section:\n%s", text)
	}
	if strings.Contains(text, waiting.Title) {
		t.Errorf("a disabled inbox leaked the waiting matter title %q:\n%s", waiting.Title, text)
	}
}

func TestSnapshotFollowingStoreUnavailableOmitsSection(t *testing.T) {
	h := newInboxHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	h.st.imu.Lock()
	h.st.fail = errors.New("inbox store unavailable")
	h.st.imu.Unlock()

	text := followingGateSnapshot(t, h)
	if strings.Contains(text, "- 在跟的事") {
		t.Fatalf("an unavailable inbox store produced a Following up section instead of leaving it out:\n%s", text)
	}
}
