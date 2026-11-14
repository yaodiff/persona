#if DEBUG
// i18n-ignore-file: DEBUG-only made-up recording for screenshots
import Foundation
#if os(iOS)
import AVFoundation
import UIKit
#endif

/// Demo and screenshots use made-up text only and never start the microphone; with a hub paired they sync into its Knowledge as usual (UI tests check this step with the mock hub).
enum RecordingSamples {
    /// Once per launch: the page appears again after its recording was deleted, and must not bring it back.
    nonisolated(unsafe) private static var installed = false

    /// `-showRecordingSample second`: the same recording under another id, for a test that deletes one (the hub keeps the first's tombstone).
    static let secondSample = UUID(uuidString: "00000000-0000-0000-0000-000000000004")!

    static func installIfAsked() -> UUID? {
        let asked = UserDefaults.standard.string(forKey: "showRecordingSample")
        guard !installed, UserDefaults.standard.bool(forKey: "showRecordingSample") || asked == "second" else { return nil }
        installed = true
        let id = asked == "second" ? secondSample : UUID(uuidString: "00000000-0000-0000-0000-000000000001")!
        RecordingLibrary.shared.restore(id)
        var entry = RecordingEntry(id: id, createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .watch,
                                   title: "项目讨论", active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 90, sentences: [
            RecordingSentence(start: 0, end: 12, text: "这次讨论确定三个重点：先验证核心流程，再补充测试，最后检查体验。"),
            RecordingSentence(start: 28, end: 40, text: "下周整理一次进展，把未解决的问题列出来。"),
            RecordingSentence(start: 65, end: 78, text: "需要把今天的结论变成待办，避免遗漏。"),
        ])]
        entry.marks = [28]
        try? RecordingLibrary.shared.save(entry)
        return id
    }

    /// The meeting being recorded (`-showMeetingSample recording|paused|interrupted`).
    static let liveMeeting = UUID(uuidString: "00000000-0000-0000-0000-000000000002")!
    /// A finished meeting, transcribed (`-showMeetingSample minutes|pending` opens it).
    static let pastMeeting = UUID(uuidString: "00000000-0000-0000-0000-000000000003")!
    /// A longer finished meeting for the summary card (#6563, `-showMeetingSample overview` opens it): five minutes with
    /// lines in each, so a hub that makes a topic of each minute has five, in two groups.
    static let overviewMeeting = UUID(uuidString: "00000000-0000-0000-0000-000000006563")!

    #if os(iOS)
    /// The lines the `flow` sample hears, one segment of one sentence each, a few seconds apart.
    private static let flowLines = [
        "好，我们开始，今天先看录音入口的进展。",
        "新版入口已经合进主干，开始录音只要一步。",
        "锁屏和灵动岛可以看到时长，也能直接停止。",
        "决定：默认用中文转写，中英混说也要能用。",
        "张三负责周五前补上长录音的真机测试。",
        "还没定的是 Mac 上要不要也支持录音。",
        "下周同一时间再对一次进展，今天就到这里。",
    ]
    nonisolated(unsafe) private static var flowTask: Task<Void, Never>?

    /// `-showMeetingSample flow`: a meeting that records itself with no microphone. The clock and the level move, a line is
    /// heard every few seconds, and Stop, Pause and Bookmark work (`RecordingCapture`'s DEBUG sample paths), so the
    /// simulator can show the whole way from the live sheet through the wrap-up to the minutes (mock hub, `recording_minutes`).
    @MainActor private static func startFlow(_ meeting: RecordingMeeting) {
        RecordingLibrary.shared.restore(liveMeeting)
        var live = RecordingEntry(id: liveMeeting, createdAt: .now, source: .phone, title: "产品周会", active: true)
        live.meeting = meeting
        live.attendance = RecordingAttendance(role: .participant, session: nil, sessionTitle: "Persona", mainPicked: true)
        try? RecordingLibrary.shared.save(live)
        let capture = RecordingCapture.shared
        capture.showSample(liveMeeting, elapsed: 1)
        flowTask?.cancel()
        flowTask = Task { @MainActor in
            var elapsed = 1.0
            var heard = 0
            var lastLine = 0.0
            while !Task.isCancelled, capture.recordingID == liveMeeting {
                try? await Task.sleep(for: .milliseconds(100))
                guard capture.state == .recording else { capture.advanceSample(elapsed: elapsed, level: 0); continue }
                elapsed += 0.1
                // Speech in bursts: a slow swell and a quicker flutter.
                let swell = max(0, sin(elapsed * 0.9)), flutter = 0.5 + 0.5 * sin(elapsed * 9)
                capture.advanceSample(elapsed: elapsed, level: min(1, 0.08 + 0.7 * swell * flutter))
                if elapsed - lastLine >= 6, heard < flowLines.count {
                    lastLine = elapsed
                    let start = max(0, elapsed - 8), line = flowLines[heard], index = heard
                    heard += 1
                    RecordingLibrary.shared.change(liveMeeting) { entry in
                        entry.segments.append(RecordingSegment(index: index, start: start, duration: 6, sentences: [
                            RecordingSentence(start: start, end: start + 5, text: line)]))
                    }
                }
            }
        }
    }
    #endif

    /// `-showMeetingSample overview`: a finished sprint review of five minutes (`overviewMeeting`) whose minutes the hub
    /// writes with a summary card (#6563; the mock hub with `shape: overview`): the first minute holds two points and
    /// three decisions, so its topic folds after four, and every later minute a point or a decision with a figure, an
    /// action item or the open question. The `minutes` sample stays as it was, the four cards' baseline.
    private static func installOverviewMeeting() {
        let start = Date(timeIntervalSince1970: 1_800_090_000)
        let meeting = RecordingMeeting(eventID: "sample-sprint", title: "迭代评审", start: start, end: start.addingTimeInterval(1800),
                                       location: "3 楼会议室", attendees: ["张三", "李四", "王五"])
        RecordingLibrary.shared.restore(overviewMeeting)
        var entry = RecordingEntry(id: overviewMeeting, createdAt: start, source: .phone, title: "迭代评审", active: false,
                                   expectedSegments: 5)
        entry.meeting = meeting
        // Each minute opens on its subject and a colon, which names its topic at a hub that titles a topic by its first
        // line.
        let minutes: [[(Double, String)]] = [
            [(2, "导出与发版：导出保留 4 MB 上限，发版日期取决于缓存修复。"), (14, "导出现在会丢掉超过 4 MB 的条目。"),
             (27, "决定：导出保留 4 MB 上限，跳过条目时给出提示。"), (39, "决定：超限的条目在列表里标灰。"),
             (51, "决定：提示文案控制在 2 行以内。")],
            [(63, "发版日期：20 号发版现在有风险。"), (78, "决定：缓存问题周三前修不好，发版就推迟 1 周。")],
            [(125, "压测：目标从 2 万 QPS 提到 4 万 QPS。"), (140, "决定：压测目标定为 4 万 QPS。"),
             (160, "王五负责周五前分享压测报告。")],
            [(185, "缓存问题：弱网下复现了 1 次。"), (200, "决定：缓存修复优先于新功能。"),
             (215, "张三负责周五前补上提示文案和回归测试。")],
            [(241, "下次评审：下周同一时间再对 1 次进展。"), (250, "还没定的是上限按条目算还是按录音算。"),
             (262, "决定：下次评审前发出版本说明。"), (280, "李四负责周三前确认缓存修复。")],
        ]
        entry.segments = minutes.enumerated().map { index, lines in
            RecordingSegment(index: index, start: Double(index * 60), duration: 60, sentences: lines.map { at, text in
                RecordingSentence(start: at, end: at + 10, text: text)
            })
        }
        try? RecordingLibrary.shared.save(entry)
    }

    /// `-showMeetingSample recording|paused|interrupted`: a meeting 12:34 into its recording, as the recording page and its
    /// Live Activity show it (no microphone); `listening|taking-part|answered`: the same with Persona attending (#2981);
    /// `room-listening|room-taking-part`: the same as a room meeting (#3862) started with the live view (phase 2), whose
    /// sheet is the meeting page;
    /// `room-whiteboard`: `room-taking-part` started with photos, which adds a made-up whiteboard photo as the camera
    /// would (#3862 phase 3); `room-talk-held`: `room-taking-part` with Talk held for 10 s once the meeting call is up
    /// (#3862 phase 4; every room sample feeds canned audio into the recorder's tap for Talk); `room-hub`:
    /// `room-listening` in hub transcription (#3862 phase 5), whose canned audio also goes up the audio socket, so a UI
    /// test checks the uplink against the mock hub's hub mode with no microphone; `room-hub-highlights`: `room-hub`
    /// started with the highlights too, as a hub with both starts it (the Highlights card, the hub's lines on the Full
    /// Transcript page); `minutes` / `pending`: a finished meeting whose page opens (its minutes from the hub, or still
    /// being written); `overview`: a longer finished meeting whose page opens, for the summary card (#6563,
    /// `installOverviewMeeting`); `room-highlights`: `room-taking-part` started with the highlights too (phase 2b),
    /// 24:20 in, with a line in each of fourteen minutes (`highlightsLines`), so the hub's highlights and the transcript
    /// at their minutes have real lines. The recordings are made up.
    static func showMeetingIfAsked() {
        #if os(iOS)
        guard let mode = UserDefaults.standard.string(forKey: "showMeetingSample") else {
            // A run without them: an earlier run's made-up meetings go, so other tests see only their own recordings.
            for id in [liveMeeting, pastMeeting, overviewMeeting] where RecordingLibrary.shared.entry(id) != nil {
                try? RecordingLibrary.shared.remove(id)
            }
            return
        }
        // The summary card's meeting alone, or the others alone: a run syncs only the made-up meetings it shows.
        let others = mode == "overview" ? [liveMeeting, pastMeeting] : [overviewMeeting]
        for id in others where RecordingLibrary.shared.entry(id) != nil { try? RecordingLibrary.shared.remove(id) }
        if mode == "overview" { installOverviewMeeting(); return }
        let start = Date(timeIntervalSince1970: 1_800_003_600)
        let meeting = RecordingMeeting(eventID: "sample-weekly", title: "产品周会", start: start, end: start.addingTimeInterval(3600),
                                       location: "3 楼会议室", attendees: ["张三", "李四", "王五"])
        RecordingLibrary.shared.restore(pastMeeting)
        var past = RecordingEntry(id: pastMeeting, createdAt: start, source: .phone, title: "产品周会", active: false, expectedSegments: 2)
        past.meeting = meeting
        past.segments = [
            RecordingSegment(index: 0, start: 0, duration: 60, sentences: [
                RecordingSentence(start: 2, end: 14, text: "今天先过一下新版录音入口，目标是一步就能开始录。"),
                RecordingSentence(start: 21, end: 38, text: "锁屏和灵动岛要能看到时长，也能直接停止。"),
                RecordingSentence(start: 44, end: 58, text: "决定：默认用中文转写，中英混说也要能用。"),
            ]),
            RecordingSegment(index: 1, start: 60, duration: 60, sentences: [
                RecordingSentence(start: 63, end: 80, text: "张三负责周五前补上长录音的真机测试。"),
                RecordingSentence(start: 86, end: 101, text: "还没定的是 Mac 上要不要也支持录音。"),
                RecordingSentence(start: 105, end: 118, text: "李四负责下周同一时间约大家再对一次进展。"),
            ]),
        ]
        past.marks = [44]
        try? RecordingLibrary.shared.save(past)
        if mode == "minutes" || mode == "pending" { return }
        if mode == "flow" { startFlow(meeting); return }

        RecordingLibrary.shared.restore(liveMeeting)
        let highlighted = mode == "room-highlights"
        let elapsed: Double = highlighted ? highlightsElapsed : 754
        var live = RecordingEntry(id: liveMeeting, createdAt: .now.addingTimeInterval(-elapsed), source: .phone, title: "产品周会", active: true)
        live.meeting = meeting
        live.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [
            RecordingSentence(start: 1, end: 9, text: "好，我们开始，今天先看录音入口的进展。"),
        ])]
        live.marks = [65, 300]
        // `listening` / `taking-part` / `answered`: Persona attends (#2981), listening in, taking part, or taking part with two
        // answers given. The main chat attends, picked for it (no session named, `mainPicked`: never the default, #3862);
        // the made-up meeting's upload tells the hub, as a real one would, and its lines go to the hub's live log, the last
        // one just before the tap (12:34). `room-listening` / `room-taking-part`: the meeting's own room conversation
        // attends instead (#3862), not private; `room-whiteboard` takes part and takes photos; `room-highlights` takes part
        // with the highlights (phase 2b) and its own fourteen lines.
        let hub = mode == "room-hub" || mode == "room-hub-highlights"
        let room = ["room-listening", "room-taking-part", "room-whiteboard", "room-talk-held", "room-highlights"].contains(mode) || hub
        let photos = mode == "room-whiteboard"
        if ["listening", "taking-part", "answered"].contains(mode) || room {
            if room {
                // Started with the live view (#3862 phase 2): the page shows the room's topics once the hub has any (and
                // with the highlights, phase 2b, its highlights). `room-hub`: listening in, transcribed by the hub (phase
                // 5): its uploads ask for `hub`, and once the hub's `200` says so the canned audio (`startTalkAudio`) goes
                // up the audio socket.
                live.attendance = RecordingAttendance.room(mode == "room-listening" || hub ? .listener : .participant, private: false,
                                                           photos: photos, liveView: true,
                                                           highlights: highlighted || mode == "room-hub-highlights",
                                                           transcription: hub ? .hub : nil)
            } else {
                live.attendance = RecordingAttendance(role: mode == "listening" ? .listener : .participant, session: nil,
                                                      sessionTitle: "Persona", mainPicked: true)
            }
            live.segments.append(RecordingSegment(index: 1, start: 720, duration: 30, sentences: [
                RecordingSentence(start: 741, end: 748, text: "上周定的长录音测试谁负责？"),
            ]))
            if highlighted { live.segments = highlightsSegments }
        }
        if photos {
            // An earlier run's photos not sent yet stay behind: this run's whiteboard is the only one.
            try? FileManager.default.removeItem(at: RecordingLibrary.shared.folder(liveMeeting).appending(path: MeetingPhotos.listFile))
        }
        try? RecordingLibrary.shared.save(live)
        RecordingCapture.shared.showSample(liveMeeting, elapsed: elapsed, paused: mode == "paused", interrupted: mode == "interrupted")
        if photos { Task { await addWhiteboard() } }
        // The room meetings' microphone for Talk (#3862 phase 4): canned audio into the recorder's tap. `room-talk-held`:
        // `room-taking-part` with Talk held for a while once the meeting call is up (the held page's screenshot), its
        // first-use note shown again.
        if room {
            MeetingCall.shared.forgetPrivacyNote()
            startTalkAudio()
        }
        if mode == "room-talk-held" { Task { await holdTalk() } }
        if hub { Task { await announceToHub() } }
        if mode == "answered" {
            let first = MeetingAsk.Exchange(recordingID: liveMeeting, at: 612, question: "Mac 上的录音现在谁在跟？", typed: true,
                                            state: .answered("还没有定负责人。上次周会把它列为待定问题，计划下周同一时间再对。"),
                                            sources: ["产品周会 · 2026-11-03 14:00–15:00"])
            let second = MeetingAsk.Exchange(recordingID: liveMeeting, at: 731, question: "上周定的长录音测试谁负责？", typed: false,
                                             state: .answered("上周的周会定了：录音入口只做一步开始，默认用中文转写。长录音的真机测试由张三负责，周五前完成。"),
                                             sources: ["产品周会 · 2026-11-03 14:00–15:00"])
            MeetingAsk.shared.showSample([first, second])
            MeetingSpeaker.shared.showSample(second.id)
        }
        #endif
    }

    /// `room-highlights`: how far into its recording the meeting is (24:20).
    static let highlightsElapsed: Double = 1460

    /// `room-highlights`: a product meeting's lines, each in a minute of its own (starts at least 61 s apart, so a
    /// highlight set at a line's second has that line alone in its minute), from 0:05 to 23:40.
    static let highlightsLines: [RecordingSentence] = [
        RecordingSentence(start: 5, end: 12, text: "好，我们开始，今天先过导出功能、供应商和下季度定价。"),
        RecordingSentence(start: 80, end: 88, text: "导出上限现在是 4 MB，几家客户反馈大文件导不出来。"),
        RecordingSentence(start: 150, end: 159, text: "我建议这个版本先保留上限，下个版本再放开。"),
        RecordingSentence(start: 225, end: 233, text: "同意，这个版本保留 4 MB 的导出上限，发版说明里写清楚。"),
        RecordingSentence(start: 370, end: 379, text: "接下来是供应商，他们的合同下个月到期。"),
        RecordingSentence(start: 460, end: 469, text: "他们最近两次交付都晚了一周左右。"),
        RecordingSentence(start: 545, end: 553, text: "但现在换供应商成本太高，三季度前也来不及迁移。"),
        RecordingSentence(start: 620, end: 629, text: "那就先续约到三季度，下周再核一次成本。"),
        RecordingSentence(start: 830, end: 838, text: "定价方面，销售希望企业版按席位收费。"),
        RecordingSentence(start: 915, end: 923, text: "按用量收费对小团队更友好，两种方案都算一下。"),
        RecordingSentence(start: 1050, end: 1058, text: "张三周五前出一版两种定价方案的测算。"),
        RecordingSentence(start: 1185, end: 1193, text: "还有招聘计划，目前没有人负责。"),
        RecordingSentence(start: 1270, end: 1278, text: "招聘计划谁来负责，下次会上再定。"),
        RecordingSentence(start: 1420, end: 1428, text: "今天先到这里，下周同一时间再对一次进展。"),
    ]

    /// `highlightsLines`, a segment of its own each, as 30-second segments land.
    private static var highlightsSegments: [RecordingSegment] {
        highlightsLines.enumerated().map { n, line in
            RecordingSegment(index: n, start: max(0, line.start - 2), duration: 30, sentences: [line])
        }
    }

    #if os(iOS)
    nonisolated(unsafe) private static var talkAudioTask: Task<Void, Never>?
    /// The made-up words of a Talk span asked without the call (`transcribeTalkSample`).
    static let talkQuestion = "上次评审定的导出上限是多少？"

    /// The room samples' microphone (#3862 phase 4): canned PCM into the recorder's tap (`RecordingCapture.feedSample`),
    /// 100 ms at a time at about real time, so Talk has audio to send with no microphone: 3 s of a voice-like tone, then
    /// 2 s of quiet, again and again (a talk window hears speech, then silence). Nothing reaches the level meter or the
    /// files: only Talk listens, and in hub mode (`room-hub`, `room-hub-highlights`, phase 5) the uplink.
    @MainActor private static func startTalkAudio() {
        talkAudioTask?.cancel()
        guard let format = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16000, channels: 1, interleaved: false) else { return }
        talkAudioTask = Task { @MainActor in
            var tick = 0
            while !Task.isCancelled, RecordingCapture.shared.recordingID == liveMeeting, RecordingCapture.shared.isSample {
                try? await Task.sleep(for: .milliseconds(100))
                let frames = 1600
                guard let buffer = AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(frames)),
                      let channel = buffer.floatChannelData?[0] else { return }
                buffer.frameLength = AVAudioFrameCount(frames)
                let voiced = tick % 50 < 30
                for i in 0..<frames {
                    let t: Double = Double(tick * frames + i) / 16000
                    // A low hum with a slower swell: loud enough to count as speech (`MeetingTalkWindow.speechLevel`).
                    // In steps with their types written out: as one expression Xcode 26.0 gives up type-checking it.
                    let hum: Double = sin(2 * Double.pi * 180 * t)
                    let swell: Double = 0.6 + 0.4 * sin(2 * Double.pi * 3 * t)
                    channel[i] = voiced ? Float(0.12 * hum * swell) : 0
                }
                RecordingCapture.shared.feedSample(buffer)
                tick += 1
            }
        }
    }

    /// `room-talk-held`: once the meeting call is up (at most 30 s; without one the span goes without it) and the page
    /// has shown the Wording under Talk for a moment (at most 10 s more; without it the span stays on the phone), Talk is
    /// held for 10 seconds as a finger would, so the page shows it held, then let go.
    @MainActor private static func holdTalk() async {
        for _ in 0..<150 where !MeetingCall.shared.isLive(liveMeeting) {
            try? await Task.sleep(for: .milliseconds(200))
        }
        for _ in 0..<50 where !MeetingCall.shared.privacyShown(liveMeeting) {
            try? await Task.sleep(for: .milliseconds(200))
        }
        guard MeetingCall.shared.beginTalk(liveMeeting, from: .hold) else { return }
        try? await Task.sleep(for: .seconds(10))
        if MeetingCall.shared.talk?.window == false { MeetingCall.shared.endTalk() }
    }

    /// `room-hub`, `room-hub-highlights`: the meeting goes to the hub at once, ahead of the others, as a real start's does
    /// (`MeetingRecorder.startMeeting`), once the app has its hub (a launch that pairs has none for a moment, and the
    /// sync's first pass then finds nothing to send to): its `200` names the mode in effect, and the uplink follows it.
    @MainActor private static func announceToHub() async {
        for _ in 0..<100 where AppModel.shared.api == nil {
            try? await Task.sleep(for: .milliseconds(200))
        }
        RecordingSync.shared.attendanceChanged(liveMeeting)
    }

    /// Talk without a call in a room sample: there is no segment to transcribe, so the span's words are made up as a
    /// transcription would write them, and the spoken question goes as it would in a real meeting.
    @MainActor static func transcribeTalkSample(_ id: UUID, span: RecordingSpan) {
        guard id == liveMeeting, RecordingCapture.shared.isSample else { return }
        RecordingLibrary.shared.change(id) { entry in
            let index = entry.nextIndex
            entry.segments.append(RecordingSegment(index: index, start: span.start, duration: max(0, span.end - span.start), sentences: [
                RecordingSentence(start: span.start, end: span.end, text: talkQuestion)]))
        }
    }

    /// `room-whiteboard`: once the hub has the meeting with its room (the first upload's `200`, 10 s at most), a made-up
    /// whiteboard goes the way a camera photo goes (`MeetingPhotos.add`), so the real pipeline runs: downscaled, read on
    /// the phone, uploaded, sent into the room, and the room's note.
    private static func addWhiteboard() async {
        for _ in 0..<50 {
            if let entry = RecordingLibrary.shared.entry(liveMeeting), RecordingSync.shared.attendanceSettled(entry) { break }
            try? await Task.sleep(for: .milliseconds(200))
        }
        let at = RecordingCapture.shared.timeline
        guard let jpeg = await Task.detached(priority: .userInitiated, operation: { RecordingSamples.whiteboard() }).value,
              let entry = RecordingLibrary.shared.entry(liveMeeting) else { return }
        MeetingPhotos.shared.add(.camera(jpeg), to: entry, at: at)
    }

    /// A whiteboard as a phone camera would hand it over: 2000 × 1500 px, white, a few lines in a dark marker, a box drawn
    /// around the decision. Drawn off the main actor (`UIGraphicsImageRenderer` is safe there).
    nonisolated private static func whiteboard() -> Data? {
        let size = CGSize(width: 2000, height: 1500)
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        format.opaque = true
        let renderer = UIGraphicsImageRenderer(size: size, format: format)
        let ink = UIColor(red: 0.1, green: 0.14, blue: 0.32, alpha: 1)
        let red = UIColor(red: 0.72, green: 0.12, blue: 0.12, alpha: 1)
        let lines: [(text: String, size: CGFloat, color: UIColor)] = [
            ("Q3 预算", 150, ink),
            ("导出上限 4 MB", 120, red),
            ("回归测试：张三 周五", 120, ink),
            ("Mac 录音：下周再定", 110, ink),
        ]
        return renderer.jpegData(withCompressionQuality: 0.9) { context in
            UIColor.white.setFill()
            context.fill(CGRect(origin: .zero, size: size))
            var y: CGFloat = 170
            for line in lines {
                let attributes: [NSAttributedString.Key: Any] = [.font: UIFont.systemFont(ofSize: line.size, weight: .bold),
                                                                 .foregroundColor: line.color]
                (line.text as NSString).draw(at: CGPoint(x: 180, y: y), withAttributes: attributes)
                y += line.size * 1.9
            }
            // The decision boxed in red, as people mark one on a board.
            let box = UIBezierPath(rect: CGRect(x: 140, y: 170 + 150 * 1.9 - 30, width: 1120, height: 200))
            box.lineWidth = 12
            red.setStroke()
            box.stroke()
        }
    }
    #endif
}

#if os(iOS)
// MARK: - All-day recording

extension RecordingSamples {
    /// The made-up past day (`-openDayRecap YES` opens it): yesterday, closed at its midnight, with a meeting mark, pauses,
    /// a call, an erase on the watch and a silent minute. Its recap is the hub's: the mock writes one from its text
    /// (`POST /mock/recording_day {"recap": …}`).
    static let pastDay = UUID(uuidString: "00000000-0000-0000-0000-0000000000da")!
    /// Today's made-up day, which the watch is recording (`today`) or waits on after a call (`paused`).
    static let todayDay = UUID(uuidString: "00000000-0000-0000-0000-0000000000db")!

    /// The made-up days asked for at launch (`-showDaySample`), nil without them. Read once: the Today section shows for
    /// them as it would for a paired watch.
    static let daySampleMode: String? = UserDefaults.standard.string(forKey: "showDaySample")

    /// The modes `-showDaySample` takes.
    private static let dayModes: Set<String> = ["today", "paused", "recap", "pending", "failed", "consent"]

    /// The first install's work; later calls wait for it.
    private static var dayInstall: Task<Void, Never>?

    /// `-showDaySample today|paused|recap|pending|failed|consent`: yesterday's day in Recent Recordings and today's in the
    /// Today section, both transcribed (no microphone, no speech recognition) and stored as the watch would send them, so
    /// the phone uploads them to the hub as it would a real day; `paused` has the watch waiting for Continue after a call.
    /// `recap|pending|failed` name the hub's answer the screenshot is for (the UI test sets the mock to it). They stand in
    /// for a user who agreed to the consent sheet; `consent` is one who hasn't yet: no day, the watch off, and the Agree
    /// row. Without the argument an earlier run's samples go, so other tests see only their own recordings. Once per
    /// launch.
    static func installDaySampleIfAsked() async {
        if dayInstall == nil {
            let mode = daySampleMode
            dayInstall = Task { await installDays(mode) }
        }
        await dayInstall?.value
    }

    private static func installDays(_ mode: String?) async {
        await removeEarlierDays(rebuilding: mode.map(dayModes.contains) ?? false)
        guard let mode, dayModes.contains(mode) else { return }
        let consent = RecordingDayConsent.shared
        if mode == "consent" {
            consent.forget()
            RecordingDaySummaries.shared.receive(watch: RecordingDayWatchState(recording: nil, day: nil, state: .off, since: nil,
                                                                              next: nil, pending: 0))
            return
        }
        // The watch records only once the user agreed (docs/assistant/day-recap.md「Retention and privacy」).
        consent.agree()
        let library = RecordingLibrary.shared, store = RecordingDayStore.shared
        let zone = TimeZone.current, now = Date.now
        let paused = mode == "paused"
        let past = pastSample(now: now, zone: zone), today = todaySample(now: now, zone: zone, paused: paused)
        for day in [past, today] {
            library.restore(day.id)
            await write(day, to: store)
            library.adoptDay(id: day.id, day: day.day, timeZone: zone.identifier, createdAt: day.startedAt, closed: day.closed)
        }
        RecordingDaySummaries.shared.receive(watch: RecordingDayWatchState(
            recording: today.id, day: today.day, state: paused ? .interrupted : .recording,
            since: today.lastChange, next: today.chunks.count, pending: 0))
    }

    /// An earlier run's made-up days, with everything the store kept of them: built again on every run (another day,
    /// another mode).
    /// `rebuilding`: the made-up days are built again now, so an earlier run's erases over a stretch of time go too (every
    /// launch of a DEBUG build comes here; without a sample it leaves them, a real erase kept for a day still to come).
    private static func removeEarlierDays(rebuilding: Bool) async {
        let library = RecordingLibrary.shared, store = RecordingDayStore.shared
        for id in [pastDay, todayDay] {
            let stored = await store.record(id) != nil
            guard stored || library.entry(id) != nil else { continue }
            // Removed here only: a delete on the hub would tombstone the id there, and the new sample couldn't go up. (The
            // day's upload also asks the watch to delete its copy, which it doesn't have: it confirms, and that is all.)
            try? library.remove(id)
            RecordingDaySync.shared.removedHere(id)
            // Read while deleted: the store lets go of the day it holds in memory before the new one is written.
            _ = await store.record(id)
        }
        // An earlier run's erases (Erase Last 10 Minutes) would otherwise reach the same hours of the day built again.
        await store.forgetSampleRequests([pastDay, todayDay], ranges: rebuilding)
    }

    /// A made-up day: its chunks and control events as the watch would send them, and the text the phone would have
    /// transcribed.
    private nonisolated struct SampleDay: Sendable {
        nonisolated struct Chunk: Sendable {
            var header: RecordingDayHeader
            var sentences: [RecordingSentence]
            var state: RecordingDayChunk.State
        }

        var id: UUID
        var day: String
        var zone: TimeZone
        var startedAt: Date
        var chunks: [Chunk] = []
        var events: [RecordingDayEvent] = []

        var closed: Bool { events.contains { $0.type == .closed } }
        /// When the watch's state last changed: the last `started` or `paused`.
        var lastChange: Date {
            events.last { $0.type == .started || $0.type == .paused }?.at ?? startedAt
        }

        /// The seq the next chunk gets: every seq below it was sent, or erased before it was.
        var nextSeq: Int { chunks.count + reservedSeqs }

        /// A chunk of `seconds` from `at`, its lines spread over it (`skipped`: quieter than speech).
        mutating func chunk(_ at: Date, seconds: Double = 60, level: Double = -32, lines: [String] = [], skipped: Bool = false) {
            let header = RecordingDayHeader(recording: id, recordingStartedAt: startedAt, tz: zone.identifier, day: day,
                                            seq: nextSeq, startedAt: at, duration: seconds, level: level)
            chunks.append(Chunk(header: header, sentences: RecordingSamples.sentences(lines, seconds: seconds),
                                state: skipped ? .skipped : .transcribed))
        }

        /// A seq the watch allocated and erased before sending it: no chunk ever comes for it.
        mutating func erasedChunk() -> Int {
            let seq = nextSeq
            reservedSeqs += 1
            return seq
        }

        mutating func event(_ make: (Int) -> RecordingDayEvent) {
            events.append(make(events.count))
        }

        var envelopes: [RecordingDayEventEnvelope] {
            events.map { RecordingDayEventEnvelope(recording: id, recordingStartedAt: startedAt, tz: zone.identifier, day: day, event: $0) }
        }

        /// Seqs handed out without a chunk (`erasedChunk`); a chunk's seq counts them.
        private var reservedSeqs = 0

        init(id: UUID, day: String, zone: TimeZone, startedAt: Date) {
            self.id = id
            self.day = day
            self.zone = zone
            self.startedAt = startedAt
        }
    }

    /// Stores the chunks and events as the receiver does, then gives the chunks their text as the transcriber would.
    /// The store's receiver calls block on its queue, so they run off the main actor.
    private static func write(_ day: SampleDay, to store: RecordingDayStore) async {
        await Task.detached(priority: .userInitiated) {
            // Never decoded: every chunk is transcribed already. The store needs some audio for a chunk that has it.
            let audio = Data(repeating: 0, count: 64)
            for chunk in day.chunks { _ = try? store.store(header: chunk.header, audio: audio) }
            for envelope in day.envelopes { _ = try? store.store(event: envelope) }
        }.value
        // The text and its hub positions in one write, as the transcriber saves them (`RecordingDayUpload.place`).
        await store.update(day.id) { record in
            for chunk in day.chunks {
                record.chunks[chunk.header.seq]?.state = chunk.state
                record.chunks[chunk.header.seq]?.attempts = 0
                record.chunks[chunk.header.seq]?.sentences = chunk.state == .transcribed ? chunk.sentences : nil
            }
            RecordingDayUpload.place(&record)
        }
    }

    /// Yesterday, 09:12 to 19:07, closed at its midnight: a design review the watch marked as a meeting, a call, ten
    /// minutes erased on the watch, lunch, the afternoon, the evening, a silent minute, then Stop for Today.
    private static func pastSample(now: Date, zone: TimeZone) -> SampleDay {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        let yesterday = calendar.date(byAdding: .day, value: -1, to: now) ?? now.addingTimeInterval(-86_400)
        let midnight = calendar.startOfDay(for: yesterday)
        func at(_ hour: Int, _ minute: Int) -> Date {
            calendar.date(bySettingHour: hour, minute: minute, second: 0, of: midnight) ?? midnight
        }
        let reason = RecordingDayContract.Reason.self
        var day = SampleDay(id: pastDay, day: RecordingDayCalendar.day(of: yesterday, in: zone), zone: zone, startedAt: at(9, 12))
        day.event { .started($0, at: at(9, 12)) }
        day.chunk(at(9, 12), lines: ["早上先把导出功能的发布清单过一遍。", "超过 4 MB 的条目导出时会被丢掉，这个要先修。"])
        day.event { .paused($0, at: at(9, 13), reason: reason.paused) }
        day.event { .started($0, at: at(9, 41)) }
        day.event { .mark($0, at: at(9, 41), label: RecordingDayEvent.Label.meeting) }
        day.chunk(at(9, 41), lines: ["设计评审会议开始，先看新的导出流程。", "大家同意把进度条放到列表顶部。"])
        day.chunk(at(9, 42), lines: ["Bo 负责明天把上传失败的测试修好。"])
        day.event { .paused($0, at: at(9, 43), reason: reason.interrupted) }
        day.event { .started($0, at: at(10, 20)) }
        day.chunk(at(10, 20), lines: ["刚才接了个电话，继续说导出的事。"])
        day.event { .paused($0, at: at(10, 21), reason: reason.paused) }
        // Erase the Last 10 Minutes on the watch, before the chunk it was writing reached the phone.
        day.event { .started($0, at: at(11, 5)) }
        let erased = day.erasedChunk()
        day.event { .erase($0, id: UUID(), from: at(10, 56), to: at(11, 6), seqs: [erased]) }
        day.event { .paused($0, at: at(11, 6), reason: reason.paused) }
        day.event { .started($0, at: at(12, 30)) }
        day.chunk(at(12, 30), lines: ["中午和平台组一起吃饭，聊了上传接口的限流。", "他们建议把重试间隔调长一点。"])
        day.event { .paused($0, at: at(12, 31), reason: reason.paused) }
        day.event { .started($0, at: at(15, 20)) }
        day.chunk(at(15, 20), lines: ["下午整理周报，记得周五前发出去。"])
        day.event { .paused($0, at: at(15, 21), reason: reason.paused) }
        day.event { .started($0, at: at(19, 5)) }
        day.chunk(at(19, 5), lines: ["晚上回顾一下：导出修复和设计评审都有进展。"])
        day.chunk(at(19, 6), level: -62, skipped: true)
        day.event { .paused($0, at: at(19, 7), reason: reason.paused) }
        let seqCount = day.nextSeq
        day.event { .closed($0, at: RecordingDayCalendar.nextMidnight(after: yesterday, in: zone), seqCount: seqCount) }
        return day
    }

    /// Today, from two and a half hours ago (or just after midnight) to two minutes ago: a pause, a call, then the last
    /// minutes, which "Erase Last 10 Minutes" reaches. `paused`: a call took the microphone after them.
    private static func todaySample(now: Date, zone: TimeZone, paused: Bool) -> SampleDay {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        let start = max(calendar.startOfDay(for: now).addingTimeInterval(60), now.addingTimeInterval(-150 * 60))
        // Every chunk ends two minutes before now at the latest; just after midnight the day is shorter, and so are they.
        let room = max(10, now.timeIntervalSince(start) - 120)
        let seconds = min(60, room / 10)
        func at(_ share: Double) -> Date { start.addingTimeInterval(share * (room - seconds)) }
        let reason = RecordingDayContract.Reason.self
        var day = SampleDay(id: todayDay, day: RecordingDayCalendar.day(of: now, in: zone), zone: zone, startedAt: start)
        day.event { .started($0, at: start) }
        day.chunk(start, seconds: seconds, lines: ["今天先把全天录音的手机端过一遍。", "Today 一栏要能看到录了多久、转写到哪。"])
        day.event { .paused($0, at: start.addingTimeInterval(seconds), reason: reason.paused) }
        day.event { .started($0, at: at(0.2)) }
        day.chunk(at(0.2), seconds: seconds, lines: ["下午三点和设计组对一下回顾页的布局。"])
        day.event { .paused($0, at: at(0.2).addingTimeInterval(seconds), reason: reason.interrupted) }
        day.event { .started($0, at: at(0.45)) }
        day.chunk(at(0.45), seconds: seconds, lines: ["接完电话了，继续看同步的状态。"])
        day.chunk(at(0.45).addingTimeInterval(seconds), seconds: seconds, lines: ["记得明天把截图发到群里。"])
        day.event { .paused($0, at: at(0.45).addingTimeInterval(2 * seconds), reason: reason.paused) }
        day.event { .started($0, at: at(1)) }
        day.chunk(at(1), seconds: seconds, lines: ["这段话说完就删掉，试一下删掉最近 10 分钟。"])
        if paused { day.event { .paused($0, at: at(1).addingTimeInterval(seconds), reason: reason.interrupted) } }
        return day
    }

    /// Lines spread evenly over a chunk, each a moment shorter than its share.
    private nonisolated static func sentences(_ lines: [String], seconds: Double) -> [RecordingSentence] {
        guard !lines.isEmpty else { return [] }
        let share = seconds / Double(lines.count)
        return lines.enumerated().map { i, text in
            RecordingSentence(start: Double(i) * share, end: Double(i) * share + share * 0.9, text: text)
        }
    }
}
#endif
#endif
