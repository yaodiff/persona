package node

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// Re-splitting the last week by directory (docs/server/machines.md "Token usage by project", #16530): the usage the
// manager stored before this node metered by directory is not split. Once, with a manager that keeps directories, the
// node re-reads its transcripts from a week ago to where its live window starts, by the ten minutes, with the live
// meter's rules (a reply counted once, the same launcher and agent) and the directory each session started in, and
// sends them with what the directories resolve to; the manager splits each stored ten minutes in those shares, its
// sums unchanged (devices/usage_resplit.go). Gentle like the backfill: after a delay, rate-limited, gone with the
// connection, and a cut one goes on from the last ten minutes the manager recorded. The manager may then name a
// second pass, the temporary folders' (resplitPassTemp, #16827).

// resplitDelay: after the connection, and after the meter has asked whether the manager keeps directories.
var resplitDelay = 4 * time.Minute

// resplitPassTemp is the re-split of the usage the manager stored under UsageDirTemp (#16827): a node that names a
// temporary folder by its path (usage_dirs.go) re-reads the week once more and sends the temporary folders' buckets,
// and the manager splits what it had under UsageDirTemp among them. Asked only by a node that says it names them
// (usage.resplit.status' temp_dirs): an older one's re-read still has UsageDirTemp, and would move nothing.
const resplitPassTemp = "temp"

// resplitPasses: the usage stored before directories, then that stored under UsageDirTemp; each at most once a
// connection.
const resplitPasses = 2

// resplitUsage asks the manager whether this machine's last week wants re-splitting and does it, pass by pass.
func (n *Node) resplitUsage(ctx context.Context, stateLoaded <-chan struct{}) {
	select {
	case <-ctx.Done():
		return
	case <-stateLoaded:
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(resplitDelay):
	}
	dirs := n.usageDirs.get()
	if n.peer == nil || dirs == nil { // the manager keeps no directories (or the meter is not up)
		return
	}
	for range resplitPasses {
		if !n.resplitPass(ctx, dirs) {
			return
		}
	}
}

// resplitPass asks the manager what is left to re-split and does it: false when nothing is (or the manager does not
// know it, or names a pass this node does not), or it was cut.
func (n *Node) resplitPass(ctx context.Context, dirs *usageDirs) bool {
	var st struct {
		backfillRange
		Pass string `json:"pass"` // "" the usage stored before directories, resplitPassTemp
	}
	ask := map[string]any{}
	if dirs.temps {
		ask["temp_dirs"] = true
	}
	cctx, cancel := context.WithTimeout(ctx, backfillCall)
	err := n.peer.Call(cctx, "usage.resplit.status", ask, &st)
	cancel()
	if err != nil || st.Done || st.Since >= st.Before { // an older manager does not know it
		return false
	}
	if st.Pass != "" && (st.Pass != resplitPassTemp || !dirs.temps) {
		return false
	}
	since, before := time.Unix(st.Since, 0), time.Unix(st.Before, 0)
	started := time.Now()
	lim := &readLimiter{rate: backfillRate, start: started}
	files := slices.DeleteFunc(n.backfillFiles(ctx, since, lim), func(f backfillFile) bool { return !tokenuse.FileMetered(f.ad.agent()) })
	queries := slices.DeleteFunc(n.usageQueries(), func(q usageQuery) bool { return !tokenuse.FileMetered(q.agent()) })
	buckets, err := n.usageScan(ctx, files, queries, since, before, lim, int64(resplitStep/time.Second))
	if err != nil {
		return false
	}
	if st.Pass == resplitPassTemp { // only the sessions that started in a temporary folder
		temp := map[string]bool{}
		buckets = slices.DeleteFunc(buckets, func(b proto.UsageBucket) bool {
			is, seen := temp[b.Dir]
			if !seen {
				is = n.usageTempKey(b.Dir)
				temp[b.Dir] = is
			}
			return !is
		})
	}
	resolved := map[string]*proto.UsageDir{}
	moved := 0
	for _, req := range resplitRequests(buckets, before) {
		if dirs := n.backfillDirs(ctx, req["buckets"].([]proto.UsageBucket), resolved); len(dirs) > 0 {
			req["dirs"] = dirs
		}
		if st.Pass != "" {
			req["pass"] = st.Pass
		}
		var res struct {
			Moved int `json:"moved"`
		}
		cctx, cancel := context.WithTimeout(ctx, backfillCall)
		err := n.peer.Call(cctx, "usage.resplit", req, &res)
		cancel()
		if err != nil { // the next connection asks again, from the ten minutes after the last recorded
			n.logger.Printf("usage re-split%s: %v (%d split)", resplitLabel(st.Pass), err, moved)
			return false
		}
		moved += res.Moved
	}
	n.logger.Printf("usage re-split%s: %d buckets read in %s, %d split", resplitLabel(st.Pass), len(buckets), time.Since(started).Round(time.Second), moved)
	return true
}

// resplitLabel names a pass in the log ("" for the first).
func resplitLabel(pass string) string {
	if pass == "" {
		return ""
	}
	return " (" + pass + ")"
}

// resplitStep: the ten minutes the manager keeps the last week in.
const resplitStep = 10 * time.Minute

// resplitRequests are the re-split's usage.resplit requests: its buckets with a directory (in time order) in whole ten
// minutes, each saying the last ten minutes it holds, the last the ten minutes before end, and done.
func resplitRequests(buckets []proto.UsageBucket, end time.Time) []map[string]any {
	keep := slices.DeleteFunc(slices.Clone(buckets), func(b proto.UsageBucket) bool { return b.Dir == "" })
	out := backfillRequestsSized(keep, end, true, backfillChunk)
	step := int64(resplitStep / time.Second)
	for _, req := range out {
		if !req["done"].(bool) {
			continue
		}
		req["through"] = end.Unix() - step
	}
	return out
}
