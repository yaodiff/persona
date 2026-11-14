import Foundation
import Network
#if canImport(UIKit)
import UIKit
#endif
import PersonaWire

/// `phone_device_info`: model, OS, battery, storage, network, locale, the app's version.
enum DeviceTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_device_info",
                    description: """
                    Describe the phone: model identifier and name, iOS version, battery percentage and \
                    charging state, low power mode, free and total storage in bytes, current network \
                    type (wifi/cellular/wired/offline), timezone and locale, and the Persona app's version, \
                    build number and build channel (debug/release). Cheap and safe to call when you need \
                    context about the device.
                    """,
                    inputSchema: Schema.object([:])
                ),
                source: .device,
                run: { _ in await info() }
            )
        ]
    }

    static func info() async -> [ToolContent] {
        let device = UIDevice.current
        let wasMonitoring = device.isBatteryMonitoringEnabled
        device.isBatteryMonitoringEnabled = true
        let level = device.batteryLevel
        let state = device.batteryState
        if !wasMonitoring { device.isBatteryMonitoringEnabled = false }

        let battery: JSONValue
        if level < 0 {
            // Simulators and some devices report -1 when unknown.
            battery = .string("unknown")
        } else {
            battery = .number(Double((level * 100).rounded()))
        }
        let charging: String = switch state {
        case .charging: "charging"
        case .full: "full"
        case .unplugged: "unplugged"
        default: "unknown"
        }

        let storage = Self.storage()
        let app = AppVersion.current
        let network = await NetworkProbe.currentType()

        return Output.json(Output.object([
            ("model", .string(deviceModel())),
            ("model_name", .string(device.name)),
            ("system", .string("\(device.systemName) \(device.systemVersion)")),
            ("ios_version", .string(device.systemVersion)),
            ("battery_percent", battery),
            ("battery_state", .string(charging)),
            ("low_power_mode", .bool(ProcessInfo.processInfo.isLowPowerModeEnabled)),
            ("storage_free_bytes", storage.free.map { JSONValue.number(Double($0)) }),
            ("storage_total_bytes", storage.total.map { JSONValue.number(Double($0)) }),
            ("network", .string(network)),
            ("timezone", .string(TimeZone.current.identifier)),
            ("utc_offset_seconds", .number(Double(TimeZone.current.secondsFromGMT()))),
            ("locale", .string(Locale.current.identifier)),
            ("preferred_languages", .array(Locale.preferredLanguages.map { .string($0) })),
            ("thermal_state", .string(thermalName(ProcessInfo.processInfo.thermalState))),
            ("app_version", .string(app.version)),
            ("app_build", .string(app.build)),
            ("app_channel", .string(app.channel)),
        ]))
    }

    /// `uname` gives the hardware identifier (e.g. iPhone17,1) which Claude
    /// understands better than the user-visible device name.
    private static func deviceModel() -> String {
        var info = utsname()
        guard uname(&info) == 0 else { return "" }
        let machine = withUnsafeBytes(of: &info.machine) { buffer -> String in
            let bytes = buffer.prefix { $0 != 0 }
            return String(decoding: bytes, as: UTF8.self)
        }
        return machine
    }

    private static func storage() -> (free: Int64?, total: Int64?) {
        let url = URL(fileURLWithPath: NSHomeDirectory())
        let values = try? url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey, .volumeTotalCapacityKey])
        let free = values?.volumeAvailableCapacityForImportantUsage
        let total = values?.volumeTotalCapacity.map(Int64.init)
        return (free, total)
    }

    private static func thermalName(_ state: ProcessInfo.ThermalState) -> String {
        switch state {
        case .nominal: "nominal"
        case .fair: "fair"
        case .serious: "serious"
        case .critical: "critical"
        @unknown default: "unknown"
        }
    }
}

/// Reads one snapshot of `NWPathMonitor`, which only reports through its
/// callback. The monitor lives outside the main actor because it calls back on
/// its own queue.
nonisolated enum NetworkProbe {
    static func currentType() async -> String {
        let monitor = NWPathMonitor()
        let path: NWPath? = await withCheckedContinuation { (continuation: CheckedContinuation<NWPath?, Never>) in
            let gate = SingleResumeBox<NWPath?>(continuation)
            monitor.pathUpdateHandler = { path in
                gate.succeed(path)
                monitor.cancel()
            }
            monitor.start(queue: DispatchQueue.global(qos: .utility))
        }
        guard let path else { return "unknown" }
        guard path.status == .satisfied else { return "offline" }
        if path.usesInterfaceType(.wifi) { return "wifi" }
        if path.usesInterfaceType(.cellular) { return "cellular" }
        if path.usesInterfaceType(.wiredEthernet) { return "wired" }
        if path.usesInterfaceType(.loopback) { return "loopback" }
        if path.isExpensive { return "cellular" }
        return "other"
    }
}

/// A one-shot box that does not require the payload to be `Error`-throwing.
/// `SingleResume` in PhoneToolSupport is typed for throwing continuations.
nonisolated final class SingleResumeBox<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, Never>?

    init(_ continuation: CheckedContinuation<T, Never>) {
        self.continuation = continuation
    }

    func succeed(_ value: T) {
        lock.lock()
        let pending = continuation
        continuation = nil
        lock.unlock()
        pending?.resume(returning: value)
    }
}
