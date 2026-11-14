#if DEBUG && (os(iOS) || os(visionOS))
import Foundation
import PersonaWire

/// The runtime CLI over the hub (docs/operations/runtime-cli.md「Reaching a real device through the hub」), for a DEBUG build on a real phone: launched with
/// `-runtimeCLIHub YES`, the app lists `phone_runtime_cli` with its tools in `register_phone`, but only when the hub
/// says it keeps that tool out of the agents' tools (`runtime_cli` in its features: an older hub would offer it to
/// them). The hub's `POST /api/v1/phone/runtime-cli` then comes as a tool call `{cmd, args}`, run by the same commands
/// as the local port (`RuntimeCLI.run`). Off by default; not in Release builds.
extension RuntimeCLI {
    static let hubTool = "phone_runtime_cli"
    /// The hub's answers are WebSocket messages: past this the call fails with the size (a snapshot as JPEG, at a
    /// smaller `scale`, fits).
    static let hubReplyLimit = 16 << 20

    static var overHub: Bool { UserDefaults.standard.bool(forKey: "runtimeCLIHub") }

    /// What `register_phone` adds: only with the switch on and a hub that keeps it from the agents.
    static func hubToolSpecs(features: Set<String>) -> [ToolSpec] {
        guard overHub, features.contains("runtime_cli") else { return [] }
        return [ToolSpec(name: hubTool, description: "DEBUG builds only: the runtime CLI (docs/operations/runtime-cli.md), for the user's own scripts.",
                         inputSchema: .object(["type": .string("object"), "properties": .object([
                             "cmd": .object(["type": .string("string")]), "args": .object(["type": .string("object")]),
                         ])]))]
    }

    /// A tool call from the hub: `{cmd, args}` → the command's answer as JSON text.
    static func runFromHub(_ input: JSONValue) async -> (text: String, isError: Bool) {
        guard overHub else { return ("the runtime CLI over the hub is off (-runtimeCLIHub YES)", true) }
        guard let data = try? JSON.encoder.encode(input),
              let call = (try? JSONSerialization.jsonObject(with: data)) as? Args,
              let cmd = call["cmd"] as? String else { return ("want {cmd, args}", true) }
        let reply = encode(await run(cmd, call["args"] as? Args ?? [:]))
        guard reply.count <= hubReplyLimit else {
            return ("the answer is \(reply.count) bytes, over \(hubReplyLimit): ask for less (snapshot format=jpeg, a smaller scale)", true)
        }
        return (String(decoding: reply, as: UTF8.self), false)
    }
}
#endif
