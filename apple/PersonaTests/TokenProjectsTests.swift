import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Token usage by project (#16530): the hub's answers decode (an unknown kind or group from a newer hub too), a range
/// groups its projects in the hub's order, what has no name of its own is named by the app (never a folder that would
/// show the account's name), shares read as whole percents at 10% and up. The tests run in Chinese.
struct TokenProjectsTests {
    private static let list = """
    {"usage": {"zone": "UTC", "stored": true, "ranges": [
      {"days": 1, "total": {"cache_read": 100}, "not_split": {}, "projects": [
        {"key": "git:github.com/acme/app", "kind": "repo", "group": "development", "name": "Acme/App", "host": "github.com", "machines": 2, "share": 1, "tokens": {"cache_read": 100}}]},
      {"days": 3, "total": {"cache_read": 400}, "projects": []},
      {"days": 7, "total": {"cache_read": 1000}, "not_split": {"cache_read": 100}, "projects": [
        {"key": "git:github.com/acme/app", "kind": "repo", "group": "development", "name": "Acme/App", "host": "github.com", "machines": 2, "share": 0.5, "tokens": {"cache_read": 500}},
        {"key": "assistant", "kind": "assistant", "group": "other", "name": "", "machines": 1, "share": 0.2, "tokens": {"cache_read": 200}},
        {"key": "local:0123", "kind": "local", "group": "development", "name": "scratch", "machines": 1, "share": 0.1, "tokens": {"cache_read": 100}},
        {"key": "x", "kind": "something-new", "group": "elsewhere", "name": "", "machines": 1, "share": 0.1, "tokens": {"cache_read": 100}}]}]}}
    """

    @Test func decodesAndGroupsTheProjects() throws {
        struct R: Decodable { var usage: UsageProjects }
        let u = try JSONDecoder().decode(R.self, from: Data(Self.list.utf8)).usage
        #expect(u.ranges.map(\.days) == [1, 3, 7])
        let week = u.ranges[2]
        #expect(week.notSplit.total == 100 && week.total.total == 1000)
        #expect(week.projects(in: .development).map(\.key) == ["git:github.com/acme/app", "local:0123"])
        #expect(week.projects(in: .other).map(\.key) == ["assistant"])
        #expect(week.total(of: .development) == 600 && week.total(of: .other) == 200)
        // A kind or group a newer hub sends reads unknown.
        #expect(week.projects[3].kind == .unknown && week.projects[3].group == .unknown)
        #expect(u.ranges[1].notSplit.total == 0 && u.ranges[1].projects.isEmpty)
        #expect(week.projects[0].machines == 2 && week.projects[0].host == "github.com" && week.projects[2].host.isEmpty)
    }

    @Test func decodesAProjectsBreakdown() throws {
        let json = """
        {"usage": {"zone": "UTC", "stored": true,
          "project": {"key": "git:github.com/acme/app", "kind": "repo", "group": "development", "name": "Acme/App", "machines": 2, "share": 0.5, "tokens": {"cache_read": 500}},
          "ranges": [{"days": 7, "total": {"in": 10, "cache_read": 500, "req": 42}, "share": 0.5, "cache_hit": 0.93,
            "machines": [{"key": "a", "name": "Studio", "tokens": {"cache_read": 499}}, {"key": "gone", "tokens": {"cache_read": 1}}],
            "agents": [{"key": "claude", "tokens": {"cache_read": 500}}], "models": [{"key": "claude-opus-5-5", "tokens": {"cache_read": 500}}],
            "launchers": [{"key": "", "tokens": {"cache_read": 500}}],
            "places": [{"machine": "a", "machine_name": "Studio", "place": "~/code/app-wt/feature", "worktree": true, "tokens": {"cache_read": 250}},
                       {"machine": "a", "place": "~/code/app", "tokens": {"cache_read": 250}}]}],
          "days": [{"date": "2026-11-17", "tokens": {"cache_read": 70}}],
          "hours": [{"t": 1800000000, "hour": 9, "tokens": {"cache_read": 7}}]}}
        """
        struct R: Decodable { var usage: UsageProjectDetail }
        let d = try JSONDecoder().decode(R.self, from: Data(json.utf8)).usage
        let b = try #require(d.ranges.first)
        #expect(b.cacheHit == 0.93 && b.total.requests == 42 && b.share == 0.5)
        #expect(b.machines.map(\.name) == ["Studio", ""] && b.launchers.first?.key == "")
        #expect(b.places.first?.worktree == true && b.places.last?.worktree == false && b.places.last?.machineName == "")
        #expect(d.days.first?.tokens.total == 70 && d.hours.first?.hour == 9 && d.project.name == "Acme/App")
    }

    @Test func namesWhatHasNoNameOfItsOwn() {
        func name(_ kind: UsageProjectKind, _ n: String = "") -> String {
            TokenProjectText.name(UsageProject(key: "k", kind: kind, group: .other, name: n))
        }
        #expect(name(.repo, "acme/app") == "acme/app")
        #expect(name(.folder, "Trip") == "Trip")
        #expect(name(.home, "someone") == "主目录")
        #expect(name(.temp) == "临时文件夹")
        #expect(name(.assistant) == "助手")
        #expect(name(.unknown) == "未知目录")
    }

    @Test func namesLaunchersAsSettingsDoes() {
        let names = ["claude-max": "Anthropic 订阅", "blank": "  "]
        #expect(TokenProjectText.launcher("claude-max", names: names) == "Anthropic 订阅")
        #expect(TokenProjectText.launcher("kimi", names: names) == "kimi", "no display name: its own name")
        #expect(TokenProjectText.launcher("blank", names: names) == "blank", "a blank display name is none")
        #expect(TokenProjectText.launcher("", names: names) == "终端里打开的")
    }

    @Test func writesShares() {
        #expect(TokenProjectText.share(0.361) == 0.36.formatted(.percent.precision(.fractionLength(0))))
        #expect(TokenProjectText.share(0.024) == 0.024.formatted(.percent.precision(.fractionLength(0...1))))
        #expect(TokenProjectText.share(0.0004).hasPrefix("<"))
    }
}
