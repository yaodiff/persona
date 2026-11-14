package persona.core.rustcore.wire


sealed class Action {
    @Throws(com.novi.serde.SerializationError::class)
    abstract fun serialize(serializer: com.novi.serde.Serializer)

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): Action {
            val index = deserializer.deserialize_variant_index()
            return when (index) {
                0 -> OpenChat.load(deserializer)
                1 -> CloseChat.load(deserializer)
                2 -> Chat.load(deserializer)
                3 -> WirePayload.load(deserializer)
                4 -> ContinueFill.load(deserializer)
                5 -> HubChanged.load(deserializer)
                6 -> Ack.load(deserializer)
                else -> throw com.novi.serde.DeserializationError("Unknown variant index for Action: " + index)
            }
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): Action {
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

    data class OpenChat(
        val chat: String,
        val snapshot: persona.core.rustcore.wire.ChatSnapshot
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(0)
            serializer.serialize_str(this.chat)
            this.snapshot.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): OpenChat {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                val snapshot = persona.core.rustcore.wire.ChatSnapshot.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return OpenChat(chat, snapshot)
            }
        }
    }

    data class CloseChat(
        val chat: String
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(1)
            serializer.serialize_str(this.chat)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): CloseChat {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return CloseChat(chat)
            }
        }
    }

    data class Chat(
        val chat: String,
        val event: persona.core.rustcore.wire.ChatEvent
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(2)
            serializer.serialize_str(this.chat)
            this.event.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Chat {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                val event = persona.core.rustcore.wire.ChatEvent.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return Chat(chat, event)
            }
        }
    }

    data class WirePayload(
        val chat: String,
        val payload: persona.core.rustcore.wire.Payload
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(3)
            serializer.serialize_str(this.chat)
            this.payload.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): WirePayload {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                val payload = persona.core.rustcore.wire.Payload.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return WirePayload(chat, payload)
            }
        }
    }

    data class ContinueFill(
        val chat: String
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(4)
            serializer.serialize_str(this.chat)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): ContinueFill {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return ContinueFill(chat)
            }
        }
    }

    data class HubChanged(
        val chat: String
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(5)
            serializer.serialize_str(this.chat)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): HubChanged {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return HubChanged(chat)
            }
        }
    }

    data class Ack(
        val chat: String,
        val seq: ULong
    ) : Action() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(6)
            serializer.serialize_str(this.chat)
            serializer.serialize_u64(this.seq)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Ack {
                deserializer.increase_container_depth()
                val chat = deserializer.deserialize_str()
                val seq = deserializer.deserialize_u64()
                deserializer.decrease_container_depth()
                return Ack(chat, seq)
            }
        }
    }
}

