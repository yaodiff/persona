// swift-tools-version: 6.2
// PersonaScreenKit: the live-screen video channel's wire protocol and the capture decisions (#3954,
// docs/extensions/computer-use.md "Live screen video (#3954)"). Pure Swift with Foundation only, so the same codec runs
// in the Mac's Persona Screen helper (apple/PersonaLink) and in the iOS app's video client. The watch and the widgets
// don't use it.
import PackageDescription

let package = Package(
    name: "PersonaScreenKit",
    platforms: [.macOS(.v15), .iOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "PersonaScreenKit", targets: ["PersonaScreenKit"])
    ],
    targets: [
        .target(name: "PersonaScreenKit", path: "Sources/PersonaScreenKit"),
        .testTarget(name: "PersonaScreenKitTests", dependencies: ["PersonaScreenKit"], path: "Tests/PersonaScreenKitTests")
    ],
    swiftLanguageModes: [.v5]
)
