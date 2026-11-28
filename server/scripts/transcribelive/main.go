// Command transcribelive checks hub transcription (#3862 phase 5) against a running hub whose voice extension is set
// up with a real GPT-Live-compatible service (docs/operations/testing.md "Hub transcription live check"). It plays the
// phone: it has the hub read a made-up meeting aloud (a third Chinese, a third English, a third mixed in one sentence,
// one line said by "Persona" inside a speaking span), uploads a hub-mode meeting, streams the audio over the audio
// socket in real time, ends it, waits for the final pass and the minutes, and reports what came back:
//
//   - the live lines and the final transcript against the script (character error rate, Latin words kept as said);
//   - latency: each line frame after its sentence ended, the first caption after each sentence began, the final pass
//     and the minutes;
//   - Persona's echo: marked in the hub's transcript, never a live line;
//   - the minutes' script (share of Han letters) against the recording's language;
//   - the upstream's billed seconds (usage), and whether the hub deleted the audio.
//
// Run it from server/:
//
//	cd server
//	go run ./scripts/transcribelive -hub http://127.0.0.1:18660 -token-file <file> -kb <PERSONA_HOME>/data -lang zh-CN -out /tmp/live-zh
//
// Use a test hub: the meeting is made up and marked private, and the recording is deleted at the end (-keep keeps it).
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

//go:embed testdata/mixed_meeting.json
var meetingJSON []byte

type line struct {
	Voice string  `json:"voice"`
	Lang  string  `json:"lang"`
	Text  string  `json:"text"`
	Echo  bool    `json:"echo,omitempty"` // said by Persona: inside a speaking span with this text
	Start float64 `json:"start,omitempty"`
	End   float64 `json:"end,omitempty"`
}

type meeting struct {
	Title    string   `json:"title"`
	Glossary []string `json:"glossary"`
	Lines    []line   `json:"lines"`
}

type client struct {
	base, token string
	http        *http.Client
}

func (c *client) do(method, path string, body any) (int, []byte, http.Header) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		log.Printf("%s %s: %v", method, path, err)
		return 0, nil, nil
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, res.Header
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// to16k resamples mono PCM16 to 16 kHz (linear: enough for speech the service reads back).
func to16k(pcm []byte, rate int) []byte {
	if rate == 16000 || rate <= 0 {
		return pcm
	}
	n := len(pcm) / 2
	in := make([]float64, n)
	for i := range in {
		in[i] = float64(int16(binary.LittleEndian.Uint16(pcm[2*i:])))
	}
	m := int(float64(n) * 16000 / float64(rate))
	out := make([]byte, 2*m)
	for j := 0; j < m; j++ {
		x := float64(j) * float64(rate) / 16000
		i, f := int(x), x-math.Floor(x)
		v := in[min(i, n-1)]
		if i+1 < n {
			v = v*(1-f) + in[i+1]*f
		}
		binary.LittleEndian.PutUint16(out[2*j:], uint16(int16(math.Round(v))))
	}
	return out
}

// trim cuts the silence around a read-aloud line, keeping 0.1 s.
func trim(pcm []byte) []byte {
	n := len(pcm) / 2
	loud := func(i int) bool { v := int16(binary.LittleEndian.Uint16(pcm[2*i:])); return v > 300 || v < -300 }
	a, b := 0, n
	for a < n && !loud(a) {
		a++
	}
	for b > a && !loud(b-1) {
		b--
	}
	return pcm[2*max(0, a-1600) : 2*min(n, b+1600)]
}

func fail(format string, args ...any) {
	fmt.Printf("FAIL "+format+"\n", args...)
	os.Exit(1)
}

func timeoutExit(format string, args ...any) {
	fmt.Printf("TIMEOUT "+format+"\n", args...)
	os.Exit(1)
}

type hubSegmentsFile struct {
	Pass     string        `json:"pass"`
	Segments []hubSentence `json:"segments"`
}

func waitForMeta(kbDir, id string, timeout time.Duration) bool {
	p := filepath.Join(kbDir, "recordings", id, "meta.json")
	if _, err := os.Stat(p); err == nil {
		return true
	}
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for time.Now().Before(deadline) {
		<-t.C
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	_, err := os.Stat(p)
	return err == nil
}

func rescore(dir string) (string, error) {
	sb, err := os.ReadFile(filepath.Join(dir, "script.json"))
	if err != nil {
		return "", fmt.Errorf("read script.json: %w", err)
	}
	script, err := parseScriptJSON(sb)
	if err != nil {
		return "", fmt.Errorf("parse script.json: %w", err)
	}
	hb, err := os.ReadFile(filepath.Join(dir, "hub_segments.json"))
	if err != nil {
		return "", fmt.Errorf("read hub_segments.json: %w", err)
	}
	var hs hubSegmentsFile
	if err := json.Unmarshal(hb, &hs); err != nil {
		return "", fmt.Errorf("parse hub_segments.json: %w", err)
	}
	var live []hubSentence
	fb, err := os.ReadFile(filepath.Join(dir, "frames.json"))
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read frames.json: %w", err)
		}
	} else {
		var all []frame
		if err := json.Unmarshal(fb, &all); err != nil {
			return "", fmt.Errorf("parse frames.json: %w", err)
		}
		live = liveSentencesFromFrames(all)
	}
	timing, labels := scoreAll(script, live, hs.Segments)
	out := map[string]any{
		"timing": timing,
		"labels": labels,
	}
	rb, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		return "", fmt.Errorf("marshal rescore.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rescore.json"), rb, 0o600); err != nil {
		return "", fmt.Errorf("write rescore.json: %w", err)
	}
	return formatResultLine(timing, labels, ""), nil
}

// synthesize has the hub read every line aloud (POST /api/v1/extensions/voice/speak) and lays them out with short
// silences; it reuses <out>/meeting.pcm and <out>/script.json when they are there.
func synthesize(c *client, m *meeting, voices map[string]string, out string) []byte {
	pcmPath, scriptPath := filepath.Join(out, "meeting.pcm"), filepath.Join(out, "script.json")
	if b, err := os.ReadFile(pcmPath); err == nil {
		if s, err := os.ReadFile(scriptPath); err == nil {
			if lines, err := parseScriptJSON(s); err == nil {
				m.Lines = lines
				log.Printf("reusing %s (%.1f s)", pcmPath, float64(len(b))/32000)
				return b
			}
		}
	}
	var buf bytes.Buffer
	silence := func(sec float64) { buf.Write(make([]byte, 2*int(sec*16000))) }
	silence(1)
	gaps := []float64{0.7, 1.1, 0.6, 0.9, 1.2, 0.8}
	for i := range m.Lines {
		l := &m.Lines[i]
		lang := "zh-CN"
		if l.Lang == "en" {
			lang = "en-US"
		}
		body := map[string]any{"text": l.Text, "lang": lang}
		if v := voices[l.Voice]; v != "" {
			body["voice"] = v
		}
		var pcm []byte
		for try := 0; try < 3 && pcm == nil; try++ {
			st, b, h := c.do("POST", "/api/v1/extensions/voice/speak", body)
			if st != http.StatusOK || len(b) < 16000 {
				log.Printf("speak line %d: %d %s", i, st, string(b[:min(len(b), 200)]))
				continue
			}
			rate := 16000
			fmt.Sscan(h.Get("X-Persona-Audio-Rate"), &rate)
			pcm = trim(to16k(b, rate))
		}
		if pcm == nil {
			fail("could not have line %d read aloud", i)
		}
		l.Start = float64(buf.Len()) / 32000
		buf.Write(pcm)
		l.End = float64(buf.Len()) / 32000
		silence(gaps[i%len(gaps)])
		log.Printf("line %2d %5.1f-%5.1f %s", i, l.Start, l.End, l.Text)
	}
	silence(2)
	os.WriteFile(pcmPath, buf.Bytes(), 0o600)
	s, _ := json.MarshalIndent(m.Lines, "", " ")
	os.WriteFile(scriptPath, s, 0o600)
	return buf.Bytes()
}

type frame struct {
	At    float64         `json:"recv"` // seconds after recording second 0 was sent
	Frame json.RawMessage `json:"frame"`
	typ   string
}

func main() {
	hub := flag.String("hub", "", "the hub's address (default: public_url of $PERSONA_HOME/hub.json)")
	tokenFile := flag.String("token-file", "", "a file holding a full device token (default: token of $PERSONA_HOME/hub.json)")
	kbDir := flag.String("kb", "", "the hub's knowledge base directory, to read the meeting's files (default: $PERSONA_HOME/data)")
	out := flag.String("out", "transcribelive-out", "where the audio, the frames and report.json go")
	lang := flag.String("lang", "zh-CN", "the recording's language: the minutes are written in it")
	role := flag.String("role", "listener", "the room's role: listener or participant")
	voiceFlag := flag.String("voices", "", "voices for the script's speakers, F=<id>,M=<id>,E=<id>,P=<id> (default: the model's)")
	price := flag.Float64("price-per-minute", 0, "the upstream's price per billed minute, for the cost line")
	keep := flag.Bool("keep", false, "keep the recording afterwards")
	synthOnly := flag.Bool("synth-only", false, "only make the meeting's audio")
	rescoreDir := flag.String("rescore", "", "recompute timing and label scores from <dir>/{script,frames,hub_segments}.json with no hub")
	flag.Parse()
	if *rescoreDir != "" {
		resLine, err := rescore(*rescoreDir)
		if err != nil {
			fail("rescore: %v", err)
		}
		fmt.Println(resLine)
		return
	}
	home := os.Getenv("PERSONA_HOME")
	if home == "" {
		home = filepath.Join(os.Getenv("HOME"), ".persona")
	}
	var hj struct {
		PublicURL string `json:"public_url"`
		Token     string `json:"token"`
	}
	if b, err := os.ReadFile(filepath.Join(home, "hub.json")); err == nil {
		json.Unmarshal(b, &hj)
	}
	c := &client{base: strings.TrimRight(*hub, "/"), token: hj.Token, http: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Minute}}
	if c.base == "" {
		c.base = strings.TrimRight(hj.PublicURL, "/")
	}
	if *tokenFile != "" {
		b, err := os.ReadFile(*tokenFile)
		if err != nil {
			fail("%v", err)
		}
		c.token = strings.TrimSpace(string(b))
	}
	if *kbDir == "" {
		*kbDir = filepath.Join(home, "data")
	}
	if c.base == "" || c.token == "" {
		fail("no hub address or token: pass -hub and -token-file")
	}
	voices := map[string]string{}
	for _, kv := range strings.Split(*voiceFlag, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			voices[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	var m meeting
	if err := json.Unmarshal(meetingJSON, &m); err != nil {
		fail("%v", err)
	}
	os.MkdirAll(*out, 0o700)
	pcm := synthesize(c, &m, voices, *out)
	if *synthOnly {
		fmt.Printf("RESULT synth-only: %d lines (%.1f s)\n", len(m.Lines), float64(len(pcm))/32000)
		return
	}
	report, resLine := run(c, &m, pcm, *lang, *role, *kbDir, *out, *keep)
	if u, ok := report["usage_seconds"].(float64); ok && *price > 0 {
		report["cost"] = u / 60 * *price
	}
	rb, err := json.MarshalIndent(report, "", " ")
	if err != nil {
		fail("marshal report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(*out, "report.json"), rb, 0o600); err != nil {
		fail("write report.json: %v", err)
	}
	fmt.Println(string(rb))
	fmt.Println(resLine)
}

// uploadBody is a hub-mode meeting upload as the phone sends it (docs/assistant/meeting-minutes.md "Choosing the
// mode", "The timeline"): no segments of its own, the room's role, marked private.
func uploadBody(m *meeting, lang, role string, started time.Time, rev int64, timeline map[string]any) map[string]any {
	return map[string]any{"kind": "meeting", "title": m.Title, "language": lang, "started_at": started.Format(time.RFC3339),
		"device": "phone", "transcription": "hub", "room": map[string]any{"attendance": role}, "private": true,
		"segments": []any{}, "revision": rev, "timeline": timeline}
}

func run(c *client, m *meeting, pcm []byte, lang, role, kbDir, out string, keep bool) (map[string]any, string) {
	dur := float64(len(pcm)) / 32000
	report := map[string]any{"language": lang, "audio_seconds": dur}
	st, b, _ := c.do("GET", "/api/v1/assistant", nil)
	var as map[string]any
	json.Unmarshal(b, &as)
	ext, _ := as["meeting_transcription_extension"].(string)
	report["meeting_transcription_extension"] = ext
	log.Printf("assistant %d: default %v, ready %v, extension %q", st, as["meeting_transcription_default"], as["meeting_transcription_ready"], ext)
	if ext != "" {
		log.Printf("WARNING: meeting_transcription_extension is %q (measuring that extension instead of the built-in adapter)", ext)
	}
	if as["meeting_transcription_ready"] != true {
		fail("hub transcription is not ready (meeting_transcription_ready)")
	}
	c.do("PUT", "/api/v1/assistant", map[string]any{"meeting_glossary": m.Glossary})

	id := newID()
	report["recording_id"] = id
	deleteRec := func() {
		st, b, _ := c.do("DELETE", "/api/v1/recordings/"+id, nil)
		log.Printf("delete: %d %s", st, string(b))
	}
	started := time.Now().UTC()
	var mu sync.Mutex // the timeline and the upload revision
	spans := []map[string]any{}
	timeline := func() map[string]any {
		return map[string]any{"roles": []any{map[string]any{"at": 0.0, "attendance": role}}, "speaking": append([]map[string]any(nil), spans...)}
	}
	rev := int64(0)
	upload := func(extra map[string]any) map[string]any {
		mu.Lock()
		rev++
		body := uploadBody(m, lang, role, started, rev, timeline())
		mu.Unlock()
		for k, v := range extra {
			body[k] = v
		}
		st, b, _ := c.do("PUT", "/api/v1/recordings/"+id, body)
		var r map[string]any
		json.Unmarshal(b, &r)
		if st != http.StatusOK {
			log.Printf("upload: %d %s", st, string(b))
		}
		return r
	}
	first := upload(nil)
	report["upload_200"] = map[string]any{"transcription": first["transcription"], "transcription_problem": first["transcription_problem"], "room": first["room"]}
	if first["transcription"] != "hub" {
		fail("the hub did not take hub mode: %v", first)
	}
	if !waitForMeta(kbDir, id, time.Minute) {
		deleteRec()
		fail("no meta.json for the recording within 60 s: check -kb")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	ws, res, err := websocket.Dial(ctx, strings.Replace(c.base, "http", "ws", 1)+"/api/v1/recordings/"+id+"/audio",
		&websocket.DialOptions{HTTPHeader: h, HTTPClient: c.http})
	if err != nil {
		code := 0
		if res != nil {
			code = res.StatusCode
		}
		fail("audio socket: %v (%d)", err, code)
	}
	ws.SetReadLimit(1 << 20)
	send := func(v any) { b, _ := json.Marshal(v); ws.Write(ctx, websocket.MessageText, b) }
	send(map[string]any{"type": "start", "at": 0.0, "timeline": timeline()})
	send(map[string]any{"type": "role", "at": 0.0, "attendance": role})
	var (
		fmu    sync.Mutex
		frames []frame
		t0     time.Time
		ready  = make(chan map[string]any, 1)
		closed = make(chan struct{})
	)
	go func() {
		defer close(closed)
		for {
			typ, b, err := ws.Read(ctx)
			if err != nil {
				log.Printf("socket: %v", err)
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var f map[string]any
			json.Unmarshal(b, &f)
			fmu.Lock()
			at := 0.0
			if !t0.IsZero() {
				at = time.Since(t0).Seconds()
			}
			frames = append(frames, frame{At: at, Frame: b, typ: fmt.Sprint(f["type"])})
			fmu.Unlock()
			switch f["type"] {
			case "ready":
				ready <- f
			case "line":
				end, _ := f["end"].(float64)
				log.Printf("line %v [%.1f-%.1f] +%.1fs %v", f["n"], f["start"], end, at-end, f["text"])
			case "problem", "error":
				log.Printf("%s", b)
			}
		}
	}()
	select {
	case r := <-ready:
		report["ready_frame"] = r
	case <-time.After(30 * time.Second):
		fail("no ready frame in 30 s")
	}
	// Real time, as the phone sends it: 100 ms frames on a monotonic clock, an upload every 15 s with the timeline.
	t0 = time.Now()
	const frameBytes = 3200
	nextUpload := 15.0
	for off, k := 0, 0; off < len(pcm); off, k = off+frameBytes, k+1 {
		sec := float64(off) / 32000
		for _, l := range m.Lines {
			if !l.Echo {
				continue
			}
			from, to := l.Start-0.05, l.End+0.05
			if sec <= from && from < sec+0.1 {
				send(map[string]any{"type": "speaking", "on": true, "at": from, "text": l.Text})
			}
			if sec <= to && to < sec+0.1 {
				send(map[string]any{"type": "speaking", "on": false, "at": to})
				mu.Lock()
				spans = append(spans, map[string]any{"start": from, "end": to, "text": l.Text})
				mu.Unlock()
			}
		}
		if err := ws.Write(ctx, websocket.MessageBinary, pcm[off:min(off+frameBytes, len(pcm))]); err != nil {
			fail("audio: %v", err)
		}
		if sec >= nextUpload {
			nextUpload += 15
			go upload(map[string]any{"duration": sec})
		}
		time.Sleep(time.Until(t0.Add(time.Duration(k+1) * 100 * time.Millisecond)))
	}
	send(map[string]any{"type": "end", "at": dur})
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		log.Print("the socket is still open 30 s after end")
	}
	completeAt := time.Now()
	upload(map[string]any{"duration": dur, "complete": true, "ended_at": started.Add(time.Duration(dur * float64(time.Second))).Format(time.RFC3339)})
	log.Print("complete; waiting for the final pass")

	var tr map[string]any
	var finalAt time.Time
	for deadline := time.Now().Add(40 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		_, b, _ := c.do("GET", "/api/v1/recordings/"+id+"/live?parts=transcription", nil)
		var l map[string]any
		json.Unmarshal(b, &l)
		if t, _ := l["transcription"].(map[string]any); t != nil && (t["state"] == "final" || t["state"] == "failed") {
			tr, finalAt = t, time.Now()
			break
		}
	}
	if tr == nil {
		if !keep {
			deleteRec()
		}
		timeoutExit("final pass did not finish within 40 minutes")
	}
	report["transcription"] = tr
	report["final_pass_seconds"] = finalAt.Sub(completeAt).Seconds()
	log.Printf("transcription %v after %.0f s", tr, finalAt.Sub(completeAt).Seconds())
	if tr["state"] == "failed" {
		if !keep {
			deleteRec()
		}
		fail("final pass failed: %v", tr)
	}

	dir := filepath.Join(kbDir, "recordings", id)
	var meta map[string]any
	var minutesAt time.Time
	for deadline := time.Now().Add(30 * time.Minute); time.Now().Before(deadline); time.Sleep(5 * time.Second) {
		if b, err := os.ReadFile(filepath.Join(dir, "meta.json")); err == nil {
			json.Unmarshal(b, &meta)
			if s, _ := meta["minutes_state"].(string); s == "ready" || s == "failed" {
				minutesAt = time.Now()
				break
			}
		}
	}
	minutesState, _ := meta["minutes_state"].(string)
	report["minutes_state"] = meta["minutes_state"]
	if !minutesAt.IsZero() {
		report["minutes_after_final_seconds"] = minutesAt.Sub(finalAt).Seconds()
	} else {
		report["minutes_after_final_seconds"] = nil
	}
	minutes, _ := os.ReadFile(filepath.Join(dir, "minutes.md"))
	report["minutes_md"] = string(minutes)
	report["minutes_han_share"] = hanShare(string(minutes))
	var hs hubSegmentsFile
	if b, err := os.ReadFile(filepath.Join(dir, "hub_segments.json")); err == nil {
		os.WriteFile(filepath.Join(out, "hub_segments.json"), b, 0o600)
		json.Unmarshal(b, &hs)
	}
	var mj struct {
		Transcription map[string]any `json:"transcription"`
	}
	if b, err := os.ReadFile(filepath.Join(dir, "meeting.json")); err == nil {
		json.Unmarshal(b, &mj)
	}
	report["usage_seconds"] = mj.Transcription["usage_seconds"]
	time.Sleep(5 * time.Second) // the deletion follows the minutes' write
	_, err = os.Stat(filepath.Join(dir, "attachments", "audio"))
	report["audio_deleted"] = os.IsNotExist(err)
	_, detail, _ := c.do("GET", "/api/v1/kb/recordings/"+id, nil)
	os.WriteFile(filepath.Join(out, "detail.json"), detail, 0o600)

	var refs []string
	var ref strings.Builder
	for _, l := range m.Lines {
		if !l.Echo {
			refs = append(refs, l.Text)
			ref.WriteString(l.Text)
		}
	}
	var final strings.Builder
	var echoMarked []string
	for _, s := range hs.Segments {
		if s.Echo {
			echoMarked = append(echoMarked, s.Text)
			continue
		}
		final.WriteString(s.Text)
	}
	fmu.Lock()
	all := frames
	fmu.Unlock()
	var live strings.Builder
	var lineLat, captionLat []float64
	var lines []map[string]any
	for _, f := range all {
		if f.typ != "line" {
			continue
		}
		var l map[string]any
		json.Unmarshal(f.Frame, &l)
		live.WriteString(fmt.Sprint(l["text"]))
		if end, ok := l["end"].(float64); ok {
			lineLat = append(lineLat, f.At-end)
		}
		lines = append(lines, l)
	}
	for _, l := range m.Lines {
		best := math.Inf(1)
		for _, f := range all {
			if f.typ != "interim" {
				continue
			}
			var i map[string]any
			json.Unmarshal(f.Frame, &i)
			s, _ := i["start"].(float64)
			if strings.TrimSpace(fmt.Sprint(i["text"])) != "" && s >= l.Start-1.5 && s <= l.End && f.At >= l.Start {
				best = math.Min(best, f.At-l.Start)
			}
		}
		if !math.IsInf(best, 1) {
			captionLat = append(captionLat, best)
		}
	}
	fb, _ := json.MarshalIndent(all, "", " ")
	os.WriteFile(filepath.Join(out, "frames.json"), fb, 0o600)
	echoKey := ""
	head := func(s string) string { r := []rune(s); return string(r[:min(len(r), 8)]) }
	perLine := []map[string]any{}
	for _, l := range m.Lines {
		if l.Echo {
			echoKey = normalize(l.Text)
		}
		best, said := 9.0, ""
		for _, s := range hs.Segments {
			if s.End > l.Start-1 && s.Start < l.End+1 {
				if d := cer(l.Text, s.Text); d < best {
					best, said = d, s.Text
				}
			}
		}
		perLine = append(perLine, map[string]any{"lang": l.Lang, "echo": l.Echo, "script": l.Text, "hub": said, "cer": best})
	}
	liveSents := liveSentencesFromFrames(all)
	timing, labels := scoreAll(m.Lines, liveSents, hs.Segments)

	report["per_line"] = perLine
	report["live_lines"] = lines
	report["final_pass"] = hs.Pass
	report["cer_live"] = cer(ref.String(), live.String())
	report["cer_final"] = cer(ref.String(), final.String())
	report["latin_words_kept_final"] = latinRecall(refs, final.String())
	report["latin_words_kept_live"] = latinRecall(refs, live.String())
	report["echo_marked"] = echoMarked
	report["echo_unmarked_in_final"] = echoKey != "" && strings.Contains(normalize(final.String()), head(echoKey))
	report["echo_in_live"] = echoKey != "" && strings.Contains(normalize(live.String()), head(echoKey))
	report["line_latency"] = summary(lineLat)
	report["caption_latency_from_sentence_start"] = summary(captionLat)
	report["timing"] = timing
	report["labels"] = labels
	if !keep {
		deleteRec()
	}
	return report, formatResultLine(timing, labels, minutesState)
}
