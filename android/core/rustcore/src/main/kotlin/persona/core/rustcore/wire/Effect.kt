package persona.core.rustcore.wire


sealed class Effect {
    @Throws(com.novi.serde.SerializationError::class)
    abstract fun serialize(serializer: com.novi.serde.Serializer)

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): Effect {
            val index = deserializer.deserialize_variant_index()
            return when (index) {
                0 -> Fetch.load(deserializer)
                1 -> Rejected.load(deserializer)
                else -> throw com.novi.serde.DeserializationError("Unknown variant index for Effect: " + index)
            }
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): Effect {
            val deserializer = com.novi.bincode.BincodeDeserializer(input)
            val value = deserialize(deserializer)
            if (deserializer.get_buffer_offset() < input.size) {
                throw com.novi.serde.DeserializationError("Some input bytes were not read")
            }
            return value
        }
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun bincodeSerialize(): ByteArray {
        val serializer = com.novi.bincode.BincodeSerializer()
        serialize(serializer)
        return serializer.get_bytes()
    }

    data class Fetch(
        val chat: String,
        val request: persona.core.rustcore.wire.Request
    ) : Effect() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(0)
            serializer.serialize_str(this.chat)
            this.request.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Fetch {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                val request = persona.core.rustcore.wire.Request.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return Fetch(chat, request)
            }
        }
    }

    data class Rejected(
        val reason: String
    ) : Effect() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(1)
            serializer.serialize_str(this.reason)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Rejected {
                deserializer.increase_container_depth()
                val reason = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Rejected(reason)
            }
        }
    }
}

