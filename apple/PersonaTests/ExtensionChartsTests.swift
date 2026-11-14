import Foundation
import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// UI version 6's charts on iOS (#827): what a spark reads to VoiceOver and its color, a candle's direction, and the
/// chart showcase read with every new field (schema/extension/examples/showcase/chart-v6.json, spark.json).
@MainActor
struct ExtensionChartsTests {
    private static let showcase = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples/showcase")

    private static func view(_ name: String) throws -> ExtensionView {
        try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: showcase.appending(path: name)))
    }

    /// The same catalog entries as the app's (the tests run in Chinese): the numbers go in as the text they are drawn as.
    @Test func aSparkSaysWhereItWent() {
        #expect(TrendLine.spoken([1, 3, 2, 5]) == String(localized: "Trending up, from \("1") to \("5"), low \("1"), high \("5")"))
        #expect(TrendLine.spoken([5, 4]) == String(localized: "Trending down, from \("5") to \("4"), low \("4"), high \("5")"))
        #expect(TrendLine.spoken([2, 2]) == String(localized: "Flat at \("2"), low \("2"), high \("2")"))
        #expect(TrendLine.spoken([.nan]) == "", "nothing to say")
    }

    @Test func aSparksColor() {
        #expect(ExtensionSparkLine.color(ExtensionSpark(values: [1, 2], color: "teal")) == Theme.Hue.teal)
        #expect(ExtensionSparkLine.color(ExtensionSpark(values: [1, 2], tone: "danger")) == Theme.danger)
        #expect(ExtensionSparkLine.color(ExtensionSpark(values: [1, 2])) == Color.secondary, "the quiet ink")
    }

    @Test func aCandleRisesWhenItClosesHigher() {
        #expect(CandleChart.Candle(label: "a", open: 1, high: 3, low: 0.5, close: 2).rises)
        #expect(!CandleChart.Candle(label: "a", open: 2, high: 3, low: 0.5, close: 1).rises)
    }

    @Test func theShowcasesAreReadWhole() throws {
        let charts = try Self.view("chart-v6.json").sections.flatMap(\.items).compactMap { if case .chart(let c) = $0 { c } else { nil } }
        let candles = try #require(charts.first { $0.kind == ExtensionChartKind.candlestick }?.series.first?.points)
        #expect(candles.allSatisfy { $0.open != nil && $0.high != nil && $0.low != nil && $0.close != nil })
        #expect(charts.contains { $0.kind == ExtensionChartKind.area })
        let items = try Self.view("spark.json").sections.flatMap(\.items)
        #expect(items.contains { if case .row(let r) = $0 { r.spark != nil } else { false } })
        #expect(items.contains { if case .metric(let m) = $0 { m.spark != nil } else { false } })
    }
}
