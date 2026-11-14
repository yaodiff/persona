package persona.core.rustcore.wire


data class ChatSnapshot(
    val items_json: String,
    val history: String?,
    val covered: Long,
    val has_more: Boolean?,
    val loaded_once: Boolean
) {

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize(serializer: com.novi.serde.Serializer) {
        serializer.increase_container_depth()
        serializer.serialize_str(this.items_json)
        TraitHelpers.serialize_option_str(this.history, serializer)
        serializer.serialize_i64(this.covered)
        TraitHelpers.serialize_option_bool(this.has_more, serializer)
        serializer.serialize_bool(this.loaded_once)
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
        fun deserialize(deserializer: com.novi.serde.Deserializer): ChatSnapshot {
            deserializer.increase_container_depth()
            val items_json = deserializer.deserialize_str()
            val history = TraitHelpers.deserialize_option_str(deserializer)
            val covered = deserializer.deserialize_i64()
            val has_more = TraitHelpers.deserialize_option_bool(deserializer)
            val loaded_once = deserializer.deserialize_bool()
            deserializer.decrease_container_depth()
            return ChatSnapshot(items_json, history, covered, has_more, loaded_once)
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): ChatSnapshot {
            val deserializer = com.novi.bincode.BincodeDeserializer(input)
            val value = deserialize(deserializer)
            if (deserializer.get_buffer_offset() < input.size) {
                throw com.novi.serde.DeserializationError("Some input bytes were not read")
            }
            return value
        }
    }
}
