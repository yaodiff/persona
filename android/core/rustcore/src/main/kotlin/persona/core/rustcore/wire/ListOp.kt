package persona.core.rustcore.wire


sealed class ListOp {
    @Throws(com.novi.serde.SerializationError::class)
    abstract fun serialize(serializer: com.novi.serde.Serializer)

    companion object {
        @Throws(com.novi.serde.DeserializationError::class)
        fun deserialize(deserializer: com.novi.serde.Deserializer): ListOp {
            val index = deserializer.deserialize_variant_index()
            return when (index) {
                0 -> Remove.load(deserializer)
                1 -> Insert.load(deserializer)
                2 -> Update.load(deserializer)
                else -> throw com.novi.serde.DeserializationError("Unknown variant index for ListOp: " + index)
            }
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): ListOp {
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

    data class Remove(
        val index: UInt
    ) : ListOp() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(0)
            serializer.serialize_u32(this.index)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Remove {
                deserializer.increase_container_depth()
                val index = deserializer.deserialize_u32()
                deserializer.decrease_container_depth()
                return Remove(index)
            }
        }
    }

    data class Insert(
        val index: UInt,
        val id: String,
        val json: String
    ) : ListOp() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(1)
            serializer.serialize_u32(this.index)
            serializer.serialize_str(this.id)
            serializer.serialize_str(this.json)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Insert {
                deserializer.increase_container_depth()
                val index = deserializer.deserialize_u32()
                val id = deserializer.deserialize_str()
                val json = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Insert(index, id, json)
            }
        }
    }

    data class Update(
        val index: UInt,
        val id: String,
        val json: String
    ) : ListOp() {

        @Throws(com.novi.serde.SerializationError::class)
        override fun serialize(serializer: com.novi.serde.Serializer) {
            serializer.increase_container_depth()
            serializer.serialize_variant_index(2)
            serializer.serialize_u32(this.index)
            serializer.serialize_str(this.id)
            serializer.serialize_str(this.json)
            serializer.decrease_container_depth()
        }

        companion object {
            @Throws(com.novi.serde.DeserializationError::class)
            fun load(deserializer: com.novi.serde.Deserializer): Update {
                deserializer.increase_container_depth()
                val index = deserializer.deserialize_u32()
                val id = deserializer.deserialize_str()
                val json = deserializer.deserialize_str()
                deserializer.decrease_container_depth()
                return Update(index, id, json)
            }
        }
    }
}

