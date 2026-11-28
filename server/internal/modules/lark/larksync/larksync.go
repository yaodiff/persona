// Package larksync reads what changed for one Lark user sync binding: the owner's chats (with their threads and
// attachments) and message search, Drive, Docx, Sheets, Bitable, Wiki and Minutes, read as the owner with their user
// access token (internal/larksyncwire, docs/extensions/bots.md "Reading as you: daily sync").
//
// The module owns the open platform here; the manager owns the state. A run never writes anything itself: it asks the
// dedup index what it already holds (Index.Lookup), decides per canonical key (larksyncwire.Decide), and hands back
// only what is new or newer together with the cursors to save and the index records to write (staged on the index
// when it can stage, see stageRecords), so a run that fails to hand its result over moves nothing.
//
//persona:layer application
package larksync

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// Defaults for a binding that leaves them out.
const (
	defaultBudget   = 20 * time.Minute
	defaultBackfill = 30 * 24 * time.Hour
)

// Syncer reads one binding. One Syncer per binding: its rate limits (limiter) are its own, so a busy binding never
// slows another down. Safe for concurrent use (a Run and the knowledge base's attachment fetches through Media).
type Syncer struct {
	http  *http.Client
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	// jitter is a random duration in [0, d) added to exponential backoff so retries from several bindings spread out.
	jitter func(d time.Duration) time.Duration
	lim    *limiter
}

var (
	_ larksyncwire.Syncer                = (*Syncer)(nil)
	_ larksyncwire.NameResolver          = (*Syncer)(nil)
	_ larksyncwire.ChatNameResolver      = (*Syncer)(nil)
	_ larksyncwire.PendingLinkBackfiller = (*Syncer)(nil)
)

// NewSyncer is a binding's reader. httpClient nil uses a client with a 60 s timeout; now nil is time.Now.
func NewSyncer(httpClient *http.Client, now func() time.Time) *Syncer {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	s := &Syncer{http: httpClient, now: now, sleep: sleepCtx, jitter: randJitter}
	s.lim = newLimiter(func() time.Time { return s.now() })
	return s
}

// Run reads what changed since cursors (larksyncwire.Syncer). Every resource is read on its own: one that fails
// costs only its own content and cursor (a ResourceError), except a refused token, which stops the run with an
// *larksyncwire.Error of ErrKindAuth (or the token func's own *Error) next to what was finished before it.
func (s *Syncer) Run(ctx context.Context, b larksyncwire.Binding, token func(ctx context.Context) (string, error), cursors map[string]string, idx larksyncwire.Index) (larksyncwire.Result, error) {
	if token == nil {
		return larksyncwire.Result{}, &larksyncwire.Error{Kind: larksyncwire.ErrKindAuth, Op: "run", Msg: "no token"}
	}
	budget := cmpDuration(b.Budget, defaultBudget)
	// The budget is counted on the syncer's clock (tests drive it); the context bounds what a single request can
	// still take once the budget is gone in real time.
	runCtx, cancel := context.WithTimeout(ctx, budget+time.Minute)
	defer cancel()
	r := newRun(s, b, token, cursors, idx, s.now().Add(budget))
	err := r.read(runCtx)
	switch {
	case err != nil && ctx.Err() != nil:
		return larksyncwire.Result{}, ctx.Err() // the caller gave up: nothing of this run counts
	case err != nil && runCtx.Err() != nil:
		r.partial, err = true, nil // the budget's safety net: what finished counts, the rest is read next time
	}
	res, ferr := r.finish(ctx)
	if ferr != nil {
		return larksyncwire.Result{}, ferr
	}
	return res, err
}

// read runs the binding's readers in order. In full mode, IM and search come first so the documents and minutes their
// messages link to are found; Drive and Wiki follow, then linked cloud documents and minutes. In IM incremental mode
// (larksyncwire.ModeIM), only IM chats, p2p chat discovery, and the cloud documents and minutes linked in those new
// messages run, with reserved budget slices so a busy chat backlog cannot starve p2p discovery or linked docs.
func (r *run) read(ctx context.Context) error {
	defer r.saveNameBook()
	if r.b.Mode == larksyncwire.ModeIM {
		if r.b.Tier != larksyncwire.TierFull {
			return nil
		}
		budget := r.deadline.Sub(r.start)
		steps := []struct {
			fn       func(context.Context) error
			deadline time.Time
		}{
			{r.readIM, r.start.Add(budget * 60 / 100)},
			{r.readP2PDiscovery, r.start.Add(budget * 80 / 100)},
			{r.readLinkedDocs, r.deadline},
			{r.readMinutes, r.deadline},
		}
		for _, st := range steps {
			if !r.s.now().Before(r.deadline) {
				r.partial = true
				return nil
			}
			r.stepDeadline = st.deadline
			r.sess.deadline = st.deadline
			err := st.fn(ctx)
			r.stepDeadline = time.Time{}
			r.sess.deadline = r.deadline
			if err != nil {
				return err
			}
		}
		return nil
	}
	steps := []func(context.Context) error{r.readIM, r.readSearch, r.readDrive, r.readWiki, r.readLinkedDocs, r.readMinutes}
	for _, step := range steps {
		if r.overBudget() {
			r.partial = true
			return nil
		}
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ResolveUserNames looks up Lark user display names by open_id using the binding's user token and per-binding rate
// limiter (larksyncwire.NameResolver). Non-user ids (such as cli_* bots) and per-user errors are skipped; a missing
// contact scope, refused token, or cancelled context stops lookups and returns that error alongside whatever names were
// already resolved.
func (s *Syncer) ResolveUserNames(ctx context.Context, b larksyncwire.Binding, token func(ctx context.Context) (string, error), openIDs []string) (map[string]string, error) {
	if token == nil {
		return nil, &larksyncwire.Error{Kind: larksyncwire.ErrKindAuth, Op: "user name", Msg: "no token"}
	}
	budget := cmpDuration(b.Budget, 2*time.Minute)
	deadline := s.now().Add(budget)
	runCtx, cancel := context.WithTimeout(ctx, budget+30*time.Second)
	defer cancel()
	sess := &session{s: s, base: botcard.ResolveDomain(b.App.Domain), token: token, deadline: deadline}
	out := map[string]string{}
	seen := map[string]bool{}
	for _, id := range openIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || !isContactUser(id, "") {
			continue
		}
		seen[id] = true
		if !s.now().Before(deadline) {
			return out, context.DeadlineExceeded
		}
		var data struct {
			User struct {
				Name string `json:"name"`
			} `json:"user"`
		}
		err := sess.getJSON(runCtx, request{
			family: famIM,
			op:     "user name",
			path:   "/open-apis/contact/v3/users/" + pathEsc(id),
			query:  map[string]string{"user_id_type": "open_id"},
		}, &data)
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if runCtx.Err() != nil {
				return out, runCtx.Err()
			}
			if fatal(err) {
				return out, err
			}
			if e := asSyncError(err); e != nil && e.Kind == larksyncwire.ErrKindScope {
				return out, err
			}
			continue
		}
		if name := strings.TrimSpace(data.User.Name); name != "" {
			out[id] = name
		}
	}
	return out, nil
}

// ResolveNamesWithFallback resolves sender display names and unnamed p2p chat names for a binding's sender-name
// backfill (larksyncwire.ChatNameResolver) using the binding's user token and per-binding rate limiter:
//  1. contact/v3/users/:open_id for human senders (same-tenant directory);
//  2. GET /open-apis/im/v1/chats/:chat_id/members cached per chat when contact/v3 misses external users;
//  3. GET /open-apis/im/v1/chats/:chat_id + chat members for unnamed p2p chats (peer name + bot DM name);
//  4. Message-carried sender/mention names via GET /open-apis/im/v1/messages/:id for remaining unresolved senders.
//
// A missing chat-members scope silently disables only the chat-members fallback; a missing contact scope stops
// contact/v3 calls and is returned alongside partial results if any requested human open_id remains unresolved.
func (s *Syncer) ResolveNamesWithFallback(ctx context.Context, b larksyncwire.Binding, token func(ctx context.Context) (string, error), req larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
	out := larksyncwire.NameResolveResult{
		UserNames:    map[string]string{},
		P2PChatNames: map[string]string{},
	}
	if token == nil {
		return out, &larksyncwire.Error{Kind: larksyncwire.ErrKindAuth, Op: "user name", Msg: "no token"}
	}
	budget := cmpDuration(b.Budget, 2*time.Minute)
	deadline := s.now().Add(budget)
	runCtx, cancel := context.WithTimeout(ctx, budget+30*time.Second)
	defer cancel()
	sess := &session{s: s, base: botcard.ResolveDomain(b.App.Domain), token: token, deadline: deadline}
	overBudget := func() bool { return !s.now().Before(deadline) }
	owner := openIDOf(b.AccountID)
	now := s.now()
	prog := parseNamesProgress(req.Progress, now)
	// tracked are the member list scans this round keeps in prog: the chat's senders they were checked for, since when.
	type trackedScan struct {
		seen []string
		at   int64
	}
	tracked := map[string]trackedScan{}
	chatMembers := map[string]*chatMemberList{}
	// finish ends the round: it keeps the progress and the names learned, and turns the budget running out into a
	// partial round, which is not an error (the next round resumes).
	finish := func(err error) (larksyncwire.NameResolveResult, error) {
		for chatID, t := range tracked {
			if l := chatMembers[chatID]; l != nil && !l.denied {
				prog.keepScan(chatID, l, t.seen, t.at)
			}
		}
		out.Progress = prog.encode()
		book := parseNameBook(req.NameBook)
		book.learnAll(out.UserNames, now)
		out.NameBook = book.encode()
		if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			out.Partial, err = true, nil
		}
		return out, err
	}

	contactTried := map[string]string{}
	contactOff := false
	var contactScopeErr error
	lookupContact := func(id string) (string, error) {
		id = strings.TrimSpace(id)
		if id == "" || !isContactUser(id, "") {
			return "", nil
		}
		if name, seen := contactTried[id]; seen {
			return name, nil
		}
		if tried(prog.Contact, id) {
			contactTried[id] = ""
			return "", nil
		}
		if contactOff {
			return "", nil
		}
		if overBudget() {
			return "", context.DeadlineExceeded
		}
		var data struct {
			User struct {
				Name string `json:"name"`
			} `json:"user"`
		}
		err := sess.getJSON(runCtx, request{
			family: famIM,
			op:     "user name",
			path:   "/open-apis/contact/v3/users/" + pathEsc(id),
			query:  map[string]string{"user_id_type": "open_id"},
		}, &data)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if runCtx.Err() != nil {
				return "", runCtx.Err()
			}
			if fatal(err) {
				return "", err
			}
			if e := asSyncError(err); e != nil && e.Kind == larksyncwire.ErrKindScope {
				contactOff = true
				contactScopeErr = err
				return "", nil
			}
			contactTried[id] = ""
			if e := asSyncError(err); e == nil || (e.Kind != larksyncwire.ErrKindRateLimited && e.Kind != larksyncwire.ErrKindTransient) {
				mark(&prog.Contact, id, now) // not visible to this user: asked again after namesRetryAfter
			}
			return "", nil
		}
		name := strings.TrimSpace(data.User.Name)
		contactTried[id] = name
		if name == "" {
			mark(&prog.Contact, id, now)
		}
		if name != "" {
			out.UserNames[id] = name
		}
		return name, nil
	}

	requestedSenders := map[string]bool{}
	for _, rawID := range req.OpenIDs {
		if id := strings.TrimSpace(string(rawID)); id != "" {
			requestedSenders[id] = true
		}
	}
	for _, sids := range req.ChatSenders {
		for _, sid := range sids {
			if sid = strings.TrimSpace(sid); sid != "" {
				requestedSenders[sid] = true
			}
		}
	}

	known := nameBookNames(req.NameBook)
	chatMembersOff := false
	// memberName reads chatID's members on demand until want is among them ("" reads the whole list) and says want's
	// name; it returns the members read so far.
	memberName := func(chatID, want string) (map[string]string, error) {
		if chatID == "" {
			return nil, nil
		}
		l := chatMembers[chatID]
		if l == nil {
			if chatMembersOff {
				return nil, nil
			}
			l = &chatMemberList{}
			if want != "" {
				// A group's scan resumes where an earlier round stopped while it was checked for every sender
				// this chat wants named now; otherwise it starts again from the first page.
				wanted := chatWanted(req.ChatSenders[chatID])
				t := trackedScan{seen: wanted, at: now.Unix()}
				if sc := prog.scanFor(chatID, wanted); sc != nil {
					l.next, l.pages, l.done = sc.Next, sc.Pages, sc.Done
					t = trackedScan{seen: sc.Seen, at: sc.At}
				}
				tracked[chatID] = t
			}
			chatMembers[chatID] = l
		}
		if !l.done && !chatMembersOff {
			if overBudget() {
				return l.names, context.DeadlineExceeded
			}
			if _, err := l.find(runCtx, sess, chatID, want, overBudget); err != nil {
				if ctx.Err() != nil {
					return l.names, ctx.Err()
				}
				if runCtx.Err() != nil {
					return l.names, runCtx.Err()
				}
				return l.names, err
			}
			if l.denied {
				chatMembersOff = true
			}
			if !l.done && overBudget() {
				return l.names, context.DeadlineExceeded
			}
		}
		for id, name := range l.names {
			if name != "" && requestedSenders[id] && out.UserNames[id] == "" {
				out.UserNames[id] = name
			}
		}
		return l.names, nil
	}

	// Pass 1: resolve senders chat-by-chat; once a chat has a contact/v3 miss, load its member list once and serve
	// every remaining member in that chat from the cache without repeating failed contact/v3 lookups.
	for _, chatID := range sortedKeys(req.ChatSenders) {
		for _, sid := range req.ChatSenders[chatID] {
			sid = strings.TrimSpace(sid)
			if sid == "" || out.UserNames[sid] != "" || !isContactUser(sid, "") {
				continue
			}
			if l := chatMembers[chatID]; l != nil {
				if name := l.names[sid]; name != "" {
					out.UserNames[sid] = name
					continue
				}
			}
			name, err := lookupContact(sid)
			if err != nil {
				return finish(err)
			}
			if name != "" {
				continue
			}
			members, err := memberName(chatID, sid)
			if err != nil {
				return finish(err)
			}
			if mname := members[sid]; mname != "" {
				out.UserNames[sid] = mname
			}
		}
	}

	// Pass 2: resolve any remaining OpenIDs not associated with a chat in ChatSenders.
	for _, rawID := range req.OpenIDs {
		id := strings.TrimSpace(string(rawID))
		if id == "" || out.UserNames[id] != "" || !isContactUser(id, "") {
			continue
		}
		if _, err := lookupContact(id); err != nil {
			return finish(err)
		}
	}

	// Pass 3: resolve unnamed p2p chats via resolved peer senders, chat info, and chat members; if a p2p chat's peer
	// name is found (including a bot DM's chat name), also fill any unnamed non-owner sender in that 1-on-1 chat.
	seenP2P := map[string]bool{}
	for _, rawChatID := range req.P2PChatIDs {
		chatID := strings.TrimSpace(string(rawChatID))
		if chatID == "" || seenP2P[chatID] {
			continue
		}
		seenP2P[chatID] = true
		peer := ""
		for _, sid := range req.ChatSenders[chatID] {
			sid = strings.TrimSpace(sid)
			if sid != "" && sid != owner && out.UserNames[sid] != "" {
				peer = out.UserNames[sid]
				break
			}
		}
		var targetID string
		if peer == "" {
			if overBudget() {
				return finish(context.DeadlineExceeded)
			}
			var data struct {
				Name          string `json:"name"`
				OwnerID       string `json:"owner_id"`
				P2PTargetID   string `json:"p2p_target_id"`
				P2PTargetType string `json:"p2p_target_type"`
			}
			err := sess.getJSON(runCtx, request{
				family: famIM,
				op:     "chat info",
				path:   "/open-apis/im/v1/chats/" + pathEsc(chatID),
				query:  map[string]string{"user_id_type": "open_id"},
			}, &data)
			if err != nil {
				if ctx.Err() != nil {
					return finish(ctx.Err())
				}
				if runCtx.Err() != nil {
					return finish(runCtx.Err())
				}
				if fatal(err) {
					return finish(err)
				}
			} else {
				if name := strings.TrimSpace(data.Name); name != "" {
					peer = name
				} else {
					if data.P2PTargetID != "" && isContactUser(data.P2PTargetID, data.P2PTargetType) {
						targetID = data.P2PTargetID
					} else if data.OwnerID != "" && data.OwnerID != owner && isContactUser(data.OwnerID, "") {
						targetID = data.OwnerID
					}
					if targetID != "" {
						if name := out.UserNames[targetID]; name != "" {
							peer = name
						} else {
							name, cerr := lookupContact(targetID)
							if cerr != nil {
								return finish(cerr)
							}
							peer = name
						}
					}
				}
			}
		}
		if peer == "" && targetID == "" {
			// A bot's single chat: no API a user token may call names another app's bot; the book may.
			for _, sid := range req.ChatSenders[chatID] {
				if name := known[strings.TrimSpace(sid)]; name != "" && sid != owner {
					peer = name
					break
				}
			}
		}
		if peer == "" {
			members, err := memberName(chatID, "")
			if err != nil {
				return finish(err)
			}
			if targetID != "" && members[targetID] != "" {
				peer = members[targetID]
			} else if p := uniqueP2PPeerName(members, owner); p != "" {
				peer = p
			}
		}
		if peer != "" {
			out.P2PChatNames[chatID] = peer
			for _, sid := range req.ChatSenders[chatID] {
				sid = strings.TrimSpace(sid)
				if sid != "" && sid != owner && out.UserNames[sid] == "" {
					out.UserNames[sid] = peer
				}
			}
		}
	}

	// Pass 4: fallback to message-carried sender/mention info for any sender still unresolved.
	msgNames := map[string]string{}
	for _, sid := range sortedKeys(req.SampleMessages) {
		if sid == "" || out.UserNames[sid] != "" {
			continue
		}
		if name := msgNames[sid]; name != "" {
			out.UserNames[sid] = name
			continue
		}
		msgID := strings.TrimSpace(req.SampleMessages[sid])
		if msgID == "" || tried(prog.Message, sid) {
			continue
		}
		if overBudget() {
			return finish(context.DeadlineExceeded)
		}
		var data struct {
			Items []apiMessage `json:"items"`
		}
		err := sess.getJSON(runCtx, request{
			family: famIM,
			op:     "get message",
			path:   "/open-apis/im/v1/messages/" + pathEsc(msgID),
			query:  map[string]string{"user_id_type": "open_id"},
		}, &data)
		if err != nil {
			if ctx.Err() != nil {
				return finish(ctx.Err())
			}
			if runCtx.Err() != nil {
				return finish(runCtx.Err())
			}
			if fatal(err) {
				return finish(err)
			}
			continue
		}
		for _, am := range data.Items {
			extractMessageNames(am, msgNames)
		}
		if name := msgNames[sid]; name != "" {
			out.UserNames[sid] = name
		} else {
			mark(&prog.Message, sid, now)
		}
	}
	// Pass 5: the binding's name book, for a sender no API names now (a member who left the group, a bot an earlier
	// message mentioned).
	for _, sid := range sortedKeys(req.SampleMessages) {
		if sid != "" && out.UserNames[sid] == "" && known[sid] != "" {
			out.UserNames[sid] = known[sid]
		}
	}

	// Pass 6: read the chat around a still-unnamed sender's sample message for an @mention of them (someone replying
	// to a member who has since left the group).
	scanned := map[string]bool{}
	for _, sid := range sortedKeys(req.SampleMessages) {
		chatID := strings.TrimSpace(req.SampleChats[sid])
		at := req.SampleTimes[sid]
		if sid == "" || out.UserNames[sid] != "" || chatID == "" || at.IsZero() {
			continue
		}
		key := chatID + "\x00" + strconv.FormatInt(at.Unix()/3600, 10)
		if !scanned[key] && !tried(prog.Mention, key) {
			scanned[key] = true
			if overBudget() {
				return finish(context.DeadlineExceeded)
			}
			var want []string
			for _, other := range req.ChatSenders[chatID] {
				if other = strings.TrimSpace(other); other != "" && out.UserNames[other] == "" {
					want = append(want, other)
				}
			}
			if err := scanMentions(runCtx, sess, chatID, at, want, msgNames, overBudget); err != nil {
				if ctx.Err() != nil {
					return finish(ctx.Err())
				}
				if runCtx.Err() != nil {
					return finish(runCtx.Err())
				}
				return finish(err)
			}
			if overBudget() {
				return finish(context.DeadlineExceeded) // cut short: this hour is scanned again next round
			}
			if msgNames[sid] == "" {
				mark(&prog.Mention, key, now)
			}
		}
		for _, other := range req.ChatSenders[chatID] {
			if other = strings.TrimSpace(other); other != "" && out.UserNames[other] == "" && msgNames[other] != "" {
				out.UserNames[other] = msgNames[other]
			}
		}
	}

	// If Pass 4-6 learned a peer's name for a still-unnamed p2p chat, populate P2PChatNames too.
	for _, rawChatID := range req.P2PChatIDs {
		chatID := strings.TrimSpace(string(rawChatID))
		if chatID == "" || out.P2PChatNames[chatID] != "" {
			continue
		}
		for _, sid := range req.ChatSenders[chatID] {
			sid = strings.TrimSpace(sid)
			if sid != "" && sid != owner && out.UserNames[sid] != "" {
				out.P2PChatNames[chatID] = out.UserNames[sid]
				break
			}
		}
	}

	if contactScopeErr != nil {
		for _, rawID := range req.OpenIDs {
			id := strings.TrimSpace(string(rawID))
			if id != "" && isContactUser(id, "") && out.UserNames[id] == "" {
				return finish(contactScopeErr)
			}
		}
	}
	return finish(nil)
}

// chatWanted is the sorted, unique contact users among a chat's unnamed senders: what its member list scan is
// checked for.
func chatWanted(senders []string) []string {
	var out []string
	for _, sid := range senders {
		if sid = strings.TrimSpace(sid); sid != "" && isContactUser(sid, "") {
			out = append(out, sid)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Media fetches a message's image or file by its attachment Ref ("<binding key>|<message id>|<key>|<image|file>"),
// at most limit bytes (botcard.ErrTooLarge beyond), and returns it with its content type.
func (s *Syncer) Media(ctx context.Context, b larksyncwire.Binding, token func(ctx context.Context) (string, error), ref string, limit int64) ([]byte, string, error) {
	if token == nil {
		return nil, "", &larksyncwire.Error{Kind: larksyncwire.ErrKindAuth, Op: "media", Msg: "no token"}
	}
	binding, messageID, key, kind, ok := parseRef(ref)
	if !ok || binding != b.Key {
		return nil, "", &larksyncwire.Error{Kind: larksyncwire.ErrKindOther, Op: "media", Msg: "not an attachment of this binding"}
	}
	if limit <= 0 {
		limit = 20 << 20
	}
	sess := &session{s: s, base: botcard.ResolveDomain(b.App.Domain), token: token}
	return sess.download(ctx, request{family: famIM, op: "media", path: "/open-apis/im/v1/messages/" + pathEsc(messageID) + "/resources/" + pathEsc(key), query: map[string]string{"type": kind}}, limit)
}

// attachmentRef is the Ref of a message's file or image: the binding it was read through comes first, so the
// knowledge base's lazy fetch goes back to a binding that can read it.
func attachmentRef(binding, messageID, key, kind string) string {
	return binding + "|" + messageID + "|" + key + "|" + kind
}

// parseRef splits an attachmentRef. The parts after the binding key never hold a "|", so they are taken from the
// right.
func parseRef(ref string) (binding, messageID, key, kind string, ok bool) {
	parts := strings.Split(ref, "|")
	if len(parts) < 4 {
		return "", "", "", "", false
	}
	n := len(parts)
	binding = strings.Join(parts[:n-3], "|")
	messageID, key, kind = parts[n-3], parts[n-2], parts[n-1]
	if binding == "" || messageID == "" || key == "" || (kind != "image" && kind != "file") {
		return "", "", "", "", false
	}
	return binding, messageID, key, kind, true
}

// openIDOf is the owner's open id in an account id "<brand>:<tenant_key>:<open_id>", which tells the owner's own
// messages (Message.Mine).
func openIDOf(accountID string) string {
	if i := strings.LastIndex(accountID, ":"); i >= 0 {
		return accountID[i+1:]
	}
	return accountID
}

func cmpDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// asSyncError is err's *larksyncwire.Error, or nil.
func asSyncError(err error) *larksyncwire.Error {
	var e *larksyncwire.Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}
