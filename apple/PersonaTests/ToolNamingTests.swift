import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Claude Code's newer tools and other servers' MCP tools read as words in the chat, never as their raw names and a
/// JSON dump (#2986): verbs and subjects for the current tool set, "<Server> · <Tool>" for MCP tools, a subject picked
/// by the fields that say the most, and arguments as a key/value list.
@MainActor
struct ToolNamingTests {
    private func call(_ name: String, _ input: [String: JSONValue] = [:], failed: Bool = false) -> ToolCall {
        let use = Item(id: "1#0", seq: 1, ts: Date(timeIntervalSince1970: 1), role: "assistant", kind: .toolUse, text: nil,
                       toolUseID: "t1", toolName: name, toolInput: .object(input), isError: nil, image: nil)
        let error = Item(id: "2#0", seq: 2, ts: Date(timeIntervalSince1970: 2), role: "user", kind: .toolResult, text: "No matching tools",
                         toolUseID: "t1", toolName: nil, toolInput: nil, isError: true, image: nil)
        return ToolCall(use: use, result: failed ? error : nil)
    }

    // MARK: Names in words

    @Test(arguments: [
        ("create_issue", "Create issue"),
        ("createIssue", "Create issue"),
        ("list-pull-requests", "List pull requests"),
        ("getPRComments", "Get PR comments"),
        ("HTTPServerStart", "HTTP server start"),
        ("browser_take_screenshot", "Browser take screenshot"),
        ("search_github_repos", "Search GitHub repos"),
        ("get_url", "Get URL"),
        ("ios_build", "iOS build"),
        ("ShareOnboardingGuide", "Share onboarding guide"),
        ("x", "X"),
        ("", ""),
    ])
    func identifiersReadAsWords(identifier: String, expected: String) {
        #expect(ToolNaming.humanize(identifier) == expected, "\(identifier)")
    }

    @Test(arguments: [
        ("github", "GitHub"),
        ("chrome-devtools", "Chrome DevTools"),
        ("google_drive", "Google Drive"),
        ("plugin_context7_context7", "Context7"),
        ("plugin_lark_docs", "Lark Docs"),
        ("claude_ai_Gmail", "Gmail"),
        ("ide", "IDE"),
        ("linear", "Linear"),
    ])
    func serversReadAsNames(server: String, expected: String) {
        #expect(ToolNaming.serverName(server) == expected, "\(server)")
    }

    @Test func otherServersToolsReadAsServerAndTool() {
        #expect(ToolNaming.mcpLabel("mcp__github__create_issue") == "GitHub · Create issue")
        #expect(ToolNaming.mcpLabel("mcp__ide__getDiagnostics") == "IDE · Get diagnostics")
        #expect(ToolNaming.mcpLabel("mcp__plugin_playwright_playwright__browser_navigate") == "Playwright · Browser navigate")
        #expect(ToolNaming.mcpLabel("mcp__claude_ai_Gmail__search_threads") == "Gmail · Search threads")
        // Persona's own tools keep their names (PersonaToolNames); built-in tools are not MCP tools.
        #expect(ToolNaming.mcpLabel("mcp__persona__agent_read") == nil)
        #expect(ToolNaming.mcpLabel("Read") == nil)
        #expect(ToolPresentation.verb("mcp__github__create_issue") == "GitHub · Create issue")
        #expect(ToolPresentation.verb("mcp__persona__agent_read") == "读 Agent 对话")
        // A table's bare name under another server is that server's tool, not the built-in one.
        #expect(ToolPresentation.verb("mcp__files__Read") == "Files · Read")
        #expect(ToolPresentation.symbol("mcp__files__Read") == Theme.Icon.skills)
        #expect(ToolNaming.tableName("mcp__files__Read") == "mcp__files__Read")
        #expect(ToolNaming.tableName("mcp__persona__ask_user") == "ask_user")
    }

    // MARK: Claude Code's tools

    @Test(arguments: [
        ("TaskCreate", "加待办"),
        ("TaskUpdate", "更新待办"),
        ("TaskList", "看待办列表"),
        ("TaskGet", "看待办"),
        ("TaskStop", "停止任务"),
        ("KillShell", "停止任务"),
        ("TaskOutput", "读输出"),
        ("SendMessage", "给 Agent 发消息"),
        ("ListAgents", "看 Agent 列表"),
        ("ScheduleWakeup", "安排下次唤醒"),
        ("CronCreate", "设定期任务"),
        ("CronList", "看定期任务"),
        ("CronDelete", "删定期任务"),
        ("RemoteTrigger", "云端定期任务"),
        ("Monitor", "监视"),
        ("LSP", "查代码"),
        ("EnterWorktree", "进入 worktree"),
        ("ExitWorktree", "离开 worktree"),
        ("SendUserFile", "发文件给你"),
        ("Artifact", "发布 Artifact"),
        ("PushNotification", "通知你"),
        ("ReportFindings", "报告审查结果"),
        ("PowerShell", "运行"),
        ("Skill", "使用技能"),
        ("ToolSearch", "加载工具"),
        ("WaitForMcpServers", "等 MCP 服务器"),
        ("ListMcpResourcesTool", "看 MCP 资源列表"),
        ("ReadMcpResourceTool", "读 MCP 资源"),
        ("NotebookEdit", "编辑"),
        ("TodoWrite", "更新待办"),
        ("EnterPlanMode", "进入计划模式"),
        ("ExitPlanMode", "提交计划"),
    ])
    func claudeCodeToolsHaveVerbs(name: String, expected: String) {
        #expect(ToolPresentation.verb(name) == expected, "\(name)")
    }

    /// A few say what they did from their input; a run's chips still group them by their name's verb.
    @Test func callsSayWhatTheyDid() {
        #expect(ToolPresentation.verb(call("TaskUpdate", ["taskId": .string("3"), "status": .string("completed")])) == "完成待办")
        #expect(ToolPresentation.verb(call("TaskUpdate", ["taskId": .string("3"), "status": .string("in_progress")])) == "开始待办")
        #expect(ToolPresentation.verb(call("TaskUpdate", ["taskId": .string("3"), "owner": .string("me")])) == "更新待办")
        #expect(ToolPresentation.verb(call("NotebookEdit", ["edit_mode": .string("insert")])) == "加单元格")
        #expect(ToolPresentation.verb(call("NotebookEdit", ["edit_mode": .string("delete")])) == "删单元格")
        #expect(ToolPresentation.verb(call("NotebookEdit", [:])) == "改单元格")
        #expect(ToolPresentation.verb(call("LSP", ["operation": .string("findReferences")])) == "找引用")
        #expect(ToolPresentation.verb(call("LSP", ["operation": .string("outgoingCalls")])) == "看调用层级")
        #expect(ToolPresentation.verb(call("ScheduleWakeup", ["stop": .bool(true)])) == "停止循环")
        #expect(ToolPresentation.verb(call("ExitWorktree", ["action": .string("remove")])) == "移除 worktree")
        #expect(ToolPresentation.verb(call("ExitWorktree", ["action": .string("keep")])) == "离开 worktree")
        #expect(ToolPresentation.verb(call("Bash", ["command": .string("ls")])) == "运行")
    }

    @Test func subjectsSayWhatACallIsAbout() {
        #expect(ToolPresentation.subject(call("TaskCreate", ["subject": .string("Write tests"), "description": .string("…")])) == "Write tests")
        #expect(ToolPresentation.subject(call("TaskUpdate", ["taskId": .string("3"), "status": .string("completed")])) == "#3")
        #expect(ToolPresentation.subject(call("TaskUpdate", ["taskId": .string("3"), "activeForm": .string("Writing tests")])) == "Writing tests")
        #expect(ToolPresentation.subject(call("TaskList")) == "")
        #expect(ToolPresentation.subject(call("TaskStop", ["task_id": .string("b7f2")])) == "b7f2")
        #expect(ToolPresentation.subject(call("SendMessage", ["to": .string("researcher"), "summary": .string("assign task 1"),
                                                              "message": .string("start on task #1")])) == "researcher · assign task 1")
        #expect(ToolPresentation.subject(call("SendMessage", ["to": .string("lead"), "message": .string("Tests pass.\nDetails below")])) == "lead · Tests pass.")
        #expect(ToolPresentation.subject(call("SendMessage", ["to": .string("lead"), "message": .object(["type": .string("shutdown_response")])]))
            == "lead · Shutdown response")
        #expect(ToolPresentation.subject(call("ToolSearch", ["query": .string("select:WebFetch,Monitor")])) == "WebFetch, Monitor")
        #expect(ToolPresentation.subject(call("ToolSearch", ["query": .string("slack send")])) == "slack send")
        #expect(ToolPresentation.subject(call("Monitor", ["command": .string("tail -f log\n# more")])) == "tail -f log")
        #expect(ToolPresentation.subject(call("Monitor", ["ws": .object(["url": .string("wss://events.example.com")])])) == "wss://events.example.com")
        #expect(ToolPresentation.subject(call("PowerShell", ["command": .string("Get-ChildItem")])) == "Get-ChildItem")
        #expect(ToolPresentation.subject(call("LSP", ["operation": .string("findReferences"), "filePath": .string("/a/ToolViews.swift"),
                                                      "line": .number(120), "character": .number(4)])) == "ToolViews.swift:120")
        #expect(ToolPresentation.subject(call("LSP", ["operation": .string("workspaceSymbol"), "query": .string("ToolNaming"),
                                                      "filePath": .string("/a/b.swift"), "line": .number(1)])) == "ToolNaming")
        #expect(ToolPresentation.subject(call("CronCreate", ["cron": .string("7 * * * *"), "prompt": .string("check CI\nthen report")])) == "7 * * * * · check CI")
        #expect(ToolPresentation.subject(call("ScheduleWakeup", ["delaySeconds": .number(300), "reason": .string("wait for CI")])) == "wait for CI")
        #expect(ToolPresentation.subject(call("EnterWorktree", ["path": .string("/r/.claude/worktrees/fix-1")])) == "fix-1")
        #expect(ToolPresentation.subject(call("SendUserFile", ["files": .array([.string("/tmp/report.pdf"), .string("/tmp/chart.png")])])) == "report.pdf, chart.png")
        #expect(ToolPresentation.subject(call("PushNotification", ["message": .string("Build done"), "status": .string("proactive")])) == "Build done")
        #expect(ToolPresentation.subject(call("NotebookEdit", ["notebook_path": .string("/n/analysis.ipynb")])) == "analysis.ipynb")
        let one: JSONValue = .object(["file": .string("a.swift"), "summary": .string("Crashes on nil"), "short_summary": .string("Nil crash"),
                                      "failure_scenario": .string("…")])
        #expect(ToolPresentation.subject(call("ReportFindings", ["findings": .array([one])])) == "Nil crash")
        #expect(ToolPresentation.subject(call("ReportFindings", ["findings": .array([one, one])])) == "2 条发现")
        #expect(ToolPresentation.subject(call("mcp__github__create_issue", ["owner": .string("o"), "repo": .string("r"),
                                                                            "title": .string("Crash on launch")])) == "Crash on launch")
    }

    @Test func symbolsComeFromTheVocabulary() {
        #expect(ToolPresentation.symbol("Monitor") == Theme.Icon.monitor)
        #expect(ToolPresentation.symbol("PowerShell") == Theme.Icon.terminal)
        #expect(ToolPresentation.symbol("TaskCreate") == Theme.Icon.todo)
        #expect(ToolPresentation.symbol("TaskStop") == Theme.Icon.stopLoop)
        #expect(ToolPresentation.symbol("SendMessage") == Theme.Icon.agentChat)
        #expect(ToolPresentation.symbol("LSP") == Theme.Icon.code)
        #expect(ToolPresentation.symbol("mcp__persona__phone_contacts_search") == Theme.Icon.phone)
        #expect(ToolPresentation.symbol("SomethingNew") == Theme.Icon.unknownTool)
    }

    // MARK: The generic fallback

    @Test func subjectIsTheMostTellingField() {
        func subject(_ o: [String: JSONValue]) -> String { ToolNaming.subject(.object(o)) }
        #expect(subject(["title": .string("T"), "description": .string("D\nmore")]) == "D")
        #expect(subject(["name": .string("N"), "title": .string("T")]) == "T")
        #expect(subject(["path": .string("/a/b/Store.swift"), "query": .string("q")]) == "Store.swift")
        #expect(subject(["url": .string("https://example.com"), "pattern": .string("p")]) == "https://example.com")
        // No telling field: the first string by field name; numbers, flags and blanks say nothing.
        #expect(subject(["zeta": .string("z"), "alpha": .string("  a  "), "count": .number(3)]) == "a")
        #expect(subject(["count": .number(3), "on": .bool(true), "note": .string("   ")]) == "")
        #expect(ToolNaming.subject(.array([.string("x")])) == "")
    }

    @Test func argumentsReadAsAKeyValueList() {
        let args = ToolNaming.arguments(.object([
            "repo": .string("persona"),
            "title": .string("Crash on launch"),
            "body": .string("Steps:\n1. Open\n2. Crash"),
            "labels": .array([.string("bug"), .string("ios")]),
            "draft": .bool(false),
            "assignee": .null,
            "meta": .object(["b": .bool(true), "a": .string("x/y")]),
            "blank": .string(" "),
            "none": .array([]),
        ]))
        // The telling fields first, then the rest by name; blank values left out.
        #expect(args.map(\.id) == ["title", "assignee", "body", "draft", "labels", "meta", "repo"])
        #expect(args.map(\.label) == ["Title", "Assignee", "Body", "Draft", "Labels", "Meta", "Repo"])
        let byID = Dictionary(uniqueKeysWithValues: args.map { ($0.id, $0) })
        #expect(byID["title"]?.code == false && byID["title"]?.lines == 1)
        #expect(byID["repo"]?.code == true)
        #expect(byID["body"]?.lines == 3 && byID["body"]?.value == "Steps:\n1. Open\n2. Crash")
        #expect(byID["labels"]?.value == "bug, ios")
        #expect(byID["draft"]?.value == "false")
        #expect(byID["assignee"]?.value == "null")
        #expect(byID["meta"]?.value == #"{"a":"x/y","b":true}"#)
        #expect(ToolNaming.arguments(.object(["file_path": .string("/a")]), skipping: ["file_path"]).isEmpty)
        #expect(ToolNaming.arguments(.string("x")).isEmpty)
    }

    /// A value's few lines go to its words: blank lines between paragraphs (and lines of only spaces) close up to one
    /// break, so a three-line value is not cut on an empty line with no "…". A line's own indent stays.
    @Test func blankLinesInAValueCloseUp() {
        let body = "Exporting takes 41 s.\n\n\n   \nSteps: Settings > Export.\r\n\r\nLast year."
        let args = ToolNaming.arguments(.object(["body": .string(body), "code": .string("def f():\n\n    return 1\n")]))
        let byID = Dictionary(uniqueKeysWithValues: args.map { ($0.id, $0) })
        #expect(byID["body"]?.value == "Exporting takes 41 s.\nSteps: Settings > Export.\nLast year.")
        #expect(byID["body"]?.lines == 3)
        #expect(byID["code"]?.value == "def f():\n    return 1")
        // Blank lines around one line of text leave one line.
        let one = ToolNaming.arguments(.object(["note": .string("\n\n  Only this\n\n")])).first
        #expect(one?.value == "Only this" && one?.lines == 1)
    }

    /// Any number a tool sends lists without trapping: past Int's range it prints as a Double (`JSONValue.string`).
    @Test func hugeNumbersListWithoutTrapping() {
        #expect(JSONValue.number(3).string == "3")
        #expect(JSONValue.number(2.5).string == "2.5")
        #expect(JSONValue.number(1e20).string == "1e+20")
        #expect(JSONValue.number(1e20).int == nil && JSONValue.string("inf").int == nil && JSONValue.string("nan").int == nil)
        #expect(JSONValue.number(-3.7).int == -3 && JSONValue.string("42").int == 42)
        let args = ToolNaming.arguments(.object(["limit": .number(1e20), "ids": .array([.number(Double(UInt64.max)), .number(7)])]))
        #expect(args.map(\.value) == ["1.8446744073709552e+19, 7", "1e+20"])
    }

    // MARK: Runs of steps

    @Test func bookkeepingStaysQuietInARun() {
        #expect(ToolNaming.isQuiet("ToolSearch") && ToolNaming.isQuiet("TaskList") && ToolNaming.isQuiet("ListAgents"))
        #expect(!ToolNaming.isQuiet("TaskCreate") && !ToolNaming.isQuiet("mcp__x__ToolSearch"))
        let run = [call("ToolSearch", ["query": .string("select:Monitor")]), call("Bash", ["command": .string("ls")]),
                   call("TaskList")]
        #expect(ToolRunSummary.chips(run).map(\.label) == ["运行"])
        // Nothing else in the run: its bookkeeping is what it did.
        #expect(ToolRunSummary.chips([call("ToolSearch", ["query": .string("q")])]).map(\.label) == ["加载工具"])
        #expect(ToolRunSummary.chips([call("PowerShell", ["command": .string("dir")]), call("Bash", ["command": .string("ls")])]).count == 1)
        // One call's chip says what it did, as its row does; several share their name's verb.
        let done = call("TaskUpdate", ["taskId": .string("3"), "status": .string("completed")])
        #expect(ToolRunSummary.chips([done, call("Bash", ["command": .string("ls")])]).first?.label == "完成待办")
        #expect(ToolRunSummary.chips([done, done]).first?.label == "更新待办")
        // Bookkeeping that failed still shows, red: a closed run never hides a failed step.
        let failed = ToolRunSummary.chips([call("ToolSearch", ["query": .string("select:Nope")], failed: true), call("Bash", ["command": .string("ls")])])
        #expect(failed.map(\.label) == ["加载工具", "运行"])
        #expect(failed.map(\.isError) == [true, false])
        #expect(ToolNaming.speaks(call("TaskList", failed: true)) && !ToolNaming.speaks(call("TaskList")))
    }

    @Test func aNotebookEditCountsItsNewLines() {
        let inserted = ToolRunSummary.diff(call("NotebookEdit", ["new_source": .string("a\nb\nc"), "edit_mode": .string("insert")]))
        #expect(inserted.adds == 3 && inserted.dels == 0)
        #expect(ToolRunSummary.diff(call("NotebookEdit", ["new_source": .string("a\nb\n")])).adds == 2)
        let deleted = ToolRunSummary.diff(call("NotebookEdit", ["new_source": .string("x"), "edit_mode": .string("delete")]))
        #expect(deleted.adds == 0 && deleted.dels == 0)
        #expect(ToolNaming.lineCount("") == 0 && ToolNaming.lineCount("x") == 1)
    }

    @Test func shellToolsShowTheirCommand() {
        #expect(ToolNaming.commandLine("Bash", .object(["command": .string("ls")])) == "$ ls")
        #expect(ToolNaming.commandLine("PowerShell", .object(["command": .string("dir")])) == "PS> dir")
        #expect(ToolNaming.commandLine("Monitor", .object(["command": .string("tail -f x")])) == "$ tail -f x")
        #expect(ToolNaming.commandLine("Monitor", .object(["ws": .object(["url": .string("wss://e.example.com")])])) == "wss://e.example.com")
        #expect(ToolNaming.commandLine("Read", .object([:])) == nil)
    }

    @Test func wordsGiveWayAtTheirEndAPathInItsMiddle() {
        #expect(ToolPresentation.truncation("Read", subject: "/Users/me/code/weather/Sources/App/Forecast.swift") == .middle)
        #expect(ToolPresentation.truncation("Grep", subject: "func\\s+load") == .middle)
        #expect(ToolPresentation.truncation("Monitor", subject: "Watch the export's throughput") == .tail)
        #expect(ToolPresentation.truncation("mcp__github__create_issue", subject: "CSV export times out on a year of hourly data") == .tail)
        #expect(ToolPresentation.truncation("TodoWrite", subject: "Fix") == .tail)
        let issue = call("mcp__github__create_issue", ["title": .string("CSV export times out on a year of hourly data")])
        #expect(ToolRunSummary.chips([issue]).first?.truncatesTail == true)
        let read = call("Read", ["file_path": .string("/Users/me/code/weather/Sources/App/Forecast.swift")])
        #expect(ToolRunSummary.chips([read]).first?.truncatesTail == false)
        let done = call("TaskUpdate", ["taskId": .string("1"), "status": .string("completed")])
        #expect(ToolRunSummary.chips([done]).first?.detail == "#1")
        #expect(ToolRunSummary.chips([done]).first?.truncatesTail == true)
    }
}
