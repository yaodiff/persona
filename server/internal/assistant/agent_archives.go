package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const agentArchivesKey = "sessions.archived"
const agentArchiveTimeout = 3 * time.Second

type agentArchive struct {
	Session   proto.Session `json:"session"`
	Archived  bool          `json:"archived"`
	ChangedAt time.Time     `json:"changed_at"`
	// ClosedAt: "Archive and close" closed its agent then (agent_archive_close.go). Whatever the agent wrote while it
	// was being closed is not new activity: only activity after this moment restores the session.
	ClosedAt time.Time `json:"closed_at,omitempty"`
	// wrote is the archive's write that last changed the record (agentArchiveBook.writes), kept in memory only: 0 for
	// a record as loaded, written before anything this process read.
	wrote uint64
}

// activeAfter is the moment after which source activity restores the archived session. A row archived with no time of
// its own (an Antigravity CLI pane's row has none, #13710) counts activity from the archive on, not from the beginning
// of time: the conversation's own row, listed with its last change once the pane is gone, is not new activity (#14520).
func (rec agentArchive) activeAfter() time.Time {
	at := rec.Session.UpdatedAt
	if at.IsZero() {
		at = rec.ChangedAt
	}
	if rec.ClosedAt.After(at) {
		return rec.ClosedAt
	}
	return at
}

// archiveSessionOf is the id a row's archive record is kept under, its archive key: for an Antigravity CLI pane's row,
// the id its conversation is listed under once no pane shows it, agy-<conversation id>, which the row names first among
// its aliases (docs/protocol/protocol.md "Agent session archives"); else its own (#14520). The archive then follows the
// conversation to whichever row lists it next, and is not kept under a pane's id, which the engine gives to another
// conversation later.
func archiveSessionOf(s proto.Session) string {
	if aliases := s.Aliases(); s.Agent == "agy" && s.PaneID != "" && len(aliases) > 0 {
		return aliases[0]
	}
	return s.SessionID
}

// A just-closed confirmation can remain in a node's cached list until its next push.
// New work or a newer transcript still restores it; the old blocked snapshot cannot undo the close.
//
// mark is the archive's write count when s was read from the node's list (archiveMark). A row read before the
// record last changed says nothing about activity after that: a reader that read the list while the agent still
// worked or waited on the user, then waited for the archive's lock while the agent was closed and archived, would
// otherwise restore it for good (#4372). The next list read weighs it again.
func (rec agentArchive) active(s proto.Session, mark uint64) bool {
	if rec.wrote > mark {
		return false
	}
	return s.UpdatedAt.After(rec.activeAfter()) || agentArchiveBusy(s) && (rec.ClosedAt.IsZero() || s.Status != proto.StatusBlocked)
}

type agentArchiveBook struct {
	once     sync.Once
	gate     chan struct{}
	snapshot atomic.Pointer[map[string]agentArchive]
	loaded   bool
	records  map[string]agentArchive
	// Pending automatic restores survive subsequent idle snapshots if a database
	// write fails. They are retried; they are never reported as persisted success.
	pending map[string]bool
	// writes counts the record writes (saveAgentArchivesLocked), which stamp each record they change.
	writes atomic.Uint64
	// tails are the machines' archive-only rows as keys (archiveTail), under the gate.
	tails map[string]archiveTail
}

// archiveTail is the keys of a machine's records its node does not list, sorted: the rows its whole list appends
// after the node's (#6836). It stays good while the record set (records, never changed once published) and the
// listed keys that have a record are what it was made for: a list that changes in other rows, as on almost every
// push, reuses it instead of going through every record of the machine and sorting their keys again.
type archiveTail struct {
	records *map[string]agentArchive
	listed  string
	keys    []string
	// restored and archived are keys split by the record's Archived, in keys' order: the rows the apps see, and the
	// ones a hub that takes them apart gets in fleet.archived.
	restored, archived []string
}

// splitTail splits keys by their records' Archived (archiveTail).
func splitTail(records map[string]agentArchive, keys []string) (restored, archived []string) {
	for _, key := range keys {
		if records[key].Archived {
			archived = append(archived, key)
		} else {
			restored = append(restored, key)
		}
	}
	return restored, archived
}

// archiveMark is taken before reading a list the archive is applied to (archivedSessions): the records written
// after it are newer than anything the list shows.
func (svc *Service) archiveMark() uint64 { return svc.agentArchives.writes.Load() }

// Archive mutations serialize through a cancellable gate. Readers of published
// visibility metadata do not wait for a database operation holding the gate.
func (b *agentArchiveBook) lock(ctx context.Context) error {
	b.once.Do(func() { b.gate = make(chan struct{}, 1) })
	select {
	case b.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-b.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (b *agentArchiveBook) unlock() { <-b.gate }

func (svc *Service) loadAgentArchivesLocked(ctx context.Context) error {
	b := &svc.agentArchives
	if b.loaded {
		return nil
	}
	store := svc.archiveStore()
	if store == nil {
		return errors.New("no database")
	}
	records := map[string]agentArchive{}
	if _, err := store.Setting(ctx, agentArchivesKey, &records); err != nil {
		return err
	}
	if records == nil {
		records = map[string]agentArchive{}
	}
	for key, rec := range records {
		// A snapshot saved before titles were cut (#16343) can hold a whole opening prompt as its title: the archive's
		// rows go out with the cut one, and the next write saves it.
		if title := transcript.ListTitle(rec.Session.Title); title != rec.Session.Title {
			rec.Session.Title = title
			records[key] = rec
		}
	}
	b.records, b.pending, b.loaded = records, map[string]bool{}, true
	b.snapshot.Store(&records)
	return nil
}

func (svc *Service) saveAgentArchivesLocked(ctx context.Context, records map[string]agentArchive) error {
	store := svc.archiveStore()
	if store == nil {
		return errors.New("no database")
	}
	n := svc.agentArchives.writes.Add(1)
	for key, rec := range records {
		if old, ok := svc.agentArchives.records[key]; !ok || old.Archived != rec.Archived ||
			!old.ChangedAt.Equal(rec.ChangedAt) || !old.ClosedAt.Equal(rec.ClosedAt) {
			rec.wrote = n
			records[key] = rec
		}
	}
	if err := store.PutSetting(ctx, agentArchivesKey, records); err != nil {
		return err
	}
	svc.agentArchives.records = records
	svc.agentArchives.snapshot.Store(&records)
	return nil
}

func agentArchiveBusy(s proto.Session) bool {
	for _, child := range s.Subagents {
		if child.Running {
			return true
		}
	}
	for _, workflow := range s.Workflows {
		if workflow.Running {
			return true
		}
	}
	// Awaiting the user's reply (a finished turn that ends with a question, #5096) is not busy: the agent is idle,
	// and the user archiving it is the reply. "Delete chat" took those too, and archive replaces it.
	return s.Starting || s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked ||
		s.State == proto.StateWorking || s.State == proto.StateNeedsYou || s.BackgroundWorking ||
		s.BackgroundWaiting || s.PendingInteractions > 0 || len(s.Background) > 0
}

// archiveSnapshot contains identity and display metadata, not stale runtime
// controls. The source transcript remains on its computer and is read by ID.
func archiveSnapshot(s proto.Session) proto.Session {
	preview := []rune(s.LastText)
	if len(preview) > 180 {
		preview = preview[:180]
	}
	caps := &proto.SessionCapabilities{Read: true}
	if s.Capabilities != nil {
		caps.StopCause = s.Capabilities.StopCause
		caps.Terminal = s.Capabilities.Terminal
	}
	return proto.Session{
		MachineID: s.MachineID, SessionID: s.SessionID, Title: transcript.ListTitle(s.Title),
		Cwd: s.Cwd, WorkspaceCwd: s.WorkspaceCwd, RepoRoot: s.RepoRoot, Worktree: s.Worktree, Branch: s.Branch,
		Space: s.Space,
		Agent: s.Agent, AgentLabel: s.AgentLabel, AgentName: s.AgentName,
		SourceKind: s.SourceKind, SourceThreadID: s.SourceThreadID, SourceInstanceID: s.SourceInstanceID,
		ParentThreadID: s.ParentThreadID, ParentSessionID: s.ParentSessionID, RootSessionID: s.RootSessionID, IsSubagent: s.IsSubagent,
		UpdatedAt: s.UpdatedAt, LastText: string(preview),
		ReadOnlyReason: s.ReadOnlyReason,
		Status:         proto.StatusOffline, State: proto.StateOffline,
		Capabilities: caps,
	}
}

// archivedSessions overlays the manager's durable visibility on the current
// inventory and retains old archived/restored rows beyond its seven-day window.
// Observed new activity permanently restores a row, including a busy-to-idle
// transition with no timestamp change. Only that transition writes the database.
// mark is archiveMark taken before list was read: a record changed after it is not
// weighed against the list (agentArchive.active).
func (svc *Service) archivedSessions(ctx context.Context, machine string, list []proto.Session, mark uint64) ([]proto.Session, bool, error) {
	a, changed, err := svc.applyArchives(ctx, machine, list, mark, true)
	return a.whole(), changed, err
}

// archivedListed is archivedSessions without the rows only the archive keeps: list's own rows with the archive's
// marks, and its restores, for a reader of rows the node lists (listedSession, listedSessions). The records the
// machine has gathered over months are not copied for it.
func (svc *Service) archivedListed(ctx context.Context, machine string, list []proto.Session, mark uint64) ([]proto.Session, error) {
	a, _, err := svc.applyArchives(ctx, machine, list, mark, false)
	return a.list, err
}

// archiveApplied is the archive applied to a machine's list: the list's own rows with the archive's marks, and, when
// asked for, the keys of the rows only the archive has (tail) with the records they are in. records is never changed
// once published (saveAgentArchivesLocked replaces it), so it is read after the archive's lock is let go.
type archiveApplied struct {
	list    []proto.Session
	records map[string]agentArchive
	tail    []string
	// restored and archived are tail split by the records' Archived (archiveTail).
	restored, archived []string
	// of and listed are what the tail was made for (archiveTail): the record set's identity and the listed keys with
	// a record, in list order.
	of     *map[string]agentArchive
	listed string
}

// whole is the machine's whole list: its own rows, then the archive's rows in tail's order. It grows list once.
func (a archiveApplied) whole() []proto.Session {
	if len(a.tail) == 0 {
		return a.list
	}
	list := slices.Grow(a.list, len(a.tail))
	for _, key := range a.tail {
		list = append(list, archiveRow(a.records[key]))
	}
	return list
}

// applyArchives applies the archive to list (archivedSessions); rest: the keys of the archive's rows list does not
// have are wanted too.
func (svc *Service) applyArchives(ctx context.Context, machine string, list []proto.Session, mark uint64, rest bool) (archiveApplied, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
	defer cancel()
	b := &svc.agentArchives
	// listed stays a variable of its own until the result is made: escape analysis follows a struct as one, and
	// listed leaks (tailKeys keeps parts of it), which would take the caller's list to the heap with it.
	if err := b.lock(ctx); err != nil {
		records := b.snapshot.Load()
		if records == nil {
			return archiveApplied{list: list}, false, err
		}
		listed := markAgentArchives(machine, list, *records, nil, mark)
		var t archiveTail
		if rest {
			t.keys = tailKeys(machine, *records, listed)
			t.restored, t.archived = splitTail(*records, t.keys)
		}
		return archiveApplied{list: list, records: *records, of: records, listed: listed, tail: t.keys, restored: t.restored,
			archived: t.archived}, false, err
	}
	defer b.unlock()
	if err := svc.loadAgentArchivesLocked(ctx); err != nil {
		return archiveApplied{list: list}, false, err
	}
	if len(b.records) == 0 {
		return archiveApplied{list: list, of: b.snapshot.Load()}, false, nil
	}
	var next map[string]agentArchive
	var listedBuf strings.Builder
	for i := range list {
		var listedOut *strings.Builder
		if next == nil {
			listedOut = &listedBuf
		}
		rec, ok := rowArchive(machine, &list[i], b.records, listedOut)
		if !ok {
			continue
		}
		if rec.Archived {
			// The restore goes under the row's archive key, newer than the record that archived it (under that key, or
			// under an id the row went by, which stays as it was).
			active := rec.active(list[i], mark)
			if active || len(b.pending) > 0 {
				sid := archiveSessionOf(list[i])
				key := runtimeListID(machine, sid)
				if active {
					b.pending[key] = true
				}
				if b.pending[key] {
					restored := rec
					restored.Archived = false
					restored.ChangedAt = nextAgentArchiveChangedAt(restored.ChangedAt)
					restored.Session = archiveSnapshot(list[i])
					restored.Session.MachineID, restored.Session.SessionID = machine, sid
					if next == nil {
						next = maps.Clone(b.records)
					}
					next[key] = restored
					continue
				}
			}
		}
		if next == nil && !list[i].Assistant {
			list[i].Archived = rec.Archived
			list[i].ArchiveChangedAt = archiveChangedAt(rec.ChangedAt)
			overlayClosedArchive(&list[i], rec) // as markAgentArchives does
		}
	}
	var saveErr error
	var listed string
	if next != nil {
		saveErr = svc.saveAgentArchivesLocked(ctx, next)
		if saveErr == nil {
			for key := range b.pending {
				if rec, ok := next[key]; ok && !rec.Archived {
					delete(b.pending, key)
				}
			}
		}
		listed = markAgentArchives(machine, list, b.records, b.pending, mark)
	} else {
		listed = listedBuf.String()
	}
	of := b.snapshot.Load()
	var t archiveTail
	if rest {
		t = b.tailFor(machine, of, b.records, listed)
	}
	return archiveApplied{list: list, records: b.records, of: of, listed: listed, tail: t.keys, restored: t.restored,
		archived: t.archived}, next != nil && saveErr == nil, saveErr
}

// archivedInventory is machine's whole list with the archive applied (archivedSessions), its children folded under
// their parents among the rows left listed (foldInventory).
func (svc *Service) archivedInventory(ctx context.Context, machine string) ([]proto.Session, bool, error) {
	return svc.archivedInventoryAtMark(ctx, machine, svc.archiveMark())
}

// archivedInventoryAtMark applies only activity visible at mark, so a publisher cannot undo an archive written after
// the operation whose publication it is finishing.
func (svc *Service) archivedInventoryAtMark(ctx context.Context, machine string, mark uint64) ([]proto.Session, bool, error) {
	list, changed, err := svc.archivedSessions(ctx, machine, svc.decoratedUnarchived(machine), mark)
	svc.foldInventory(machine, list)
	return list, changed, err
}

// archivedRow is the row machine's whole list (archivedInventory) has for session sid when its node does not list it:
// its archive record's, found by its key instead of by building the list with every record of the machine. Its fold
// is the caller's (foldSubset).
func (svc *Service) archivedRow(ctx context.Context, machine, sid string) (proto.Session, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
	defer cancel()
	b := &svc.agentArchives
	if err := b.lock(ctx); err != nil { // as archivedSessions: the records last published
		if records := b.snapshot.Load(); records != nil {
			row, ok := archiveRowOf(machine, sid, *records)
			return row, ok, err
		}
		return proto.Session{}, false, err
	}
	defer b.unlock()
	if err := svc.loadAgentArchivesLocked(ctx); err != nil {
		return proto.Session{}, false, err
	}
	row, ok := archiveRowOf(machine, sid, b.records)
	return row, ok, nil
}

// archiveRowOf is the row overlayAgentArchives appends for machine's session sid, when records has one.
func archiveRowOf(machine, sid string, records map[string]agentArchive) (proto.Session, bool) {
	rec, ok := lookupRuntimeListID(records, machine, sid)
	if !ok || rec.Session.MachineID != machine {
		return proto.Session{}, false
	}
	return archiveRow(rec), true
}

// archiveRow is a record's row in its machine's list.
func archiveRow(rec agentArchive) proto.Session {
	s := rec.Session
	s.Archived = rec.Archived
	s.ArchiveChangedAt = archiveChangedAt(rec.ChangedAt)
	return s
}

// markAgentArchives puts the archive's marks on list's rows, each row's from the newest record among its own id and its
// aliases (rowArchive). It returns the keys of list's rows that have a record,
// and of the ids their conversations went by that have one (proto.Session.Aliases, #8131), in list order, one per
// line: what the rows only the archive has depend on (tailKeys) besides the records. Paseo's conversations were
// listed under their transcripts once, and an archive record kept then repeated the row listed under its own id
// now; the record stays, and is shown again if no row names its id any more.
func markAgentArchives(machine string, list []proto.Session, records map[string]agentArchive, pending map[string]bool, mark uint64) string {
	var listed strings.Builder
	for i := range list {
		rec, ok := rowArchive(machine, &list[i], records, &listed)
		if !ok || list[i].Assistant {
			continue
		}
		// A storage timeout must neither reveal unrelated archived rows nor hide
		// new work. The durable restore remains pending until its write succeeds.
		active := rec.active(list[i], mark)
		restoring := false
		if len(pending) > 0 {
			restoring, _ = lookupRuntimeListID(pending, machine, archiveSessionOf(list[i])) // under its archive key
		}
		list[i].Archived = rec.Archived && !restoring && !active
		list[i].ArchiveChangedAt = archiveChangedAt(rec.ChangedAt)
		overlayClosedArchive(&list[i], rec)
	}
	return listed.String()
}

// overlayClosedArchive shows an archived row whose Agent was stopped and closed with a reason (a closed task's,
// archiveRetiredTaskAgent) as that archive: the reason, offline, read-only, its terminal still viewable and the stop
// cause the node reports, without live controls. A row that works again keeps what its node says.
func overlayClosedArchive(row *proto.Session, rec agentArchive) {
	if !row.Archived || rec.ClosedAt.IsZero() || rec.Session.ReadOnlyReason == "" || agentArchiveBusy(*row) {
		return
	}
	row.ReadOnlyReason = rec.Session.ReadOnlyReason
	row.Status, row.State = proto.StatusOffline, proto.StateOffline
	caps := &proto.SessionCapabilities{Read: true, Terminal: row.PaneID != ""}
	if current := row.Capabilities; current != nil {
		caps.Terminal = caps.Terminal || current.Terminal
		caps.StopCause = current.StopCause
	}
	row.Capabilities = caps
}

func lookupArchiveKey(machine, session string, records map[string]agentArchive, listed *strings.Builder) (agentArchive, bool) {
	var buf [128]byte
	if len(machine)+len(session)+7 <= len(buf) {
		if key, ok := appendRuntimeListID(buf[:0], machine, session); ok {
			r, found := records[string(key)]
			if found && listed != nil {
				listed.Write(key)
				listed.WriteByte('\n')
			}
			return r, found
		}
	}
	key := runtimeListID(machine, session)
	r, found := records[key]
	if found && listed != nil {
		listed.WriteString(key)
		listed.WriteByte('\n')
	}
	return r, found
}

// rowArchive is the record that says whether s, listed on machine, is archived: the newest among those under its own
// id and its aliases, its own on a tie; ok false when there is none. An archive made under an id the conversation went
// by holds for the row listed now (#14395): an Antigravity CLI pane's row, closed since, or a pane's own id an archive
// was kept under before #14520. Activity in the row restores it under the row's archive key (applyArchives), and a
// record under another id stays as it was, as Paseo's do (#8131). listed, when not nil, gets the key of every record
// found, one per line, the aliases' first.
func rowArchive(machine string, s *proto.Session, records map[string]agentArchive, listed *strings.Builder) (rec agentArchive, ok bool) {
	for _, alias := range s.Aliases() {
		if r, found := lookupArchiveKey(machine, alias, records, listed); found {
			if !ok || r.ChangedAt.After(rec.ChangedAt) {
				rec, ok = r, true
			}
		}
	}
	if r, found := lookupArchiveKey(machine, s.SessionID, records, listed); found {
		if !ok || !rec.ChangedAt.After(r.ChangedAt) {
			rec, ok = r, true
		}
	}
	return rec, ok
}

// tailKeys are the keys of machine's records that are not among listed (markAgentArchives), sorted: stable
// inventories avoid needless delta/list revisions.
func tailKeys(machine string, records map[string]agentArchive, listed string) []string {
	seen := map[string]bool{}
	for key := range strings.Lines(listed) {
		seen[strings.TrimSuffix(key, "\n")] = true
	}
	var keys []string
	for key, rec := range records {
		if !seen[key] && rec.Session.MachineID == machine {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// tailFor is machine's archiveTail, kept in b.tails while records (of) and listed are the same. The gate is held.
func (b *agentArchiveBook) tailFor(machine string, of *map[string]agentArchive, records map[string]agentArchive, listed string) archiveTail {
	if t, ok := b.tails[machine]; ok && t.records == of && t.listed == listed {
		return t
	}
	t := archiveTail{records: of, listed: listed, keys: tailKeys(machine, records, listed)}
	t.restored, t.archived = splitTail(records, t.keys)
	if b.tails == nil {
		b.tails = map[string]archiveTail{}
	}
	b.tails[machine] = t
	return t
}

// apart is the whole list told apart (Hello.ArchivedApart, #6836): the rows the apps see, in the order the whole list
// has them (the list's own rows, then the archive's restored rows), and the archived ones (archivedPart). The hub's
// Hub.shown leaves exactly those out of the whole list.
func (a archiveApplied) apart() ([]proto.Session, archivedPart) {
	part := archivedPart{records: a.records, keys: a.archived, of: a.of, listed: a.listed}
	shown := a.list
	for i := range a.list {
		if a.list[i].Archived && !a.list[i].Assistant {
			shown = make([]proto.Session, 0, len(a.list)-1+len(a.restored))
			for j := range a.list {
				if a.list[j].Archived && !a.list[j].Assistant {
					part.rows = append(part.rows, a.list[j])
				} else {
					shown = append(shown, a.list[j])
				}
			}
			break
		}
	}
	shown = slices.Grow(shown, len(a.restored))
	for _, key := range a.restored {
		shown = append(shown, archiveRow(a.records[key]))
	}
	return shown, part
}

// archivedPart is a machine's archived rows told apart: the rows of its node's list marked archived (rows, in list
// order), and the keys of the archive's own archived rows (keys, sorted), made into rows only when they are told
// (tellArchived). of and listed are what keys were made for (archiveTail).
type archivedPart struct {
	rows    []proto.Session
	records map[string]agentArchive
	keys    []string
	of      *map[string]agentArchive
	listed  string
}

func (svc *Service) archiveStore() Store {
	return svc.store()
}

func runtimeListIDSafe(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

func appendRuntimeListID(dst []byte, machine, session string) ([]byte, bool) {
	if !runtimeListIDSafe(machine) || !runtimeListIDSafe(session) {
		return nil, false
	}
	dst = append(dst, '[', '"')
	dst = append(dst, machine...)
	dst = append(dst, '"', ',', '"')
	dst = append(dst, session...)
	dst = append(dst, '"', ']')
	return dst, true
}

func lookupRuntimeListID[V any](m map[string]V, machine, session string) (V, bool) {
	var buf [128]byte
	if len(machine)+len(session)+7 <= len(buf) {
		if key, ok := appendRuntimeListID(buf[:0], machine, session); ok {
			v, found := m[string(key)]
			return v, found
		}
	}
	v, found := m[runtimeListID(machine, session)]
	return v, found
}

func runtimeListID(machine, session string) string {
	if runtimeListIDSafe(machine) && runtimeListIDSafe(session) {
		return `["` + machine + `","` + session + `"]`
	}
	raw, _ := json.Marshal([2]string{machine, session})
	return string(raw)
}

// decorated applies manager-owned archive visibility to its final agent states.
func (svc *Service) decorated(machine string) []proto.Session {
	list, _, err := svc.archivedInventory(context.Background(), machine)
	if err != nil {
		svc.logf("load agent archives: %v", err)
	}
	return list
}

// decoratedInto is decorated with the machine's rows copied into buf's array instead of a new one (liveLoop's own
// list of the machine from its last round, fleetInputsInto). The node's rows overwrite the array's whole before they
// are decorated, so nothing of its last decoration stays; the list returned is buf's array unless it outgrew it. Its
// rows are folded as archivedInventory folds them, and the fold is kept for the machine the same way.
func (svc *Service) decoratedInto(machine string, buf []proto.Session) []proto.Session {
	mark := svc.archiveMark() // before the list is read, as archivedInventory
	list := buf[:0]
	if svc.devs != nil {
		list, _ = svc.devs.AppendSessions(list, machine)
	}
	list, _, err := svc.archivedSessions(context.Background(), machine, svc.decorateInventory(machine, list, true), mark)
	svc.foldInventory(machine, list)
	if err != nil {
		svc.logf("load agent archives: %v", err)
	}
	return list
}

func (svc *Service) listArchivedSessions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), agentArchiveTimeout)
	defer cancel()
	mid := r.URL.Query().Get("machine_id")
	if mid != "" {
		if m, ok := svc.fleetMachine(mid); !ok {
			apierr.Write(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
			return
		} else {
			mid = m.ID
		}
	}
	out := []proto.Session{}
	ext := r.Header.Get(RoleHeader) == roleExtension // never a conversation of a space hidden from extensions (docs/extensions/extensions.md)
	for _, m := range svc.fleetMachines() {
		if mid != "" && mid != m.ID {
			continue
		}
		list, changed, err := svc.archivedInventory(ctx, m.ID)
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, i18n.Err("hub-agent_archives.storage"))
			return
		}
		if changed {
			svc.publishList(m.ID)
		}
		for _, row := range list {
			if row.Archived && !row.Assistant && (!row.HiddenByDirectory() || r.URL.Query().Get("include_hidden") == "1") && !(ext && svc.extensionHides(ctx, runSpace(row))) {
				row.Manager = svc.id
				row.NativeArchive = svc.nativeArchiveView(m.ID, archiveSessionOf(row))
				out = append(out, row)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return runtimeListID(out[i].MachineID, out[i].SessionID) < runtimeListID(out[j].MachineID, out[j].SessionID)
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	apierr.WriteJSON(w, map[string]any{"sessions": out})
}

// agentArchiveError is a refusal of an archive operation: the HTTP status and code the apps get, and its message.
type agentArchiveError struct {
	status int
	code   string
	err    error
}

func (e *agentArchiveError) Error() string { return e.err.Error() }
func (e *agentArchiveError) Unwrap() error { return e.err }

func archiveRefusal(status int, code string, err error) error {
	return &agentArchiveError{status: status, code: code, err: err}
}

// writeAgentArchiveError answers an archive operation's error: its refusal, else a storage failure.
func (svc *Service) writeAgentArchiveError(w http.ResponseWriter, err error) {
	var refusal *agentArchiveError
	if errors.As(err, &refusal) {
		apierr.WriteCode(w, refusal.status, refusal.code, refusal.err)
		return
	}
	svc.logf("agent archive: %v", err)
	apierr.Write(w, http.StatusInternalServerError, i18n.Err("hub-agent_archives.storage"))
}

// agentArchiveTarget is machine mid's session sid as the archive sees it (the overlay applied), with the canonical
// machine id; a refusal when there is no such machine or session, or it is an assistant conversation (those archive
// in their own API). Every other agent, whatever its runtime, archives the same way.
func (svc *Service) agentArchiveTarget(ctx context.Context, mid, sid string) (proto.Session, error) {
	m, ok := svc.fleetMachine(mid)
	if !ok {
		return proto.Session{}, archiveRefusal(http.StatusNotFound, "", i18n.Err("devices-api.unknown_machine"))
	}
	list, _, err := svc.archivedInventory(ctx, m.ID)
	if err != nil {
		return proto.Session{}, err
	}
	for _, row := range list {
		if row.SessionID != sid {
			continue
		}
		if row.Assistant {
			return proto.Session{}, archiveRefusal(http.StatusBadRequest, "", i18n.Err("hub-agent_archives.unsupported"))
		}
		row.MachineID = m.ID
		return row, nil
	}
	return proto.Session{}, archiveRefusal(http.StatusNotFound, "", i18n.Err("hub-deleted_sessions.unknown"))
}

// setAgentArchive archives (or restores) machine mid's session sid: the one way every caller changes an agent's
// archive state (the apps' routes, "Archive and close", the assistant's tools). A busy session is refused (409).
// The record is the manager's; the session's source stays as it is.
func (svc *Service) setAgentArchive(ctx context.Context, mid, sid string, archived bool) (proto.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
	defer cancel()
	row, err := svc.agentArchiveTarget(ctx, mid, sid)
	if err != nil {
		return row, err
	}
	if archived && agentArchiveBusy(row) {
		return row, archiveBusyRefusal(row, false)
	}
	changedAt, err := svc.noteAgentArchiveAt(ctx, row, archived)
	if err != nil {
		return row, err
	}
	row.ArchiveChangedAt = archiveChangedAt(changedAt)
	svc.publishList(row.MachineID)
	return row, nil
}

func (svc *Service) setAgentSessionArchive(w http.ResponseWriter, r *http.Request, mid string, archived bool) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, i18n.Err("hub-agent_archives.forbidden"))
		return
	}
	if archived && r.URL.Query().Get("force") == "1" {
		if err := svc.forceArchiveConfirmation(r.Context(), mid, r.PathValue("sid")); err != nil {
			svc.writeAgentArchiveError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "archived": true, "close": map[string]any{"closed": true, "outcome": archiveCloseClosed}})
		return
	}
	if archived && r.URL.Query().Get("close") == "1" {
		svc.archiveAndCloseHTTP(w, r, mid)
		return
	}
	row, err := svc.setAgentArchive(r.Context(), mid, r.PathValue("sid"), archived)
	if err != nil {
		svc.writeAgentArchiveError(w, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"ok": true, "native": svc.nativeArchiveAwaited(r.Context(), row.MachineID, archiveSessionOf(row))}) // agent_archive_native.go
}

func (svc *Service) noteAgentArchive(ctx context.Context, s proto.Session, archived bool) error {
	_, err := svc.noteAgentArchiveAt(ctx, s, archived)
	return err
}

// nextAgentArchiveChangedAt returns the next per-session archive generation. The archive gate serializes callers;
// using the persisted previous marker keeps it strictly increasing when the wall clock repeats or moves backwards.
func nextAgentArchiveChangedAt(previous time.Time) time.Time {
	now := time.Now().UTC()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}

// noteAgentArchiveAt also returns the generation it wrote: a close started for this archive must complete only for
// that same generation, not for a restore or re-archive written while its node call was out.
func (svc *Service) noteAgentArchiveAt(ctx context.Context, s proto.Session, archived bool) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
	defer cancel()
	b := &svc.agentArchives
	if err := b.lock(ctx); err != nil {
		return time.Time{}, err
	}
	defer b.unlock()
	if err := svc.loadAgentArchivesLocked(ctx); err != nil {
		return time.Time{}, err
	}
	snapshot := archiveSnapshot(s)
	snapshot.SessionID = archiveSessionOf(s) // a pane's row: its conversation's own id
	key := runtimeListID(s.MachineID, snapshot.SessionID)
	next := maps.Clone(b.records)
	changedAt := nextAgentArchiveChangedAt(b.records[key].ChangedAt)
	next[key] = agentArchive{Session: snapshot, Archived: archived, ChangedAt: changedAt}
	if err := svc.saveAgentArchivesLocked(ctx, next); err != nil {
		return time.Time{}, err
	}
	delete(b.pending, key)
	svc.nativeArchiveWanted(snapshot, archived) // the tool's own archive follows (agent_archive_native.go)
	return changedAt, nil
}

func (svc *Service) agentArchiveRoutes() {
	svc.handle("GET /api/v1/sessions/archived", svc.listArchivedSessions)
	svc.handle("POST /api/v1/sessions/{mid}/{sid}/archive", func(w http.ResponseWriter, r *http.Request) {
		svc.setAgentSessionArchive(w, r, r.PathValue("mid"), true)
	})
	svc.handle("POST /api/v1/sessions/{mid}/{sid}/unarchive", func(w http.ResponseWriter, r *http.Request) {
		svc.setAgentSessionArchive(w, r, r.PathValue("mid"), false)
	})
}

// archiveChangedAt is a record's change time as a session carries it (proto.Session.ArchiveChangedAt): nil for none.
func archiveChangedAt(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
