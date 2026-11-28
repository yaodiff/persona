package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/remoteimage"
)

// picturesRig is an assistant with a connected plain channel "examplechat" that sends pictures when imagesOut, in
// zh-Hans. It records what channels.send got and which pictures were fetched at which size; a fetch of an address
// under /missing fails, one under /heic is a HEIC, one under /big is over the limit at 2048 px.
func picturesRig(t *testing.T, imagesOut bool) (*Service, func() []map[string]any, func() []string) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.hello.Locale = "zh-Hans"
	var mu sync.Mutex
	var sends []map[string]any
	var fetched []string
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Format: channel.FormatPlain, MaxText: 3500,
				Asks: channel.AsksText, Push: true, ImagesOut: imagesOut}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		sends = append(sends, m)
		mu.Unlock()
		return map[string]any{}, nil
	})
	s := newTestService(t, f)
	s.channelPicture = func(_ context.Context, caller, rawURL string, size int) (*remoteimage.Image, error) {
		mu.Lock()
		fetched = append(fetched, fmt.Sprintf("%s %s %d", caller, rawURL, size))
		mu.Unlock()
		switch {
		case strings.Contains(rawURL, "/missing"):
			return nil, &remoteimage.Error{Code: remoteimage.CodeFetchFailed, Status: 502, Detail: "gone"}
		case strings.Contains(rawURL, "/heic"):
			return &remoteimage.Image{Type: "image/heic", Data: []byte("heic")}, nil
		case strings.Contains(rawURL, "/big") && size >= 2048:
			return &remoteimage.Image{Type: "image/jpeg", Data: bytes.Repeat([]byte{1}, channel.ImageMost+1)}, nil
		}
		return &remoteimage.Image{Type: "image/jpeg", Data: []byte("jpeg:" + rawURL + fmt.Sprint(size))}, nil
	}
	return s, func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return append([]map[string]any(nil), sends...)
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), fetched...)
		}
}

// images is what one channels.send carried as pictures: their types and decoded bytes.
func images(t *testing.T, send map[string]any) []channel.Blob {
	t.Helper()
	raw, _ := json.Marshal(send["images"])
	var out []channel.Blob
	if send["images"] != nil {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// An answer's pictures on lines of their own go to a channel that sends pictures as pictures, with its text; the ones
// in code and inline stay as text, and one that could not be fetched (or that the chat app can't show) is named with
// its address (#11986).
func TestChannelAnswerSendsItsPictures(t *testing.T) {
	s, sends, fetched := picturesRig(t, true)
	answer := strings.Join([]string{
		"结论：图在下面。",
		"",
		"![图表](https://pics.example/chart.png)",
		"",
		"![坏的](https://pics.example/missing.png) ![苹果格式](https://pics.example/heic.heic)",
		"",
		"```md",
		"![代码里的](https://pics.example/code.png)",
		"```",
		"行内 ![内联](https://pics.example/inline.png) 留着",
	}, "\n")
	if err := s.channelSend(context.Background(), "examplechat", "run-1", answer, channel.OutAnswer, "m1"); err != nil {
		t.Fatal(err)
	}
	got := sends()
	if len(got) != 1 {
		t.Fatalf("sent %d messages", len(got))
	}
	pics := images(t, got[0])
	if len(pics) != 1 || pics[0].MIME != "image/jpeg" || string(pics[0].Data) != "jpeg:https://pics.example/chart.png2048" {
		t.Fatalf("sent pictures %+v", pics)
	}
	lang := i18n.Lang("zh-Hans")
	text := got[0]["text"].(string)
	for _, want := range []string{
		"结论：图在下面。",
		i18n.T(lang, "assistant-channels_plugin.image_link", i18n.Args{"alt": i18n.Verbatim("坏的"), "url": i18n.Verbatim("https://pics.example/missing.png")}),
		i18n.T(lang, "assistant-channels_plugin.image_link", i18n.Args{"alt": i18n.Verbatim("苹果格式"), "url": i18n.Verbatim("https://pics.example/heic.heic")}),
		"![代码里的](https://pics.example/code.png)",
		"行内 ![内联](https://pics.example/inline.png) 留着",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "chart.png") || strings.Contains(text, "\n\n\n") {
		t.Errorf("text:\n%s", text)
	}
	for _, f := range fetched() {
		if strings.Contains(f, "code.png") || strings.Contains(f, "inline.png") || !strings.HasPrefix(f, "channel:examplechat ") {
			t.Errorf("fetched %q", f)
		}
	}
}

// A channel that does not send pictures gets the answer as it is, and nothing is fetched; nor are notes and questions
// searched for pictures.
func TestChannelAnswerKeepsPicturesAsTextWithoutImagesOut(t *testing.T) {
	answer := "看图\n\n![图表](https://pics.example/chart.png)"
	s, sends, fetched := picturesRig(t, false)
	if err := s.channelSend(context.Background(), "examplechat", "run-1", answer, channel.OutAnswer, ""); err != nil {
		t.Fatal(err)
	}
	s2, sends2, fetched2 := picturesRig(t, true)
	if err := s2.channelSend(context.Background(), "examplechat", "note-1", answer, channel.OutNote, ""); err != nil {
		t.Fatal(err)
	}
	for _, got := range [][]map[string]any{sends(), sends2()} {
		if len(got) != 1 || got[0]["text"] != answer || got[0]["images"] != nil {
			t.Fatalf("sent %v", got)
		}
	}
	if len(fetched())+len(fetched2()) != 0 {
		t.Fatalf("fetched %v %v", fetched(), fetched2())
	}
}

// At most ImagesMost pictures go; the rest are named. A picture whose detail keeps it over the limit is drawn smaller.
// An answer of nothing but pictures is sent with empty text.
func TestChannelAnswerPictureLimits(t *testing.T) {
	s, sends, fetched := picturesRig(t, true)
	var lines []string
	lines = append(lines, "![大图](https://pics.example/big.jpg)")
	for i := range channel.ImagesMost {
		lines = append(lines, fmt.Sprintf("![第%d张](https://pics.example/%d.png)", i, i))
	}
	if err := s.channelSend(context.Background(), "examplechat", "run-1", strings.Join(lines, "\n"), channel.OutPush, ""); err != nil {
		t.Fatal(err)
	}
	got := sends()
	pics := images(t, got[0])
	if len(got) != 1 || len(pics) != channel.ImagesMost {
		t.Fatalf("sent %d messages, %d pictures", len(got), len(pics))
	}
	if string(pics[0].Data) != "jpeg:https://pics.example/big.jpg1024" {
		t.Fatalf("the big one: %q", pics[0].Data[:min(40, len(pics[0].Data))])
	}
	last := fmt.Sprintf("https://pics.example/%d.png", channel.ImagesMost-1)
	if text := got[0]["text"].(string); !strings.Contains(text, last) || strings.Contains(text, "big.jpg") {
		t.Fatalf("text %q", text)
	}
	if f := fetched(); len(f) != channel.ImagesMost+1 { // the big one twice, three more; the last never
		t.Fatalf("fetched %v", f)
	}

	s, sends, _ = picturesRig(t, true)
	if err := s.channelSend(context.Background(), "examplechat", "run-2", "![只有图](https://pics.example/only.png)", channel.OutAnswer, ""); err != nil {
		t.Fatal(err)
	}
	if got := sends(); len(got) != 1 || got[0]["text"] != "" || len(images(t, got[0])) != 1 {
		t.Fatalf("sent %v", got)
	}
}

// blockImages takes a line of nothing but pictures, and no other.
func TestBlockImages(t *testing.T) {
	for _, c := range []struct {
		line string
		want []string
	}{
		{"![a](https://x.example/a.png)", []string{"https://x.example/a.png"}},
		{`![a](https://x.example/a.png "title")  ![](<https://x.example/b.png>)`, []string{"https://x.example/a.png", "https://x.example/b.png"}},
		{"see ![a](https://x.example/a.png)", nil},
		{"![a](https://x.example/a.png) and more", nil},
		{"[a](https://x.example/a.png)", nil},
		{"", nil},
	} {
		pics, ok := blockImages(c.line)
		var got []string
		for _, p := range pics {
			got = append(got, p.address)
		}
		if ok != (c.want != nil) || strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%q: %v %v", c.line, got, ok)
		}
	}
}

// An address that isn't http(s) is not fetched.
func TestChannelImageOnlyFetchesWebAddresses(t *testing.T) {
	s, _, fetched := picturesRig(t, true)
	for _, a := range []string{"/Users/me/chart.png", "file:///etc/passwd", "data:image/png;base64,AAAA"} {
		if _, ok := s.channelImage(context.Background(), "examplechat", a); ok {
			t.Errorf("%q was sent", a)
		}
	}
	if f := fetched(); len(f) != 0 {
		t.Fatalf("fetched %v", f)
	}
}

// When no picture of an answer can be sent, each is still named by its address in its place: the Markdown image
// would show as code on a plain channel. With no fetcher at all it is the same (#11986, review of #12158).
func TestChannelAnswerNamesEveryPictureWhenNoneIsSent(t *testing.T) {
	answer := "看图\n\n![坏的](https://pics.example/missing.png)\n\n完"
	lang := i18n.Lang("zh-Hans")
	link := i18n.T(lang, "assistant-channels_plugin.image_link", i18n.Args{"alt": i18n.Verbatim("坏的"), "url": i18n.Verbatim("https://pics.example/missing.png")})
	s, sends, _ := picturesRig(t, true)
	s2, sends2, _ := picturesRig(t, true)
	s2.channelPicture, s2.remoteImages = nil, nil
	for i, svc := range []*Service{s, s2} {
		if err := svc.channelSend(context.Background(), "examplechat", "run-1", answer, channel.OutAnswer, ""); err != nil {
			t.Fatal(err)
		}
		got := [][]map[string]any{sends(), sends2()}[i]
		if len(got) != 1 || got[0]["images"] != nil || got[0]["text"] != "看图\n\n"+link+"\n\n完" {
			t.Fatalf("rig %d sent %v", i, got)
		}
	}
}

// Pictures in an indented code block (four spaces or a tab) stay text and are never fetched; a picture indented up to
// three spaces is still a block image (#11986, review of #12158).
func TestChannelAnswerLeavesIndentedCodeAlone(t *testing.T) {
	s, sends, fetched := picturesRig(t, true)
	answer := strings.Join([]string{
		"这样写：",
		"",
		"    ![示意](https://pics.example/code.png)",
		"",
		"\t![制表符](https://pics.example/tab.png)",
		"",
		"   ![图](https://pics.example/chart.png)",
	}, "\n")
	if err := s.channelSend(context.Background(), "examplechat", "run-1", answer, channel.OutAnswer, ""); err != nil {
		t.Fatal(err)
	}
	got := sends()
	if len(got) != 1 || len(images(t, got[0])) != 1 {
		t.Fatalf("sent %v", got)
	}
	text := got[0]["text"].(string)
	for _, want := range []string{"    ![示意](https://pics.example/code.png)", "\t![制表符](https://pics.example/tab.png)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "chart.png") {
		t.Errorf("text:\n%s", text)
	}
	if f := fetched(); len(f) != 1 || !strings.Contains(f[0], "chart.png") {
		t.Errorf("fetched %v", f)
	}
}

// TestChannelAnswerSendsPicturesUnderListItems: a picture indented under a list item, after a blank line, is a
// paragraph of that item, not code (CommonMark: an item's code needs four columns past its content), so it is sent;
// four columns past the item's content it is code and stays text.
func TestChannelAnswerSendsPicturesUnderListItems(t *testing.T) {
	s, sends, fetched := picturesRig(t, true)
	answer := strings.Join([]string{
		"Steps:",
		"",
		"10. Open the page",
		"",
		"    ![shot](https://pics.example/list.png)",
		"",
		"- Then the menu",
		"",
		"  ![menu](https://pics.example/bullet.png)",
		"",
		"      ![code](https://pics.example/item-code.png)",
		"",
		"  1. A nested step",
		"",
		"     ![nested](https://pics.example/nested.png)",
		"",
		"  ```",
		"  ![fenced](https://pics.example/item-fence.png)",
		"  ```",
		"",
		"Done.",
		"",
		"    ![after](https://pics.example/after.png)",
	}, "\n")
	if err := s.channelSend(context.Background(), "examplechat", "run-1", answer, channel.OutAnswer, ""); err != nil {
		t.Fatal(err)
	}
	got := sends()
	if len(got) != 1 || len(images(t, got[0])) != 3 {
		t.Fatalf("sent %v", got)
	}
	text := got[0]["text"].(string)
	for _, want := range []string{"      ![code](https://pics.example/item-code.png)", "  ![fenced](https://pics.example/item-fence.png)",
		"    ![after](https://pics.example/after.png)"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lost %q:\n%s", want, text)
		}
	}
	for _, gone := range []string{"list.png", "bullet.png", "nested.png"} {
		if strings.Contains(text, gone) {
			t.Errorf("text still has %s:\n%s", gone, text)
		}
	}
	if f := fetched(); len(f) != 3 {
		t.Errorf("fetched %v", f)
	}
}

func TestListItemContentColumn(t *testing.T) {
	for _, c := range []struct {
		line    string
		content int
		ok      bool
	}{
		{"- a", 2, true}, {"* a", 2, true}, {"+ a", 2, true}, {"1. a", 3, true}, {"10) a", 4, true},
		{"  - a", 4, true}, {"-   a", 4, true}, {"-      a", 2, true}, {"-", 2, true}, {"1.", 3, true},
		{"-a", 0, false}, {"1.a", 0, false}, {"1234567890. a", 0, false}, {"text", 0, false},
		{"- - -", 0, false}, {"***", 0, false}, {"![x](https://pics.example/x.png)", 0, false},
	} {
		content, ok := listItem(c.line)
		if ok != c.ok || (ok && content != c.content) {
			t.Errorf("listItem(%q) = %d, %v; want %d, %v", c.line, content, ok, c.content, c.ok)
		}
	}
}

// TestChannelAnswerFenceNeedsLessThanFourColumns: three backticks four columns in, continuing a paragraph, are text of
// that paragraph (CommonMark), not a fence, so the picture after them is still sent.
func TestChannelAnswerFenceNeedsLessThanFourColumns(t *testing.T) {
	s, sends, _ := picturesRig(t, true)
	answer := "Run this:\n    ```\n\n![shot](https://pics.example/after-fence.png)"
	if err := s.channelSend(context.Background(), "examplechat", "run-1", answer, channel.OutAnswer, ""); err != nil {
		t.Fatal(err)
	}
	got := sends()
	if len(got) != 1 || len(images(t, got[0])) != 1 {
		t.Fatalf("sent %v", got)
	}
	if text := got[0]["text"].(string); strings.Contains(text, "after-fence.png") {
		t.Errorf("text:\n%s", text)
	}
}
