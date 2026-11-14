import Foundation
import Testing
@testable import Persona
import PersonaWire

/// UI version 6's tiles on iOS (#827): the showcase's grids and carousels read whole, with what a tile can hold
/// (schema/extension/examples/showcase/grid.json, carousel.json, home-demo.json).
@MainActor
struct ExtensionTilesTests {
    private static let showcase = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples/showcase")

    private static func items(_ name: String) throws -> [ExtensionComponent] {
        try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: showcase.appending(path: name))).sections.flatMap(\.items)
    }

    @Test func gridsAndCarouselsAreReadWhole() throws {
        let grids = try Self.items("grid.json").compactMap { if case .grid(let g) = $0 { g } else { nil } }
        #expect(grids.count == 3 && grids.allSatisfy { !$0.tiles.isEmpty })
        let carousels = try Self.items("carousel.json").compactMap { if case .carousel(let c) = $0 { c } else { nil } }
        #expect(Set(carousels.map { $0.width ?? ExtensionTileWidth.regular }) == Set(ExtensionTileWidth.all))
        let tiles = (grids.flatMap(\.tiles) + carousels.flatMap(\.tiles))
        #expect(tiles.contains { $0.active == true } && tiles.contains { $0.icon != nil })
    }

    @Test func aTilesSwitchIsAControlThisAppDraws() throws {
        let tiles = try Self.items("home-demo.json").flatMap { c -> [ExtensionTile] in
            switch c {
            case .grid(let g): g.tiles
            case .carousel(let c): c.tiles
            default: []
            }
        }
        for c in tiles.compactMap(\.control) {
            #expect(c.kind == ExtensionRowControlKind.switch && c.drawable, "a tile's control is a switch: \(c.name)")
        }
    }
}
