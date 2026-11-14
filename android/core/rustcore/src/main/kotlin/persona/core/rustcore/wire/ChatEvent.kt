package persona.core.rustcore.wire


sealed class ChatEvent {
    @Throws(com.novi.serde.SerializationError::class)
    abstract fun serialize(serializer: com.novi.serde.Serializer)

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): ChatEvent {
            val index = deserializer.deserialize_variant_index()
            return when (index) {
                0 -> Latest.load(deserializer)
                1 -> Pushed.load(deserializer)
                2 -> After.load(deserializer)
                3 -> Reloaded.load(deserializer)
                4 -> Older.load(deserializer)
                5 -> Range.load(deserializer)
                6 -> Reconnected.load(deserializer)
                7 -> LoadOlder.load(deserializer)
                8 -> OpenFold.load(deserializer)
                9 -> FillFailed.load(deserializer)
                10 -> OlderFailed.load(deserializer)
                11 -> RangeFailed.load(deserializer)
                else -> throw com.novi.serde.DeserializationError("Unknown variant index for ChatEvent: " + index)
            }
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): ChatEvent {
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

    data class Latest(
        val batch: persona.core.rustcore.wire.WireBatch
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(0)
            this.batch.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Latest {
                deserializer.increase_container_depth()
                val batch = persona.core.rustcore.wire.WireBatch.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return Latest(batch)
            }
        }
    }

    data class Pushed(
        val batch: persona.core.rustcore.wire.WireBatch
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(1)
            this.batch.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Pushed {
                deserializer.increase_container_depth()
                val batch = persona.core.rustcore.wire.WireBatch.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return Pushed(batch)
            }
        }
    }

    data class After(
        val batch: persona.core.rustcore.wire.WireBatch
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(2)
            this.batch.serialize(serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): After {
                deserializer.increase_container_depth()
                val batch = persona.core.rustcore.wire.WireBatch.deserialize(deserializer)
                deserializer.decrease_container_depth()
                return After(batch)
            }
        }
    }

    data class Reloaded(
        val items_json: String,
        val history: String?,
        val to_seq: Long?,
        val has_more: Boolean
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(3)
            serializer.serialize_str(this.items_json)
            TraitHelpers.serialize_option_str(this.history, serializer)
            TraitHelpers.serialize_option_i64(this.to_seq, serializer)
            serializer.serialize_bool(this.has_more)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Reloaded {
                deserializer.increase_container_depth()
                val items_json = deserializer.deserialize_str()
                val history = TraitHelpers.deserialize_option_str(deserializer)
                val to_seq = TraitHelpers.deserialize_option_i64(deserializer)
                val has_more = deserializer.deserialize_bool()
                deserializer.decrease_container_depth()
                return Reloaded(items_json, history, to_seq, has_more)
            }
        }
    }

    data class Older(
        val items_json: String,
        val has_more: Boolean
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(4)
            serializer.serialize_str(this.items_json)
            serializer.serialize_bool(this.has_more)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Older {
                deserializer.increase_container_depth()
                val items_json = deserializer.deserialize_str()
                val has_more = deserializer.deserialize_bool()
                deserializer.decrease_container_depth()
                return Older(items_json, has_more)
            }
        }
    }

    data class Range(
        val fold_id: String?,
        val items_json: String,
        val more: Boolean
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(5)
            TraitHelpers.serialize_option_str(this.fold_id, serializer)
            serializer.serialize_str(this.items_json)
            serializer.serialize_bool(this.more)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Range {
                deserializer.increase_container_depth()
                val fold_id = TraitHelpers.deserialize_option_str(deserializer)
                val items_json = deserializer.deserialize_str()
                val more = deserializer.deserialize_bool()
                deserializer.decrease_container_depth()
                return Range(fold_id, items_json, more)
            }
        }
    }

    object Reconnected : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(6)
            serializer.decrease_container_depth()
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun load(deserializer: com.novi.serde.Deserializer): Reconnected {
            deserializer.increase_container_depth()
            deserializer.decrease_container_depth()
            return Reconnected
        }
    }

    object LoadOlder : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(7)
            serializer.decrease_container_depth()
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun load(deserializer: com.novi.serde.Deserializer): LoadOlder {
            deserializer.increase_container_depth()
            deserializer.decrease_container_depth()
            return LoadOlder
        }
    }

    data class OpenFold(
        val fold_id: String
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(8)
            serializer.serialize_str(this.fold_id)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): OpenFold {
                deserializer.increase_container_depth()
                val fold_id = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return OpenFold(fold_id)
            }
        }
    }

    object FillFailed : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(9)
            serializer.decrease_container_depth()
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun load(deserializer: com.novi.serde.Deserializer): FillFailed {
            deserializer.increase_container_depth()
            deserializer.decrease_container_depth()
            return FillFailed
        }
    }

    object OlderFailed : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(10)
            serializer.decrease_container_depth()
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun load(deserializer: com.novi.serde.Deserializer): OlderFailed {
            deserializer.increase_container_depth()
            deserializer.decrease_container_depth()
            return OlderFailed
        }
    }

    data class RangeFailed(
        val fold_id: String?
    ) : ChatEvent() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(11)
            TraitHelpers.serialize_option_str(this.fold_id, serializer)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): RangeFailed {
                deserializer.increase_container_depth()
                val fold_id = TraitHelpers.deserialize_option_str(deserializer)
                deserializer.decrease_container_depth()
                return RangeFailed(fold_id)
            }
        }
    }
}

