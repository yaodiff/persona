package persona.core.rustcore.wire


sealed class Payload {
    @Throws(com.novi.serde.SerializationError::class)
    abstract fun serialize(serializer: com.novi.serde.Serializer)

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): Payload {
            val index = deserializer.deserialize_variant_index()
            return when (index) {
                0 -> Latest.load(deserializer)
                1 -> Newer.load(deserializer)
                2 -> Reloaded.load(deserializer)
                3 -> Older.load(deserializer)
                4 -> Range.load(deserializer)
                5 -> Frame.load(deserializer)
                else -> throw com.novi.serde.DeserializationError("Unknown variant index for Payload: " + index)
            }
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): Payload {
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
        val body: String
    ) : Payload() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(0)
            serializer.serialize_str(this.body)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Latest {
                deserializer.increase_container_depth()
                val body = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Latest(body)
            }
        }
    }

    data class Newer(
        val body: String
    ) : Payload() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(1)
            serializer.serialize_str(this.body)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Newer {
                deserializer.increase_container_depth()
                val body = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Newer(body)
            }
        }
    }

    data class Reloaded(
        val body: String
    ) : Payload() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(2)
            serializer.serialize_str(this.body)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Reloaded {
                deserializer.increase_container_depth()
                val body = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Reloaded(body)
            }
        }
    }

    data class Older(
        val body: String
    ) : Payload() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(3)
            serializer.serialize_str(this.body)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Older {
                deserializer.increase_container_depth()
                val body = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Older(body)
            }
        }
    }

    data class Range(
        val fold_id: String?,
        val body: String
    ) : Payload() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(4)
            TraitHelpers.serialize_option_str(this.fold_id, serializer)
            serializer.serialize_str(this.body)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Range {
                deserializer.increase_container_depth()
                val fold_id = TraitHelpers.deserialize_option_str(deserializer)
                val body = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Range(fold_id, body)
            }
        }
    }

    data class Frame(
        val body: String
    ) : Payload() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(5)
            serializer.serialize_str(this.body)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Frame {
                deserializer.increase_container_depth()
                val body = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Frame(body)
            }
        }
    }
}

