// swift-tools-version: 6.2
import PackageDescription

// The app's SSH tunnel to its hub (docs/operations/network.md「Connecting to hub through SSH」): an in-process SSH client (swift-nio-ssh over
// Network.framework) that listens on the device's loopback and carries each connection made there to the hub through
// one direct-tcpip channel. Shared by the iOS and Vision apps; the watch does not link it.
let package = Package(
    name: "PersonaSSH",
    platforms: [.iOS(.v26), .macOS(.v26), .visionOS(.v26)],
    products: [
        .library(name: "PersonaSSH", targets: ["PersonaSSH"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-nio-ssh.git", from: "0.15.1"),
        .package(url: "https://github.com/apple/swift-nio.git", from: "2.81.0"),
        .package(url: "https://github.com/apple/swift-nio-transport-services.git", from: "1.25.0"),
    ],
    targets: [
        .target(name: "PersonaSSH", dependencies: [
            .product(name: "NIOSSH", package: "swift-nio-ssh"),
            .product(name: "NIOCore", package: "swift-nio"),
            .product(name: "NIOTransportServices", package: "swift-nio-transport-services"),
        ]),
        .testTarget(name: "PersonaSSHTests", dependencies: ["PersonaSSH"]),
    ]
)
