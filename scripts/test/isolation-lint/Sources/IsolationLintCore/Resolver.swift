import Foundation

/// Where a blocking call runs, as far as the source can tell.
public enum Verdict: String, Codable, Sendable, Comparable {
    /// Off the main actor in every target that compiles the file.
    case off
    /// The lint cannot tell. Counted like `main`: it never passes silently.
    case unknown
    /// On the main actor in at least one target.
    case main

    var rank: Int { self == .off ? 0 : self == .unknown ? 1 : 2 }
    public static func < (a: Verdict, b: Verdict) -> Bool { a.rank < b.rank }
}

public struct Finding: Codable, Sendable, Equatable {
    public var file: String
    public var line: Int
    public var column: Int
    public var rule: String
    public var verdict: Verdict
    /// Why, as a chain from the call outwards.
    public var reason: String

    public init(file: String, line: Int, column: Int, rule: String, verdict: Verdict, reason: String) {
        self.file = file
        self.line = line
        self.column = column
        self.rule = rule
        self.verdict = verdict
        self.reason = reason
    }
}

/// Names from the SDK that the lint knows about. Everything else from outside the scanned sources is unknown to it.
enum Known {
    /// With MainActor as the default isolation, a type that conforms to one of these on its own declaration is
    /// inferred `nonisolated` (a protocol from another module that inherits from `SendableMetatype`). Only the ones
    /// checked against the compiler are listed; a type conforming to another such protocol is read as MainActor,
    /// which errs towards reporting.
    static let inferNonisolated: Set<String> = ["Error", "LocalizedError", "CodingKey"]

    /// Protocols and classes that are `@MainActor` in the SDK: a type that inherits from one is on the main actor in
    /// a target without a default isolation too.
    static let mainActor: Set<String> = [
        "View", "App", "Scene", "ViewModifier", "Widget", "WidgetBundle", "ButtonStyle", "PrimitiveButtonStyle", "ToggleStyle",
        "LabelStyle", "UIViewRepresentable", "UIViewControllerRepresentable", "NSViewRepresentable",
        "NSViewControllerRepresentable", "WKInterfaceObjectRepresentable", "UIView", "UIViewController", "UIResponder",
        "UIControl", "UIApplicationDelegate", "UISceneDelegate", "UIWindowSceneDelegate", "UITableViewController",
        "UITableViewCell", "UICollectionViewCell", "UIScrollViewDelegate", "UITextViewDelegate", "UITextFieldDelegate",
        "NSView", "NSViewController", "NSWindow", "NSWindowController", "NSResponder", "NSApplicationDelegate",
        "NSWindowDelegate", "NSMenuDelegate", "NSTextFieldDelegate", "WKNavigationDelegate", "WKUIDelegate",
        "WKScriptMessageHandler", "WKApplicationDelegate", "WKExtensionDelegate",
    ]

    /// Protocols and base types that say nothing about isolation and whose requirements are all listed in
    /// `requirements`: conforming to them neither isolates a type nor lets unseen code call its other members.
    static let neutral: Set<String> = [
        "Sendable", "Codable", "Encodable", "Decodable", "Hashable", "Equatable", "Comparable", "Identifiable", "CaseIterable",
        "RawRepresentable", "Error", "LocalizedError", "CustomStringConvertible", "CustomDebugStringConvertible", "CodingKey",
        "OptionSet", "AnyObject", "BitwiseCopyable", "Copyable", "Escapable", "NSObject", "NSObjectProtocol",
        "String", "Substring", "Character", "Int", "Int8", "Int16", "Int32", "Int64", "UInt", "UInt8", "UInt16", "UInt32", "UInt64",
        "Double", "Float", "Bool",
    ]

    /// What the neutral protocols call on a conforming type.
    static let requirements: [String: Set<String>] = [
        "Codable": ["init", "encode"], "Encodable": ["encode"], "Decodable": ["init"],
        "Hashable": ["hash", "==", "hashValue"], "Equatable": ["=="], "Comparable": ["<", "<=", ">", ">=", "=="],
        "Identifiable": ["id"], "CaseIterable": ["allCases"], "RawRepresentable": ["rawValue", "init"],
        "LocalizedError": ["errorDescription", "failureReason", "recoverySuggestion", "helpAnchor"],
        "CustomStringConvertible": ["description"], "CustomDebugStringConvertible": ["debugDescription"],
        "CodingKey": ["stringValue", "intValue", "init"], "OptionSet": ["rawValue", "init"],
        "NSObject": ["description", "debugDescription", "hash", "isEqual", "init", "copy", "mutableCopy"],
        "NSObjectProtocol": ["description", "debugDescription", "hash", "isEqual"],
    ]

    /// What SDK protocols that isolate a type call on it. A protocol listed here calls nothing else.
    static let sdkRequirements: [String: Set<String>] = [
        "View": ["body"], "App": ["body", "init"], "Scene": ["body"], "ViewModifier": ["body"],
        "UIViewRepresentable": ["makeUIView", "updateUIView", "dismantleUIView", "makeCoordinator", "sizeThatFits"],
        "UIViewControllerRepresentable": ["makeUIViewController", "updateUIViewController", "dismantleUIViewController",
                                          "makeCoordinator", "sizeThatFits"],
        "NSViewRepresentable": ["makeNSView", "updateNSView", "dismantleNSView", "makeCoordinator", "sizeThatFits"],
        "NSViewControllerRepresentable": ["makeNSViewController", "updateNSViewController", "dismantleNSViewController",
                                          "makeCoordinator", "sizeThatFits"],
        "Observable": [], "ObservableObject": ["objectWillChange"],
    ]

    /// Base classes from the SDK: a class calls only what a subclass overrides (or exposes to Objective-C).
    static let classes: Set<String> = [
        "NSObject", "UIViewController", "UIView", "UIResponder", "UIControl", "UITableViewController", "UITableViewCell",
        "UICollectionViewCell", "UIHostingController", "NSView", "NSViewController", "NSWindow", "NSWindowController",
        "NSResponder", "NSHostingController", "NSHostingView", "Operation", "Thread", "UNNotificationServiceExtension",
        "WKWebView", "CALayer", "URLProtocol", "InputStream", "OutputStream", "NSTextView", "UITextView", "UIScrollView",
    ]

    /// Types from the SDK that are not isolated: extending one in a target without a default isolation adds
    /// nonisolated members.
    static let unisolatedTypes: Set<String> = [
        "URL", "Data", "String", "Substring", "Date", "Array", "Dictionary", "Set", "Optional", "Int", "Double", "Bool",
        "FileManager", "Bundle", "JSONDecoder", "JSONEncoder", "UserDefaults", "URLRequest", "URLComponents", "UUID",
        "Sequence", "Collection", "RandomAccessCollection", "DateComponents", "Calendar", "Locale", "TimeInterval",
        "CGFloat", "CGSize", "CGPoint", "CGRect", "Color", "Font", "Result", "Character", "Range", "ClosedRange",
    ]

    /// Concrete types from the SDK: a member the scanned sources add to one is reached only through a value of that
    /// type (or of a subclass). A member added to any other outside type, such as a protocol, is reached by name.
    static let concrete: Set<String> = [
        "URL", "Data", "String", "Substring", "Date", "FileManager", "Bundle", "UserDefaults", "UIImage", "NSImage", "CGImage",
        "Int", "Double", "Bool", "UUID", "URLRequest", "URLSession", "URLComponents", "JSONDecoder", "JSONEncoder", "Color",
        "Font", "CGSize", "CGRect", "CGPoint", "CGFloat", "Calendar", "Locale", "UIColor", "NSColor", "FileHandle", "Character",
        "DateComponents", "NSAttributedString", "AttributedString", "Notification", "Image", "Text", "PHAsset", "AVAsset",
        "AVURLAsset", "UIDevice", "UIApplication", "UIScreen", "ProcessInfo", "Duration", "Decimal", "NSString", "NSData",
        "NSError", "NSNumber", "NSURL", "NSDate", "Task", "DispatchQueue", "OperationQueue", "Thread", "Timer",
    ]

    /// Functions that run their closure before they return, on the thread that called them. By name: the lint takes
    /// `values.map { … }` for a collection's. A Combine publisher or an async sequence has operators of the same names
    /// that keep the closure (`sequenceOperators`); those are not taken as run in place in a file that imports
    /// Combine, or in the sequence of a `for await`.
    static let inline: Set<String> = [
        "map", "flatMap", "compactMap", "filter", "forEach", "reduce", "sorted", "sort", "contains", "first", "last",
        "firstIndex", "lastIndex", "allSatisfy", "min", "max", "removeAll", "drop", "prefix", "partition", "mapValues",
        "compactMapValues", "withLock", "withLockIfAvailable", "withUnsafeBytes", "withUnsafeMutableBytes",
        "withUnsafeBufferPointer", "withUnsafeMutableBufferPointer", "withExtendedLifetime", "withUnsafePointer",
        "withUnsafeMutablePointer", "withCString", "autoreleasepool", "withCheckedContinuation",
        "withCheckedThrowingContinuation", "withUnsafeContinuation", "withUnsafeThrowingContinuation", "withTaskGroup",
        "withThrowingTaskGroup", "withDiscardingTaskGroup", "withThrowingDiscardingTaskGroup", "withTaskCancellationHandler",
        "withAnimation", "withTransaction", "enumerateObjects", "merging", "merge", "elementsEqual", "starts", "split",
    ]

    /// The names in `inline` that publishers and async sequences have too.
    static let sequenceOperators: Set<String> = [
        "map", "flatMap", "compactMap", "filter", "reduce", "contains", "first", "last", "allSatisfy", "min", "max", "drop",
        "prefix",
    ]
}

/// What the declaration around a piece of code is isolated to, read from the source alone.
enum Isolation {
    case main(String)
    /// An actor that is not the main actor.
    case otherActor(String)
    /// No actor: the code runs on its caller's thread. `extensionsFollow`: a plain extension's members are nonisolated
    /// too (true only for the protocol inference; an explicit `nonisolated` does not reach a plain extension).
    case nonisolated(String, extensionsFollow: Bool)
    case unknown(String)
}

/// One step of the answer for a frame.
private enum Shape {
    case constant(Verdict, String)
    /// As another frame.
    case same(Node, String)
    /// Where the declaration is called from.
    case callers(DeclRef, String)
    /// A closure handed to a function of the scanned sources: where that function calls its parameter.
    case flow([Edge], unseen: [String], String)
}

/// A place that runs a frame's code. `weak`: only a name says so (a call on a value whose type the lint cannot read),
/// which can make the answer unknown but never main.
struct Edge {
    var node: Node
    var line: Int
    var weak = false
}

struct Node: Hashable {
    var file: Int
    var frame: Int
    var config: Int
}

struct DeclRef: Hashable {
    var file: Int
    var frame: Int
    /// A stored instance property's value runs in the initializers of its type.
    var asInitializerOfOwner = false
}

/// The answer for a node while the fixpoint runs. `none`: no evidence yet (only a cycle of callers).
private struct Answer {
    enum Level: Int { case none = 0, off, unknown, main }
    var level: Level = .none
    var reason = ""
    /// The last step of the reason alone.
    var detail = ""
}

public final class Lint {
    let files: [FileFacts]
    let configs: [[Int]]                    // per file: indexes into `allConfigs`
    var allConfigs: [TargetConfig] = []
    var configNames: [[String]] = []        // per config: the targets that have it

    var typesByName: [String: [(file: Int, index: Int)]] = [:]
    var extensionsByName: [String: [(file: Int, index: Int)]] = [:]
    var functionsByName: [String: [(file: Int, frame: Int)]] = [:]
    var callsByName: [String: [(file: Int, index: Int)]] = [:]
    var referencesByName: [String: [(file: Int, index: Int)]] = [:]
    var propertiesByName: [String: [(file: Int, property: PropertyType)]] = [:]
    var globalActors: Set<String> = []
    /// A type alias and what it stands for, both ways: either name reaches the members of the other.
    var aliases: [String: Set<String>] = [:]
    var typeVariables: Set<String> = []
    var metatypes: Set<String> = []
    var declaredFunctionNames: Set<String> = []
    var propertyFramesByName: [String: [(file: Int, frame: Int)]] = [:]

    public struct Input: Sendable {
        public var path: String
        public var source: String
        public init(path: String, source: String) { self.path = path; self.source = source }
    }

    public init(files inputs: [Input], project: ProjectMap) {
        var parsed = [FileFacts?](repeating: nil, count: inputs.count)
        parsed.withUnsafeMutableBufferPointer { buffer in
            let slots = UncheckedSlots(buffer)
            DispatchQueue.concurrentPerform(iterations: inputs.count) { i in
                slots.set(i, FileFacts(path: inputs[i].path, source: inputs[i].source))
            }
        }
        files = parsed.map { $0! }
        var index: [ConfigKey: Int] = [:]
        var perFile: [[Int]] = []
        for file in files {
            var mine: [Int] = []
            for config in project.configs(for: file.path) {
                let key = ConfigKey(defaultMain: config.defaultMain, nonsending: config.nonsending)
                let at: Int
                if let known = index[key] { at = known } else {
                    at = allConfigs.count
                    index[key] = at
                    allConfigs.append(config)
                    configNames.append([])
                }
                if !configNames[at].contains(config.name) { configNames[at].append(config.name) }
                if !mine.contains(at) { mine.append(at) }
            }
            perFile.append(mine)
        }
        configs = perFile
        for (f, file) in files.enumerated() {
            for (i, type) in file.types.enumerated() {
                typesByName[type.name, default: []].append((f, i))
                if type.attributes.contains("globalActor") { globalActors.insert(type.name) }
            }
            for (i, ext) in file.extensions.enumerated() {
                extensionsByName[Self.last(ext.extended), default: []].append((f, i))
            }
            for (i, frame) in file.frames.enumerated() {
                if case .function(let info) = frame.kind {
                    functionsByName[info.name, default: []].append((f, i))
                    declaredFunctionNames.insert(info.name)
                }
                if case .property(let info) = frame.kind {
                    for name in info.names { propertyFramesByName[name, default: []].append((f, i)) }
                }
            }
            for (i, call) in file.calls.enumerated() { callsByName[call.name, default: []].append((f, i)) }
            for (i, reference) in file.references.enumerated() { referencesByName[reference.name, default: []].append((f, i)) }
            for property in file.properties { propertiesByName[property.name, default: []].append((f, property)) }
            for alias in file.typealiases {
                if let target = alias.target, target != alias.name {
                    aliases[alias.name, default: []].insert(target)
                    aliases[target, default: []].insert(alias.name)
                } else {
                    typeVariables.insert(alias.name)
                }
            }
            typeVariables.formUnion(file.typeVariables)
            metatypes.formUnion(file.metatypes)
        }
    }

    private struct ConfigKey: Hashable { var defaultMain: Bool; var nonsending: Bool }

    static func last(_ qualified: String) -> String {
        qualified.split(separator: ".").last.map(String.init) ?? qualified
    }

    func targetText(_ config: Int) -> String {
        let names = configNames[config]
        guard let first = names.first else { return "its target" }
        return names.count == 1 ? "target \(first)" : "target \(first) and \(names.count - 1) more"
    }

    // MARK: Frames

    /// The type or extension around a frame, if any.
    func owner(_ file: Int, _ frame: Int) -> Int? {
        var at = files[file].frames[frame].parent
        while at > 0 {
            switch files[file].frames[at].kind {
            case .type, .ext: return at
            default: at = files[file].frames[at].parent
            }
        }
        return nil
    }

    func ownerName(_ file: Int, _ frame: Int?) -> String? {
        guard let frame else { return nil }
        switch files[file].frames[frame].kind {
        case .type(let i): return files[file].types[i].name
        case .ext(let i): return Self.last(files[file].extensions[i].extended)
        default: return nil
        }
    }

    /// The types and extensions around a frame, nearest first.
    func enclosingOwners(_ file: Int, _ frame: Int) -> [String] {
        var out: [String] = []
        var at = frame
        while at > 0 {
            if let name = ownerName(file, at) { out.append(name) }
            at = files[file].frames[at].parent
        }
        return out
    }

    /// Whether code in a frame is static code of its type (`static func`, `static var`) or instance code; nil when it
    /// can be either (a stored property's initial value).
    func siteIsStatic(_ file: Int, _ frame: Int) -> Bool? {
        var at = frame
        while at > 0 {
            let item = files[file].frames[at]
            let parentIsOwner: Bool
            switch files[file].frames[max(item.parent, 0)].kind {
            case .type, .ext, .file: parentIsOwner = true
            default: parentIsOwner = false
            }
            if parentIsOwner {
                switch item.kind {
                case .function(let info): return info.kind == .function || info.kind == .subscripted ? info.isStatic : false
                case .property(let info): return info.isComputed || info.isLazy ? info.isStatic : (info.isStatic ? true : nil)
                default: return nil
                }
            }
            at = item.parent
        }
        return nil
    }

    /// The frame is declared inside a function body, a closure or a property's code.
    func isLocal(_ file: Int, _ frame: Int) -> Bool {
        let parent = files[file].frames[frame].parent
        guard parent >= 0 else { return false }
        switch files[file].frames[parent].kind {
        case .file, .type, .ext: return false
        default: return true
        }
    }

    /// The frame a local declaration's name is visible in, or nil for a member or a global.
    func localScope(_ file: Int, _ frame: Int) -> Int? {
        isLocal(file, frame) ? files[file].frames[frame].parent : nil
    }

    func describe(_ file: Int, _ frame: Int) -> String {
        let item = files[file].frames[frame]
        let place = "\(files[file].path):\(item.line)"
        let scope = ownerName(file, owner(file, frame)).map { $0 + "." } ?? ""
        switch item.kind {
        case .file: return "top-level code (\(place))"
        case .type(let i): return "\(files[file].types[i].qualified) (\(place))"
        case .ext(let i): return "extension \(files[file].extensions[i].extended) (\(place))"
        case .function(let info):
            switch info.kind {
            case .initializer: return "\(scope)init (\(place))"
            case .deinitializer: return "\(scope)deinit (\(place))"
            case .subscripted: return "\(scope)subscript (\(place))"
            case .function: return "\(scope)\(info.name)() (\(place))"
            }
        case .property(let info): return "\(scope)\(info.names.first ?? "_") (\(place))"
        case .closure(let info):
            if case .argument(let callee, _, _, _, _) = info.role { return "the closure passed to \(callee.joined(separator: ".")) (\(place))" }
            return "a closure (\(place))"
        case .asyncLet: return "async let (\(place))"
        case .defaultArgument: return "a default argument (\(place))"
        }
    }

    // MARK: Declared isolation

    func actorAttribute(_ attributes: Set<String>) -> Isolation? {
        if attributes.contains("MainActor") { return .main("@MainActor") }
        if let actor = attributes.first(where: { globalActors.contains($0) }) { return .otherActor("@\(actor)") }
        return nil
    }

    /// What members written in a type's body are isolated to. Static members of an actor are handled by the caller.
    func typeIsolation(_ file: Int, _ index: Int, _ config: Int, depth: Int = 0) -> Isolation {
        let type = files[file].types[index]
        let cfg = allConfigs[config]
        if let actor = actorAttribute(type.attributes) { return actor }
        if type.kind == .actor {
            return type.declaresExecutor ? .unknown("actor \(type.name) names its own executor") : .otherActor("actor \(type.name)")
        }
        if type.modifiers.contains("nonisolated") { return .nonisolated("nonisolated \(type.kind.rawValue) \(type.name)", extensionsFollow: false) }
        // A type declared inside another declaration.
        let parent = files[file].frames[type.frame].parent
        if parent > 0 {
            switch files[file].frames[parent].kind {
            case .type(let outer):
                let around = files[file].types[outer]
                if around.kind == .actor { return .nonisolated("\(type.name) is nested in actor \(around.name)", extensionsFollow: false) }
                if case .nonisolated(let why, _) = typeIsolation(file, outer, config, depth: depth + 1) {
                    return .nonisolated("\(type.name) is nested in \(around.name) (\(why))", extensionsFollow: false)
                }
            case .ext(let outer):
                if files[file].extensions[outer].modifiers.contains("nonisolated") {
                    return .nonisolated("\(type.name) is nested in a nonisolated extension", extensionsFollow: false)
                }
            case .file:
                break
            default:
                // A local type follows the code it is declared in; only the main actor is certain from here.
                if case .main = staticIsolation(Node(file: file, frame: parent, config: config)) { return .main("declared in main-actor code") }
                return cfg.defaultMain ? .unknown("\(type.name) is declared inside a function that is not on the main actor")
                    : .nonisolated("local type \(type.name)", extensionsFollow: false)
            }
        }
        // A subclass has its superclass's isolation.
        if type.kind == .class, depth < 8, let first = type.inherits.first, let bases = typesByName[first] {
            let classes = bases.filter { files[$0.file].types[$0.index].kind == .class }
            if let base = classes.first, classes.count == 1 {
                switch typeIsolation(base.file, base.index, config, depth: depth + 1) {
                case .nonisolated(let why, _): return .nonisolated("subclass of \(first) (\(why))", extensionsFollow: false)
                case .main(let why) where !cfg.defaultMain: return .main("subclass of \(first) (\(why))")
                case .unknown(let why): return .unknown(why)
                default: break
                }
            } else if classes.count > 1 {
                return .unknown("\(type.name) inherits from \(first), which names \(classes.count) classes")
            }
        }
        if cfg.defaultMain {
            if let inferred = type.inherits.first(where: { Known.inferNonisolated.contains($0) }) {
                return .nonisolated("\(type.name) conforms to \(inferred), so it is inferred nonisolated", extensionsFollow: true)
            }
            return .main("MainActor by default in \(targetText(config))")
        }
        // No default isolation: only what the type inherits can isolate it.
        var unread: [String] = []
        for name in type.inherits {
            switch inheritedIsolation(name, config, depth: depth + 1) {
            case .main(let why): return .main(why)
            case .unknown: unread.append(name)
            default: break
            }
        }
        if let name = unread.first {
            return .unknown("\(type.name) inherits from \(name), which this lint cannot read (no default isolation in \(targetText(config)))")
        }
        return .nonisolated("no default isolation in \(targetText(config))", extensionsFollow: true)
    }

    /// Whether inheriting from `name` puts a type on the main actor, in a target without a default isolation.
    func inheritedIsolation(_ name: String, _ config: Int, depth: Int) -> Isolation {
        if Known.mainActor.contains(name) { return .main("\(name) is @MainActor") }
        if Known.neutral.contains(name) { return .nonisolated(name, extensionsFollow: true) }
        guard let found = typesByName[Self.last(name)], depth < 8 else {
            return .unknown("\(name) is not declared in the scanned sources")
        }
        var worst: Isolation = .nonisolated(name, extensionsFollow: true)
        for (file, index) in found {
            let type = files[file].types[index]
            if type.attributes.contains("MainActor") { return .main("\(name) is @MainActor") }
            // A protocol or class of a target that defaults to MainActor is MainActor where that target compiles it.
            if configs[file].contains(where: { allConfigs[$0].defaultMain }) && !type.modifiers.contains("nonisolated") && type.kind != .actor {
                worst = .unknown("\(name) is MainActor by default in another target")
            }
            for parent in type.inherits {
                switch inheritedIsolation(parent, config, depth: depth + 1) {
                case .main(let why): return .main(why)
                case .unknown(let why): worst = .unknown(why)
                default: break
                }
            }
        }
        return worst
    }

    /// What a member declared directly in the type or extension at `ownerFrame` is isolated to.
    func memberIsolation(_ file: Int, _ ownerFrame: Int?, isStatic: Bool, _ config: Int) -> Isolation {
        let cfg = allConfigs[config]
        guard let ownerFrame else {
            return cfg.defaultMain ? .main("MainActor by default in \(targetText(config))")
                : .nonisolated("no default isolation in \(targetText(config))", extensionsFollow: true)
        }
        switch files[file].frames[ownerFrame].kind {
        case .type(let index):
            let type = files[file].types[index]
            if type.kind == .actor, isStatic, actorAttribute(type.attributes) == nil {
                return .nonisolated("a static member of actor \(type.name)", extensionsFollow: false)
            }
            return typeIsolation(file, index, config)
        case .ext(let index):
            let ext = files[file].extensions[index]
            if let actor = actorAttribute(ext.attributes) { return actor }
            if ext.modifiers.contains("nonisolated") { return .nonisolated("nonisolated extension \(ext.extended)", extensionsFollow: false) }
            let name = Self.last(ext.extended)
            guard let found = typesByName[name], !found.isEmpty else {
                // A type from the SDK.
                if cfg.defaultMain { return .main("MainActor by default in \(targetText(config))") }
                if Known.mainActor.contains(name) { return .main("\(name) is @MainActor") }
                if Known.unisolatedTypes.contains(name) || Known.neutral.contains(name) {
                    return .nonisolated("no default isolation in \(targetText(config))", extensionsFollow: true)
                }
                return .unknown("extension of \(name), which this lint cannot read (no default isolation in \(targetText(config)))")
            }
            var answer: Isolation?
            for (typeFile, typeIndex) in found {
                let type = files[typeFile].types[typeIndex]
                let one: Isolation
                if type.kind == .actor, isStatic, actorAttribute(type.attributes) == nil {
                    one = .nonisolated("a static member of actor \(type.name)", extensionsFollow: false)
                } else if type.kind == .protocol {
                    if let actor = actorAttribute(type.attributes) { one = actor }
                    else if cfg.defaultMain { one = .main("MainActor by default in \(targetText(config))") }
                    else { one = inheritedIsolation(name, config, depth: 0) }
                } else {
                    switch typeIsolation(typeFile, typeIndex, config) {
                    case .nonisolated(let why, let follow):
                        // An explicit `nonisolated` on the type does not reach a plain extension.
                        one = follow || !cfg.defaultMain ? .nonisolated(why, extensionsFollow: follow)
                            : .main("a plain extension of \(name) is MainActor by default in \(targetText(config))")
                    case let other: one = other
                    }
                }
                answer = answer.map { Self.worse($0, one) } ?? one
            }
            return answer ?? .unknown("extension of \(name)")
        default:
            return .unknown("declared in an unexpected place")
        }
    }

    /// A member of a type that is not isolated is on the main actor all the same when it meets a requirement of a
    /// protocol that is, unless the member itself says `nonisolated` (checked before this is asked).
    func witnessIsolation(_ name: String, _ ownerType: String?) -> Isolation? {
        guard let ownerType else { return nil }
        for base in ancestors(ownerType).sorted() where base != ownerType {
            if let declared = typesByName[base] {
                for (f, i) in declared where files[f].types[i].kind == .protocol && files[f].types[i].requirements.contains(name) {
                    let requirement = files[f].types[i]
                    if requirement.attributes.contains("MainActor") {
                        return .main("it meets a requirement of \(base), which is @MainActor")
                    }
                    if !requirement.modifiers.contains("nonisolated"), configs[f].contains(where: { allConfigs[$0].defaultMain }) {
                        return .main("it meets a requirement of \(base), which is MainActor by default")
                    }
                }
            } else if Known.mainActor.contains(base), Known.sdkRequirements[base]?.contains(name) == true {
                return .main("it meets a requirement of \(base), which is @MainActor")
            }
        }
        return nil
    }

    static func worse(_ a: Isolation, _ b: Isolation) -> Isolation {
        func rank(_ i: Isolation) -> Int {
            switch i {
            case .main: return 3
            case .unknown: return 2
            case .nonisolated: return 1
            case .otherActor: return 0
            }
        }
        return rank(b) > rank(a) ? b : a
    }

    /// The isolation the compiler gives the code of a frame, without asking who calls it.
    func staticIsolation(_ node: Node, depth: Int = 0) -> Isolation {
        let file = files[node.file]
        let frame = file.frames[node.frame]
        guard depth < 64 else { return .unknown("too deeply nested") }
        let parent = Node(file: node.file, frame: frame.parent, config: node.config)
        switch frame.kind {
        case .file:
            return memberIsolation(node.file, nil, isStatic: true, node.config)
        case .type, .ext:
            return memberIsolation(node.file, node.frame, isStatic: false, node.config)
        case .function(let info):
            if let actor = actorAttribute(info.attributes) { return actor }
            if info.attributes.contains("concurrent") { return .nonisolated("@concurrent", extensionsFollow: false) }
            if info.modifiers.contains("nonisolated") { return .nonisolated("nonisolated", extensionsFollow: false) }
            if info.hasIsolatedParameter { return .nonisolated("isolated to a parameter", extensionsFollow: false) }
            if isLocal(node.file, node.frame) {
                if info.attributes.contains("Sendable") { return .nonisolated("a @Sendable local function", extensionsFollow: false) }
                return staticIsolation(parent, depth: depth + 1)
            }
            let ownerFrame = owner(node.file, node.frame)
            var isolation = memberIsolation(node.file, ownerFrame, isStatic: info.isStatic, node.config)
            if case .otherActor(let why) = isolation, info.kind == .initializer, !info.isAsync, !why.hasPrefix("@") {
                // An actor's synchronous initializer runs where it is called.
                isolation = .nonisolated("a synchronous initializer of an actor", extensionsFollow: false)
            }
            if case .nonisolated = isolation, let witness = witnessIsolation(info.name, ownerName(node.file, ownerFrame)) { return witness }
            return isolation
        case .property(let info):
            if let actor = actorAttribute(info.attributes) { return actor }
            if info.modifiers.contains("nonisolated") { return .nonisolated("nonisolated", extensionsFollow: false) }
            let ownerFrame = owner(node.file, node.frame)
            let isolation = memberIsolation(node.file, ownerFrame, isStatic: info.isStatic, node.config)
            if case .nonisolated = isolation {
                for name in info.names {
                    if let witness = witnessIsolation(name, ownerName(node.file, ownerFrame)) { return witness }
                }
            }
            return isolation
        case .closure(let info):
            if let actor = actorAttribute(info.attributes) { return actor }
            if info.attributes.contains("concurrent") { return .nonisolated("@concurrent", extensionsFollow: false) }
            let kind = closureKind(node, info)
            switch kind {
            case .leaves(let why): return .nonisolated(why, extensionsFollow: false)
            case .main(let why): return .main(why)
            default: break
            }
            let around = staticIsolation(parent, depth: depth + 1)
            // `{ @Sendable in … }` is not isolated, whatever is around it; only a task's closure still inherits.
            if info.attributes.contains("Sendable") {
                if case .task = kind { return around }
                return .nonisolated("an explicit @Sendable closure", extensionsFollow: false)
            }
            if case .main = around { return around }
            switch kind {
            case .unrecognized, .stored, .memberwise:
                // The parameter or variable it goes to may be typed `@MainActor`, which would isolate the closure.
                return .unknown("\(describe(node.file, node.frame)) may be isolated by what it is handed to, which the lint cannot read")
            default:
                return around
            }
        case .asyncLet:
            return .nonisolated("async let", extensionsFollow: false)
        case .defaultArgument:
            return .unknown("a default argument is evaluated by the caller")
        }
    }

    // MARK: Closures

    enum ClosureKind {
        /// Leaves the actor it is written in: a detached task, a global or private queue, a task group's child.
        case leaves(String)
        case main(String)
        /// `Task { }`: starts on the actor of the code around it, or on the global executor when there is none.
        case task
        /// Runs before the call returns, on the same thread.
        case inline
        /// Kept by a function of the scanned sources (an `@escaping` parameter): it runs where that function calls it.
        case scanned([(file: Int, frame: Int)], String)
        /// Kept in a stored property through a struct's memberwise initializer: it runs where the property is called.
        case memberwise([(file: Int, frame: Int)], String)
        /// The lint does not know the function it is passed to.
        case unrecognized(String)
        case stored
    }

    func closureKind(_ node: Node, _ info: ClosureInfo) -> ClosureKind {
        let file = files[node.file]
        switch info.role {
        case .immediate:
            return .inline
        case .stored:
            return .stored
        case .assigned(let name, let receiver):
            // The stored properties of the scanned sources this can be: the closure runs where one of them is called.
            let stored = (propertyFramesByName[name] ?? []).filter { f, frame in
                guard case .property(let property) = files[f].frames[frame].kind, !property.isComputed, !isLocal(f, frame) else { return false }
                let ownerType = ownerName(f, owner(f, frame))
                let member = Member(file: f, ownerType: ownerType, local: nil, isStatic: property.isStatic || ownerType == nil,
                                    privateScope: property.modifiers.contains("private") || property.modifiers.contains("fileprivate"))
                return reaches(receiver, file: node.file, frame: node.frame, member) != .no
            }
            return stored.isEmpty ? .stored : .memberwise(stored, "the property \(name)")
        case .argument(let callee, let receiver, let label, let trailingIndex, let argumentIndex):
            let text = callee.joined(separator: ".")
            let name = callee.last ?? ""
            let head = Array(callee.dropLast())
            if head == ["Task"], name == "detached" { return .leaves("Task.detached") }
            if head == ["Thread"], name == "detachNewThread" { return .leaves("Thread.detachNewThread") }
            if callee == ["Task"] || (head == ["Task"] && name == "immediate") { return .task }
            if head == ["MainActor"], name == "run" || name == "assumeIsolated" { return .main("MainActor.\(name)") }
            if head == ["DispatchQueue", "main"], ["async", "asyncAfter", "sync", "asyncAndWait"].contains(name) {
                return .main("DispatchQueue.main.\(name)")
            }
            if head == ["OperationQueue", "main"], name == "addOperation" || name == "addBarrierBlock" { return .main("OperationQueue.main") }
            if head == ["RunLoop", "main"], name == "perform" { return .main("RunLoop.main.perform") }
            if name == "async" || name == "asyncAfter" {
                if head == ["DispatchQueue", "global()"] { return .leaves("DispatchQueue.global().\(name)") }
                // `queue.async`, `self.queue.async`, `Self.queue.async`: a queue this file creates with a label.
                let queue = head.count == 1 ? head[0] : (head.count == 2 && (head[0] == "self" || head[0] == "Self") ? head[1] : nil)
                if let queue, file.privateQueues[queue] == true { return .leaves("\(queue).\(name), a queue of its own") }
                return .unrecognized(text)
            }
            if name == "addTask" || name == "addTaskUnlessCancelled" {
                // A task group's child, when it is written inside the group's closure and nothing here is named so.
                if !declaredFunctionNames.contains(name), insideTaskGroup(node) { return .leaves("a task group's \(name)") }
                return .unrecognized(text)
            }
            if name == "sync" || name == "asyncAndWait" {
                // Runs on the thread that calls it while that thread waits, for a queue that is not the main one.
                if head == ["DispatchQueue", "global()"] { return .inline }
                let queue = head.count == 1 ? head[0] : (head.count == 2 && (head[0] == "self" || head[0] == "Self") ? head[1] : nil)
                if let queue, file.privateQueues[queue] == true { return .inline }
            }
            // `onCancel` runs on the thread that cancels the task.
            if name == "withTaskCancellationHandler", label == "onCancel" { return .unrecognized("\(text)'s onCancel") }
            // A function of the scanned sources.
            let candidates = scannedCallees(name, receiver, node)
            if !candidates.isEmpty {
                var mainActor = true, nonEscaping = true
                for (f, frame) in candidates {
                    guard case .function(let function) = files[f].frames[frame].kind else { continue }
                    let parameter = Self.parameter(function, label: label, trailingIndex: trailingIndex, argumentIndex: argumentIndex)
                    mainActor = mainActor && parameter?.mainActorClosure == true
                    nonEscaping = nonEscaping && parameter.map { !$0.escaping && $0.isFunctionType } == true
                }
                if mainActor { return .main("\(text)'s @MainActor parameter") }
                if nonEscaping { return .inline }
                return .scanned(candidates, text)
            }
            if let label, let stored = memberwiseProperties(callee, receiver, label: label, node) {
                return .memberwise(stored, "the property \(label)")
            }
            if Known.inline.contains(name) {
                // A lazy sequence keeps the closure; so may a function of the scanned sources with this name, when
                // the receiver does not say which one is called.
                if head.contains("lazy") { return .unrecognized(text) }
                // So does a publisher or an async sequence, which runs it where its values are produced.
                if Known.sequenceOperators.contains(name), info.inAsyncSequence || file.imports.contains("Combine") {
                    return .unrecognized(text)
                }
                let keeps = (functionsByName[name] ?? []).contains { f, frame in
                    guard case .function(let function) = files[f].frames[frame].kind else { return false }
                    return function.parameters.contains { $0.escaping }
                }
                if keeps, !receiverIsRead(receiver, node) { return .unrecognized(text) }
                return .inline
            }
            return .unrecognized(text)
        }
    }

    /// The receiver names something the lint can look up: a scanned function is found through it when it is the callee.
    func receiverIsRead(_ receiver: Receiver, _ node: Node) -> Bool {
        switch receiver {
        case .none, .selfInstance, .selfType, .type, .construct: return true
        case .variable, .member, .result: return valueTypes(receiver, file: node.file, frame: node.frame) != nil
        default: return false
        }
    }

    /// `T(label: { … })` where `T` is a struct of the scanned sources without an initializer that takes `label`: the
    /// memberwise initializer stores the closure in the property `label`.
    func memberwiseProperties(_ callee: [String], _ receiver: Receiver, label: String, _ node: Node) -> [(file: Int, frame: Int)]? {
        guard let name = callee.last else { return nil }
        let lexical = enclosingOwners(node.file, node.frame).first
        var type: String?
        if name == "Self" { type = lexical }
        else if name == "init" {
            if case .type(let named) = receiver { type = named }
            if receiver == .selfType { type = lexical }
        } else if name.first?.isUppercase == true { type = name }
        guard let type, let declared = typesByName[type], !declared.isEmpty,
              declared.allSatisfy({ files[$0.file].types[$0.index].kind == .struct }) else { return nil }
        let explicit = (functionsByName["init"] ?? []).contains { f, frame in
            guard case .function(let function) = files[f].frames[frame].kind, ownerName(f, owner(f, frame)) == type else { return false }
            return function.parameters.contains { $0.label == label }
        }
        if explicit { return nil }
        let stored = (propertyFramesByName[label] ?? []).filter { f, frame in
            guard case .property(let info) = files[f].frames[frame].kind, !info.isStatic, !info.isComputed,
                  let ownerFrame = owner(f, frame), case .type = files[f].frames[ownerFrame].kind else { return false }
            return ownerName(f, ownerFrame) == type
        }
        return stored.isEmpty ? nil : stored
    }

    /// Where a closure kept in a stored property runs: where the property is called.
    private func storedFlow(_ stored: [(file: Int, frame: Int)], _ why: String) -> Shape {
        var nodes: [Edge] = []
        var unseen: [String] = []
        for (f, frame) in stored {
            guard case .property(let info) = files[f].frames[frame].kind else { continue }
            let member = Member(file: f, ownerType: ownerName(f, owner(f, frame)), local: nil, isStatic: info.isStatic,
                                privateScope: info.modifiers.contains("private") || info.modifiers.contains("fileprivate"))
            for (at, name) in info.names.enumerated() {
                let arity = at < info.arities.count ? info.arities[at] : nil
                for (file, index) in callsByName[name] ?? [] {
                    let call = files[file].calls[index]
                    // A closure of two arguments is not what `x.name(a)` calls.
                    if let arity, call.labels.count + call.trailingClosures != arity { continue }
                    let reach = reaches(call.receiver, file: file, frame: call.frame, member)
                    guard reach != .no else { continue }
                    for config in configs[file] {
                        nodes.append(Edge(node: Node(file: file, frame: call.frame, config: config), line: call.line, weak: reach == .maybe))
                    }
                }
                for (file, index) in referencesByName[name] ?? [] {
                    let reference = files[file].references[index]
                    if reference.indirect != .no { unseen.append("\(files[file].path):\(reference.line) names the property in a key path"); continue }
                    guard !reference.readsValue, reaches(reference.receiver, file: file, frame: reference.frame, member) != .no else { continue }
                    unseen.append("\(files[file].path):\(reference.line) may read the closure without calling it")
                }
            }
        }
        if nodes.isEmpty, unseen.isEmpty { unseen.append("the lint found no call of it") }
        return .flow(nodes, unseen: unseen, why)
    }

    /// The functions of the scanned sources a call `receiver.name { … }` can be, when the receiver says enough.
    func scannedCallees(_ name: String, _ receiver: Receiver, _ node: Node) -> [(file: Int, frame: Int)] {
        guard let declared = functionsByName[name] else { return [] }
        let lexical = enclosingOwners(node.file, node.frame)
        var types: [String]
        var globals = false
        switch receiver {
        case .none: types = lexical; globals = true
        case .selfInstance, .selfType: types = Array(lexical.prefix(1))
        case .type(let type): types = [type]
        case .variable, .member, .construct, .result:
            guard let known = valueTypes(receiver, file: node.file, frame: node.frame) else { return [] }
            types = known
        default: return []
        }
        return declared.filter { f, frame in
            if let scope = localScope(f, frame) { return f == node.file && receiver == .none && isInside(node.file, node.frame, scope) }
            guard let owner = ownerName(f, owner(f, frame)) else { return globals }
            return types.contains { related($0, owner) }
        }
    }

    /// Where a function of the scanned sources calls the closure it was given.
    private func flow(_ candidates: [(file: Int, frame: Int)], _ info: ClosureInfo, _ node: Node, _ why: String) -> Shape {
        guard case .argument(_, _, let label, let trailingIndex, let argumentIndex) = info.role else {
            return .constant(.unknown, why)
        }
        var nodes: [Edge] = []
        var unseen: [String] = []
        for (f, frame) in candidates {
            guard case .function(let function) = files[f].frames[frame].kind else { continue }
            let here = describe(f, frame)
            guard let parameter = Self.parameter(function, label: label, trailingIndex: trailingIndex, argumentIndex: argumentIndex),
                  parameter.isFunctionType else {
                unseen.append("the lint cannot match the closure to a parameter of \(here)")
                continue
            }
            if parameter.mainActorClosure {
                unseen.append("\(here) takes it as a @MainActor closure")
                continue
            }
            var called = false
            for call in files[f].calls where call.name == parameter.name && call.receiver == .none && isInside(f, call.frame, frame) {
                called = true
                let config = configs[f].contains(node.config) ? [node.config] : configs[f]
                for one in config { nodes.append(Edge(node: Node(file: f, frame: call.frame, config: one), line: call.line)) }
            }
            for reference in files[f].references where reference.name == parameter.name && reference.receiver == .none
                && !reference.readsValue && isInside(f, reference.frame, frame) {
                called = true
                unseen.append("\(here) passes the closure on at line \(reference.line)")
            }
            if !called { unseen.append("\(here) never calls the closure where the lint can see") }
        }
        return .flow(nodes, unseen: unseen, why)
    }

    /// Whether `frame` is `ancestor` or lies inside it.
    func isInside(_ file: Int, _ frame: Int, _ ancestor: Int) -> Bool {
        var at = frame
        while at >= 0 {
            if at == ancestor { return true }
            at = files[file].frames[at].parent
        }
        return false
    }

    /// The parameter a closure argument goes to, by label or by its place among the trailing closures.
    static func parameter(_ function: FunctionInfo, label: String?, trailingIndex: Int?, argumentIndex: Int?) -> Parameter? {
        if let trailingIndex {
            if trailingIndex > 0, let label { return function.parameters.first { $0.label == label || $0.name == label } }
            // The first trailing closure: the first function-typed parameter that a trailing closure can fill.
            let closures = function.parameters.filter(\.isFunctionType)
            return closures.count == 1 ? closures[0] : closures.first
        }
        if let label { return function.parameters.first { $0.label == label } }
        if let argumentIndex, argumentIndex < function.parameters.count { return function.parameters[argumentIndex] }
        return nil
    }

    func insideTaskGroup(_ node: Node) -> Bool {
        let file = files[node.file]
        var at = file.frames[node.frame].parent
        while at > 0 {
            if case .closure(let info) = file.frames[at].kind, case .argument(let callee, _, _, _, _) = info.role,
               let name = callee.last,
               ["withTaskGroup", "withThrowingTaskGroup", "withDiscardingTaskGroup", "withThrowingDiscardingTaskGroup"].contains(name) {
                return true
            }
            if case .function = file.frames[at].kind { return false }
            at = file.frames[at].parent
        }
        return false
    }

    // MARK: Where a frame runs

    private func shape(_ node: Node) -> Shape {
        let file = files[node.file]
        let frame = file.frames[node.frame]
        let cfg = allConfigs[node.config]
        let parent = Node(file: node.file, frame: frame.parent, config: node.config)
        let here = describe(node.file, node.frame)
        switch frame.kind {
        case .file, .type, .ext:
            switch staticIsolation(node) {
            case .main(let why): return .constant(.main, "\(here) is on the main actor (\(why))")
            case .otherActor(let why): return .constant(.off, "\(here) is on \(why)")
            case .nonisolated(let why, _): return .constant(.unknown, "\(here) is not isolated (\(why)) and the lint cannot tell what runs it")
            case .unknown(let why): return .constant(.unknown, why)
            }
        case .function(let info):
            if info.kind == .deinitializer, !info.modifiers.contains("isolated") {
                return .constant(.unknown, "\(here) runs on whichever thread releases the last reference")
            }
            switch staticIsolation(node) {
            case .main(let why): return .constant(.main, "\(here) is on the main actor (\(why))")
            case .otherActor(let why): return .constant(.off, "\(here) is isolated to \(why)")
            case .unknown(let why): return .constant(.unknown, "\(here): \(why)")
            case .nonisolated(let why, _):
                if why == "@concurrent" { return .constant(.off, "\(here) is @concurrent") }
                if info.isAsync, !cfg.nonsending, !info.modifiers.contains("nonisolated(nonsending)"), !info.hasIsolatedParameter {
                    return .constant(.off, "\(here) is a nonisolated async function, which runs on the global executor in \(targetText(node.config))")
                }
                return .callers(DeclRef(file: node.file, frame: node.frame), "\(here) is not isolated (\(why)): it runs where it is called")
            }
        case .property(let info):
            switch staticIsolation(node) {
            case .main(let why): return .constant(.main, "\(here) is on the main actor (\(why))")
            case .otherActor(let why):
                if info.isComputed || info.isStatic || info.isLazy { return .constant(.off, "\(here) is isolated to \(why)") }
                return .callers(DeclRef(file: node.file, frame: node.frame, asInitializerOfOwner: true),
                                "\(here) is set in the initializers of its type")
            case .unknown(let why): return .constant(.unknown, "\(here): \(why)")
            case .nonisolated(let why, _):
                let stored = !info.isComputed && !info.isStatic && !info.isLazy && owner(node.file, node.frame) != nil
                return .callers(DeclRef(file: node.file, frame: node.frame, asInitializerOfOwner: stored),
                                stored ? "\(here) is set in the initializers of its type, which is not isolated (\(why))"
                                    : "\(here) is not isolated (\(why)): it runs where it is read")
            }
        case .closure(let info):
            if let actor = actorAttribute(info.attributes) {
                if case .main = actor { return .constant(.main, "\(here) is @MainActor") }
                return .constant(.off, "\(here) is isolated to a global actor")
            }
            if info.attributes.contains("concurrent") { return .constant(.off, "\(here) is @concurrent") }
            switch closureKind(node, info) {
            case .leaves(let why): return .constant(.off, "\(here) leaves the actor: \(why)")
            case .main(let why): return .constant(.main, "\(here) runs on the main actor: \(why)")
            case .inline: return .same(parent, "")
            case .task:
                switch staticIsolation(parent) {
                case .main(let why): return .constant(.main, "\(here) starts on the main actor, as the code around it (\(why))")
                case .otherActor(let why): return .constant(.off, "\(here) starts on \(why)")
                case .nonisolated(let why, _): return .constant(.off, "\(here) is a Task started from code that is not isolated (\(why)): it runs on the global executor")
                case .unknown(let why): return .constant(.unknown, "\(here): \(why)")
                }
            case .scanned(let candidates, let callee):
                return flow(candidates, info, node, "\(here) runs where \(callee) calls it")
            case .unrecognized(let callee):
                // A closure is isolated like the code it is written in unless the parameter it goes to is @Sendable,
                // which the lint cannot see for a function it does not have the source of.
                if !info.attributes.contains("Sendable"), case .main(let why) = staticIsolation(parent) {
                    return .constant(.main, "\(here) is written in main-actor code (\(why)) and stays on the main actor unless \(callee) takes a @Sendable closure, which the lint cannot see")
                }
                return .constant(.unknown, "\(here): the lint does not know where \(callee) runs its closure")
            case .memberwise(let stored, let what):
                if !info.attributes.contains("Sendable"), case .main(let why) = staticIsolation(parent) {
                    return .constant(.main, "\(here) is written in main-actor code (\(why)) and kept in \(what): it stays on the main actor unless the property's type is @Sendable")
                }
                return storedFlow(stored, "\(here) is kept in \(what) and runs where that is called")
            case .stored:
                if !info.attributes.contains("Sendable"), case .main(let why) = staticIsolation(parent) {
                    return .constant(.main, "\(here) is written in main-actor code (\(why)) and is kept: it stays on the main actor unless its type is @Sendable")
                }
                return .constant(.unknown, "\(here) is kept and called later, the lint cannot tell from where")
            }
        case .asyncLet:
            return .constant(.off, "\(here) runs in a child task")
        case .defaultArgument:
            return .callers(DeclRef(file: node.file, frame: frame.parent), "\(here) is evaluated where the function is called")
        }
    }

    // MARK: Callers

    private struct Callers {
        var nodes: [Edge] = []
        /// Things that can run the declaration without a call the lint can read.
        var unseen: [String] = []
    }

    private var callerCache: [DeclRef: Callers] = [:]
    private var ancestorCache: [String: Set<String>] = [:]
    private var metatypeCache: [String: Bool] = [:]

    /// Everything the type named `name` inherits from, over its declarations and extensions in the scanned sources.
    func allInherits(_ name: String) -> [String] {
        var out: [String] = []
        for (file, index) in typesByName[name] ?? [] { out += files[file].types[index].inherits }
        for (file, index) in extensionsByName[name] ?? [] { out += files[file].extensions[index].inherits }
        out += aliases[name] ?? []
        return out
    }

    /// The type and everything it inherits from, directly or not.
    func ancestors(_ name: String) -> Set<String> {
        if let known = ancestorCache[name] { return known }
        var seen: Set<String> = [name]
        var pending = [name]
        while let next = pending.popLast() {
            for inherited in allInherits(next) {
                let base = Self.last(inherited)
                if seen.insert(base).inserted { pending.append(base) }
            }
        }
        ancestorCache[name] = seen
        return seen
    }

    func inheritsTransitively(_ name: String, from base: String) -> Bool {
        name == base || ancestors(name).contains(base)
    }

    func related(_ a: String?, _ b: String?) -> Bool {
        guard let a, let b else { return a == nil && b == nil }
        return a == b || inheritsTransitively(a, from: b) || inheritsTransitively(b, from: a)
    }

    /// A name used as a type that may stand for any type: a generic parameter, an associated type, or a type alias
    /// for something that is not a name.
    func isTypeVariable(_ name: String) -> Bool {
        typeVariables.contains(name)
    }

    private var typingDepth = 0

    /// What a declaration says its value is: the type it states, else the type of the expression it comes from.
    func stated(_ type: String?, _ from: Receiver?, file: Int, frame: Int) -> [String]? {
        if let type { return [type] }
        if let from { return valueTypes(from, file: file, frame: frame) }
        return nil
    }

    /// The types a name may have where it is used: every binding of it in the frames around the use, the properties
    /// of the types around it and the globals. nil when one of them states no type, or when there is none.
    func variableTypes(_ name: String, file: Int, frame: Int) -> [String]? {
        var out: [String] = []
        var found = false
        let bound = files[file].bindings[name] ?? []
        let declared = propertiesByName[name] ?? []
        var at = frame
        while at >= 0 {
            for binding in bound where binding.frame == at {
                guard let types = stated(binding.type, binding.from, file: file, frame: at) else { return nil }
                found = true
                out += types
            }
            if let owner = ownerName(file, at) {
                for (f, property) in declared where property.owner.map({ related($0, owner) }) == true {
                    guard let types = stated(property.type, property.from, file: f, frame: property.frame) else { return nil }
                    found = true
                    out += types
                }
            }
            at = files[file].frames[at].parent
        }
        for (f, property) in declared where property.owner == nil {
            guard let types = stated(property.type, property.from, file: f, frame: property.frame) else { return nil }
            found = true
            out += types
        }
        return found ? out : nil
    }

    /// The types the value before the dot may have, from what declarations state; nil when the lint cannot tell.
    /// `(external)` is a type from outside the scanned sources.
    func valueTypes(_ receiver: Receiver, file: Int, frame: Int) -> [String]? {
        // `let a = b.next`, `let b = a.next`: give up instead of going round.
        guard typingDepth < 8 else { return nil }
        typingDepth += 1
        defer { typingDepth -= 1 }
        switch receiver {
        case .construct(let type):
            return [type]
        case .variable(let name):
            return variableTypes(name, file: file, frame: frame)
        case .member(let name, let base):
            var owners: [String]
            var named = false
            switch base {
            case .selfInstance, .selfType:
                guard let nearest = enclosingOwners(file, frame).first else { return nil }
                owners = [nearest]
            case .type(let type):
                owners = [type]
                named = true
            default:
                guard let types = valueTypes(base, file: file, frame: frame) else { return nil }
                owners = types
            }
            var out: [String] = []
            for owner in owners {
                if isTypeVariable(owner) { return nil }
                let declared = (propertiesByName[name] ?? []).filter { related($0.property.owner, owner) }
                if declared.isEmpty {
                    // `URLSession.shared`: a static property of an outside type is of an outside type. A property
                    // of an outside value (`items.first`) or one a scanned type inherits can be anything.
                    if named, typesByName[owner] == nil { out.append("(external)"); continue }
                    return nil
                }
                for (f, property) in declared {
                    guard let types = stated(property.type, property.from, file: f, frame: property.frame) else { return nil }
                    out += types
                }
            }
            return out
        case .result(let name, let base):
            // What the functions of this name return, when the lint can read what they are called on (otherwise
            // a function from outside the scanned sources may be the one called).
            switch base {
            case .none, .selfInstance, .selfType, .type, .construct: break
            case .variable, .member, .result: guard valueTypes(base, file: file, frame: frame) != nil else { return nil }
            default: return nil
            }
            var out: [String] = []
            for (f, declared) in functionsByName[name] ?? [] {
                guard case .function(let function) = files[f].frames[declared].kind else { continue }
                let ownerType = ownerName(f, owner(f, declared))
                let member = Member(file: f, ownerType: ownerType, local: localScope(f, declared), isStatic: function.isStatic || ownerType == nil,
                                    privateScope: function.modifiers.contains("private") || function.modifiers.contains("fileprivate"))
                guard reaches(base, file: file, frame: frame, member) != .no else { continue }
                guard let returns = function.returns else { return nil }
                out.append(returns)
            }
            return out.isEmpty ? nil : out
        default:
            return nil
        }
    }

    /// Whether a use can name a member: `maybe` when only the name says so and the lint cannot read the type of the
    /// value it is used on.
    enum Reach: Int, Comparable {
        case no, maybe, yes
        static func < (a: Reach, b: Reach) -> Bool { a.rawValue < b.rawValue }
    }

    /// Whether a value whose declared type is `type` can have the member of `owner` called on it.
    func typeReaches(_ type: String, _ owner: String) -> Reach {
        if isTypeVariable(type) || owner == "Optional" { return .maybe }
        if typesByName[owner] == nil {
            // A member the scanned sources add to an outside type: a protocol there can be met by anything.
            if Known.concrete.contains(owner) { return related(type, owner) ? .yes : .no }
            return .maybe
        }
        if type == "(external)" { return .no }
        return related(type, owner) ? .yes : .no
    }

    /// A metatype of the type is used as a value somewhere (`T.self`): its static members and initializers can be
    /// called through a variable.
    func metatypeEscapes(_ owner: String) -> Bool {
        if let known = metatypeCache[owner] { return known }
        let escapes = metatypes.contains { related($0, owner) }
        metatypeCache[owner] = escapes
        return escapes
    }

    /// A member (or a global) that a call or a reference may name.
    struct Member {
        var file: Int
        var ownerType: String?
        /// Declared inside a function body: the frame it is declared in, which is as far as its name reaches.
        var local: Int?
        var isStatic: Bool
        /// `private` or `fileprivate`: used in its own file only.
        var privateScope: Bool
    }

    /// Whether an expression with this receiver, written in `frame` of `file`, can name the member.
    func reaches(_ receiver: Receiver, file: Int, frame: Int, _ member: Member) -> Reach {
        if member.privateScope, file != member.file { return .no }
        // A function declared inside another: a bare name inside the body that declares it.
        if let scope = member.local {
            return file == member.file && receiver == .none && isInside(file, frame, scope) ? .yes : .no
        }
        guard let ownerType = member.ownerType else {
            // A global function or variable: a bare name, or `Module.name`.
            if case .type(let name) = receiver { return typesByName[name] == nil && !isTypeVariable(name) ? .yes : .no }
            return receiver == .none ? .yes : .no
        }
        let isStatic = member.isStatic
        let lexical = enclosingOwners(file, frame)
        switch receiver {
        case .none, .selfInstance:
            guard let nearest = lexical.first else { return .no }
            if related(nearest, ownerType) {
                // A bare name reaches a static member only from static code, an instance member only from
                // instance code (`self` in static code is the type).
                guard let siteStatic = siteIsStatic(file, frame) else { return .yes }
                return siteStatic == isStatic ? .yes : .no
            }
            // Code of a nested type sees the static members of the types around it by their bare names.
            return isStatic && receiver == .none && lexical.dropFirst().contains { related($0, ownerType) } ? .yes : .no
        case .selfType:
            return isStatic && lexical.first.map { related($0, ownerType) } == true ? .yes : .no
        case .superInstance:
            return lexical.first.map { inheritsTransitively($0, from: ownerType) } == true ? .yes : .no
        case .type(let name):
            guard isStatic else { return .no }
            if related(name, ownerType) { return .yes }
            return isTypeVariable(name) ? .maybe : .no
        case .implicit, .metatype:
            return isStatic ? .maybe : .no
        case .construct(let type):
            return isStatic ? .no : typeReaches(type, ownerType)
        case .variable, .member, .result:
            let types = valueTypes(receiver, file: file, frame: frame)
            if isStatic {
                if let types { return types.contains("(metatype)") ? .maybe : .no }
                return metatypeEscapes(ownerType) ? .maybe : .no
            }
            guard let types else { return .maybe }
            return types.map { typeReaches($0, ownerType) }.max() ?? .no
        case .other:
            if isStatic { return metatypeEscapes(ownerType) ? .maybe : .no }
            return .maybe
        }
    }

    static func labelsMatch(_ call: CallSite, _ function: FunctionInfo) -> Bool {
        var parameters = function.parameters[...]
        for label in call.labels {
            // Skip parameters with a default value until one takes this argument.
            var matched = false
            while let parameter = parameters.first {
                if parameter.label == label {
                    if !parameter.variadic { parameters = parameters.dropFirst() }
                    matched = true
                    break
                }
                guard parameter.hasDefault || parameter.variadic else { return false }
                parameters = parameters.dropFirst()
            }
            if !matched { return false }
        }
        // What is left is filled by trailing closures or by default values.
        var trailing = call.trailingClosures
        for parameter in parameters {
            if parameter.variadic || parameter.hasDefault { if parameter.isFunctionType, trailing > 0 { trailing -= 1 }; continue }
            if parameter.isFunctionType, trailing > 0 { trailing -= 1; continue }
            return false
        }
        return trailing == 0 || function.parameters.contains { $0.isFunctionType }
    }

    private func callers(of decl: DeclRef) -> Callers {
        if let known = callerCache[decl] { return known }
        var out = Callers()
        let frame = files[decl.file].frames[decl.frame]
        let ownerType = ownerName(decl.file, owner(decl.file, decl.frame))
        let local = localScope(decl.file, decl.frame)

        func add(_ file: Int, _ atFrame: Int, _ line: Int, weak: Bool = false) {
            for config in configs[file] { out.nodes.append(Edge(node: Node(file: file, frame: atFrame, config: config), line: line, weak: weak)) }
            if configs[file].isEmpty { out.unseen.append("\(files[file].path):\(line) is in no target the lint knows") }
        }

        /// What can run the member without a call the lint can read: Objective-C, a superclass, a protocol.
        func externallyCallable(name: String, modifiers: Set<String>, attributes: Set<String>) {
            if !attributes.isDisjoint(with: ["objc", "IBAction", "IBOutlet", "IBInspectable", "NSManaged", "_cdecl"]) {
                out.unseen.append("it is exposed to Objective-C")
            }
            guard let ownerType else { return }
            let hidden = modifiers.contains("private") || modifiers.contains("fileprivate")
            for base in ancestors(ownerType).sorted() where base != ownerType {
                if typesByName[base] != nil { continue }   // declared here: calls through it are read by name
                if let requirements = Known.requirements[base] ?? Known.sdkRequirements[base] {
                    if requirements.contains(name) { out.unseen.append("\(base) calls it") }
                    continue
                }
                if Known.neutral.contains(base) { continue }
                if modifiers.contains("override") { out.unseen.append("it overrides a method of \(base), which calls it"); continue }
                if Known.classes.contains(base) { continue }
                if !hidden { out.unseen.append("\(ownerType) inherits from \(base), which the lint cannot read and which may call it") }
            }
        }

        switch frame.kind {
        case .function(let info) where info.kind == .function && !decl.asInitializerOfOwner:
            let member = Member(file: decl.file, ownerType: ownerType, local: local, isStatic: info.isStatic || ownerType == nil,
                                privateScope: info.modifiers.contains("private") || info.modifiers.contains("fileprivate") || local != nil)
            for (file, index) in callsByName[info.name] ?? [] {
                let call = files[file].calls[index]
                var reach = reaches(call.receiver, file: file, frame: call.frame, member)
                guard reach != .no, Self.labelsMatch(call, info) else { continue }
                if call.receiver == .implicit, !call.chained, let ownerType, let returns = info.returns, returns != "Self",
                   !isTypeVariable(returns), !related(returns, ownerType),
                   typesByName[ownerType]?.contains(where: { files[$0.file].types[$0.index].kind != .protocol }) == true {
                    // `.name(…)` makes a value of the type the context asks for, and this function makes another.
                    reach = .no
                }
                guard reach != .no else { continue }
                add(file, call.frame, call.line, weak: reach == .maybe)
            }
            for (file, index) in referencesByName[info.name] ?? [] {
                let reference = files[file].references[index]
                // A key path names a property. `.name` alone is a value of the type the context asks for, and what
                // is read, indexed or assigned to is not a function handed on.
                if reference.indirect == .keyPath || reference.readsValue || reference.receiver == .implicit { continue }
                // `return x.name` from a function that returns a named type: a value, not a function.
                if reference.returned, case .function(let around) = files[file].frames[reference.frame].kind,
                   let returns = around.returns, returns != "(function)", !isTypeVariable(returns), aliases[returns] == nil { continue }
                if reference.indirect == .selector {
                    // `#selector(name)`, `#selector(T.name)`: an instance method either way.
                    var either = member
                    either.isStatic.toggle()
                    if reaches(reference.receiver, file: file, frame: reference.frame, member) != .no
                        || reaches(reference.receiver, file: file, frame: reference.frame, either) != .no {
                        out.unseen.append("\(files[file].path):\(reference.line) names it in #selector")
                    }
                    continue
                }
                let reach = reaches(reference.receiver, file: file, frame: reference.frame, member)
                guard reach != .no else { continue }
                // `values.map(read)`: called before `map` returns, like a closure there would be.
                if let callee = reference.argumentOf, let last = callee.last, Known.inline.contains(last),
                   functionsByName[last] == nil, !callee.contains("lazy") {
                    add(file, reference.frame, reference.line, weak: reach == .maybe)
                    continue
                }
                out.unseen.append(reach == .yes ? "\(files[file].path):\(reference.line) passes it on as a value"
                    : "\(files[file].path):\(reference.line) may pass it on as a value (the lint cannot read the type of the value before `.\(info.name)`)")
            }
            if info.isOperator { out.unseen.append("an operator is called wherever it is used") }
            externallyCallable(name: info.name, modifiers: info.modifiers, attributes: info.attributes)
        case .function(let info) where info.kind == .subscripted:
            out.unseen.append("a subscript is called wherever the type is indexed")
        case .function,
             .type,
             .ext,
             .property where decl.asInitializerOfOwner:
            // An initializer (or what runs in every initializer): `T(…)`, `T.init(…)`, `.init(…)`, `Self(…)`,
            // `self.init(…)`, `super.init(…)`.
            guard let ownerType else { out.unseen.append("an initializer outside a type"); break }
            var function: FunctionInfo?
            if case .function(let info) = frame.kind, !decl.asInitializerOfOwner { function = info }
            let privateScope = function.map { $0.modifiers.contains("private") || $0.modifiers.contains("fileprivate") } ?? false
            for name in [ownerType, "init", "Self"] + (aliases[ownerType] ?? []).sorted() {
                for (file, index) in callsByName[name] ?? [] {
                    let call = files[file].calls[index]
                    if privateScope, file != decl.file { continue }
                    let lexical = enclosingOwners(file, call.frame).first
                    let reached: Reach
                    if name == "init" {
                        switch call.receiver {
                        case .type(let type): reached = related(type, ownerType) ? .yes : isTypeVariable(type) ? .maybe : .no
                        case .selfType, .selfInstance: reached = related(lexical, ownerType) ? .yes : .no
                        case .superInstance: reached = lexical.map { inheritsTransitively($0, from: ownerType) } == true ? .yes : .no
                        case .implicit, .metatype: reached = .maybe
                        case .variable, .member, .result, .other: reached = metatypeEscapes(ownerType) ? .maybe : .no
                        case .none, .construct: reached = .no
                        }
                    } else if name != "Self" {
                        // `T(…)`, or `Alias(…)`.
                        reached = .yes
                    } else {
                        reached = call.receiver == .none && related(lexical, ownerType) ? .yes : .no
                    }
                    guard reached != .no, function.map({ Self.labelsMatch(call, $0) }) ?? true else { continue }
                    add(file, call.frame, call.line, weak: reached == .maybe)
                }
            }
            // `T.init` handed on as a function.
            for (file, index) in referencesByName["init"] ?? [] {
                let reference = files[file].references[index]
                guard case .type(let type) = reference.receiver, type == ownerType || isTypeVariable(type) else { continue }
                out.unseen.append("\(files[file].path):\(reference.line) passes \(ownerType).init on as a value")
            }
            if metatypeEscapes(ownerType), function?.modifiers.contains("required") == true {
                out.unseen.append("\(ownerType).self is passed around, so a required initializer can be called through it")
            }
            externallyCallable(name: "init", modifiers: function?.modifiers ?? [], attributes: function?.attributes ?? [])
            // A subclass's initializers run this one.
            for (name, list) in typesByName where name != ownerType {
                for (file, index) in list where files[file].types[index].kind == .class && files[file].types[index].inherits.first == ownerType {
                    add(file, files[file].types[index].frame, files[file].frames[files[file].types[index].frame].line)
                }
            }
        case .property(let info):
            let member = Member(file: decl.file, ownerType: ownerType, local: local, isStatic: info.isStatic || ownerType == nil,
                                privateScope: info.modifiers.contains("private") || info.modifiers.contains("fileprivate"))
            for name in info.names {
                for (file, index) in referencesByName[name] ?? [] {
                    let reference = files[file].references[index]
                    if reference.indirect != .no {
                        if !member.privateScope || file == decl.file {
                            out.unseen.append("\(files[file].path):\(reference.line) names it in a key path or selector")
                        }
                        continue
                    }
                    let reach = reaches(reference.receiver, file: file, frame: reference.frame, member)
                    guard reach != .no else { continue }
                    add(file, reference.frame, reference.line, weak: reach == .maybe)
                }
                // `name(…)`: calling the property's value reads it.
                for (file, index) in callsByName[name] ?? [] {
                    let call = files[file].calls[index]
                    let reach = reaches(call.receiver, file: file, frame: call.frame, member)
                    guard reach != .no else { continue }
                    add(file, call.frame, call.line, weak: reach == .maybe)
                }
                externallyCallable(name: name, modifiers: info.modifiers, attributes: info.attributes)
            }
        default:
            out.unseen.append("the lint does not trace this kind of declaration")
        }
        callerCache[decl] = out
        return out
    }

    // MARK: Fixpoint

    private var answers: [Node: Answer] = [:]
    private var shapes: [Node: Shape] = [:]
    /// The nodes in the order they were found, so that two runs give the same reasons.
    private var order: [Node] = []

    private func resolve(_ roots: [Node]) {
        // Discover every node the roots depend on.
        var pending = roots
        var dependencies: [Node: [Node]] = [:]
        while let node = pending.popLast() {
            guard shapes[node] == nil else { continue }
            let shape = shape(node)
            shapes[node] = shape
            order.append(node)
            answers[node] = Answer()
            var next: [Node] = []
            switch shape {
            case .constant: break
            case .same(let other, _): next = [other]
            case .callers(let decl, _): next = callers(of: decl).nodes.map(\.node)
            case .flow(let nodes, _, _): next = nodes.map(\.node)
            }
            dependencies[node] = next
            pending += next
        }
        // Raise answers until nothing changes. Every step is monotone over none < off < unknown < main.
        var changed = true
        while changed {
            changed = false
            for node in order {
                let now = evaluate(node, shapes[node]!)
                if now.level.rawValue > answers[node]!.level.rawValue {
                    answers[node] = now
                    changed = true
                }
            }
        }
    }

    private func evaluate(_ node: Node, _ shape: Shape) -> Answer {
        switch shape {
        case .constant(let verdict, let reason):
            return Answer(level: verdict == .main ? .main : verdict == .unknown ? .unknown : .off, reason: reason)
        case .same(let other, _):
            return answers[other] ?? Answer()
        case .flow(let nodes, let unseen, let why):
            var best = strongest(nodes, why)
            if best.level.rawValue < Answer.Level.unknown.rawValue, let first = unseen.first {
                best = Answer(level: .unknown, reason: "\(why), and \(first)")
            }
            return best
        case .callers(let decl, let why):
            let found = callers(of: decl)
            var best = strongest(found.nodes, why)
            if best.level.rawValue < Answer.Level.unknown.rawValue {
                if let unseen = found.unseen.first {
                    return Answer(level: .unknown, reason: "\(why), and \(unseen)")
                }
                if found.nodes.isEmpty {
                    return Answer(level: .unknown, reason: "\(why), and the lint found no call of it")
                }
                if best.level == .off {
                    var sites: [String] = []
                    for edge in found.nodes {
                        let site = "\(files[edge.node.file].path):\(edge.line)"
                        if !sites.contains(site) { sites.append(site) }
                    }
                    let listed = sites.prefix(8).joined(separator: ", ") + (sites.count > 8 ? ", …" : "")
                    best.reason = "\(why); every use the lint found is off the main actor (\(sites.count): \(listed)), e.g. \(best.detail)"
                }
            }
            return best
        }
    }

    /// The highest answer among the places that run a frame. A weak edge counts as unknown where it would be main.
    private func strongest(_ edges: [Edge], _ why: String) -> Answer {
        var best = Answer()
        for edge in edges {
            let answer = answers[edge.node] ?? Answer()
            var level = answer.level
            if edge.weak, level == .main { level = .unknown }
            guard level.rawValue > best.level.rawValue else { continue }
            let place = "\(files[edge.node.file].path):\(edge.line)"
            let detail = edge.weak && answer.level != .off
                ? "it may be called at \(place), on a value whose type the lint cannot read: \(answer.reason)"
                : "called at \(place): \(answer.reason)"
            best = Answer(level: level, reason: "\(why) ← \(detail)", detail: detail)
        }
        return best
    }

    // MARK: Findings

    public func findings() -> [Finding] {
        var roots: [Node] = []
        for (f, file) in files.enumerated() {
            for finding in file.findings {
                for config in configs[f] { roots.append(Node(file: f, frame: finding.frame, config: config)) }
            }
        }
        resolve(roots)
        var out: [Finding] = []
        for (f, file) in files.enumerated() {
            for finding in file.findings {
                var verdict = Verdict.off
                var reason = ""
                if configs[f].isEmpty {
                    verdict = .unknown
                    reason = "no target or package the lint knows compiles this file"
                }
                for config in configs[f] {
                    var answer = answers[Node(file: f, frame: finding.frame, config: config)] ?? Answer()
                    if answer.level == .none {
                        answer = Answer(level: .unknown, reason: "the lint found only calls that lead back to this code")
                    }
                    let one: Verdict = answer.level == .main ? .main : answer.level == .unknown ? .unknown : .off
                    if reason.isEmpty || one > verdict {
                        verdict = one
                        reason = answer.reason
                    }
                }
                if file.hasParseError, verdict == .off {
                    verdict = .unknown
                    reason = "the file has a syntax error, so its structure cannot be trusted"
                }
                out.append(Finding(file: file.path, line: finding.line, column: finding.column, rule: finding.rule,
                                   verdict: verdict, reason: reason))
            }
        }
        return out.sorted { ($0.file, $0.line, $0.column, $0.rule) < ($1.file, $1.line, $1.column, $1.rule) }
    }
}

/// Lets `concurrentPerform` fill disjoint slots of one array.
private struct UncheckedSlots<Element>: @unchecked Sendable {
    let buffer: UnsafeMutableBufferPointer<Element?>
    init(_ buffer: UnsafeMutableBufferPointer<Element?>) { self.buffer = buffer }
    func set(_ index: Int, _ value: Element) { buffer[index] = value }
}
