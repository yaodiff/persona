import Darwin
import SwiftUI
import UIKit
@testable import Persona

/// How far below its caller's frame `body` reaches into this thread's stack (#6735): the unused stack is painted with a
/// pattern first, and the lowest word no longer holding it after `body` is the deepest point reached.
///
/// An iPhone's main thread has a 1 MB stack (1008 KB in a crash report's `vmRegionInfo`); the simulator's has 8 MB, so
/// a page that needs most of a megabyte runs here and crashes only on a phone. Measuring the depth catches it in the
/// simulator.
enum StackPeak {
    /// Half the device's main-thread stack: the run loop, UIKit and the attribute graph sit below a page's body when it
    /// renders on the phone, and data the test does not have (more rows, open sheets) goes deeper.
    static let pageBudget = 512 * 1024

    private static let pattern: UInt64 = 0x5EED_5EED_5EED_5EED

    @inline(never) static func measure(_ body: () -> Void) -> Int {
        let thread = pthread_self()
        let top = UInt(bitPattern: pthread_get_stackaddr_np(thread))
        let bottom = top - UInt(pthread_get_stacksize_np(thread))
        var marker = 0
        let here = withUnsafeMutablePointer(to: &marker) { UInt(bitPattern: $0) }
        // Below this frame and the calls that paint and scan; above the guard pages.
        let start = (here - 32 * 1024) & ~UInt(7)
        let end = (bottom + 64 * 1024) & ~UInt(7)
        paint(from: end, to: start)
        body()
        return Int(here - lowestTouched(from: end, to: start))
    }

    /// The stack a view's first render in a phone-sized window reaches.
    @MainActor static func render<V: View>(_ view: V, in scene: UIWindowScene) -> (bytes: Int, window: UIWindow) {
        let window = UIWindow(windowScene: scene)
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 844)
        let host = UIHostingController(rootView: view)
        let bytes = measure {
            window.rootViewController = host
            window.isHidden = false
            window.layoutIfNeeded()
        }
        return (bytes, window)
    }

    @inline(never) private static func paint(from low: UInt, to high: UInt) {
        var p = low
        while p < high { UnsafeMutablePointer<UInt64>(bitPattern: p)!.pointee = pattern; p += 8 }
    }

    @inline(never) private static func lowestTouched(from low: UInt, to high: UInt) -> UInt {
        var p = low
        while p < high, UnsafeMutablePointer<UInt64>(bitPattern: p)!.pointee == pattern { p += 8 }
        return p
    }
}
