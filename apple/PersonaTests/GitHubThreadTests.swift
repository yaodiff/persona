import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A GitHub pull request or issue as GitHub's thread (#1055): the page's fields decode, consecutive checks are one
/// card, a quote reply is a Markdown quote without an @ (GitHub would notify), a hunk's lines are marked. Made-up
/// repositories and people only.
struct GitHubThreadTests {
    private let raw = #"""
    {"chat":{"id":"example-org/example-repo#12","name":"Add the example","kind":"pr","state":"open","reply":"github_comment",
             "author":{"id":"alice","name":"alice","avatar":"avatar:alice:4"},"labels":[{"name":"enhancement","color":"a2eeef"}],
             "branch":{"head":"feat/example","base":"main"},"body":"示例**描述**","body_format":"markdown"},
     "messages":[{"id":"e1","at":"2026-11-03T08:00:00Z","sender":{"name":"alice"},"event":true,"text":"alice 加了标签 enhancement"},
                 {"id":"r1","at":"2026-11-03T09:00:00Z","sender":{"name":"bob"},"kind":"review","review":"changes_requested","text":"改一下"},
                 {"id":"rc1","at":"2026-11-03T09:00:01Z","sender":{"name":"bob"},"kind":"code","text":"这里",
                  "code":{"path":"src/a.go","line":42,"hunk":"@@ -1 +1 @@\n ctx\n-old\n+new"}},
                 {"id":"ck1","at":"2026-11-03T10:00:00Z","sender":{"name":"github-actions"},"kind":"check","check":{"name":"lint","state":"success"}},
                 {"id":"ck2","at":"2026-11-03T10:01:00Z","sender":{"name":"github-actions"},"kind":"check",
                  "check":{"name":"test","state":"failure","log":"--- FAIL"}},
                 {"id":"c1","at":"2026-11-03T11:00:00Z","sender":{"name":"alice"},"text":"好了"},
                 {"id":"ck3","at":"2026-11-03T12:00:00Z","sender":{"name":"github-actions"},"kind":"check","check":{"name":"build","state":"pending"}}]}
    """#

    @Test func decodesAPullRequest() throws {
        let page = try JSON.decoder.decode(ThreadPage.self, from: Data(raw.utf8))
        #expect(page.chat.isGitHub && !page.chat.isChat)
        #expect(page.chat.branch?.head == "feat/example" && page.chat.labels?.first?.color == "a2eeef")
        #expect(page.messages[2].code?.line == 42)
        #expect(page.messages[4].check?.log == "--- FAIL")
        #expect(GitHubThread.reference(page.chat.id) == "example-org/example-repo · #12")
    }

    @Test func checksInARowAreOneCard() throws {
        let page = try JSON.decoder.decode(ThreadPage.self, from: Data(raw.utf8))
        let entries = GitHubThread.entries(page.messages)
        #expect(entries.map(\.id) == ["e1", "r1", "rc1", "checks-ck1", "c1", "checks-ck3"])
        guard case .checks(let run) = entries[3] else {
            Issue.record("no checks card")
            return
        }
        #expect(GitHubThread.checksSummary(run) == "2 项检查 · 1 项失败")
        guard case .checks(let later) = entries[5] else { return }
        #expect(GitHubThread.checksSummary(later) == "1 项检查 · 1 项进行中")
        let passed = run.filter { $0.check?.state == "success" }
        #expect(GitHubThread.checksSummary(passed) == "1 项检查 · 全部通过")
    }

    @Test func aQuoteReplyQuotesWithoutMentioning() throws {
        let page = try JSON.decoder.decode(ThreadPage.self, from: Data(raw.utf8))
        let quoted = GitHubThread.quoting(page.messages[1], reply: "改好了")
        #expect(quoted == "> 改一下\n\n改好了")
        #expect(!quoted.contains("@"))
        #expect(GitHubThread.quoting(nil, reply: "改好了") == "改好了")
    }

    @Test func hunkLinesAreMarked() {
        let lines = GitHubThread.hunkLines("@@ -1 +1 @@\n ctx\n-old\n+new")
        #expect(lines.map(\.mark) == [nil, nil, "-", "+"])
    }

    @Test func statesAndReviewsInWords() {
        #expect(GitHubThread.stateTitle("merged") == "已合并")
        #expect(GitHubThread.stateTitle("draft") == "草稿")
        #expect(GitHubThread.stateTitle("open") == "打开")
        #expect(GitHubThread.reviewTitle("approved") == "批准了")
        #expect(GitHubThread.reviewTitle("changes_requested") == "要求修改")
    }
}
