package persona.core.contracts.api

/** A parent reference in an inbox tree: another item, a to-do, or an Agent session (`<machine>/<session>`). */
data class InboxParentRef(val kind: String, val id: String, val by: String? = null)
