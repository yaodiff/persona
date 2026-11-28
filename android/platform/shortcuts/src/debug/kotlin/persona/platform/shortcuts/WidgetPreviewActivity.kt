package persona.platform.shortcuts

import android.app.Activity
import android.appwidget.AppWidgetHost
import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.os.Bundle
import android.util.SizeF
import android.util.TypedValue
import android.view.Gravity
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import kotlin.math.ceil

/**
 * Debug builds only: the home-screen widgets hosted the way a launcher hosts them (AppWidgetHost, RemoteViews), at
 * common sizes over the wallpaper, for captures without dragging them onto a home screen. Pass `--es debug.widget main`
 * for [MainChatWidget] (`2×2` / `4×2` / `4×3`), `ask` for the Ask Persona widget, `agents` (default) for the Agents
 * widget, or `all` for all three. Needs the bind permission once:
 *
 *     adb shell appwidget grantbind --package <package> --user 0
 *     adb shell am start -n <package>/persona.platform.shortcuts.WidgetPreviewActivity
 *     adb shell am start -n <package>/persona.platform.shortcuts.WidgetPreviewActivity --es debug.widget main
 *     adb shell am start -n <package>/persona.platform.shortcuts.WidgetPreviewActivity --es debug.widget ask
 */
class WidgetPreviewActivity : Activity() {
    private lateinit var host: AppWidgetHost
    private val ids = mutableListOf<Int>()

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val manager = AppWidgetManager.getInstance(this)
        val targets = when (intent?.getStringExtra(EXTRA_WIDGET)?.lowercase(java.util.Locale.ROOT)) {
            WIDGET_MAIN -> listOf(MainChatWidgetReceiver::class.java to MAIN_CHAT_SIZES)
            "ask" -> listOf(AskPersonaWidgetReceiver::class.java to ASK_SIZES)
            "all" -> listOf(
                AskPersonaWidgetReceiver::class.java to ASK_SIZES,
                MainChatWidgetReceiver::class.java to MAIN_CHAT_SIZES,
                PersonaWidgetReceiver::class.java to AGENT_SIZES,
            )
            else -> listOf(PersonaWidgetReceiver::class.java to AGENT_SIZES)
        }
        host = AppWidgetHost(this, HOST_ID)
        host.startListening()
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
        }
        fun dp(value: Float) = ceil(TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, resources.displayMetrics)).toInt()
        for ((receiver, sizes) in targets) {
            val provider = ComponentName(this, receiver)
            val info = manager.installedProviders.firstOrNull { it.provider == provider }
            if (info == null) {
                column.addView(TextView(this).apply { text = "找不到 Persona 小组件" })  // i18n-ignore: debug preview, developer-only
                continue
            }
            for ((width, height) in sizes) {
                val id = host.allocateAppWidgetId()
                ids += id
                if (!manager.bindAppWidgetIdIfAllowed(id, provider)) {
                    column.addView(TextView(this).apply { text = "先运行 adb shell appwidget grantbind --package $packageName --user 0" })  // i18n-ignore: debug preview, developer-only
                    break
                }
                val view = host.createView(this, id, info)
                // As a launcher does: the widget's content gets the whole size. AppWidgetHostView would otherwise add its
                // default padding and report a size that much smaller (updateAppWidgetSize takes the padding off).
                view.setPadding(0, 0, 0, 0)
                manager.updateAppWidgetOptions(id, Bundle().apply {
                    putParcelableArrayList(AppWidgetManager.OPTION_APPWIDGET_SIZES, arrayListOf(SizeF(width, height)))
                    putInt(AppWidgetManager.OPTION_APPWIDGET_MIN_WIDTH, width.toInt())
                    putInt(AppWidgetManager.OPTION_APPWIDGET_MAX_WIDTH, width.toInt())
                    putInt(AppWidgetManager.OPTION_APPWIDGET_MIN_HEIGHT, height.toInt())
                    putInt(AppWidgetManager.OPTION_APPWIDGET_MAX_HEIGHT, height.toInt())
                })
                column.addView(view, LinearLayout.LayoutParams(dp(width), dp(height)).apply { topMargin = dp(16f) })
            }
        }
        val scroll = ScrollView(this).apply {
            isFillViewport = true
            fitsSystemWindows = true
            addView(column)
        }
        setContentView(scroll)
    }

    override fun onDestroy() {
        ids.forEach { val _ = runCatching { host.deleteAppWidgetId(it) } }
        val _ = runCatching { host.stopListening() }
        super.onDestroy()
    }

    private companion object {
        const val HOST_ID = 0x4A57
        const val EXTRA_WIDGET = "debug.widget"
        const val WIDGET_MAIN = "main"
        /**
         * The smallest the Agents widget can be, then the heights for one, two and three agents (WidgetLayout); two
         * agents at the width of two launcher cells on a phone, where fixed size buckets used to show none.
         */
        val AGENT_SIZES = listOf(110f to 100f, 250f to 160f, 170f to 210f, 250f to 260f)
        /** Common launcher sizes for Ask Persona: 2 × 1, 4 × 1, 2 × 2 and 4 × 2. */
        val ASK_SIZES = listOf(160f to 68f, 300f to 68f, 160f to 160f, 300f to 160f)
        /** MainChatWidget preview sizes: minimum 2x2, compact 2x2, default 4x2, and large 4x3. */
        val MAIN_CHAT_SIZES = listOf(110f to 100f, 140f to 140f, 290f to 150f, 290f to 250f)
    }
}
