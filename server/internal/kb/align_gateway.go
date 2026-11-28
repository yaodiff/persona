package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/larksyncwire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// Aligning the Lark user sync's messages with the Lark gateway's.
//
// The gateway (source "lark") archives the owner's Lark chats as "lark-<feed id>", by ids this repository cannot map to
// the open platform's oc_/om_ ids; the user sync archives the same chats as "larksync-<oc_ id>". A chat file belongs to
// one connector (attributeChat), so the sync cannot append into the gateway's chats: instead every message it hands
// over is looked for in the gateway's archive first, and one found there is not written again (Engine.Align).
//
// A message is the same when its chat, its sender, its body and its time agree (messageFingerprint, the same rule as
// the sync's larksync.MessageFingerprint): the chat is the one the crosswalk pairs it with (L1), or else any gateway
// chat of the same name (L2), whose day files are read for the message's day; the times agree when their 2-second
// buckets are at most one apart. The day files keep only the minute; the gateway chat's receipts give a line its exact
// time when they bind it unambiguously (receiptLineHash), and a line without one agrees when its minute holds the
// message's time. The first L2 match pairs the two chats in the crosswalk, so later messages compare within that
// chat alone.

// crosswalkFile pairs the sync's chats with the gateway's: <data>/chats/.crosswalk.json, {"<sync chat id>": "<gateway
// chat id>"}, both knowledge-base chat ids ("larksync-oc_…" → "lark-…").
const crosswalkFile = ".crosswalk.json"

// larkSyncRepairFile records the highest completed one-time backfill repair version for pre-#15486 larksync archives
// (<data>/chats/.larksync_repair.json).
const (
	larkSyncRepairFile    = ".larksync_repair.json"
	larkSyncRepairVersion = 5
)

// alignNamesEvery is how long the gateway chats' names are kept before they are listed again: a chat the gateway
// creates meanwhile is found within it.
const alignNamesEvery = time.Minute

// alignDaysMost bounds the parsed day files kept between messages (a batch is mostly one chat's one or two days).
const alignDaysMost = 64

// alignMidnight: a message this close to midnight is also looked for in the neighbouring day's file (a chat's day
// files may have been written in another zone, and the times may differ by a bucket across midnight).
const alignMidnight = 10 * time.Minute

// NewGatewayAligner is an Engine.Align for the Lark user sync (larksyncwire.ConnectorID): it says a message is a
// duplicate when the Lark gateway already archived it, and pairs the two chats in the crosswalk on the first match.
// Messages of every other source are never duplicates.
func NewGatewayAligner(s *Store) func(Source, SourceChat, SourceMessage) bool {
	a := &gatewayAligner{s: s, now: time.Now, days: map[string]gatewayDay{}}
	return a.align
}

// gatewayAligner keeps what one aligner read: the crosswalk, the gateway chats by name and recently parsed days.
type gatewayAligner struct {
	s   *Store
	now func() time.Time

	mu       sync.Mutex
	walk     map[string]string // the crosswalk; nil until read
	names    map[string][]string
	namesAt  time.Time
	days     map[string]gatewayDay // "<chat id>/<day>" → its parsed lines
	walkFail bool                  // the crosswalk could not be read: L2 only, and nothing is written over it
}

// gatewayDay is one gateway day file's message lines, kept while the file is unchanged.
type gatewayDay struct {
	mod   time.Time
	size  int64
	lines []gatewayLine
}

// gatewayLine is one archived message as the aligner compares it.
type gatewayLine struct {
	minute     time.Time // the line's minute (the day file's "15:04")
	exact      time.Time // its time from the chat's receipts; zero when they do not bind it to one
	sender     string
	body       string // without its trailing attachments
	formatBody string // retains named file placeholders for the stricter format-only comparison
	media      bool   // it had attachments
	mediaCount int    // number of archived attachment markers, or live media references
	key        string // "15:04 " + receiptBodyHash of the line, as the receipts bind it; "" for a sync message
	unique     bool   // no other line of its day file has the same key, so it can be dropped alone
}

func (a *gatewayAligner) align(src Source, sc SourceChat, m SourceMessage) bool {
	if !syncSource(src) {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id := ChatID(src.Name(), sc.ID)
	walk := a.crosswalk()
	if gw, ok := walk[id]; ok {
		return a.settle(id, m.ID, a.within(gw, sc, m, true)) // L1: only the paired chat
	}
	name := normalizeText(sc.Name)
	if name == "" {
		return false
	}
	match, found := "", alignVerdict{}
	for _, gw := range a.gatewayChats()[name] {
		if v := a.within(gw, sc, m, false); v.matched() {
			if match != "" {
				return false // same-name chats are ambiguous: never choose the first one
			}
			match, found = gw, v
		}
	}
	if match != "" {
		a.pair(id, match)
		return a.settle(id, m.ID, found)
	}
	return false
}

// settle turns a verdict on sync message msgID of sync chat id into align's answer: a duplicate is not written; a
// sync message fuller than the gateway's line is written, and that line is noted to be dropped once it is
// (Engine.settleSuperseded). Without an ID the write cannot be checked, so both copies stay.
func (a *gatewayAligner) settle(id, msgID string, v alignVerdict) bool {
	if v.drop != nil {
		if msgID != "" && !a.s.ReadOnly() {
			if a.s.superseded == nil {
				a.s.superseded = map[string]map[string]gatewayDrop{}
			}
			if a.s.superseded[id] == nil {
				a.s.superseded[id] = map[string]gatewayDrop{}
			}
			a.s.superseded[id][msgID] = *v.drop
		}
		return false
	}
	return v.dup
}

// syncSource: src is the Lark user sync (its archive source or its connector).
func syncSource(src Source) bool {
	if src.Name() == larksyncwire.ConnectorID {
		return true
	}
	if o, ok := src.(interface{ ConnectorID() string }); ok {
		return o.ConnectorID() == larksyncwire.ConnectorID
	}
	return false
}

// ownerSender is how archive lines (buildLine) and syncMessage name the owner's own messages.
const ownerSender = "我" // i18n-ignore: archive data

// unknownSender is how archive lines (buildLine) and syncMessage mark a message whose sender has no display name.
const unknownSender = "?"

// within reports whether gateway chat gw archived m. paired is true when gw came from chats/.crosswalk.json (L1).
func (a *gatewayAligner) within(gw string, sc SourceChat, m SourceMessage, paired bool) alignVerdict {
	sender, body, media := syncMessage(m)
	senderUnknown := sender == unknownSender
	want := map[string]bool{}
	for k := -1; k <= 1; k++ {
		want[messageFingerprint(sc.Name, sender, m.At.Add(time.Duration(k)*2*time.Second), body)] = true
	}
	same := messageFingerprint(sc.Name, sender, m.At, body)
	var wantNoSender map[string]bool
	var sameNoSender string
	ensureNoSender := func() {
		if wantNoSender != nil {
			return
		}
		wantNoSender = make(map[string]bool, 3)
		for k := -1; k <= 1; k++ {
			wantNoSender[messageFingerprint(sc.Name, "", m.At.Add(time.Duration(k)*2*time.Second), body)] = true
		}
		sameNoSender = messageFingerprint(sc.Name, "", m.At, body)
	}
	for _, day := range alignDays(m.At, a.s.Zone()) {
		for _, l := range a.day(gw, day) {
			if l.body == "" && body == "" && (!l.media || !media) {
				continue // two empty texts are the same message only when both were attachments
			}
			// When one side has no display name (for example a bot/app sender or a binding without contact scope)
			// and the other side is not the owner's own message, degrade to the senderless fingerprint
			// (chat name + 2-second bucket + body) so the message still aligns with the gateway's copy.
			fallbackSender := (senderUnknown && l.sender != ownerSender) || (l.sender == unknownSender && sender != ownerSender)
			if !l.exact.IsZero() {
				if want[messageFingerprint(sc.Name, l.sender, l.exact, l.body)] {
					return dupVerdict
				}
				if fallbackSender {
					ensureNoSender()
					if wantNoSender[messageFingerprint(sc.Name, "", l.exact, l.body)] {
						return dupVerdict
					}
				}
				continue
			}
			// Only the minute: the line agrees when its minute, widened by a bucket each side, holds the message.
			if m.At.Before(l.minute.Add(-2*time.Second)) || !m.At.Before(l.minute.Add(time.Minute+2*time.Second)) {
				continue
			}
			if messageFingerprint(sc.Name, l.sender, m.At, l.body) == same {
				return dupVerdict
			}
			// On a minute-only line (no exact timestamp), require a non-empty body before ignoring the sender so
			// two unnamed attachment-only messages in the same minute do not collide.
			if fallbackSender && body != "" {
				ensureNoSender()
				if messageFingerprint(sc.Name, "", m.At, l.body) == sameNoSender {
					return dupVerdict
				}
			}
		}
	}
	src := gatewayLine{exact: m.At, sender: sender, body: body, formatBody: m.Text, media: media, mediaCount: len(m.Media)}
	if a.withinFormatted(gw, src, paired) {
		return dupVerdict
	}
	return a.withinLoose(gw, src, paired)
}

// syncPlaceholder is the text the sync gives an image or a file of a message ("[image]", "[file <name>]"), which the
// gateway's archive writes as an attachment instead.
var syncPlaceholder = lazyre.New(`\s*\[(?:image|file(?: [^\]]*)?)\]\s*$`)

// syncMessage is m's sender as an archive line names it (buildLine: the owner is "我") and its text without the
// attachments the line would carry.
func syncMessage(m SourceMessage) (sender, body string, media bool) {
	sender = strings.TrimSpace(m.SenderName)
	if m.Mine {
		sender = ownerSender
	}
	if sender == "" {
		sender = unknownSender
	}
	body = strings.TrimSpace(m.Text)
	media = len(m.Media) > 0
	if media {
		for {
			loc := syncPlaceholder.FindStringIndex(body)
			if loc == nil {
				break
			}
			body = body[:loc[0]]
		}
	}
	return sender, body, media
}

// alignDays are the days (YYYY-MM-DD in zone) whose files may hold a message of at.
func alignDays(at time.Time, zone *time.Location) []string {
	t := at.In(zone)
	out := []string{t.Format("2006-01-02")}
	start := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, zone)
	if t.Sub(start) < alignMidnight {
		out = append(out, start.Add(-time.Hour).Format("2006-01-02"))
	}
	if next := start.AddDate(0, 0, 1); next.Sub(t) <= alignMidnight {
		out = append(out, next.Format("2006-01-02"))
	}
	return out
}

// day is gateway chat id's lines of day, parsed again only when its file changed.
func (a *gatewayAligner) day(id, day string) []gatewayLine {
	p := a.s.path(KindChat, id, "messages", day+".md")
	st, err := os.Stat(p)
	if err != nil {
		return nil
	}
	key := id + "/" + day
	if d, ok := a.days[key]; ok && d.mod.Equal(st.ModTime()) && d.size == st.Size() {
		return d.lines
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	zone := a.s.Zone()
	// The exact times: the receipts bound to a line by its minute and its rendered text (as DayWithIdentity does).
	var r messageReceipts
	_, _ = readJSON(a.s.path(KindChat, id, ingestReceipts), &r)
	exact := map[string][]time.Time{}
	for ref, line := range r.Lines {
		at, ok := r.IDs[ref]
		if !ok || line.Hash == "" || at.In(zone).Format("2006-01-02") != day {
			continue
		}
		k := at.In(zone).Format("15:04 ") + line.Hash
		exact[k] = append(exact[k], at)
	}
	var lines []gatewayLine
	keys := map[string]int{}
	for _, msg := range messages(string(raw)) {
		t := msg.text
		if len(t) < 6 || t[5] != ' ' {
			continue
		}
		minute, err := time.ParseInLocation("2006-01-02 15:04", day+" "+t[:5], zone)
		if err != nil {
			continue
		}
		sender, text, ok := strings.Cut(t[6:], ": ")
		if !ok {
			continue
		}
		l := gatewayLine{minute: minute, sender: sender, key: t[:6] + receiptBodyHash(t[6:])}
		if ts := exact[l.key]; len(ts) == 1 {
			l.exact = ts[0]
		}
		keys[l.key]++
		text, l.formatBody, l.media, l.mediaCount = stripArchiveMedia(text)
		l.body = text
		lines = append(lines, l)
	}
	for i := range lines {
		lines[i].unique = keys[lines[i].key] == 1
	}
	if len(a.days) >= alignDaysMost {
		clear(a.days)
	}
	a.days[key] = gatewayDay{mod: st.ModTime(), size: st.Size(), lines: lines}
	return lines
}

// gatewayChats are the gateway's chat ids by their normalized names, listed again after alignNamesEvery.
func (a *gatewayAligner) gatewayChats() map[string][]string {
	now := a.now()
	if a.names != nil && now.Sub(a.namesAt) < alignNamesEvery {
		return a.names
	}
	names := map[string][]string{}
	for _, c := range a.s.Chats() {
		if c.Source != gatewaySource {
			continue
		}
		if n := normalizeText(c.Name); n != "" {
			names[n] = append(names[n], c.ID)
		}
	}
	a.names, a.namesAt = names, now
	return names
}

func (a *gatewayAligner) walkPath() string {
	return filepath.Join(a.s.Dir, KindChat, crosswalkFile)
}

// crosswalk is the crosswalk as stored, read once.
func (a *gatewayAligner) crosswalk() map[string]string {
	if a.walk != nil {
		return a.walk
	}
	a.walk = map[string]string{}
	if ok, err := readJSON(a.walkPath(), &a.walk); err != nil || !ok {
		if err != nil {
			a.walkFail = true // a damaged file is left for the owner to see; L2 still finds duplicates
		}
		a.walk = map[string]string{}
	}
	return a.walk
}

// pair records that sync chat id is gateway chat gw. A failed write only costs the next message an L2 search.
func (a *gatewayAligner) pair(id, gw string) {
	if a.walkFail || a.s.ReadOnly() {
		return
	}
	a.walk[id] = gw
	raw, err := json.MarshalIndent(a.walk, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(a.walkPath()), 0o700); err != nil {
		return
	}
	_ = managerconf.WriteFileAtomic(a.walkPath(), raw, 0o600)
}

// messageFingerprint is a message's identity across sources: the hex SHA-256 of its chat's name, its sender, its
// 2-second bucket (Unix seconds / 2) and the hex SHA-256 of its body, joined by NULs, the texts normalized
// (normalizeText). It must stay byte-for-byte the Lark module's larksync.MessageFingerprint: the knowledge base cannot
// import the module, so both follow the one written rule (the test pins a vector).
func messageFingerprint(chatName, sender string, at time.Time, body string) string {
	b := sha256.Sum256([]byte(normalizeText(body)))
	sum := sha256.Sum256([]byte(normalizeText(chatName) + "\x00" + normalizeText(sender) + "\x00" +
		strconv.FormatInt(at.Unix()/2, 10) + "\x00" + hex.EncodeToString(b[:])))
	return hex.EncodeToString(sum[:])
}

// normalizeText is s trimmed, its runs of white space one space, in lower case.
func normalizeText(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// larkSyncRepairState is <data>/chats/.larksync_repair.json.
type larkSyncRepairState struct {
	Version int `json:"version"`
}

// alignArchivedLine reports whether an already-archived larksync day-file line (with optional exact timestamp from
// .receipts.json) duplicates a message in a gateway ("lark") chat, recording the crosswalk pair on first L2 match.
func (a *gatewayAligner) alignArchivedLine(id, chatName string, gl gatewayLine) alignVerdict {
	a.mu.Lock()
	defer a.mu.Unlock()
	walk := a.crosswalk()
	if gw, ok := walk[id]; ok {
		return a.withinArchived(gw, chatName, gl, true)
	}
	name := normalizeText(chatName)
	if name == "" {
		return alignVerdict{}
	}
	match, found := "", alignVerdict{}
	for _, gw := range a.gatewayChats()[name] {
		if v := a.withinArchived(gw, chatName, gl, false); v.matched() {
			if match != "" {
				return alignVerdict{}
			}
			match, found = gw, v
		}
	}
	if match != "" {
		a.pair(id, match)
	}
	return found
}

// withinTimeWindow reports whether srcExact and dst agree in time: when dst has an exact receipt timestamp, both must
// fall in the same or a neighbouring 2-second bucket; on an already-paired crosswalk chat (L1) whose gateway archive
// lacks .receipts.json (dst.exact is zero), srcExact must fall within dst's minute widened by one 2-second bucket on
// each side (the same window withinArchived uses). Unpaired chats (L2) never fall back to minute precision here.
func withinTimeWindow(srcExact time.Time, dst gatewayLine, paired bool) bool {
	if !dst.exact.IsZero() {
		bucket := dst.exact.Unix()/2 - srcExact.Unix()/2
		return bucket >= -1 && bucket <= 1
	}
	if !paired || dst.minute.IsZero() {
		return false
	}
	return !srcExact.Before(dst.minute.Add(-2*time.Second)) && srcExact.Before(dst.minute.Add(time.Minute+2*time.Second))
}

// The additional format-only match is deliberately stricter than the legacy fingerprint: exact receipt times (or, on an
// already-paired crosswalk chat whose legacy gateway archive has no .receipts.json, a minute-level window widened by
// one 2-second bucket on each side), equal attachment counts, a non-empty equivalent body (or matching explicit
// media-kind placeholder on both sides) and exactly one candidate are required. On unpaired chats (L2), exact receipt
// times on both sides and the same known sender are required; on already-paired crosswalk chats (L1), an unknown sender
// ("?") may match a non-owner gateway line when there is a non-empty body (or exact timestamps on both sides) so
// bot/app and pre-backfill external-member lines still deduplicate without colliding unnamed media-only lines in the
// same minute. URLs, URL case, named files and code remain significant. No edit distance, substring match or
// attachment-only guess is used.
func (a *gatewayAligner) withinFormatted(gw string, src gatewayLine, paired bool) bool {
	if src.exact.IsZero() {
		return false
	}
	srcUnknown := src.sender == "" || src.sender == unknownSender
	if !paired && srcUnknown {
		return false
	}
	srcRaw := src.formatBody
	if srcRaw == "" {
		srcRaw = src.body
	}
	srcNorm := equivalentGatewayBody(srcRaw, src.mediaCount)
	srcMediaKind := ""
	if srcNorm == "" {
		srcMediaKind = equivalentMediaKind(srcRaw, src.mediaCount)
		if srcMediaKind == "" {
			return false
		}
	}
	matches := 0
	for _, day := range alignDays(src.exact, a.s.Zone()) {
		for _, dst := range a.day(gw, day) {
			if dst.media != src.media || dst.mediaCount != src.mediaCount {
				continue
			}
			if !withinTimeWindow(src.exact, dst, paired) {
				continue
			}
			dstUnknown := dst.sender == "" || dst.sender == unknownSender
			senderMatch := !srcUnknown && !dstUnknown && normalizeText(dst.sender) == normalizeText(src.sender)
			if !senderMatch && paired && (!dst.exact.IsZero() || srcNorm != "") {
				senderMatch = (srcUnknown && dst.sender != ownerSender) || (dstUnknown && src.sender != ownerSender)
			}
			if !senderMatch {
				continue
			}
			dstRaw := dst.formatBody
			if dstRaw == "" {
				dstRaw = dst.body
			}
			dstNorm := equivalentGatewayBody(dstRaw, dst.mediaCount)
			if srcNorm != "" {
				if srcNorm == dstNorm {
					matches++
				}
			} else if dstNorm == "" && srcMediaKind == equivalentMediaKind(dstRaw, dst.mediaCount) {
				matches++
			}
		}
	}
	return matches == 1
}

var (
	trailingAttachment  = lazyre.New(`\s*\[附件\](?:\([^()\s]*\))?\s*$`)                // i18n-ignore: archive data
	trailingKindMedia   = lazyre.New(`\s*\[(?:文件|视频|语音)\]\s*$`)                       // i18n-ignore: archive data
	gatewayMarkdownLink = lazyre.New(`\[([^\[\]\n]*)\]\((https?://[^\s()]+)\)`)       // Markdown link [label](url)
	gatewayParenLink    = lazyre.New(`(https?://[^\s()]+)\s+\((https?://[^\s()]+)\)`) // self-labeled "url (url)"
	gatewayAutoLink     = lazyre.New(`<((?:https?://)[^<>\s]+)>`)                     // auto-link <url>
	gatewayInlineMedia  = lazyre.New(`\[(?:image|video|audio|图片|视频|语音)\]`)            // i18n-ignore: archive data
	gatewayFileTag      = lazyre.New(`\[(?:file|文件)(?:\s*[:：]\s*|\s+)([^\[\]\n]+)\]`) // i18n-ignore: archive data
	gatewayFileLead     = lazyre.New(`^\[文件\]\s+([^\n]+)$`)                           // i18n-ignore: archive data
	gatewayBold         = lazyre.New(`\*\*([^*\n]+)\*\*`)                             // bold post title **title**
	gatewayBracketEmoji = lazyre.New(`\[([A-Z][A-Z0-9_+-]{1,31})\]`)                  // [THUMBSUP] -> :THUMBSUP:
	gatewayCardPrefix   = lazyre.New(`^(?:\[卡片\]|\[card\])\s+`)                       // i18n-ignore: archive data
)

// stripArchiveMedia separates trailing archived attachment markers ("[附件]" / "[附件](...)") and history kind
// placeholders ("[文件]", "[视频]", "[语音]") from an archived line's text. When "[附件]" markers are present, a
// preceding "[视频]"/"[语音]"/"[文件]" is part of the message text (formatBody) rather than a second attachment,
// preventing mediaCount double-counting against live sync lines.
func stripArchiveMedia(text string) (body, formatBody string, media bool, mediaCount int) {
	for {
		loc := trailingAttachment.FindStringIndex(text)
		if loc == nil {
			break
		}
		text, media = text[:loc[0]], true
		mediaCount++
	}
	formatBody = strings.TrimSpace(text)
	hasAttachment := media
	for {
		loc := trailingKindMedia.FindStringIndex(text)
		if loc == nil {
			break
		}
		if !hasAttachment {
			mediaCount++
		}
		text, media = text[:loc[0]], true
	}
	return strings.TrimSpace(text), formatBody, media, mediaCount
}

// equivalentGatewayBody reconciles renderer differences between larksync and the Lark gateway: Markdown/auto/plain
// links (including self-labeled links), HTML entities, bold post titles, card prefixes, emoji codes, @all/@所有人,
// named file wrappers, standalone card/sticker placeholders, and inline media placeholders backed by real media.
// Code and unsupported Markdown remain literal. A caption and an attachment's identity are not interchangeable: an
// empty caption never authorizes this fallback.
func equivalentGatewayBody(body string, mediaCount int) string {
	if !strings.Contains(body, "`") {
		body = normalizeGatewayLinks(body)
		body = gatewayAutoLink.ReplaceAllString(body, "$1")
		body = html.UnescapeString(body)
		body = gatewayBold.ReplaceAllString(body, "$1")
		body = strings.TrimSpace(body)
		body = gatewayCardPrefix.ReplaceAllString(body, "")
		body = strings.ReplaceAll(body, "@所有人", "@all") // i18n-ignore: archive data
		body = gatewayBracketEmoji.ReplaceAllString(body, ":$1:")
		switch strings.TrimSpace(body) {
		case "[卡片]": // i18n-ignore: archive data
			body = "[card]"
		case "[表情]", "[表情包]": // i18n-ignore: archive data
			body = "[sticker]"
		}
		if mediaCount > 0 {
			body = normalizeGatewayFilePlaceholders(body)
			if marks := gatewayInlineMedia.FindAllStringIndex(body, -1); len(marks) > 0 && len(marks) <= mediaCount {
				body = gatewayInlineMedia.ReplaceAllString(body, " ")
			}
		}
	}
	return strings.Join(strings.Fields(body), " ")
}

// normalizeGatewayLinks rewrites Markdown links "[label](url)" to "label (url)" (or plain "url" when label is empty
// or identical to url) and collapses identical "url (url)" pairs to "url". URL destinations and case stay verbatim.
func normalizeGatewayLinks(body string) string {
	if idxs := gatewayMarkdownLink.FindAllStringSubmatchIndex(body, -1); len(idxs) > 0 {
		var b strings.Builder
		last := 0
		for _, m := range idxs {
			b.WriteString(body[last:m[0]])
			label := strings.TrimSpace(body[m[2]:m[3]])
			href := body[m[4]:m[5]]
			if label == "" || label == href {
				b.WriteString(href)
			} else {
				b.WriteString(label + " (" + href + ")")
			}
			last = m[1]
		}
		b.WriteString(body[last:])
		body = b.String()
	}
	if idxs := gatewayParenLink.FindAllStringSubmatchIndex(body, -1); len(idxs) > 0 {
		var b strings.Builder
		last := 0
		for _, m := range idxs {
			b.WriteString(body[last:m[0]])
			left, right := body[m[2]:m[3]], body[m[4]:m[5]]
			if left == right {
				b.WriteString(left)
			} else {
				b.WriteString(body[m[0]:m[1]])
			}
			last = m[1]
		}
		b.WriteString(body[last:])
		body = b.String()
	}
	return body
}

// normalizeGatewayFilePlaceholders unwraps named file placeholders ("[file report.pdf]", "[文件: report.pdf]",
// "[文件] report.pdf") to their bare file name while preserving the file name verbatim so distinct files stay distinct.
func normalizeGatewayFilePlaceholders(body string) string {
	body = gatewayFileTag.ReplaceAllString(body, "$1")
	if trimmed := strings.TrimSpace(body); gatewayFileLead.MatchString(trimmed) {
		body = gatewayFileLead.ReplaceAllString(trimmed, "$1")
	}
	return body
}

// equivalentMediaKind returns a canonical media-kind token when body is solely a known explicit placeholder from
// larksync or the Lark gateway backed by real attachments. An empty body returns "" so an attachment-only line
// without a placeholder never matches a typed placeholder.
func equivalentMediaKind(body string, mediaCount int) string {
	if mediaCount <= 0 {
		return ""
	}
	switch strings.TrimSpace(body) {
	case "[image]", "[图片]": // i18n-ignore: archive data
		return "image"
	case "[video]", "[视频]": // i18n-ignore: archive data
		return "video"
	case "[audio]", "[语音]": // i18n-ignore: archive data
		return "audio"
	}
	return ""
}

// hasGatewayCandidate reports whether sync chat id is already paired in .crosswalk.json or shares a normalized name
// with at least one gateway ("lark") chat.
func (a *gatewayAligner) hasGatewayCandidate(id, chatName string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.crosswalk()[id]; ok {
		return true
	}
	name := normalizeText(chatName)
	return name != "" && len(a.gatewayChats()[name]) > 0
}

// withinArchived compares an already-archived larksync line against gateway chat gw using the same fingerprint and
// senderless-fallback rules as within. paired is true when gw came from chats/.crosswalk.json (L1).
func (a *gatewayAligner) withinArchived(gw, chatName string, src gatewayLine, paired bool) alignVerdict {
	sender := src.sender
	if sender == "" {
		sender = unknownSender
	}
	senderUnknown := sender == unknownSender
	body := strings.TrimSpace(src.body)
	media := src.media
	at := src.exact
	hasExact := !at.IsZero()
	if !hasExact {
		at = src.minute
	}

	var want, wantNoSender map[string]bool
	var same, sameNoSender string
	if hasExact {
		want = make(map[string]bool, 3)
		for k := -1; k <= 1; k++ {
			want[messageFingerprint(chatName, sender, at.Add(time.Duration(k)*2*time.Second), body)] = true
		}
	}
	same = messageFingerprint(chatName, sender, at, body)
	ensureNoSender := func() {
		if sameNoSender != "" {
			return
		}
		if hasExact {
			wantNoSender = make(map[string]bool, 3)
			for k := -1; k <= 1; k++ {
				wantNoSender[messageFingerprint(chatName, "", at.Add(time.Duration(k)*2*time.Second), body)] = true
			}
		}
		sameNoSender = messageFingerprint(chatName, "", at, body)
	}

	for _, day := range alignDays(at, a.s.Zone()) {
		for _, l := range a.day(gw, day) {
			if l.body == "" && body == "" && (!l.media || !media) {
				continue
			}
			fallbackSender := (senderUnknown && l.sender != ownerSender) || (l.sender == unknownSender && sender != ownerSender)
			if hasExact && !l.exact.IsZero() {
				if want[messageFingerprint(chatName, l.sender, l.exact, l.body)] {
					return dupVerdict
				}
				if fallbackSender {
					ensureNoSender()
					if wantNoSender[messageFingerprint(chatName, "", l.exact, l.body)] {
						return dupVerdict
					}
				}
				continue
			}
			if hasExact {
				if at.Before(l.minute.Add(-2*time.Second)) || !at.Before(l.minute.Add(time.Minute+2*time.Second)) {
					continue
				}
			} else if !l.exact.IsZero() {
				if l.exact.Before(src.minute.Add(-2*time.Second)) || !l.exact.Before(src.minute.Add(time.Minute+2*time.Second)) {
					continue
				}
			} else {
				if !src.minute.Equal(l.minute) {
					continue
				}
			}
			if messageFingerprint(chatName, l.sender, at, l.body) == same {
				return dupVerdict
			}
			if fallbackSender && body != "" {
				ensureNoSender()
				if messageFingerprint(chatName, "", at, l.body) == sameNoSender {
					return dupVerdict
				}
			}
		}
	}
	if a.withinFormatted(gw, src, paired) {
		return dupVerdict
	}
	return a.withinLoose(gw, src, paired)
}

// parseArchiveLine parses one formatted dayLine (from dayLines) into a gatewayLine for alignment.
func parseArchiveLine(dl dayLine, day string, zone *time.Location, exact time.Time) (gatewayLine, bool) {
	ms := messages(dl.text)
	if len(ms) != 1 {
		return gatewayLine{}, false
	}
	t := ms[0].text
	if len(t) < 6 || t[5] != ' ' {
		return gatewayLine{}, false
	}
	minute, err := time.ParseInLocation("2006-01-02 15:04", day+" "+t[:5], zone)
	if err != nil {
		return gatewayLine{}, false
	}
	sender, text, ok := strings.Cut(t[6:], ": ")
	if !ok {
		return gatewayLine{}, false
	}
	gl := gatewayLine{minute: minute, exact: exact, sender: sender}
	text, gl.formatBody, gl.media, gl.mediaCount = stripArchiveMedia(text)
	// Also strip larksync attachment placeholders when attachments were not downloaded (older than attachment_days).
	if gl.media {
		for {
			loc := syncPlaceholder.FindStringIndex(text)
			if loc == nil {
				break
			}
			text = text[:loc[0]]
		}
	}
	gl.body = strings.TrimSpace(text)
	return gl, true
}

// DedupeLarkSyncChats runs a one-time, version-marked, idempotent backfill pass over already-ingested larksync-*
// chats to align and remove messages that duplicate a Lark gateway ("lark-*") chat, recording their pairing in
// chats/.crosswalk.json.
//
// Why overlapping chats stay separate directories linked by .crosswalk.json rather than merged into one chat file:
// attributeChat enforces single-connector ownership per chat directory (for deletion, retention and cursors), and
// chat day files are plain Markdown lines without per-message sources[]. Removing only the duplicated lines from the
// larksync copy while keeping their receipt IDs in larksync-<id>/.receipts.json and linking both chats in
// .crosswalk.json eliminates duplicate messages, keeps all non-overlapping messages intact with zero content loss,
// and preserves both connectors' provenance (gateway copy in lark-*, unique messages + received IDs in larksync-*,
// plus canonical sources in lark_sync_index).
func (e *Engine) DedupeLarkSyncChats() int {
	if e.Store.ReadOnly() {
		return 0
	}
	markerPath := filepath.Join(e.Store.Dir, KindChat, larkSyncRepairFile)
	var marker larkSyncRepairState
	if ok, err := readJSON(markerPath, &marker); err == nil && ok && marker.Version >= larkSyncRepairVersion {
		return 0
	}

	e.Store.ingestMu.Lock()
	defer e.Store.ingestMu.Unlock()

	if ok, err := readJSON(markerPath, &marker); err == nil && ok && marker.Version >= larkSyncRepairVersion {
		return 0
	}

	aligner := &gatewayAligner{s: e.Store, now: time.Now, days: map[string]gatewayDay{}}
	var targets []*Chat
	for _, c := range e.Store.Chats() {
		if c.Source == larksyncwire.ConnectorID || c.Connector == larksyncwire.ConnectorID {
			targets = append(targets, c)
		}
	}

	totalDropped := 0
	allOK := true
	// Gateway lines a fuller sync line supersedes (align_loose.go), by gateway chat: dropped after every sync chat
	// was read, so the aligner compared against the gateway archive as it was.
	gatewayDrops := map[string]map[string]bool{}
	for _, tc := range targets {
		if !aligner.hasGatewayCandidate(tc.ID, tc.Name) {
			continue
		}
		dropped, err := e.dedupeLarkSyncChat(aligner, tc.ID, gatewayDrops)
		if err != nil {
			allOK = false
			e.logf("dedupe larksync %s: %v", tc.ID, err)
			continue
		}
		totalDropped += dropped
	}
	for _, gw := range slices.Sorted(maps.Keys(gatewayDrops)) {
		dropped, err := e.dropGatewayLines(gw, gatewayDrops[gw])
		if err != nil {
			allOK = false
			e.logf("dedupe superseded gateway lines %s: %v", gw, err)
			continue
		}
		totalDropped += dropped
	}
	if totalDropped > 0 {
		e.Store.SearchIndexPurge()
	}
	if allOK {
		if raw, err := json.MarshalIndent(larkSyncRepairState{Version: larkSyncRepairVersion}, "", "  "); err == nil {
			if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err == nil {
				_ = managerconf.WriteFileAtomic(markerPath, append(raw, '\n'), 0o600)
			}
		}
	}
	return totalDropped
}

// dedupeLarkSyncChat removes already-ingested lines in larksync chat id that match a gateway chat, keeping their
// receipt IDs (so replay never re-writes them) and updating counts and retention ledgers atomically. A line fuller
// than its gateway copy stays, and the gateway line is added to gatewayDrops ("<day>\x00<key>" by gateway chat).
// Caller holds e.Store.ingestMu.
func (e *Engine) dedupeLarkSyncChat(aligner *gatewayAligner, id string, gatewayDrops map[string]map[string]bool) (int, error) {
	return e.dropChatLines(id, func(chatName, day string, dl dayLine, _ string, _ bool, exact time.Time) bool {
		gl, ok := parseArchiveLine(dl, day, e.Store.Zone(), exact)
		if !ok {
			return false
		}
		v := aligner.alignArchivedLine(id, chatName, gl)
		if v.drop != nil {
			if gatewayDrops[v.drop.chat] == nil {
				gatewayDrops[v.drop.chat] = map[string]bool{}
			}
			gatewayDrops[v.drop.chat][v.drop.day+"\x00"+v.drop.key] = true
			return false
		}
		return v.dup
	})
}

// dropGatewayLines removes the lines of gateway chat gw that keys names ("<day>\x00<key>"), each only while it is the
// one line of its day with that key; their receipt IDs stay. Caller holds e.Store.ingestMu.
func (e *Engine) dropGatewayLines(gw string, keys map[string]bool) (int, error) {
	return e.dropChatLines(gw, func(_, day string, _ dayLine, key string, unique bool, _ time.Time) bool {
		return unique && key != "" && keys[day+"\x00"+key]
	})
}

// dropChatLines removes the lines of chat id that drop picks (given the chat's name, the line's day, the line, its
// receipt key, whether that key is the only one of its day and its exact time when the receipts bind it), keeping
// their receipt IDs (so replay never re-writes them) and updating counts and retention ledgers atomically.
// Caller holds e.Store.ingestMu.
func (e *Engine) dropChatLines(id string, drop func(chatName, day string, dl dayLine, key string, unique bool, exact time.Time) bool) (int, error) {
	s := e.Store
	s.chatMu.Lock()
	if err := s.recoverIngest(id); err != nil {
		s.chatMu.Unlock()
		return 0, err
	}
	c, ok := s.Chat(id)
	if !ok {
		s.chatMu.Unlock()
		return 0, nil
	}
	r, err := s.receipts(c.Source, c.SourceID, id)
	if err != nil {
		s.chatMu.Unlock()
		return 0, err
	}
	zone := s.Zone()
	days := s.Days(id)
	ledger, hasLedger, lerr := s.readLedger(id)
	s.chatMu.Unlock()

	type boundRef struct {
		ref string
		at  time.Time
	}
	updatedDays := map[string][]byte{}
	var emptyDays []string
	droppedTotal := 0
	ledgerChanged := false

	for _, day := range days {
		p := s.path(KindChat, id, "messages", day+".md")
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		rows, endsNL, lines := dayLines(string(raw))
		if len(lines) == 0 {
			continue
		}
		byReceipt := map[string][]boundRef{}
		for ref, lr := range r.Lines {
			at, exists := r.IDs[ref]
			if !exists || lr.Hash == "" || at.In(zone).Format("2006-01-02") != day {
				continue
			}
			k := at.In(zone).Format("15:04 ") + lr.Hash
			byReceipt[k] = append(byReceipt[k], boundRef{ref: ref, at: at})
		}
		lineCounts := map[string]int{}
		lineKeys := make([]string, len(lines))
		for i, dl := range lines {
			if dl.key == "" {
				continue
			}
			ms := messages(dl.text)
			if len(ms) == 1 && len(ms[0].text) >= 6 && ms[0].text[5] == ' ' {
				k := ms[0].text[:6] + receiptBodyHash(ms[0].text[6:])
				lineKeys[i] = k
				lineCounts[k]++
			}
		}

		gone := map[int]bool{}
		droppedDay := 0
		keptAny := false
		keptKeys := map[string]bool{}
		for i, dl := range lines {
			if dl.key == "" {
				keptAny = true
				continue
			}
			k := lineKeys[i]
			var exact time.Time
			if k != "" && lineCounts[k] == 1 && len(byReceipt[k]) == 1 {
				exact = byReceipt[k][0].at
			}
			if drop(c.Name, day, dl, k, k != "" && lineCounts[k] == 1, exact) {
				droppedDay++
				for row := dl.first; row <= dl.last; row++ {
					gone[row] = true
				}
				if refs := byReceipt[k]; len(refs) > 0 {
					delete(r.Lines, refs[0].ref)
					byReceipt[k] = refs[1:]
				}
				continue
			}
			keptAny = true
			keptKeys[dl.key] = true
		}
		if droppedDay == 0 {
			continue
		}
		droppedTotal += droppedDay
		if !keptAny {
			emptyDays = append(emptyDays, day)
			if hasLedger && lerr == nil {
				if _, ok := ledger.Lines[day]; ok {
					delete(ledger.Lines, day)
					ledgerChanged = true
				}
			}
		} else {
			updatedDays[day] = []byte(joinRows(rows, endsNL, gone))
			if hasLedger && lerr == nil {
				for key := range ledger.Lines[day] {
					if !keptKeys[key] {
						delete(ledger.Lines[day], key)
						ledgerChanged = true
					}
				}
				if _, ok := ledger.Lines[day]; ok && len(ledger.Lines[day]) == 0 {
					delete(ledger.Lines, day)
					ledgerChanged = true
				}
			}
		}
	}

	if droppedTotal == 0 {
		return 0, nil
	}

	s.chatMu.Lock()
	var finalChat Chat
	err = func() error {
		defer s.chatMu.Unlock()
		s.lockArchive()
		defer s.unlockArchive()
		if err := s.pendingIngestLocked(id); err != nil {
			return err
		}
		var fresh Chat
		exists, err := readJSON(s.entityPath(KindChat, id), &fresh)
		if err != nil || !exists {
			return err
		}
		fresh.Messages = max(0, fresh.Messages-droppedTotal)
		fresh.Pending = max(0, fresh.Pending-droppedTotal)
		current := map[string]json.RawMessage{}
		if _, err := readJSON(s.entityPath(KindChat, id), &current); err != nil {
			return err
		}
		incoming := protectedEntity(&fresh, &fresh.Tagging, current, false)
		metaRaw, err := json.MarshalIndent(incoming, "", "  ")
		if err != nil {
			return err
		}
		in := ingestIntent{Version: 1, ChatID: id, Days: updatedDays, Receipts: r, Meta: metaRaw}
		if hasLedger && lerr == nil && ledgerChanged {
			if lraw, err := json.MarshalIndent(ledger, "", "  "); err == nil {
				in.Retention = append(lraw, '\n')
			}
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if err := s.writeIngest(s.path(KindChat, id, ingestJournal), raw); err != nil {
			return err
		}
		if err := s.finishIngestLocked(&in); err != nil {
			return err
		}
		for _, d := range emptyDays {
			_ = os.Remove(s.path(KindChat, id, "messages", d+".md"))
		}
		if err := json.Unmarshal(in.Meta, &fresh); err == nil {
			finalChat = fresh
		}
		return nil
	}()
	if err != nil {
		return 0, err
	}
	s.notify(KindChat, id, "messages")
	s.notify(KindChat, id, "metadata")
	if finalChat.ID != "" {
		e.index(KindChat, finalChat.ID, finalChat.Name, finalChat.Tagging, finalChat.OneLine, finalChat.LastAt)
	}
	return droppedTotal, nil
}

// repairLarkSyncSenders repairs already-ingested pre-#15486 larksync messages whose sender was written as "?" when a
// backfill batch delivers their resolved SenderName. For each matching unnamed line:
//   - if e.Align now identifies it as a duplicate of a gateway ("lark") chat message, the duplicate line is removed
//     while keeping its receipt ID in .receipts.json (and pairing .crosswalk.json);
//   - otherwise the line's sender prefix is updated in place from "?:" to "<SenderName>:", its receipt hash and
//     retention ledger key are updated, and the sender's Person record (and unnamed DM chat name) is updated.
//
// Idempotent: replaying the same batch finds no "?:" line matching the receipt hash and makes no writes.
// Caller holds e.Store.ingestMu.
func (e *Engine) repairLarkSyncSenders(src Source, sc SourceChat, cut time.Time, repairs []SourceMessage) error {
	s := e.Store
	id := ChatID(src.Name(), sc.ID)

	// Pre-check which repairs actually correspond to an unnamed ("?: ") line on disk before invoking e.Align
	// (so normal replays of already-named messages never trigger extra alignment calls or writes).
	s.chatMu.Lock()
	c0, exists, r0, err := s.ingestState(src.Name(), sc, cut)
	if err != nil || !exists {
		s.chatMu.Unlock()
		return err
	}
	zone0 := s.Zone()
	needNameFill := sc.Name != "" && c0.Name == ""
	var matchedRepairs []SourceMessage
	dayCache := map[string][]dayLine{}
	for _, m := range repairs {
		at, ok := r0.IDs[m.ID]
		if !ok {
			continue
		}
		lr, hasLine := r0.Lines[m.ID]
		if !hasLine || lr.Mine || lr.Hash == "" {
			continue
		}
		day := at.In(zone0).Format("2006-01-02")
		lines, cached := dayCache[day]
		if !cached {
			if raw, rerr := os.ReadFile(s.path(KindChat, id, "messages", day+".md")); rerr == nil {
				_, _, lines = dayLines(string(raw))
			}
			dayCache[day] = lines
		}
		hhmm := at.In(zone0).Format("15:04")
		for _, dl := range lines {
			if dl.key == "" {
				continue
			}
			ms := messages(dl.text)
			if len(ms) == 1 && len(ms[0].text) >= 6 && ms[0].text[5] == ' ' &&
				ms[0].text[:5] == hhmm &&
				strings.HasPrefix(ms[0].text[6:], unknownSender+": ") &&
				receiptBodyHash(ms[0].text[6:]) == lr.Hash {
				matchedRepairs = append(matchedRepairs, m)
				break
			}
		}
	}
	s.chatMu.Unlock()

	if len(matchedRepairs) == 0 && !needNameFill {
		return nil
	}

	// Run alignment outside chatMu/lockArchive because gatewayAligner.gatewayChats acquires metaMu via Store.Chats.
	alignedDup := map[string]bool{}
	if e.Align != nil {
		for _, m := range matchedRepairs {
			if e.Align(src, sc, m) {
				alignedDup[m.ID] = true
			}
		}
	}

	s.chatMu.Lock()
	c, exists, r, err := s.ingestState(src.Name(), sc, cut)
	if err != nil || !exists {
		s.chatMu.Unlock()
		return err
	}
	if err := attributeChat(c, src); err != nil {
		s.chatMu.Unlock()
		return err
	}
	zone := s.Zone()
	ledger, hasLedger, lerr := s.readLedger(id)

	// Group repairs by day file so each day is read and rewritten at most once.
	type dayState struct {
		rows     []string
		endsNL   bool
		lines    []dayLine
		gone     map[int]bool
		handled  map[int]bool
		modified bool
	}
	days := map[string]*dayState{}
	loadDay := func(day string) (*dayState, error) {
		if ds, ok := days[day]; ok {
			return ds, nil
		}
		raw, rerr := os.ReadFile(s.path(KindChat, id, "messages", day+".md"))
		if rerr != nil {
			if os.IsNotExist(rerr) {
				days[day] = nil
				return nil, nil
			}
			return nil, rerr
		}
		rows, endsNL, lines := dayLines(string(raw))
		ds := &dayState{rows: rows, endsNL: endsNL, lines: lines, gone: map[int]bool{}, handled: map[int]bool{}}
		days[day] = ds
		return ds, nil
	}

	var renamedMsgs []SourceMessage
	droppedCount := 0
	ledgerChanged := false
	changed := false
	senderReplacer := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

	for _, m := range matchedRepairs {
		at, ok := r.IDs[m.ID]
		if !ok {
			continue
		}
		lr, hasLine := r.Lines[m.ID]
		if !hasLine || lr.Mine || lr.Hash == "" {
			continue
		}
		day := at.In(zone).Format("2006-01-02")
		hhmm := at.In(zone).Format("15:04")
		ds, derr := loadDay(day)
		if derr != nil {
			s.chatMu.Unlock()
			return derr
		}
		if ds == nil {
			continue
		}
		for i := range ds.lines {
			dl := &ds.lines[i]
			if dl.key == "" || ds.handled[dl.first] {
				continue
			}
			ms := messages(dl.text)
			if len(ms) != 1 || len(ms[0].text) < 6 || ms[0].text[5] != ' ' {
				continue
			}
			t := ms[0].text
			if t[:5] != hhmm || !strings.HasPrefix(t[6:], unknownSender+": ") {
				continue
			}
			if receiptBodyHash(t[6:]) != lr.Hash {
				continue
			}
			ds.handled[dl.first] = true
			ds.modified = true
			changed = true
			if alignedDup[m.ID] {
				for row := dl.first; row <= dl.last; row++ {
					ds.gone[row] = true
				}
				delete(r.Lines, m.ID)
				droppedCount++
				if hasLedger && lerr == nil {
					if _, ok := ledger.Lines[day][dl.key]; ok {
						delete(ledger.Lines[day], dl.key)
						if len(ledger.Lines[day]) == 0 {
							delete(ledger.Lines, day)
						}
						ledgerChanged = true
					}
				}
			} else {
				newSender := strings.TrimSpace(senderReplacer.Replace(m.SenderName))
				oldPrefix := "- " + hhmm + " " + unknownSender + ": "
				newPrefix := "- " + hhmm + " " + newSender + ": "
				if strings.HasPrefix(ds.rows[dl.first], oldPrefix) {
					ds.rows[dl.first] = newPrefix + ds.rows[dl.first][len(oldPrefix):]
					newText := strings.Join(ds.rows[dl.first:dl.last+1], "\n")
					newBody := newSender + ": " + t[6+len(unknownSender+": "):]
					r.Lines[m.ID] = messageReceiptLine{Hash: receiptBodyHash(newBody), Mine: false}
					if hasLedger && lerr == nil {
						if rule, ok := ledger.Lines[day][dl.key]; ok {
							delete(ledger.Lines[day], dl.key)
							if ledger.Lines[day] == nil {
								ledger.Lines[day] = map[string]string{}
							}
							ledger.Lines[day][lineKey(newText)] = rule
							ledgerChanged = true
						}
					}
					renamedMsgs = append(renamedMsgs, m)
				}
			}
			break
		}
	}

	nameFilled := sc.Name != "" && c.Name == ""
	if !changed && !nameFilled {
		s.chatMu.Unlock()
		return nil
	}
	if nameFilled {
		c.Name = sc.Name
	}
	// notePerson takes personMu (ordered after chatMu, before metaMu/lockArchive).
	for _, m := range renamedMsgs {
		e.notePerson(c, src.Name(), m)
	}

	updatedDays := map[string][]byte{}
	var emptyDays []string
	for day, ds := range days {
		if ds == nil || !ds.modified {
			continue
		}
		keptAny := false
		for _, dl := range ds.lines {
			if !ds.gone[dl.first] {
				keptAny = true
				break
			}
		}
		if !keptAny {
			emptyDays = append(emptyDays, day)
			if hasLedger && lerr == nil {
				if _, ok := ledger.Lines[day]; ok {
					delete(ledger.Lines, day)
					ledgerChanged = true
				}
			}
		} else {
			updatedDays[day] = []byte(joinRows(ds.rows, ds.endsNL, ds.gone))
		}
	}

	var finalChat Chat
	commitErr := func() error {
		defer s.chatMu.Unlock()
		s.lockArchive()
		defer s.unlockArchive()
		if err := s.pendingIngestLocked(id); err != nil {
			return err
		}
		c.Messages = max(0, c.Messages-droppedCount)
		c.Pending = max(0, c.Pending-droppedCount)
		current := map[string]json.RawMessage{}
		if _, err := readJSON(s.entityPath(KindChat, id), &current); err != nil {
			return err
		}
		incoming := protectedEntity(c, &c.Tagging, current, false)
		metaRaw, err := json.MarshalIndent(incoming, "", "  ")
		if err != nil {
			return err
		}
		in := ingestIntent{Version: 1, ChatID: id, Days: updatedDays, Receipts: r, Meta: metaRaw}
		if hasLedger && lerr == nil && ledgerChanged {
			if lraw, err := json.MarshalIndent(ledger, "", "  "); err == nil {
				in.Retention = append(lraw, '\n')
			}
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		if err := s.writeIngest(s.path(KindChat, id, ingestJournal), raw); err != nil {
			return err
		}
		if err := s.finishIngestLocked(&in); err != nil {
			return err
		}
		for _, d := range emptyDays {
			_ = os.Remove(s.path(KindChat, id, "messages", d+".md"))
		}
		if err := json.Unmarshal(in.Meta, &finalChat); err != nil {
			return err
		}
		return nil
	}()
	if commitErr != nil {
		return commitErr
	}
	s.notify(KindChat, id, "messages")
	s.notify(KindChat, id, "metadata")
	s.SearchIndexPurge()
	if finalChat.ID != "" {
		e.index(KindChat, finalChat.ID, finalChat.Name, finalChat.Tagging, finalChat.OneLine, finalChat.LastAt)
	}
	return nil
}
