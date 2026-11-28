package proto

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/screen"
)

type answerFixture struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Source       []string `json:"source"`
	FromTest     string   `json:"from_test"`
	Derived      bool     `json:"derived"`
	DesignChange bool     `json:"design_change"`
	Input        struct {
		Profile     string          `json:"profile"`
		Interaction json.RawMessage `json:"interaction"`
		Entry       string          `json:"entry"`
		Kind        string          `json:"kind"`
		Locked      bool            `json:"locked"`
	} `json:"input"`
	Expected struct {
		Allowed    bool            `json:"allowed"`
		Resolution json.RawMessage `json:"resolution"`
		Actions    []struct {
			ID             string `json:"id"`
			Mode           string `json:"mode"`
			Authentication string `json:"authentication"`
			RequiresUnlock *bool  `json:"requires_unlock"`
			AvailableNow   *bool  `json:"available_now"`
		} `json:"actions"`
	} `json:"expected"`
}

func answerFixtureRoot(t *testing.T) string {
	t.Helper()
	// go test runs in the package directory; runtime.Caller is not usable under -trimpath.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "../../.."))
}

func answerFixtures(t *testing.T, domain string) []answerFixture {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(answerFixtureRoot(t), "spec/fixtures", domain, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("fixture files: %v", err)
	}
	var all []answerFixture
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			SchemaVersion int             `json:"schema_version"`
			Cases         []answerFixture `json:"cases"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if doc.SchemaVersion != 1 || len(doc.Cases) == 0 {
			t.Fatalf("invalid fixture document: %s", file)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, bytes.TrimSpace(data), "", "  "); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(append(pretty.Bytes(), '\n'), data) {
			t.Fatalf("expected two-space JSON: %s", file)
		}
		all = append(all, doc.Cases...)
	}
	return all
}

func TestResolutionFixtures(t *testing.T) {
	names := map[string]bool{}
	policies := map[string]int{}
	for _, domain := range []string{"answer", "question-review", "approval"} {
		for _, c := range answerFixtures(t, domain) {
			t.Run(c.Name, func(t *testing.T) {
				if !regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`).MatchString(c.Name) {
					t.Fatal("name must be snake_case")
				}
				if names[c.Name] {
					t.Fatal("duplicate fixture name")
				}
				names[c.Name] = true
				if c.Description == "" || len(c.Source) == 0 {
					t.Fatal("missing provenance")
				}
				flags := 0
				if c.Derived {
					flags++
				}
				if c.DesignChange {
					flags++
				}
				if c.FromTest != "" {
					flags++
				}
				if flags != 1 {
					t.Fatal("exactly one provenance marker is required")
				}
				if (c.Input.Profile == "unified") != c.DesignChange {
					t.Fatal("design changes must be explicit")
				}
				if domain == "approval" {
					key := c.Input.Profile + "/" + c.Input.Entry + "/" + c.Input.Kind
					if c.Input.Locked {
						key += "/locked"
					} else {
						key += "/unlocked"
					}
					policies[key]++
					if len(c.Expected.Actions) == 0 {
						t.Fatal("missing policy actions")
					}
					ids := map[string]bool{}
					for _, a := range c.Expected.Actions {
						if ids[a.ID] {
							t.Fatal("duplicate action")
						}
						ids[a.ID] = true
						if c.Input.Profile == "unified" && c.Input.Entry != "conversation" && a.Mode != "" && a.Mode != "acceptEdits" {
							t.Fatal("peripheral plan approval must use acceptEdits")
						}
						if !c.Input.Locked && (a.RequiresUnlock == nil || *a.RequiresUnlock) {
							t.Fatal("unlocked entry must not request unlock")
						}
						if a.RequiresUnlock != nil && (a.AvailableNow == nil || *a.RequiresUnlock == *a.AvailableNow) {
							t.Fatal("unlock and immediate availability disagree")
						}
						if a.Authentication == "required" && c.Input.Locked && (a.RequiresUnlock == nil || !*a.RequiresUnlock) {
							t.Fatal("locked authenticated action must require unlock")
						}
					}
					return
				}
				var ix Interaction
				if err := json.Unmarshal(c.Input.Interaction, &ix); err != nil {
					t.Fatal(err)
				}
				if ix.Dialog != nil {
					parsed := screen.ParseDialog(ix.Screen)
					if parsed != nil {
						parsed.Cursor = ""    // where the ❯ is: the hub's own, never on the wire
						parsed.LegacyRev = "" // the rev older hubs and nodes compute (#5961): never on the wire either (#5970)
					}
					if !reflect.DeepEqual(parsed, ix.Dialog) {
						t.Fatalf("screen parser differs from fixture dialog: %+v / %+v", parsed, ix.Dialog)
					}
				}
				if ix.ID == "" || ix.Kind == "" {
					t.Fatal("missing interaction identity")
				}
				raw := c.Expected.Resolution
				if len(raw) == 0 {
					t.Fatal("missing resolution (use null for no request)")
				}
				if bytes.Equal(raw, []byte("null")) {
					return
				}
				if !c.Expected.Allowed {
					t.Fatal("disabled operation must not emit a resolution")
				}
				var r Resolution
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&r); err != nil {
					t.Fatalf("hub Resolution decoder: %v", err)
				}
				encoded, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				var before, after map[string]any
				if err := json.Unmarshal(raw, &before); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(encoded, &after); err != nil {
					t.Fatal(err)
				}
				// Swift encodes non-nil empty strings; Go omits their zero values.
				for k, v := range before {
					if v == "" && (k == "feedback" || k == "message" || k == "text") {
						delete(before, k)
					}
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatalf("wire fields lost: %s -> %s", raw, encoded)
				}
				if r.Dismiss {
					return
				}
				switch ix.Kind {
				case IxQuestion:
					if len(r.Answers) != len(ix.Questions) {
						t.Fatal("every question needs an answer")
					}
					for _, q := range ix.Questions {
						if r.Answers[q.Question] == "" {
							t.Fatal("missing original question key")
						}
					}
				case IxPlan:
					if r.Approve == nil {
						t.Fatal("approve must remain a boolean pointer, including false")
					}
					if *r.Approve && r.Mode != "acceptEdits" && r.Mode != "bypassPermissions" && r.Mode != "default" {
						t.Fatal("explicit supported mode required")
					}
				case IxPermission:
					if r.Allow == nil {
						t.Fatal("allow must remain a boolean pointer, including false")
					}
				case IxScreen:
					if r.Option != "" && (ix.Dialog == nil || ix.Dialog.Option(r.Option) == nil) {
						t.Fatal("option must exist in hub dialog")
					}
					if r.Option != "" && (len(r.Keys) != 0 || len(r.PreKeys) != 0) {
						t.Fatal("hub options must not include legacy keys")
					}
				default:
					t.Fatalf("unknown interaction kind: %s", ix.Kind)
				}
			})
		}
	}
	for _, name := range reviewConfirmedCases {
		if !names[name] {
			t.Errorf("review-confirmed case missing: %s", name)
		}
	}
	for _, profile := range []string{"ios_current", "unified"} {
		for _, entry := range []string{"conversation", "notification", "lock_screen", "watch", "siri_shortcut"} {
			for _, kind := range []string{IxPlan, IxPermission, IxQuestion, IxScreen} {
				for _, lock := range []string{"locked", "unlocked"} {
					if policies[profile+"/"+entry+"/"+kind+"/"+lock] != 1 {
						t.Errorf("missing or duplicate policy: %s/%s/%s/%s", profile, entry, kind, lock)
					}
				}
			}
		}
	}
}

// swiftBlock copies a declaration verbatim. It does not rewrite the iOS implementation.
func swiftBlock(t *testing.T, source, marker string) string {
	t.Helper()
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("Swift declaration missing: %s", marker)
	}
	open := strings.Index(source[start:], "{") + start
	depth := 0
	for i := open; i < len(source); i++ {
		switch source[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[start : i+1]
			}
		}
	}
	t.Fatalf("unterminated Swift declaration: %s", marker)
	return ""
}

// swiftCallEnd returns the index just past the parenthesis closing the first call in s, or -1.
func swiftCallEnd(s string) int {
	depth := 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// The generated Swift executables only live in t.TempDir. Enable this on macOS for source-backed checks.
//
// Two programs compile from the same copied declarations, so one contract drifting from its source does not hide the
// others: "answers" replays every answer fixture except `resolve`, and "transport" adds AppModel.resolve for the
// `resolve` cases.
func TestResolutionFixturesIOS(t *testing.T) {
	if os.Getenv("PERSONA_VERIFY_IOS_FIXTURES") != "1" {
		t.Skip("set PERSONA_VERIFY_IOS_FIXTURES=1 on macOS to run the read-only Swift oracle")
	}
	root := answerFixtureRoot(t)
	read := func(path string) string {
		b, e := os.ReadFile(filepath.Join(root, path))
		if e != nil {
			t.Fatal(e)
		}
		return string(b)
	}
	models := read("apple/Shared/Models.swift")
	views := read("apple/Persona/Views/Interactions/InteractionViews.swift")
	chat := read("apple/Shared/ChatStore.swift")
	notification := read("apple/Persona/App/NotificationActions.swift")
	source := "import Foundation\n" + read("apple/Packages/PersonaWire/Sources/PersonaWire/JSONValue.swift") + "\n"
	for _, marker := range []string{"nonisolated struct SessionKey", "nonisolated struct MachineRef", "nonisolated enum InteractionKind", "nonisolated struct QuestionOption", "nonisolated struct Question:", "@propertyWrapper\nnonisolated struct InteractionOccurrenceDate:", "nonisolated struct Interaction:", "extension Interaction {", "nonisolated struct Delegate:", "nonisolated struct InteractionField:", "nonisolated struct LenientText:", "nonisolated struct Resolution:", "nonisolated struct ScreenDialog:"} {
		source += swiftBlock(t, models, marker) + "\n"
	}
	rules := read("apple/Shared/RuleSuggestion.swift")
	source += swiftBlock(t, rules, "nonisolated struct RuleScope:") + "\n" + swiftBlock(t, rules, "nonisolated struct RuleSuggestion:") + "\n"
	text := read("apple/Shared/InteractionText.swift")
	for _, marker := range []string{"nonisolated enum SecretRun {", "nonisolated enum SecretHandoff {", "nonisolated extension Interaction {"} {
		source += swiftBlock(t, text, marker) + "\n"
	}
	source += swiftBlock(t, read("apple/Shared/Elicitation.swift"), "nonisolated struct ElicitationRequest:") + "\n"
	source += swiftBlock(t, read("apple/Shared/CommandAsk.swift"), "nonisolated struct CommandAsk:") + "\n"
	// Whether a card offers Always (#3590), as the view decides it.
	source += "extension Interaction {\n" + swiftBlock(t, views, "var offersAlways: Bool {") + "\n" + swiftBlock(t, views, "var agentName: String? {") + "\n}\n"
	source += swiftBlock(t, chat, "nonisolated enum ScreenParser") + "\n" + swiftBlock(t, chat, "nonisolated private extension Character") + "\n"
	source += "enum NotificationActions {\n" + notification[strings.Index(notification, "    static let allow"):strings.Index(notification, "    static func category")] + swiftBlock(t, notification, "static func resolution(action:") + "\n}\n"
	source += "enum ApproveFirstIntent {\n" + swiftBlock(t, read("apple/Persona/Intents/ApproveFirstIntent.swift"), "static func resolution(for ix:") + "\n}\n"
	// Several questions get steps and a review before they go (#2970). The review is drawn on iPhone and iPad only
	// (`#if os(iOS)`); this macOS program replays that branch, kept in step with the source below.
	stepsEnabled := swiftBlock(t, read("apple/Persona/Views/Interactions/ScreenStepsLogic.swift"), "static var enabled: Bool {")
	if strings.Join(strings.Fields(stepsEnabled), " ") != "static var enabled: Bool { #if os(iOS) true #else false #endif }" {
		t.Fatalf("ScreenStepsLogic.enabled changed: revisit the review replay\n%s", stepsEnabled)
	}
	source += "enum ScreenStepsLogic { static let enabled = true }\n"
	steps := read("apple/Persona/Views/Interactions/QuestionSteps.swift")
	source += "enum QuestionSteps {\n"
	// A single-select pick on a card of several questions steps a pause later (#6549), through these as written.
	for _, marker := range []string{"static func enabled(", "static var pause: Duration {", "@MainActor static func step(replacing", "static func next(after from:"} {
		source += swiftBlock(t, steps, marker) + "\n"
	}
	source += "}\n"
	source += swiftQuestionSupport + "\n"
	source += "#if TRANSPORT\n" + transportSource(t, read, text) + "#endif\n"
	question := swiftBlock(t, views, "struct QuestionForm:")
	// A struct with SwiftUI's State semantics, as QuestionForm is: its closures read the form's state without self.
	source += "@MainActor struct QuestionProbe {\n" + swiftQuestionFields
	for _, marker := range []string{"private var questions:", "private var stepped:", "private var isSending:", "private var needsFooter:", "private func pick(", "private var isLast:", "private func advance(", "private func answer(", "private var allAnswered:", "private var canAdvance:", "private func submit(", "private func send("} {
		source += swiftBlock(t, question, marker) + "\n"
	}
	// The review footer's own Previous and Submit closures, compiled as written in the form.
	footer := strings.Index(question, "ScreenReviewFooter(")
	if footer < 0 {
		t.Fatal("QuestionForm review footer missing")
	}
	// Only the footer call itself, so a missing label cannot pick up the inline ScreenReview's closure.
	review := question[footer:]
	if end := swiftCallEnd(review); end < 0 {
		t.Fatal("QuestionForm review footer call unterminated")
	} else {
		review = review[:end]
	}
	for _, pair := range [][2]string{{"back: ", "reviewBack"}, {"submit: ", "reviewSubmit"}} {
		at := strings.Index(review, pair[0])
		if at < 0 || !strings.HasPrefix(review[at+len(pair[0]):], "{") {
			t.Fatalf("review footer closure changed: %s", pair[0])
		}
		source += "func " + pair[1] + "() " + swiftBlock(t, review[at:], pair[0]+"{")[len(pair[0]):] + "\n"
	}
	source += swiftQuestionRun + "\n}\n"
	confirmed := make([]string, len(reviewConfirmedCases))
	for i, name := range reviewConfirmedCases {
		confirmed[i] = `"` + name + `"`
	}
	source += "let confirmedThroughReview: Set<String> = [" + strings.Join(confirmed, ", ") + "]\n"
	source += "struct ScreenProbe { var interaction: Interaction\n" + swiftBlock(t, views, "private func answer(_ opt: ScreenParser.Dialog.Option") + "\nfunc run(_ key: String, _ text: String?) -> Resolution { answer(interaction.screenDialog!.options.first { $0.key == key }!, text: text) }\n}\n"
	// Compile the literal button constructors from the actual view, rather than duplicate their modes.
	source += "func button(_ action: String, _ feedback: String) -> Resolution {\nswitch action {\n"
	for _, pair := range [][2]string{{"approve_all", "Resolution(approve: true, mode: \"bypassPermissions\")"}, {"approve_edits", "Resolution(approve: true, mode: \"acceptEdits\")"}, {"reject", "Resolution(approve: false, feedback: feedback)"}, {"allow", "Resolution(allow: true)"}, {"deny", "Resolution(allow: false)"}, {"always", "Resolution(allow: true, always: true)"}, {"dismiss", "Resolution(dismiss: true)"}} {
		src := views
		if pair[0] == "dismiss" {
			src = models
		}
		pos := strings.Index(src, pair[1])
		if pos < 0 {
			t.Fatalf("button constructor changed: %s", pair[0])
		}
		source += "case \"" + pair[0] + "\": return " + src[pos:pos+len(pair[1])] + "\n"
	}
	source += "default: fatalError(action)\n}\n}\n"
	watch := read("apple/PersonaWatch/PersonaWatchApp.swift")
	watchExpr := "Resolution(approve: true, mode: \"acceptEdits\")"
	at := strings.Index(watch, watchExpr)
	if at < 0 {
		t.Fatal("watch plan constructor changed")
	}
	source += "func watchPlan() -> Resolution { " + watch[at:at+len(watchExpr)] + " }\n" + swiftFixtureMain
	run := func(t *testing.T, flags ...string) {
		dir := t.TempDir()
		file := filepath.Join(dir, "main.swift")
		binary := filepath.Join(dir, "verify")
		if err := os.WriteFile(file, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		compile := exec.CommandContext(ctx, "swiftc", append([]string{"-parse-as-library", file, "-o", binary}, flags...)...)
		if out, err := compile.CombinedOutput(); err != nil {
			t.Fatalf("swiftc: %v\n%s", err, out)
		}
		verify := exec.CommandContext(ctx, binary, filepath.Join(root, "spec/fixtures/approval/ios_current.json"), filepath.Join(root, "spec/fixtures/answer"), filepath.Join(root, "spec/fixtures/question-review"))
		if out, err := verify.CombinedOutput(); err != nil {
			t.Fatalf("Swift fixtures: %v\n%s", err, out)
		} else {
			t.Log(string(out))
		}
	}
	t.Run("answers", func(t *testing.T) { run(t) })
	t.Run("transport", func(t *testing.T) { run(t, "-D", "TRANSPORT") })
}

// transportSource is what AppModel.resolve needs, copied from its sources: the answer bookkeeping, the retry through a
// hub restart, the API error and its gap rules, the hub's reply, the receipts, and the app model's own helpers around
// resolve, including what it does when the hub says a card ended. Only the app host is a stand-in (swiftResolveSupport).
func transportSource(t *testing.T, read func(string) string, interactionText string) string {
	t.Helper()
	var b strings.Builder
	add := func(source string, markers ...string) {
		for _, marker := range markers {
			b.WriteString(swiftBlock(t, source, marker) + "\n")
		}
	}
	add(read("apple/Persona/App/AnsweringInteractions.swift"), "nonisolated struct AnsweringInteractions {", "nonisolated struct AnswerAttempts {")
	add(read("apple/Shared/Reconnect.swift"), "nonisolated enum OpRetry {", "nonisolated extension Error {", "nonisolated enum OpReceipt {")
	client := read("apple/Shared/APIClient.swift")
	add(client, "nonisolated struct APIError: LocalizedError")
	b.WriteString("extension Error {\n")
	add(client, "nonisolated var isCancellation: Bool {", "nonisolated var isTimeout: Bool {", "nonisolated var isOffline: Bool {", "nonisolated private static var offlineCodes: Set<Int> {", "nonisolated var isQuiet: Bool {")
	b.WriteString("}\n")
	add(read("apple/Shared/Answered.swift"), "nonisolated struct ResolveResult:", "nonisolated struct AnswerLine:", "nonisolated enum Answered {")
	delegated := read("apple/Shared/Delegated.swift")
	add(delegated, "nonisolated struct AssistantAnswer:", "nonisolated struct DelegatedAnswer:")
	kept := regexp.MustCompile(`static let kept = \d+`).FindString(delegated)
	if kept == "" {
		t.Fatal("Delegated.kept missing")
	}
	b.WriteString("enum Delegated { " + kept + " }\n")
	b.WriteString("extension Interaction {\n")
	add(interactionText, "var isSessionless: Bool {", "func canAnswer(session: Session?) -> Bool {")
	b.WriteString("}\n")
	b.WriteString(swiftResolveSupport + "\n@MainActor final class ResolveProbe {\n" + swiftResolveFields)
	app := read("apple/Persona/App/AppModel.swift")
	add(app, "func canAnswer(_ ix: Interaction) -> Bool {", "func resolve(_ ix:", "private struct InteractionOccurrence {",
		"private func occurrence(", "private func laterOccurrence(", "private func markEndedInteraction(", "private func markEndedOccurrence(",
		"private func hasLaterOccurrence(", "private func putBack(_ ix: Interaction) {", "private func forgetEndedInteraction(", "private func keepDelegated(_ ix: Interaction, _ answer: AssistantAnswer) {")
	// The app's handling of the hub's interaction_resolved event, a case of its event switch, as written there.
	const resolvedCase = "case .interactionResolved(let id, let assistant):"
	start := strings.Index(app, resolvedCase)
	if start < 0 {
		t.Fatal("AppModel interaction_resolved handling missing")
	}
	body := app[start+len(resolvedCase):]
	end := strings.Index(body, "\n        case .")
	if end < 0 {
		t.Fatal("AppModel interaction_resolved handling unterminated")
	}
	b.WriteString("func interactionResolved(_ id: String, assistant: AssistantAnswer?, group: String) {" + body[:end] + "\n}\n}\n")
	return b.String()
}

// reviewConfirmedCases are the questions.json cases whose expected Resolution is what the review's Submit sends: on a
// card of several questions their click opens the review (#2970), and the replay then confirms it (#8483).
// spec/fixtures/question-review/review.json asserts the click itself. Every other question case must leave the review
// closed.
var reviewConfirmedCases = []string{"multiple_questions_send_original_keys", "all_answered_can_submit_from_earlier_page"}

const swiftQuestionSupport = `
// SwiftUI's State: storage outside the view, so a copy of the view and its closures see the same values.
@propertyWrapper struct State<Value> {
 final class Box { var value: Value; init(_ value: Value) { self.value = value } }
 let box: Box
 init(wrappedValue: Value) { box = Box(wrappedValue) }
 var wrappedValue: Value { get { box.value } nonmutating set { box.value = newValue } }
}
enum Haptics { static func tap() {}; static func success() {}; static func attention() {}; static func error() {} }
struct Animation { static var snappy: Self { .init() }; static func snappy(duration: Double) -> Self { .init() } }
enum Theme { enum Motion { static var standard: Animation { .init() } } }
func withAnimation(_ animation: Animation, _ body: () -> Void) { body() }
enum AnswerProgress: Equatable { case sending; case failed(String) }
@MainActor final class Spy {
 var result: Resolution?
 func resolve(_ ix: Interaction, _ r: Resolution) async { result = r }
 // The fixtures' "sending" is this form's own flag; the model reports no answer of its own on the way.
 func answerProgress(_ uid: String) -> AnswerProgress? { nil }
}
`
const swiftQuestionFields = `
var interaction: Interaction
@State var picks: [String: Set<String>] = [:]
@State var other: [String: String] = [:]
@State var page = 0
@State var sending = false
@State var writing = false
@State var reviewing = false
@State var pendingStep: Task<Void, Never>?
@State var otherFocused = false
let model = Spy()
init(_ ix: Interaction) { interaction = ix }
`
const swiftQuestionRun = `
// One click on the snapshot in op, then (confirm) the review's Submit when that click opened the review.
func run(_ op: [String: Any], confirm: Bool) async -> [String: Any] {
 page = op["page"] as? Int ?? 0
 sending = op["sending"] as? Bool ?? false
 reviewing = op["reviewing"] as? Bool ?? false
 picks = (op["picks"] as? [String: [String]] ?? [:]).mapValues { Set($0) }
 other = op["other"] as? [String: String] ?? [:]
 let action = op["action"] as! String
 let inReview = stepped && reviewing
 let allowed: Bool
 switch action {
 // The review replaces the options and the footer; its own footer is disabled while the answer is on its way.
 case "confirm_review", "review_back": allowed = inReview && !isSending
 case "pick_option": allowed = !inReview && !isSending
 default: allowed = !inReview && canAdvance && !isSending
 }
 if allowed {
  switch action {
  case "pick_option": pick(questions[page], questions[page].options[op["option_index"] as! Int])
  case "advance": advance()
  case "confirm_review": reviewSubmit()
  case "review_back": reviewBack()
  default: submit()
  }
 }
 let opened = !inReview && stepped && reviewing
 if confirm {
  precondition(opened, "the click must open the review before its Submit is confirmed")
  reviewSubmit()
 }
 for _ in 0..<100 where model.result == nil && sending { try! await Task.sleep(for: .milliseconds(10)) }
 var out: [String: Any] = ["allowed": allowed, "resolution": wire(model.result), "next_page": page, "picks": picks.mapValues { $0.sorted() }, "other": other, "reviewing": reviewing]
 if let q = questions.first, let option = q.options.first {
  let d = QuestionOption.display(option.label)
  out["display"] = ["text": d.0, "recommended": d.1]
 }
 return out
}
`
const swiftFixtureMain = `
func wire(_ r: Resolution?) -> Any { r.map { try! JSONSerialization.jsonObject(with: JSONEncoder().encode($0)) } ?? NSNull() }
func check(_ expected: [String: Any], _ actual: [String: Any], _ name: String) {
 for (key,value) in expected {
  let lhs = try! JSONSerialization.data(withJSONObject: ["v":value], options: [.sortedKeys])
  let rhs = try! JSONSerialization.data(withJSONObject: ["v":actual[key] ?? NSNull()], options: [.sortedKeys])
  if lhs != rhs { fatalError("\(name).\(key): expected \(String(decoding: lhs,as: UTF8.self)), got \(String(decoding: rhs,as: UTF8.self))") }
 }
}

// What one click leaves in the draft the next click starts from (#9238). Advance, Submit and a click made while an answer
// is on its way change neither map. An accepted single choice replaces its question's picks and empties its Other entry:
// the key stays (other[q.question] = "" in QuestionForm.pick). An accepted multiple choice only toggles its pick.
// persona-core's answer tests assert the same rules for the same cases.
func checkDraft(_ op: [String: Any], _ ix: Interaction, _ actual: [String: Any], _ name: String) {
 var picks = (op["picks"] as? [String: [String]] ?? [:]).mapValues { Set($0) }
 var other = op["other"] as? [String: String] ?? [:]
 if op["action"] as? String == "pick_option", actual["allowed"] as? Bool == true {
  let q = ix.questions![op["page"] as? Int ?? 0]
  let label = q.options[op["option_index"] as! Int].label
  if q.multiSelect {
   var set = picks[q.question, default: []]
   if set.contains(label) { set.remove(label) } else { set.insert(label) }
   picks[q.question] = set
  } else {
   picks[q.question] = [label]
   other[q.question] = ""
  }
 }
 let gotPicks = (actual["picks"] as! [String: [String]]).mapValues { Set($0) }
 precondition(gotPicks == picks, "\(name): the picks a click leaves are \(gotPicks), not \(picks)")
 let gotOther = actual["other"] as! [String: String]
 precondition(gotOther == other, "\(name): the Other entries a click leaves are \(gotOther), not \(other)")
}

// The issue's own case (#9238), clicked through iOS's own code: two single choices, a custom answer typed for the first,
// then its option picked. Every click starts from what the one before left, as a person's taps do.
@MainActor func draftChain() async throws {
 func single(_ question: String, _ labels: [String]) -> [String: Any] {
  ["question": question, "multi_select": false, "options": labels.map { ["label": $0] }]
 }
 let card: [String: Any] = ["id": "example_request", "machine_id": "example_machine", "session_id": "example_session", "kind": "question", "questions": [single("One", ["A", "B"]), single("Two", ["C", "D"])]]
 let form = QuestionProbe(try JSONDecoder().decode(Interaction.self, from: JSONSerialization.data(withJSONObject: card)))
 form.other = ["One": "custom"]
 func click(_ action: String, _ option: Int? = nil, confirm: Bool = false) async -> [String: Any] {
  var op: [String: Any] = ["action": action, "page": form.page, "picks": form.picks.mapValues { $0.sorted() }, "other": form.other, "sending": false, "reviewing": form.reviewing]
  if let option { op["option_index"] = option }
  let out = await form.run(op, confirm: confirm)
  await form.pendingStep?.value // the page turn a pick on a card of several questions schedules, a pause later (#6549)
  return out
 }
 func expect(_ step: String, _ out: [String: Any], allowed: Bool, page: Int, picks: [String: Set<String>], other: [String: String], answers: [String: String]? = nil) {
  precondition(out["allowed"] as? Bool == allowed, "draft chain, \(step): allowed")
  precondition(form.page == page, "draft chain, \(step): page \(form.page)")
  precondition(form.picks == picks, "draft chain, \(step): picks \(form.picks)")
  precondition(form.other == other, "draft chain, \(step): other \(form.other)")
  let sent = (out["resolution"] as? [String: Any])?["answers"] as? [String: String]
  precondition(sent == answers, "draft chain, \(step): answers \(String(describing: sent))")
 }
 var out = await click("pick_option", 1)
 expect("pick One=B", out, allowed: true, page: 1, picks: ["One": ["B"]], other: ["One": ""])
 out = await click("advance")
 expect("Advance with Two open", out, allowed: false, page: 1, picks: ["One": ["B"]], other: ["One": ""])
 out = await click("submit")
 expect("Submit with Two open", out, allowed: false, page: 1, picks: ["One": ["B"]], other: ["One": ""])
 out = await click("pick_option", 1)
 expect("pick Two=D", out, allowed: true, page: 1, picks: ["One": ["B"], "Two": ["D"]], other: ["One": "", "Two": ""])
 out = await click("submit", confirm: true) // the review opens, then its Submit sends
 expect("Submit", out, allowed: true, page: 1, picks: ["One": ["B"], "Two": ["D"]], other: ["One": "", "Two": ""], answers: ["One": "B", "Two": "D"])
}

@main struct Main {
 @MainActor static func main() async throws {
  var files: [URL] = []
  for folder in CommandLine.arguments.dropFirst(2) {
   files += try FileManager.default.contentsOfDirectory(at: URL(fileURLWithPath: folder), includingPropertiesForKeys: nil).filter { $0.pathExtension == "json" }.sorted { $0.path < $1.path }
  }
  var count = 0, transport = 0
  var confirmed = Set<String>()
  for file in files {
   let doc = try JSONSerialization.jsonObject(with: Data(contentsOf:file)) as! [String:Any]
   for c in doc["cases"] as! [[String:Any]] {
    let input = c["input"] as! [String:Any]
    if input["profile"] as! String != "ios_current" { continue }
    let ix = try JSONDecoder().decode(Interaction.self, from: JSONSerialization.data(withJSONObject: input["interaction"]!))
    let op = input["operation"] as! [String:Any], expected = c["expected"] as! [String:Any], name = c["name"] as! String
    let action = op["action"] as! String
    var actual: [String:Any]
    if action == "resolve" {
     #if TRANSPORT
     let p = ResolveProbe()
     p.client = (op["api_available"] as! Bool) ? ProbeAPI(op["outcome"] as! String) : nil
     p.client?.hubEnds = { [unowned p] in p.interactionResolved(ix.id, assistant: nil, group: ix.group) }
     p.connection.networkUp = op["network_up"] as! Bool
     p.interactions[ix.uid] = ix
     let r = try JSONDecoder().decode(Resolution.self, from: JSONSerialization.data(withJSONObject: op["resolution"]!))
     await p.resolve(ix,r)
     actual = ["allowed": p.client != nil && p.connection.networkUp, "resolution": wire(p.client?.sent), "card_present": p.interactions[ix.uid] != nil]
     #else
     transport += 1; continue
     #endif
    } else if action == "inspect_dialog" {
     var dialog: Any = NSNull()
     if let d = ix.screenDialog { dialog = ["question": d.question, "detail": d.detail as Any? ?? NSNull(), "steps": d.steps.map { ["label": $0.label, "done": $0.done] as [String: Any] }] }
     actual = ["allowed": true, "resolution": NSNull(), "dialog": dialog]
    } else if ix.kind == .question && action != "dismiss" {
     let confirm = confirmedThroughReview.contains(name)
     actual = await QuestionProbe(ix).run(op, confirm: confirm)
     checkDraft(op, ix, actual, name)
     // A click that opens the review sends nothing; a case that does not assert the review must not open it.
     if expected["reviewing"] == nil && actual["reviewing"] as! Bool != confirm { fatalError("\(name): the review is \(actual["reviewing"]!) here; say so in the fixture") }
     if confirm { confirmed.insert(name) }
    }
    else {
     var allowed = true; var r: Resolution?
     if op["entry"] as? String == "notification" { r = NotificationActions.resolution(action:action,text:op["text"] as? String) }
     else if action == "screen_option" {
      let text = (op["text"] as? String)?.trimmingCharacters(in:.whitespacesAndNewlines)
      allowed = text == nil || !text!.isEmpty
      if allowed { r = ScreenProbe(interaction:ix).run(op["option"] as! String,text) }
     } else if action == "terminal_text" {
      let text = op["text"] as! String; allowed = !text.isEmpty
      if allowed { r = Resolution(keys:["enter"],text:text) }
     } else if action == "terminal_keys" { r = Resolution(keys:op["keys"] as? [String]) }
     else {
      if action == "always" { allowed = ix.offersAlways }
      if action.hasPrefix("approve_") { allowed = !(op["rejecting"] as? Bool ?? false) }
      if allowed { r = button(action,op["feedback"] as? String ?? "") }
     }
     actual = ["allowed":allowed,"resolution":wire(r)]
    }
    check(expected,actual,name); count += 1
   }
  }
  precondition(confirmed == confirmedThroughReview, "review-confirmed cases missing: \(confirmedThroughReview.subtracting(confirmed).sorted())")
  print("Verified \(count) iOS answer fixtures against source declarations and button constructors (\(confirmed.count) through the review; \(transport) transport cases left to the transport replay)")
  try await draftChain()
  print("Verified the draft each question click leaves, for every case above and for the two-question chain (#9238)")
  let policies = try JSONSerialization.jsonObject(with: Data(contentsOf:URL(fileURLWithPath:CommandLine.arguments[1]))) as! [String:Any]
  var modes = 0
  for c in policies["cases"] as! [[String:Any]] {
   let input = c["input"] as! [String:Any], expected = c["expected"] as! [String:Any]
   let entry = input["entry"] as! String, kind = input["kind"] as! String
   for a in expected["actions"] as! [[String:Any]] {
    guard let mode = a["mode"] as? String else { continue }
    let r: Resolution
    if entry == "notification" || entry == "lock_screen" { r = NotificationActions.resolution(action:NotificationActions.approvePlan)! }
    else if entry == "siri_shortcut" {
     let data = try JSONSerialization.data(withJSONObject:["id":"example_request","machine_id":"example_machine","session_id":"example_session","kind":kind])
     r = ApproveFirstIntent.resolution(for:try JSONDecoder().decode(Interaction.self,from:data))!
    } else if entry == "watch" { r = watchPlan() } else { r = button(a["id"] as! String,"") }
    precondition(r.mode == mode,"approval mode mismatch"); modes += 1
   }
  }
  print("Verified \(modes) current approval modes; authentication is checked separately against source and iOS test assertions")
 }
}
`

const swiftResolveSupport = `
// The app host around resolve: a session its source lets the app answer, toasts, and an API that never connects.
struct Session { var canApprove = true }
// Answered's lines for an MCP form read its fields through Elicitation; no transport case is a form.
enum Elicitation {
 enum Action: String { case accept, decline, cancel }
 struct Field { var key: String; var title: String }
 static func fields(_ schema: JSONValue?, order: [String]? = nil) -> [Field] { [] }
 static func shown(_ f: Field, _ value: JSONValue) -> String { "" }
}
struct ProbeConnection { var networkUp = true }
struct Toast { enum Style { case info, success, error }; let message: String; let style: Style; init(_ message: String, style: Style = .info) { self.message = message; self.style = style } }
@MainActor final class ProbeAPI {
 var outcome: String; var sent: Resolution?
 // The hub ends a card whose dialog changed before its 409 comes back (fleet_ix_screen.go, fleet_ix_codex.go):
 // interaction_resolved reaches the app first (AnsweringInteractions).
 var hubEnds: () -> Void = {}
 init(_ outcome: String) { self.outcome = outcome }
 func resolve(_ id: String, _ r: Resolution, opID: UUID, handoffCard: Bool) async throws -> ResolveResult {
  sent = r
  switch outcome {
  case "gone": throw APIError(status: 410, message: "gone", fields: ["status": "expired"])
  case "prompt_changed": hubEnds(); throw APIError(status: 409, message: "prompt changed", fields: ["code": "prompt_changed"])
  case "error": throw APIError(status: 500, message: "failed")
  default: return ResolveResult(ok: true)
  }
 }
}
`
const swiftResolveFields = `
var client: ProbeAPI?
var connection = ProbeConnection()
var interactions: [String: Interaction] = [:]
var endedInteractionIDs: Set<String> = []
private var endedInteractionOccurrences: [String: InteractionOccurrence] = [:]
static let endedInteractionOccurrenceLimit = 500
var answering = AnsweringInteractions()
var answerAttempts = AnswerAttempts()
var delegatedAnswers: [DelegatedAnswer] = []
var toast: Toast?
func api(_ group: String) -> ProbeAPI? { client }
func session(_ key: SessionKey) -> Session? { Session() }
func summariesChanged() {}
func withdrawTodoAsk(_ id: String, group: String) -> Bool { false }
func notDone() {}
func show(_ error: Error) {}
`

// UI-only gates and authentication metadata cannot run without SwiftUI/AppIntents hosts.
// Keep their exact source evidence checked; the runtime Swift oracle covers the callable parts.
func TestResolutionFixtureSourceEvidence(t *testing.T) {
	root := answerFixtureRoot(t)
	evidence := map[string][]string{
		"apple/Persona/Views/Interactions/InteractionViews.swift": {
			".disabled(!canAdvance || isSending)", ".disabled(isSending)", "if interaction.offersAlways {",
			"if rejecting {", "rejecting = false", ".disabled(text.isEmpty)",
			"if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {",
			"let t = text.trimmingCharacters(in: .whitespacesAndNewlines)",
			"Resolution(keys: [\"enter\"], text: t)", "Resolution(keys: keys)",
		},
		"apple/Persona/Views/Interactions/ScreenReview.swift": {
			".disabled(sending != nil)",
		},
		"apple/PersonaWatch/PersonaWatchApp.swift": {
			"Resolution(approve: true, mode: \"acceptEdits\")", "Resolution(approve: false, feedback: \"请重新考虑这个计划\")",
			"Resolution(allow: true)", "Resolution(allow: false)", "Resolution(option: opt.key)", "Resolution(keys: [opt.key])",
			"Button(\"Submit\") { submit() }", "act(.dismissed)",
		},
		"apple/PersonaTests/NotificationActionsTests.swift": {
			"#expect(allow.options.contains(.authenticationRequired))",
			"#expect(!deny.options.contains(.authenticationRequired))",
			"#expect(plan.actions.allSatisfy { $0.options.contains(.authenticationRequired) })",
			"#expect(cats[\"INTERACTION_QUESTION\"]?.actions.isEmpty == true)",
			"#expect(cats[\"INTERACTION_SCREEN\"]?.actions.isEmpty == true)",
		},
		"apple/PersonaTests/FleetIntentsTests.swift": {
			"#expect(AllowPermissionIntent.authenticationPolicy == .requiresAuthentication)",
			"#expect(ApprovePlanIntent.authenticationPolicy == .requiresAuthentication)",
			"#expect(DenyPermissionIntent.authenticationPolicy == .alwaysAllowed)",
		},
		"apple/PersonaTests/IntentsTests.swift": {
			"#expect(ApproveFirstIntent.authenticationPolicy == .requiresAuthentication)",
		},
		"apple/PersonaWidgets/FleetLiveActivity.swift": {
			"Button(intent: ApprovePlanIntent(interactionID: id, interactionCreatedAt: item.interactionCreatedAt))", "Link(destination: FleetLink.url(item)!)", ".widgetURL(FleetLink.whole(state))",
		},
	}
	for path, snippets := range evidence {
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, snippet := range snippets {
			if !strings.Contains(string(data), snippet) {
				t.Errorf("source evidence changed in %s: %s", path, snippet)
			}
		}
	}

	// The review of several questions (#2970) replaces the options and the form's footer, and its own footer shows the
	// answer on its way and is then disabled. Checked inside QuestionForm: the BeautifulUI form has lookalikes.
	views, err := os.ReadFile(filepath.Join(root, "apple/Persona/Views/Interactions/InteractionViews.swift"))
	if err != nil {
		t.Fatal(err)
	}
	question := swiftBlock(t, string(views), "struct QuestionForm:")
	for _, snippet := range []string{
		"private var showsFooter: Bool { (needsFooter || (canSkip && !skipInHeader)) && !(stepped && reviewing) }",
		"private var showsReviewFooter: Bool { stepped && reviewing }",
		"if stepped, reviewing {", "ScreenReviewFooter(rows: QuestionSteps.rows(questions, answers: shownAnswers), sending: isSending ? \"submit\" : nil,",
	} {
		if !strings.Contains(question, snippet) {
			t.Errorf("source evidence changed in QuestionForm: %s", snippet)
		}
	}

	fleet, err := os.ReadFile(filepath.Join(root, "apple/LiveActivity/FleetIntents.swift"))
	if err != nil {
		t.Fatal(err)
	}
	for name, policy := range map[string]string{"AllowPermissionIntent": ".requiresAuthentication", "ApprovePlanIntent": ".requiresAuthentication", "DenyPermissionIntent": ".alwaysAllowed"} {
		declaration := swiftBlock(t, string(fleet), "struct "+name+":")
		if !strings.Contains(declaration, "authenticationPolicy: IntentAuthenticationPolicy { "+policy+" }") {
			t.Errorf("authentication policy changed: %s", name)
		}
	}
	notifications, err := os.ReadFile(filepath.Join(root, "apple/Persona/App/NotificationActions.swift"))
	if err != nil {
		t.Fatal(err)
	}
	// Each action's identifier and unlock options; the title is a localization key (#5430) and may change freely.
	for id, options := range map[string]string{
		"allow": ".authenticationRequired", "deny": ".destructive", "approvePlan": ".authenticationRequired", "rejectPlan": ".authenticationRequired",
	} {
		re := regexp.MustCompile(`identifier: ` + id + `, title: String\(localized: "[^"]*"\), options: \[` + regexp.QuoteMeta(options) + `\]`)
		if !re.MatchString(string(notifications)) {
			t.Errorf("notification authentication changed: %s must keep options [%s]", id, options)
		}
	}
	// Siri says yes from a locked phone only after an unlock, like the Live Activity (#1878). The watch declares no
	// policy of its own; its fixtures never infer one from omission.
	data, err := os.ReadFile(filepath.Join(root, "apple/Persona/Intents/ApproveFirstIntent.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(swiftBlock(t, string(data), "struct ApproveFirstIntent:"), "authenticationPolicy: IntentAuthenticationPolicy { .requiresAuthentication }") {
		t.Fatal("Siri authentication policy changed: revisit current fixtures")
	}
}
