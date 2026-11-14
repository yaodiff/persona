package persona.core.rustcore.wire


data class ChatPatch(
    val version: ULong,
    val covered: Long,
    val history: String?,
    val has_more: Boolean?,
    val loaded_once: Boolean,
    val verified: Boolean,
    val acked: ULong?,
    val ops: kotlin.collections.List<persona.core.rustcore.wire.ListOp>
) {

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize(serializer: com.novi.serde.Serializer) {
        serializer.increase_container_depth()
        serializer.serialize_u64(this.version)
        serializer.serialize_i64(this.covered)
        TraitHelpers.serialize_option_str(this.history, serializer)
        TraitHelpers.serialize_option_bool(this.has_more, serializer)
        serializer.serialize_bool(this.loaded_once)
        serializer.serialize_bool(this.verified)
        TraitHelpers.serialize_option_u64(this.acked, serializer)
        TraitHelpers.serialize_vector_ListOp(this.ops, serializer)
        serializer.decrease_container_depth()
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun bincodeSerialize(): ByteArray {
        val serializer = com.novi.bincode.BincodeSerializer()
        serialize(serializer)
        return serializer.get_bytes()
    }

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): ChatPatch {
            deserializer.increase_container_depth()
            val version = deserializer.deserialize_u64()
            val covered = deserializer.deserialize_i64()
            val history = TraitHelpers.deserialize_option_str(deserializer)
            val has_more = TraitHelpers.deserialize_option_bool(deserializer)
            val loaded_once = deserializer.deserialize_bool()
            val verified = deserializer.deserialize_bool()
            val acked = TraitHelpers.deserialize_option_u64(deserializer)
            val ops = TraitHelpers.deserialize_vector_ListOp(deserializer)
            deserializer.decrease_container_depth()
            return ChatPatch(version, covered, history, has_more, loaded_once, verified, acked, ops)
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): ChatPatch {
            val deserializer = com.novi.bincode.BincodeDeserializer(input)
            val value = deserialize(deserializer)
            if (deserializer.get_buffer_offset() < input.size) {
                throw com.novi.serde.DeserializationError("Some input bytes were not read")
            }
            return value
        }
    }
}
