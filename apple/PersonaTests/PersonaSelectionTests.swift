import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// Package selection and retired drawing compatibility, run against both native apps.
@MainActor struct PersonaSelectionTests {
    private nonisolated static let packIDs = ["nwverse", "demo", "extension-look"]

    nonisolated static let removed = ["persona-visor", "persona-circuit", "persona-pixel", "persona-facet", "persona-blob", "spark", "halo"] + LegacyNames.retiredLookIDs + Array(LegacyNames.looks.keys)

    private final class Icons: AppIconSetting {
        var supportsAlternateIcons = true
        var alternateIconName: String?
        var calls: [String?] = []
        init(_ name: String?) { alternateIconName = name }
        func setAlternateIconName(_ name: String?) async throws {
            calls.append(name)
            alternateIconName = name
        }
    }

    private func info(_ id: String, rev: String = "1") -> PersonaInfo {
        let manifest = PersonaManifest(format: 1, id: id, name: "Mint", avatar: .init(sizes: ["128": "avatar.png"]),
                                       dot: .init(states: ["idle": .init(file: "idle.png")]))
        return PersonaInfo(id: id, rev: rev, bytes: 1, manifest: manifest)
    }

    @Test(arguments: removed) func legacySelectionFallsBackWithoutErasingTheOldPreference(_ id: String) async throws {
        let suite = "persona-selection-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        let root = FileManager.default.temporaryDirectory.appending(path: suite)
        defer {
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: root)
        }
        defaults.set(id, forKey: PersonaStore.chosenKey)
        let store = PersonaStore(root: root, defaults: defaults)
        #expect(store.chosenID == "persona" && store.current.id == "persona")
        #expect(store.current.avatarDesign == .mochi && store.appIcon == .standard)
        #expect(AppearanceChoice.resolve(style: nil, color: nil, persona: id).color == .style)
        #expect(!WatchPet.supports(id))
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == id)

        let icons = Icons("AppIcon-Visor")
        let model = AppIconModel(system: icons)
        await model.follow(store.appIcon, scheme: .system)
        await model.follow(store.appIcon, scheme: .system)
        #expect(icons.calls.count == 1 && icons.calls[0] == nil)
        #expect(icons.alternateIconName == nil && model.current == .standard)
    }

    @Test(arguments: ["AppIcon-Visor", "AppIcon-Circuit", "AppIcon-Pixel", "AppIcon-Facet", "AppIcon-Blob"])
    func removedAlternateIconIsExplicitlyReset(_ name: String) async {
        let icons = Icons(name)
        let model = AppIconModel(system: icons)
        await model.follow(nil, scheme: .system)
        #expect(icons.calls.count == 1 && icons.calls[0] == nil)
        #expect(icons.alternateIconName == nil)
    }

    private func list(_ entries: [PersonaInfo], on hub: FakeHub) throws {
        let data = try JSONEncoder().encode(entries)
        hub.on("GET", "/api/v1/personas", json: "{\"personas\":\(String(decoding: data, as: UTF8.self))}")
        for entry in entries {
            for file in entry.manifest.files {
                hub.on("GET", "/api/v1/personas/\(entry.id)/\(file)", json: "fixture")
            }
        }
    }

    @Test func cachedAndLiveCatalogsValidateEveryPackage() async throws {
        let suite = "persona-selection-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        let root = FileManager.default.temporaryDirectory.appending(path: suite)
        defer {
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: root)
        }
        var mismatch = info("mismatch")
        mismatch.manifest.id = "another-id"
        var futureFormat = info("future-format")
        futureFormat.manifest.format = 2
        var unsafe = info("unsafe")
        unsafe.manifest.avatar.sizes = ["128": "../avatar.png"]
        var missingIdle = info("missing-idle")
        missingIdle.manifest.dot.states = [:]
        let entries = Self.packIDs.map { info($0) } + [mismatch, futureFormat, unsafe, missingIdle]
            + Self.removed.map { info($0) } + [info("persona")]
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try JSONEncoder().encode(entries).write(to: root.appending(path: "index.json"))
        let store = PersonaStore(root: root, defaults: defaults)
        #expect(store.offered.map(\.id) == ["persona"] + Self.packIDs)

        let hub = FakeHub()
        try list(entries, on: hub)
        await store.refresh(hub.client)
        #expect(store.offered.map(\.id) == ["persona"] + Self.packIDs)
        let cached = try JSONDecoder().decode([PersonaInfo].self, from: Data(contentsOf: root.appending(path: "index.json")))
        #expect(cached.map(\.id) == Self.packIDs)
        for id in Self.packIDs {
            await store.choose(try #require(store.offered.first { $0.id == id }), api: hub.client)
            #expect(store.chosenID == id && store.current.id == id && store.appIcon == .standard)
            #expect(PersonaStore(root: root, defaults: defaults).current.id == id)
        }
        #expect(PersonaStore(root: root, defaults: defaults).offered.map(\.id) == ["persona"] + Self.packIDs)
        await store.choose(Persona(info: mismatch), api: hub.client)
        #expect(store.chosenID == Self.packIDs.last)
        #expect(hub.sent("GET", "/api/v1/personas/mismatch/avatar.png").isEmpty)
    }

    @Test(arguments: packIDs) func missingPackageKeepsItsSelectionAndResumesWhenItReturns(_ id: String) async throws {
        let suite = "persona-selection-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        let root = FileManager.default.temporaryDirectory.appending(path: suite)
        defer {
            defaults.removePersistentDomain(forName: suite)
            try? FileManager.default.removeItem(at: root)
        }
        let hub = FakeHub()
        try list([info(id)], on: hub)
        let store = PersonaStore(root: root, defaults: defaults)
        await store.refresh(hub.client)
        await store.choose(try #require(store.offered.first { $0.id == id }), api: hub.client)
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == id)
        #expect(PersonaStore(root: root, defaults: defaults).current.id == id)

        try list([], on: hub)
        await store.refresh(hub.client)
        #expect(store.current.id == "persona" && store.chosenID == id)
        let relaunched = PersonaStore(root: root, defaults: defaults)
        #expect(relaunched.current.id == "persona" && relaunched.chosenID == id)
        #expect(defaults.string(forKey: PersonaStore.chosenKey) == id)

        try list([info(id, rev: "2")], on: hub)
        await relaunched.refresh(hub.client)
        #expect(relaunched.current.id == id && relaunched.current.rev == "2")
        #expect(relaunched.isOnPhone(relaunched.current))
        #expect(PersonaStore(root: root, defaults: defaults).current == relaunched.current)
    }

    @Test(arguments: packIDs + ["temporarily-missing"]) func packageAppearanceDefaultsAndWatchPetGating(_ id: String) {
        #expect(PersonaSelection.resolve(id) == id)
        #expect(AppearanceChoice.resolve(style: nil, color: nil, persona: id).color == .persona)
        #expect(AppearanceChoice.resolve(style: "claude", color: nil, persona: id).color == .persona)
        #expect(AppearanceChoice.resolve(style: nil, color: "coral", persona: id).color == .coral)
        #expect(AppearanceChoice.resolve(style: nil, color: "style", persona: id).color == .style)
        #expect(WatchPet.supports(id) == (id == "nwverse"))
    }

    @Test func retainedIDsAndDrawingVocabulary() {
        #expect(PersonaSelection.resolve(nil) == "persona")
        #expect(PersonaSelection.resolve("") == "persona")
        #expect(!WatchPet.supports(nil) && !WatchPet.supports("persona"))
        #expect(PersonaSelection.resolve("persona") == "persona")
        #expect(PersonaSelection.resolve("nwverse") == "nwverse")
        #expect(Persona.builtins.map(\.id) == ["persona"])
        #expect(AssistantAvatar.Design.allCases.map(\.rawValue) == ["mochi"])
        #expect(ExtensionBuiltinLook.all == ["persona"])
        #expect(AppearanceChoice.resolve(style: nil, color: nil, persona: "nwverse").color == .persona)
    }
}
