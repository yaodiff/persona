package kb

import (
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// The loose tier of aligning the Lark user sync with the Lark gateway (align_gateway.go).
//
// The fingerprint and format tiers compare two renderings of one message after reconciling known renderer
// differences. Four kinds of difference were still seen on live archives after repair version 3: an image placeholder
// on one side only (the other renders the image as an attachment, or not at all), a leading @mention written another
// way or spaced differently, a card or rich post that one side expands in full and the other gives only its title,
// and two otherwise identical texts one character apart. The loose tier accepts those, and only those, and only
// within the tightest frame the earlier tiers use:
//
//   - the same chat (the crosswalk's pair, or the one gateway chat of that name), the same known sender (on a paired
//     chat an unknown sender may stand for any non-owner, as in the format tier), both times exact from the receipts
//     and in the same or a neighbouring 2-second bucket — or, on an already-paired gateway chat whose legacy archive
//     has no .receipts.json, the sync message's exact time within the gateway line's minute widened by one 2-second
//     bucket on each side;
//   - exactly one gateway line in that frame passes; two or more is ambiguous and nothing is dropped;
//   - the texts, with media/sticker/card placeholders removed (both sides' "[image]", "[Image]", "[photo]", "[sticker]",
//     "[表情]", "【图片】", "[附件](…)" and the like), are equal ignoring white space, or after one-sided standard Lark
//     inline emoticons ("[捂脸]", ":THUMBSUP:"), or after one leading @mention (at most looseMentionMost characters,
//     written otherwise or left out on one side) followed by the same text of at least looseTitleMin characters; or the
//     shorter one is the longer one's first lines (a card's or post's title, line by line, at least looseTitleMin
//     characters); or both are at least looseSimilarMin characters, differ in at most one character per looseSimilarPer
//     (98 % alike), and carry the same numbers and the same links.
//
// Why 98 % and not lower: two different messages from one sender in one 2-second bucket that share 98 % of at least
// 40 characters, every number and every link are not two messages a reader tells apart (a resend, an edit, one
// renderer clipping the last character). A lower ratio would start to accept template messages (alerts, bot reports)
// that differ by a word; requiring equal numbers and links keeps those apart even at 98 % (a build number, an amount,
// a host index or a URL is what usually distinguishes them). Short texts never match by similarity at all.
//
// Which copy stays: the fuller one (more characters once placeholders are removed, then more attachments). When the
// gateway's line is the shorter one (it kept a card's title only), the sync message is written and the gateway line is
// dropped instead, keeping its receipt ID, so the archive keeps the full card once.

// Thresholds of the loose tier (see above).
const (
	looseTitleMin    = 4  // the shorter text must have at least this many characters to stand for the longer's title
	looseSimilarMin  = 40 // both texts must have at least this many characters to match by similarity
	looseSimilarPer  = 50 // at most one differing character per this many characters of the longer text (98 %)
	looseMentionMost = 40 // the longest leading mention (white space removed) the mention rule sets aside
)

var (
	looseMedia        = lazyre.New(`(?i)\[(?:image|img|photo|picture|pic|video|audio|voice|media|file|attachment|sticker|emoji|emotion|emoticon|card|merge_forward|forward|location|share_chat|share_user|hongbao|图片|图像|照片|视频|语音|语音消息|音频|文件|附件|表情|表情包|贴纸|动画表情|卡片|合并转发|聊天记录|位置|红包|分享了一个群|分享了一张名片)\](?:\([^()\s]*\))?|【(?:图片|图像|照片|视频|语音|语音消息|音频|文件|附件|表情|表情包|贴纸|动画表情|卡片|合并转发|聊天记录|位置|红包|分享了一个群|分享了一张名片)】`)                                                                                       // i18n-ignore: archive data
	looseBracketEmoji = lazyre.New(`(?:\[|【)(?:微笑|捂脸|偷笑|呲牙|抱拳|比心|狗头|笑哭|鼓掌|点赞|赞同|玫瑰|握手|加油|害羞|机智|裂开|吃瓜|合十|祈祷|感谢|谢谢|破涕为笑|得意|发呆|尴尬|流泪|委屈|坏笑|哈欠|鄙视|阴险|亲亲|可怜|笑脸|开心|打脸|白眼|思考|吐血|石化|发怒|抓狂|奋斗|疑问|敲打|傲慢|大哭|傻笑|震惊|惊喜|无语|爱心|心碎|礼花|烟花|礼物|咖啡|啤酒|干杯|蛋糕|擦汗|抠鼻|飞吻|撇嘴|惊恐|流汗|再见|拜拜|泪奔|撒花|摸头|摸鱼|赞叹|叹气|皱眉|调皮|吐舌|鬼脸|脸红|捂嘴|闭嘴|难过|伤心|泪目|悲伤|生气|害怕|冷汗|斜眼|骄傲|花痴|拜托|拍手|抱抱|拥抱|击掌|挥手|拳头|胜利|作揖|激动|欢呼|猪头|太阳|月亮|星星|彩虹|闪电|炸弹|西瓜|鸡腿|火锅|便便|发财|暴富|锦鲤|暗中观察|捂嘴笑|黑人问号|翻白眼|加油鸭|灵光一闪|好的|收到|赞|强|弱|汗|酷|晕|色|哭|笑|嘘|衰|困|睡|吐|吓|呆|耶|踩)(?:\]|】)`) // i18n-ignore: archive data
	looseColonEmoji   = lazyre.New(`:[A-Z][A-Z0-9_+-]{1,31}:`)
	looseAtTag        = lazyre.New(`<at\b[^>]*>([^<]*)</at>`)
	looseMention      = lazyre.New(`^(?:@\S+(?:\s+|$))+`)
	looseDigits       = lazyre.New(`[0-9]+`)
	looseURL          = lazyre.New(`https?://[^\s()<>]+`)
)

// gatewayDrop is a gateway chat's line to drop: its chat id, its day and its receipt key (gatewayLine.key).
type gatewayDrop struct {
	chat, day, key string
}

// alignVerdict is what comparing a sync message with a gateway chat found: dup, the sync copy is a duplicate; drop,
// the gateway line is the shorter copy of the same message and goes instead; neither, no match.
type alignVerdict struct {
	dup  bool
	drop *gatewayDrop
}

var dupVerdict = alignVerdict{dup: true}

func (v alignVerdict) matched() bool { return v.dup || v.drop != nil }

// looseText is a message's text as the loose tier compares it.
type looseText struct {
	lines  []string // its non-empty lines, white space collapsed, the first without its leading @mentions
	flat   string   // lines joined without any white space
	full   string   // the same with the leading @mentions kept
	runes  int      // characters of flat
	digits []string // its numbers, in order
	urls   []string // its links, in order
}

func looseForm(raw string, mediaCount int) looseText {
	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		l = looseAtTag.ReplaceAllString(l, "@$1")
		l = equivalentGatewayBody(l, mediaCount)
		l = strings.Join(strings.Fields(looseMedia.ReplaceAllString(l, " ")), " ")
		if l != "" {
			lines = append(lines, l)
		}
	}
	var t looseText
	t.full = noSpace(strings.Join(lines, ""))
	if len(lines) > 0 {
		if first := strings.TrimSpace(looseMention.ReplaceAllString(lines[0], "")); first == "" {
			lines = lines[1:]
		} else {
			lines[0] = first
		}
	}
	t.lines = lines
	t.flat = noSpace(strings.Join(lines, ""))
	t.runes = utf8.RuneCountInString(t.flat)
	joined := strings.Join(lines, " ")
	t.digits = looseDigits.FindAllString(joined, -1)
	t.urls = looseURL.FindAllString(joined, -1)
	return t
}

func noSpace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// looseMatch says whether a and b are one message by the loose tier's text rules (match), and whether one holds less
// of it than the other (shorter: a title, a character fewer) rather than the same text written another way (white
// space, placeholders, mentions).
func looseMatch(a, b looseText) (match, shorter bool) {
	if a.full == "" || b.full == "" {
		return false, false
	}
	if a.full == b.full || sameAfterMention(a.full, b.full) || sameIgnoringOneSidedEmoji(a.full, b.full) {
		return true, false
	}
	short, long := a, b
	if short.runes > long.runes {
		short, long = b, a
	}
	if n := len(short.lines); n > 0 && n < len(long.lines) && short.runes >= looseTitleMin {
		title := true
		for i := range short.lines {
			if noSpace(short.lines[i]) != noSpace(long.lines[i]) {
				title = false
				break
			}
		}
		if title {
			return true, true
		}
	}
	ar, br := []rune(a.flat), []rune(b.flat)
	if min(len(ar), len(br)) < looseSimilarMin || !slices.Equal(a.digits, b.digits) || !slices.Equal(a.urls, b.urls) {
		return false, false
	}
	if editsWithin(ar, br, max(1, max(len(ar), len(br))/looseSimilarPer)) {
		return true, true
	}
	return false, false
}

// sameAfterMention says whether a and b (white space removed) are the same text after a leading @mention that one
// side writes otherwise or leaves out: what follows their longest common ending is, on each side, nothing or one
// mention ("@" and at most looseMentionMost characters, a display name may hold spaces), and that ending is at least
// looseTitleMin characters.
func sameAfterMention(a, b string) bool {
	if !strings.HasPrefix(a, "@") && !strings.HasPrefix(b, "@") {
		return false
	}
	ar, br := []rune(a), []rune(b)
	n := 0
	for n < len(ar) && n < len(br) && ar[len(ar)-1-n] == br[len(br)-1-n] {
		n++
	}
	if n < looseTitleMin {
		return false
	}
	head := func(r []rune) bool {
		h := r[:len(r)-n]
		return len(h) == 0 || (h[0] == '@' && len(h) <= looseMentionMost)
	}
	return head(ar) && head(br)
}

// sameIgnoringOneSidedEmoji reports whether a and b (white space removed) are identical (or sameAfterMention) of at
// least looseTitleMin runes once standard Lark/Feishu inline emoticons are stripped, provided the difference is
// one-sided (one renderer omitted the emoticon) or cross-format (Chinese bracket emoticon vs ":CODE:") rather than two
// different emoticons of the same format on otherwise identical text.
func sameIgnoringOneSidedEmoji(a, b string) bool {
	aBracket, bBracket := looseBracketEmoji.MatchString(a), looseBracketEmoji.MatchString(b)
	aColon, bColon := looseColonEmoji.MatchString(a), looseColonEmoji.MatchString(b)
	if !(aBracket || bBracket || aColon || bColon) {
		return false
	}
	if (aBracket && bBracket) || (aColon && bColon) {
		return false
	}
	strip := func(s string) string {
		s = looseBracketEmoji.ReplaceAllString(s, "")
		return looseColonEmoji.ReplaceAllString(s, "")
	}
	sa, sb := strip(a), strip(b)
	if utf8.RuneCountInString(sa) < looseTitleMin || utf8.RuneCountInString(sb) < looseTitleMin {
		return false
	}
	return sa == sb || sameAfterMention(sa, sb)
}

// editsWithin reports whether a and b are at most k insertions, deletions or substitutions apart (Levenshtein,
// stopping as soon as every path is over k).
func editsWithin(a, b []rune, k int) bool {
	if d := len(a) - len(b); d > k || -d > k {
		return false
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			best = min(best, cur[j])
		}
		if best > k {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(b)] <= k
}

// syncFuller says whether the sync copy (s, with sMedia attachments) holds more than the gateway's (g): when one
// holds less of the message (shorter), the one with more characters once placeholders and leading
// mentions are set aside; otherwise, and on a tie, the one with more attachments. Texts that differ only in white
// space, placeholders or how a mention is written are equally full, and the gateway's copy stays.
func syncFuller(shorter bool, s, g looseText, sMedia, gMedia int) bool {
	if shorter && s.runes != g.runes {
		return s.runes > g.runes
	}
	return sMedia > gMedia
}

// withinLoose looks for src (a sync message, or an archived sync line) in gateway chat gw by the loose tier's rules.
// paired is true when gw came from chats/.crosswalk.json (L1).
func (a *gatewayAligner) withinLoose(gw string, src gatewayLine, paired bool) alignVerdict {
	if src.exact.IsZero() {
		return alignVerdict{}
	}
	srcUnknown := src.sender == "" || src.sender == unknownSender
	if !paired && srcUnknown {
		return alignVerdict{}
	}
	srcRaw := src.formatBody
	if srcRaw == "" {
		srcRaw = src.body
	}
	sf := looseForm(srcRaw, max(src.mediaCount, 1))
	if sf.full == "" {
		return alignVerdict{}
	}
	var found gatewayLine
	var foundDay string
	var foundText looseText
	var foundShorter bool
	matches := 0
	for _, day := range alignDays(src.exact, a.s.Zone()) {
		for _, dst := range a.day(gw, day) {
			if !withinTimeWindow(src.exact, dst, paired) {
				continue
			}
			dstUnknown := dst.sender == "" || dst.sender == unknownSender
			senderMatch := !srcUnknown && !dstUnknown && normalizeText(dst.sender) == normalizeText(src.sender)
			if !senderMatch && paired {
				senderMatch = (srcUnknown && dst.sender != ownerSender) || (dstUnknown && src.sender != ownerSender)
			}
			if !senderMatch {
				continue
			}
			dstRaw := dst.formatBody
			if dstRaw == "" {
				dstRaw = dst.body
			}
			df := looseForm(dstRaw, max(dst.mediaCount, 1))
			if match, shorter := looseMatch(sf, df); match {
				matches++
				found, foundDay, foundText, foundShorter = dst, day, df, shorter
			}
		}
	}
	if matches != 1 {
		return alignVerdict{}
	}
	if syncFuller(foundShorter, sf, foundText, src.mediaCount, found.mediaCount) {
		if !found.unique || found.key == "" {
			return alignVerdict{} // the gateway line cannot be told from an identical one: keep both copies
		}
		return alignVerdict{drop: &gatewayDrop{chat: gw, day: foundDay, key: found.key}}
	}
	return dupVerdict
}

// settleSuperseded drops the gateway lines the aligner found shorter than sync messages of chat syncID it let through
// (gatewayAligner.settle), for those messages that are now written in the chat; the rest are forgotten (a message
// the selection did not keep leaves the gateway's copy alone). Caller holds e.Store.ingestMu, not chatMu.
func (e *Engine) settleSuperseded(syncID string) {
	s := e.Store
	pending := s.superseded[syncID]
	if len(pending) == 0 {
		return
	}
	delete(s.superseded, syncID)
	var r messageReceipts
	if ok, err := readJSON(s.path(KindChat, syncID, ingestReceipts), &r); err != nil || !ok {
		return
	}
	drops := map[string]map[string]bool{}
	for id, d := range pending {
		if _, written := r.Lines[id]; !written {
			continue
		}
		if drops[d.chat] == nil {
			drops[d.chat] = map[string]bool{}
		}
		drops[d.chat][d.day+"\x00"+d.key] = true
	}
	dropped := 0
	for _, gw := range slices.Sorted(maps.Keys(drops)) {
		n, err := e.dropGatewayLines(gw, drops[gw])
		if err != nil {
			e.logf("drop superseded gateway lines %s: %v", gw, err)
			continue
		}
		dropped += n
	}
	if dropped > 0 {
		s.SearchIndexPurge()
	}
}
