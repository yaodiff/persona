// swift-tools-version: 6.0
import PackageDescription

// The isolation-aware lint for blocking calls on the main actor (docs/apple/performance.md, "Automated checks", #4262).
// swift-syntax is pinned to the release that matches the minimum compiler (Xcode 26.0, Swift 6.2).
let package = Package(
    name: "IsolationLint",
    platforms: [.macOS(.v13)],
    dependencies: [
        .package(url: "https://github.com/swiftlang/swift-syntax.git", exact: "602.0.0"),
    ],
    targets: [
        .target(name: "IsolationLintCore", dependencies: [
            .product(name: "SwiftSyntax", package: "swift-syntax"),
            .product(name: "SwiftParser", package: "swift-syntax"),
        ]),
        .executableTarget(name: "isolation-lint", dependencies: ["IsolationLintCore"]),
        .testTarget(name: "IsolationLintTests", dependencies: ["IsolationLintCore"]),
    ]
)
