import Foundation
import Testing
@testable import Persona
import PersonaWire

/// An agent's closeout (`Closeout`, docs/assistant/agent-work.md "Closeout", #3612): what the hub sends on `work`, how a
/// claim stands against the hub's finding, the list rows' line, and the raw block left out of the last reply while
/// the card shows it (the web's lib/closeout.ts and its tests).
@MainActor
struct CloseoutTests {
    private func session(state: String = "idle", lastText: String = "最后一句", work: String?) throws -> Session {
        let w = work.map { #","work":\#($0)"# } ?? ""
        let json = #"{"session_id":"s","status":"done","state":"\#(state)","last_text":"\#(lastText)"\#(w)}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private func closeout(_ json: String) throws -> Closeout {
        try JSON.decoder.decode(Closeout.self, from: Data(json.utf8))
    }

    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String?) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
    }

    // MARK: Decoding

    @Test func decodesTheWholeCloseoutWithItsArtifacts() throws {
        let s = try session(work: #"""
        {"phase":"done","line":"导出改成流式写出","by":"rule","turn_at":"2026-11-10T01:00:00Z",
         "artifacts":[{"kind":"pr","ref":"https://github.com/example/app/pull/31"},{"kind":"commit","ref":"4f2a91c"}],
         "closeout":{"status":"done","did":["导出改成流式写出","补了压测用例"],"merged":"yes","deployed":"yes",
                     "verified":"线上导出 50 万行用时 11 秒","checked":{"merged":"yes","deployed":"yes"}}}
        """#)
        let c = try #require(s.work?.closeout)
        #expect(c.known == .done && c.did == ["导出改成流式写出", "补了压测用例"])
        #expect(c.merged == "yes" && c.deployed == "yes" && c.verified == "线上导出 50 万行用时 11 秒")
        #expect(c.checked == Closeout.Checked(merged: "yes", deployed: "yes"))
        #expect(s.work?.artifacts == [WorkArtifact(kind: "pr", ref: "https://github.com/example/app/pull/31"), WorkArtifact(kind: "commit", ref: "4f2a91c")])
        #expect(s.shownCloseout == c)
    }

    /// Session lists carry only where the closeout stands: no `did`, no `verified`, no artifacts.
    @Test func aListRowCarriesOnlyWhereItStands() throws {
        let s = try session(work: #"{"phase":"blocked","on":"external","line":"等 DBA","closeout":{"status":"blocked","merged":"no","waiting":"DBA 批准迁移窗口"}}"#)
        let c = try #require(s.work?.closeout)
        #expect(c.known == .blocked && c.waiting == "DBA 批准迁移窗口" && c.did == nil && c.verified == nil && c.checked == nil)
        #expect(s.work?.artifacts == nil)
    }

    @Test func anOlderHubSendsNoCloseout() throws {
        let s = try session(work: #"{"phase":"done","line":"已合并 PR #12"}"#)
        #expect(s.work?.closeout == nil && s.shownCloseout == nil && s.work?.notFinished == false)
        #expect(s.listPreview(muted: false) == "✓ 已合并 PR #12")
    }

    /// Without a closeout nothing new is drawn (DESIGN_SYSTEM.md 1.2 rule 2): no card (`shownCloseout`, which the chat's
    /// header and its lines key on), no suffix, the row's line as before, and an empty phase with a line is not
    /// "not finished" (that needs the closeout).
    @Test(arguments: [("idle", #"{"phase":"done","line":"已合并 PR #12"}"#, "✓ 已合并 PR #12"),
                      ("unread", #"{"phase":"","line":"还在等 CI"}"#, "还在等 CI"),
                      ("idle", nil, "最后一句")])
    func withoutACloseoutTheRowAndTheChatAreAsBefore(state: String, work: String?, line: String) throws {
        let s = try session(state: state, work: work)
        #expect(s.shownCloseout == nil && s.closeoutPreview() == nil && s.work?.notFinished != true)
        #expect(s.listPreview(muted: false) == line)
        let user = item(1, .text, role: "user", text: "修一下导出")
        let lines = [ChatLine.row(.user(user)), .row(.assistant(item(2, .text, text: "修好了。")))]
        #expect(ChatLines.hidingCloseout(lines).map(\.id) == lines.map(\.id))
    }

    /// A closeout or an artifact list in a shape this build does not know is as if there were none: the work state
    /// and the session stay.
    @Test(arguments: [#""closeout":"done""#, #""closeout":42"#, #""closeout":["done"]"#, #""artifacts":"pr""#, #""artifacts":[1,2]"#])
    func anOddCloseoutNeverDropsTheWorkState(field: String) throws {
        let s = try session(work: #"{"phase":"done","line":"已合并 PR #12",\#(field)}"#)
        #expect(s.work?.phase == "done" && s.work?.closeout == nil && s.work?.artifacts == nil)
        #expect(s.listPreview(muted: false) == "✓ 已合并 PR #12")
    }

    /// Fields in another shape are left out one by one; a status this build does not know is no closeout to show.
    @Test func oddFieldsAreLeftOutOneByOne() throws {
        let c = try closeout(#"{"status":"done","did":"一句话","merged":true,"deployed":"yes","verified":["a"],"checked":"yes","extra":1}"#)
        #expect(c.known == .done && c.did == nil && c.merged == nil && c.deployed == "yes" && c.verified == nil && c.checked == nil)
        #expect(try closeout("{}").known == nil)
        let odd = try session(work: #"{"phase":"done","line":"结论","closeout":{"status":"parked","merged":"yes"}}"#)
        #expect(odd.work?.closeout?.status == "parked" && odd.shownCloseout == nil)
        #expect(odd.listPreview(muted: false) == "✓ 结论")
        let back = try JSON.decoder.decode(Closeout.self, from: JSONEncoder().encode(c))
        #expect(back == c)
    }

    // MARK: Claims against the hub's finding

    @Test func aClaimIsConfirmedContradictedUncheckedOrFound() throws {
        let c = try closeout(#"{"status":"done","merged":"yes","deployed":"yes","checked":{"merged":"yes","deployed":"no"}}"#)
        #expect(c.claim(.merged) == Closeout.Claim(said: true, found: true, state: .confirmed))
        #expect(c.claim(.deployed) == Closeout.Claim(said: true, found: false, state: .contradicted))
        #expect(c.contradicted)
        let unchecked = try closeout(#"{"status":"done","merged":"no"}"#)
        #expect(unchecked.claim(.merged) == Closeout.Claim(said: false, found: nil, state: .unchecked))
        #expect(unchecked.claim(.deployed) == nil && !unchecked.contradicted)
        let found = try closeout(#"{"status":"done","checked":{"merged":"yes"}}"#)
        #expect(found.claim(.merged) == Closeout.Claim(said: nil, found: true, state: .found))
        // "n/a" and anything that is neither yes nor no is as if it were not said.
        #expect(try closeout(#"{"status":"done","merged":"n/a","deployed":"maybe"}"#).claim(.merged) == nil)
    }

    @Test func theSuffixSaysWhereItStandsInAFewWords() throws {
        #expect(try closeout(#"{"status":"done","merged":"yes","deployed":"no"}"#).suffix == "已合并 · 未部署")
        #expect(try closeout(#"{"status":"done","merged":"yes","deployed":"yes","checked":{"merged":"yes","deployed":"yes"}}"#).suffix == "已合并 ✓ · 已部署 ✓")
        #expect(try closeout(#"{"status":"done","merged":"yes","deployed":"yes","checked":{"merged":"no","deployed":"no"}}"#).suffix == "✗ 说已合并，实际没有 · ✗ 说已部署，实际没有")
        // The agent said no and the hub found it merged: the hub's finding, not marked as a broken claim.
        #expect(try closeout(#"{"status":"done","merged":"no","checked":{"merged":"yes"}}"#).suffix == "已合并 ✓")
        #expect(try closeout(#"{"status":"done","checked":{"deployed":"no"}}"#).suffix == "未部署")
        #expect(try closeout(#"{"status":"done"}"#).suffix == "")
    }

    // MARK: The list rows' line

    /// Today's line stays, with the short merged / deployed suffix after it.
    @Test func theRowKeepsItsLineAndAddsTheSuffix() throws {
        let done = try session(work: #"{"phase":"done","line":"已合并 PR #12，测试全过","closeout":{"status":"done","merged":"yes","deployed":"no","checked":{"merged":"yes"}}}"#)
        #expect(done.workLine() == "✓ 已合并 PR #12，测试全过", "workLine is the line as before")
        #expect(done.listPreview(muted: false) == "✓ 已合并 PR #12，测试全过 · 已合并 ✓ · 未部署")
        let blocked = try session(work: #"{"phase":"blocked","on":"external","line":"等 DBA 给迁移窗口","closeout":{"status":"blocked","merged":"no"}}"#)
        #expect(blocked.listPreview(muted: false) == "等 DBA 给迁移窗口 · 未合并")
        let plain = try session(work: #"{"phase":"done","line":"查清了原因","closeout":{"status":"done"}}"#)
        #expect(plain.listPreview(muted: false) == "✓ 查清了原因", "nothing said on merged or deployed: no suffix")
    }

    /// A turn nobody can call finished: an empty phase, the closeout and a line. The row shows the line as it is
    /// (no check mark, no wait), with a claim the hub found untrue marked; it never counts as waiting.
    @Test func anEmptyPhaseWithALineIsNotFinished() throws {
        let s = try session(state: "unread", work: #"{"phase":"","line":"自述已合并，但 PR 还没合并","closeout":{"status":"done","merged":"yes","deployed":"no","checked":{"merged":"no"}}}"#)
        #expect(s.work?.notFinished == true && s.work?.hubWaits == false && s.state == .unread)
        #expect(s.listPreview(muted: false) == "自述已合并，但 PR 还没合并 · ✗ 说已合并，实际没有 · 未部署")
        let missing = try session(state: "unread", work: #"{"phase":"","line":"没做完：要你确认分词库","closeout":{"status":"not_done","missing":"要你确认分词库"}}"#)
        #expect(missing.work?.notFinished == true)
        #expect(missing.listPreview(muted: false) == "没做完：要你确认分词库")
        #expect(try session(work: #"{"phase":"done","line":"结论","closeout":{"status":"done"}}"#).work?.notFinished == false)
    }

    /// The hub gave no line: the closeout's own words, never the raw block of the last reply.
    @Test func theCloseoutsOwnWordsWhenTheHubGaveNoLine() throws {
        let raw = #"<closeout status=\"not_done\">\nmissing: 要你确认分词库\n</closeout>"#
        #expect(try session(lastText: raw, work: #"{"phase":"","closeout":{"status":"not_done","missing":"要你确认分词库"}}"#).listPreview(muted: false) == "没做完：要你确认分词库")
        #expect(try session(lastText: raw, work: #"{"phase":"","closeout":{"status":"not_done"}}"#).listPreview(muted: false) == "没做完")
        #expect(try session(lastText: raw, work: #"{"phase":"","closeout":{"status":"blocked","waiting":"DBA","merged":"no"}}"#).listPreview(muted: false) == "卡在：DBA · 未合并")
        #expect(try session(lastText: raw, work: #"{"phase":"","closeout":{"status":"blocked"}}"#).listPreview(muted: false) == "卡住了")
        #expect(try session(lastText: raw, work: #"{"phase":"","closeout":{"status":"done","deployed":"yes"}}"#).listPreview(muted: false) == "自述已完成 · 已部署")
    }

    /// A new turn has no closeout yet, and the previous turn's no longer says where the work stands.
    @Test func aWorkingSessionShowsNoCloseout() throws {
        let work = #"{"phase":"done","line":"旧的结论","closeout":{"status":"done","merged":"yes"}}"#
        let s = try session(state: "working", work: work)
        #expect(s.shownCloseout == nil && s.listPreview(muted: false) == "最后一句")
        #expect(try session(work: #"{"phase":"working","closeout":{"status":"done","merged":"yes"}}"#).shownCloseout == nil)
        #expect(try session(work: work).shownCloseout != nil)
    }

    // MARK: Artifacts

    @Test func artifactsReadAsLinksOrReferences() {
        let pr = WorkArtifact(kind: "pr", ref: " https://github.com/example/app/pull/31 ")
        #expect(pr.link?.absoluteString == "https://github.com/example/app/pull/31" && pr.label == "PR #31")
        #expect(WorkArtifact(kind: "pr", ref: "https://git.example.org/group/app/-/merge_requests/7").label == "MR !7")
        #expect(WorkArtifact(kind: "url", ref: "https://example.org/report?id=3").label == "example.org/report?id=3")
        let commit = WorkArtifact(kind: "commit", ref: "4f2a91c")
        #expect(commit.link == nil && commit.label == "4f2a91c")
        // Only web links open: a path or another scheme is a reference to read.
        #expect(WorkArtifact(kind: "doc", ref: "docs/migrations/split-users.md").link == nil)
        #expect(WorkArtifact(kind: "url", ref: "file:///etc/hosts").link == nil)
        #expect(WorkArtifact(kind: "url", ref: "javascript:alert(1)").link == nil)
    }

    // MARK: The raw block in the reply

    private let block = "<closeout status=\"done\" merged=\"yes\">\ndid: 改了导出\nverified: untested\n</closeout>"

    @Test func stripTakesExactlyTheBlock() {
        #expect(Closeout.strip("导出修好了。\n\n" + block) == "导出修好了。")
        #expect(Closeout.strip("前面\n\n" + block + "\n\n后面") == "前面\n\n后面")
        #expect(Closeout.strip(block) == "")
        #expect(Closeout.strip("没有记录的回复 <b>加粗</b>") == "没有记录的回复 <b>加粗</b>")
        // Only the last block goes, the one the hub read; an earlier one is part of what the reply says.
        let earlier = "<closeout status=\"not_done\">\nmissing: 还差测试\n</closeout>"
        #expect(Closeout.strip("先写了一版：\n\n" + earlier + "\n\n补完了。\n\n" + block) == "先写了一版：\n\n" + earlier + "\n\n补完了。")
        // A block that never closes is not one.
        #expect(Closeout.strip("说到一半 <closeout status=\"done\">\ndid: 半截") == "说到一半 <closeout status=\"done\">\ndid: 半截")
    }

    @Test func aFenceAroundTheBlockAloneGoesWithIt() {
        #expect(Closeout.strip("修好了。\n\n```\n" + block + "\n```") == "修好了。")
        #expect(Closeout.strip("修好了。\n\n```xml\n" + block + "\n```\n\n完。") == "修好了。\n\n完。")
        // A fence that holds something else too stays, without the block.
        let mixed = Closeout.strip("```\nlog line\n" + block + "\n```")
        #expect(mixed.contains("log line") && mixed.hasPrefix("```") && mixed.hasSuffix("```") && !mixed.contains("closeout"))
    }

    @Test func theLastReplyShowsWithoutItsBlock() {
        let user = item(1, .text, role: "user", text: "修一下导出")
        let earlier = item(2, .text, text: "先看看。\n\n" + block)
        let follow = item(3, .text, role: "user", text: "继续")
        let last = item(4, .text, text: "修好了。\n\n" + block)
        let lines = [ChatLine.row(.user(user)), .row(.assistant(earlier)), .row(.user(follow)),
                     .work(id: "w-4", items: [], live: false, start: nil, end: nil), .row(.assistant(last))]
        let shown = ChatLines.hidingCloseout(lines)
        #expect(shown.map(\.id) == lines.map(\.id))
        guard case .row(.assistant(let answer)) = shown[4].content, case .row(.assistant(let before)) = shown[1].content else {
            Issue.record("expected the answers in place")
            return
        }
        // The words without the block under an id of their own, so the Markdown kept for the raw reply (by its id) is
        // never drawn for them; the line keeps the reply's id.
        #expect(answer.text == "修好了。" && answer.id == last.id + ChatLines.closeoutShownSuffix && shown[4].id == last.id)
        #expect(shown[4].holds(answer.id))
        #expect(before.text == earlier.text, "an earlier turn's block stays as it was written")
    }

    @Test func aReplyThatWasOnlyTheBlockIsNotDrawn() {
        let user = item(1, .text, role: "user", text: "修一下导出")
        let last = item(2, .text, text: block)
        let lines = [ChatLine.row(.user(user)), .work(id: "w-2", items: [], live: false, start: nil, end: nil), .row(.assistant(last))]
        #expect(ChatLines.hidingCloseout(lines).map(\.id) == [user.id, "w-2"])
    }

    @Test func nothingChangesWithoutABlockInTheLastWords() {
        let user = item(1, .text, role: "user", text: "修一下导出")
        let answer = item(2, .text, text: "修好了。\n\n" + block)
        let follow = item(3, .text, role: "user", text: "再看看日志")
        // A message came after the reply: that exchange has no closing words yet, and the older reply stays whole.
        let waiting = [ChatLine.row(.user(user)), .row(.assistant(answer)), .row(.user(follow))]
        let shown = ChatLines.hidingCloseout(waiting)
        guard case .row(.assistant(let kept)) = shown[1].content else { Issue.record("expected the answer"); return }
        #expect(shown.count == 3 && kept.text == answer.text)
        let plain = [ChatLine.row(.user(user)), .row(.assistant(item(2, .text, text: "修好了。")))]
        #expect(ChatLines.hidingCloseout(plain).map(\.id) == plain.map(\.id))
        #expect(ChatLines.hidingCloseout([]).isEmpty)
    }
}
