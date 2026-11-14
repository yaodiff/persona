// swift-tools-version: 6.0
// screenbench: the live screen's encoding bench (#6396). It renders synthetic Mac screens (text, typing, scrolling, a
// video), scales them to each quality tier and encodes them with Persona Screen's VideoToolbox settings, then decodes
// and compares: bytes a second, SSIM against what was encoded and against the full-size screen, and what idle refinement
// adds. No capture, so it needs no Screen Recording permission. A tool only: nothing ships with it.
import PackageDescription

let package = Package(
    name: "screenbench",
    platforms: [.macOS(.v15)],
    dependencies: [
        .package(path: "../../../apple/Packages/PersonaScreenKit")
    ],
    targets: [
        .executableTarget(name: "screenbench", dependencies: [.product(name: "PersonaScreenKit", package: "PersonaScreenKit")],
                          path: "Sources/screenbench")
    ],
    swiftLanguageModes: [.v5]
)
