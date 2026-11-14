package persona.core.rustcore.wire

object TraitHelpers {
    @Throws(com.novi.serde.SerializationError::class)
    fun serialize_option_bool(value: Boolean?, serializer: com.novi.serde.Serializer) {
        if (value == null) {
            serializer.serialize_option_tag(false)
        } else {
            serializer.serialize_option_tag(true)
            serializer.serialize_bool(value)
        }
    }

    @Throws(com.novi.serde.DeserializationError::class)
    fun deserialize_option_bool(deserializer: com.novi.serde.Deserializer): Boolean? {
        val tag = deserializer.deserialize_option_tag()
        return if (!tag) {
            null
        } else {
            deserializer.deserialize_bool()
        }
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize_option_i64(value: Long?, serializer: com.novi.serde.Serializer) {
        if (value == null) {
            serializer.serialize_option_tag(false)
        } else {
            serializer.serialize_option_tag(true)
            serializer.serialize_i64(value)
        }
    }

    @Throws(com.novi.serde.DeserializationError::class)
    fun deserialize_option_i64(deserializer: com.novi.serde.Deserializer): Long? {
        val tag = deserializer.deserialize_option_tag()
        return if (!tag) {
            null
        } else {
            deserializer.deserialize_i64()
        }
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize_option_str(value: String?, serializer: com.novi.serde.Serializer) {
        if (value == null) {
            serializer.serialize_option_tag(false)
        } else {
            serializer.serialize_option_tag(true)
            serializer.serialize_str(value)
        }
    }

    @Throws(com.novi.serde.DeserializationError::class)
    fun deserialize_option_str(deserializer: com.novi.serde.Deserializer): String? {
        val tag = deserializer.deserialize_option_tag()
        return if (!tag) {
            null
        } else {
            deserializer.deserialize_str()
        }
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize_option_u64(value: ULong?, serializer: com.novi.serde.Serializer) {
        if (value == null) {
            serializer.serialize_option_tag(false)
        } else {
            serializer.serialize_option_tag(true)
            serializer.serialize_u64(value)
        }
    }

    @Throws(com.novi.serde.DeserializationError::class)
    fun deserialize_option_u64(deserializer: com.novi.serde.Deserializer): ULong? {
        val tag = deserializer.deserialize_option_tag()
        return if (!tag) {
            null
        } else {
            deserializer.deserialize_u64()
        }
    }

    @Throws(com.novi.serde.SerializationError::class)
    fun serialize_vector_ListOp(value: kotlin.collections.List<persona.core.rustcore.wire.ListOp>, serializer: com.novi.serde.Serializer) {
        serializer.serialize_len(value.size.toLong())
        for (item in value) {
            item.serialize(serializer)
        }
    }

    @Throws(com.novi.serde.DeserializationError::class)
    fun deserialize_vector_ListOp(deserializer: com.novi.serde.Deserializer): kotlin.collections.List<persona.core.rustcore.wire.ListOp> {
        val length = deserializer.deserialize_len()
        val obj = ArrayList<persona.core.rustcore.wire.ListOp>(length.toInt())
        var i = 0L
        while (i < length) {
            obj.add(persona.core.rustcore.wire.ListOp.deserialize(deserializer))
            i += 1
        }
        return obj
    }

}

