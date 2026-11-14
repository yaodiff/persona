import SwiftUI

extension View {
    /// Ends a segment of a long modifier chain (#6735): the rest of the chain starts from an erased view.
    ///
    /// Unoptimized (Debug), a function reserves a stack slot on entry for every intermediate of its modifier chain, and
    /// each holds everything before it (bindings with their values, ids, closures), so the slots add up like a triangle.
    /// On an iPhone, whose main thread has a 1 MB stack (the simulator's has 8 MB), a page's chains overflowed it. Cut a
    /// chain every few heavy modifiers. The erased type is the same on every update, so identity and state stay; measure
    /// with `StackPeak` (docs/lessons/ios.md, "A Debug build on an iPhone has a 1 MB main-thread stack").
    func stackSegment() -> AnyView { AnyView(self) }
}
