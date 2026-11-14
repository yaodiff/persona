import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The hub's stage on a todo (#2776): one of Plan / Build / Review / Merge / Deploy / Verified, read tolerantly (a hub from before
/// the field, or one sending a name this build does not know, draws no bar at all), and the PR it named (#2768).
struct TodoStageTests {
    private func decode(_ json: String) throws -> TodoItem {
        try JSON.decoder.decode(TodoItem.self, from: Data(json.utf8))
    }

    @Test func theSixStepsAreInTheOrderTheBarDrawsThem() {
        #expect(TodoStage.allCases.map(\.rawValue) == ["plan", "develop", "review", "merge", "deploy", "verify"])
        #expect(TodoStage.allCases.map(\.label) == ["方案", "开发", "审查", "合入", "部署", "已验证"])
        #expect(TodoStage.allCases.map(\.stepNumber) == [1, 2, 3, 4, 5, 6])
        #expect(TodoStage.stepCount == 6)
    }

    /// At the accessibility text sizes the bar names only the current step, in a line under the dots, and VoiceOver
    /// reads every dot in full: its name, its place, and done / current / still ahead (#5892). A no-break space before
    /// the "·" keeps it at the end of the first line when the line wraps, so the second never starts with it.
    @Test func atTheAccessibilitySizesTheLineNamesTheCurrentStepAndEveryDotSaysItAll() {
        #expect(TodoStageBar.summary(.develop) == "第 2 步，共 6 步\u{00A0}· 开发") // i18n-ignore: the app's zh-Hans words
        #expect(TodoStageBar.summary(.verify) == "第 6 步，共 6 步\u{00A0}· 已验证") // i18n-ignore
        #expect(TodoStageBar.dotLabel(.plan, current: .develop) == "方案，第 1 步，共 6 步，已完成") // i18n-ignore
        #expect(TodoStageBar.dotLabel(.develop, current: .develop) == "开发，第 2 步，共 6 步，当前步骤") // i18n-ignore
        #expect(TodoStageBar.dotLabel(.review, current: .develop) == "审查，第 3 步，共 6 步，还没到") // i18n-ignore
        #expect(TodoStageBar.dotLabel(.verify, current: .verify) == "已验证，第 6 步，共 6 步，当前步骤") // i18n-ignore
    }

    @Test func aStageTheHubNamesIsReadAndAnythingElseIsNone() {
        #expect(TodoStage(hub: "review") == .review)
        #expect(TodoStage(hub: "verify")?.stepNumber == 6)
        // A newer hub's name, an empty one (an old hub's zero value) and no field at all draw no bar.
        #expect(TodoStage(hub: "shipped") == nil)
        #expect(TodoStage(hub: "") == nil)
        #expect(TodoStage(hub: nil) == nil)
    }

    @Test func stageAndPRDecodeFromTheHub() throws {
        let todo = try decode(#"{"id":"t1","title":"修小组件","stage":"review","pr_url":"https://example.com/shop-api/pull/128"}"#)
        #expect(todo.stage == "review")
        #expect(todo.todoStage == .review)
        #expect(todo.prLink?.absoluteString == "https://example.com/shop-api/pull/128")
        #expect(todo.prLabel == "PR #128")
    }

    @Test func anOldPayloadDecodesWithNeither() throws {
        let todo = try decode(#"{"id":"t1","title":"旧 hub 的待办"}"#)
        #expect(todo.stage == nil)
        #expect(todo.todoStage == nil)
        #expect(todo.prLink == nil)
        #expect(todo.prLabel == nil)
        // The app's own caches (StateCache, the watch summary) keep a todo as it came.
        let again = try JSON.decoder.decode(TodoItem.self, from: JSON.encoder.encode(todo))
        #expect(again.stage == nil && again.prURL == nil)
    }

    @Test func aPRThatIsNotAWebAddressIsNotShown() throws {
        #expect(try decode(#"{"id":"t1","title":"t","pr_url":"not a url"}"#).prLink == nil)
        #expect(try decode(#"{"id":"t1","title":"t","pr_url":"file:///tmp/pull/128"}"#).prLink == nil)
    }
}
