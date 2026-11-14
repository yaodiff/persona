import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

struct MachineMetricsTests {
    @Test func decodesTheHubsResponse() throws {
        let json = """
        {"machine_id":"m1","online":true,
         "system":{"os":"linux","distro":"ubuntu","like":"debian","name":"Ubuntu 24.04.1 LTS","version":"24.04",
                   "kernel":"6.8.0","arch":"amd64","cores":16,"memory":68719476736,"boot_time":1790000000},
         "latest":{"t":1790003600,"cpu":42.5,"cores":[40,45],"load":3.1,"mem_used":34359738368,"mem_total":68719476736,
                   "swap_used":0,"swap_total":0,"net_rx":1024,"disk_used":10,"disk_total":100},
         "points":[{"t":1790000000,"cpu":10},{"t":1790000010,"gap":true},{"t":1790000600,"cpu":20,"rx":5}]}
        """
        let m = try JSON.decoder.decode(MachineMetrics.self, from: Data(json.utf8))
        #expect(m.system?.family == .ubuntu)
        #expect(m.system?.bootDate == Date(timeIntervalSince1970: 1_790_000_000))
        #expect(m.latest?.memPercent == 50)
        #expect(m.latest?.netTx == nil) // not measured is not 0
        #expect(m.points.map(\.isGap) == [false, true, false])
    }

    @Test func osFamilies() {
        #expect(OSFamily(distro: "macos", like: nil, os: "darwin") == .macos)
        #expect(OSFamily(distro: "pop", like: "ubuntu debian", os: "linux") == .ubuntu)
        #expect(OSFamily(distro: "raspbian", like: "debian", os: "linux") == .debian)
        #expect(OSFamily(distro: "rocky", like: "rhel centos fedora", os: "linux") == .rocky)
        #expect(OSFamily(distro: "somethingnew", like: "fedora", os: "linux") == .fedora)
        #expect(OSFamily(distro: "gentoo", like: nil, os: "linux") == .linux)
        #expect(OSFamily(distro: "freebsd", like: nil, os: "freebsd") == .other)
    }

    @Test func alertsWorstFirst() {
        var s = MetricSample(t: 0, cpu: 95, load: 20, memUsed: 85, memTotal: 100, swapUsed: 0, swapTotal: 0, diskUsed: 10, diskTotal: 100)
        #expect(MetricAlert.of(s, cores: 16).map(\.label) == ["CPU 高", "内存紧张", "负载高"])
        #expect(MetricAlert.of(s, cores: 16).first?.level == .critical)
        s.cpu = 40; s.memUsed = 50; s.load = 4
        #expect(MetricAlert.of(s, cores: 16).isEmpty)
        s.diskUsed = 92
        #expect(MetricAlert.of(s, cores: nil) == [MetricAlert(label: "磁盘快满", level: .critical)])
    }

    @Test func formats() {
        #expect(MetricFormat.bytes(512.0) == "512 B")
        #expect(MetricFormat.bytes(1536.0) == "1.50 KB")
        #expect(MetricFormat.bytes(Int64(64) << 30) == "64.0 GB")
        #expect(MetricFormat.bytes(Double(200) * 1024 * 1024 * 1024) == "200 GB")
        // Rounding that reaches the next unit or the next width takes it.
        #expect(MetricFormat.bytes(1023.0) == "1023 B")
        #expect(MetricFormat.bytes(1023.4 * 1024) == "1023 KB")
        #expect(MetricFormat.bytes(1023.6 * 1024) == "1.00 MB")
        #expect(MetricFormat.bytes(99.97 * 1024 * 1024 * 1024) == "100 GB")
        #expect(MetricFormat.bytes(9.998 * 1024 * 1024) == "10.0 MB")
        #expect(MetricFormat.rate(nil) == "—")
        #expect(MetricFormat.rate(3.91 * 1024 * 1024) == "3.91 MB/s")
        #expect(MetricFormat.percent(4.46) == "4.5%")
        #expect(MetricFormat.percent(93.6) == "94%")
        #expect(MetricFormat.percent(0) == "0%")
        let now = Date(timeIntervalSince1970: 1_000_000)
        #expect(MetricFormat.uptime(since: now.addingTimeInterval(-273_660), now: now) == "3 天 4 小时")
        #expect(MetricFormat.uptime(since: now.addingTimeInterval(-18_720), now: now) == "5 小时 12 分钟")
    }

    /// A gap starts a new line: no segment of the chart spans a time the machine sent nothing.
    @Test @MainActor func gapsSplitTheLines() {
        let pts: [MetricPoint] = [
            .init(t: 0, cpu: 1), .init(t: 10, cpu: 2), .init(t: 20, gap: true), .init(t: 600, cpu: 3), .init(t: 610, cpu: nil),
        ]
        let values = pts.chartValues(ChartSeries(name: "CPU", color: .blue) { $0.cpu })
        #expect(values.map(\.series) == ["CPU#0", "CPU#0", "CPU#1"])
        #expect(pts.nearest(Date(timeIntervalSince1970: 12), step: 10)?.t == 10)
        #expect(pts.nearest(Date(timeIntervalSince1970: 300), step: 10) == nil) // in the gap: nothing to read
    }

    // MARK: The dashboard's sparkline (drawn on a canvas, #390)

    @Test @MainActor func sparklineRunsSplitAtGapsAndMapTheDomain() {
        let t0 = Date(timeIntervalSince1970: 1_790_000_000)
        let points = [MetricPoint(t: 1_790_000_000, cpu: 0), MetricPoint(t: 1_790_000_600, cpu: 50),
                      MetricPoint(t: 1_790_001_200, gap: true), MetricPoint(t: 1_790_001_800, cpu: 100),
                      MetricPoint(t: 1_790_002_400, gap: true), MetricPoint(t: 1_790_003_000, cpu: 25), MetricPoint(t: 1_790_003_600, cpu: 25)]
        let values = points.chartValues(ChartSeries(name: "CPU", color: .blue) { $0.cpu })
        let runs = SparklineGeometry.runs(values, domain: t0...t0.addingTimeInterval(3600), top: 100, size: CGSize(width: 60, height: 20))
        // Three runs between the gaps; the one-point run in the middle has nothing to draw.
        #expect(runs.count == 2)
        #expect(runs[0] == [CGPoint(x: 0, y: 20), CGPoint(x: 10, y: 10)]) // 0 at the bottom, 50 halfway
        #expect(runs[1] == [CGPoint(x: 50, y: 15), CGPoint(x: 60, y: 15)])
    }

    @Test @MainActor func sparklineCurveNeverOvershoots() {
        // A step: flat, up, flat. The tangents at the corners are 0, so the curve stays between the levels.
        let step = [CGPoint(x: 0, y: 10), CGPoint(x: 1, y: 10), CGPoint(x: 2, y: 0), CGPoint(x: 3, y: 0)]
        let t = SparklineGeometry.tangents(step)
        #expect(t[1] == 0 && t[2] == 0)
        // A peak is flat at the top.
        #expect(SparklineGeometry.tangents([CGPoint(x: 0, y: 5), CGPoint(x: 1, y: 0), CGPoint(x: 2, y: 5)])[1] == 0)
        // A straight run stays straight, ends included.
        #expect(SparklineGeometry.tangents([CGPoint(x: 0, y: 0), CGPoint(x: 1, y: 2), CGPoint(x: 2, y: 4)]) == [2, 2, 2])
        // Two points: a straight line.
        #expect(SparklineGeometry.tangents([CGPoint(x: 0, y: 0), CGPoint(x: 2, y: 1)]) == [0.5, 0.5])
        // Every control point of the step's curve lies between its levels.
        var ys: [CGFloat] = []
        SparklineGeometry.line(step).forEach { e in
            if case .curve(let to, let c1, let c2) = e { ys += [to.y, c1.y, c2.y] }
        }
        #expect(!ys.isEmpty && ys.allSatisfy { (0...10).contains($0) })
    }

    @Test @MainActor func reusingTheStrokePreservesWashAndOriginalPath() {
        let cases: [[CGPoint]] = [
            [], [CGPoint(x: 0, y: 7)],
            [CGPoint(x: 0, y: 10), CGPoint(x: 10, y: 0)],
            [CGPoint(x: 0, y: 10), CGPoint(x: 1, y: 10), CGPoint(x: 2, y: 0), CGPoint(x: 3, y: 0)],
            [CGPoint(x: 0, y: 5), CGPoint(x: 1, y: 0), CGPoint(x: 1, y: 10), CGPoint(x: 2, y: 5)],
            [CGPoint(x: 1, y: 0), CGPoint(x: 1, y: 10)],
            [CGPoint(x: 1, y: 0), CGPoint(x: 1, y: 10), CGPoint(x: 1, y: 5)],
        ]
        for points in cases {
            let stroke = SparklineGeometry.line(points)
            let original = pathElements(stroke)
            for bottom in [CGFloat(0), 20, 37.5] {
                let reused = SparklineGeometry.wash(points, line: stroke, bottom: bottom)
                let expected = pathElements(legacyWash(points, bottom: bottom))
                #expect(pathElements(reused) == expected)
                #expect(pathElements(SparklineGeometry.wash(points, bottom: bottom)) == expected)
                #expect(pathElements(stroke) == original)
            }
        }
    }

    @Test @MainActor func washUsesTheSuppliedPathInsteadOfRebuildingIt() {
        let points = [CGPoint(x: 0, y: 3), CGPoint(x: 10, y: 7)]
        var supplied = Path()
        supplied.move(to: points[0])
        supplied.addLine(to: points[1])
        let prefix = pathElements(supplied)
        let wash = SparklineGeometry.wash(points, line: supplied, bottom: 17)
        #expect(Array(pathElements(wash).prefix(prefix.count)) == prefix)
        #expect(prefix != pathElements(SparklineGeometry.line(points)))
        #expect(pathElements(supplied) == prefix)
    }

    @Test @MainActor func reusingPathsKeepsGapRunsSeparate() {
        let start = Date(timeIntervalSince1970: 0)
        let values = [ChartValue(series: "first", name: "CPU", date: start, value: 0),
                      ChartValue(series: "first", name: "CPU", date: start.addingTimeInterval(10), value: 50),
                      ChartValue(series: "singleton", name: "CPU", date: start.addingTimeInterval(20), value: 100),
                      ChartValue(series: "last", name: "CPU", date: start.addingTimeInterval(50), value: 25),
                      ChartValue(series: "last", name: "CPU", date: start.addingTimeInterval(60), value: 25)]
        let runs = SparklineGeometry.runs(values, domain: start...start.addingTimeInterval(60),
                                         top: 100, size: CGSize(width: 60, height: 20))
        #expect(runs.count == 2)
        for run in runs {
            let stroke = SparklineGeometry.line(run)
            let fill = SparklineGeometry.wash(run, line: stroke, bottom: 20)
            #expect(pathElements(fill) == pathElements(legacyWash(run, bottom: 20)))
            #expect(pathElements(stroke) == pathElements(SparklineGeometry.line(run)))
        }
    }

    /// Independent reference for the old renderer's close-to-baseline sequence.
    @MainActor private func legacyWash(_ points: [CGPoint], bottom: CGFloat) -> Path {
        var path = SparklineGeometry.line(points)
        if let first = points.first, let last = points.last {
            path.addLine(to: CGPoint(x: last.x, y: bottom))
            path.addLine(to: CGPoint(x: first.x, y: bottom))
            path.closeSubpath()
        }
        return path
    }

    @MainActor private func pathElements(_ path: Path) -> [CGFloat] {
        var values: [CGFloat] = []
        path.forEach { element in
            switch element {
            case .move(let point): values += [0, point.x, point.y]
            case .line(let point): values += [1, point.x, point.y]
            case .quadCurve(let point, let control): values += [2, point.x, point.y, control.x, control.y]
            case .curve(let point, let first, let second):
                values += [3, point.x, point.y, first.x, first.y, second.x, second.y]
            case .closeSubpath: values += [4]
            }
        }
        return values
    }
}
