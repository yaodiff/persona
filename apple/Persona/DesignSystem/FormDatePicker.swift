import SwiftUI

/// A form's date row: a date, or a date and a time, with its label (#6108): the system's compact picker beside its
/// label. The interface stops at xxxLarge (docs/design/DESIGN_SYSTEM.md, "Dynamic Type"), so the row never needs the
/// accessibility-size wheel layout. `identifier` is the picker's accessibility identifier.
struct FormDatePicker: View {
    private let title: LocalizedStringKey
    @Binding private var selection: Date
    private let range: PartialRangeFrom<Date>?
    private let components: DatePickerComponents
    private let identifier: String

    init(_ title: LocalizedStringKey, selection: Binding<Date>, in range: PartialRangeFrom<Date>? = nil,
         displayedComponents: DatePickerComponents, identifier: String) {
        self.title = title
        _selection = selection
        self.range = range
        self.components = displayedComponents
        self.identifier = identifier
    }

    var body: some View {
        picker.accessibilityIdentifier(identifier)
    }

    @ViewBuilder private var picker: some View {
        if let range {
            DatePicker(title, selection: $selection, in: range, displayedComponents: components)
        } else {
            DatePicker(title, selection: $selection, displayedComponents: components)
        }
    }
}

extension View {
    /// A form sheet's detents when it holds a `FormDatePicker`.
    func formSheetDetents(_ detents: Set<PresentationDetent>) -> some View {
        presentationDetents(detents)
    }
}
