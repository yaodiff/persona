package persona.core.contracts

/** Rolling-upgrade contracts for baseline 39a706a338. Keep legacy names out of product code and copy. */
object ProtocolCompatibility {
    const val CAPABILITY = "persona_contracts_v1"
    val pairingMasterKeyUris = listOf("android-keystore://persona.hub-data.master", "android-keystore://jarvis.hub-data.master")
    const val LEGACY_PANE_PREFERENCES = "jarvis-panes"
    fun legacyPaneKey(key: String) = if (key == "split.persona") "split.jarvis" else key
    fun isUpdateAPK(name: String) = name.endsWith(".apk") && (name.startsWith("persona-update-") || name.startsWith("jarvis-update-"))
    val phoneConfirmationActions = setOf("persona.phone.confirm", "jarvis.phone.confirm")
    val appSchemes = setOf("persona", "jarvis")
    val personaServers = setOf("persona", "jarvis")
    val clientHeaders = listOf("X-Persona-Client", "X-Jarvis-Client")
    val operationHeaders = listOf("X-Persona-Op-Id", "X-Jarvis-Op-Id")
    val legacyBuiltins = setOf("jarvis", "jarvis-circuit", "jarvis-pixel", "jarvis-facet", "jarvis-blob")
    val contextTag = Regex("<(?:persona|jarvis)-context\\b([^>]*)>([\\s\\S]*?)</(?:persona|jarvis)-context>")
    val contextBlock = Regex("${contextTag.pattern}\\s*")
    val leadContext = Regex("^\\s*<(?:persona|jarvis)-context\\b[^>]*>[\\s\\S]*?</(?:persona|jarvis)-context>")
    val contextClosingTag = Regex("<(\\s*/\\s*(?:persona|jarvis)-context\\s*>)", RegexOption.IGNORE_CASE)
    private const val names = "(?:persona|jarvis)-(?:context|file)"
    val unclosedContextBlock = Regex("<$names\\b[^>]*>[\\s\\S]*$", RegexOption.IGNORE_CASE)
    val strayClosingTag = Regex("</\\s*$names\\s*>", RegexOption.IGNORE_CASE)
    val internalTagBlock = Regex(
        "<(system-reminder|command-name|command-message|command-args|local-command-[a-z-]+)\\b[^>]*>[\\s\\S]*?</\\1>",
        RegexOption.IGNORE_CASE,
    )
    val internalTagSingle = Regex(
        "</?(?:system-reminder|command-name|command-message|command-args|local-command-[a-z-]+|persona-[a-z-]+|" + "jarvis-[a-z-]+)\\b[^>]*>",
        RegexOption.IGNORE_CASE,
    )
    val fileTag = Regex("\\s*<(?:persona|jarvis)-file\\b([^>]*?)/?>(?:\\s*</(?:persona|jarvis)-file>)?\\s*")
    private val outgoingElement = Regex("<\\s*/?\\s*(?:persona|jarvis)-(?:context|file)\\b[^>]*>", RegexOption.IGNORE_CASE)
    private val outgoingTag = Regex("(^<\\s*/?\\s*)persona-(context|file)", RegexOption.IGNORE_CASE)
    private val outgoingURL = Regex("(\\burl=[\"'])persona://")

    fun hasContextTag(text: String) = text.contains("<persona-context") || text.contains("<jarvis-context")
    fun hasFileTag(text: String) = text.contains("<persona-file") || text.contains("<jarvis-file")
    fun isAppLink(text: String) = appSchemes.any { text.trim().startsWith("$it://") }
    fun canonicalAssistantPath(path: String?) = if (path == "j") "persona" else path
    fun canonicalTab(tab: String?) = if (tab.equals("jarvis", ignoreCase = true)) "persona" else tab
    private val builtinAliases = mapOf("jarvis" to "persona-visor", "jarvis-circuit" to "persona-circuit", "jarvis-pixel" to "persona-pixel", "jarvis-facet" to "persona-facet", "jarvis-blob" to "persona-blob")
    fun canonicalBuiltin(id: String?) = builtinAliases[id] ?: id
    fun builtinForHub(id: String, features: Collection<String> = emptyList()) = if (CAPABILITY in features) id else builtinAliases.entries.firstOrNull { it.value == id }?.key ?: id
    fun managedProvider(source: String?) = source in personaServers
    fun canonicalTool(name: String) = when (name) {
        "ask_jarvis" -> "ask_persona"
        "jarvis_settings" -> "persona_settings"
        else -> name
    }

    /** Canonical fields win, including explicit false; opaque tool JSON never passes through this alias lookup. */
    fun fieldAliases(name: String): List<String> = when (name) {
        "started_by_persona" -> listOf("started_by_jarvis")
        else -> emptyList()
    }

    fun replyReceiptSeed(kind: String, clientId: String) = "jarvis:reply:$kind:$clientId"
    fun operationHeaders(id: String) = operationHeaders.associateWith { id }

    /** Use old spellings until the target explicitly advertises its contract; the UI still composes canonical markup. */
    fun messageForHub(text: String, features: Collection<String> = emptyList()): String {
        if (CAPABILITY in features) return text
        return outgoingElement.replace(text) { tag ->
            outgoingURL.replace(outgoingTag.replaceFirst(tag.value, "$1jarvis-$2"), "$1jarvis://")
        }
    }
}
