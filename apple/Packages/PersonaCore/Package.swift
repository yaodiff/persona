// swift-tools-version: 6.2
import PackageDescription

// The shared Rust core's Apple binding. Only the iOS app and its tests link this package; watch, widgets, Vision and
// Persona Link do not. Run native/app-core/scripts/build-apple.sh before `swift test` or
// xcodebuild: the XCFramework is an ignored build product, never a committed binary.
//
// `PersonaCoreTestSupport` reads the shared JSON fixtures from the source tree and replays them through the C binding.
// Only `PersonaCoreTests` and the iOS `PersonaTests` link it, never the app: its file reads and directory listings stay
// out of the shipped `PersonaCore` library.
let package = Package(
    name: "PersonaCore",
    platforms: [.iOS(.v26), .macOS(.v26)],
    products: [
        .library(name: "PersonaCore", targets: ["PersonaCore"]),
        .library(name: "PersonaCoreTestSupport", targets: ["PersonaCoreTestSupport"]),
    ],
    targets: [
        .binaryTarget(name: "PersonaCoreC", path: "build/PersonaCoreC.xcframework"),
        .target(name: "PersonaCoreSerde", path: "Sources/Serde"),
        .target(
            name: "PersonaCoreWire",
            dependencies: ["PersonaCoreSerde"],
            path: "Sources/PersonaCoreWire"
        ),
        .target(
            name: "PersonaCore",
            dependencies: ["PersonaCoreC", "PersonaCoreWire", "PersonaCoreSerde"],
            path: "Sources/PersonaCore",
            swiftSettings: [
                .swiftLanguageMode(.v6),
                .defaultIsolation(nil),
                .enableExperimentalFeature("StrictConcurrency"),
                .unsafeFlags(["-warnings-as-errors"]),
            ]
        ),
        .target(
            name: "PersonaCoreTestSupport",
            dependencies: ["PersonaCore"],
            path: "Tests/PersonaCoreTestSupport",
            swiftSettings: [
                .swiftLanguageMode(.v6),
                .defaultIsolation(nil),
                .enableExperimentalFeature("StrictConcurrency"),
                .unsafeFlags(["-warnings-as-errors"]),
            ]
        ),
        .testTarget(
            name: "PersonaCoreTests",
            dependencies: ["PersonaCore", "PersonaCoreTestSupport"],
            path: "Tests/PersonaCoreTests",
            swiftSettings: [
                .swiftLanguageMode(.v6),
                .defaultIsolation(nil),
                .enableExperimentalFeature("StrictConcurrency"),
                .unsafeFlags(["-warnings-as-errors"]),
            ]
        ),
    ]
)
