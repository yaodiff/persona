package persona.feature.machines

import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import persona.core.contracts.*
import persona.core.designsystem.LocalizedFormats
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.isNestedSubtask
import persona.core.domain.sort.foldRows
import java.text.NumberFormat
import java.time.Instant
import java.time.ZoneId
import java.util.Locale
import kotlin.math.abs

internal fun percent(used: Number?, total: Number?): Double? =
    if (used != null && total != null && total.toDouble() > 0) used.toDouble() * 100 / total.toDouble() else null

/**
 * The live machine list (pushed over the WebSocket) sees a computer come and go before the next metrics poll does:
 * where it has the computer, its online state wins, so a computer that just dropped off is not shown Online with its
 * last numbers (#3730).
 */
internal fun MachineMetrics.live(machine: Machine?): MachineMetrics =
    if (machine == null || machine.online == online) this else copy(online = machine.online)

internal fun MachineMetrics.current(now: Long): MetricSample? =
    latest?.takeIf { online && now - it.t in 0..30 }

internal data class MetricAlert(@StringRes val label: Int, val critical: Boolean)
internal fun alerts(sample: MetricSample?, cores: Int?): List<MetricAlert> {
    if (sample == null) return emptyList()
    val result = mutableListOf<MetricAlert>()
    fun add(@StringRes label: Int, value: Double?, warning: Double, critical: Double) {
        if (value != null && value.isFinite() && value >= warning) result += MetricAlert(label, value >= critical)
    }
    add(R.string.machines_alert_cpu, sample.cpu, 75.0, 90.0)
    add(R.string.machines_alert_memory, percent(sample.memUsed, sample.memTotal), 80.0, 90.0)
    add(R.string.machines_alert_disk, percent(sample.diskUsed, sample.diskTotal), 80.0, 90.0)
    add(R.string.machines_alert_load, cores?.takeIf { it > 0 }?.let { sample.load?.div(it) }, 1.0, 2.0)
    return result.sortedByDescending { it.critical }
}

/**
 * A computer's running sessions, as the Agents list counts them on its computer chip and header: a subtask another
 * session started, listed under that one's "Subtasks", is not one of them unless it needs the user (#4494).
 */
internal fun machineSessions(sessions: List<Session>, machineId: String, hierarchy: Boolean = false): List<Session> =
    foldRows(sessions.filter { it.machineId == machineId }.map(::AgentSession), hierarchy)
    .filter { !it.session.assistant && it.isLive && !it.isNestedSubtask }.map { it.session }
    .sortedWith(compareBy<Session> { it.agentState.priority }.thenByDescending { it.updated })

/** History keeps the disk's used bytes; the chart shows them as a share of [total] (gaps stay gaps). */
internal fun diskPercentPoints(points: List<MetricPoint>, total: Long?): List<MetricPoint> =
    if (total == null || total <= 0) points.map { it.copy(disk = null) }
    else points.map { point -> point.copy(disk = point.disk?.let { it * 100 / total }) }

internal data class PlotPoint(val t: Long, val value: Double)

/** A separate path per run also breaks missing values for an individual measure. */
internal fun segments(points: List<MetricPoint>, step: Int, value: (MetricPoint) -> Double?): List<List<PlotPoint>> {
    val result = mutableListOf<List<PlotPoint>>()
    var run = mutableListOf<PlotPoint>()
    fun flush() { if (run.isNotEmpty()) result += run; run = mutableListOf() }
    for (point in points.sortedBy { it.t }) {
        val v = value(point)?.takeIf { it.isFinite() && it >= 0 }
        if (point.gap || v == null) { flush(); continue }
        if (run.lastOrNull()?.let { point.t - it.t > 3L * step } == true) flush()
        run += PlotPoint(point.t, v)
    }
    flush()
    return result
}

internal fun selectedPoint(points: List<MetricPoint>, time: Long, step: Int): MetricPoint? {
    val ordered = points.sortedBy { it.t }
    // Do not borrow an edge value from either side of an explicitly marked outage.
    val before = ordered.lastOrNull { it.t <= time }
    val after = ordered.firstOrNull { it.t >= time }
    if (before?.gap == true || after?.gap == true) return null
    if (before != null && after != null && after.t - before.t > 3L * step) return null
    return ordered.filterNot { it.gap }.minByOrNull { abs(it.t - time) }
        ?.takeIf { abs(it.t - time) <= 2L * step }
}

/** Numbers, sizes, rates and times on the machine pages, in the language of the page (docs/protocol/i18n.md). */
internal object MetricFormat {
    private fun oneDecimal(format: NumberFormat) = format.apply { minimumFractionDigits = 1; maximumFractionDigits = 1 }
    fun number(value: Double?, locale: Locale): String =
        value?.takeIf(Double::isFinite)?.let { oneDecimal(NumberFormat.getNumberInstance(locale)).format(it) } ?: "—"
    /** [value] is already a percentage (0–100). */
    fun percent(value: Double?, locale: Locale): String =
        value?.takeIf(Double::isFinite)?.let { oneDecimal(NumberFormat.getPercentInstance(locale)).format(it / 100) } ?: "—"
    fun bytes(value: Number?, locale: Locale): String {
        var v = value?.toDouble()?.takeIf { it.isFinite() && it >= 0 } ?: return "—"
        val units = listOf("B", "KB", "MB", "GB", "TB")
        var unit = 0
        // Pick the unit after rounding to the one decimal shown, so 1023.96 KB reads 1.0 MB, not 1,024.0 KB (#4260).
        while (Math.round(v * 10) >= 10240 && unit < units.lastIndex) { v /= 1024; unit++ }
        return "${number(v, locale)} ${units[unit]}"
    }
    fun rate(value: Double?, locale: Locale): String = if (value == null) "—" else "${bytes(value, locale)}/s"
    fun time(seconds: Long, locale: Locale): String = LocalizedFormats.format(Instant.ofEpochSecond(seconds), LocalizedFormats.TIME, locale)
    /** When the machine was last online (month, day and time), or null when the hub did not say. */
    fun lastSeen(value: String?, locale: Locale): String? = parseTime(value)?.let {
        LocalizedFormats.format(it, LocalizedFormats.MONTH_DAY_TIME, locale)
    }
    /** When the machine started, as Nov 2, 2026, 08:30 in the phone's zone (in the app's language). */
    fun boot(boot: Double?, locale: Locale, zone: ZoneId = ZoneId.systemDefault()): String? = boot?.takeIf { it.isFinite() && it > 0 }?.let {
        LocalizedFormats.format(Instant.ofEpochSecond(it.toLong()), LocalizedFormats.DATE_TIME, locale, zone)
    }
    /** How long it has been up, in minutes, or null without a boot time. */
    fun uptimeMinutes(boot: Double?, now: Long): Long? = boot?.takeIf { it.isFinite() && it > 0 && it <= now }?.let { (now - it.toLong()) / 60 }
}

/** How long it has been up: 3d 4h in English, the Chinese or Japanese form otherwise. */
@Composable
internal fun uptimeText(boot: Double?, now: Long): String? = MetricFormat.uptimeMinutes(boot, now)?.let { minutes ->
    when {
        minutes >= 1440 -> stringResource(R.string.machines_uptime_days, (minutes / 1440).toInt(), (minutes % 1440 / 60).toInt())
        minutes >= 60 -> stringResource(R.string.machines_uptime_hours, (minutes / 60).toInt(), (minutes % 60).toInt())
        else -> stringResource(R.string.machines_uptime_minutes, minutes.toInt())
    }
}

