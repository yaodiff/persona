package persona.core.rustcore.wire


sealed class Request {
    @Throws(com.novi.serde.SerializationError::class)
    abstract fun serialize(serializer: com.novi.serde.Serializer)

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): Request {
            val index = deserializer.deserialize_variant_index()
            return when (index) {
                0 -> FetchAfter.load(deserializer)
                1 -> FetchLatest.load(deserializer)
                2 -> Subscribe.load(deserializer)
                3 -> FetchOlder.load(deserializer)
                4 -> FetchRange.load(deserializer)
                else -> throw com.novi.serde.DeserializationError("Unknown variant index for Request: " + index)
            }
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): Request {
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

    data class FetchAfter(
        val after_seq: Long
    ) : Request() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(0)
            serializer.serialize_i64(this.after_seq)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): FetchAfter {
                deserializer.increase_container_depth()
                val after_seq = deserializer.deserialize_i64()
                deserializer.decrease_container_depth()
                return FetchAfter(after_seq)
            }
        }
    }

    data class FetchLatest(
        val fold_work: Boolean
    ) : Request() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(1)
            serializer.serialize_bool(this.fold_work)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): FetchLatest {
                deserializer.increase_container_depth()
                val fold_work = deserializer.deserialize_bool()
                deserializer.decrease_container_depth()
                return FetchLatest(fold_work)
            }
        }
    }

    data class Subscribe(
        val since_seq: Long?,
        val fold_work: Boolean
    ) : Request() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(2)
            TraitHelpers.serialize_option_i64(this.since_seq, serializer)
            serializer.serialize_bool(this.fold_work)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Subscribe {
                deserializer.increase_container_depth()
                val since_seq = TraitHelpers.deserialize_option_i64(deserializer)
                val fold_work = deserializer.deserialize_bool()
                deserializer.decrease_container_depth()
                return Subscribe(since_seq, fold_work)
            }
        }
    }

    data class FetchOlder(
        val before_seq: Long,
        val fold_work: Boolean
    ) : Request() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(3)
            serializer.serialize_i64(this.before_seq)
            serializer.serialize_bool(this.fold_work)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): FetchOlder {
                deserializer.increase_container_depth()
                val before_seq = deserializer.deserialize_i64()
                val fold_work = deserializer.deserialize_bool()
                deserializer.decrease_container_depth()
                return FetchOlder(before_seq, fold_work)
            }
        }
    }

    data class FetchRange(
        val fold_id: String?,
        val from_seq: Long,
        val through_seq: Long
    ) : Request() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(4)
            TraitHelpers.serialize_option_str(this.fold_id, serializer)
            serializer.serialize_i64(this.from_seq)
            serializer.serialize_i64(this.through_seq)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): FetchRange {
                deserializer.increase_container_depth()
                val fold_id = TraitHelpers.deserialize_option_str(deserializer)
                val from_seq = deserializer.deserialize_i64()
                val through_seq = deserializer.deserialize_i64()
                deserializer.decrease_container_depth()
                return FetchRange(fold_id, from_seq, through_seq)
            }
        }
    }
}

