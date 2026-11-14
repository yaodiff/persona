import Foundation
import Testing
@testable import Persona

/// The core is an iOS binding, not another Shared dependency. This source-level scope check works without linking
/// any excluded target, so watch/widgets/Vision/Persona Link cannot acquire it through a convenience package edit.
struct PersonaCoreIntegrationTests {
    private let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
    private let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent()


    @Test func onlyTheIPhoneAppAndItsTestsLinkTheCore() throws {
        let project = try String(contentsOf: apple.appending(path: "project.yml"), encoding: .utf8)
        for target in ["PersonaVision", "PersonaWatch", "PersonaWidgets", "PersonaNotify", "PersonaShare",
                       "PersonaVisionWidgets", "PersonaVisionShare", "PersonaVisionNotify", "PersonaUITests"] {
            let block = try #require(project.components(separatedBy: "\n  \(target):").dropFirst().first?.components(separatedBy: "\n  Persona").first)
            #expect(!block.contains("package: PersonaCore"), "\(target) must not link PersonaCore")
        }
        for folder in ["Shared", "Vision", "PersonaWatch", "PersonaWidgets", "LiveActivity", "PersonaLink"] {
            let files = try FileManager.default.subpathsOfDirectory(atPath: apple.appending(path: folder).path)
            for file in files where file.hasSuffix(".swift") {
                let source = try String(contentsOf: apple.appending(path: folder).appending(path: file), encoding: .utf8)
                #expect(!source.contains("import PersonaCore"), "\(folder)/\(file) must stay independent")
            }
        }
        let link = try String(contentsOf: repo.appending(path: "apple/PersonaLink/Package.swift"), encoding: .utf8)
        #expect(!link.contains("PersonaCore"))
    }
}
