package persona.feature.terminal

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.terminal.*
import java.util.Base64
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class TerminalCriticalPathTest {
    @Test fun resumesFramesRecoversGapsReleasesControlAndBatchesWheel() = runTest {
        val dispatcher = StandardTestDispatcher(testScheduler)
        Dispatchers.setMain(dispatcher)
        try {
            val transport = FakeTerminal()
            val model = TerminalViewModel(SessionKey("machine", "session"), transport, dispatcher)
            val run = launch { model.run() }
            runCurrent()
            assertTrue(transport.subscriptions.isEmpty())
            model.grid(TerminalSize(40, 20)); advanceTimeBy(250); runCurrent()
            assertEquals(TerminalSize(40, 20), transport.subscriptions.last().size)
            transport.events.emit(TerminalControlEvent("machine", "session", "active")); runCurrent()
            fun frame(seq: Int, full: Boolean, text: String, epoch: String = "epoch") = TerminalEvent("machine", "session",
                epoch, seq.toLong(), full, 40, 20, Base64.getEncoder().encodeToString(text.toByteArray()))
            transport.events.emit(frame(1, true, "中A")); runCurrent()
            assertEquals(2, model.state.value.lines[0][0].width)
            transport.events.emit(frame(2, false, "B")); runCurrent()
            transport.connection.value = ConnectionState.Connecting
            advanceTimeBy(250); runCurrent()
            model.type("不得发送"); assertTrue(transport.inputs.isEmpty())
            transport.connection.value = ConnectionState.Online
            advanceTimeBy(250); runCurrent()
            assertEquals("epoch", transport.subscriptions.last().epoch)
            assertEquals(2, transport.subscriptions.last().after)
            transport.events.emit(TerminalControlEvent("machine", "session", "active")); runCurrent()
            transport.events.emit(frame(4, false, "lost")); runCurrent()
            assertNull(transport.subscriptions.last().epoch)
            assertEquals(TerminalControl.Active, model.state.value.control)
            transport.events.emit(frame(10, true, "new", "replacement")); runCurrent()
            transport.events.emit(TerminalControlEvent("machine", "session", "active")); runCurrent()
            val at = TerminalCellPosition(7, 8)
            TerminalWheel(20f, at).move(65f, model::wheel)
            assertEquals(listOf(3), transport.inputs.map { it.scroll }) // one message for the move, not one a line
            assertTrue(transport.inputs.all { it.at == at })
            model.watch(); model.press("enter"); runCurrent()
            assertEquals(1, transport.inputs.size)
            // Watching, keys go through /keys instead of the stream.
            assertEquals(listOf(FakeTerminal.Keys(listOf("enter"), "")), transport.keys)
            transport.events.emit(TerminalControlEvent("machine", "session", "busy")); runCurrent()
            assertEquals(TerminalControl.Busy, model.state.value.control)
            model.takeOver(true); assertTrue(transport.subscriptions.last().takeover)
            transport.events.emit(TerminalControlEvent("machine", "session", "taken")); runCurrent()
            model.type("只看时走 keys"); runCurrent(); assertEquals(1, transport.inputs.size)
            assertEquals(FakeTerminal.Keys(emptyList(), "只看时走 keys"), transport.keys.last())
            run.cancelAndJoin()
            assertEquals(1, transport.unsubscribes)
        } finally { Dispatchers.resetMain() }
    }

    @Test fun resumeSessionBringsBackASessionThatIsNotRunning() = runTest {
        val dispatcher = StandardTestDispatcher(testScheduler)
        Dispatchers.setMain(dispatcher)
        try {
            val transport = FakeTerminal()
            val resumer = FakeResumer()
            val model = TerminalViewModel(SessionKey("machine", "session"), transport, dispatcher, resumer = resumer)
            val failures = mutableListOf<String?>()
            val collecting = launch { model.resumeFailures.collect { failures += it } }
            val run = launch { model.run() }
            runCurrent(); model.grid(TerminalSize(40, 20)); advanceTimeBy(250); runCurrent()
            suspend fun notRunning() { transport.events.emit(TerminalEvent("machine", "session", error = "session not running")); advanceTimeBy(250); runCurrent() }
            notRunning()
            assertEquals(TerminalNote.NoTerminal, model.state.value.note)
            assertTrue(model.state.value.resumable)
            // The hub refused: its words are shown and the note stays.
            resumer.failure = TerminalResumeFailed("no such directory")
            model.resume(); runCurrent()
            assertEquals(listOf<String?>("no such directory"), failures)
            assertFalse(model.state.value.resuming)
            assertEquals(TerminalNote.NoTerminal, model.state.value.note)
            // On its way: one request at a time, then the terminal connects at once instead of after the back-off.
            resumer.failure = null; resumer.gate = CompletableDeferred()
            model.resume(); model.resume(); runCurrent()
            assertTrue(model.state.value.resuming)
            assertEquals(2, resumer.calls)
            val subscriptions = transport.subscriptions.size
            resumer.gate!!.complete(Unit); runCurrent()
            assertFalse(model.state.value.resuming)
            assertNull(model.state.value.note)
            advanceTimeBy(250); runCurrent()
            assertEquals(subscriptions + 1, transport.subscriptions.size)
            assertFalse(model.state.value.resumable)
            // A session a message resumes (Codex): no Resume session.
            resumer.can = false
            notRunning()
            assertEquals(TerminalNote.NoTerminal, model.state.value.note)
            assertFalse(model.state.value.resumable)
            run.cancelAndJoin(); collecting.cancel()
        } finally { Dispatchers.resetMain() }
    }

    private class FakeResumer : TerminalResumer {
        var can = true
        var failure: Exception? = null
        var gate: CompletableDeferred<Unit>? = null
        var calls = 0
        override fun canResume(key: SessionKey) = can
        override suspend fun resume(key: SessionKey) { calls++; gate?.await(); failure?.let { throw it } }
    }

    @Test fun aStoppedAgentCanResumeWhileItsRetainedShellStreams() = runTest {
        val dispatcher = StandardTestDispatcher(testScheduler)
        Dispatchers.setMain(dispatcher)
        try {
            var stopped = true
            var resumed = 0
            val resumer = object : TerminalResumer {
                override fun canResume(key: SessionKey) = true
                override fun isStopped(key: SessionKey) = stopped
                override suspend fun resume(key: SessionKey) { resumed++; stopped = false }
            }
            val transport = FakeTerminal()
            val model = TerminalViewModel(SessionKey("machine", "session"), transport, dispatcher,
                MemoryTerminalPreferences(drive = false), resumer)
            val run = launch { model.run() }
            runCurrent()
            transport.events.emit(TerminalEvent("machine", "session", "shell", 1, true, 40, 20,
                Base64.getEncoder().encodeToString("$ ".toByteArray())))
            advanceTimeBy(250); runCurrent()
            assertTrue(model.state.value.loaded)
            assertNull(model.state.value.note)
            assertTrue(model.state.value.resumable)
            assertEquals(0, resumed)
            model.resume(); runCurrent()
            advanceTimeBy(250); runCurrent()
            assertEquals(1, resumed)
            assertFalse(model.state.value.resumable)
            run.cancelAndJoin()
        } finally { Dispatchers.resetMain() }
    }

    @Test fun compositionCommitsOnceAndModifiersCoverTapLockAndHold() {
        val sent = mutableListOf<String>()
        val marked = mutableListOf<String>()
        val input = TerminalComposition(sent::add, marked::add) {}
        input.compose("zhong"); input.compose("zhongwen")
        assertTrue(sent.isEmpty())
        input.commit("中文"); input.finish()
        assertEquals(listOf("中文"), sent)
        input.compose("ni"); input.delete(1); assertEquals("n", input.marked)
        input.compose("你"); input.finish(); assertEquals(listOf("中文", "你"), sent)
        input.compose("不得发送"); input.clear(); input.finish(); assertEquals(2, sent.size)
        val out = mutableListOf<String>()
        val keyed = TerminalComposition({ out += "text:$it" }, {}, { out += "key:$it" })
        keyed.commit("ls"); keyed.commit("\n")
        assertEquals(listOf("text:ls", "key:enter"), out); out.clear()
        keyed.commit("a\tb"); assertEquals(listOf("text:a", "key:tab", "text:b"), out); out.clear()
        keyed.commit("x\r\ny"); assertEquals(listOf("text:x", "key:enter", "text:y"), out)
        val modifiers = TerminalModifiers()
        val ctrl = TerminalModifiers.Key.CTRL
        modifiers.tap(ctrl); assertEquals("ctrl+c", modifiers.apply("c"))
        modifiers.pressed(); assertEquals(ModifierState.OFF, modifiers[ctrl])
        modifiers.tap(ctrl); modifiers.tap(ctrl, quick = true); modifiers.pressed()
        assertEquals(ModifierState.LOCKED, modifiers[ctrl])
        modifiers.tap(ctrl); modifiers.hold(ctrl); assertEquals("\u0003", TerminalKeyBytes.encode(modifiers.apply("c")))
        modifiers.pressed(); modifiers.release(ctrl); assertEquals(ModifierState.OFF, modifiers[ctrl])
        assertEquals("\u001b[200~一\r二\u001b[201~", TerminalKeyBytes.text("一\n二"))
        val decoded = ServerEvent.decode("""{"type":"terminal_control","machine_id":"machine","session_id":"session","state":"active"}""")
        assertTrue(decoded is TerminalControlEvent)
    }
}

internal class FakeTerminal : TerminalTransport {
    override val connection = MutableStateFlow(ConnectionState.Online)
    override val features = MutableStateFlow(setOf("terminal_stream", "terminal_control", "terminal_scroll", "terminal_click"))
    override val events = MutableSharedFlow<ServerEvent>()
    data class Subscription(val epoch: String?, val after: Int, val size: TerminalSize?, val takeover: Boolean)
    data class Input(val text: String, val scroll: Int, val at: TerminalCellPosition?)
    data class Keys(val keys: List<String>, val text: String)
    val subscriptions = mutableListOf<Subscription>()
    /** The key each subscription named (a session's, or a pane's address). */
    val subscribedKeys = mutableListOf<SessionKey>()
    val inputs = mutableListOf<Input>()
    val keys = mutableListOf<Keys>()
    var unsubscribes = 0
    override fun subscribe(key: SessionKey, epoch: String?, after: Int, size: TerminalSize?, takeover: Boolean): Boolean {
        subscriptions += Subscription(epoch, after, size, takeover); subscribedKeys += key; return true
    }
    override fun unsubscribe(key: SessionKey) { unsubscribes++ }
    override fun input(key: SessionKey, text: String, size: TerminalSize?, scroll: Int, at: TerminalCellPosition?, click: TerminalCellPosition?): Boolean {
        inputs += Input(text, scroll, at); return true
    }
    override suspend fun screen(key: SessionKey): TerminalScreen { screens++; return TerminalScreen("只读屏幕") }
    /** Holds each /keys request until completed; [failKeys] makes them fail. */
    var gate: kotlinx.coroutines.CompletableDeferred<Unit>? = null
    var failKeys = false
    var screens = 0
    override suspend fun keys(key: SessionKey, keys: List<String>, text: String) {
        this.keys += Keys(keys, text)
        gate?.await()
        if (failKeys) throw java.io.IOException("hub unreachable")
    }
}
