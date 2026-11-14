import CoreGraphics
import Foundation
import Testing
@testable import Persona

/// Which of a Mac's displays the VNC page offers, and where touches land with one of them shown (#80).
struct VNCDisplaysTests {
    private func screen(_ id: UInt32, _ x: CGFloat, _ w: CGFloat, _ h: CGFloat = 300) -> RFBClient.Screen {
        RFBClient.Screen(id: id, frame: CGRect(x: x, y: 0, width: w, height: h))
    }

    private let twoNodeDisplays = [MachineDisplay(id: 1, x: 0, y: 0, width: 240, height: 150, main: true),
                                   MachineDisplay(id: 2, x: 240, y: 0, width: 240, height: 150)]

    /// The server's own screens win, numbered left to right; with one screen from it, the node's arrangement is used; a
    /// screen beyond the framebuffer (a list from before a resize) is not trusted.
    @Test func theServersScreensComeFirst() {
        let fb = CGSize(width: 960, height: 300)
        let own = VNCDisplays.layout(server: [screen(9, 480, 480), screen(4, 0, 480)], machine: twoNodeDisplays, framebuffer: fb)
        #expect(own == [VNCDisplay(id: "screen-4", frame: CGRect(x: 0, y: 0, width: 480, height: 300)),
                        VNCDisplay(id: "screen-9", frame: CGRect(x: 480, y: 0, width: 480, height: 300))])
        #expect(VNCDisplays.layout(server: [screen(1, 0, 960)], machine: twoNodeDisplays, framebuffer: fb).map(\.id) == ["display-1", "display-2"])
        #expect(VNCDisplays.fromServer([screen(1, 0, 480), screen(2, 480, 600)], framebuffer: fb).isEmpty)
    }

    /// The node's arrangement (points, global display space) scaled onto the framebuffer (pixels): a laptop left of a
    /// larger main display, top edges level, sent at 2× and at 1×.
    @Test func theNodesArrangementIsScaledOntoTheFramebuffer() {
        let node = [MachineDisplay(id: 3, x: 0, y: 0, width: 2560, height: 1440, main: true),
                    MachineDisplay(id: 1, x: -1728, y: 0, width: 1728, height: 1117)]
        #expect(VNCDisplays.fromMachine(node, framebuffer: CGSize(width: 8576, height: 2880)) == [
            VNCDisplay(id: "display-1", frame: CGRect(x: 0, y: 0, width: 3456, height: 2234)),
            VNCDisplay(id: "display-3", frame: CGRect(x: 3456, y: 0, width: 5120, height: 2880)),
        ])
        #expect(VNCDisplays.fromMachine(node, framebuffer: CGSize(width: 4288, height: 1440)).map(\.frame.minX) == [0, 1728])
    }

    /// #7775: a wheel notch scrolls a point of the Mac's screen, so two fingers turn view points into Mac points through
    /// the framebuffer's pixels a point: 2 for displays sent at 2×, 1 at 1× or with nothing to go by.
    @Test func pixelsPerMacPointFollowTheArrangement() {
        let node = [MachineDisplay(id: 3, x: 0, y: 0, width: 2560, height: 1440, main: true),
                    MachineDisplay(id: 1, x: -1728, y: 0, width: 1728, height: 1117)]
        #expect(VNCDisplays.pixelsPerMacPoint(node, framebuffer: CGSize(width: 8576, height: 2880)) == 2)
        #expect(VNCDisplays.pixelsPerMacPoint(node, framebuffer: CGSize(width: 4288, height: 1440)) == 1)
        #expect(VNCDisplays.pixelsPerMacPoint([node[0]], framebuffer: CGSize(width: 5120, height: 2880)) == 2, "one display too")
        #expect(VNCDisplays.pixelsPerMacPoint([], framebuffer: CGSize(width: 1920, height: 1080)) == 1, "nothing to go by")
        #expect(VNCDisplays.pixelsPerMacPoint(node, framebuffer: CGSize(width: 8576, height: 1000)) == 1, "not its shape")
    }

    /// An arrangement without the framebuffer's shape (a display plugged in or out since the node looked, or a server that
    /// draws them otherwise) offers nothing; nor do fewer than two displays.
    @Test func anArrangementThatDoesNotFitOffersNothing() {
        let node = [MachineDisplay(id: 1, x: 0, y: 0, width: 1512, height: 982), MachineDisplay(id: 2, x: 1512, y: 0, width: 1920, height: 1080)]
        #expect(VNCDisplays.fromMachine(node, framebuffer: CGSize(width: 3432, height: 1080)).count == 2)
        #expect(VNCDisplays.fromMachine(node, framebuffer: CGSize(width: 1512, height: 982)).isEmpty, "only the laptop is sent")
        #expect(VNCDisplays.fromMachine(node, framebuffer: CGSize(width: 6864, height: 2260)).isEmpty, "4% off its shape")
        #expect(VNCDisplays.fromMachine(Array(node.prefix(1)), framebuffer: CGSize(width: 1512, height: 982)).isEmpty)
        #expect(VNCDisplays.layout(server: [], machine: [], framebuffer: CGSize(width: 960, height: 300)).isEmpty)
    }

    /// Display 2 of a 960 × 300 framebuffer drawn in a 240 × 150 pt rect: a touch lands on its pixels in whole-framebuffer
    /// coordinates, a touch beside the picture is kept on it, and the framebuffer is drawn so that display fills the rect.
    @Test func touchesLandOnTheDisplayShown() {
        let port = VNCViewport(visible: CGRect(x: 480, y: 0, width: 480, height: 300), rect: CGRect(x: 10, y: 100, width: 240, height: 150))
        #expect(port.toScreen(CGPoint(x: 130, y: 175)) == CGPoint(x: 720, y: 150))
        #expect(port.toView(CGPoint(x: 720, y: 150)) == CGPoint(x: 130, y: 175))
        #expect(port.clamped(port.toScreen(.zero)) == CGPoint(x: 480, y: 0))
        #expect(port.clamped(CGPoint(x: 2000, y: 400)) == CGPoint(x: 959, y: 299))
        #expect(port.framebufferFrame(CGSize(width: 960, height: 300)) == CGRect(x: -240, y: 0, width: 480, height: 150))
        #expect(port.pixelsPerPoint == 2)
        // All of it: the plain mapping, the picture exactly in its rect.
        let all = VNCViewport(visible: CGRect(x: 0, y: 0, width: 960, height: 300), rect: CGRect(x: 0, y: 50, width: 480, height: 150))
        #expect(all.toScreen(CGPoint(x: 240, y: 125)) == CGPoint(x: 480, y: 150))
        #expect(all.framebufferFrame(CGSize(width: 960, height: 300)) == CGRect(x: 0, y: 0, width: 480, height: 150))
    }

    /// The node's `displays` decode; a list the app cannot read costs only the display choice, not the machine.
    @Test func displaysDecode() throws {
        let json = #"{"enabled":true,"screen_sharing":true,"displays":[{"id":3,"x":0,"y":0,"width":2560,"height":1440,"main":true},{"id":1,"x":-1728,"y":0,"width":1728,"height":1117}]}"#
        let c = try JSONDecoder().decode(MachineComputer.self, from: Data(json.utf8))
        #expect(c.displays == [MachineDisplay(id: 3, x: 0, y: 0, width: 2560, height: 1440, main: true),
                               MachineDisplay(id: 1, x: -1728, y: 0, width: 1728, height: 1117)])
        let odd = try JSONDecoder().decode(MachineBrowser.self, from: Data(#"{"enabled":true,"screen_sharing":true,"displays":"two"}"#.utf8))
        #expect(odd.screenSharing && odd.displays.isEmpty)
        #expect(try JSONDecoder().decode(MachineComputer.self, from: Data(#"{"enabled":true}"#.utf8)).displays.isEmpty, "an older node")
    }
}
