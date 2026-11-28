package persona.core.designsystem

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.util.LruCache
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.staticCompositionLocalOf
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.launch
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.LookCurrent
import persona.core.contracts.api.PersonaInfo
import persona.core.contracts.api.PersonaManifest
import persona.core.contracts.api.PersonaPacks
import persona.core.contracts.api.PersonaPacksUnsupported
import java.io.File

/**
 * The hub's persona packages on this phone (docs/apple/persona.md "On the phone"), as on iOS: what the hub offers (kept across
 * launches in index.json), the packages downloaded, one directory per rev under files/personas/<id>/<rev>, and a
 * thumbnail each for Settings' cards. A package is used only once all of it is there (it is downloaded beside and moved
 * in whole); the chosen one shows from disk at launch, before the hub answers or without it. The choice itself is
 * [AppStyleStore.personaId], kept when its package leaves the hub, so it comes back with it.
 *
 * A hub that keeps the choice (GET /api/v1/looks, #3200) is the one truth of it: its choice wins over this phone's,
 * this phone's old choice moves up once when the hub has none, a choice made here goes to the hub, and another
 * device's arrives as a `look` push ([follow]). [AppStyleStore.personaId] is then this phone's copy of it.
 */
class PersonaStore(context: Context, private val style: AppStyleStore, private val packs: () -> PersonaPacks?) {
    private val app = context.applicationContext
    private val root = rootDir(app)
    private val index = File(root, "index.json")
    private val thumbnails = File(root, ".thumbnails")
    private val lock = Mutex()
    private val frameCache = LruCache<String, PersonaFrames>(6)

    private val _offered = MutableStateFlow(readIndex())
    /** The hub's packages that check out, as last listed. */
    val offered: StateFlow<List<PersonaInfo>> = _offered.asStateFlow()
    private val _current = MutableStateFlow(_offered.value.firstOrNull { it.id == style.personaId.value && onPhone(it) })
    /** The chosen package when it is on the phone; null shows the built-in ([AppStyleStore.face]). */
    val current: StateFlow<PersonaInfo?> = _current.asStateFlow()
    private val _downloading = MutableStateFlow(emptySet<String>())
    val downloading: StateFlow<Set<String>> = _downloading.asStateFlow()
    private val _error = MutableStateFlow<String?>(null)
    /** Why the last list or download failed (Settings › Persona look shows it). */
    val error: StateFlow<String?> = _error.asStateFlow()
    private val _pictures = MutableStateFlow(0)
    /** Bumped when pictures land on disk: the views that asked for one before ask again. */
    val pictures: StateFlow<Int> = _pictures.asStateFlow()

    private val _uploaded = MutableStateFlow(_offered.value.filter { it.manifest.uploaded }.map { it.id }.toSet())
    /** The looks made from an image (`uploaded` in GET /api/v1/looks, or the manifest's `source.kind`): the only ones the apps can delete. */
    val uploaded: StateFlow<Set<String>> = _uploaded.asStateFlow()
    private val _canUpload = MutableStateFlow(false)
    /** The hub makes avatars from images (`look_uploads`, #2404): Settings offers New from Photo. */
    val canUpload: StateFlow<Boolean> = _canUpload.asStateFlow()

    /** Whether the hub keeps the choice (it answered GET /api/v1/looks): choices go there. */
    @Volatile private var synced = false
    private val background = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private fun directory(p: PersonaInfo) = packageDir(root, p)
    fun onPhone(p: PersonaInfo) = directory(p).isDirectory
    private fun thumbnail(p: PersonaInfo) = File(thumbnails, "${p.id}-${p.rev}.png")

    private fun readIndex(): List<PersonaInfo> = readIndexFile(index)

    /** Lists the hub's packages; the chosen one is downloaded when its rev is new, and revs nobody uses go. */
    suspend fun refresh() = lock.withLock {
        val api = packs() ?: return@withLock
        val list = try {
            val looks = try { api.looks() } catch (e: PersonaPacksUnsupported) { null }
            synced = looks != null
            _canUpload.value = looks != null && api.uploads
            if (looks != null) {
                settle(looks.current, api)
                _uploaded.value = looks.looks.filter { it.uploaded || it.manifest?.uploaded == true }.map { it.id }.toSet()
                looks.looks.mapNotNull { it.pack }.filter { it.usable }
            } else api.list().filter { it.usable }.also { _uploaded.value = emptySet() }
        } catch (e: CancellationException) { throw e } catch (e: PersonaPacksUnsupported) {
            _error.value = e.message; return@withLock
        } catch (e: Exception) {
            _error.value = app.getString(R.string.designsystem_avatars_list_failed, e.message ?: e::class.simpleName); return@withLock
        }
        _error.value = null
        _offered.value = list
        withContext(Dispatchers.IO) {
            root.mkdirs()
            File(root, ".index.json.part").apply { writeText(JsonCodec.encodeToString(list)); renameTo(index) }
        }
        val chosen = list.firstOrNull { it.id == style.personaId.value }
        if (chosen != null && !onPhone(chosen)) download(chosen, api)
        // Re-read the choice: a built-in picked during the download wins. A new rev that did not come keeps the old
        // one on screen (prune keeps what is shown); a package gone from the hub shows the built-in, the choice kept.
        val id = style.personaId.value
        _current.value = when {
            chosen == null || chosen.id != id -> null
            onPhone(chosen) -> chosen
            else -> _current.value?.takeIf { it.id == id && onPhone(it) }
        }
        // The others' faces for Settings' cards, one small picture each (the package itself comes when chosen).
        for (p in list) if (!onPhone(p) && !thumbnail(p).exists()) {
            val file = p.manifest.avatarFile(THUMBNAIL_PX) ?: continue
            val bytes = try { api.file(p.id, file) } catch (e: CancellationException) { throw e } catch (_: Exception) { continue }
            withContext(Dispatchers.IO) { thumbnails.mkdirs(); writeWhole(thumbnail(p), bytes) }
            _pictures.value++
        }
        prune(list)
    }

    /** The choice to keep given the hub's: the hub's once it has one; else this phone's moves up, once, if unset there. */
    private suspend fun settle(current: LookCurrent, api: PersonaPacks) {
        val id = current.id ?: if (style.personaChosen) {
            runCatching { api.choose(style.personaId.value, ifUnset = true).id }.getOrNull()
        } else null
        if (id != null && id != style.personaId.value) {
            choices++
            style.setPersonaId(id)
        }
    }

    /** Picks a package, downloading it first; on failure the choice stays as it was and [error] says why. */
    suspend fun choose(p: PersonaInfo) {
        if (!p.usable) return
        val asked = ++choices
        val shown = lock.withLock {
            if (!onPhone(p)) download(p, packs() ?: return@withLock false)
            // Another choice made while this one downloaded wins.
            if (!onPhone(p) || asked != choices) return@withLock false
            style.setPersonaId(p.id)
            _current.value = p
            true
        }
        // The hub pushes what it keeps back to every device, this one too: only a choice shown here goes there.
        if (shown) tellHub(p.id)
    }

    /** Picks a built-in face at once (a download under way for an earlier choice is then not applied). */
    fun choose(design: PersonaDesign) {
        choices++
        style.setFace(design)
        _current.value = null
        tellHub(design.id)
    }

    /** Sends a choice made here to a hub that keeps it, so every device follows; when that fails it stays this phone's. */
    private fun tellHub(id: String) {
        if (!synced) return
        val api = packs() ?: return
        background.launch { val _ = runCatching { api.choose(id) } }
    }

    /** Another device chose [id] (the hub's `look` push): show it here, downloading its package first. */
    suspend fun follow(id: String) {
        val restored = restoredPersonaId(id)
        if (restored == style.personaId.value) return
        val design = PersonaDesign.entries.firstOrNull { it.id == restored }
        if (design != null) {
            choices++
            style.setFace(design)
            _current.value = null
            return
        }
        val asked = ++choices
        style.setPersonaId(restored)
        _current.value = null
        val p = _offered.value.firstOrNull { it.id == restored } ?: run { refresh(); return }
        lock.withLock {
            if (!onPhone(p)) download(p, packs() ?: return@withLock)
            if (asked == choices && onPhone(p)) _current.value = p
        }
    }

    /**
     * Makes an avatar from [image] (New from Photo, #2407) and chooses it: the upload asks the hub to choose it for every
     * device, so this phone only takes it on, with no PUT of its own ([fetch]).
     */
    suspend fun upload(name: String, subtitle: String?, image: ByteArray): LookUploadOutcome {
        val api = packs() ?: return LookUploadOutcome.Unreachable
        val made = try { api.upload(name, subtitle, image, choose = true) }
            catch (e: CancellationException) { throw e } catch (e: Exception) { return failure(e) }
        return fetch(made.look.id)
    }

    /**
     * Lists the hub's looks again and brings [id]'s package here, chosen as the hub's choice says (the one just made,
     * unless another device chose since). Try Again after an upload whose package did not come: the image is not sent again.
     */
    suspend fun fetch(id: String): LookUploadOutcome {
        refresh()
        if (_offered.value.none { it.id == id }) return LookUploadOutcome.NotDownloaded(id)
        return if (style.personaId.value != id || _current.value?.id == id) LookUploadOutcome.Done else LookUploadOutcome.NotDownloaded(id)
    }

    /**
     * Deletes an avatar made from an image (DELETE /api/v1/looks/uploads/{id}); one already gone (404) counts as deleted.
     * When it was the one chosen here the default comes back here, as the hub chooses it for the others.
     */
    suspend fun delete(id: String): LookUploadOutcome {
        val api = packs() ?: return LookUploadOutcome.Unreachable
        try { api.deleteUpload(id) } catch (e: CancellationException) { throw e } catch (e: Exception) {
            if (persona.core.hub.HubFailure.from(e).status != 404) return failure(e)
        }
        if (style.personaId.value == id) {
            choices++
            style.setFace(PersonaDesign.Default)
            _current.value = null
        }
        _uploaded.value -= id
        refresh()
        return LookUploadOutcome.Done
    }

    /** The hub's refusal in its words; no answer at all is [LookUploadOutcome.Unreachable]. */
    private fun failure(e: Exception): LookUploadOutcome {
        val failure = persona.core.hub.HubFailure.from(e)
        return if (failure.status != null) LookUploadOutcome.Refused(failure.message) else LookUploadOutcome.Unreachable
    }

    /** Counts choices, so a download finishing after a later choice does not undo it. */
    @Volatile private var choices = 0

    private suspend fun download(p: PersonaInfo, api: PersonaPacks) {
        _downloading.value += p.id
        val part = File(File(root, p.id), ".${p.rev}.part")
        try {
            withContext(Dispatchers.IO) { part.deleteRecursively() }
            for (file in p.manifest.files) {
                val bytes = api.file(p.id, file)
                withContext(Dispatchers.IO) { File(part, file).apply { parentFile?.mkdirs() }.writeBytes(bytes) }
            }
            withContext(Dispatchers.IO) {
                p.manifest.files.firstOrNull { !File(part, it).isFile }?.let { error(app.getString(R.string.designsystem_avatars_missing_file, it)) }
                directory(p).deleteRecursively()
                check(part.renameTo(directory(p))) { app.getString(R.string.designsystem_avatars_not_saved) }
            }
            _pictures.value++
            _error.value = null
        } catch (e: CancellationException) {
            withContext(Dispatchers.IO + kotlinx.coroutines.NonCancellable) { part.deleteRecursively() }; throw e
        } catch (e: Exception) {
            withContext(Dispatchers.IO) { part.deleteRecursively() }
            _error.value = app.getString(R.string.designsystem_avatars_download_failed, p.manifest.name, e.message ?: e::class.simpleName)
        } finally {
            _downloading.value -= p.id
        }
    }

    /** Removes the revs and thumbnails that are neither offered nor on screen. */
    private suspend fun prune(list: List<PersonaInfo>) = withContext(Dispatchers.IO) {
        val keep = list.map { "${it.id}/${it.rev}" }.toSet() + listOfNotNull(_current.value?.let { "${it.id}/${it.rev}" })
        thumbnails.listFiles()?.forEach { t -> if (keep.none { t.name == it.replace('/', '-') + ".png" }) t.delete() }
        root.listFiles()?.filter { it.isDirectory && !it.name.startsWith(".") }?.forEach { id ->
            id.listFiles()?.filter { !it.name.startsWith(".") && "${id.name}/${it.name}" !in keep }?.forEach { it.deleteRecursively() }
            if (id.list().isNullOrEmpty()) id.delete()
        }
    }

    /** The state's frames (by its wire name), decoded once and kept for the states last shown. */
    suspend fun frames(p: PersonaInfo, state: PersonaState): PersonaFrames? {
        val spec = p.manifest.dotState(state.wire) ?: return null
        val file = File(directory(p), spec.file)
        frameCache.get(file.path)?.let { return it }
        return withContext(Dispatchers.Default) {
            runCatching { decodeApng(file.readBytes(), spec.fps) }.getOrNull()
        }?.also { frameCache.put(file.path, it) }
    }

    /** The face [pixels] wide (its dark one when [dark]), from the package, or its thumbnail while the package is not on the phone. */
    suspend fun avatar(p: PersonaInfo, pixels: Int, dark: Boolean = false): Bitmap? = withContext(Dispatchers.IO) {
        val file = if (onPhone(p)) p.manifest.avatarFile(pixels, dark)?.let { File(directory(p), it) } else thumbnail(p)
        file?.takeIf { it.isFile }?.let { BitmapFactory.decodeFile(it.path) }
    }

    private fun writeWhole(file: File, bytes: ByteArray) {
        val part = File(file.parentFile, ".${file.name}.part")
        part.writeBytes(bytes)
        part.renameTo(file)
    }

    companion object {
        /** Settings' cards show faces 64 dp wide, at up to 3 pixels a dp. */
        const val THUMBNAIL_PX = 192

        private fun rootDir(context: Context) = File(context.applicationContext.filesDir, "personas")
        private fun packageDir(root: File, p: PersonaInfo) = File(File(root, p.id), p.rev)
        private fun readIndexFile(file: File): List<PersonaInfo> = runCatching {
            JsonCodec.decodeFromString<List<PersonaInfo>>(file.readText()).filter { it.usable }
        }.getOrDefault(emptyList())

        /**
         * The chosen persona package's colours from disk when installed on this phone; null for built-ins or absent
         * packages. Lets home-screen widgets follow [ThemeColor.Persona] without starting [PersonaStore] or hitting the hub.
         */
        fun currentColors(context: Context, personaId: String): PersonaManifest.Colors? {
            val root = rootDir(context)
            return readIndexFile(File(root, "index.json"))
                .firstOrNull { it.id == personaId && packageDir(root, it).isDirectory }
                ?.manifest?.colors
        }
    }
}

/** How an upload or a delete of an avatar made from an image ended. */
sealed interface LookUploadOutcome {
    /** Made (or deleted), and on this phone as the hub's choice says. */
    data object Done : LookUploadOutcome
    /** Made, but its package did not come to this phone: [PersonaStore.fetch] tries again without sending the image. */
    data class NotDownloaded(val id: String) : LookUploadOutcome
    /** The hub refused, in its own words (the app's language). */
    data class Refused(val words: String) : LookUploadOutcome
    /** No answer from the hub. */
    data object Unreachable : LookUploadOutcome
}

val LocalPersonaStore = staticCompositionLocalOf<PersonaStore?> { null }

/** The chosen hub package when it is on the phone; [PersonaFace] plays its frames instead of drawing a built-in. */
val LocalPersonaPack = compositionLocalOf<PersonaInfo?> { null }

/** Persona's name: the chosen package's, else the product's. */
val LocalPersonaName = compositionLocalOf { "Persona" }
