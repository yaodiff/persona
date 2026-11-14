import Foundation
import PersonaWire

/// A session-local, immutable-at-publication keyed tree. Generated wire envelopes are validated
/// against their canonical schemas; all mutations operate on original JSON, never legacy DTOs.
nonisolated struct ExtensionStreamTree: Equatable, Sendable {
    typealias Raw = ExtensionStreamJSON
    typealias Failure = Raw.Failure
    let session: String
    let epoch: String
    private(set) var version: Int64
    let rootKey: String
    private(set) var view: Raw
    let components: Set<String>

    init(snapshot: Raw, components: Set<String>) throws {
        try ExtensionStreamSchema.check(snapshot, definition: "UIStreamSnapshot")
        let dto = try JSONDecoder().decode(ExtensionUIStreamSnapshot.self, from: snapshot.data)
        self.session = dto.session; self.epoch = dto.epoch; self.version = dto.version
        self.rootKey = dto.rootKey; self.view = snapshot["view"]!; self.components = components
        try validate()
    }

    var nativeView: ExtensionView { get throws { try JSONDecoder().decode(ExtensionView.self, from: view.data) } }

    mutating func apply(_ delta: Raw) throws {
        try ExtensionStreamSchema.check(delta, definition: "UIStreamDelta")
        let dto = try JSONDecoder().decode(ExtensionUIStreamDelta.self, from: delta.data)
        guard dto.session == session, dto.epoch == epoch, dto.baseVersion == version,
              dto.version == version || (version < 9_007_199_254_740_991 && dto.version == version + 1),
              dto.operations.isEmpty || dto.version == version + 1 else { throw Failure.version }
        var staged = self
        for operation in delta["operations"]!.array! { try staged.applyOperation(operation) }
        staged.version = dto.version
        try staged.validate()
        self = staged
    }

    /// Resolve an opaque action ID to its saved node/event slot, including page/section controls.
    /// Action IDs may appear in several slots; a caller must not silently select an ambiguous slot.
    func slots(action: String) -> [(key: String, event: String, saved: Raw)] {
        var result: [(String, String, Raw)] = []
        func visit(_ value: Raw, owner: String, event inherited: String = "press", review: Bool = false) {
            if let object = value.object {
                let key = object["key"]?.string ?? owner
                let ownEvent = object["type"]?.string == "segmented" ? "change" : inherited
                for (name, child) in object {
                    let nextEvent = ["search", "filter", "tabs", "control"].contains(name) ? "change" :
                        (name == "buttons" && (review || object["type"]?.string == "composer") ? "submit" : ownEvent)
                    if child["id"]?.string == action, name != "push" {
                        result.append((key, nextEvent, child))
                    } else { visit(child, owner: key, event: nextEvent, review: name == "review") }
                }
            } else if let array = value.array { for child in array { visit(child, owner: owner, event: inherited) } }
        }
        visit(view, owner: rootKey)
        return result
    }

    private enum Location: Equatable { case root, section(Int), item(Int, Int), tile(Int, Int, Int) }
    private func location(_ key: String) -> Location? {
        if key == rootKey { return .root }
        for (s, section) in (view["sections"]?.array ?? []).enumerated() {
            if section["key"]?.string == key { return .section(s) }
            for (i, item) in (section["items"]?.array ?? []).enumerated() {
                if item["key"]?.string == key { return .item(s, i) }
                for (t, tile) in (item["tiles"]?.array ?? []).enumerated() where tile["key"]?.string == key { return .tile(s, i, t) }
            }
        }
        return nil
    }
    private func payload(_ location: Location) throws -> Raw {
        switch location {
        case .root: view
        case .section(let s): view["sections"]!.array![s]
        case .item(let s, let i): view["sections"]!.array![s]["items"]!.array![i]
        case .tile: throw Failure.topology
        }
    }
    private mutating func setChildren(parent: Location, _ children: [Raw]) throws {
        var root = view.object!; var sections = root["sections"]!.array!
        switch parent {
        case .root: root["sections"] = .array(children)
        case .section(let s):
            var section = sections[s].object!; section["items"] = .array(children)
            sections[s] = .object(section); root["sections"] = .array(sections)
        default: throw Failure.topology
        }
        view = .object(root)
    }
    private func children(_ parent: Location) throws -> [Raw] {
        switch parent {
        case .root: view["sections"]!.array!
        case .section(let s): view["sections"]!.array![s]["items"]!.array!
        default: throw Failure.topology
        }
    }
    private mutating func remove(_ location: Location) throws -> Raw {
        let node = try payload(location)
        switch location {
        case .section(let i): var list = try children(.root); list.remove(at: i); try setChildren(parent: .root, list)
        case .item(let s, let i): var list = try children(.section(s)); list.remove(at: i); try setChildren(parent: .section(s), list)
        default: throw Failure.topology
        }
        return node
    }
    private mutating func insert(_ node: Raw, section: Bool, parent key: String, before: String?) throws {
        guard let parent = location(key) else { throw Failure.topology }
        switch (section, parent) { case (true, .root), (false, .section): break; default: throw Failure.topology }
        var list = try children(parent)
        if let before {
            guard let index = list.firstIndex(where: { $0["key"]?.string == before }) else { throw Failure.topology }
            list.insert(node, at: index)
        } else { list.append(node) }
        try setChildren(parent: parent, list)
    }
    private mutating func applyOperation(_ operation: Raw) throws {
        let op = operation["op"]!.string!
        if op == "insert" {
            let section = operation["section"] != nil
            try insert(operation[section ? "section" : "item"]!, section: section,
                       parent: operation["parent"]!.string!, before: operation["before"]?.string)
            return
        }
        guard let key = operation["key"]?.string, let target = location(key) else { throw Failure.topology }
        switch op {
        case "replace":
            switch target {
            case .root:
                guard let root = operation["view"] else { throw Failure.topology }; view = root
            case .section(let index):
                guard let replacement = operation["section"], replacement["key"]?.string == key else { throw Failure.topology }
                var list = try children(.root); list[index] = replacement; try setChildren(parent: .root, list)
            case .item(let s, let i):
                guard let replacement = operation["item"], replacement["key"]?.string == key else { throw Failure.topology }
                var list = try children(.section(s)); list[i] = replacement; try setChildren(parent: .section(s), list)
            case .tile: throw Failure.topology
            }
        case "remove": _ = try remove(target)
        case "move":
            let section: Bool
            switch target { case .section: section = true; case .item: section = false; default: throw Failure.topology }
            guard operation["parent"]?.string != key, operation["before"]?.string != key else { throw Failure.topology }
            let node = try remove(target)
            try insert(node, section: section, parent: operation["parent"]!.string!, before: operation["before"]?.string)
        default: throw Failure.unsupported
        }
    }

    private func validate() throws {
        try ExtensionStreamSchema.check(view, definition: "UIStreamView")
        guard view.data.count <= 1_048_576 else { throw Failure.bounds }
        var keys: Set<String> = [rootKey]; var count = 1
        func key(_ value: Raw) throws {
            guard let key = value["key"]?.string, (1...128).contains(key.unicodeScalars.count), keys.insert(key).inserted else { throw Failure.topology }
            count += 1; guard count <= 300 else { throw Failure.bounds }
        }
        func depth(_ value: Raw, _ level: Int) throws {
            guard level <= 16 else { throw Failure.bounds }
            if let object = value.object { for value in object.values { try depth(value, level + 1) } }
            if let array = value.array { for value in array { try depth(value, level + 1) } }
        }
        try depth(view, 1)
        var actions: Set<String> = []
        func semantics(_ raw: Raw, isAction: Bool = false) throws {
            if let object = raw.object {
                if isAction, let id = object["id"]?.string {
                    guard (1...128).contains(id.unicodeScalars.count), actions.insert(id).inserted else { throw Failure.topology }
                }
                for field in ["url", "line_url"] {
                    if var text = object[field]?.string {
                        if field == "line_url", text.isEmpty { continue }
                        text = text.replacingOccurrences(of: "{start}", with: "1").replacingOccurrences(of: "{end}", with: "1")
                        guard let url = URL(string: text), url.scheme?.lowercased() == "https", url.host()?.isEmpty == false,
                              url.user == nil, url.password == nil else { throw Failure.schema }
                    }
                }
                if let control = object["control"], control["on"] != nil, control["kind"]?.string != "switch" { throw Failure.schema }
                if let name = object["name"]?.string {
                    guard (1...128).contains(name.unicodeScalars.count), !name.hasPrefix("~") else { throw Failure.schema }
                }
                let actionFields: Set<String> = ["action", "actions", "load", "expand", "comment", "reply", "context", "mark_viewed", "draft", "resolve", "discard"]
                for (name, child) in object where name != "values" { try semantics(child, isAction: actionFields.contains(name)) }
                if let values = object["values"]?.object {
                    guard values.keys.allSatisfy({ (1...128).contains($0.unicodeScalars.count) && !$0.hasPrefix("~") }) else { throw Failure.schema }
                }
            } else if let array = raw.array { for child in array { try semantics(child, isAction: isAction) } }
        }
        try semantics(view)
        for section in view["sections"]!.array! {
            try key(section)
            for item in section["items"]!.array! {
                try key(item)
                guard let type = item["type"]?.string, components.contains(type) else { throw Failure.unsupported }
                for tile in item["tiles"]?.array ?? [] { try key(tile) }
            }
        }
    }
}
