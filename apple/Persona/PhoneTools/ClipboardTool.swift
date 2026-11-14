import Foundation
#if canImport(UIKit)
import UIKit
#endif
import PersonaWire

/// The part of the general pasteboard the clipboard tools use: `UIPasteboard` in the app, a stand-in in tests.
@MainActor protocol ClipboardBoard: AnyObject {
    var string: String? { get set }
    var hasStrings: Bool { get }
    var changeCount: Int { get }
}

extension UIPasteboard: ClipboardBoard {}

/// Clipboard tools. iOS shows a system "Allow Paste?" prompt the first time the
/// app reads the pasteboard with content copied by another app; the user must
/// tap Allow for the read to succeed.
///
/// iOS gives the general pasteboard only to the app on screen (#4192): in the background (woken by the hub's silent
/// push, or a connection still open from an earlier wake) a write is dropped and a read comes back empty. So both tools
/// refuse unless Persona is the active app, with the wording of the other tools that need it in front (`Foreground`),
/// and a write answers ok only once the pasteboard shows it landed.
enum ClipboardTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_clipboard_get",
                    description: """
                    Read the phone's clipboard as text. iOS may show an "Allow Paste?" prompt the \
                    first time, which the user has to confirm. Returns "text" and its character count, \
                    or text: null when the clipboard is empty or holds something that is not text.
                    """,
                    inputSchema: Schema.object([:])
                ),
                source: .clipboard,
                run: { _ in try get() }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_clipboard_set",
                    description: """
                    Replace the phone's clipboard with the given text, for example a link or a draft \
                    the user asked for. Overwrites whatever was there.
                    """,
                    inputSchema: Schema.object([
                        "text": Schema.string("Text to place on the clipboard."),
                    ], required: ["text"])
                ),
                source: .clipboard,
                run: { args in try set(args) }
            ),
        ]
    }

    /// Whether Persona may use the general pasteboard now: on an iPhone only as the active app. A Mac and Vision Pro
    /// have no such rule the tools know of.
    static var inFront: Bool {
        #if os(iOS)
        Foreground.isActive
        #else
        true
        #endif
    }

    static func get(board: any ClipboardBoard = UIPasteboard.general, inFront: Bool = ClipboardTool.inFront) throws -> [ToolContent] {
        try requireFront(inFront)
        let text = board.string
        if let text, !text.isEmpty {
            return Output.json(Output.object([
                ("text", .string(text)),
                ("characters", .number(Double(text.count))),
            ]))
        }
        let hint = board.hasStrings
            ? "The clipboard holds a string the app is not allowed to read yet. The user may have to tap Allow on the paste prompt."
            : "The clipboard is empty or does not hold text."
        return Output.json(Output.object([
            ("text", .null),
            ("note", .string(hint)),
        ]))
    }

    static func set(_ args: JSONValue, board: any ClipboardBoard = UIPasteboard.general, inFront: Bool = ClipboardTool.inFront) throws -> [ToolContent] {
        // Keep an intentionally empty string writable (clear the clipboard).
        let text: String
        if let raw = args["text"]?.string {
            text = raw
        } else {
            throw ToolFailure(.invalidArgument, "text is required")
        }
        try requireFront(inFront)
        let before = board.changeCount
        board.string = text
        // A write iOS took moves the change count, and leaves a string unless it cleared the clipboard.
        guard board.changeCount != before, text.isEmpty || board.hasStrings else {
            throw ToolFailure(.unavailable, "The text did not reach the iPhone's clipboard: iOS did not take the write. "
                + "Nothing was copied. Ask the user to open Persona and try again.")
        }
        return Output.json(Output.object([
            ("ok", .bool(true)),
            ("characters", .number(Double(text.count))),
        ]))
    }

    private static func requireFront(_ inFront: Bool) throws {
        guard inFront else {
            throw ToolFailure(.unavailable, "Persona is not open on the iPhone, so it cannot use the clipboard: iOS lets only "
                + "the app on screen read or write it. Ask the user to open Persona and try again.")
        }
    }
}
