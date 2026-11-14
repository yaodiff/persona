package persona.core.rustcore.wire


class Config {

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize(serializer: com.novi.serde.Serializer) {
        serializer.increase_container_depth()
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
        fun deserialize(deserializer: com.novi.serde.Deserializer): Config {
            deserializer.increase_container_depth()
            deserializer.decrease_container_depth()
            return Config()
        }

        @Throws(com.novi.serde.DeserializationError::class)
        fun bincodeDeserialize(input: ByteArray): Config {
            val deserializer = com.novi.bincode.BincodeDeserializer(input)
            val value = deserialize(deserializer)
            if (deserializer.get_buffer_offset() < input.size) {
                throw com.novi.serde.DeserializationError("Some input bytes were not read")
            }
            return value
        }
    }

    override fun equals(other: Any?): Boolean {
        return other is Config
    }

    override fun hashCode(): Int {
        return 7
    }
}
