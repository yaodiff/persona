// swift-tools-version: 6.0
// Persona Screen: the independent Link install's recording helper, not the retired menu bar app.
import PackageDescription

let package = Package(
    name: "PersonaScreen",
    defaultLocalization: "en",
    platforms: [.macOS(.v15)],
    dependencies: [
        // The live screen's wire protocol and capture decisions, shared with the iOS app (#3954).
        .package(path: "../Packages/PersonaScreenKit")
    ],
    targets: [
        // Persona Screen: the iOS live-screen video helper (#3954, docs/extensions/computer-use.md), the app bundle the
        // node launches, capturing with ScreenCaptureKit and encoding with VideoToolbox; mac-link.sh embeds it in
        // the independently installed Link bundle. Its codec and decisions are PersonaScreenKit.
        .executableTarget(name: "PersonaScreen", dependencies: [.product(name: "PersonaScreenKit", package: "PersonaScreenKit")],
                          path: "Sources/PersonaScreen")
    ],
    swiftLanguageModes: [.v5]
)
