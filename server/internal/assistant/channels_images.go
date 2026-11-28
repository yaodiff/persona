package assistant

import (
	"context"
	"net/url"
	"strings"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Pictures in an answer to a channel that sends them (#11986, docs/extensions/channels.md "Capabilities"): a picture
// on a line of its own (the block images the apps draw, docs/assistant/assistant.md "Pictures in replies (#9473)") is
// taken out of the text and sent as a picture of the chat app. It is fetched as the apps' pictures are, through
// remoteimage (the open internet only, checked and scaled), so a channel never makes the manager fetch what the
// apps would not. Inline pictures and pictures in code (fenced or indented) stay in the text; a picture indented under
// a list item is that item's, not code, unless it is four columns past the item's content, as CommonMark reads it. One
// past ImagesMost or one that can't be fetched becomes a line naming it with its address, so it is not lost.

// channelImageSides are the long sides a picture a channel sends is drawn at, the first whose picture fits ImageMost:
// as large as a remote image gets, then smaller (each drawn from the source remoteimage keeps).
var channelImageSides = []int{2048, 1024, 512}

// markdownImage is one Markdown image: ![alt](address "title"), the address optionally in <>.
var markdownImage = lazyre.New(`!\[([^\]\n]*)\]\(\s*<?([^\s()<>]+)>?(?:\s+"[^"\n]*")?\s*\)`)

// blockImage is one picture of an answer's block-image line.
type blockImage struct{ alt, address string }

// channelImages takes the pictures on lines of their own out of an answer to a channel that sends them, fetched and
// fitted to the channel's limits. text is the answer without them, a picture that could not be fetched named in its
// place.
func (s *Service) channelImages(ctx context.Context, id, text string) (string, []channel.Blob) {
	lines := strings.Split(text, "\n")
	gone := make([]bool, len(lines)) // lines that were nothing but pictures, all sent
	var blobs []channel.Blob
	changed := false
	fence := ""
	// para: the line before was a paragraph's, which an indented line continues; code: in an indented code block;
	// items: the content columns of the open list items, innermost last.
	para, code := false, false
	var items []int
	lang := s.lang(ctx)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		indent := indentOf(line)
		base := 0 // where the innermost open list item's content starts; code and fences count from it
		if len(items) > 0 {
			base = items[len(items)-1]
		}
		if fence != "" {
			if indent < base+4 && strings.HasPrefix(trimmed, fence) && strings.TrimLeft(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if trimmed == "" {
			para = false // a blank line ends a paragraph, not an indented code block
			continue
		}
		if !para { // after a blank line, a line left of an item's content is outside that item
			for len(items) > 0 && indent < items[len(items)-1] {
				items = items[:len(items)-1]
			}
			base = 0
			if len(items) > 0 {
				base = items[len(items)-1]
			}
		}
		if indent >= base+4 && (!para || code) { // CommonMark indented code: its pictures stay text
			code = true
			continue
		}
		code, para = false, true
		if content, ok := listItem(line); ok { // a new item, which may also end a paragraph
			for len(items) > 0 && indent < items[len(items)-1] {
				items = items[:len(items)-1]
			}
			items = append(items, content)
			continue
		}
		if mark := fenceOf(trimmed); mark != "" && indent < base+4 { // further in, it continues the paragraph
			fence, para = mark, false
			continue
		}
		pics, ok := blockImages(trimmed)
		if !ok {
			continue
		}
		var kept []string
		for _, pic := range pics {
			if len(blobs) >= channel.ImagesMost {
				kept = append(kept, imageLink(lang, pic))
				continue
			}
			blob, ok := s.channelImage(ctx, id, pic.address)
			if !ok {
				kept = append(kept, imageLink(lang, pic))
				continue
			}
			blobs = append(blobs, blob)
		}
		lines[i], gone[i], changed = strings.Join(kept, "\n"), len(kept) == 0, true
	}
	if !changed {
		return text, nil
	}
	// Even when no picture could be sent, each is named by its line: the text keeps no Markdown image a plain
	// channel would show as code.
	return withoutLines(lines, gone), blobs
}

// indentOf is how far line is indented, a tab counting to the next multiple of four columns.
func indentOf(line string) int {
	n := 0
	for _, c := range line {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n
		}
	}
	return n
}

// listItem reports whether line starts a CommonMark list item ("- ", "* ", "+ ", "1. ", "1) ") and the column its
// content starts at: past the marker and the spaces after it, or one past the marker when there are five or more
// (the item's content is then indented code) or nothing follows it. A thematic break ("- - -", "***") is not an item.
func listItem(line string) (int, bool) {
	col, rest := 0, line
	for rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		if rest[0] == '\t' {
			col += 4 - col%4
		} else {
			col++
		}
		rest = rest[1:]
	}
	if thematicBreak(rest) {
		return 0, false
	}
	n := 0
	switch {
	case rest != "" && strings.IndexByte("-*+", rest[0]) >= 0:
		n = 1
	default:
		for n < len(rest) && n < 9 && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		if n == 0 || n >= len(rest) || (rest[n] != '.' && rest[n] != ')') {
			return 0, false
		}
		n++
	}
	marker := col + n
	rest = rest[n:]
	if rest == "" || strings.TrimSpace(rest) == "" {
		return marker + 1, true
	}
	if rest[0] != ' ' && rest[0] != '\t' {
		return 0, false
	}
	end := marker
	for rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
		if rest[0] == '\t' {
			end += 4 - end%4
		} else {
			end++
		}
		rest = rest[1:]
	}
	if end-marker > 4 {
		return marker + 1, true
	}
	return end, true
}

// thematicBreak reports whether s (a line without its indent) is three or more of one of "-", "*", "_", with spaces.
func thematicBreak(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.IndexByte("-*_", s[0]) < 0 {
		return false
	}
	n := 0
	for _, c := range s {
		switch {
		case byte(c) == s[0]:
			n++
		case c != ' ' && c != '\t':
			return false
		}
	}
	return n >= 3
}

// channelImage is the picture at address as a channel sends it: JPEG or PNG, at most ImageMost. ok is false for an
// address that isn't on the open internet, a type the chat app can't show (HEIC, AVIF) and a fetch that failed.
func (s *Service) channelImage(ctx context.Context, id, address string) (channel.Blob, bool) {
	get := s.channelPicture
	if get == nil && s.remoteImages != nil {
		get = s.remoteImages.Get
	}
	if get == nil {
		return channel.Blob{}, false
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return channel.Blob{}, false
	}
	for _, side := range channelImageSides { // a smaller size when a picture's detail keeps it over the limit
		img, err := get(ctx, "channel:"+id, address, side)
		if err != nil {
			s.logf("channels: %s: a picture of the answer was not fetched: %v", id, err)
			return channel.Blob{}, false
		}
		if img.Type != "image/jpeg" && img.Type != "image/png" {
			return channel.Blob{}, false
		}
		if len(img.Data) <= channel.ImageMost {
			return channel.Blob{MIME: img.Type, Data: img.Data}, true
		}
	}
	return channel.Blob{}, false
}

// blockImages are the pictures of a line that has nothing but Markdown images (and spaces) on it; ok is false for any
// other line.
func blockImages(line string) ([]blockImage, bool) {
	if !strings.HasPrefix(line, "![") {
		return nil, false
	}
	matches := markdownImage.FindAllStringSubmatchIndex(line, -1)
	if len(matches) == 0 {
		return nil, false
	}
	var pics []blockImage
	at := 0
	for _, m := range matches {
		if strings.TrimSpace(line[at:m[0]]) != "" {
			return nil, false
		}
		pics = append(pics, blockImage{alt: strings.TrimSpace(line[m[2]:m[3]]), address: line[m[4]:m[5]]})
		at = m[1]
	}
	if strings.TrimSpace(line[at:]) != "" {
		return nil, false
	}
	return pics, true
}

// imageLink names a picture that was not sent: its description and its address, which the chat app makes a link.
func imageLink(lang i18n.Lang, pic blockImage) string {
	return i18n.T(lang, "assistant-channels_plugin.image_link", i18n.Args{"alt": i18n.Verbatim(pic.alt), "url": i18n.Verbatim(pic.address)})
}

// fenceOf is the fence a trimmed line opens or closes (``` or ~~~, three or more), "" when it is not one.
func fenceOf(trimmed string) string {
	for _, c := range []string{"`", "~"} {
		if strings.HasPrefix(trimmed, c+c+c) {
			n := len(trimmed) - len(strings.TrimLeft(trimmed, c))
			return trimmed[:n]
		}
	}
	return ""
}

// withoutLines joins lines without the gone ones, and without the blank line a gone one leaves doubled (or leading).
func withoutLines(lines []string, gone []bool) string {
	var out []string
	dropBlank := false
	for i, line := range lines {
		if gone[i] {
			dropBlank = len(out) == 0 || strings.TrimSpace(out[len(out)-1]) == ""
			continue
		}
		if dropBlank && strings.TrimSpace(line) == "" {
			continue
		}
		dropBlank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
