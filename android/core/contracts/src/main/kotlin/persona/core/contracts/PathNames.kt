package persona.core.contracts

/** Compact last path segment for POSIX or Windows paths, blank for bare roots (e.g. "/", "C:\"). */
fun compactFolderName(path: String?): String {
    val trimmed = path?.trim().orEmpty().trimEnd('/', '\\')
    if (trimmed.isEmpty()) return ""
    val segment = trimmed.substringAfterLast('/').substringAfterLast('\\')
    return if (segment.length == 2 && segment[0].isLetter() && segment[1] == ':') "" else segment
}
