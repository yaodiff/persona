import PersonaCoreSerde


indirect public enum Action: Hashable, Sendable {
    case openChat(chat: String, snapshot: PersonaCoreWire.ChatSnapshot)
    case closeChat(chat: String)
    case chat(chat: String, event: PersonaCoreWire.ChatEvent)
    case wirePayload(chat: String, payload: PersonaCoreWire.Payload)
    case continueFill(chat: String)
    case hubChanged(chat: String)
    case ack(chat: String, seq: UInt64)

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        switch self {
        case .openChat(let chat, let snapshot):
            try serializer.serialize_variant_index(value: 0)
            try serializer.serialize_str(value: chat)
            try snapshot.serialize(serializer: serializer)
        case .closeChat(let chat):
            try serializer.serialize_variant_index(value: 1)
            try serializer.serialize_str(value: chat)
        case .chat(let chat, let event):
            try serializer.serialize_variant_index(value: 2)
            try serializer.serialize_str(value: chat)
            try event.serialize(serializer: serializer)
        case .wirePayload(let chat, let payload):
            try serializer.serialize_variant_index(value: 3)
            try serializer.serialize_str(value: chat)
            try payload.serialize(serializer: serializer)
        case .continueFill(let chat):
            try serializer.serialize_variant_index(value: 4)
            try serializer.serialize_str(value: chat)
        case .hubChanged(let chat):
            try serializer.serialize_variant_index(value: 5)
            try serializer.serialize_str(value: chat)
        case .ack(let chat, let seq):
            try serializer.serialize_variant_index(value: 6)
            try serializer.serialize_str(value: chat)
            try serializer.serialize_u64(value: seq)
        }
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> Action {
        let index = try deserializer.deserialize_variant_index()
        try deserializer.increase_container_depth()
        switch index {
        case 0:
            let chat = try deserializer.deserialize_str()
            let snapshot = try PersonaCoreWire.ChatSnapshot.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .openChat(chat: chat, snapshot: snapshot)
        case 1:
            let chat = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .closeChat(chat: chat)
        case 2:
            let chat = try deserializer.deserialize_str()
            let event = try PersonaCoreWire.ChatEvent.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .chat(chat: chat, event: event)
        case 3:
            let chat = try deserializer.deserialize_str()
            let payload = try PersonaCoreWire.Payload.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .wirePayload(chat: chat, payload: payload)
        case 4:
            let chat = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .continueFill(chat: chat)
        case 5:
            let chat = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .hubChanged(chat: chat)
        case 6:
            let chat = try deserializer.deserialize_str()
            let seq = try deserializer.deserialize_u64()
            try deserializer.decrease_container_depth()
            return .ack(chat: chat, seq: seq)
        default: throw DeserializationError.invalidInput(issue: "Unknown variant index for Action: \(index)")
        }
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> Action {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

indirect public enum ChatEvent: Hashable, Sendable {
    case latest(batch: PersonaCoreWire.WireBatch)
    case pushed(batch: PersonaCoreWire.WireBatch)
    case after(batch: PersonaCoreWire.WireBatch)
    case reloaded(items_json: String, history: String?, to_seq: Int64?, has_more: Bool)
    case older(items_json: String, has_more: Bool)
    case range(fold_id: String?, items_json: String, more: Bool)
    case reconnected
    case loadOlder
    case openFold(fold_id: String)
    case fillFailed
    case olderFailed
    case rangeFailed(fold_id: String?)

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        switch self {
        case .latest(let batch):
            try serializer.serialize_variant_index(value: 0)
            try batch.serialize(serializer: serializer)
        case .pushed(let batch):
            try serializer.serialize_variant_index(value: 1)
            try batch.serialize(serializer: serializer)
        case .after(let batch):
            try serializer.serialize_variant_index(value: 2)
            try batch.serialize(serializer: serializer)
        case .reloaded(let items_json, let history, let to_seq, let has_more):
            try serializer.serialize_variant_index(value: 3)
            try serializer.serialize_str(value: items_json)
            try serialize_option_str(value: history, serializer: serializer)
            try serialize_option_i64(value: to_seq, serializer: serializer)
            try serializer.serialize_bool(value: has_more)
        case .older(let items_json, let has_more):
            try serializer.serialize_variant_index(value: 4)
            try serializer.serialize_str(value: items_json)
            try serializer.serialize_bool(value: has_more)
        case .range(let fold_id, let items_json, let more):
            try serializer.serialize_variant_index(value: 5)
            try serialize_option_str(value: fold_id, serializer: serializer)
            try serializer.serialize_str(value: items_json)
            try serializer.serialize_bool(value: more)
        case .reconnected:
            try serializer.serialize_variant_index(value: 6)
        case .loadOlder:
            try serializer.serialize_variant_index(value: 7)
        case .openFold(let fold_id):
            try serializer.serialize_variant_index(value: 8)
            try serializer.serialize_str(value: fold_id)
        case .fillFailed:
            try serializer.serialize_variant_index(value: 9)
        case .olderFailed:
            try serializer.serialize_variant_index(value: 10)
        case .rangeFailed(let fold_id):
            try serializer.serialize_variant_index(value: 11)
            try serialize_option_str(value: fold_id, serializer: serializer)
        }
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> ChatEvent {
        let index = try deserializer.deserialize_variant_index()
        try deserializer.increase_container_depth()
        switch index {
        case 0:
            let batch = try PersonaCoreWire.WireBatch.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .latest(batch: batch)
        case 1:
            let batch = try PersonaCoreWire.WireBatch.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .pushed(batch: batch)
        case 2:
            let batch = try PersonaCoreWire.WireBatch.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .after(batch: batch)
        case 3:
            let items_json = try deserializer.deserialize_str()
            let history = try deserialize_option_str(deserializer: deserializer)
            let to_seq = try deserialize_option_i64(deserializer: deserializer)
            let has_more = try deserializer.deserialize_bool()
            try deserializer.decrease_container_depth()
            return .reloaded(items_json: items_json, history: history, to_seq: to_seq, has_more: has_more)
        case 4:
            let items_json = try deserializer.deserialize_str()
            let has_more = try deserializer.deserialize_bool()
            try deserializer.decrease_container_depth()
            return .older(items_json: items_json, has_more: has_more)
        case 5:
            let fold_id = try deserialize_option_str(deserializer: deserializer)
            let items_json = try deserializer.deserialize_str()
            let more = try deserializer.deserialize_bool()
            try deserializer.decrease_container_depth()
            return .range(fold_id: fold_id, items_json: items_json, more: more)
        case 6:
            try deserializer.decrease_container_depth()
            return .reconnected
        case 7:
            try deserializer.decrease_container_depth()
            return .loadOlder
        case 8:
            let fold_id = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .openFold(fold_id: fold_id)
        case 9:
            try deserializer.decrease_container_depth()
            return .fillFailed
        case 10:
            try deserializer.decrease_container_depth()
            return .olderFailed
        case 11:
            let fold_id = try deserialize_option_str(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .rangeFailed(fold_id: fold_id)
        default: throw DeserializationError.invalidInput(issue: "Unknown variant index for ChatEvent: \(index)")
        }
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> ChatEvent {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

public struct ChatPatch: Hashable, Sendable {
    @Indirect public var version: UInt64
    @Indirect public var covered: Int64
    @Indirect public var history: String?
    @Indirect public var hasMore: Bool?
    @Indirect public var loadedOnce: Bool
    @Indirect public var verified: Bool
    @Indirect public var acked: UInt64?
    @Indirect public var ops: [PersonaCoreWire.ListOp]

    public init(version: UInt64, covered: Int64, history: String?, hasMore: Bool?, loadedOnce: Bool, verified: Bool, acked: UInt64?, ops: [PersonaCoreWire.ListOp]) {
        self.version = version
        self.covered = covered
        self.history = history
        self.hasMore = hasMore
        self.loadedOnce = loadedOnce
        self.verified = verified
        self.acked = acked
        self.ops = ops
    }

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        try serializer.serialize_u64(value: self.version)
        try serializer.serialize_i64(value: self.covered)
        try serialize_option_str(value: self.history, serializer: serializer)
        try serialize_option_bool(value: self.hasMore, serializer: serializer)
        try serializer.serialize_bool(value: self.loadedOnce)
        try serializer.serialize_bool(value: self.verified)
        try serialize_option_u64(value: self.acked, serializer: serializer)
        try serialize_vector_ListOp(value: self.ops, serializer: serializer)
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> ChatPatch {
        try deserializer.increase_container_depth()
        let version = try deserializer.deserialize_u64()
        let covered = try deserializer.deserialize_i64()
        let history = try deserialize_option_str(deserializer: deserializer)
        let hasMore = try deserialize_option_bool(deserializer: deserializer)
        let loadedOnce = try deserializer.deserialize_bool()
        let verified = try deserializer.deserialize_bool()
        let acked = try deserialize_option_u64(deserializer: deserializer)
        let ops = try deserialize_vector_ListOp(deserializer: deserializer)
        try deserializer.decrease_container_depth()
        return ChatPatch.init(version: version, covered: covered, history: history, hasMore: hasMore, loadedOnce: loadedOnce, verified: verified, acked: acked, ops: ops)
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> ChatPatch {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

public struct ChatSnapshot: Hashable, Sendable {
    @Indirect public var itemsJson: String
    @Indirect public var history: String?
    @Indirect public var covered: Int64
    @Indirect public var hasMore: Bool?
    @Indirect public var loadedOnce: Bool

    public init(itemsJson: String, history: String?, covered: Int64, hasMore: Bool?, loadedOnce: Bool) {
        self.itemsJson = itemsJson
        self.history = history
        self.covered = covered
        self.hasMore = hasMore
        self.loadedOnce = loadedOnce
    }

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        try serializer.serialize_str(value: self.itemsJson)
        try serialize_option_str(value: self.history, serializer: serializer)
        try serializer.serialize_i64(value: self.covered)
        try serialize_option_bool(value: self.hasMore, serializer: serializer)
        try serializer.serialize_bool(value: self.loadedOnce)
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> ChatSnapshot {
        try deserializer.increase_container_depth()
        let itemsJson = try deserializer.deserialize_str()
        let history = try deserialize_option_str(deserializer: deserializer)
        let covered = try deserializer.deserialize_i64()
        let hasMore = try deserialize_option_bool(deserializer: deserializer)
        let loadedOnce = try deserializer.deserialize_bool()
        try deserializer.decrease_container_depth()
        return ChatSnapshot.init(itemsJson: itemsJson, history: history, covered: covered, hasMore: hasMore, loadedOnce: loadedOnce)
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> ChatSnapshot {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

public struct Config: Hashable, Sendable {

    public init() {
    }

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> Config {
        try deserializer.increase_container_depth()
        try deserializer.decrease_container_depth()
        return Config.init()
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> Config {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

indirect public enum Effect: Hashable, Sendable {
    case fetch(chat: String, request: PersonaCoreWire.Request)
    case rejected(reason: String)

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        switch self {
        case .fetch(let chat, let request):
            try serializer.serialize_variant_index(value: 0)
            try serializer.serialize_str(value: chat)
            try request.serialize(serializer: serializer)
        case .rejected(let reason):
            try serializer.serialize_variant_index(value: 1)
            try serializer.serialize_str(value: reason)
        }
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> Effect {
        let index = try deserializer.deserialize_variant_index()
        try deserializer.increase_container_depth()
        switch index {
        case 0:
            let chat = try deserializer.deserialize_str()
            let request = try PersonaCoreWire.Request.deserialize(deserializer: deserializer)
            try deserializer.decrease_container_depth()
            return .fetch(chat: chat, request: request)
        case 1:
            let reason = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .rejected(reason: reason)
        default: throw DeserializationError.invalidInput(issue: "Unknown variant index for Effect: \(index)")
        }
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> Effect {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

indirect public enum ListOp: Hashable, Sendable {
    case remove(index: UInt32)
    case insert(index: UInt32, id: String, json: String)
    case update(index: UInt32, id: String, json: String)

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        switch self {
        case .remove(let index):
            try serializer.serialize_variant_index(value: 0)
            try serializer.serialize_u32(value: index)
        case .insert(let index, let id, let json):
            try serializer.serialize_variant_index(value: 1)
            try serializer.serialize_u32(value: index)
            try serializer.serialize_str(value: id)
            try serializer.serialize_str(value: json)
        case .update(let index, let id, let json):
            try serializer.serialize_variant_index(value: 2)
            try serializer.serialize_u32(value: index)
            try serializer.serialize_str(value: id)
            try serializer.serialize_str(value: json)
        }
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> ListOp {
        let index = try deserializer.deserialize_variant_index()
        try deserializer.increase_container_depth()
        switch index {
        case 0:
            let index = try deserializer.deserialize_u32()
            try deserializer.decrease_container_depth()
            return .remove(index: index)
        case 1:
            let index = try deserializer.deserialize_u32()
            let id = try deserializer.deserialize_str()
            let json = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .insert(index: index, id: id, json: json)
        case 2:
            let index = try deserializer.deserialize_u32()
            let id = try deserializer.deserialize_str()
            let json = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .update(index: index, id: id, json: json)
        default: throw DeserializationError.invalidInput(issue: "Unknown variant index for ListOp: \(index)")
        }
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> ListOp {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

indirect public enum Payload: Hashable, Sendable {
    case latest(body: String)
    case newer(body: String)
    case reloaded(body: String)
    case older(body: String)
    case range(fold_id: String?, body: String)
    case frame(body: String)

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        switch self {
        case .latest(let body):
            try serializer.serialize_variant_index(value: 0)
            try serializer.serialize_str(value: body)
        case .newer(let body):
            try serializer.serialize_variant_index(value: 1)
            try serializer.serialize_str(value: body)
        case .reloaded(let body):
            try serializer.serialize_variant_index(value: 2)
            try serializer.serialize_str(value: body)
        case .older(let body):
            try serializer.serialize_variant_index(value: 3)
            try serializer.serialize_str(value: body)
        case .range(let fold_id, let body):
            try serializer.serialize_variant_index(value: 4)
            try serialize_option_str(value: fold_id, serializer: serializer)
            try serializer.serialize_str(value: body)
        case .frame(let body):
            try serializer.serialize_variant_index(value: 5)
            try serializer.serialize_str(value: body)
        }
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> Payload {
        let index = try deserializer.deserialize_variant_index()
        try deserializer.increase_container_depth()
        switch index {
        case 0:
            let body = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .latest(body: body)
        case 1:
            let body = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .newer(body: body)
        case 2:
            let body = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .reloaded(body: body)
        case 3:
            let body = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .older(body: body)
        case 4:
            let fold_id = try deserialize_option_str(deserializer: deserializer)
            let body = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .range(fold_id: fold_id, body: body)
        case 5:
            let body = try deserializer.deserialize_str()
            try deserializer.decrease_container_depth()
            return .frame(body: body)
        default: throw DeserializationError.invalidInput(issue: "Unknown variant index for Payload: \(index)")
        }
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> Payload {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

indirect public enum Request: Hashable, Sendable {
    case fetchAfter(after_seq: Int64)
    case fetchLatest(fold_work: Bool)
    case subscribe(since_seq: Int64?, fold_work: Bool)
    case fetchOlder(before_seq: Int64, fold_work: Bool)
    case fetchRange(fold_id: String?, from_seq: Int64, through_seq: Int64)

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        switch self {
        case .fetchAfter(let after_seq):
            try serializer.serialize_variant_index(value: 0)
            try serializer.serialize_i64(value: after_seq)
        case .fetchLatest(let fold_work):
            try serializer.serialize_variant_index(value: 1)
            try serializer.serialize_bool(value: fold_work)
        case .subscribe(let since_seq, let fold_work):
            try serializer.serialize_variant_index(value: 2)
            try serialize_option_i64(value: since_seq, serializer: serializer)
            try serializer.serialize_bool(value: fold_work)
        case .fetchOlder(let before_seq, let fold_work):
            try serializer.serialize_variant_index(value: 3)
            try serializer.serialize_i64(value: before_seq)
            try serializer.serialize_bool(value: fold_work)
        case .fetchRange(let fold_id, let from_seq, let through_seq):
            try serializer.serialize_variant_index(value: 4)
            try serialize_option_str(value: fold_id, serializer: serializer)
            try serializer.serialize_i64(value: from_seq)
            try serializer.serialize_i64(value: through_seq)
        }
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> Request {
        let index = try deserializer.deserialize_variant_index()
        try deserializer.increase_container_depth()
        switch index {
        case 0:
            let after_seq = try deserializer.deserialize_i64()
            try deserializer.decrease_container_depth()
            return .fetchAfter(after_seq: after_seq)
        case 1:
            let fold_work = try deserializer.deserialize_bool()
            try deserializer.decrease_container_depth()
            return .fetchLatest(fold_work: fold_work)
        case 2:
            let since_seq = try deserialize_option_i64(deserializer: deserializer)
            let fold_work = try deserializer.deserialize_bool()
            try deserializer.decrease_container_depth()
            return .subscribe(since_seq: since_seq, fold_work: fold_work)
        case 3:
            let before_seq = try deserializer.deserialize_i64()
            let fold_work = try deserializer.deserialize_bool()
            try deserializer.decrease_container_depth()
            return .fetchOlder(before_seq: before_seq, fold_work: fold_work)
        case 4:
            let fold_id = try deserialize_option_str(deserializer: deserializer)
            let from_seq = try deserializer.deserialize_i64()
            let through_seq = try deserializer.deserialize_i64()
            try deserializer.decrease_container_depth()
            return .fetchRange(fold_id: fold_id, from_seq: from_seq, through_seq: through_seq)
        default: throw DeserializationError.invalidInput(issue: "Unknown variant index for Request: \(index)")
        }
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> Request {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

public struct WireBatch: Hashable, Sendable {
    @Indirect public var itemsJson: String
    @Indirect public var history: String?
    @Indirect public var fromSeq: Int64?
    @Indirect public var toSeq: Int64?
    @Indirect public var backlog: Bool
    @Indirect public var hasMore: Bool?
    @Indirect public var moreAfter: Bool

    public init(itemsJson: String, history: String?, fromSeq: Int64?, toSeq: Int64?, backlog: Bool, hasMore: Bool?, moreAfter: Bool) {
        self.itemsJson = itemsJson
        self.history = history
        self.fromSeq = fromSeq
        self.toSeq = toSeq
        self.backlog = backlog
        self.hasMore = hasMore
        self.moreAfter = moreAfter
    }

    public func serialize<S: Serializer>(serializer: S) throws {
        try serializer.increase_container_depth()
        try serializer.serialize_str(value: self.itemsJson)
        try serialize_option_str(value: self.history, serializer: serializer)
        try serialize_option_i64(value: self.fromSeq, serializer: serializer)
        try serialize_option_i64(value: self.toSeq, serializer: serializer)
        try serializer.serialize_bool(value: self.backlog)
        try serialize_option_bool(value: self.hasMore, serializer: serializer)
        try serializer.serialize_bool(value: self.moreAfter)
        try serializer.decrease_container_depth()
    }

    public func bincodeSerialize() throws -> [UInt8] {
        let serializer = BincodeSerializer.init();
        try self.serialize(serializer: serializer)
        return serializer.get_bytes()
    }

    public static func deserialize<D: Deserializer>(deserializer: D) throws -> WireBatch {
        try deserializer.increase_container_depth()
        let itemsJson = try deserializer.deserialize_str()
        let history = try deserialize_option_str(deserializer: deserializer)
        let fromSeq = try deserialize_option_i64(deserializer: deserializer)
        let toSeq = try deserialize_option_i64(deserializer: deserializer)
        let backlog = try deserializer.deserialize_bool()
        let hasMore = try deserialize_option_bool(deserializer: deserializer)
        let moreAfter = try deserializer.deserialize_bool()
        try deserializer.decrease_container_depth()
        return WireBatch.init(itemsJson: itemsJson, history: history, fromSeq: fromSeq, toSeq: toSeq, backlog: backlog, hasMore: hasMore, moreAfter: moreAfter)
    }

    public static func bincodeDeserialize(input: [UInt8]) throws -> WireBatch {
        let deserializer = BincodeDeserializer.init(input: input);
        let obj = try deserialize(deserializer: deserializer)
        if deserializer.get_buffer_offset() < input.count {
            throw DeserializationError.invalidInput(issue: "Some input bytes were not read")
        }
        return obj
    }
}

func serialize_option_bool<S: Serializer>(value: Bool?, serializer: S) throws {
    if let value = value {
        try serializer.serialize_option_tag(value: true)
        try serializer.serialize_bool(value: value)
    } else {
        try serializer.serialize_option_tag(value: false)
    }
}

func deserialize_option_bool<D: Deserializer>(deserializer: D) throws -> Bool? {
    let tag = try deserializer.deserialize_option_tag()
    if tag {
        return try deserializer.deserialize_bool()
    } else {
        return nil
    }
}

func serialize_option_i64<S: Serializer>(value: Int64?, serializer: S) throws {
    if let value = value {
        try serializer.serialize_option_tag(value: true)
        try serializer.serialize_i64(value: value)
    } else {
        try serializer.serialize_option_tag(value: false)
    }
}

func deserialize_option_i64<D: Deserializer>(deserializer: D) throws -> Int64? {
    let tag = try deserializer.deserialize_option_tag()
    if tag {
        return try deserializer.deserialize_i64()
    } else {
        return nil
    }
}

func serialize_option_str<S: Serializer>(value: String?, serializer: S) throws {
    if let value = value {
        try serializer.serialize_option_tag(value: true)
        try serializer.serialize_str(value: value)
    } else {
        try serializer.serialize_option_tag(value: false)
    }
}

func deserialize_option_str<D: Deserializer>(deserializer: D) throws -> String? {
    let tag = try deserializer.deserialize_option_tag()
    if tag {
        return try deserializer.deserialize_str()
    } else {
        return nil
    }
}

func serialize_option_u64<S: Serializer>(value: UInt64?, serializer: S) throws {
    if let value = value {
        try serializer.serialize_option_tag(value: true)
        try serializer.serialize_u64(value: value)
    } else {
        try serializer.serialize_option_tag(value: false)
    }
}

func deserialize_option_u64<D: Deserializer>(deserializer: D) throws -> UInt64? {
    let tag = try deserializer.deserialize_option_tag()
    if tag {
        return try deserializer.deserialize_u64()
    } else {
        return nil
    }
}

func serialize_vector_ListOp<S: Serializer>(value: [PersonaCoreWire.ListOp], serializer: S) throws {
    try serializer.serialize_len(value: value.count)
    for item in value {
        try item.serialize(serializer: serializer)
    }
}

func deserialize_vector_ListOp<D: Deserializer>(deserializer: D) throws -> [PersonaCoreWire.ListOp] {
    let length = try deserializer.deserialize_len()
    var obj : [PersonaCoreWire.ListOp] = []
    for _ in 0..<length {
        obj.append(try PersonaCoreWire.ListOp.deserialize(deserializer: deserializer))
    }
    return obj
}
