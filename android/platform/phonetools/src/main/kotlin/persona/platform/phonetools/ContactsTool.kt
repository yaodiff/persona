package persona.platform.phonetools

import android.content.ContentProviderResult
import android.content.ContentUris
import android.content.Context
import android.provider.ContactsContract
import android.provider.ContactsContract.Contacts
import android.provider.ContactsContract.Data
import android.provider.ContactsContract.RawContacts
import android.provider.ContactsContract.CommonDataKinds.*
import persona.core.contracts.api.PhoneToolResult
import kotlinx.serialization.json.*

internal interface ContactsBackend {
    fun snapshot(contactId: Long): ContactSnapshot?
    fun contactId(rawContactId: Long): Long?
    fun contact(contactId: Long): JsonObject?
    fun apply(writes: List<ProviderWrite>): Array<ContentProviderResult>
}

/** phone_contacts_search and the confirmed RawContacts/Data writes. */
internal class ContactsTool(
    private val context: Context,
    private val backend: ContactsBackend = AndroidContactsBackend(context),
) {
    private val resolver = context.contentResolver

    fun search(args: JsonObject): PhoneToolResult {
        val query = args.text("query").orEmpty()
        val limit = args.int("limit", 10, 1, 50)
        val ids = if (query.isEmpty()) firstContacts(limit) else matches(query, limit)
        val people = ids.mapNotNull(::contact)
        return PhoneToolResult.text(buildJsonObject {
            put("query", query); put("count", people.size); put("results", JsonArray(people))
            if (people.isEmpty()) put("note", "No contacts matched. Try a shorter query, or the company or phone number instead.")
        }.toString())
    }

    /** The address book's own order (its sort key), like an empty search on iOS. */
    private fun firstContacts(limit: Int): List<Long> = buildList {
        resolver.query(Contacts.CONTENT_URI, arrayOf(Contacts._ID), null, null, "${Contacts.SORT_KEY_PRIMARY} ASC")?.use { cursor ->
            while (size < limit && cursor.moveToNext()) add(cursor.getLong(0))
        } ?: unavailable("通讯录暂不可用 / The contacts provider is not available on this phone.")  // i18n-ignore: tool result for the model
    }

    /**
     * Name, nickname, company, job title, phone number or email containing [query] (case-insensitive); a query of at least
     * three digits also matches phone numbers written with spaces, dashes or brackets.
     */
    private fun matches(query: String, limit: Int): List<Long> {
        val escaped = query.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")
        val digits = query.filter(Char::isDigit).takeIf { it.length >= 3 }
        val types = listOf(StructuredName.CONTENT_ITEM_TYPE, Nickname.CONTENT_ITEM_TYPE, Organization.CONTENT_ITEM_TYPE,
            Phone.CONTENT_ITEM_TYPE, Email.CONTENT_ITEM_TYPE)
        val like = "LIKE ? ESCAPE '\\'"
        val number = "REPLACE(REPLACE(REPLACE(REPLACE(REPLACE(${Data.DATA1}, ' ', ''), '-', ''), '(', ''), ')', ''), '.', '')"
        val selection = "${Data.MIMETYPE} IN (${types.joinToString { "?" }}) AND (${Data.DISPLAY_NAME} $like OR ${Data.DATA1} $like" +
            " OR (${Data.MIMETYPE} = ? AND ${Organization.TITLE} $like)" +
            (if (digits != null) " OR (${Data.MIMETYPE} = ? AND $number LIKE ?)" else "") + ")"
        val pattern = "%$escaped%"
        val values = types + listOf(pattern, pattern, Organization.CONTENT_ITEM_TYPE, pattern) +
            (if (digits != null) listOf(Phone.CONTENT_ITEM_TYPE, "%$digits%") else emptyList())
        val found = linkedSetOf<Long>()
        resolver.query(Data.CONTENT_URI, arrayOf(Data.CONTACT_ID), selection, values.toTypedArray(), null)?.use { cursor ->
            while (found.size < MAX_CANDIDATES && cursor.moveToNext()) found += cursor.getLong(0)
        } ?: unavailable("通讯录暂不可用 / The contacts provider is not available on this phone.")  // i18n-ignore: tool result for the model
        if (found.isEmpty()) return emptyList()
        // Back in the address book's order, as iOS enumerates it.
        return buildList {
            resolver.query(Contacts.CONTENT_URI, arrayOf(Contacts._ID), "${Contacts._ID} IN (${found.joinToString(",")})", null,
                "${Contacts.SORT_KEY_PRIMARY} ASC")?.use { cursor ->
                while (size < limit && cursor.moveToNext()) add(cursor.getLong(0))
            }
        }
    }

    internal fun contact(id: Long): JsonObject? {
        var found = false
        var name = ""; var nickname = ""; var company = ""; var job = ""; var birthday: String? = null
        val phones = mutableListOf<JsonElement>(); val emails = mutableListOf<JsonElement>()
        resolver.query(Data.CONTENT_URI, arrayOf(Data.DISPLAY_NAME, Data.MIMETYPE, Data.DATA1, Data.DATA2, Data.DATA3, Data.DATA4),
            "${Data.CONTACT_ID} = ?", arrayOf(id.toString()), null)?.use { cursor ->
            while (cursor.moveToNext()) {
                found = true
                name = cursor.getString(0).orEmpty()
                val value = cursor.getString(2).orEmpty()
                when (cursor.getString(1)) {
                    Phone.CONTENT_ITEM_TYPE -> phones += buildJsonObject {
                        put("label", Phone.getTypeLabel(context.resources, cursor.getInt(3), cursor.getString(4)).toString())
                        put("number", value)
                    }
                    Email.CONTENT_ITEM_TYPE -> emails += buildJsonObject {
                        put("label", Email.getTypeLabel(context.resources, cursor.getInt(3), cursor.getString(4)).toString())
                        put("email", value)
                    }
                    Nickname.CONTENT_ITEM_TYPE -> if (nickname.isEmpty()) nickname = value
                    Organization.CONTENT_ITEM_TYPE -> { company = value; job = cursor.getString(5).orEmpty() }
                    Event.CONTENT_ITEM_TYPE -> if (cursor.getInt(3) == Event.TYPE_BIRTHDAY) birthday = value
                }
            }
        } ?: unavailable("通讯录暂不可用 / The contacts provider is not available on this phone.")  // i18n-ignore: tool result for the model
        if (!found) return null
        // Keys only when there is something to say, as iOS drops empty fields.
        return buildJsonObject {
            put("id", id.toString())
            if (name.isNotEmpty()) put("name", name)
            if (nickname.isNotEmpty()) put("nickname", nickname)
            if (company.isNotEmpty()) put("company", company)
            if (job.isNotEmpty()) put("job_title", job)
            if (phones.isNotEmpty()) put("phones", JsonArray(phones))
            if (emails.isNotEmpty()) put("emails", JsonArray(emails))
            birthday?.takeIf { it.isNotEmpty() }?.let { put("birthday", it) }
        }
    }

    fun create(args: JsonObject): PhoneToolResult {
        val edit = ContactEdit.plan(null, args)
        val writes = mutableListOf(ProviderWrite(ProviderWrite.Kind.Insert, RawContacts.CONTENT_URI, mapOf(
            RawContacts.ACCOUNT_TYPE to null, RawContacts.ACCOUNT_NAME to null,
        )))
        val given = edit.after("given_name"); val family = edit.after("family_name")
        if (given != null || family != null) writes += ProviderWrite(ProviderWrite.Kind.Insert, Data.CONTENT_URI,
            mapOf(Data.MIMETYPE to StructuredName.CONTENT_ITEM_TYPE, StructuredName.GIVEN_NAME to given,
                StructuredName.FAMILY_NAME to family), backReferences = mapOf(Data.RAW_CONTACT_ID to 0))
        val company = edit.after("company"); val job = edit.after("job_title")
        if (company != null || job != null) writes += ProviderWrite(ProviderWrite.Kind.Insert, Data.CONTENT_URI,
            mapOf(Data.MIMETYPE to Organization.CONTENT_ITEM_TYPE, Organization.COMPANY to company,
                Organization.TITLE to job), backReferences = mapOf(Data.RAW_CONTACT_ID to 0))
        for (entry in edit.phones) writes += ProviderWrite(ProviderWrite.Kind.Insert, Data.CONTENT_URI,
            entry.providerValues(email = false), backReferences = mapOf(Data.RAW_CONTACT_ID to 0))
        for (entry in edit.emails) writes += ProviderWrite(ProviderWrite.Kind.Insert, Data.CONTENT_URI,
            entry.providerValues(email = true), backReferences = mapOf(Data.RAW_CONTACT_ID to 0))
        val results = backend.apply(writes)
        val rawId = ContentUris.parseId(requireNotNull(results[0].uri))
        val contactId = backend.contactId(rawId) ?: error("The new raw contact has no aggregate contact id")
        return PhoneToolResult.text((backend.contact(contactId) ?: error("The new contact could not be read back")).toString())
    }

    fun prepareUpdate(args: JsonObject): ContactUpdatePreparation {
        val id = args.text("id")?.toLongOrNull() ?: invalid("id is required")
        val snapshot = backend.snapshot(id)
            ?: invalid("no contact with id \"$id\"; call phone_contacts_search to get a current id")
        return ContactUpdatePreparation(args, snapshot, ContactEdit.plan(snapshot.fields, args))
    }

    fun update(prepared: ContactUpdatePreparation): PhoneToolResult {
        val snapshot = prepared.snapshot
        // Everything up to the batch is a definite refusal: re-read, compare with what the user approved, plan.
        val writes = beforeWrite {
            val current = backend.snapshot(snapshot.contactId) ?: throw PhoneToolWriteRefusal(staleContactMessage)
            if (current != snapshot) throw PhoneToolWriteRefusal(staleContactMessage)
            updateWrites(prepared).ifEmpty { throw PhoneToolWriteRefusal(staleContactMessage) }
        }
        backend.apply(writes)
        // A changed name can re-aggregate the raw contact into another contact: read back through the raw contact, as create does.
        val contactId = backend.contactId(snapshot.rawContactId) ?: error("The changed raw contact has no aggregate contact id")
        return PhoneToolResult.text((backend.contact(contactId) ?: error("The changed contact could not be read back")).toString())
    }

    /** The provider operations of an approved edit; pure, so the card's plan and the executed batch cannot differ. */
    internal fun updateWrites(prepared: ContactUpdatePreparation): List<ProviderWrite> {
        val snapshot = prepared.snapshot
        val edit = prepared.edit
        val writes = mutableListOf<ProviderWrite>()

        fun upsertScalar(rowId: Long?, mimeType: String, values: Map<String, Any?>) {
            val all = values + (Data.MIMETYPE to mimeType)
            if (rowId == null) {
                writes += ProviderWrite(ProviderWrite.Kind.Insert, Data.CONTENT_URI,
                    all + (Data.RAW_CONTACT_ID to snapshot.rawContactId))
            } else {
                writes += ProviderWrite(ProviderWrite.Kind.Update, Data.CONTENT_URI, all, id = rowId)
            }
        }
        if (edit.fields.any { it.key == "given_name" || it.key == "family_name" }) {
            upsertScalar(snapshot.nameRowId, StructuredName.CONTENT_ITEM_TYPE, mapOf(
                StructuredName.GIVEN_NAME to (edit.after("given_name") ?: snapshot.fields.givenName),
                StructuredName.FAMILY_NAME to (edit.after("family_name") ?: snapshot.fields.familyName),
            ))
        }
        if (edit.fields.any { it.key == "company" || it.key == "job_title" }) {
            upsertScalar(snapshot.organizationRowId, Organization.CONTENT_ITEM_TYPE, mapOf(
                Organization.COMPANY to (edit.after("company") ?: snapshot.fields.company),
                Organization.TITLE to (edit.after("job_title") ?: snapshot.fields.jobTitle),
            ))
        }
        fun editEntries(old: List<ContactEntryRow>, new: List<LabeledEntry>, replace: Boolean, email: Boolean) {
            if (replace) for (row in old) writes += ProviderWrite(ProviderWrite.Kind.Delete, Data.CONTENT_URI, id = row.id)
            for (entry in new) writes += ProviderWrite(ProviderWrite.Kind.Insert, Data.CONTENT_URI,
                entry.providerValues(email) + (Data.RAW_CONTACT_ID to snapshot.rawContactId))
        }
        editEntries(snapshot.phones, edit.phones, edit.replacePhones, email = false)
        editEntries(snapshot.emails, edit.emails, edit.replaceEmails, email = true)
        return writes
    }

    private companion object {
        const val MAX_CANDIDATES = 500
        const val staleContactMessage = "联系人在确认后已变化，未写入 / The contact changed after it was shown for confirmation. " + // i18n-ignore: tool result for the model
            "Nothing was written. Call phone_contacts_search and prepare a fresh confirmation instead of retrying this call."
    }
}

private class AndroidContactsBackend(context: Context) : ContactsBackend {
    private val appContext = context.applicationContext
    private val resolver = appContext.contentResolver

    override fun snapshot(contactId: Long): ContactSnapshot? {
        // New rows go to the raw contact that already holds the name, then the company, else the lowest raw id.
        var nameRaw: Long? = null; var organizationRaw: Long? = null; var firstRaw: Long? = null
        var nameRow: Long? = null
        var organizationRow: Long? = null
        var given = ""; var family = ""; var company = ""; var job = ""
        val phones = mutableListOf<ContactEntryRow>()
        val emails = mutableListOf<ContactEntryRow>()
        var found = false
        resolver.query(Data.CONTENT_URI,
            arrayOf(Data._ID, Data.RAW_CONTACT_ID, Data.MIMETYPE, Data.DATA1, Data.DATA2, Data.DATA3, Data.DATA4),
            "${Data.CONTACT_ID} = ?", arrayOf(contactId.toString()), "${Data._ID} ASC")?.use { cursor ->
            while (cursor.moveToNext()) {
                found = true
                val id = cursor.getLong(0)
                val raw = cursor.getLong(1)
                firstRaw = firstRaw?.let { minOf(it, raw) } ?: raw
                when (cursor.getString(2)) {
                    // An aggregate of several raw contacts has one name row each: the first one (lowest id) is the one
                    // edited. Two rows inside the same raw contact are ambiguous and refused.
                    StructuredName.CONTENT_ITEM_TYPE -> if (nameRow == null) {
                        nameRow = id; nameRaw = raw; given = cursor.getString(4).orEmpty(); family = cursor.getString(5).orEmpty()
                    } else if (nameRaw == raw) {
                        unavailable("这个联系人有多个姓名行，暂不能安全修改 / This contact has multiple name rows and cannot be changed safely.")  // i18n-ignore: tool result for the model
                    }
                    Organization.CONTENT_ITEM_TYPE -> if (organizationRow == null) {
                        organizationRow = id; organizationRaw = raw; company = cursor.getString(3).orEmpty(); job = cursor.getString(6).orEmpty()
                    } else if (organizationRaw == raw) {
                        unavailable("这个联系人有多个公司行，暂不能安全修改 / This contact has multiple organization rows and cannot be changed safely.")  // i18n-ignore: tool result for the model
                    }
                    Phone.CONTENT_ITEM_TYPE -> phones += ContactEntryRow(id,
                        LabeledEntry(cursor.getString(3).orEmpty(), phoneLabel(cursor.getInt(4), cursor.getString(5))))
                    Email.CONTENT_ITEM_TYPE -> emails += ContactEntryRow(id,
                        LabeledEntry(cursor.getString(3).orEmpty(), emailLabel(cursor.getInt(4), cursor.getString(5))))
                }
            }
        } ?: unavailable("通讯录暂不可用 / The contacts provider is not available on this phone.")  // i18n-ignore: tool result for the model
        if (!found) return null
        val fields = ContactFields(given, family, company, job, phones.map { it.entry.value }, emails.map { it.entry.value })
        return ContactSnapshot(contactId, requireNotNull(nameRaw ?: organizationRaw ?: firstRaw), fields, nameRow, organizationRow, phones, emails)
    }

    override fun contactId(rawContactId: Long): Long? = resolver.query(RawContacts.CONTENT_URI,
        arrayOf(RawContacts.CONTACT_ID), "${RawContacts._ID} = ?", arrayOf(rawContactId.toString()), null)?.use { cursor ->
        if (cursor.moveToNext() && !cursor.isNull(0)) cursor.getLong(0) else null
    }

    override fun contact(contactId: Long): JsonObject? = ContactsTool(appContext).contact(contactId)

    override fun apply(writes: List<ProviderWrite>): Array<ContentProviderResult> =
        resolver.applyProviderWrites(ContactsContract.AUTHORITY, writes)

    private fun phoneLabel(type: Int, custom: String?): String = when {
        type == Phone.TYPE_CUSTOM && custom?.equals("iPhone", ignoreCase = true) == true -> "iphone"
        type == Phone.TYPE_HOME -> "home"
        type == Phone.TYPE_WORK -> "work"
        type == Phone.TYPE_MAIN -> "main"
        type == Phone.TYPE_MOBILE -> "mobile"
        else -> "other"
    }

    private fun emailLabel(type: Int, custom: String?): String = when {
        type == Email.TYPE_CUSTOM && custom?.equals("iPhone", ignoreCase = true) == true -> "iphone"
        type == Email.TYPE_HOME -> "home"
        type == Email.TYPE_WORK -> "work"
        else -> "other"
    }
}
