package iphone

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/iphonectl"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// bridgePhone is an iphoneHost with one paired iPhone whose bridge answers the wire as the device bridge does: a
// screen with a search field and a Pay button, screenshots written to out_file (or inline), inputs taken.
type bridgePhone struct {
	fakeIPhones
	inline bool             // answer screenshots inline rather than in out_file
	fail   map[string]error // the session's error for an op
	tree   string           // ax_tree's answer ("" bridgePhoneTree)
	apps   string           // list_apps's answer ("" bridgePhoneApps)
	mu     sync.Mutex
	ops    []string
}

const bridgePhoneApps = `{"apps":[{"bundle_id":"com.example.shop","name":"Shop","type":"user"},
  {"bundle_id":"com.example.persona-build","name":"Persona","type":"user"},
  {"bundle_id":"com.example.notes","name":"Persona Notes","type":"user"}]}`

const bridgePhoneTree = `{
  "app": {"bundle_id": "com.example.shop", "name": "Shop"},
  "point_width": 390, "point_height": 844,
  "root": {"role": "application", "label": "Shop", "frame": [0, 0, 390, 844], "children": [
    {"role": "searchField", "label": "Search", "frame": [16, 100, 358, 36], "enabled": true},
    {"role": "button", "label": "Pay", "frame": [16, 700, 358, 50], "enabled": true}
  ]}
}`

const bridgePhoneInfo = `{"name": "Phone", "product_type": "iPhone16,2", "os_version": "18.1", "point_width": 390,
  "point_height": 844, "orientation": "portrait", "locked": false,
  "capabilities": {"screenshot": true, "ax_tree": true, "input": true, "apps": true}}`

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func newBridgePhone() *bridgePhone {
	return &bridgePhone{fakeIPhones: fakeIPhones{installed: true, links: []iphoneLink{{ID: "d1", Paired: true, State: "connected"}}}}
}

func (b *bridgePhone) Call(_ context.Context, device, op string, args json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.ops = append(b.ops, op)
	b.mu.Unlock()
	if device != "d1" {
		return nil, &rpc.Error{Code: "not_paired", Message: "no record"}
	}
	if err := b.fail[op]; err != nil {
		return nil, err
	}
	switch op {
	case "device_info":
		return json.RawMessage(bridgePhoneInfo), nil
	case "ax_tree":
		return json.RawMessage(cmp.Or(b.tree, bridgePhoneTree)), nil
	case "list_apps":
		return json.RawMessage(cmp.Or(b.apps, bridgePhoneApps)), nil
	case "screenshot":
		png, _ := base64.StdEncoding.DecodeString(onePixelPNG)
		var a struct {
			OutFile string `json:"out_file"`
		}
		_ = json.Unmarshal(args, &a)
		if b.inline || a.OutFile == "" {
			out, _ := json.Marshal(map[string]any{"mime": "image/png", "data": png, "width": 1, "height": 1})
			return out, nil
		}
		if err := os.WriteFile(a.OutFile, png, 0o600); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"mime":"image/png","width":1,"height":1}`), nil
	}
	return json.RawMessage(`{}`), nil
}

// count is how many times op was sent.
func (b *bridgePhone) count(op string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, o := range b.ops {
		if o == op {
			n++
		}
	}
	return n
}

// inputs are the operations that change the phone, in order.
func (b *bridgePhone) inputs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, op := range b.ops {
		if !iphoneReadOnly[op] {
			out = append(out, op)
		}
	}
	return out
}

func useCore(t *testing.T, b *bridgePhone) {
	t.Helper()
	useIPhones(t, &b.fakeIPhones, nil)
	oldHost, oldCore := iphones, iphoneCore
	iphones, iphoneCore = b, &iphoneControl{}
	t.Cleanup(func() { iphones, iphoneCore = oldHost, oldCore })
}

func TestIPhoneCoreLooksThroughTheBridge(t *testing.T) {
	for _, inline := range []bool{false, true} {
		b := newBridgePhone()
		b.inline = inline
		useCore(t, b)
		res, err := Call(context.Background(), json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":true}}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(res.Text, "Pay") || !strings.Contains(res.Text, "Search") || !json.Valid(res.Output) {
			t.Fatalf("inline %v: look %q %s", inline, res.Text, res.Output)
		}
		if res.Image == nil || res.Image.Data == "" || res.Image.Width != 1 {
			t.Fatalf("inline %v: screenshot %+v", inline, res.Image)
		}
		if len(b.inputs()) != 0 {
			t.Fatalf("a look sent input: %v", b.inputs())
		}
	}
}

func TestIPhoneCoreAsksBeforeAPaymentAndResumesWithTheApproval(t *testing.T) {
	b := newBridgePhone()
	useCore(t, b)
	ctx := context.Background()
	if _, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
		t.Fatal(err)
	}
	do := `{"session":"s1","op":"do","args":{"steps":[{"action":"tap","target":{"label":"Pay"}}]}}`
	res, err := Call(ctx, json.RawMessage(do))
	if err != nil {
		t.Fatal(err)
	}
	if !res.NeedsApproval || res.ApprovalID == "" {
		t.Fatalf("a payment goes ahead: %+v", res)
	}
	if a := res.Approval; a == nil || a.Step != 1 || a.Action != "tap" || !slices.Contains(a.Needs, "payment") ||
		a.App.BundleID != "com.example.shop" || a.Target == nil || a.Target.Label != "Pay" {
		t.Fatalf("approval %+v", a)
	}
	if strings.Contains(res.Text, res.ApprovalID) || strings.Contains(string(res.Output), res.ApprovalID) {
		t.Fatal("the approval id reaches the model")
	}
	if len(b.inputs()) != 0 {
		t.Fatalf("sent before approval: %v", b.inputs())
	}

	// The manager comes back once the owner approved, with only the id: the step is sent, once.
	resume := json.RawMessage(`{"device":"d1","session":"s1","op":"do","approved":"` + res.ApprovalID + `"}`)
	res, err = Call(ctx, resume)
	if err != nil {
		t.Fatal(err)
	}
	if res.NeedsApproval || strings.Join(b.inputs(), ",") != "tap" {
		t.Fatalf("after approval: %+v, inputs %v", res, b.inputs())
	}
	// The id is spent: nothing is sent, and the error says so in the core's words.
	_, err = Call(ctx, resume)
	if code, sent := iphoneCallErr(t, err); code != "not_pending" || sent != proto.IPhoneNotSent ||
		!strings.Contains(err.Error(), "not pending") || strings.Join(b.inputs(), ",") != "tap" {
		t.Fatalf("approved twice: %s %s %v, inputs %v", code, sent, err, b.inputs())
	}
	// So are steps the core can't read.
	_, err = Call(ctx, json.RawMessage(`{"device":"d1","session":"s1","op":"do","args":{"steps":[{"action":"fly"}]}}`))
	if code, sent := iphoneCallErr(t, err); code != "bad_args" || sent != proto.IPhoneNotSent {
		t.Fatalf("bad steps: %s %s %v", code, sent, err)
	}
}

func TestIPhoneCoreNeverOperatesPersonasOwnApp(t *testing.T) {
	b := newBridgePhone()
	useCore(t, b)
	ctx := context.Background()
	self := `"self_apps":["com.example.shop"],"self_names":["Shop"]`
	if _, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false},`+self+`}`)); err != nil {
		t.Fatal(err)
	}
	for _, params := range []string{
		`{"session":"s1","op":"do","args":{"steps":[{"action":"tap","target":{"label":"Search"}}]},` + self + `}`,
		// A call without them keeps the rule: the node holds them while it runs.
		`{"session":"s1","op":"do","args":{"steps":[{"action":"tap","target":{"label":"Search"}}]}}`,
		`{"session":"s1","op":"do","args":{"steps":[{"action":"launch","app":"com.example.shop"}]}}`,
	} {
		res, err := Call(ctx, json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		var out iphonectl.DoResult
		if json.Unmarshal(res.Output, &out) != nil || len(out.Steps) != 1 || out.Steps[0].Delivery != iphonectl.DeliveryNotSent ||
			!strings.Contains(out.Steps[0].Detail, "Persona's own app") {
			t.Fatalf("%s:\n%s", params, res.Text)
		}
	}
	if len(b.inputs()) != 0 {
		t.Fatalf("operated Persona's app: %v", b.inputs())
	}
}

// TestIPhoneApprovalMirrorsTheCore: proto.IPhoneApproval mirrors iphonectl.Approval by hand. Every field of the core's,
// filled in, goes through it and back unchanged; a field added on one side only turns this red.
func TestIPhoneApprovalMirrorsTheCore(t *testing.T) {
	core := &iphonectl.Approval{Step: 2, Action: "type", Needs: []iphonectl.Sensitivity{iphonectl.Payment, iphonectl.Confirm},
		App: iphonectl.App{BundleID: "com.example.shop", Name: "Shop"}, Target: &iphonectl.ElementRef{N: 4, Role: "button", Label: "Pay"},
		Text: "hello", TextLen: 5, Key: "return", Button: "home", LaunchApp: "com.example.other", Evidence: []string{`button "Pay"`},
		Steps: []iphonectl.StepSummary{{Action: "tap", Target: &iphonectl.ElementRef{N: 4, Role: "button", Label: "Pay"},
			At: &iphonectl.Point{X: 1.5, Y: 2}, TextLen: 3, Key: "tab", Button: "lock", App: "com.example.other"}},
		At: &iphonectl.Point{X: 10, Y: 20.5}}
	want, _ := json.Marshal(core)
	for _, field := range []string{"step", "action", "needs", "app", "target", "text", "text_len", "key", "button", "launch_app",
		"evidence", "steps", "at"} {
		if !strings.Contains(string(want), `"`+field+`":`) {
			t.Fatalf("the test leaves %s empty: %s", field, want)
		}
	}
	mirrored, err := iphoneApproval(core)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(mirrored)
	var w, g any
	_ = json.Unmarshal(want, &w)
	_ = json.Unmarshal(got, &g)
	if !reflect.DeepEqual(w, g) {
		t.Fatalf("lost on the way:\ncore  %s\nproto %s", want, got)
	}
	// Strictly: no field of the core's is unknown to the proto type.
	dec := json.NewDecoder(strings.NewReader(string(want)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(new(proto.IPhoneApproval)); err != nil {
		t.Fatal(err)
	}
	// And field by field, each type and the one it mirrors: the same JSON names and options, so a field added to
	// either side alone (even one left empty here) shows.
	for _, pair := range [][2]any{{iphonectl.Approval{}, proto.IPhoneApproval{}}, {iphonectl.StepSummary{}, proto.IPhoneStepSummary{}},
		{iphonectl.App{}, proto.IPhoneApp{}}, {iphonectl.ElementRef{}, proto.IPhoneElementRef{}}, {iphonectl.Point{}, proto.IPhonePoint{}}} {
		c, p := jsonTags(reflect.TypeOf(pair[0])), jsonTags(reflect.TypeOf(pair[1]))
		if !slices.Equal(c, p) {
			t.Errorf("%T %v\n%T %v", pair[0], c, pair[1], p)
		}
	}
}

// jsonTags are a struct's fields as JSON writes them: name and options, with whether it is a list or a pointer.
func jsonTags(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		out = append(out, f.Tag.Get("json")+" "+f.Type.Kind().String())
	}
	slices.Sort(out)
	return out
}

func TestIPhoneCoreConfirmAsksBeforeAnyInput(t *testing.T) {
	b := newBridgePhone()
	useCore(t, b)
	ctx := context.Background()
	if _, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
		t.Fatal(err)
	}
	res, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"do","confirm":true,
		"args":{"steps":[{"action":"tap","target":{"label":"Search"}},{"action":"type","text":"hello"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if a := res.Approval; !res.NeedsApproval || a == nil || !slices.Contains(a.Needs, "confirm") || len(a.Steps) != 2 {
		t.Fatalf("confirm: %+v %+v", res, a)
	}
	if len(b.inputs()) != 0 {
		t.Fatalf("sent before approval: %v", b.inputs())
	}
}

func TestIPhoneCoreKeepsWhetherAnInputWasSent(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		err  error
		want iphonectl.Delivery
	}{
		// The session refused before writing anything, with a code the bridge doesn't have.
		{"session refused", &rpc.Error{Code: "not_paired", Message: "no record", Data: json.RawMessage(`{"sent":"no"}`)}, iphonectl.DeliveryNotSent},
		{"bridge refused", &rpc.Error{Code: "locked", Message: "locked"}, iphonectl.DeliveryNotSent},
		// Handed to the bridge, then the link dropped: it may have happened.
		{"uncertain", &rpc.Error{Code: "uncertain", Message: "link lost", Data: json.RawMessage(`{"sent":"unknown"}`)}, iphonectl.DeliveryUnknown},
		{"refusal code but maybe sent", &rpc.Error{Code: "locked", Message: "x", Data: json.RawMessage(`{"sent":"unknown"}`)}, iphonectl.DeliveryUnknown},
		{"no code", errors.New("bridge crashed"), iphonectl.DeliveryUnknown},
	} {
		b := newBridgePhone()
		b.fail = map[string]error{"tap": c.err}
		useCore(t, b)
		if _, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
			t.Fatal(err)
		}
		res, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"do","args":{"steps":[{"action":"tap","target":{"label":"Search"}},{"action":"type","text":"hi"}]}}`))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var out iphonectl.DoResult
		if err := json.Unmarshal(res.Output, &out); err != nil || len(out.Steps) == 0 {
			t.Fatalf("%s: %s (%v)", c.name, res.Output, err)
		}
		if out.Steps[0].Delivery != c.want {
			t.Errorf("%s: tap %s, want %s", c.name, out.Steps[0].Delivery, c.want)
		}
		// Nothing after a failed input goes, and a tap that may have happened is never sent again.
		if got := strings.Join(b.inputs(), ","); got != "tap" {
			t.Errorf("%s: inputs %s", c.name, got)
		}
	}
}

func TestIPhoneWireCallKeepsTheCode(t *testing.T) {
	ctx := context.Background()
	call := iphoneWireCall(func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return nil, &rpc.Error{Code: "busy", Message: "pairing", Data: json.RawMessage(`{"sent":"no"}`)}
	})
	_, err := call(ctx, "tap", nil)
	var e *iphonectl.Error
	if !errors.As(err, &e) || e.Code != "busy" || e.Message != "pairing" || !e.NotSent {
		t.Fatalf("%#v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	call = iphoneWireCall(func(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) { return nil, ctx.Err() })
	if _, err := call(cancelled, "tap", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("the context's error: %v", err)
	}
}

// doSteps runs one do and returns its steps' results.
func doSteps(t *testing.T, params string) []iphonectl.StepResult {
	t.Helper()
	res, err := Call(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", params, err)
	}
	var out iphonectl.DoResult
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("%s: %s", params, res.Output)
	}
	return out.Steps
}

func refusedAsPersonas(s []iphonectl.StepResult) bool {
	return len(s) == 1 && s[0].Delivery == iphonectl.DeliveryNotSent && strings.Contains(s[0].Detail, "Persona's own app")
}

// With no bundle id known (the list unreadable, none in the call), an app named Persona in front is still never
// touched.
func TestIPhoneCoreKeepsOutOfAnAppNamedPersonaWithNoBundleID(t *testing.T) {
	b := newBridgePhone()
	b.tree = strings.Replace(bridgePhoneTree, `{"bundle_id": "com.example.shop", "name": "Shop"}`, `{"bundle_id": "com.example.unknown", "name": "Persona"}`, 1)
	b.fail = map[string]error{"list_apps": &rpc.Error{Code: "unsupported", Message: "no apps"}}
	useCore(t, b)
	if _, err := Call(context.Background(), json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{`{"action":"tap","target":{"label":"Search"}}`, `{"action":"type","text":"x"}`,
		`{"action":"key","key":"return"}`, `{"action":"swipe","direction":"up"}`} {
		if s := doSteps(t, `{"session":"s1","op":"do","args":{"steps":[`+step+`]}}`); !refusedAsPersonas(s) {
			t.Errorf("%s: %+v", step, s)
		}
	}
	if len(b.inputs()) != 0 {
		t.Fatalf("operated it: %v", b.inputs())
	}
}

// The device's own app list gives Persona's bundle id, so launching it is refused too; the list is read once, and
// an app whose name only starts with Persona is not taken for it.
func TestIPhoneCoreLearnsPersonasBundleIDFromTheDevice(t *testing.T) {
	b := newBridgePhone()
	useCore(t, b)
	if _, err := Call(context.Background(), json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
		t.Fatal(err)
	}
	if s := doSteps(t, `{"session":"s1","op":"do","args":{"steps":[{"action":"launch","app":"com.example.persona-build"}]}}`); !refusedAsPersonas(s) {
		t.Fatalf("launch: %+v", s)
	}
	if s := doSteps(t, `{"session":"s1","op":"do","args":{"steps":[{"action":"launch","app":"com.example.notes"}]}}`); len(s) != 1 || s[0].Delivery != iphonectl.DeliverySent {
		t.Fatalf("another app: %+v", s)
	}
	if n := b.count("list_apps"); n != 1 {
		t.Fatalf("list_apps %d times", n)
	}
	if got := strings.Join(b.inputs(), ","); got != "launch_app" {
		t.Fatalf("inputs %s", got)
	}

	// A list that can't be read is tried again a while later, without failing the call.
	b = newBridgePhone()
	b.fail = map[string]error{"list_apps": &rpc.Error{Code: "timeout", Message: "slow"}}
	useCore(t, b)
	for range 2 {
		if _, err := Call(context.Background(), json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
			t.Fatal(err)
		}
	}
	if n := b.count("list_apps"); n != 1 {
		t.Fatalf("retried at once: %d", n)
	}
}

// What the call names (from the manager) only adds: a name the device list matches, and a bundle id.
func TestIPhoneCoreAddsTheCallsSelfApps(t *testing.T) {
	b := newBridgePhone()
	useCore(t, b)
	ctx := context.Background()
	if _, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false},"self_names":["Persona Notes"]}`)); err != nil {
		t.Fatal(err)
	}
	if s := doSteps(t, `{"session":"s1","op":"do","args":{"steps":[{"action":"launch","app":"com.example.notes"}]}}`); !refusedAsPersonas(s) {
		t.Fatalf("named by the call: %+v", s)
	}
	if s := doSteps(t, `{"session":"s1","op":"do","args":{"steps":[{"action":"launch","app":"com.example.persona-build"}]}}`); !refusedAsPersonas(s) {
		t.Fatalf("the default name dropped: %+v", s)
	}
}

// What a type step types goes into no log, no error and no result text of the node: only the action and its length.
func TestIPhoneCoreNeverWritesTheTypedText(t *testing.T) {
	const secret = "Hunter2-SECRET-text"
	var logs strings.Builder
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })
	ctx := context.Background()
	for _, c := range []struct {
		name string
		fail error
		text string
	}{
		{"sent", nil, secret},
		{"lost on the way", &rpc.Error{Code: "uncertain", Message: "keys was sent but its result never came back", Data: json.RawMessage(`{"sent":"unknown"}`)}, secret},
		{"refused by the bridge", &rpc.Error{Code: "locked", Message: "locked"}, secret},
		{"not typable", nil, secret + "é"},
		{"too long", nil, strings.Repeat(secret, 200)},
	} {
		b := newBridgePhone()
		if c.fail != nil {
			b.fail = map[string]error{"keys": c.fail}
		}
		useCore(t, b)
		if _, err := Call(ctx, json.RawMessage(`{"session":"s1","op":"look","args":{"screenshot":false}}`)); err != nil {
			t.Fatal(err)
		}
		params, _ := json.Marshal(map[string]any{"session": "s1", "op": "do", "args": map[string]any{
			"steps": []map[string]any{{"action": "type", "text": c.text, "target": map[string]any{"label": "Search"}}}}})
		res, err := Call(ctx, params)
		seen := logs.String()
		if err != nil {
			seen += err.Error()
		}
		if res != nil {
			seen += res.Text + string(res.Output)
		}
		if strings.Contains(seen, "Hunter2") {
			t.Errorf("%s: the typed text is out: %s", c.name, seen)
		}
	}
}
