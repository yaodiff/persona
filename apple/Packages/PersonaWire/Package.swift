// swift-tools-version: 6.2
import PackageDescription

// The wire layer every app target shares (#7895, stage 1 of #7891): JSONValue and the JSON date codec, and the extension
// protocol's generated types (scripts/gen/extension_schema.py writes Sources/PersonaWire/ExtensionWire.swift). Foundation only,
// so the watch, the widgets and the notification extensions link it too; its tests run with `swift test`.
let package = Package(
    name: "PersonaWire",
    platforms: [.iOS(.v26), .macOS(.v26), .visionOS(.v26), .watchOS(.v10)],
    products: [
        .library(name: "PersonaWire", targets: ["PersonaWire"]),
    ],
    targets: [
        .target(name: "PersonaWire"),
        .testTarget(name: "PersonaWireTests", dependencies: ["PersonaWire"]),
    ]
)
