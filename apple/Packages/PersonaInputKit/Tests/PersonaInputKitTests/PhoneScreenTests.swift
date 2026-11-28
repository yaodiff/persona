import XCTest
@testable import PersonaInputKit

final class PhoneScreenTests: XCTestCase {
    func testAPhonePictureGetsTheNarrowest2xWidthAndItsSteps() {
        // A 402 × 760 picture (an iPhone between the live screen's bars): ratio 1.89, 540 × 1021.
        let fit = PhoneScreenFit(viewWidth: 402, viewHeight: 760)
        XCTAssertEqual(fit.baseWidth, 540)
        XCTAssertEqual(fit.size(width: 540), PhoneScreenSize(width: 540, height: 1021))
        XCTAssertEqual(fit.widths.first, 540)
        XCTAssertEqual(fit.widths.last, 1080)
        XCTAssertEqual(fit.widths.count, 55)
    }

    func testAShortPictureStartsWhereItIsTallEnoughFor2x() {
        // 1.25: 540 wide is 675 tall, under 800; the first width at least 800 tall is 640.
        let fit = PhoneScreenFit(ratio: 1.25)
        XCTAssertEqual(fit.baseWidth, 640)
        XCTAssertGreaterThanOrEqual(fit.size(width: fit.baseWidth).height, PhoneScreenFit.minHeight)
    }

    func testTheRatioIsKeptInRange() {
        XCTAssertEqual(PhoneScreenFit(ratio: 0.5).ratio, 1)
        XCTAssertEqual(PhoneScreenFit(ratio: 9).ratio, 2.2)
        XCTAssertEqual(PhoneScreenFit(viewWidth: 0, viewHeight: 100).ratio, 2)
        XCTAssertEqual(PhoneScreenFit(ratio: .nan).ratio, 2)
        let tallest = PhoneScreenFit(ratio: 2.2).size(width: PhoneScreenFit.maxWidth)
        XCTAssertLessThanOrEqual(tallest.height * 2, PhoneScreenFit.maxPixels.height)
    }

    func testAWindowThatFitsStaysAtItsWidth() {
        let fit = PhoneScreenFit(ratio: 1.9)
        let area = PhoneScreenSize(width: 540, height: 990)
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 541, height: 990), in: area, at: 540), 540)
    }

    func testAWindowWithAMinimumSizeGetsTheNarrowestWidthThatHoldsIt() {
        let fit = PhoneScreenFit(ratio: 1.9)
        let area = PhoneScreenSize(width: 540, height: 990)
        // 580 wide needs 580 exactly (no float overshoot to 590).
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 580, height: 990), in: area, at: 540), 580)
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 740, height: 600), in: area, at: 540), 740)
        // Too tall too: the larger of the two ratios.
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 560, height: 1188), in: area, at: 540), 650)
        // Wider than the widest: the widest.
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 2400, height: 900), in: area, at: 540), 1080)
    }

    func testTheDisplayGoesAgainstTheMainDisplayOnItsFirstFreeSide() {
        let main = PhoneScreenRect(x: 0, y: 0, width: 1920, height: 1080)
        let size = PhoneScreenSize(width: 540, height: 950)
        XCTAssertEqual(PhoneScreenPlacement.origin(for: size, main: main, others: []).map { [$0.x, $0.y] }, [1920, 0])
        let right = PhoneScreenRect(x: 1920, y: 200, width: 1512, height: 982)
        XCTAssertEqual(PhoneScreenPlacement.origin(for: size, main: main, others: [right]).map { [$0.x, $0.y] }, [-540, 0])
        let left = PhoneScreenRect(x: -2560, y: -300, width: 2560, height: 1440)
        XCTAssertEqual(PhoneScreenPlacement.origin(for: size, main: main, others: [right, left]).map { [$0.x, $0.y] }, [0, 1080])
        let below = PhoneScreenRect(x: 0, y: 1080, width: 800, height: 600)
        XCTAssertEqual(PhoneScreenPlacement.origin(for: size, main: main, others: [right, left, below]).map { [$0.x, $0.y] }, [0, -950])
        let above = PhoneScreenRect(x: -100, y: -900, width: 3000, height: 900)
        XCTAssertNil(PhoneScreenPlacement.origin(for: size, main: main, others: [right, left, below, above]))
    }

    func testADisplayThatOnlySharesAnEdgeLeavesTheSideFree() {
        let main = PhoneScreenRect(x: 0, y: 0, width: 1920, height: 1080)
        // Touches the right strip's corner only.
        let corner = PhoneScreenRect(x: 2460, y: 950, width: 100, height: 100)
        XCTAssertEqual(PhoneScreenPlacement.origin(for: PhoneScreenSize(width: 540, height: 950), main: main, others: [corner]).map { [$0.x, $0.y] }, [1920, 0])
    }

    func testRestoreRecordsKeepTheFirstFramePerWindow() throws {
        var r = PhoneScreenRestore()
        let first = PhoneScreenRestore.Record(pid: 10, window: 7, app: "A", frame: .init(x: 1, y: 2, width: 3, height: 4),
                                              minimized: false, fullScreen: true)
        r.add(first)
        var again = first
        again.frame = .init(x: 9, y: 9, width: 9, height: 9)
        r.add(again)
        XCTAssertEqual(r.records, [first])
        let data = try JSONEncoder().encode(r)
        XCTAssertEqual(try JSONDecoder().decode(PhoneScreenRestore.self, from: data), r)
        r.remove(pid: 10, window: 7)
        XCTAssertNil(r.record(pid: 10, window: 7))
    }

    func testMessagesRoundTripAndLeaveOutWhatIsNotSet() throws {
        let req = PhoneScreenRequest(op: "place", pid: 42, window: 9)
        let line = String(decoding: try JSONEncoder().encode(req), as: UTF8.self)
        XCTAssertFalse(line.contains("width"))
        XCTAssertEqual(try JSONDecoder().decode(PhoneScreenRequest.self, from: Data(line.utf8)), req)
        // The node writes snake-free keys; an unknown key from a newer node is ignored.
        let newer = #"{"op":"start","width":402,"height":760,"later":true}"#
        XCTAssertEqual(try JSONDecoder().decode(PhoneScreenRequest.self, from: Data(newer.utf8)),
                       PhoneScreenRequest(op: "start", width: 402, height: 760))
        XCTAssertEqual(PhoneScreenPreflight.line(trusted: true), "phone_screen: 1 granted")
    }

    func testTerminalsAndEditorsGetBiggerTextAndBrowsersDoNot() {
        XCTAssertEqual(PhoneScreenZoom.steps(for: "com.mitchellh.ghostty"), 2)
        XCTAssertEqual(PhoneScreenZoom.steps(for: "com.microsoft.VSCode"), 2)
        XCTAssertEqual(PhoneScreenZoom.steps(for: "com.google.Chrome"), 0)
        XCTAssertEqual(PhoneScreenZoom.steps(for: "com.apple.Safari"), 0)
        XCTAssertEqual(PhoneScreenZoom.steps(for: nil), 0)
        // An app-wide zoom is undone on the app even when its window went away; every one of them gets the zoom.
        XCTAssertTrue(PhoneScreenZoom.appWide.isSubset(of: PhoneScreenZoom.apps))
        XCTAssertFalse(PhoneScreenZoom.appWide.contains("com.mitchellh.ghostty"))
    }

    func testTheZoomGoesInTheRecordAndAnOldRecordStillReads() throws {
        var r = PhoneScreenRestore()
        r.add(.init(pid: 3, window: 4, app: "T", frame: .init(x: 0, y: 0, width: 1, height: 1), minimized: false, fullScreen: false))
        r.setZoom(2, pid: 3, window: 4)
        XCTAssertEqual(r.record(pid: 3, window: 4)?.zoom, 2)
        r.setZoom(5, pid: 9, window: 9) // no record: nothing
        XCTAssertEqual(r.records.count, 1)
        let old = #"{"records":[{"pid":1,"window":2,"app":"A","frame":{"x":0,"y":0,"width":1,"height":1},"minimized":false,"fullScreen":false}]}"#
        XCTAssertNil(try JSONDecoder().decode(PhoneScreenRestore.self, from: Data(old.utf8)).records.first?.zoom)
    }

    private func record(_ pid: Int32 = 3, _ window: UInt32 = 4) -> PhoneScreenRestore.Record {
        .init(pid: pid, window: window, app: "Terminal", frame: .init(x: 10, y: 20, width: 600, height: 400), minimized: false,
              fullScreen: false)
    }

    private func tempStore() throws -> (PhoneScreenRestoreStore, URL) {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("phone-screen-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        addTeardownBlock { try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir.path)
            try? FileManager.default.removeItem(at: dir) }
        return (PhoneScreenRestoreStore(url: dir.appendingPathComponent("phone-screen.json")), dir)
    }

    /// #9540: the restore file can't be written (its directory is read-only): no ⌘+ goes out, and the record in memory
    /// stays as the disk has it (the frame saved before the move, no zoom).
    func testAFailedSaveSendsNoZoomKeyAndKeepsMemoryAsTheDisk() throws {
        let (store, dir) = try tempStore()
        var restore = PhoneScreenRestore()
        restore.add(record())
        XCTAssertTrue(store.writeRecords(restore), "the frame record before the move")
        try FileManager.default.setAttributes([.posixPermissions: 0o500], ofItemAtPath: dir.path) // the next write fails
        var sent: [Int] = []
        let went = PhoneScreenZoom.raise(&restore, pid: 3, window: 4, steps: 2, save: store.writeRecords, send: { sent.append($0) })
        XCTAssertFalse(went)
        XCTAssertEqual(sent, [], "a zoom key was sent with no record of it on disk")
        XCTAssertNil(restore.record(pid: 3, window: 4)?.zoom, "memory says zoomed, the disk doesn't")
        XCTAssertEqual(store.readRecords(), restore, "memory and disk differ")
        // A save that fails some other way (the closure says so) is the same.
        XCTAssertFalse(PhoneScreenZoom.raise(&restore, pid: 3, window: 4, steps: 2, save: { _ in false }, send: { sent.append($0) }))
        XCTAssertEqual(sent, [])
    }

    /// #9540: the zoom saved, then the helper "crashes": a new session reads the file and gets back exactly the ⌘− that
    /// undo it; no record, no window: nothing is raised; already raised: not raised twice.
    func testAZoomSavedBeforeACrashIsUndoneAfterTheRestart() throws {
        let (store, _) = try tempStore()
        var restore = PhoneScreenRestore()
        restore.add(record())
        XCTAssertTrue(store.writeRecords(restore))
        var sent: [Int] = []
        XCTAssertTrue(PhoneScreenZoom.raise(&restore, pid: 3, window: 4, steps: 2, save: store.writeRecords, send: { sent.append($0) }))
        XCTAssertEqual(sent, [2])
        // The order the keys need: by the time they went, the disk had the steps.
        let afterCrash = store.readRecords()
        XCTAssertEqual(afterCrash.record(pid: 3, window: 4)?.zoom, 2)
        XCTAssertEqual(afterCrash.records.map(PhoneScreenZoom.undo), [-2])
        XCTAssertFalse(PhoneScreenZoom.raise(&restore, pid: 3, window: 4, steps: 2, save: store.writeRecords, send: { sent.append($0) }))
        XCTAssertFalse(PhoneScreenZoom.raise(&restore, pid: 9, window: 9, steps: 2, save: store.writeRecords, send: { sent.append($0) }))
        XCTAssertEqual(sent, [2], "raised twice, or a window with no record")
        // Put back: the record goes and so does the file.
        restore.remove(pid: 3, window: 4)
        XCTAssertTrue(store.writeRecords(restore))
        XCTAssertFalse(FileManager.default.fileExists(atPath: store.url.path))
        XCTAssertEqual(PhoneScreenZoom.undo(record()), 0)
    }

    func testOverlapAndMatch() {
        let a = PhoneScreenRect(x: 0, y: 0, width: 10, height: 10)
        XCTAssertFalse(a.overlaps(PhoneScreenRect(x: 10, y: 0, width: 5, height: 5)))
        XCTAssertTrue(a.overlaps(PhoneScreenRect(x: 9, y: 9, width: 5, height: 5)))
        XCTAssertTrue(a.matches(PhoneScreenRect(x: 1.5, y: 0, width: 10, height: 11)))
        XCTAssertFalse(a.matches(PhoneScreenRect(x: 3, y: 0, width: 10, height: 10)))
    }

    func testExtremeWidthsNonFiniteGeometryAndScrollReaderAtomicity() throws {
        // Non-finite viewWidth/viewHeight consistently fall back to ratio 2.
        XCTAssertEqual(PhoneScreenFit(viewWidth: .infinity, viewHeight: 100).ratio, 2)
        XCTAssertEqual(PhoneScreenFit(viewWidth: 100, viewHeight: .infinity).ratio, 2)

        // Huge positive/negative widths clamp instead of trapping Int((Double(width) * ratio).rounded()).
        let fit = PhoneScreenFit(ratio: 2.2)
        XCTAssertEqual(fit.size(width: Int.max).height, Int.max)
        XCTAssertEqual(fit.size(width: Int.min).height, Int.min)
        XCTAssertEqual(PhoneScreenFit.clampedInt(Double(Int.max)), Int.max)
        XCTAssertEqual(PhoneScreenFit.clampedInt(Double(Int.max).nextDown), Int(Double(Int.max).nextDown))
        XCTAssertEqual(PhoneScreenFit.clampedInt(Double(Int.min)), Int.min)
        XCTAssertEqual(PhoneScreenFit.clampedInt(Double(Int.min).nextUp), Int(Double(Int.min).nextUp))
        XCTAssertEqual(PhoneScreenFit.clampedInt(.infinity), Int.max)
        XCTAssertEqual(PhoneScreenFit.clampedInt(-.infinity), Int.min)
        XCTAssertEqual(PhoneScreenFit.clampedInt(.nan), 0)

        // Non-positive current width falls back to baseWidth; huge current clamps to maxWidth.
        let area = PhoneScreenSize(width: 540, height: 990)
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 700, height: 990), in: area, at: 0), fit.baseWidth)
        XCTAssertEqual(fit.width(toFit: PhoneScreenSize(width: 700, height: 990), in: area, at: Int.max), PhoneScreenFit.maxWidth)

        // Non-finite, zero-area, and negative-dimension rects/tolerances are rejected cleanly.
        let valid = PhoneScreenRect(x: 0, y: 0, width: 1920, height: 1080)
        let nanRect = PhoneScreenRect(x: .nan, y: 0, width: 100, height: 100)
        let overflowRect = PhoneScreenRect(x: 1e308, y: 0, width: 1e308, height: 100)
        let zeroWidthRect = PhoneScreenRect(x: 100, y: 100, width: 0, height: 100)
        let zeroHeightRect = PhoneScreenRect(x: 100, y: 100, width: 100, height: 0)
        let negativeWidthRect = PhoneScreenRect(x: 100, y: 100, width: -50, height: 100)
        let negativeHeightRect = PhoneScreenRect(x: 100, y: 100, width: 100, height: -50)
        let ulpCollapsedRect = PhoneScreenRect(x: 1e20, y: 0, width: 1, height: 100)
        XCTAssertFalse(valid.overlaps(nanRect))
        XCTAssertFalse(valid.overlaps(overflowRect))
        XCTAssertFalse(valid.overlaps(zeroWidthRect))
        XCTAssertFalse(zeroWidthRect.overlaps(valid))
        XCTAssertFalse(valid.overlaps(zeroHeightRect))
        XCTAssertFalse(zeroHeightRect.overlaps(valid))
        XCTAssertFalse(valid.overlaps(negativeWidthRect))
        XCTAssertFalse(negativeWidthRect.overlaps(valid))
        XCTAssertFalse(valid.overlaps(negativeHeightRect))
        XCTAssertFalse(negativeHeightRect.overlaps(valid))
        XCTAssertFalse(ulpCollapsedRect.overlaps(PhoneScreenRect(x: 0, y: 0, width: 2e20, height: 100)))
        XCTAssertFalse(valid.matches(valid, tolerance: .nan))
        XCTAssertFalse(valid.matches(valid, tolerance: -1))
        XCTAssertFalse(nanRect.matches(nanRect, tolerance: 2))
        XCTAssertNil(PhoneScreenPlacement.origin(for: PhoneScreenSize(width: 540, height: 950), main: nanRect, others: []))
        XCTAssertNil(PhoneScreenPlacement.origin(for: PhoneScreenSize(width: 0, height: 950), main: valid, others: []))
        XCTAssertNil(PhoneScreenPlacement.origin(for: PhoneScreenSize(width: 540, height: 950), main: PhoneScreenRect(x: 0, y: 0, width: 0, height: 100), others: []))
        // Zero-area and negative-size entries in `others` must not block placement on the right side.
        let degenerateRightOthers = [
            PhoneScreenRect(x: 2000, y: 100, width: 0, height: 500),
            PhoneScreenRect(x: 2000, y: 100, width: 500, height: 0),
            PhoneScreenRect(x: 2200, y: 100, width: -100, height: 500),
            PhoneScreenRect(x: 2000, y: 400, width: 500, height: -100),
        ]
        XCTAssertEqual(
            PhoneScreenPlacement.origin(for: PhoneScreenSize(width: 540, height: 950), main: valid, others: degenerateRightOthers).map { [$0.x, $0.y] },
            [1920, 0]
        )

        // ScrollRecordReader.push rolls back atomically on a mid-batch decode error, handles non-zero startIndex Data slices, and scales linearly.
        var reader = ScrollRecordReader()
        let good = ScrollRecord(kind: .scroll, phase: .changed, momentum: .none, dy: 3.25, dx: -0.5).encoded()
        var bad = good
        bad[0] = 0x05 // invalid message type
        // Buffer first 4 bytes of a valid record, then push 12 valid bytes + 16 invalid bytes -> must throw and keep original 4 bytes intact.
        XCTAssertEqual(try reader.push(Data(good).prefix(4)), [])
        XCTAssertThrowsError(try reader.push(Array(good.dropFirst(4)) + bad)) { err in
            XCTAssertEqual(err as? ScrollRecord.DecodeError, .type(0x05))
        }
        // Completing the originally buffered record using a non-zero startIndex Data slice succeeds cleanly.
        let dataTail = Data(good).dropFirst(4)
        XCTAssertEqual(dataTail.startIndex, 4)
        let recovered = try reader.push(dataTail)
        XCTAssertEqual(recovered.count, 1)
        XCTAssertEqual(recovered[0].dy, 3.25)

        // Large batch (4,000 records = 64 KB) + partial tail decodes linearly and rolls back cleanly on a large invalid push.
        var bulk = [UInt8]()
        bulk.reserveCapacity(4_000 * ScrollRecord.size)
        for _ in 0 ..< 4_000 { bulk.append(contentsOf: good) }
        XCTAssertEqual(try reader.push(bulk + good.prefix(4)).count, 4_000)
        XCTAssertThrowsError(try reader.push(Array(good.dropFirst(4)) + bulk + bad)) { err in
            XCTAssertEqual(err as? ScrollRecord.DecodeError, .type(0x05))
        }
        XCTAssertEqual(try reader.push(Data(good).dropFirst(4)).count, 1)
        XCTAssertEqual(try reader.push(bulk).count, 4_000)
    }
}
