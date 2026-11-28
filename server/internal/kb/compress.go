package kb

import (
	"context"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/aiwire"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Model is the small model that compresses and tags (the hub's ai.Client).
type Model interface {
	JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error
}

// NewModel is a Model that calls the provider cfg names: the client the engine's models are, for a caller that has
// only a config (a live check of the recordings' writers, which are outside this package and the core's client).
func NewModel(cfg aiwire.Config) Model { return ai.New(cfg) }

// When a chat is due for compression: enough new messages, or some new ones and a quiet while.
const (
	compressBatch = 50
	compressQuiet = 10 * time.Minute
	compressInput = 24000 // bytes of new messages per call (Store.Since)
)

// After a failed compression the chat waits compressRetry, doubled with every further failure in a row up to
// compressRetryMax, so one chat the model cannot handle does not cost a call every round (2026-11-07: a chat the model
// refused was sent again every two minutes for three days).
const (
	compressRetry    = 10 * time.Minute
	compressRetryMax = 6 * time.Hour
)

// retryWait is how long work waits after its failures-th failure in a row: compressRetry, doubled per further failure,
// at most compressRetryMax. A project whose rewrite fails waits the same (RefreshProjects).
func retryWait(failures int) time.Duration {
	if n := failures - 1; n >= 0 && n < 16 {
		return min(compressRetry<<n, compressRetryMax)
	}
	if failures <= 0 {
		return compressRetry
	}
	return compressRetryMax
}

var importanceRe = lazyre.New(`^P[0-3]$`)

type chatDigest struct {
	Summary    string     `json:"summary"`
	OneLine    string     `json:"one_line"`
	Importance string     `json:"importance"`
	Relevance  modelScore `json:"relevance"`
	Tags       []string   `json:"tags"`
	Projects   []struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Aliases []string `json:"aliases"`
		Update  string   `json:"update"`
		MyRole  string   `json:"my_role"`
	} `json:"projects"`
	People []struct {
		Name string `json:"name"`
		Note string `json:"note"`
	} `json:"people"`
}

// DueForCompression lists chats with news to fold into their summary.
func (e *Engine) DueForCompression(now time.Time) []*Chat {
	var out []*Chat
	for _, c := range e.Store.Chats() {
		if c.Pending == 0 || now.Before(c.CompressRetryAt) {
			continue
		}
		if c.Pending >= compressBatch || now.Sub(c.LastAt) >= compressQuiet || c.CompressedAt.IsZero() && now.Sub(c.Created) >= compressQuiet {
			out = append(out, c)
		}
	}
	// The chats that matter most first; then the busiest.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Relevance != out[j].Relevance {
			return out[i].Relevance > out[j].Relevance
		}
		return out[i].Pending > out[j].Pending
	})
	return out
}

// settleCompression advances only when no live pending work changed while the
// model judged its snapshot. The minute boundary and pending count move together
// for both a successful summary and a refusal; unseen IDs must remain readable.
func settleCompression(current, snapshot *Chat) bool {
	if current.Pending != snapshot.Pending || current.LastAt.After(snapshot.LastAt) {
		return false
	}
	current.Pending = 0
	current.CoveredUntil = snapshot.LastAt.Add(time.Minute)
	return true
}

// compressFailed records that compressing c (the copy the call started from) failed with err, and returns the error to
// log. A refused snapshot is settled without a summary only when no new live work arrived; otherwise that work stays
// eligible for another judgment. Any other failure puts the chat off (compressRetry). A paused connection, no model set
// up and an ended run change nothing: the chat stays due.
func (e *Engine) compressFailed(ctx context.Context, c *Chat, err error) error {
	if deferred(ctx, err) {
		return err
	}
	e.Store.chatMu.Lock() // read and written whole: what archiving or a history import wrote meanwhile stays
	defer e.Store.chatMu.Unlock()
	if perr := e.Store.recoverIngest(c.ID); perr != nil {
		return perr
	}
	current, ok := e.Store.Chat(c.ID) // archiving may have added messages while the model was called
	if !ok {
		return err
	}
	if errors.Is(err, ai.ErrRefused) {
		settled := settleCompression(current, c)
		current.CompressFailures, current.CompressRetryAt = 0, time.Time{}
		if perr := e.Store.PutChat(current); perr != nil {
			return perr
		}
		if !settled {
			return fmt.Errorf("%w: left concurrent messages pending", err)
		}
		return fmt.Errorf("%w: skipped its messages up to %s", err, c.LastAt.UTC().Format(time.RFC3339))
	}
	current.CompressFailures++
	wait := retryWait(current.CompressFailures)
	current.CompressRetryAt = time.Now().Add(wait)
	if perr := e.Store.PutChat(current); perr != nil {
		return perr
	}
	return fmt.Errorf("%w (failed %d time(s) in a row, next try in %s)", err, current.CompressFailures, wait)
}

// putCompressed writes what compression decided for c: the chat is read again under chatMu and only set changes it,
// so the messages, people and history mode that archiving or a history import (WriteHistory) wrote while the model
// was called stay. c gets the chat as written. A chat forgotten meanwhile is not written back (false).
func (e *Engine) putCompressed(c *Chat, set func(cur *Chat)) (bool, error) {
	e.Store.chatMu.Lock()
	defer e.Store.chatMu.Unlock()
	if err := e.Store.recoverIngest(c.ID); err != nil {
		return false, err
	}
	cur, ok := e.Store.Chat(c.ID)
	if !ok {
		return false, nil
	}
	set(cur)
	if err := e.Store.PutChat(cur); err != nil {
		return false, err
	}
	*c = *cur
	return true, nil
}

// Compress folds a chat's new messages into its summary and tags, then evolves the projects and people it mentions.
func (e *Engine) Compress(ctx context.Context, c *Chat) error {
	current, ok := e.Store.Chat(c.ID)
	if !ok {
		return nil
	}
	c = current
	if e.Model == nil {
		return nil
	}
	from := c.CoveredUntil
	if from.IsZero() {
		from = c.Created.AddDate(0, 0, -30)
		// A Moments post is archived when it first concerns me, often long after it was written: the 30 days before
		// that held none of it, so 20 of 21 posts were settled without a model call (2026-11-01). The whole post,
		// back to a year before it was archived.
		if c.Kind == "moment" {
			from = c.Created.AddDate(-1, 0, 0)
		}
	}
	news := e.Store.Since(c.ID, from, compressInput)
	if strings.TrimSpace(news) == "" {
		_, err := e.putCompressed(c, func(cur *Chat) { cur.Pending = 0 })
		return err
	}
	owner := e.knowledgeOwner()
	in := prompts.RenderKBCompressInput(prompts.KBCompressInput{Language: owner.Language, Name: c.Name, Kind: c.Kind, Automated: c.Kind == "mail" && !HasPeople(c),
		Pinned: c.Pinned, People: e.pinnedPeople(c.People), Projects: e.knownProjects(), Tags: e.TopTags(40), Summary: e.Store.Summary(KindChat, c.ID), News: news})

	var d chatDigest
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := e.modelFor("kb.compress").JSONMax(cctx, prompts.RenderKBCompress(owner), in, 4000, &d); err != nil {
		return e.compressFailed(ctx, c, err)
	}
	fresh, ok := e.Store.Chat(c.ID)
	if !ok { // forgotten while the model wrote (ForgetSource): not written back
		return nil
	}
	// Whether compression links the chat goes by the chat as it is now: a lock made while the model wrote counts.
	link := !heldOffProjects(fresh)
	started := *c // the chat as the model read it; c gets what compression decides, and only that is written back
	now := time.Now().UTC()
	if strings.TrimSpace(d.Summary) != "" {
		if err := e.Store.PutSummary(KindChat, c.ID, "# "+c.Name+"\n\n"+d.Summary); err != nil {
			return err
		}
	}
	c.OneLine = oneLine(d.OneLine)
	applyTags(&c.Tagging, d.Importance, int(d.Relevance), d.Tags, now)
	if HasPeople(c) {
		for _, p := range d.Projects {
			slug := e.evolveProject(p.Name, p.Kind, p.Aliases, p.MyRole, c, link, p.Update, now)
			c.Projects = addUnique(c.Projects, slug)
		}
	}
	for _, p := range d.People {
		e.notePersonNote(c, p.Name, p.Note)
	}
	written, err := e.putCompressed(c, func(cur *Chat) {
		cur.Tagging, cur.OneLine, cur.TagRev = c.Tagging, c.OneLine, tagRev
		for _, slug := range c.Projects {
			// A project deleted while the model ran (project_trash.go) is not linked again.
			if !slices.Contains(cur.Projects, slug) && !e.Store.projectLive(slug) {
				continue
			}
			cur.Projects = addUnique(cur.Projects, slug)
		}
		// New IDs can share a timestamp or minute with the snapshot. Keep the
		// pending work and its old coverage boundary when live ingestion moved,
		// so the next pass also reads those messages at minute precision.
		settleCompression(cur, &started)
		cur.CompressedAt = now
		cur.CompressFailures, cur.CompressRetryAt = 0, time.Time{}
	})
	if err != nil || !written {
		return err
	}
	e.index(KindChat, c.ID, c.Name, c.Tagging, c.OneLine, c.LastAt)
	// A DM is mostly about the other person: their profile follows it.
	if c.Kind == "dm" || c.Kind == "mail" {
		for _, pid := range c.People {
			_, _, _ = e.Store.updatePerson(pid, false, nil, func(p *Person) bool {
				if p.Locked {
					return false
				}
				p.Importance, p.Relevance = c.Importance, c.Relevance
				p.Tags = c.Tags
				return true
			})
		}
	}
	for _, pid := range c.People {
		e.WritePersonSummary(pid)
	}
	return nil
}

// automatedSender matches mail addresses of notifications and newsletters (only the inbox is read, so a reply of
// the owner's rarely shows up: the sender is what tells).
var automatedSender = lazyre.New(`(?i)(^|[-_.@])(no[-_]?reply|do[-_]?not[-_]?reply|notifications?|notify|news(letter)?|marketing|mailer|mailer-daemon|postmaster|bounces?|info|support|service|billing|account|alerts?|updates?|hello|team|receipts?|orders?|invoices?|payments?|contact|sales|promo(tions)?|offers?|rewards?|digest|submissions?|feedback|welcome)([-_.@]|$)`)

// automatedLocal: a local part ending in one of those words ("applesupport", "teamnotify").
var automatedLocal = lazyre.New(`(?i)(support|service|notify|notifications?|alerts?|news|updates?|receipts?|billing)$`)

// bulkDomain: the subdomains bulk mail goes out from (email.apple.com, em-s.dropbox.com, txn.dropbox.com, mg.…).
var bulkDomain = lazyre.New(`(?i)^(e|em|em-[a-z]+|email|emails|emessaging|message|messages|mail|mailer|mailing|news|newsletter|txn|transactional|notify|notifications|mg|send|sendgrid|bounce|marketing)[.]`)

// newsletterHost: platforms every sender on is a newsletter.
var newsletterHost = lazyre.New(`(?i)(^|[.])(substack\.com|beehiiv\.com|mailchimp(app)?\.com|convertkit\.com|buttondown\.email|ghost\.io|medium\.com)$`)

// automatedAddress reports whether a mail address looks like a system or bulk sender rather than a person.
func automatedAddress(addr string) bool {
	if automatedSender.MatchString(addr) {
		return true
	}
	local, domain, ok := strings.Cut(addr, "@")
	return ok && (automatedLocal.MatchString(local) || bulkDomain.MatchString(domain) || newsletterHost.MatchString(domain))
}

// HasPeople reports whether real people talk in a chat: chats with bots and automated mail (notifications,
// newsletters) do not make projects.
func HasPeople(c *Chat) bool {
	switch c.Kind {
	case "bot":
		return false
	case "mail":
		return c.Mine || !automatedAddress(c.SourceID[strings.Index(c.SourceID, "-")+1:])
	}
	return true
}

// IgnoreNoiseProjects marks projects that only come from chats without people as ignored (the AI once made a
// project of every app named in store emails). Hand-tagged ones stay. Returns how many changed.
func (e *Engine) IgnoreNoiseProjects() int {
	n := 0
	for _, p := range e.Store.Projects() {
		if p.Locked || p.Status == "ignored" || !p.Live() || len(p.Chats) == 0 {
			continue
		}
		people := false
		for _, cid := range p.Chats {
			if c, ok := e.Store.Chat(cid); !ok || HasPeople(c) {
				people = true
				break
			}
		}
		if !people {
			p.Status = "ignored"
			p.Dirty = false
			if e.Store.PutProject(p) == nil {
				e.index(KindProject, p.Slug, p.Name, p.Tagging, p.OneLine, p.Updated)
				n++
			}
		}
	}
	return n
}

// tagRev is the version of the tagging rules; chats tagged under an older one can be tagged again (RetagMail).
// 1: notifications (receipts, renewals, promotions, invitations) are P3 and at most 20 unless they need me to act.
const tagRev = 1

// RetagMail queues mail the owner never replied to, tagged under older rules, for one more compression, so the rules
// reach what is already there (receipts and promotions had come out at relevance 60–75, some from addresses that
// passed for people). Hand-tagged ones stay. Returns how many were queued.
func (e *Engine) RetagMail() int {
	n := 0
	for _, c := range e.Store.Chats() {
		if c.Kind != "mail" || c.Mine || c.Locked || c.TagRev >= tagRev || c.CompressedAt.IsZero() {
			continue
		}
		written, err := e.putCompressed(c, func(cur *Chat) {
			cur.CoveredUntil = time.Time{} // read its messages again; the old summary goes in as context
			cur.Pending = max(cur.Pending, 1)
		})
		if written && err == nil {
			n++
		}
	}
	return n
}

func applyTags(t *Tagging, importance string, relevance int, tags []string, now time.Time) {
	if !t.Locked {
		if importanceRe.MatchString(importance) {
			t.Importance = importance
		}
		if relevance >= 0 && relevance <= 100 {
			t.Relevance = relevance
		}
	}
	var clean []string
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.TrimPrefix(tag, "#"))
		if tag != "" && len([]rune(tag)) <= 16 {
			clean = addUnique(clean, tag)
		}
	}
	if len(clean) > 0 {
		t.Tags = clean
	}
	t.TaggedAt = now
}

// TopTags returns the n most used tags across the knowledge base.
func (e *Engine) TopTags(n int) []string {
	count := map[string]int{}
	add := func(t []string) {
		for _, x := range t {
			count[x]++
		}
	}
	for _, c := range e.Store.Chats() {
		add(c.Tags)
	}
	for _, p := range e.Store.Projects() {
		add(p.Tags)
	}
	tags := make([]string, 0, len(count))
	for t := range count {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		if count[tags[i]] != count[tags[j]] {
			return count[tags[i]] > count[tags[j]]
		}
		return tags[i] < tags[j]
	})
	if len(tags) > n {
		tags = tags[:n]
	}
	return tags
}
