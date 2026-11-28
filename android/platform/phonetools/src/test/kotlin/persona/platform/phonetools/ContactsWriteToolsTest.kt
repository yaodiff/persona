package persona.platform.phonetools

import android.Manifest
import android.app.Application
import android.content.ContentProvider
import android.content.ContentProviderResult
import android.content.ContentUris
import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.provider.ContactsContract
import android.provider.ContactsContract.CommonDataKinds.Email
import android.provider.ContactsContract.CommonDataKinds.Organization
import android.provider.ContactsContract.CommonDataKinds.Phone
import android.provider.ContactsContract.CommonDataKinds.StructuredName
import android.provider.ContactsContract.Data
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowContentResolver

/**
 * Contact create / update over a fake provider: the generated RawContacts / Data operations, the replace rules and the
 * stale-snapshot refusal. It does not prove that a real address book (and its account sync) accepts them.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ContactsWriteToolsTest {
    private val context get() = ApplicationProvider.getApplicationContext<Context>()
    private val backend = FakeContactsBackend()
    private val tools get() = ContactsTool(context, backend)

    private fun entries(vararg items: Any) = buildJsonArray {
        for (item in items) when (item) {
            is String -> add(JsonPrimitive(item))
            is Pair<*, *> -> add(buildJsonObject { put("value", item.first as String); put("label", item.second as String) })
            is JsonObject -> add(item)
        }
    }

    private fun args(vararg pairs: Pair<String, Any>) = buildJsonObject {
        for ((key, value) in pairs) when (value) {
            is Boolean -> put(key, value)
            is JsonArray -> put(key, value)
            else -> put(key, value.toString())
        }
    }

    private val existing = ContactSnapshot(
        contactId = 5, rawContactId = 50,
        fields = ContactFields("Ann", "Lee", "Acme", "", listOf("+1 555 0100"), listOf("ann@example.com")),
        nameRowId = 100, organizationRowId = 101,
        phones = listOf(ContactEntryRow(110, LabeledEntry("+1 555 0100", "mobile"))),
        emails = listOf(ContactEntryRow(120, LabeledEntry("ann@example.com", "work"))),
    )

    private fun failure(block: () -> Unit) = runCatching(block).exceptionOrNull() as PhoneToolFailure

    @Test fun aNewContactNeedsANameOrACompany() {
        assertEquals("a new contact needs given_name, family_name or company",
            failure { tools.create(args("phones" to entries("555 0100"))) }.message)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun aNewContactIsWrittenInOneBatchWithBackReferencesAndReadBack() {
        val result = tools.create(args(
            "given_name" to "Ann", "family_name" to "Lee", "company" to "Acme", "job_title" to "CTO",
            "phones" to entries("555 0100" to "iphone", "555 0101", "5550101"),
            "emails" to entries("ann@example.com" to "mobile", "Ann@Example.com"),
        ))
        val writes = backend.batches.single()
        assertEquals(ContactsContract.RawContacts.CONTENT_URI, writes[0].uri)
        assertEquals(mapOf(ContactsContract.RawContacts.ACCOUNT_TYPE to null, ContactsContract.RawContacts.ACCOUNT_NAME to null), writes[0].values)
        val everyDataRow = writes.drop(1)
        assertTrue(everyDataRow.all { it.uri == Data.CONTENT_URI && it.backReferences == mapOf(Data.RAW_CONTACT_ID to 0) })
        assertEquals("Ann", everyDataRow[0].values[StructuredName.GIVEN_NAME])
        assertEquals("Lee", everyDataRow[0].values[StructuredName.FAMILY_NAME])
        assertEquals("Acme", everyDataRow[1].values[Organization.COMPANY])
        assertEquals("CTO", everyDataRow[1].values[Organization.TITLE])
        // The same number twice in one call is written once; the iPhone label is a custom type, as on iOS.
        val phones = everyDataRow.filter { it.values[Data.MIMETYPE] == Phone.CONTENT_ITEM_TYPE }
        assertEquals(listOf("555 0100", "555 0101"), phones.map { it.values[Data.DATA1] })
        assertEquals(Phone.TYPE_CUSTOM, phones[0].values[Data.DATA2])
        assertEquals("iPhone", phones[0].values[Data.DATA3])
        assertEquals(Phone.TYPE_MOBILE, phones[1].values[Data.DATA2])
        // An address that differs only by case is the same address; "mobile" has no email type and becomes other.
        val emails = everyDataRow.filter { it.values[Data.MIMETYPE] == Email.CONTENT_ITEM_TYPE }
        assertEquals(listOf("ann@example.com"), emails.map { it.values[Data.DATA1] })
        assertEquals(Email.TYPE_OTHER, emails[0].values[Data.DATA2])
        // The answer is the contact read back by its aggregate id, as phone_contacts_search returns it.
        assertEquals("5", result.content.single().jsonObject.getValue("text").jsonPrimitive.content.let {
            kotlinx.serialization.json.Json.parseToJsonElement(it).jsonObject.getValue("id").jsonPrimitive.content
        })
    }

    @Test fun invalidPhonesEmailsAndLabelsAreRefusedBeforeAnything() {
        assertEquals("\"12\" is not a phone number", failure { tools.create(args("given_name" to "A", "phones" to entries("12"))) }.message)
        assertEquals("\"no-at\" is not an email address", failure { tools.create(args("given_name" to "A", "emails" to entries("no-at"))) }.message)
        assertEquals("unknown label \"fax\"", failure { tools.create(args("given_name" to "A", "phones" to entries("555 0100" to "fax"))) }.message)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun anUpdateWithoutReplaceNeverOverwritesADifferentValue() {
        backend.current = existing
        val failure = failure { tools.prepareUpdate(args("id" to "5", "given_name" to "Bea")) }
        assertEquals("the contact's given_name is already \"Ann\"; pass replace=true only if the user asked to change it", failure.message)
        // The same value is not a change, and an empty field may simply be filled in.
        assertEquals("nothing to change: the contact already has these values",
            failure { tools.prepareUpdate(args("id" to "5", "given_name" to "Ann")) }.message)
        tools.update(tools.prepareUpdate(args("id" to "5", "job_title" to "CTO")))
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Update, write.kind)
        assertEquals(101L, write.id)
        assertEquals("CTO", write.values[Organization.TITLE])
        assertEquals("Acme", write.values[Organization.COMPANY])
    }

    @Test fun aMissingContactIsRefusedBeforeTheCard() {
        backend.current = null
        assertTrue(failure { tools.prepareUpdate(args("id" to "5", "company" to "X")) }.message!!.startsWith("no contact with id \"5\""))
        assertTrue(failure { tools.prepareUpdate(args("company" to "X")) }.message!!.contains("id is required"))
    }

    @Test fun phonesAndEmailsAreAddedAndDedupedAgainstTheExistingOnes() {
        backend.current = existing
        tools.update(tools.prepareUpdate(args(
            "id" to "5",
            "phones" to entries("(1) 555-0100", "555 0199" to "work", "5550199"),
            "emails" to entries("ANN@example.com", "bea@example.com"),
        )))
        val writes = backend.batches.single()
        // Nothing existing is deleted; the matching number and address are skipped; unrelated data rows are never touched.
        assertTrue(writes.none { it.kind == ProviderWrite.Kind.Delete })
        assertEquals(listOf("555 0199", "bea@example.com"), writes.map { it.values[Data.DATA1] })
        assertEquals(Phone.TYPE_WORK, writes[0].values[Data.DATA2])
        assertTrue(writes.all { it.values[Data.RAW_CONTACT_ID] == 50L })
    }

    @Test fun replaceOverwritesScalarsAndSwapsTheWholeLists() {
        backend.current = existing
        tools.update(tools.prepareUpdate(args("id" to "5", "given_name" to "Bea", "replace" to true,
            "phones" to entries("555 0200"))))
        val writes = backend.batches.single()
        assertEquals(listOf(ProviderWrite.Kind.Update, ProviderWrite.Kind.Delete, ProviderWrite.Kind.Insert), writes.map { it.kind })
        assertEquals(100L, writes[0].id)
        assertEquals("Bea", writes[0].values[StructuredName.GIVEN_NAME])
        assertEquals("Lee", writes[0].values[StructuredName.FAMILY_NAME]) // untouched halves are written back as they were
        assertEquals(110L, writes[1].id)
        assertEquals("555 0200", writes[2].values[Data.DATA1])
        // Emails were not passed, so they stay.
        assertTrue(writes.none { it.id == 120L })
    }

    @Test fun replaceWithAnEmptyListClearsIt() {
        backend.current = existing
        tools.update(tools.prepareUpdate(args("id" to "5", "replace" to true, "emails" to JsonArray(emptyList()))))
        val write = backend.batches.single().single()
        assertEquals(ProviderWrite.Kind.Delete, write.kind)
        assertEquals(120L, write.id)
    }

    @Test fun aContactChangedAfterTheCardIsRefusedWithoutAnyWrite() {
        backend.current = existing
        val prepared = tools.prepareUpdate(args("id" to "5", "given_name" to "Bea", "replace" to true))
        backend.current = existing.copy(fields = existing.fields.copy(givenName = "Anna"))
        val refusal = runCatching { tools.update(prepared) }.exceptionOrNull()
        assertTrue(refusal.toString(), refusal is PhoneToolWriteRefusal)
        assertTrue(backend.batches.isEmpty())
        // A contact that disappeared is the same refusal, not an uncertain write.
        backend.current = null
        assertTrue(runCatching { tools.update(prepared) }.exceptionOrNull() is PhoneToolWriteRefusal)
        assertTrue(backend.batches.isEmpty())
    }

    @Test fun aRenameThatReAggregatesStillReadsTheContactBackByItsNewId() {
        backend.current = existing
        val prepared = tools.prepareUpdate(args("id" to "5", "given_name" to "Bea", "replace" to true))
        // The provider merged the raw contact into another aggregate; the id the user searched with no longer exists.
        backend.aggregateId = 9
        val result = tools.update(prepared)
        assertEquals(1, backend.batches.size)
        val text = result.content.single().jsonObject.getValue("text").jsonPrimitive.content
        assertEquals("9", kotlinx.serialization.json.Json.parseToJsonElement(text).jsonObject.getValue("id").jsonPrimitive.content)
    }

    @Test fun aBatchFailureIsLeftToTheRegistryToAnswerUncertain() {
        backend.current = existing
        val prepared = tools.prepareUpdate(args("id" to "5", "company" to "Beta", "replace" to true))
        backend.applyFailure = IllegalStateException("provider died")
        assertTrue(runCatching { tools.update(prepared) }.exceptionOrNull() is IllegalStateException)
    }

    @Test fun theCardShowsWhatIsOverwrittenAndNeedsNoProvider() {
        backend.current = existing
        val prepared = tools.prepareUpdate(args("id" to "5", "company" to "Beta", "replace" to true,
            "phones" to entries("555 0200")))
        val reads = backend.reads
        val text = prepared.text(context.resources)
        assertEquals(reads, backend.reads)
        assertTrue(text, text.contains("Acme") && text.contains("Beta"))
        assertTrue(text, text.contains("+1 555 0100") && text.contains("555 0200"))
        // The card for a new contact lists values, not "Not set → value".
        val create = ContactEdit.plan(null, args("given_name" to "Ann")).createSummary(context.resources)
        assertFalse(create, create.contains("→"))
    }

    @Test fun onlyAReadAndWriteGrantOffersTheWriteTools() {
        val app = ApplicationProvider.getApplicationContext<Application>()
        val permissions = PhonePermissions(app)
        fun offered() = PhoneToolRegistry({ PhoneSource.entries.toSet() }, { true }, { true }, permissions::allowed,
            { PhoneConfirmation.Denied }, { _, _ -> error("not used") }).enabledSpecs().map { it.name }.toSet()
        val writes = setOf("phone_contacts_create", "phone_contacts_update", "phone_calendar_create_event",
            "phone_calendar_update_event", "phone_calendar_delete_event")

        Shadows.shadowOf(app).grantPermissions(Manifest.permission.READ_CONTACTS, Manifest.permission.READ_CALENDAR)
        assertTrue(offered().containsAll(setOf("phone_contacts_search", "phone_calendar_events")))
        assertTrue(offered().intersect(writes).isEmpty())
        assertEquals(PhoneAccess.ReadOnly, permissions.status(PhoneSource.Contacts))
        assertEquals(PhoneAccess.ReadOnly, permissions.status(PhoneSource.Calendar))

        Shadows.shadowOf(app).grantPermissions(Manifest.permission.WRITE_CONTACTS)
        assertEquals(setOf("phone_contacts_create", "phone_contacts_update"), offered().intersect(writes))
        assertEquals(PhoneAccess.Granted, permissions.status(PhoneSource.Contacts))
        assertEquals(PhoneAccess.ReadOnly, permissions.status(PhoneSource.Calendar))

        Shadows.shadowOf(app).grantPermissions(Manifest.permission.WRITE_CALENDAR)
        assertEquals(writes, offered().intersect(writes))
        assertEquals(PhoneAccess.Granted, permissions.status(PhoneSource.Calendar))
    }

    @Test fun androidContactsBackendReadsStructuredNameFromData2AndData3AndPreservesFamilyName() {
        val rowsByContact: Map<String, List<Map<String, Any?>>> = mapOf(
            "5" to listOf(
                mapOf(
                    Data._ID to 100L, Data.RAW_CONTACT_ID to 50L, Data.MIMETYPE to StructuredName.CONTENT_ITEM_TYPE,
                    StructuredName.DISPLAY_NAME to "Ann Lee",
                    StructuredName.GIVEN_NAME to "Ann",
                    StructuredName.FAMILY_NAME to "Lee",
                ),
                mapOf(
                    Data._ID to 101L, Data.RAW_CONTACT_ID to 50L, Data.MIMETYPE to Organization.CONTENT_ITEM_TYPE,
                    Organization.COMPANY to "Acme",
                    Organization.TITLE to "Engineer",
                ),
                mapOf(
                    Data._ID to 110L, Data.RAW_CONTACT_ID to 50L, Data.MIMETYPE to Phone.CONTENT_ITEM_TYPE,
                    Phone.NUMBER to "+1 555 0100",
                    Phone.TYPE to Phone.TYPE_WORK,
                    Phone.LABEL to "iPhone",
                ),
                mapOf(
                    Data._ID to 111L, Data.RAW_CONTACT_ID to 50L, Data.MIMETYPE to Phone.CONTENT_ITEM_TYPE,
                    Phone.NUMBER to "+1 555 0101",
                    Phone.TYPE to Phone.TYPE_CUSTOM,
                    Phone.LABEL to "iPhone",
                ),
                mapOf(
                    Data._ID to 120L, Data.RAW_CONTACT_ID to 50L, Data.MIMETYPE to Email.CONTENT_ITEM_TYPE,
                    Email.ADDRESS to "ann@work.example",
                    Email.TYPE to Email.TYPE_WORK,
                    Email.LABEL to "iPhone",
                ),
                mapOf(
                    Data._ID to 121L, Data.RAW_CONTACT_ID to 50L, Data.MIMETYPE to Email.CONTENT_ITEM_TYPE,
                    Email.ADDRESS to "ann@icloud.example",
                    Email.TYPE to Email.TYPE_CUSTOM,
                    Email.LABEL to "iPhone",
                ),
            ),
            "6" to listOf(
                mapOf(
                    Data._ID to 200L, Data.RAW_CONTACT_ID to 60L, Data.MIMETYPE to StructuredName.CONTENT_ITEM_TYPE,
                    StructuredName.DISPLAY_NAME to "Ann",
                    StructuredName.GIVEN_NAME to "Ann",
                    StructuredName.FAMILY_NAME to null,
                ),
            ),
        )
        val provider = object : ContentProvider() {
            override fun onCreate() = true
            override fun query(
                uri: Uri,
                projection: Array<out String>?,
                selection: String?,
                selectionArgs: Array<out String>?,
                sortOrder: String?,
            ): Cursor {
                assertEquals(Data.CONTENT_URI, uri)
                val cols = projection ?: arrayOf(Data._ID)
                val cursor = MatrixCursor(cols)
                val rows = rowsByContact[selectionArgs?.firstOrNull()].orEmpty()
                for (row in rows) cursor.addRow(Array<Any?>(cols.size) { row[cols[it]] })
                return cursor
            }
            override fun getType(uri: Uri): String? = null
            override fun insert(uri: Uri, values: ContentValues?): Uri? = null
            override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int = 0
            override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?): Int = 0
        }
        ShadowContentResolver.registerProviderInternal(ContactsContract.AUTHORITY, provider)

        val realTools = ContactsTool(context)
        val prepared = realTools.prepareUpdate(args("id" to "5", "given_name" to "Bea", "replace" to true))
        assertEquals("Ann", prepared.snapshot.fields.givenName)
        assertEquals("Lee", prepared.snapshot.fields.familyName)
        assertEquals("Acme", prepared.snapshot.fields.company)
        assertEquals("Engineer", prepared.snapshot.fields.jobTitle)
        assertEquals(
            listOf("+1 555 0100" to "work", "+1 555 0101" to "iphone"),
            prepared.snapshot.phones.map { it.entry.value to it.entry.label },
        )
        assertEquals(
            listOf("ann@work.example" to "work", "ann@icloud.example" to "iphone"),
            prepared.snapshot.emails.map { it.entry.value to it.entry.label },
        )

        val card = prepared.text(context.resources)
        assertTrue(card, card.contains("Ann Lee") && !card.contains("Ann Lee Ann"))
        val write = realTools.updateWrites(prepared).single()
        assertEquals(ProviderWrite.Kind.Update, write.kind)
        assertEquals(100L, write.id)
        assertEquals("Bea", write.values[StructuredName.GIVEN_NAME])
        assertEquals("Lee", write.values[StructuredName.FAMILY_NAME])

        // A given-name-only contact must leave familyName empty so family_name can be added without replace=true.
        val addedFamily = realTools.prepareUpdate(args("id" to "6", "family_name" to "Lee"))
        assertEquals("Ann", addedFamily.snapshot.fields.givenName)
        assertEquals("", addedFamily.snapshot.fields.familyName)
        val familyWrite = realTools.updateWrites(addedFamily).single()
        assertEquals("Ann", familyWrite.values[StructuredName.GIVEN_NAME])
        assertEquals("Lee", familyWrite.values[StructuredName.FAMILY_NAME])
    }
}

internal class FakeContactsBackend : ContactsBackend {
    var current: ContactSnapshot? = null
    val batches = mutableListOf<List<ProviderWrite>>()
    var applyFailure: Exception? = null
    var reads = 0

    override fun snapshot(contactId: Long): ContactSnapshot? { reads++; return current?.takeIf { it.contactId == contactId } }
    /** The aggregate the raw contact belongs to now: a rename can re-aggregate it, after which the old id is gone. */
    var aggregateId = 5L
    override fun contactId(rawContactId: Long): Long? = aggregateId
    override fun contact(contactId: Long): JsonObject? {
        reads++
        if (contactId != aggregateId) return null
        return buildJsonObject { put("id", contactId.toString()); put("name", "Ann Lee") }
    }

    override fun apply(writes: List<ProviderWrite>): Array<ContentProviderResult> {
        applyFailure?.let { throw it }
        batches += writes
        return Array(writes.size) { index ->
            if (writes[index].kind == ProviderWrite.Kind.Insert) {
                ContentProviderResult(ContentUris.withAppendedId(ContactsContract.RawContacts.CONTENT_URI, 77L + index))
            } else ContentProviderResult(1)
        }
    }
}
