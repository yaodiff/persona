package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/procinfo"
)

// BenchmarkLiveClaudes is one rebuild's read of the running claudes' files, at the 67 claudes of the machine in
// #7294 (a file of about 550 bytes each, for live processes, quiet for an hour).
func BenchmarkLiveClaudes(b *testing.B) {
	dir := b.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		b.Fatal(err)
	}
	ents, _ := os.ReadDir("/proc")
	old := time.Now().Add(-time.Hour)
	written := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || written == 67 {
			continue
		}
		mark := procinfo.StartMark(pid)
		if mark == "" {
			continue
		}
		body := fmt.Sprintf(`{"pid":%d,"sessionId":"0bd545f1-681d-4d98-bf5a-%012d","cwd":"/home/user/work/project","startedAt":1793955313493,`+
			`"procStart":%q,"version":"2.1.318","peerProtocol":1,"peerFeatures":["notify_idle","reply_across_default_dirs","artifact_yield"],`+
			`"kind":"interactive","entrypoint":"cli","pidDomain":"linux:91087b542079bb6603dfcb16684bc188:pid:[4026536678]",`+
			`"asksForConvertedSkills":true,"name":"project-a8","nameSource":"derived","nameSince":1793955313493,"status":"idle",`+
			`"statusUpdatedAt":1793955313493,"updatedAt":1793955313493,"messagingSocketPath":"/tmp/claude-peer-%d.sock"}`,
			pid, pid, strings.TrimPrefix(mark, "linux:"), pid)
		path := filepath.Join(sessions, fmt.Sprintf("%d.json", pid))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			b.Fatal(err)
		}
		_ = os.Chtimes(path, old, old)
		written++
	}
	if written < 67 {
		b.Skipf("only %d live processes", written)
	}
	n := &Node{sessionScope: &nodeSessionScope{claude: dir}}
	if got := len(n.liveClaudes()); got != 67 {
		b.Fatalf("%d live claudes, want 67", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		n.liveClaudes()
	}
}
