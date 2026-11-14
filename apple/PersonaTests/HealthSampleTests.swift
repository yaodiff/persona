import Foundation
import HealthKit
import Testing
@testable import Persona
import PersonaWire

/// `phone_health_write_sample` saves exactly what its confirmation showed: one sample, read and checked from the
/// arguments before anything is asked (HealthTool.swift `HealthSample`).
@MainActor
struct HealthSampleTests {
    private let now = Date(timeIntervalSince1970: 1_793_700_000) // 2026-11-03, local time whatever the simulator's
    private func args(_ pairs: [String: JSONValue]) -> JSONValue { .object(pairs) }
    private func at(_ minutesAgo: Double) -> String { ToolDate.string(now.addingTimeInterval(-minutesAgo * 60)) }

    @Test func aWeighingIsAMomentWithTheValueItShows() throws {
        let s = try HealthSample(args(["metric": "weight", "value": .number(72.456), "start": .string(at(30)), "end": .string(at(5))]), now: now)
        #expect(s.value == 72.46)
        #expect(s.end == s.start, "a weight is measured at a moment; end is not used")
        #expect(s.summary == "写入健康：体重 72.46 千克\n\(at(30))")
    }

    @Test func stepsCoverTheirPeriod() throws {
        let s = try HealthSample(args(["metric": "steps", "value": .number(1200), "start": .string(at(120)), "end": .string(at(60))]), now: now)
        #expect(s.summary == "写入健康：步数 1200 步\n\(at(120)) → \(at(60))")
        #expect(s.object() is HKQuantitySample)
    }

    @Test func sleepIsItsPeriodWithoutAValue() throws {
        let s = try HealthSample(args(["metric": "sleep", "start": .string(at(8 * 60)), "end": .string(at(30))]), now: now)
        #expect(s.value == nil)
        #expect(s.summary == "写入健康：睡眠 7.5 小时\n\(at(8 * 60)) → \(at(30))")
        #expect(s.object() is HKCategorySample)
        #expect(throws: ToolFailure.self) { try HealthSample(args(["metric": "sleep", "start": .string(at(60))]), now: now) }
    }

    @Test func whatCannotBeOneSampleIsRefused() {
        let bad: [[String: JSONValue]] = [
            ["metric": "heart_rate", "value": .number(60), "start": .string(at(5))], // not one of the five
            ["metric": "weight", "start": .string(at(5))],                           // no value
            ["metric": "weight", "value": .number(72_500), "start": .string(at(5))], // grams for kilograms
            ["metric": "steps", "value": .number(12.5), "start": .string(at(5))],    // steps are whole
            ["metric": "steps", "value": .number(-3), "start": .string(at(5))],
            ["metric": "weight", "value": .number(72), "start": .string(at(-60))],   // an hour from now
            ["metric": "steps", "value": .number(100), "start": .string(at(5)), "end": .string(at(10))], // ends before it starts
            ["metric": "steps", "value": .number(100), "start": .string(at(26 * 60)), "end": .string(at(5))], // over 24 hours
            ["metric": "distance", "value": .number(500)],                          // no start
        ]
        for a in bad {
            #expect(throws: ToolFailure.self, "\(a)") { try HealthSample(args(a), now: now) }
        }
    }

    @Test func theConfirmationOfBadArgumentsSaysNothingIsSaved() throws {
        let tool = try #require(PhoneToolRegistry.all.first { $0.spec.name == "phone_health_write_sample" })
        let summary = tool.confirmation?(args(["metric": "weight", "value": .number(72_500), "start": .string(at(5))]))
        #expect(summary?.hasPrefix("参数有误，不会写入健康") == true, "\(summary ?? "nil")")
    }
}
