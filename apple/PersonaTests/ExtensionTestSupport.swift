import Foundation
@testable import Persona
import PersonaWire

/// A component's own fields, to look at in the tests.
extension ExtensionComponent {
    var progress: ExtensionProgressComponent? { if case .progress(let c) = self { c } else { nil } }
    var row: ExtensionRowComponent? { if case .row(let c) = self { c } else { nil } }
    var button: ExtensionButtonComponent? { if case .button(let c) = self { c } else { nil } }
    var field: ExtensionFieldComponent? { if case .field(let c) = self { c } else { nil } }
    var chart: ExtensionChartComponent? { if case .chart(let c) = self { c } else { nil } }
    var statGrid: ExtensionStatGridComponent? { if case .statGrid(let c) = self { c } else { nil } }
    var segmented: ExtensionSegmentedComponent? { if case .segmented(let c) = self { c } else { nil } }
    var metric: ExtensionMetricComponent? { if case .metric(let c) = self { c } else { nil } }
    /// A row's or a button's action.
    var action: ExtensionAction? { row?.action ?? button?.action }
}
