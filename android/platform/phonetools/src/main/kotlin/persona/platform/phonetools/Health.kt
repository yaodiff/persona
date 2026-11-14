package persona.platform.phonetools

import android.content.Context
import android.content.Intent
import androidx.health.connect.client.HealthConnectClient
import androidx.health.connect.client.permission.HealthPermission
import androidx.health.connect.client.records.ActiveCaloriesBurnedRecord
import androidx.health.connect.client.records.BasalMetabolicRateRecord
import androidx.health.connect.client.records.DistanceRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.NutritionRecord
import androidx.health.connect.client.records.Record
import androidx.health.connect.client.records.RestingHeartRateRecord
import androidx.health.connect.client.records.SleepSessionRecord
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.request.AggregateGroupByPeriodRequest
import androidx.health.connect.client.request.ReadRecordsRequest
import androidx.health.connect.client.time.TimeRangeFilter
import persona.core.contracts.api.PhoneToolResult
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import java.time.Duration
import java.time.Instant
import java.time.LocalDateTime
import java.time.Period
import java.time.ZoneId
import kotlin.reflect.KClass

/**
 * `phone_health_summary` (iOS `HealthTool`) on Health Connect: per-day aggregates, read only. Health Connect is part
 * of the system from Android 14; on 8-13 it is an app from the Play Store, which phones without Google services
 * usually don't have — then the tool says so instead of failing.
 */
object Health {
    /** Record types per metric, in the shared phone-tool schema's order. */
    val recordTypes: Map<String, KClass<out Record>> = linkedMapOf(
        "steps" to StepsRecord::class,
        "distance" to DistanceRecord::class,
        "active_energy" to ActiveCaloriesBurnedRecord::class,
        "heart_rate" to HeartRateRecord::class,
        "resting_heart_rate" to RestingHeartRateRecord::class,
        "sleep" to SleepSessionRecord::class,
        "weight" to WeightRecord::class,
        "dietary_energy" to NutritionRecord::class,
        "protein" to NutritionRecord::class,
        "carbs" to NutritionRecord::class,
        "fat" to NutritionRecord::class,
        "resting_energy" to BasalMetabolicRateRecord::class,
    )

    /** What Settings › Phone data › Health asks for: read access to every metric, nothing to write. */
    val permissions: Set<String> by lazy { recordTypes.values.map { HealthPermission.getReadPermission(it) }.toSet() }

    /** The schema's enum (iOS builds it from a dictionary, so its order differs run to run; the set is the same). */
    val metricNames: List<String> get() = recordTypes.keys.toList()

    fun status(c: Context): Int = runCatching { HealthConnectClient.getSdkStatus(c) }.getOrDefault(HealthConnectClient.SDK_UNAVAILABLE)
    fun available(c: Context) = status(c) == HealthConnectClient.SDK_AVAILABLE

    /** Why it can't be used on this phone, for the settings row and the tool error; null when it can. */
    fun unavailableReason(c: Context): String? = when (status(c)) {
        HealthConnectClient.SDK_AVAILABLE -> null
        HealthConnectClient.SDK_UNAVAILABLE_PROVIDER_UPDATE_REQUIRED -> c.getString(R.string.phonetools_health_update_required)
        else -> c.getString(R.string.phonetools_health_missing)
    }

    /** The same, in the words the tool result gives the model (not the phone's language). */
    private fun modelReason(c: Context): String? = when (status(c)) {
        HealthConnectClient.SDK_AVAILABLE -> null
        HealthConnectClient.SDK_UNAVAILABLE_PROVIDER_UPDATE_REQUIRED -> "Health Connect 需要更新"  // i18n-ignore: tool result for the model
        else -> "这台手机没有 Health Connect"  // i18n-ignore: tool result for the model
    }

    /** The read permissions granted now (empty when Health Connect is missing or does not answer). */
    suspend fun granted(c: Context): Set<String> =
        if (!available(c)) emptySet()
        else runCatching { HealthConnectClient.getOrCreate(c).permissionController.getGrantedPermissions() }.getOrDefault(emptySet())

    /** Where to install / update Health Connect (Android 13 and older, where it is an app). */
    fun installIntent(): Intent = Intent(Intent.ACTION_VIEW, android.net.Uri.parse("market://details?id=$PROVIDER"))

    /** Health Connect's own package on Android 8-13 (also in the manifest's `<queries>`). */
    private const val PROVIDER = "com.google.android.apps.healthdata"

    /** The requested metrics: known names, each once, in the order asked; `steps` when none (iOS `summary`). */
    fun metrics(args: JsonObject): List<String> {
        val out = mutableListOf<String>()
        val raw = args["metrics"] as? JsonArray ?: return listOf("steps")
        for (item in raw) {
            val name = (item as? JsonPrimitive)?.takeIf { it.isString }?.content ?: continue
            if (name !in recordTypes) invalid("unknown metric \"$name\"; use one of ${metricNames.joinToString(", ")}")
            if (name !in out) out += name
        }
        return out.ifEmpty { listOf("steps") }
    }

    /** Health Connect's sleep stages that count as asleep (iOS `allAsleepValues`): not awake, out of bed or unknown. */
    val asleepStages = setOf(
        SleepSessionRecord.STAGE_TYPE_SLEEPING,
        SleepSessionRecord.STAGE_TYPE_LIGHT,
        SleepSessionRecord.STAGE_TYPE_DEEP,
        SleepSessionRecord.STAGE_TYPE_REM,
    )

    /**
     * Hours asleep per local day (yyyy-MM-dd → hours, one decimal): every span clipped to [start, end] and split at
     * midnight, so a night that crosses it counts on both days (iOS `sleepByDay`).
     */
    fun sleepByDay(spans: List<Pair<Instant, Instant>>, start: Instant, end: Instant, zone: ZoneId): Map<String, Double> {
        val seconds = sortedMapOf<String, Long>()
        for ((s, e) in spans) {
            var cursor = maxOf(s, start)
            val stop = minOf(e, end)
            while (cursor < stop) {
                val dayStart = cursor.atZone(zone).toLocalDate().atStartOfDay(zone)
                val chunkEnd = minOf(dayStart.plusDays(1).toInstant(), stop)
                val day = dayStart.toLocalDate().toString()
                seconds[day] = (seconds[day] ?: 0L) + Duration.between(cursor, chunkEnd).seconds
                cursor = chunkEnd
            }
        }
        return seconds.filterValues { it > 0 }.mapValues { round1(it.value / 3600.0) }
    }

    fun round1(v: Double): Double = Math.round(v * 10) / 10.0

    /** The window: 00:00 of the day `days - 1` back, to now (today's numbers are partial). */
    fun window(days: Int, now: Instant, zone: ZoneId): Pair<Instant, Instant> =
        now.atZone(zone).toLocalDate().minusDays((days - 1).toLong()).atStartOfDay(zone).toInstant() to now

    suspend fun summary(c: Context, args: JsonObject): PhoneToolResult {
        modelReason(c)?.let {
            unavailable("$it，读不了健康数据。\nHealth Connect is " +  // i18n-ignore: tool result for the model
                (if (status(c) == HealthConnectClient.SDK_UNAVAILABLE_PROVIDER_UPDATE_REQUIRED) "out of date" else "not available") +
                " on this Android phone, so there is no health data to read.")
        }
        val days = args.int("days", 7, 1, 90)
        val requested = metrics(args)
        val client = HealthConnectClient.getOrCreate(c)
        val granted = client.permissionController.getGrantedPermissions()
        val usable = requested.filter { HealthPermission.getReadPermission(recordTypes.getValue(it)) in granted }
        if (usable.isEmpty()) {
            unavailable("没有权限：健康 / Permission denied: health.\n" + PhoneToolText.grantHint(PhoneSource.Health))  // i18n-ignore: tool result for the model
        }
        val zone = ZoneId.systemDefault()
        val (start, end) = window(days, Instant.now(), zone)
        val byDay = sortedMapOf<String, MutableMap<String, JsonElement>>()
        for (metric in usable) {
            val rows: Map<String, JsonElement> = try {
                if (metric == "sleep") sleep(client, start, end, zone).mapValues { JsonPrimitive(it.value) }
                else quantity(client, metric, start, end, zone)
            } catch (e: Exception) {
                // One failed metric should not sink the whole summary.
                mapOf(end.atZone(zone).toLocalDate().toString() to buildJsonObject { put("error", e.message ?: e.javaClass.simpleName) })
            }
            for ((day, v) in rows) byDay.getOrPut(day) { linkedMapOf() }[metric] = v
        }
        return PhoneToolResult.text(buildJsonObject {
            put("start", iso(start, zone))
            put("end", iso(end, zone))
            put("days", days)
            put("metrics", buildJsonArray { usable.forEach { add(JsonPrimitive(it)) } })
            // Health Connect, unlike HealthKit, says what was not granted: tell the model instead of an empty column.
            (requested - usable.toSet()).takeIf { it.isNotEmpty() }?.let { m ->
                put("not_granted", buildJsonArray { m.forEach { add(JsonPrimitive(it)) } })
            }
            put("data", buildJsonArray {
                byDay.forEach { (day, values) ->
                    add(JsonObject(linkedMapOf<String, JsonElement>("date" to JsonPrimitive(day)) + values))
                }
            })
        }.toString())
    }

    internal suspend fun quantity(client: HealthConnectClient, metric: String, start: Instant, end: Instant, zone: ZoneId): Map<String, JsonElement> {
        val wanted = when (metric) {
            "steps" -> setOf(StepsRecord.COUNT_TOTAL)
            "distance" -> setOf(DistanceRecord.DISTANCE_TOTAL)
            "active_energy" -> setOf(ActiveCaloriesBurnedRecord.ACTIVE_CALORIES_TOTAL)
            // Aggregate basal calories over the window; the record's raw BMR is a power, not an energy total.
            "resting_energy" -> setOf(BasalMetabolicRateRecord.BASAL_CALORIES_TOTAL)
            "dietary_energy" -> setOf(NutritionRecord.ENERGY_TOTAL)
            "protein" -> setOf(NutritionRecord.PROTEIN_TOTAL)
            "carbs" -> setOf(NutritionRecord.TOTAL_CARBOHYDRATE_TOTAL)
            "fat" -> setOf(NutritionRecord.TOTAL_FAT_TOTAL)
            "heart_rate" -> setOf(HeartRateRecord.BPM_AVG, HeartRateRecord.BPM_MIN, HeartRateRecord.BPM_MAX)
            "resting_heart_rate" -> setOf(RestingHeartRateRecord.BPM_AVG)
            "weight" -> setOf(WeightRecord.WEIGHT_AVG)
            else -> invalid("unknown metric \"$metric\"")
        }
        // Local date-times: Health Connect slices by the phone's days, so a day runs midnight to midnight here too.
        val groups = client.aggregateGroupByPeriod(
            AggregateGroupByPeriodRequest(
                metrics = wanted,
                timeRangeFilter = TimeRangeFilter.between(LocalDateTime.ofInstant(start, zone), LocalDateTime.ofInstant(end, zone)),
                timeRangeSlicer = Period.ofDays(1),
            ),
        )
        val out = linkedMapOf<String, JsonElement>()
        for (g in groups) {
            val r = g.result
            val v: JsonElement? = when (metric) {
                "steps" -> r[StepsRecord.COUNT_TOTAL]?.let { JsonPrimitive(it) }
                "distance" -> r[DistanceRecord.DISTANCE_TOTAL]?.let { JsonPrimitive(round1(it.inMeters)) }
                "active_energy" -> r[ActiveCaloriesBurnedRecord.ACTIVE_CALORIES_TOTAL]?.let { JsonPrimitive(round1(it.inKilocalories)) }
                "resting_energy" -> r[BasalMetabolicRateRecord.BASAL_CALORIES_TOTAL]?.let { JsonPrimitive(round1(it.inKilocalories)) }
                "dietary_energy" -> r[NutritionRecord.ENERGY_TOTAL]?.let { JsonPrimitive(round1(it.inKilocalories)) }
                "protein" -> r[NutritionRecord.PROTEIN_TOTAL]?.let { JsonPrimitive(round1(it.inGrams)) }
                "carbs" -> r[NutritionRecord.TOTAL_CARBOHYDRATE_TOTAL]?.let { JsonPrimitive(round1(it.inGrams)) }
                "fat" -> r[NutritionRecord.TOTAL_FAT_TOTAL]?.let { JsonPrimitive(round1(it.inGrams)) }
                "heart_rate" -> r[HeartRateRecord.BPM_AVG]?.let { avg ->
                    buildJsonObject {
                        put("average", round1(avg.toDouble()))
                        r[HeartRateRecord.BPM_MIN]?.let { put("min", round1(it.toDouble())) }
                        r[HeartRateRecord.BPM_MAX]?.let { put("max", round1(it.toDouble())) }
                    }
                }
                "resting_heart_rate" -> r[RestingHeartRateRecord.BPM_AVG]?.let { JsonPrimitive(round1(it.toDouble())) }
                "weight" -> r[WeightRecord.WEIGHT_AVG]?.let { JsonPrimitive(round1(it.inKilograms)) }
                else -> null
            }
            if (v != null) out[g.startTime.toLocalDate().toString()] = v
        }
        return out
    }

    private suspend fun sleep(client: HealthConnectClient, start: Instant, end: Instant, zone: ZoneId): Map<String, Double> {
        // A night that began the evening before the window still counts for its part inside it.
        val sessions = client.readRecords(ReadRecordsRequest(SleepSessionRecord::class, TimeRangeFilter.between(start.minus(Duration.ofDays(1)), end))).records
        val spans = sessions.flatMap { s ->
            if (s.stages.isEmpty()) listOf(s.startTime to s.endTime)
            else s.stages.filter { it.stage in asleepStages }.map { it.startTime to it.endTime }
        }
        return sleepByDay(spans, start, end, zone)
    }
}
