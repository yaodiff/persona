package persona.core.domain.engines

import persona.core.domain.zhAny

import persona.core.domain.zh
import persona.core.domain.ja
import persona.core.contracts.DetectedEngine
import persona.core.contracts.Machine
import org.junit.Assert.*
import org.junit.Test

class EnginesTest {
    private fun machine(
        engine: String = "herdr",
        engines: List<DetectedEngine> = emptyList(),
        online: Boolean = true,
        kind: String = "ssh",
        id: String = "m1",
    ) = Machine(id = id, kind = kind, online = online, engine = engine, engines = engines)

    private val herdr = DetectedEngine("herdr", "herdr", available = true, version = "0.9.3")
    private val core = DetectedEngine("herdr-core", "Herdr App", available = false)
    private val orca = DetectedEngine("orca", "Orca", available = false, note = "打开 Orca")

    @Test fun pairingAsksAboutTheLocalMachineElseTheFirstAndOnlyWhenEnginesAreReported() {
        val a = machine(id = "a")
        val local = machine(id = "b", kind = "local", engines = listOf(herdr))
        assertEquals("b", (Engines.pairingMachine(listOf(a, local))?.id).zhAny())
        assertEquals("a", (Engines.pairingMachine(listOf(a, machine(id = "c")))?.id).zhAny())
        assertNull(Engines.pairingMachine(emptyList()))
        assertEquals("b", (Engines.pairingStep(listOf(a, local))?.id).zhAny())
        // An older hub reports no engines: no step that could not be saved.
        assertNull(Engines.pairingStep(listOf(a, local.copy(engines = emptyList()))))
        assertNull(Engines.pairingStep(emptyList()))
    }

    @Test fun pickerStartsOnTheCurrentEngineHerdrByDefault() {
        // Never chosen: herdr, even when only another engine is detected.
        assertEquals("herdr", (Engines.initialChoice(machine(engines = listOf(herdr.copy(available = false), core.copy(available = true), orca)))).zhAny())
        assertEquals("herdr", (Engines.initialChoice(machine(engine = ""))).zhAny())
        assertEquals("orca", (Engines.initialChoice(machine(engine = "orca", engines = listOf(herdr, core.copy(available = true), orca)))).zhAny())
        assertEquals("herdr-core", (Engines.initialChoice(machine(engine = "herdr-core", engines = listOf(herdr.copy(available = false))))).zhAny())
    }

    @Test fun optionsAreTheHubsListElseTheKnownEngines() {
        val future = DetectedEngine("future", "Future", available = true)
        assertEquals(listOf("herdr", "future"), (Engines.options(machine(engines = listOf(herdr, future))).map { it.id }).zhAny())
        val fallback = Engines.options(machine())
        assertEquals(listOf("herdr", "herdr-core", "orca", "tern", "rex", "paseo", "cindy", "t3code"), (fallback.map { it.id }).zhAny())
        assertEquals(listOf("herdr", "Herdr App", "Orca", "Tern", "Rex", "Paseo", "Cindy", "T3 Code"), (fallback.map { it.name }).zhAny())
        assertTrue(fallback.none { it.available })
    }

    @Test fun unknownEnginesOnlyWhenAvailable() {
        val m = machine(engines = listOf(DetectedEngine("future", "Future", available = true), DetectedEngine("gone", "Gone")))
        assertTrue(Engines.canSelect("future", m))
        assertFalse(Engines.canSelect("gone", m))
        assertFalse(Engines.canSelect("nothing", m))
        // Known engines stay selectable offline and without detection (applied on connect).
        assertTrue(Engines.canSelect("orca", machine(online = false)))
        assertTrue(Engines.canSelect("paseo", machine(online = false)))
        assertTrue(Engines.canSelect("tern", machine(online = false)))
        assertTrue(Engines.canSelect("rex", machine(online = false)))
        assertTrue(Engines.canSelect("cindy", machine(online = false)))
        assertEquals("Paseo 里的 Agent", (Engines.detail("paseo")).zhAny())
        assertEquals("Tern 里的终端", (Engines.detail("tern")).zhAny())
        assertEquals("Rex 里的终端", (Engines.detail("rex")).zhAny())
        assertEquals("Cindy 里的 Agent", (Engines.detail("cindy")).zhAny())
        assertTrue(Engines.canSelect("t3code", machine(online = false)))
        assertEquals("T3 Code 里的 Agent", (Engines.detail("t3code")).zhAny())
    }

    /** A closed Orca is not "Not detected"; an incompatible one shows its version (its note is shown under it, #17037). */
    @Test fun detectionTellsAClosedOrcaFromAMissingOne() {
        fun on(o: DetectedEngine) = machine(engines = listOf(o))
        assertEquals("已安装，没在运行", (Engines.detection("orca", on(orca.copy(state = "stopped")))).zhAny())
        assertEquals("已安装，没在运行，版本 1.4.277", (Engines.detection("orca", on(orca.copy(state = "stopped", version = "1.4.277")))).zhAny())
        assertEquals("不兼容，版本 1.4.100", (Engines.detection("orca", on(orca.copy(state = "incompatible", version = "1.4.100")))).zhAny())
        assertEquals("没检测到", (Engines.detection("orca", on(orca.copy(state = "missing")))).zhAny())
        assertEquals("没检测到", (Engines.detection("orca", on(orca.copy(state = "from-the-future")))).zhAny())
        assertEquals("检测到可用，版本 1.4.277",
            (Engines.detection("orca", on(orca.copy(available = true, state = "running", version = "1.4.277")))).zhAny())
        assertEquals(Engines.State.Unknown, Engines.state(orca.copy(state = "sleeping")))
        assertEquals(Engines.State.Unknown, Engines.state(null))
        assertEquals(Engines.State.Stopped, Engines.state(orca.copy(state = "stopped")))
    }

    /** One rule for "Orca isn't running": online, Orca in the engine set, and its node says installed and stopped (#17037). */
    @Test fun orcaIsStoppedOnlyWhereItIsEnabledAndSaidSo() {
        val stopped = orca.copy(note = null, state = "stopped")
        val m = Machine(id = "m1", online = true, engine = "herdr", engineHosts = listOf("herdr", "orca"), engineDefault = "herdr",
            engines = listOf(herdr, stopped))
        assertTrue("in the set, not the default", Engines.orcaStopped(m))
        assertTrue(Engines.orcaStopped(m.copy(engine = "orca", engineHosts = listOf("orca"), engineDefault = "orca")))
        assertFalse("not enabled", Engines.orcaStopped(m.copy(engineHosts = listOf("herdr"))))
        assertFalse(Engines.orcaStopped(m.copy(online = false)))
        assertFalse(Engines.orcaStopped(m.copy(engines = listOf(herdr, stopped.copy(available = true, state = "running")))))
        assertFalse(Engines.orcaStopped(m.copy(engines = listOf(herdr, stopped.copy(state = "incompatible", note = "update")))))
        assertFalse("an older node", Engines.orcaStopped(m.copy(engines = listOf(herdr, stopped.copy(state = null)))))
        assertTrue("an older hub: its single engine", Engines.orcaStopped(m.copy(engine = "orca", engineHosts = null)))
        assertEquals(listOf("herdr", "herdr-core"), Engines.hosts(m.copy(engine = "herdr-both", engineHosts = null)))
        assertEquals(emptyList<String>(), Engines.hosts(m.copy(engineHosts = emptyList())))
    }

    @Test fun detectionNamesAndNotes() {
        val m = machine(engines = listOf(herdr, core))
        assertEquals("检测到可用，版本 0.9.3", (Engines.detection("herdr", m)).zhAny())
        assertEquals("没检测到", (Engines.detection("herdr-core", m)).zhAny())
        assertEquals("暂无检测信息", (Engines.detection("orca", m)).zhAny())
        assertEquals("离线，暂无检测信息", (Engines.detection("herdr", machine(online = false, engines = listOf(herdr)))).zhAny())
        assertEquals("Herdr App", (Engines.currentName(machine(engine = "herdr-core"))).zhAny())
        assertEquals("Mine", (Engines.currentName(machine(engine = "x", engines = listOf(DetectedEngine("x", "Mine"))))).zhAny())
        assertEquals("x", (Engines.currentName(machine(engine = "x"))).zhAny())
        assertEquals("桌面 App 里开的终端", (Engines.detail("herdr-core")?.zh()).zhAny())
        assertNull(Engines.detail("x"))
        assertTrue(Engines.footer(machine(online = false)).zh().startsWith("机器离线，选择会在连接后生效"))
        assertTrue(Engines.footer(machine()).zh().startsWith("只显示所选引擎里的会话"))
        assertEquals("已切换引擎", (Engines.saved(machine()).zh()).zhAny())
        assertEquals("已保存，连上后生效", (Engines.saved(machine(online = false)).zh()).zhAny())
    }

    // No terminal engine (#3302): the empty host set, which the hub says in `engine_hosts` while `engine` still says herdr.
    private fun withHosts(hosts: List<String>?, online: Boolean = true, engines: List<DetectedEngine> = listOf(herdr)) =
        machine(online = online, engines = engines).copy(engineHosts = hosts)

    /** Screens that judge by herdr read this: the empty set is none, everything else is `engine` as reported (a blank one stays blank). */
    @Test fun judgedFollowsTheChoiceAndLeavesOlderHubsAsTheyWere() {
        assertEquals(Engines.NONE, Engines.judged(withHosts(emptyList()).copy(engine = "herdr")))
        assertEquals("herdr", Engines.judged(withHosts(listOf("herdr")).copy(engine = "herdr")))
        assertEquals("herdr", Engines.judged(withHosts(null).copy(engine = "herdr")))
        assertEquals("orca", Engines.judged(withHosts(listOf("orca")).copy(engine = "orca")))
        assertEquals("", Engines.judged(withHosts(null).copy(engine = "")))
    }

    @Test fun noTerminalEngineIsReadFromTheEmptyHostSetOnly() {
        assertTrue(Engines.isNone(withHosts(emptyList())))
        // Not chosen, not detected, offline and an older hub are not "none": only the empty set is.
        assertFalse(Engines.isNone(withHosts(listOf("herdr"))))
        assertFalse(Engines.isNone(withHosts(null)))
        assertFalse(Engines.isNone(withHosts(null, online = false, engines = emptyList())))
        assertFalse(Engines.isNone(withHosts(listOf("orca"), engines = listOf(herdr.copy(available = false)))))
        assertEquals(Engines.NONE, Engines.current(withHosts(emptyList())))
        assertEquals(Engines.NONE, Engines.initialChoice(withHosts(emptyList(), online = false)))
        assertEquals("herdr", Engines.current(withHosts(listOf("herdr"))))
        assertEquals("herdr", Engines.current(withHosts(null)))
    }

    @Test fun noTerminalEngineIsListedLastAndOnlyForAHubWithHostSets() {
        assertEquals(listOf("herdr", "herdr-core", "none"), Engines.options(withHosts(listOf("herdr"), engines = listOf(herdr, core))).map { it.id })
        assertEquals(listOf("herdr", "herdr-core", "orca", "tern", "rex", "paseo", "cindy", "t3code", "none"), Engines.options(withHosts(listOf("herdr"), engines = emptyList())).map { it.id })
        assertEquals(listOf("herdr"), Engines.options(withHosts(null)).map { it.id })
        assertFalse(Engines.options(withHosts(null, engines = emptyList())).any { it.id == Engines.NONE })
        assertTrue(Engines.options(withHosts(emptyList())).last().available)
        assertTrue(Engines.canSelect(Engines.NONE, withHosts(listOf("herdr"))))
        // Needs no detection and works offline; an older hub would refuse it.
        assertTrue(Engines.canSelect(Engines.NONE, withHosts(emptyList(), online = false, engines = emptyList())))
        assertFalse(Engines.canSelect(Engines.NONE, withHosts(null)))
        assertFalse("none is no hub engine id", Engines.known.any { it.id == Engines.NONE })
    }

    @Test fun noTerminalEngineHasItsOwnWordsAndNothingToDetect() {
        val none = withHosts(emptyList())
        assertEquals("没有终端引擎", Engines.currentLabel(none).zh())
        assertEquals("没有终端引擎", Engines.label(Engines.NONE, withHosts(listOf("herdr"))).zh())
        assertEquals("No terminal engine", Engines.currentName(none))
        // An engine's name is still the hub's, then the known one.
        assertEquals("herdr", Engines.currentLabel(withHosts(listOf("herdr"))).zh())
        assertEquals("只显示桌面 App 的会话", Engines.detail(Engines.NONE)?.zh())
        assertFalse(Engines.showsDetection(Engines.NONE))
        assertTrue(Engines.showsDetection("herdr"))
        assertTrue(Engines.footer(none).zh().startsWith("显示桌面 App 的会话，不能从手机新建或继续会话"))
        assertTrue(Engines.footer(withHosts(emptyList(), online = false)).zh().startsWith("机器离线，选择会在连接后生效。显示桌面 App 的会话"))
        // The note follows what is picked, not only what is saved, and goes back when herdr is picked again.
        val herdrNow = withHosts(listOf("herdr"))
        assertTrue(Engines.footer(herdrNow, Engines.NONE).zh().startsWith("显示桌面 App 的会话"))
        assertTrue(Engines.footer(none, "herdr").zh().startsWith("只显示所选引擎里的会话"))
        assertEquals("已保存，连上后生效", Engines.saved(withHosts(emptyList(), online = false)).zh())
    }

    // #17052: several engines set up on the web, which the hub reports with `engine` its default.
    private val orcaOn = DetectedEngine("orca", "Orca", available = true)
    private val paseo = DetectedEngine("paseo", "Paseo", available = true)
    private fun set(hosts: List<String>, default: String, engine: String = default, online: Boolean = true) =
        machine(engine = engine, online = online, engines = listOf(herdr, core, orcaOn, paseo)).copy(engineHosts = hosts, engineDefault = default)

    @Test fun aSetIsSeveralEnginesNoOptionStandsFor() {
        assertTrue(Engines.isSet(set(listOf("herdr", "orca", "paseo"), "herdr")))
        assertTrue(Engines.isSet(set(listOf("herdr", "orca"), "orca")))
        // herdr with Herdr App by herdr is the herdr-both option; by Herdr App it is not.
        assertFalse(Engines.isSet(set(listOf("herdr", "herdr-core"), "herdr", engine = "herdr-both")))
        assertTrue(Engines.isSet(set(listOf("herdr", "herdr-core"), "herdr-core", engine = "herdr-both")))
        // One engine, none, and an older hub without sets are not.
        assertFalse(Engines.isSet(set(listOf("orca"), "orca")))
        assertFalse(Engines.isSet(withHosts(emptyList())))
        assertFalse(Engines.isSet(withHosts(null)))
    }

    @Test fun aSetShowsItsEnginesAndDefaultAndStartsOnTheDefault() {
        val web = set(listOf("herdr", "orca", "paseo"), "orca")
        assertEquals("orca", Engines.current(web))
        assertEquals("orca", Engines.initialChoice(web))
        assertEquals("herdr-core", Engines.current(set(listOf("herdr", "herdr-core"), "herdr-core", engine = "herdr-both")))
        assertEquals("herdr + Orca + Paseo", Engines.summary(web).zh())
        assertEquals("Orca", Engines.summary(set(listOf("orca"), "orca")).zh())
        assertEquals("没有终端引擎", Engines.summary(withHosts(emptyList())).zh())
        assertEquals("这台电脑同时使用 herdr + Orca + Paseo，默认是 Orca。要更改使用哪些引擎，请在网页版 Persona 里打开这台电脑。", Engines.setNotice(web)?.zh())
        assertTrue(Engines.setNotice(web)!!.ja().startsWith("このコンピューターはherdr + Orca + Paseoを併用していて、既定はOrcaです。"))
        assertTrue(Engines.setNotice(web)!!.english().startsWith("This computer uses herdr + Orca + Paseo together, with Orca as the default."))
        assertNull(Engines.setNotice(set(listOf("orca"), "orca")))
    }

    @Test fun aPickInsideTheSetOnlyMovesTheDefault() {
        val web = set(listOf("herdr", "orca", "paseo"), "orca")
        assertEquals(Engines.Save.Default(listOf("herdr", "orca", "paseo"), "herdr"), Engines.saveOf("herdr", web))
        assertEquals(Engines.Save.Engine("herdr-core"), Engines.saveOf("herdr-core", web))
        assertEquals(Engines.Save.Engine(Engines.NONE), Engines.saveOf(Engines.NONE, web))
        // herdr-both inside a set holding both is a new default, herdr.
        val withCore = set(listOf("herdr", "herdr-core", "orca"), "orca")
        assertEquals(Engines.Save.Default(listOf("herdr", "herdr-core", "orca"), "herdr"), Engines.saveOf("herdr-both", withCore))
        assertEquals(Engines.Save.Engine("herdr-both"), Engines.saveOf("herdr-both", web))
        // One engine is replaced as before.
        assertEquals(Engines.Save.Engine("herdr"), Engines.saveOf("herdr", set(listOf("orca"), "orca")))
        // The note says which: a new default, or that the others go.
        assertEquals("显示这台电脑所有引擎里的会话。从手机新建的会话在 herdr 里打开。", Engines.footer(web, "herdr").zh())
        assertEquals("只使用 Herdr App：其他引擎会关闭，只显示它里面的会话。", Engines.footer(web, "herdr-core").zh())
        assertTrue(Engines.footer(set(listOf("herdr", "orca"), "orca", online = false), "orca").zh().startsWith("机器离线，选择会在连接后生效。从手机新建的会话将在 Orca"))
        assertTrue(Engines.footer(set(listOf("herdr", "orca"), "orca", online = false), "paseo").zh().startsWith("机器离线，选择会在连接后生效。只使用 Paseo"))
        assertTrue(Engines.footer(web, Engines.NONE).zh().startsWith("显示桌面 App 的会话"))
        assertEquals("已更改默认引擎", Engines.saved(web, Engines.Save.Default(listOf("herdr", "orca", "paseo"), "herdr")).zh())
        assertEquals("已切换引擎", Engines.saved(web, Engines.Save.Engine("herdr-core")).zh())
        assertEquals("已保存，连上后生效", Engines.saved(web.copy(online = false), Engines.Save.Default(listOf("herdr"), "herdr")).zh())
    }

    @Test fun onlyAChangeCanBeSavedOutsidePairing() {
        val web = set(listOf("herdr", "orca", "paseo"), "herdr")
        assertFalse(Engines.canSave("herdr", web, pairing = false))
        assertTrue(Engines.canSave("orca", web, pairing = false))
        assertTrue(Engines.canSave("herdr", web, pairing = true))
        assertFalse(Engines.canSave("orca", machine(engine = "orca"), pairing = false))
        assertTrue(Engines.canSave("herdr", machine(engine = "orca"), pairing = false))
        assertFalse(Engines.canSave(Engines.NONE, withHosts(emptyList()), pairing = false))
        assertTrue(Engines.canSave(Engines.NONE, withHosts(listOf("herdr")), pairing = false))
        // What can't be chosen can't be saved either.
        assertFalse(Engines.canSave("gone", machine(), pairing = true))
    }
}
