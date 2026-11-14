// swift-tools-version: 6.2
// PersonaInputKit: the live screen's pixel scroll record (#7775, docs/extensions/computer-use.md "Pixel scrolling"),
// shared by the Mac's Persona Input helper (apple/PersonaLink/Input) and the iOS app. Pure Swift with Foundation only.
// Its own package, not PersonaScreenKit: Persona Screen links that one, and a change to its binary would ask the user to
// allow Screen Recording again.
import PackageDescription

let package = Package(
    name: "PersonaInputKit",
    platforms: [.macOS(.v15), .iOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "PersonaInputKit", targets: ["PersonaInputKit"])
    ],
    targets: [
        .target(name: "PersonaInputKit", path: "Sources/PersonaInputKit"),
        .testTarget(name: "PersonaInputKitTests", dependencies: ["PersonaInputKit"], path: "Tests/PersonaInputKitTests")
    ]
)
