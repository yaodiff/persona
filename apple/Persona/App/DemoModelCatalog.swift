// i18n-ignore-file: demo mode's made-up hub data (model settings)
import Foundation
import PersonaWire

/// Made-up model settings for Demo Mode; they never reach a provider or a computer.
enum DemoModelCatalog {
    static func configuration(tasks: [JSONValue], overrides: [String: JSONValue]) -> JSONValue {
        let providers: [JSONValue] = [
            ["id": "launcher.demo", "name": "演示对话", "kind": "launcher", "model": "model-chat"],
            ["id": "legacy.ai", "name": "演示 API", "kind": "api", "api": "messages", "model": "model-fast"]
        ]
        let definitions = [
            ("assistant", "Persona 对话", "对话与 Agent", "launcher"),
            ("inbox.agent", "AI 分身", "对话与 Agent", "launcher"),
            ("lark.agent", "飞书 /agent", "对话与 Agent", "launcher"),
            ("agent.new", "新建 Agent", "对话与 Agent", "launcher"),
            ("inbox.triage", "分拣与摘要", "收件箱", "api"),
            ("inbox.judge", "AI 分身判断", "收件箱", "api"),
            ("inbox.draft", "草拟回复", "收件箱", "api"),
            ("translate", "文本与邮件翻译", "翻译", "api"),
            ("kb", "知识处理默认模型", "知识处理", "api"),
            ("kb.compress", "会话压缩与打标", "知识处理", "api"),
            ("kb.project", "项目档案重写", "知识处理", "api"),
            ("kb.dedupe", "项目去重", "知识处理", "api"),
            ("kb.items", "事项提取与打标", "知识处理", "api"),
            ("kb.moments", "公司圈关联", "知识处理", "api"),
            ("assistant.title", "对话标题", "辅助功能", "api"),
            ("agent.title", "Agent 标签名", "辅助功能", "api"),
            ("schedule", "定时任务默认模型", "定时任务", "launcher")
        ] + tasks.compactMap { t -> (String, String, String, String)? in
            guard let id = t["id"]?.string else { return nil }
            return ("task.\(id)", t["title"]?.string ?? "跟进任务", "定时任务", "launcher")
        }
        let categories: [String: [String]] = [
            "assistant": ["与当前 persona 的直接对话", "定时任务"],
            "agent.new": ["新建 Agent", "AI 分身执行", "飞书 /agent"],
            "kb": ["文本与邮件翻译", "草拟回复", "知识整理与会话压缩"],
            "inbox.triage": ["收件箱分拣与摘要", "重要性判断"]
        ]
        let purposes = definitions.map { id, name, group, kind -> JSONValue in
            let saved = overrides[id]
            let binding = saved?["override"]?.object ?? [:]
            let providerID = kind == "api" ? "legacy.ai" : "launcher.demo"
            var effective: [String: JSONValue] = ["provider": .string(providerID), "model": .string(kind == "api" ? "model-fast" : "model-chat")]
            effective.merge(binding) { _, new in new }
            var purpose: [String: JSONValue] = [
                "id": .string(id), "name": .string(name), "group": .string(group), "kind": .string(kind),
                "override": .object(binding), "effective": .object(effective), "revision": saved?["revision"] ?? 0,
                "inherited_from": .string(binding.isEmpty ? "现有默认" : "此用途"), "status": "体验模式 · 不会调用模型",
                "fields": kind == "api" ? ["provider", "model"] : ["provider", "model", "effort"],
                "provider_ids": .array(providers.filter { $0["kind"]?.string == kind }.compactMap { $0["id"] })]
            // The four categories the hub edits (docs/assistant/model-config.md); every other use is set through its category.
            if let uses = categories[id] {
                purpose["category_id"] = .string(id)
                purpose["uses"] = .array(uses.map { ["name": .string($0)] })
            } else {
                purpose["category_id"] = .string(Self.category(id))
            }
            // Main Chat alone picks its How It Runs (#2455).
            if id == "assistant" {
                purpose["fields"] = ["provider", "model", "effort", "runtime"]
                purpose["runtimes"] = ["cli", "agentsdk"]
            }
            return .object(purpose)
        }
        return ["purposes": .array(purposes), "providers": .array(providers)]
    }

    /// The category a use is set through (the hub's modelCategoryID).
    private static func category(_ id: String) -> String {
        if id == "schedule" || id.hasPrefix("task.") { return "assistant" }
        if id == "inbox.agent" || id == "lark.agent" { return "agent.new" }
        if id == "inbox.judge" || id == "agent.triage" { return "inbox.triage" }
        return "kb"
    }
}
