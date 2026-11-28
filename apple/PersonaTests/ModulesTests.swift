import BeautifulUI
import Foundation
import PersonaModuleAPI
import Testing
@testable import Persona

/// The module list and its switch (#13432, docs/apple/ios.md "Modules"): a module that is switched off leaves no entry in
/// any of the composer's attach sources, and one that is on appears where Glasses Photo always has.
@MainActor
struct ModulesTests {
    private func registry(glasses: Bool) -> ModuleRegistry<PersonaHostApp> {
        ModuleRegistry(modules: Modules.builtIn, isEnabled: { glasses && $0.descriptor.id == "glasses" })
    }

    @Test func theListRegistersWithoutProblems() {
        let all = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { _ in true })
        #expect(all.problems.isEmpty)
        #expect(all.active.map(\.id) == ["glasses", "screen-share", "health", "voice"])
        #expect(Modules.registry.problems.isEmpty)
    }

    /// This build has no Meta Wearables SDK (CI never has it): glasses are off unless a DEBUG switch says otherwise.
    @Test func theDefaultFollowsTheBuild() {
        let built = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn)
        #expect(built.isActive("glasses") == MetaGlasses.isBuiltIn)
    }

    @Test func glassesOffLeavesNoAttachEntry() {
        let off = registry(glasses: false)
        #expect(off.composerActions.isEmpty)
        #expect(off.composerAction(id: "glasses") == nil)
        let sources = PromptItems.attachments(camera: true, modules: off.composerActions).map(\.id)
        #expect(sources == ["photo", "camera", "file"])
        let rows = AttachMoreSection.common(camera: {}, scan: {}, modules: off.composerActions, open: { _ in }, mention: {}).flatMap(\.rows).map(\.id)
        #expect(!rows.contains("attach-glasses"))
        #expect(rows == ["composer-menu-camera", "composer-menu-scan", "composer-menu-mention"])
    }

    @Test func glassesOnAddsTheEntryWhereItWas() {
        let on = registry(glasses: true)
        #expect(on.composerActions.map(\.id) == ["glasses"])
        let sources = PromptItems.attachments(camera: true, modules: on.composerActions)
        #expect(sources.map(\.id) == ["photo", "camera", "glasses", "file"])
        if case .symbol(let name) = sources[2].icon { #expect(name == Theme.Icon.glasses) } else { Issue.record("not a symbol") }
        #expect(sources[2].name == String(localized: "Glasses Photo"))
        var opened: [String] = []
        let rows = AttachMoreSection.common(camera: {}, scan: {}, modules: on.composerActions, open: { opened.append($0.id) }, mention: {}).flatMap(\.rows)
        #expect(rows.map(\.id) == ["composer-menu-camera", "composer-menu-scan", "attach-glasses", "composer-menu-mention"])
        let glasses = rows[2]
        #expect(glasses.title == String(localized: "Glasses Photo"))
        #expect(glasses.symbol == Theme.Icon.glasses)
        glasses.action()
        #expect(opened == ["glasses"])
    }

    @Test func glassesOffHandlesNoURL() {
        let url = URL(string: "persona://?metaWearablesAction=register")!
        #expect(!registry(glasses: false).handle(url))
    }

    // MARK: The hub's module flags (#13849): `modules` marks a hub that publishes them, `module.<id>` keeps a module on.

    /// A UserDefaults suite of its own, removed afterwards.
    private func withDefaults(_ body: (UserDefaults) -> Void) {
        let name = "ModulesTests.\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        defer { d.removePersistentDomain(forName: name) }
        body(d)
    }

    private func state(_ group: String, _ d: UserDefaults) -> ModuleState {
        ModuleState(group: group, defaults: d, isEnabled: { $0.descriptor.id == "glasses" })
    }

    // Screen share (#14017): the core reaches it only through the registry's places, its settings entry and the
    // `LiveScreen` capability, so with it off there is nothing to draw and nothing to open.
    @Test func screenShareOffLeavesNoEntry() {
        let off = registry(glasses: false)
        #expect(!off.isActive("screen-share"))
        for place in [PersonaHostApp.Place.chatMenu, .chatFooter, .toolCall] { #expect(off.views(at: place).isEmpty) }
        #expect(off.settingsEntry(id: "live-screen") == nil)
        #expect(off.capability((any LiveScreen).self) == nil)
        #expect(off.audioLease(id: "screen-share.pip") == nil)
    }

    @Test func screenShareOnServesItsPlaces() {
        let on = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { $0.descriptor.id == "screen-share" })
        #expect(on.problems.isEmpty)
        for place in [PersonaHostApp.Place.chatMenu, .chatFooter, .toolCall] {
            #expect(on.views(at: place).map(\.module) == ["screen-share"])
        }
        #expect(on.settingsEntry(id: "live-screen")?.module == "screen-share")
        #expect(on.capability((any LiveScreen).self) != nil)
        #expect(on.audioLease(id: "screen-share.pip") == ModuleAudioLease(id: "screen-share.pip", module: "screen-share", kind: .playback))
    }

    @Test func screenShareFollowsItsHubFlag() {
        #expect(ScreenShareModule().descriptor.feature == "module.screen-share")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "screen-share" })
            s.heard(["file_upload"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("screen-share"), "an older hub leaves it on")
            s.heard(["modules", "module.screen-share"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("screen-share"))
            s.heard(["modules"], from: "home", isHub: true, defaults: d)
            #expect(!s.registry.isActive("screen-share"))
            #expect(s.registry.offByHub == ["screen-share"])
            #expect(s.registry.views(at: .chatMenu).isEmpty && s.registry.capability((any LiveScreen).self) == nil)
        }
    }

    // Health (#14278): the core reaches its home only through the shell page `health`, so with it off the sidebar has
    // no row and no route opens a page.
    @Test func healthOffLeavesNoPage() {
        let off = registry(glasses: false)
        #expect(!off.isActive("health"))
        #expect(off.shellPage(id: SidebarRoute.healthPage) == nil)
    }

    @Test func healthOnServesItsPage() {
        let on = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { $0.descriptor.id == "health" })
        #expect(on.problems.isEmpty)
        #expect(on.shellPages.map(\.id) == [SidebarRoute.healthPage])
        #expect(on.shellPage(id: SidebarRoute.healthPage)?.module == "health")
    }

    @Test func healthFollowsItsHubFlag() {
        #expect(HealthModule().descriptor.feature == "module.health")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "health" })
            s.heard(["file_upload"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("health"), "an older hub leaves it on")
            s.heard(["modules", "module.health"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.shellPage(id: SidebarRoute.healthPage) != nil)
            s.heard(["modules", "module.screen-share"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.offByHub == ["health"])
            #expect(s.registry.shellPage(id: SidebarRoute.healthPage) == nil)
        }
    }

    // Voice (#13431): the core reaches the conversation's voice only through `ConversationVoice`, so with it off the
    // page has no call, no bar and no captions; the composer's dictation is the core's and stays.
    @Test func voiceOffProvidesNoConversationVoice() {
        let off = registry(glasses: false)
        #expect(!off.isActive("voice"))
        #expect(off.capability((any ConversationVoice).self) == nil)
    }

    @Test func voiceOnProvidesConversationVoice() {
        let on = ModuleRegistry<PersonaHostApp>(modules: Modules.builtIn, isEnabled: { $0.descriptor.id == "voice" })
        #expect(on.problems.isEmpty)
        #expect(on.capability((any ConversationVoice).self) === ConversationVoiceState.shared)
    }

    @Test func voiceFollowsItsHubFlag() {
        #expect(VoiceModule().descriptor.feature == "module.voice")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "voice" })
            s.heard(["file_upload"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("voice"), "an older hub leaves it on")
            s.heard(["modules", "module.voice"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.capability((any ConversationVoice).self) != nil)
            s.heard(["modules", "module.health"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.offByHub == ["voice"])
            #expect(s.registry.capability((any ConversationVoice).self) == nil)
            s.heard(["modules", "module.voice"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.capability((any ConversationVoice).self) != nil, "back on with the flag")
        }
    }

    // Recording (#15214): not a packaged module on Apple yet, so the registry lists none; its allowance is read from the
    // same hub answer by the same rule, and follows each hello.
    @Test func recordingFollowsItsHubFlag() {
        #expect(RecordingModule.descriptor.feature == "module.recording")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "voice" })
            @MainActor func allowance() -> RecordingAllowance { RecordingAllowance(hub: s.registry.hub) }
            #expect(allowance() == .on, "nothing heard yet")
            s.heard(["file_upload"], from: "home", isHub: true, defaults: d)
            #expect(allowance() == .on, "an older hub leaves it on")
            s.heard(["modules", "module.recording"], from: "home", isHub: true, defaults: d)
            #expect(allowance() == .on)
            s.heard(["modules", "module.voice"], from: "home", isHub: true, defaults: d)
            #expect(allowance() == .off)
            #expect(s.registry.offByHub.isEmpty, "recording is no listed module: voice stays on, and nothing else is turned off")
            #expect(s.registry.isActive("voice"))
            s.heard(["modules", "module.voice", "module.recording"], from: "home", isHub: true, defaults: d)
            #expect(allowance() == .on, "back on with the flag")
            // Another hub's hello changes nothing until that hub is the one followed.
            s.heard(["modules"], from: "work", isHub: false, defaults: d)
            #expect(allowance() == .on)
            s.follow("work", features: [], defaults: d)
            #expect(allowance() == .off)
            // The remembered answer, before the next launch's hello.
            #expect(RecordingAllowance(hub: HubModuleMemory.remembered("work", in: d)) == .off)
            #expect(RecordingAllowance(hub: HubModuleMemory.remembered("home", in: d)) == .on)
        }
    }

    /// Lark is no packaged Apple module either: the Lark bot's entry points follow `module.lark` by the same rule (#15995).
    @Test func larkFollowsItsHubFlag() {
        #expect(LarkModule.descriptor.feature == "module.lark")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "voice" })
            @MainActor func entry() -> LarkBotEntry { LarkBotEntry(hub: s.registry.hub) }
            #expect(entry() == .offered, "nothing heard yet")
            s.heard(["file_upload", "lark"], from: "home", isHub: true, defaults: d)
            #expect(entry() == .offered, "an older hub leaves it offered")
            s.heard(["modules", "module.lark"], from: "home", isHub: true, defaults: d)
            #expect(entry() == .offered)
            s.heard(["modules", "module.voice", "lark"], from: "home", isHub: true, defaults: d)
            #expect(entry() == .moduleOff, "the Lark extension's `lark` is not the module's flag")
            #expect(s.registry.offByHub.isEmpty, "lark is no listed module: voice stays on, and nothing else is turned off")
            #expect(s.registry.isActive("voice"))
            s.heard(["modules", "module.voice", "module.lark"], from: "home", isHub: true, defaults: d)
            #expect(entry() == .offered, "back with the flag")
            // Another hub's hello changes nothing until that hub is the one followed.
            s.heard(["modules"], from: "work", isHub: false, defaults: d)
            #expect(entry() == .offered)
            s.follow("work", features: [], defaults: d)
            #expect(entry() == .moduleOff)
            // The remembered answer, before the next launch's hello.
            #expect(LarkBotEntry(hub: HubModuleMemory.remembered("work", in: d)) == .moduleOff)
            #expect(LarkBotEntry(hub: HubModuleMemory.remembered("home", in: d)) == .offered)
        }
    }

    /// Mail is no packaged Apple module either: what mail offers follows `module.mail` by the same rule (#15995).
    @Test func mailFollowsItsHubFlag() {
        #expect(MailModule.descriptor.feature == "module.mail")
        withDefaults { d in
            let s = ModuleState(group: "home", defaults: d, isEnabled: { $0.descriptor.id == "voice" })
            @MainActor func access() -> MailAccess { MailAccess(hub: s.registry.hub) }
            #expect(access() == .live, "nothing heard yet")
            s.heard(["file_upload", "connectors"], from: "home", isHub: true, defaults: d)
            #expect(access() == .live, "an older hub leaves it live")
            s.heard(["modules", "module.mail"], from: "home", isHub: true, defaults: d)
            #expect(access() == .live)
            s.heard(["modules", "module.voice", "module.lark"], from: "home", isHub: true, defaults: d)
            #expect(access() == .storedOnly)
            #expect(s.registry.offByHub.isEmpty, "mail is no listed module: voice stays on, and nothing else is turned off")
            #expect(s.registry.isActive("voice"))
            s.heard(["modules", "module.voice", "module.mail"], from: "home", isHub: true, defaults: d)
            #expect(access() == .live, "back with the flag")
            // Another hub's hello changes nothing until that hub is the one followed.
            s.heard(["modules"], from: "work", isHub: false, defaults: d)
            #expect(access() == .live)
            s.follow("work", features: [], defaults: d)
            #expect(access() == .storedOnly)
            // The remembered answer, before the next launch's hello.
            #expect(MailAccess(hub: HubModuleMemory.remembered("work", in: d)) == .storedOnly)
            #expect(MailAccess(hub: HubModuleMemory.remembered("home", in: d)) == .live)
        }
    }

    @Test func glassesAreGatedByTheirHubFlag() {
        #expect(GlassesModule().descriptor.feature == "module.glasses")
    }

    @Test func anOlderHubKeepsGlassesOn() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["file_upload", "terminal_stream"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("glasses"))
            #expect(s.registry.composerActions.map(\.id) == ["glasses"])
            #expect(d.object(forKey: "hubModules.home") == nil) // nothing to remember for an older hub
        }
    }

    @Test func aPublishingHubWithTheFlagKeepsGlassesOn() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["modules", "module.glasses"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.isActive("glasses"))
        }
    }

    @Test func aPublishingHubWithoutTheFlagTurnsGlassesOff() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["modules", "module.health"], from: "home", isHub: true, defaults: d)
            #expect(!s.registry.isActive("glasses"))
            #expect(s.registry.offByHub == ["glasses"])
            let sources = PromptItems.attachments(camera: true, modules: s.registry.composerActions).map(\.id)
            #expect(sources == ["photo", "camera", "file"])
            #expect(!s.registry.handle(URL(string: "persona://?metaWearablesAction=register")!))
            // And on again with a later hello.
            s.heard(["modules", "module.glasses"], from: "home", isHub: true, defaults: d)
            #expect(s.registry.composerActions.map(\.id) == ["glasses"])
        }
    }

    @Test func theRememberedAnswerHoldsBeforeTheHello() {
        withDefaults { d in
            state("home", d).heard(["modules"], from: "home", isHub: true, defaults: d)
            #expect(d.stringArray(forKey: "hubModules.home") == [])
            // The next launch, before any hello: off, as the hub said last time.
            let next = state("home", d)
            #expect(!next.registry.isActive("glasses"))
            // A hub never heard from: on.
            #expect(state("other", d).registry.isActive("glasses"))
            // A hello with no features says nothing: the remembered answer stays.
            next.heard([], from: "home", isHub: true, defaults: d)
            #expect(!next.registry.isActive("glasses"))
            #expect(d.stringArray(forKey: "hubModules.home") == [])
        }
    }

    @Test func anotherHubsHelloIsKeptButNotApplied() {
        withDefaults { d in
            let s = state("home", d)
            s.heard(["modules"], from: "work", isHub: false, defaults: d)
            #expect(s.registry.isActive("glasses"))
            // Picking that hub applies what it said.
            s.follow("work", features: [], defaults: d)
            #expect(!s.registry.isActive("glasses"))
            s.follow("home", features: [], defaults: d)
            #expect(s.registry.isActive("glasses"))
        }
    }

    @Test func theAnswerIsWrittenOnlyWhenItChanges() {
        withDefaults { d in
            #expect(HubModuleMemory.remember(hello: ["modules", "module.glasses"], group: "home", in: d))
            #expect(!HubModuleMemory.remember(hello: ["module.glasses", "modules", "file_upload"], group: "home", in: d))
            #expect(HubModuleMemory.remember(hello: ["file_upload"], group: "home", in: d)) // the hub went back to an older one
            #expect(d.object(forKey: "hubModules.home") == nil)
            #expect(!HubModuleMemory.remember(hello: ["file_upload"], group: "home", in: d))
        }
    }
}
