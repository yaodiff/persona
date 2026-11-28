package persona.feature.files

import android.content.res.Resources
import androidx.annotation.StringRes
import persona.core.contracts.api.SessionGitCommit
import persona.core.contracts.api.SessionGitFile
import persona.core.contracts.api.SessionGitStatus
import persona.core.designsystem.Status
import java.io.ByteArrayOutputStream

// What the Git pages read out of the node's answers (git/status, git/log, git/show).

/** The file's own name; the folder it is in ("" at the repository root). */
internal val SessionGitFile.name: String get() = path.substringAfterLast('/')
internal val SessionGitFile.folder: String get() = if ('/' in path) path.substringBeforeLast('/') else ""

/** The tone of a change's letter: conflicts need you, staged changes are settled, the rest are plain. */
internal val SessionGitFile.tone: Status get() = when {
    conflicted -> Status.NeedsYou
    staged -> Status.Unread
    else -> Status.Unknown
}

/** Where a change stands: "Conflict", "Untracked", "Partially staged", "Staged" or "Unstaged". */
@get:StringRes
internal val SessionGitFile.stage: Int get() = when {
    conflicted -> R.string.files_git_conflicted
    untracked -> R.string.files_git_untracked
    staged && unstaged -> R.string.files_git_partly_staged
    staged -> R.string.files_git_staged
    else -> R.string.files_git_unstaged
}

/** Branch and tag names of a commit (`%D`): "HEAD -> main" becomes "main", a bare HEAD is dropped. */
internal val SessionGitCommit.refNames: List<String>
    get() = refs.orEmpty().split(',').map { it.trim().removePrefix("HEAD -> ") }.filter { it.isNotEmpty() && it != "HEAD" }

/**
 * A local branch (not a tag, not a branch of [remote]): the refs chip that stands out. Local branches may have
 * slashes too (feat/x), so the remote's name decides, from the upstream ("origin/main") when there is one.
 */
internal fun isLocalBranch(ref: String, remote: String = "origin") = !ref.startsWith("tag: ") && !ref.startsWith("$remote/")

/** The remote of an upstream branch ("origin/main" is origin). */
internal fun remoteOf(upstream: String?) = upstream?.substringBefore('/')?.takeIf { it.isNotEmpty() && '/' in upstream } ?: "origin"

internal val SessionGitCommit.shortHash: String get() = short.ifEmpty { hash.take(7) }

/** How the branch stands against its upstream: "In sync", deleted on the remote, or ↑ ahead ↓ behind; the stash when there is one. */
internal fun gitSync(resources: Resources, git: SessionGitStatus): String? {
    val sync = when {
        git.upstream == null -> null
        git.gone -> resources.getString(R.string.files_git_upstream_gone)
        git.ahead == 0 && git.behind == 0 -> resources.getString(R.string.files_git_synced)
        else -> listOfNotNull(git.ahead.takeIf { it > 0 }?.let { "↑$it" }, git.behind.takeIf { it > 0 }?.let { "↓$it" }).joinToString("  ")
    }
    return listOfNotNull(sync, git.stashes.takeIf { it > 0 }?.let { resources.getQuantityString(R.plurals.files_git_stashes, it, it) }).joinToString("  ").ifEmpty { null }
}

internal object GitDiffs {
    /**
     * A multi-file diff (`git show`) split per file, keyed by the new path. Handles spaces, `" b/"` segments inside
     * path names, and Git's C-quoted headers (`"a/…"` / `"b/…"` with UTF-8 octal or C escapes).
     */
    fun split(diff: String): Map<String, String> {
        val parts = LinkedHashMap<String, String>()
        var path: String? = null
        var inHeader = false
        val lines = ArrayList<String>()
        fun flush() { path?.let { parts[it] = lines.joinToString("\n") } }
        for (line in diff.split('\n')) {
            if (line.startsWith("diff --git ")) {
                flush()
                lines.clear()
                path = parseHeaderNewPath(line.removeSuffix("\r"))
                inHeader = true
            } else if (inHeader) {
                val clean = line.removeSuffix("\r")
                when {
                    clean.startsWith("@@ ") || clean.startsWith("--- ") || clean.startsWith("+++ ") ||
                        clean.startsWith("Binary files ") || clean.startsWith("GIT binary patch") -> inHeader = false
                    clean.startsWith("rename to ") -> path = unquoteGitPath(clean.removePrefix("rename to "))
                    clean.startsWith("copy to ") -> path = unquoteGitPath(clean.removePrefix("copy to "))
                }
            }
            lines += line
        }
        flush()
        return parts
    }

    /** Extracts the unquoted `b/…` path from a `diff --git <old> <new>` header line, or null if malformed. */
    private fun parseHeaderNewPath(line: String): String? {
        val rest = line.removePrefix("diff --git ")
        if (rest.startsWith('"')) {
            val (_, afterOld) = scanQuotedToken(rest, 0) ?: return null
            if (afterOld >= rest.length || rest[afterOld] != ' ') return null
            val secondStart = afterOld + 1
            val rawNew = if (secondStart < rest.length && rest[secondStart] == '"') {
                val (unquoted, end) = scanQuotedToken(rest, secondStart) ?: return null
                if (end != rest.length) return null
                unquoted
            } else {
                rest.substring(secondStart)
            }
            return rawNew.takeIf { it.startsWith("b/") }?.substring(2)
        }
        if (rest.endsWith('"')) {
            // An unquoted Git path never contains '"', so the space before the opening quote splits old and new.
            val sep = rest.indexOf(" \"")
            if (sep < 0) return null
            val (unquoted, end) = scanQuotedToken(rest, sep + 1) ?: return null
            if (end != rest.length) return null
            return unquoted.takeIf { it.startsWith("b/") }?.substring(2)
        }
        // Unquoted same-path diff (`a/<p> b/<p>`): exact midpoint avoids splitting on a `" b/"` inside `<p>`.
        val body = rest.length - 5
        if (body >= 0 && body % 2 == 0) {
            val pLen = body / 2
            if (rest.startsWith("a/") && rest.regionMatches(2 + pLen, " b/", 0, 3) &&
                rest.regionMatches(2, rest, 5 + pLen, pLen)
            ) {
                return rest.substring(5 + pLen)
            }
        }
        val at = rest.lastIndexOf(" b/")
        return if (at >= 0) rest.substring(at + 3) else null
    }

    /** Scans a Git C-quoted string starting at [start], returning `(unquoted, endIndex)` or null if unterminated. */
    private fun scanQuotedToken(s: String, start: Int): Pair<String, Int>? {
        if (start >= s.length || s[start] != '"') return null
        var i = start + 1
        while (i < s.length) {
            when (s[i]) {
                '\\' -> i += 2
                '"' -> return unquoteGitPath(s.substring(start, i + 1)) to (i + 1)
                else -> i++
            }
        }
        return null
    }

    /** Unquotes a Git C-quoted path (`quote.c` escapes and `\ooo` UTF-8 octal bytes); returns plain paths unchanged. */
    private fun unquoteGitPath(raw: String): String {
        if (raw.length < 2 || raw.first() != '"' || raw.last() != '"') return raw
        val out = ByteArrayOutputStream(raw.length)
        var i = 1
        val end = raw.length - 1
        var literalStart = i
        fun flushLiteral(until: Int) {
            if (until > literalStart) out.write(raw.substring(literalStart, until).toByteArray(Charsets.UTF_8))
        }
        while (i < end) {
            if (raw[i] != '\\') {
                i++
                continue
            }
            flushLiteral(i)
            i++
            if (i >= end) {
                out.write('\\'.code)
                literalStart = i
                break
            }
            val c = raw[i]
            if (c in '0'..'7' && i + 2 < end && raw[i + 1] in '0'..'7' && raw[i + 2] in '0'..'7') {
                val byte = (c - '0') * 64 + (raw[i + 1] - '0') * 8 + (raw[i + 2] - '0')
                if (byte <= 0xFF) {
                    out.write(byte)
                    i += 3
                    literalStart = i
                    continue
                }
            }
            val unescaped = when (c) {
                'a' -> 0x07
                'b' -> 0x08
                'f' -> 0x0C
                'n' -> 0x0A
                'r' -> 0x0D
                't' -> 0x09
                'v' -> 0x0B
                '\\' -> '\\'.code
                '"' -> '"'.code
                else -> -1
            }
            if (unescaped >= 0) {
                out.write(unescaped)
                i++
            } else {
                // Unknown escape: preserve the escaped character (including any trailing surrogate pair via flushLiteral).
                literalStart = i
                i++
                continue
            }
            literalStart = i
        }
        flushLiteral(end)
        return out.toString(Charsets.UTF_8.name())
    }

    /** One file's part of a commit's diff; [last] is true when the diff ends inside it (a cut diff is cut there). */
    data class Part(val diff: String?, val last: Boolean)

    fun part(diff: String, file: SessionGitFile): Part {
        val parts = split(diff)
        val key = file.path.takeIf { it in parts } ?: file.originalPath?.takeIf { it in parts }
        return Part(key?.let { parts[it] }, key != null && key == parts.keys.lastOrNull())
    }

    /** Added and deleted lines of the working tree's changes, for the "Changes" heading. */
    fun totals(files: List<SessionGitFile>): Pair<Int, Int> = files.sumOf { it.added } to files.sumOf { it.deleted }
}
