package persona.core.rustcore.wire


data class WireBatch(
    val items_json: String,
    val history: String?,
    val from_seq: Long?,
    val to_seq: Long?,
    val backlog: Boolean,
    val has_more: Boolean?,
    val more_after: Boolean
) {

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize(serializer: com.novi.serde.Serializer) {
        serializer.increase_container_depth()
        serializer.serialize_str(this.items_json)
        TraitHelpers.serialize_option_str(this.history, serializer)
        TraitHelpers.serialize_option_i64(this.from_seq, serializer)
        TraitHelpers.serialize_option_i64(this.to_seq, serializer)
        serializer.serialize_bool(this.backlog)
        TraitHelpers.serialize_option_bool(this.has_more, serializer)
        serializer.serialize_bool(this.more_after)
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
        fun deserialize(deserializer: com.novi.serde.Deserializer): WireBatch {
            deserializer.increase_container_depth()
            val items_json = deserializer.deserialize_str()
            val history = TraitHelpers.deserialize_option_str(deserializer)
            val from_seq = TraitHelpers.deserialize_option_i64(deserializer)
            val to_seq = TraitHelpers.deserialize_option_i64(deserializer)
            val backlog = deserializer.deserialize_bool()
            val has_more = TraitHelpers.deserialize_option_bool(deserializer)
            val more_after = deserializer.deserialize_bool()
            deserializer.decrease_container_depth()
            return WireBatch(items_json, history, from_seq, to_seq, backlog, has_more, more_after)
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): WireBatch {
            val deserializer = com.novi.bincode.BincodeDeserializer(input)
            val value = deserialize(deserializer)
            if (deserializer.get_buffer_offset() < input.size) {
                throw com.novi.serde.DeserializationError("Some input bytes were not read")
            }
            return value
        }
    }
}
