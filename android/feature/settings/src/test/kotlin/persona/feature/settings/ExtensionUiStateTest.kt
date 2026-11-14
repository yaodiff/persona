package persona.feature.settings

import persona.core.contracts.api.*
import persona.core.designsystem.Tone
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import persona.core.designsystem.JChartKind
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4

private class FakeUi : ExtensionApiStub() {
    var view = ExtensionView(title = "Example 网关", state = "tab=1", sections = listOf(ExtensionViewSection(items = listOf(
        ExtensionViewItem("field", name = "note", kind = "text", value = "旧的"),
        ExtensionViewItem("field", name = "token", kind = "secret", value = "plugin must not prefill"),
        ExtensionViewItem("field", name = "auto", kind = "toggle", value = "true"),
        ExtensionViewItem("field", name = "plan", kind = "choice", value = "team", choices = listOf(ExtensionViewChoice("solo"), ExtensionViewChoice("team", "团队"))),
        ExtensionViewItem("button", title = "保存", action = ExtensionViewAction("save", submit = true)),
    ))))
    var answer = ExtensionUiResult()
    var fail: Exception? = null
    val renders = mutableListOf<Pair<ExtensionUiContext?, String?>>()
    val actions = mutableListOf<Triple<String, String?, Map<String, String>?>>()
    var places = listOf(ExtensionUiPlace("example-gateway", "panel", "Example 网关", "launcher_section"))
    var placeContext: Pair<String?, String?>? = null

    override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?): ExtensionView {
        renders += context to state
        fail?.let { throw it }
        return view
    }
    override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?,
        values: Map<String, String>?): ExtensionUiResult {
        actions += Triple(action, state, values)
        fail?.let { throw it }
        return answer
    }
    override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?): List<ExtensionUiPlace> {
        placeContext = launcher to machine
        fail?.let { throw it }
        return places
    }
}

/** Robolectric for the words: the app's strings in Simplified Chinese (robolectric.properties). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionUiStateTest {
    private val res get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources

    @Test fun dateFieldsReadInTheAppLanguageNotTheSystems() {
        // #4105: an English phone with the app in Japanese or Chinese.
        val saved = java.util.Locale.getDefault()
        java.util.Locale.setDefault(java.util.Locale.US)
        try {
            assertEquals("2026/11/11", ExtensionViewText.whenText("date", "2026-11-11", java.util.Locale.JAPANESE))
            assertEquals("2026年11月11日", ExtensionViewText.whenText("date", "2026-11-11", java.util.Locale.SIMPLIFIED_CHINESE))
            assertEquals("2026年11月11日 14:30", ExtensionViewText.whenText("datetime", "2026-11-11T14:30", java.util.Locale.SIMPLIFIED_CHINESE))
            assertEquals("Nov 11, 2026", ExtensionViewText.whenText("date", "2026-11-11", java.util.Locale.ENGLISH))
            assertEquals("14:30", ExtensionViewText.whenText("time", "14:30", java.util.Locale.JAPANESE))
            assertEquals("soon", ExtensionViewText.whenText("date", "soon", java.util.Locale.JAPANESE))
        } finally {
            java.util.Locale.setDefault(saved)
        }
    }

    @Test fun timeFieldsFollowTheAppLanguagesClock() {
        // #4128: 24-hour in Japanese and Chinese, 12-hour in English, whatever the system language; the date part as #4105 left it.
        val saved = java.util.Locale.getDefault()
        java.util.Locale.setDefault(java.util.Locale.JAPAN)
        try {
            assertEquals("14:30", ExtensionViewText.whenText("time", "14:30", java.util.Locale.JAPANESE))
            assertEquals("2026/11/11 14:30", ExtensionViewText.whenText("datetime", "2026-11-11T14:30", java.util.Locale.JAPANESE))
            assertEquals("14:30", ExtensionViewText.whenText("time", "14:30", java.util.Locale.SIMPLIFIED_CHINESE))
            assertEquals("2026年11月11日 14:30", ExtensionViewText.whenText("datetime", "2026-11-11T14:30", java.util.Locale.SIMPLIFIED_CHINESE))
            // English puts a narrow no-break space before AM / PM on newer ICU, a plain space on older ones.
            val time = ExtensionViewText.whenText("time", "14:30", java.util.Locale.ENGLISH)
            assertTrue(time, time.startsWith("2:30") && time.endsWith("PM"))
            val morning = ExtensionViewText.whenText("time", "09:05", java.util.Locale.ENGLISH)
            assertTrue(morning, morning.startsWith("9:05") && morning.endsWith("AM"))
            val datetime = ExtensionViewText.whenText("datetime", "2026-11-11T14:30", java.util.Locale.ENGLISH)
            assertTrue(datetime, datetime.startsWith("Nov 11, 2026 2:30") && datetime.endsWith("PM"))
        } finally {
            java.util.Locale.setDefault(saved)
        }
    }

    @Test fun anImagesViewerFollowsThePictureNotItsPlace() {
        val picture = ExtensionImageSource(url = "https://example.com/a.png", path = "/api/v1/extensions/x/files/a")
        // the extension's key first, then the hub's path, the address, the ref
        assertEquals("key:hero", ExtensionViewText.imageIdentity(ExtensionViewItem("image", key = "hero", src = picture)))
        assertEquals("path:/api/v1/extensions/x/files/a", ExtensionViewText.imageIdentity(ExtensionViewItem("image", key = " ", src = picture)))
        assertEquals("url:https://example.com/b.png", ExtensionViewText.imageIdentity(ExtensionViewItem("image", src = ExtensionImageSource(url = "https://example.com/b.png"))))
        assertEquals("ref:cover", ExtensionViewText.imageIdentity(ExtensionViewItem("image", src = ExtensionImageSource(ref = "cover"))))
        // two unkeyed pictures that swap places are told apart
        assertNotEquals(ExtensionViewText.imageIdentity(ExtensionViewItem("image", src = ExtensionImageSource(url = "https://example.com/a.png"))),
            ExtensionViewText.imageIdentity(ExtensionViewItem("image", src = ExtensionImageSource(url = "https://example.com/b.png"))))
        // nothing to go by: the viewer stays with its place, as before
        assertNull(ExtensionViewText.imageIdentity(ExtensionViewItem("image", src = ExtensionImageSource())))
        assertNull(ExtensionViewText.imageIdentity(ExtensionViewItem("image")))
    }

    @Test fun tonesLinksAndItemsTheAppDraws() {
        assertEquals(Tone.Positive, ExtensionViewText.tone("positive"))
        assertEquals(Tone.Danger, ExtensionViewText.tone("danger"))
        assertEquals(Tone.Caution, ExtensionViewText.tone("caution"))
        assertEquals(Tone.Neutral, ExtensionViewText.tone("purple"))
        assertEquals(Tone.Neutral, ExtensionViewText.tone(null))
        assertTrue(ExtensionViewText.isHttps("https://example.com/console"))
        assertFalse(ExtensionViewText.isHttps("http://example.com"))
        assertFalse(ExtensionViewText.isHttps("https://"))
        assertFalse(ExtensionViewText.isHttps("javascript:alert(1)"))
        assertTrue(ExtensionViewText.drawable(ExtensionViewItem("link", title = "后台", url = "https://example.com")))
        assertFalse(ExtensionViewText.drawable(ExtensionViewItem("link", title = "后台", url = "http://example.com")))
        assertFalse(ExtensionViewText.drawable(ExtensionViewItem("chart")))
        assertFalse(ExtensionViewText.drawable(ExtensionViewItem("button", title = "无动作")))
        assertFalse(ExtensionViewText.drawable(ExtensionViewItem("field", name = "x", kind = "future-kind")))
        listOf("text", "secret", "number", "toggle", "choice", "date", "time", "datetime", "multichoice", "multiline").forEach {
            assertTrue(it, ExtensionViewText.drawable(ExtensionViewItem("field", name = "x", kind = it)))
        }
        assertTrue(ExtensionViewText.grouped(ExtensionViewItem("row")))
        assertTrue(ExtensionViewText.grouped(ExtensionViewItem("field", kind = "choice")))
        assertFalse(ExtensionViewText.grouped(ExtensionViewItem("field", kind = "text")))
        assertFalse(ExtensionViewText.grouped(ExtensionViewItem("button")))
        assertEquals(1f, ExtensionViewText.ratio(ExtensionViewItem("progress", ratio = 1.4)))
        assertEquals(0f, ExtensionViewText.ratio(ExtensionViewItem("progress", ratio = -1.0)))
        assertEquals("72%", ExtensionViewText.percent(ExtensionViewItem("progress", ratio = 0.72)))
        assertTrue(ExtensionViewText.isNumber("-12.5") && ExtensionViewText.isNumber(""))
        assertFalse(ExtensionViewText.isNumber("12a"))
        assertEquals("gw.example.com", ExtensionViewText.host("https://gw.example.com/v1"))
        assertNull(ExtensionViewText.host(""))
        assertEquals("停用测试 Key", ExtensionViewText.confirmLabel(res, ExtensionViewItem("button", title = "停用测试 Key")))
        assertEquals("确定", ExtensionViewText.confirmLabel(res, ExtensionViewItem("row", title = "一个很长很长很长的按钮名字")))
    }

    @Test fun drawsWithTheFieldsAsSentButNeverASecret() = runBlocking {
        val api = FakeUi()
        val model = ExtensionViewModel(api, "example-gateway", "home")
        model.load()
        val s = model.state.value
        assertEquals("Example 网关", s.view?.title)
        assertEquals(mapOf("note" to "旧的", "token" to "", "auto" to "true", "plan" to "team"), s.values)
        assertNull(api.renders.single().second)
        // Drawing again sends the extension's state back.
        model.load()
        assertEquals("tab=1", api.renders.last().second)
    }

    @Test fun aSubmittingButtonSendsTheFieldsAndANewViewReplacesThePage() = runBlocking {
        val api = FakeUi()
        val model = ExtensionViewModel(api, "example-gateway", "home")
        model.load()
        model.edit("note", "新的")
        model.edit("token", "s3cret")
        model.edit("auto", "false")
        api.answer = ExtensionUiResult(view = ExtensionView(title = "已保存", sections = emptyList()), toast = "已保存")
        val out = model.press(ExtensionViewAction("save", submit = true))
        val (action, state, values) = api.actions.single()
        assertEquals("save", action)
        assertEquals("tab=1", state)
        assertEquals(mapOf("note" to "新的", "token" to "s3cret", "auto" to "false", "plan" to "team"), values)
        assertEquals("已保存", out.toast)
        assertEquals("已保存", model.state.value.view?.title)
        assertNull(model.state.value.acting)
    }

    @Test fun aPlainButtonSendsNoValuesAndNothingBackDrawsAgain() = runBlocking {
        val api = FakeUi()
        val model = ExtensionViewModel(api, "example-gateway", "home")
        model.load()
        val out = model.press(ExtensionViewAction("refresh"))
        assertNull(api.actions.single().third)
        assertEquals(ExtensionActionOutcome(), out)
        assertEquals(2, api.renders.size)
    }

    @Test fun aLinkBackMustBeHttpsAndAToastDoesNotRedraw() = runBlocking {
        val api = FakeUi()
        val model = ExtensionViewModel(api, "example-gateway", "home")
        model.load()
        api.answer = ExtensionUiResult(openUrl = "https://example.com/keys")
        assertEquals("https://example.com/keys", model.press(ExtensionViewAction("key")).openUrl)
        api.answer = ExtensionUiResult(openUrl = "http://example.com/keys", toast = "好")
        val out = model.press(ExtensionViewAction("key"))
        assertNull(out.openUrl)
        assertEquals("好", out.toast)
        assertEquals(1, api.renders.size)
    }

    /** Set Up with … (#5308): `persona://ask` is taken only on the subscription pages, and refused everywhere else. */
    @Test fun askIsTakenOnlyOnTheSubscriptionPages() = runBlocking {
        val good = "persona://ask?text=Help%20me"
        assertEquals("Help me", ExtensionViewText.askText(good))
        assertEquals("Help me", ExtensionViewText.askFrom(SUBSCRIPTIONS_UI, good))
        assertEquals("a+b", ExtensionViewText.askText("persona://ask?text=a%2Bb"))
        assertEquals("a+b", ExtensionViewText.askText("persona://ask?text=a+b"))
        assertEquals("hello", ExtensionViewText.askText("jarvis://ask?text=hello"))
        assertEquals("hello", ExtensionViewText.askText("persona://ask/?text=hello"))
        assertEquals(" hi", ExtensionViewText.askText("persona://ask?text=%20hi"))
        assertEquals("a".repeat(ExtensionViewText.ASK_MOST), ExtensionViewText.askText("persona://ask?text=" + "a".repeat(ExtensionViewText.ASK_MOST)))
        listOf(
            "persona://ask?text=",
            "persona://ask?text=%20",
            "persona://ask/x?text=a",
            "persona://asks?text=a",
            "https://example.com/ask?text=a",
            "persona://ask?text=" + "a".repeat(ExtensionViewText.ASK_MOST + 1),
        ).forEach { assertNull(it, ExtensionViewText.askText(it)) }
        assertNull(ExtensionViewText.askFrom("example-gateway", good))
        assertNull(ExtensionViewText.askFrom("", good))
        val api = FakeUi()
        val ask = "persona://ask?text=" + java.net.URLEncoder.encode("<persona-context kind=\"context\" title=\"Source\" url=\"\">\nconnector: apps\nHelp me", "UTF-8")
        api.answer = ExtensionUiResult(openUrl = ask)
        val elsewhere = ExtensionViewModel(api, "example-gateway", "home")
        elsewhere.load()
        assertNull(elsewhere.press(ExtensionViewAction("setup")).openUrl)
        val pages = ExtensionViewModel(api, SUBSCRIPTIONS_UI, "pages")
        pages.load()
        assertEquals(ask, pages.press(ExtensionViewAction("setup")).openUrl)
        assertEquals(ExtensionViewText.askText(ask), ExtensionViewText.askFrom(SUBSCRIPTIONS_UI, ask))
        assertNull(ExtensionViewText.askFrom("example-gateway", ask))
    }

    @Test fun failuresSayWhyAndKeepTheView() = runBlocking {
        val api = FakeUi()
        val model = ExtensionViewModel(api, "example-gateway", "home")
        api.fail = HubFailure(HubError.Server, "扩展出错了", status = 502, code = "plugin_failed")
        model.load()
        assertEquals("读取失败：扩展出错了", model.state.value.error?.text(res))
        assertNull(model.state.value.view)
        assertFalse(model.state.value.unsupported)
        api.fail = null
        model.load()
        assertNull(model.state.value.error?.text(res))
        api.fail = HubFailure(HubError.Server, "超时", status = 502)
        val out = model.press(ExtensionViewAction("save"))
        assertEquals("没有做成：超时", out.error?.text(res))
        assertNotNull(model.state.value.view)
    }

    // #1339: a launcher section without hosts is listed on every launcher; an extension that says the gateway is not its
    // own answers 404 unsupported, and the section is hidden: no error, nothing drawn.
    @Test fun anUnsupportedGatewayHidesTheSectionWithoutAnError() = runBlocking {
        val api = FakeUi()
        val context = ExtensionUiContext("work", "workstation")
        val model = ExtensionViewModel(api, "example-gateway", "other", context)
        api.fail = ExtensionUiUnsupported()
        model.load()
        val s = model.state.value
        assertTrue(s.unsupported)
        assertNull(s.error)
        assertNull(s.view)
        assertEquals(context, api.renders.single().first)
        // Answered by a button too (the extension changed its mind): hidden, no error.
        api.fail = null
        model.load()
        assertFalse(model.state.value.unsupported)
        api.fail = ExtensionUiUnsupported()
        assertEquals(ExtensionActionOutcome(), model.press(ExtensionViewAction("refresh")))
        assertTrue(model.state.value.unsupported)
    }

    @Test fun theLaunchersPlacesAskWithTheLauncherAndMachine() = runBlocking {
        val api = FakeUi()
        api.places = api.places + ExtensionUiPlace("other", "page", "别的", "plugin_page")
        val places = ExtensionPlacesModel(api, "launcher_section", ExtensionUiContext("work", "workstation"))
        places.load()
        assertEquals("work" to "workstation", api.placeContext)
        assertEquals(listOf("panel"), places.state.value.places?.map { it.id })
        api.fail = HubFailure(HubError.Server, "坏了", status = 500)
        places.load()
        assertEquals("扩展的内容读不到：坏了", places.state.value.error?.text(res))
        assertEquals(1, places.state.value.places?.size)
    }

    // UI version 2 (docs/extensions/extensions.md "UI slots" "New items"): charts, number grids and segmented controls draw; a kind this
    // app does not know, or one it cannot draw, is skipped and the rest of the view stays.
    @Test fun theNewItemsDrawAndUnknownOnesAreSkipped() {
        val chart = ExtensionViewItem("chart", kind = "bar", unit = "tokens", stacked = true, series = listOf(
            ExtensionChartSeries("输入", points = listOf(ExtensionChartPoint("11-01", 1_200_000.0, "1.2M"), ExtensionChartPoint("11-02", 800_000.0))),
            ExtensionChartSeries("输出", "positive", listOf(ExtensionChartPoint("11-01", 3.0), ExtensionChartPoint("11-02", 4.5), ExtensionChartPoint("11-03", 9.0))),
            ExtensionChartSeries("别的", "purple", listOf(ExtensionChartPoint("11-01", 1.0), ExtensionChartPoint("11-02", 2.0)))))
        assertTrue(ExtensionViewText.drawable(chart))
        val data = ExtensionViewText.chart(chart)!!
        assertEquals(JChartKind.Bar, data.kind)
        assertTrue(data.stacked)
        assertEquals(listOf("11-01", "11-02"), data.labels)
        assertEquals(listOf("1.2M", "800K tokens"), data.series[0].displays)
        // Every series has as many points as the labels; a tone is a role colour, an unknown one neutral, none the series colour.
        assertEquals(listOf(3.0, 4.5), data.series[1].values)
        assertEquals(Tone.Positive, data.series[1].tone)
        assertEquals(Tone.Neutral, data.series[2].tone)
        assertNull(data.series[0].tone)
        assertEquals("3.5 元", ExtensionViewText.display(3.5, null, "元"))
        assertEquals("¥1.20", ExtensionViewText.display(1.2, "¥1.20", "元"))
        val pie = ExtensionViewText.chart(chart.copy(kind = "pie"))!!
        assertEquals(JChartKind.Pie, pie.kind)
        assertEquals(listOf(1_200_000.0, 800_000.0), pie.series.single().values)
        assertFalse(pie.stacked)
        assertTrue(ExtensionViewText.drawable(chart.copy(kind = "pie")))
        assertNull(ExtensionViewText.chart(chart.copy(kind = "future-chart")))
        assertFalse(ExtensionViewText.drawable(chart.copy(kind = "future-chart")))
        assertFalse(ExtensionViewText.drawable(chart.copy(series = listOf(ExtensionChartSeries("空")))))
        assertEquals(JChartKind.Line, ExtensionViewText.chart(chart.copy(kind = "line"))!!.kind)
        // In Chinese the values and the scale count in ten-thousands; the extension's own words stay as they are (#4149).
        val chinese = ExtensionViewText.chart(chart, java.util.Locale.SIMPLIFIED_CHINESE)!!
        assertEquals(listOf("1.2M", "80万 tokens"), chinese.series[0].displays)
        assertEquals("12亿", chinese.axis(1_200_000_000.0))
        assertEquals("1.2B", data.axis(1_200_000_000.0))
        assertEquals("12億", ExtensionViewText.price(1_200_000_000.0, java.util.Locale.JAPANESE))
        assertEquals("182.35", ExtensionViewText.price(182.35, java.util.Locale.JAPANESE))

        val grid = ExtensionViewItem("stat_grid", stats = listOf(ExtensionStat("今天", "1.2M", "+12%", "caution"), ExtensionStat("费用", "¥36.20", tone = "lilac")))
        assertTrue(ExtensionViewText.drawable(grid))
        assertFalse(ExtensionViewText.drawable(ExtensionViewItem("stat_grid")))
        assertEquals(listOf(Tone.Caution, Tone.Neutral), ExtensionViewText.stats(grid).map { it.tone })

        val segmented = ExtensionViewItem("segmented", name = "range", value = "7d", action = ExtensionViewAction("range"),
            choices = listOf(ExtensionViewChoice("1d", "今天"), ExtensionViewChoice("7d")))
        assertTrue(ExtensionViewText.drawable(segmented))
        assertFalse(ExtensionViewText.drawable(segmented.copy(choices = segmented.choices!!.take(1))))
        assertFalse(ExtensionViewText.drawable(segmented.copy(action = null)))

        assertFalse(ExtensionViewText.drawable(ExtensionViewItem("calendar_heatmap")))
        // A card draws no fields (it opens a page for that).
        assertFalse(ExtensionViewText.drawableOnCard(ExtensionViewItem("field", name = "x", kind = "text")))
        assertTrue(ExtensionViewText.drawableOnCard(grid))
    }

    private val range = ExtensionViewItem("segmented", name = "range", value = "7d", action = ExtensionViewAction("range"),
        choices = listOf(ExtensionViewChoice("1d", "今天"), ExtensionViewChoice("7d", "7 天"), ExtensionViewChoice("30d", "30 天")))

    private fun rangeView(value: String, state: String = "range=$value") = ExtensionView(title = "用量", state = state, sections = listOf(
        ExtensionViewSection(items = listOf(range.copy(value = value), ExtensionViewItem("field", name = "note", kind = "text", value = "备注")))))

    // Choosing another segment sends its action at once with {name: value}; the page shows the choice right away and
    // the extension's new view replaces it. The same choice sends nothing; a failure puts the old choice back.
    @Test fun aSegmentedChoiceGoesAtOnce() = runBlocking {
        val api = FakeUi()
        api.view = rangeView("7d")
        val model = ExtensionViewModel(api, "example-gateway", "usage")
        model.load()
        assertEquals("7d", model.state.value.values["range"])
        api.answer = ExtensionUiResult(view = rangeView("30d"))
        model.choose(range, "30d")
        assertEquals(Triple("range", "range=7d", mapOf("range" to "30d")), api.actions.single())
        assertEquals("range=30d", model.state.value.view?.state)
        assertEquals("30d", model.state.value.values["range"])
        model.choose(range, "30d")
        assertEquals(1, api.actions.size)
        api.fail = HubFailure(HubError.Server, "坏了", status = 502)
        val out = model.choose(range, "1d")
        assertEquals("没有做成：坏了", out.error?.text(res))
        assertEquals("30d", model.state.value.values["range"])
        // Submitting: the page's fields go with it.
        api.fail = null
        api.answer = ExtensionUiResult(view = rangeView("1d"))
        model.choose(range.copy(action = ExtensionViewAction("range", submit = true)), "1d")
        assertEquals(mapOf("range" to "1d", "note" to "备注"), api.actions.last().third)
    }

    // refresh_after (docs/extensions/extensions.md "UI slots" "Refresh"): drawn again with the extension's state once the time is up (30 s at
    // least) while the page runs it; stopped, nothing; run again after the time is up, drawn at once.
    @Test fun refreshAfterDrawsAgainWhileOnScreen() = runTest {
        val api = FakeUi()
        api.view = api.view.copy(refreshAfter = 10)
        val model = ExtensionViewModel(api, "example-gateway", "usage", now = { testScheduler.currentTime })
        model.load()
        val fresh = launch { model.keepFresh() }
        advanceTimeBy(29_000); runCurrent()
        assertEquals(1, api.renders.size)
        advanceTimeBy(1_500); runCurrent()
        assertEquals(2, api.renders.size)
        assertEquals("tab=1", api.renders.last().second)
        advanceTimeBy(30_500); runCurrent()
        assertEquals(3, api.renders.size)
        fresh.cancel()
        advanceTimeBy(120_000); runCurrent()
        assertEquals(3, api.renders.size)
        val back = launch { model.keepFresh() }
        runCurrent()
        assertEquals(4, api.renders.size)
        back.cancel()
    }

    @Test fun aViewWithoutRefreshAfterIsNotDrawnAgain() = runTest {
        val api = FakeUi()
        val model = ExtensionViewModel(api, "example-gateway", "usage", now = { testScheduler.currentTime })
        model.load()
        val fresh = launch { model.keepFresh() }
        advanceTimeBy(48L * 3600 * 1000); runCurrent()
        assertEquals(1, api.renders.size)
        fresh.cancel()
        // A day at most.
        assertEquals(1_000L + 86_400_000L, ExtensionViewText.refreshAt(ExtensionViewState(ExtensionView(refreshAfter = 1_000_000), drawnAt = 1_000L)))
        assertNull(ExtensionViewText.refreshAt(ExtensionViewState(ExtensionView(refreshAfter = 0), drawnAt = 1_000L)))
    }

    // A machine's section for a machine the hub does not know (another hub's) is hidden, not an error.
    @Test fun aMachineTheHubDoesNotKnowHidesItsSection() = runBlocking {
        val api = FakeUi()
        api.fail = ExtensionUiNotFound("没有这台机器")
        val model = ExtensionViewModel(api, "example-gateway", "gpu", ExtensionUiContext(machine = "elsewhere"))
        model.load()
        assertTrue(model.state.value.unsupported)
        assertNull(model.state.value.error?.text(res))
    }
}
