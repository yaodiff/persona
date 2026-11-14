import SwiftParser
import SwiftSyntax

// What one file says, in a form that needs no syntax tree afterwards: where each blocking call is, the declarations and
// closures around it (frames), every call and name reference (to find who calls a `nonisolated` function), and the
// types, extensions and protocols it declares.

struct Frame: Sendable {
    var parent: Int
    var kind: Kind
    var line: Int

    enum Kind: Sendable {
        case file
        case type(Int)
        case ext(Int)
        case function(FunctionInfo)
        case property(PropertyInfo)
        case closure(ClosureInfo)
        /// The value of `async let`: it runs in a child task.
        case asyncLet
        /// A parameter's default value: it is evaluated where the function is called.
        case defaultArgument
    }
}

struct Parameter: Sendable {
    var label: String?
    var name: String
    var hasDefault: Bool
    var variadic: Bool
    /// A closure parameter that may outlive the call (`@escaping`), or one the tool cannot rule out as such.
    var escaping: Bool
    var mainActorClosure: Bool
    var isFunctionType: Bool
}

struct FunctionInfo: Sendable {
    enum Kind: Sendable { case function, initializer, deinitializer, subscripted }
    var kind: Kind
    var name: String
    var parameters: [Parameter]
    var isAsync: Bool
    var isStatic: Bool
    var attributes: Set<String>
    var modifiers: Set<String>
    var hasIsolatedParameter: Bool
    var isOperator: Bool
    /// The base name of the declared result type; `Void` when there is none, nil when it is not a name.
    var returns: String?
}

struct PropertyInfo: Sendable {
    var names: [String]
    var isStatic: Bool
    var attributes: Set<String>
    var modifiers: Set<String>
    /// Has a getter or observers: its code runs when the property is used.
    var isComputed: Bool
    var isLazy: Bool
    /// For each name, how many arguments its value takes when its declared type is a function type.
    var arities: [Int?]
}

struct ClosureInfo: Sendable {
    enum Role: Sendable {
        /// `{ … }()`
        case immediate
        /// An argument of a call: the callee as a dotted path (`Task.detached`, `DispatchQueue.global().async`), the
        /// argument's label, and the callee's base name and labels for finding a local declaration.
        case argument(callee: [String], receiver: Receiver, label: String?, trailingIndex: Int?, argumentIndex: Int?)
        /// `x.name = { … }`, `name = { … }`: kept in a property or a variable.
        case assigned(name: String, receiver: Receiver)
        /// Kept in a variable, returned, or in any place the tool does not read.
        case stored
    }
    var role: Role
    var attributes: Set<String>
    /// Written in the sequence of a `for await`: an argument of an async sequence's operator, which keeps it.
    var inAsyncSequence = false
}

indirect enum Receiver: Sendable, Hashable {
    /// `f()`
    case none
    /// `self.f()`
    case selfInstance
    /// `Self.f()`
    case selfType
    /// `T.f()`: an identifier that starts with a capital letter, the last one of a dotted path.
    case type(String)
    /// `.f()`
    case implicit
    /// `super.f()`
    case superInstance
    /// `type(of: x).f()`, `T.self.f()`
    case metatype
    /// `x.f()`, `x?.f()`: a variable, a parameter or a property in scope, by name.
    case variable(String)
    /// `<base>.p.f()`: the property `p` of another receiver (`self.p`, `T.shared`, `x.p`).
    case member(String, of: Receiver)
    /// `T(…).f()`
    case construct(String)
    /// `<base>.g(…).f()`, `g(…).f()`: what calling `g` returns.
    case result(String, of: Receiver)
    /// Any other expression: a value the lint does not know the type of.
    case other
}

/// A name a file binds, in the frame it is bound in, with the type the binding states (nil when it states none).
struct Binding: Sendable {
    var frame: Int
    var type: String?
    /// When no type is stated: the expression the value comes from (`let store = Store.shared`), to be typed through
    /// the declarations it names.
    var from: Receiver?
}

/// A stored or computed property of a type, or a global variable, with the type its declaration states.
struct PropertyType: Sendable {
    var name: String
    var owner: String?
    /// The base name of the declared type (`Store` for `Store?`, `[Store]` is `Array`); nil when the declaration does
    /// not say.
    var type: String?
    /// When no type is stated: the expression the value comes from, read in `frame`.
    var from: Receiver?
    var frame: Int
}

struct CallSite: Sendable {
    var name: String
    var labels: [String?]
    var trailingClosures: Int
    var receiver: Receiver
    var frame: Int
    var line: Int
    /// The call's result is the base of a further member access: `.make(x).name`.
    var chained: Bool
}

struct TypeAlias: Sendable {
    var name: String
    /// The base name of the type it stands for, nil when that is not a name.
    var target: String?
}

enum Indirect: Sendable { case no, keyPath, selector }

struct NameReference: Sendable {
    var name: String
    var receiver: Receiver
    var frame: Int
    var line: Int
    /// Named in a key path (`\.name`) or in `#selector` / `#keyPath`: used later by something the lint cannot follow.
    var indirect: Indirect
    /// The base of a further member access, a subscript, an operand or a condition: a value is read here, a function
    /// is not passed on.
    var readsValue: Bool
    /// An argument of a call: the callee's path (`["urls", "map"]`).
    var argumentOf: [String]?
    /// What a function returns (`return x.name`, or its whole body): `frame` is that function.
    var returned = false
}

struct TypeInfo: Sendable {
    enum Kind: String, Sendable { case `class`, `struct`, `enum`, `actor`, `protocol` }
    var kind: Kind
    var name: String
    /// `Outer.Inner` through the types (and extensions) it is declared in.
    var qualified: String
    var attributes: Set<String>
    var modifiers: Set<String>
    var inherits: [String]
    var frame: Int
    /// A protocol's requirements, by name (`init` for initializers).
    var requirements: Set<String>
    /// An actor that names its own executor may run on the main thread.
    var declaresExecutor: Bool
    var isLocal: Bool
}

struct ExtensionInfo: Sendable {
    var extended: String
    var attributes: Set<String>
    var modifiers: Set<String>
    var inherits: [String]
    var frame: Int
}

struct RawFinding: Sendable {
    var rule: String
    var line: Int
    var column: Int
    var frame: Int
}

struct FileFacts: Sendable {
    var path: String
    var frames: [Frame] = []
    var findings: [RawFinding] = []
    var calls: [CallSite] = []
    var references: [NameReference] = []
    var types: [TypeInfo] = []
    var extensions: [ExtensionInfo] = []
    var typealiases: [TypeAlias] = []
    /// `let name = DispatchQueue(label: …)` without a target: a queue of its own, never the main queue. false when
    /// another declaration of the name in the file is anything else.
    var privateQueues: [String: Bool] = [:]
    /// Functions that keep a closure parameter (`@escaping`), by name.
    var escapingFunctions: Set<String> = []
    /// Every name the file binds (variables, parameters, pattern and closure bindings), each in its frame.
    var bindings: [String: [Binding]] = [:]
    /// Properties of types and global variables.
    var properties: [PropertyType] = []
    /// Types whose metatype is used as a value: `T.self`.
    var metatypes: Set<String> = []
    /// Generic parameters and associated types: names that stand for any type.
    var typeVariables: Set<String> = []
    /// The modules the file imports.
    var imports: Set<String> = []
    var hasParseError = false

    init(path: String, source: String) {
        self.path = path
        let tree = Parser.parse(source: source)
        hasParseError = tree.hasError
        let collector = Collector(tree: tree, path: path)
        collector.walk(tree)
        frames = collector.frames
        findings = collector.findings
        calls = collector.calls
        references = collector.references
        types = collector.types
        extensions = collector.extensions
        typealiases = collector.typealiases
        privateQueues = collector.privateQueues
        escapingFunctions = collector.escapingFunctions
        bindings = collector.bindings
        properties = collector.properties
        metatypes = collector.metatypes
        typeVariables = collector.typeVariables
        imports = collector.imports
    }
}

/// The blocking calls: the five patterns that scripts/test/test_apple_perf_patterns.py counted wherever they were
/// written until this lint took them over, matched on the syntax tree instead of by regex.
enum Rules {
    static let all = ["photos-fetch", "keychain-read", "file-read", "directory-listing", "image-decode"]

    static func match(_ call: FunctionCallExprSyntax) -> String? {
        let first = call.arguments.first?.label?.text
        let callee = Syntax(call.calledExpression)
        if let member = callee.as(MemberAccessExprSyntax.self) {
            let name = member.declName.baseName.text
            let base = member.base.flatMap(lastName)
            if let base, ["PHAsset", "PHAssetCollection", "PHCollectionList"].contains(base), name.hasPrefix("fetch") {
                return "photos-fetch"
            }
            if ["contentsOfDirectory", "subpathsOfDirectory", "enumerator"].contains(name), first == "at" || first == "atPath" {
                return "directory-listing"
            }
            if name == "SecItemCopyMatching" { return "keychain-read" }
            // `Data.init(contentsOf:)`, `Foundation.Data(contentsOf:)`
            let type = name == "init" ? base : name
            return type.flatMap { construction($0, first) }
        }
        if let reference = callee.as(DeclReferenceExprSyntax.self) {
            let name = reference.baseName.text
            if name == "SecItemCopyMatching" { return "keychain-read" }
            return construction(name, first)
        }
        return nil
    }

    private static func construction(_ type: String, _ first: String?) -> String? {
        if ["Data", "String", "NSData", "NSString"].contains(type), first == "contentsOf" || first == "contentsOfFile" {
            return "file-read"
        }
        if ["UIImage", "NSImage"].contains(type), first == "data" || first == "contentsOfFile" { return "image-decode" }
        return nil
    }

    /// `Data.init(contentsOf:)` named without being called: `urls.map(Data.init(contentsOf:))`.
    static func match(unapplied member: MemberAccessExprSyntax) -> String? {
        guard member.declName.baseName.text == "init", let base = member.base.flatMap(lastName),
              let first = member.declName.argumentNames?.arguments.first?.name.text else { return nil }
        return construction(base, first)
    }
}

/// The last identifier of `A`, `A.B`, `A<T>.B`; nil for anything that is not a dotted path of names.
func lastName(_ expr: ExprSyntax) -> String? {
    if let reference = expr.as(DeclReferenceExprSyntax.self) { return reference.baseName.text }
    if let member = expr.as(MemberAccessExprSyntax.self) { return member.declName.baseName.text }
    if let generic = expr.as(GenericSpecializationExprSyntax.self) { return lastName(generic.expression) }
    return nil
}

/// A callee as path components: `Task.detached` → ["Task", "detached"]; a call in the path keeps `()`:
/// `DispatchQueue.global(qos: .utility).async` → ["DispatchQueue", "global()", "async"]. nil when a part is not a name.
func calleePath(_ expr: ExprSyntax) -> [String]? {
    if let reference = expr.as(DeclReferenceExprSyntax.self) { return [reference.baseName.text] }
    if let generic = expr.as(GenericSpecializationExprSyntax.self) { return calleePath(generic.expression) }
    if let member = expr.as(MemberAccessExprSyntax.self) {
        guard let base = member.base else { return ["", member.declName.baseName.text] }
        guard let head = calleePath(base) else { return nil }
        return head + [member.declName.baseName.text]
    }
    if let call = expr.as(FunctionCallExprSyntax.self) {
        guard var head = calleePath(call.calledExpression), let last = head.popLast() else { return nil }
        head.append(last + "()")
        return head
    }
    if let optional = expr.as(OptionalChainingExprSyntax.self) { return calleePath(optional.expression) }
    if let forced = expr.as(ForceUnwrapExprSyntax.self) { return calleePath(forced.expression) }
    return nil
}

/// `calleePath` for the call a closure is handed to, where what the call is made on need not be a name:
/// `[a, b].forEach`, `(a + b).map`, `groups[key]?.forEach`. The part that is not a name becomes "(value)", which
/// matches none of the forms the lint knows by their path; `lazy` anywhere in it is kept, since a lazy sequence
/// keeps its closure.
func closureCalleePath(_ expr: ExprSyntax) -> [String]? {
    if let path = calleePath(expr) { return path }
    var callee = expr
    while true {
        if let optional = callee.as(OptionalChainingExprSyntax.self) { callee = optional.expression }
        else if let forced = callee.as(ForceUnwrapExprSyntax.self) { callee = forced.expression }
        else if let generic = callee.as(GenericSpecializationExprSyntax.self) { callee = generic.expression }
        else { break }
    }
    guard let member = callee.as(MemberAccessExprSyntax.self), let base = member.base else { return nil }
    let lazy = base.tokens(viewMode: .sourceAccurate).contains { $0.text == "lazy" }
    return [lazy ? "lazy" : "(value)", member.declName.baseName.text]
}

private final class Collector: SyntaxVisitor {
    let converter: SourceLocationConverter
    var frames: [Frame] = [Frame(parent: -1, kind: .file, line: 1)]
    var stack: [Int] = [0]
    var findings: [RawFinding] = []
    var calls: [CallSite] = []
    var references: [NameReference] = []
    var types: [TypeInfo] = []
    var extensions: [ExtensionInfo] = []
    var typealiases: [TypeAlias] = []
    var privateQueues: [String: Bool] = [:]
    var imports: Set<String> = []
    var escapingFunctions: Set<String> = []
    var bindings: [String: [Binding]] = [:]
    var properties: [PropertyType] = []
    var metatypes: Set<String> = []
    var typeVariables: Set<String> = []
    /// Names of the types and extensions around the current node, for qualified names.
    var typeNames: [String] = []
    /// How deep in function bodies and closures the current node is: a type declared there is local.
    var bodyDepth = 0

    init(tree: SourceFileSyntax, path: String) {
        converter = SourceLocationConverter(fileName: path, tree: tree)
        super.init(viewMode: .sourceAccurate)
    }

    var current: Int { stack[stack.count - 1] }

    func line(_ node: some SyntaxProtocol) -> Int {
        node.startLocation(converter: converter).line
    }

    func push(_ kind: Frame.Kind, at node: some SyntaxProtocol) {
        frames.append(Frame(parent: current, kind: kind, line: line(node)))
        stack.append(frames.count - 1)
    }

    func pop() { stack.removeLast() }

    // MARK: Declared types

    func bind(_ name: String, _ type: String?, from: Receiver? = nil) {
        bindings[name, default: []].append(Binding(frame: current, type: type == "Self" ? typeNames.last : type,
                                                   from: type == nil ? from : nil))
    }

    /// What an initial value says about its type when it is neither a construction nor a literal: the variable,
    /// property or call it is.
    func source(_ expr: ExprSyntax) -> Receiver? {
        let receiver = receiver(base: expr)
        switch receiver {
        case .variable, .member, .result: return receiver
        default: return nil
        }
    }

    /// Parameters are bound in the function's own frame, which opens after they are read.
    var parameterBindings: [(String, String?)] = []

    /// The base name of a written type: what a member of a value of this type is looked up in.
    func declaredType(_ type: TypeSyntax) -> String? {
        if let optional = type.as(OptionalTypeSyntax.self) { return declaredType(optional.wrappedType) }
        if let optional = type.as(ImplicitlyUnwrappedOptionalTypeSyntax.self) { return declaredType(optional.wrappedType) }
        if let attributed = type.as(AttributedTypeSyntax.self) { return declaredType(attributed.baseType) }
        if let some = type.as(SomeOrAnyTypeSyntax.self) { return declaredType(some.constraint) }
        if type.is(ArrayTypeSyntax.self) { return "Array" }
        if type.is(DictionaryTypeSyntax.self) { return "Dictionary" }
        if type.is(MetatypeTypeSyntax.self) { return "(metatype)" }
        if type.is(FunctionTypeSyntax.self) { return "(function)" }
        if let tuple = type.as(TupleTypeSyntax.self) {
            if tuple.elements.count == 1, let only = tuple.elements.first { return declaredType(only.type) }
            return "(tuple)"
        }
        if let identifier = type.as(IdentifierTypeSyntax.self) {
            if identifier.name.text == "Optional", let argument = identifier.genericArgumentClause?.arguments.first,
               case .type(let wrapped) = argument.argument {
                return declaredType(wrapped)
            }
            return identifier.name.text
        }
        if let member = type.as(MemberTypeSyntax.self) { return member.name.text }
        return nil
    }

    /// The type an expression certainly has: a construction `T(…)` or a literal. nil for everything else.
    func valueType(_ expr: ExprSyntax) -> String? {
        if let tried = expr.as(TryExprSyntax.self) { return valueType(tried.expression) }
        if let awaited = expr.as(AwaitExprSyntax.self) { return valueType(awaited.expression) }
        if let call = expr.as(FunctionCallExprSyntax.self) { return constructedType(call) }
        if let member = expr.as(MemberAccessExprSyntax.self), member.declName.baseName.text == "self", member.base != nil { return "(metatype)" }
        if expr.is(StringLiteralExprSyntax.self) { return "String" }
        if expr.is(IntegerLiteralExprSyntax.self) { return "Int" }
        if expr.is(FloatLiteralExprSyntax.self) { return "Double" }
        if expr.is(BooleanLiteralExprSyntax.self) { return "Bool" }
        if expr.is(ArrayExprSyntax.self) { return "Array" }
        if expr.is(DictionaryExprSyntax.self) { return "Dictionary" }
        if expr.is(ClosureExprSyntax.self) { return "(function)" }
        return nil
    }

    /// `T(…)`, `Outer.T(…)`, `T<U>(…)`, `T.init(…)`: the type's name; `type(of: x)`: a metatype.
    func constructedType(_ call: FunctionCallExprSyntax) -> String? {
        var callee = call.calledExpression
        if let generic = callee.as(GenericSpecializationExprSyntax.self) { callee = generic.expression }
        if let reference = callee.as(DeclReferenceExprSyntax.self) {
            let name = reference.baseName.text
            if name == "type", call.arguments.first?.label?.text == "of" { return "(metatype)" }
            if name == "Self" { return typeNames.last }
            return name.first?.isUppercase == true ? name : nil
        }
        if let member = callee.as(MemberAccessExprSyntax.self), let base = member.base, let path = calleePath(base),
           path.allSatisfy({ $0.first?.isUppercase == true && !$0.hasSuffix("()") }) {
            let name = member.declName.baseName.text
            if name == "init" { return path.last == "Self" ? typeNames.last : path.last }
            if name.first?.isUppercase == true { return name }
        }
        return nil
    }

    override func visit(_ node: GenericParameterSyntax) -> SyntaxVisitorContinueKind {
        typeVariables.insert(node.name.text)
        return .visitChildren
    }

    override func visit(_ node: AssociatedTypeDeclSyntax) -> SyntaxVisitorContinueKind {
        typeVariables.insert(node.name.text)
        return .visitChildren
    }

    override func visit(_ node: PrimaryAssociatedTypeSyntax) -> SyntaxVisitorContinueKind {
        typeVariables.insert(node.name.text)
        return .visitChildren
    }

    /// A name bound by a pattern that states no type: `for x in`, `case .a(let x)`, `let (a, b) =`.
    override func visit(_ node: IdentifierPatternSyntax) -> SyntaxVisitorContinueKind {
        guard let parent = node.parent else { return .visitChildren }
        if parent.is(PatternBindingSyntax.self) || parent.is(OptionalBindingConditionSyntax.self) { return .visitChildren }
        bind(node.identifier.text, nil)
        return .visitChildren
    }

    override func visit(_ node: OptionalBindingConditionSyntax) -> SyntaxVisitorContinueKind {
        guard let identifier = node.pattern.as(IdentifierPatternSyntax.self) else { return .visitChildren }
        // `if let x` alone rebinds the same value under the same name: nothing new about its type.
        let name = identifier.identifier.text
        if let annotation = node.typeAnnotation { bind(name, declaredType(annotation.type)) }
        else if let initializer = node.initializer {
            // `if let x = x`, `if let x = self.x`: the same again.
            if let reference = initializer.value.as(DeclReferenceExprSyntax.self), reference.baseName.text == name { return .visitChildren }
            if let member = initializer.value.as(MemberAccessExprSyntax.self), member.declName.baseName.text == name,
               member.base?.as(DeclReferenceExprSyntax.self)?.baseName.text == "self" { return .visitChildren }
            bind(name, valueType(initializer.value), from: source(initializer.value))
        }
        return .visitChildren
    }

    override func visit(_ node: ClosureShorthandParameterSyntax) -> SyntaxVisitorContinueKind {
        bind(node.name.text, nil)
        return .visitChildren
    }

    override func visit(_ node: ClosureParameterSyntax) -> SyntaxVisitorContinueKind {
        bind(node.secondName?.text ?? node.firstName.text, node.type.flatMap(declaredType))
        return .visitChildren
    }

    // MARK: Attributes and modifiers

    func names(_ attributes: AttributeListSyntax) -> Set<String> {
        var out = Set<String>()
        for element in attributes {
            guard let attribute = element.as(AttributeSyntax.self) else { continue }
            out.insert(attribute.attributeName.trimmedDescription)
        }
        return out
    }

    func names(_ modifiers: DeclModifierListSyntax) -> Set<String> {
        var out = Set<String>()
        for modifier in modifiers {
            out.insert(modifier.name.text)
            if let detail = modifier.detail { out.insert(modifier.name.text + "(" + detail.detail.text + ")") }
        }
        return out
    }

    func inherited(_ clause: InheritanceClauseSyntax?) -> [String] {
        guard let clause else { return [] }
        return clause.inheritedTypes.map { typeName($0.type) }
    }

    /// A type as the name it is looked up by: no attributes (`@unchecked`, `@MainActor`), no `any` / `some`, no generic
    /// arguments, no module qualifier the standard ones use.
    func typeName(_ type: TypeSyntax) -> String {
        if let attributed = type.as(AttributedTypeSyntax.self) { return typeName(attributed.baseType) }
        if let some = type.as(SomeOrAnyTypeSyntax.self) { return typeName(some.constraint) }
        if let identifier = type.as(IdentifierTypeSyntax.self) { return identifier.name.text }
        if let member = type.as(MemberTypeSyntax.self) {
            let base = typeName(member.baseType)
            return ["Swift", "Foundation", "_Concurrency"].contains(base) ? member.name.text : base + "." + member.name.text
        }
        return type.trimmedDescription
    }

    // MARK: Types and extensions

    func enterType(_ kind: TypeInfo.Kind, name: TokenSyntax, attributes: AttributeListSyntax, modifiers: DeclModifierListSyntax,
                   inheritance: InheritanceClauseSyntax?, members: MemberBlockSyntax, node: some SyntaxProtocol) {
        var requirements = Set<String>()
        var declaresExecutor = false
        for member in members.members {
            let decl = member.decl
            if let function = decl.as(FunctionDeclSyntax.self) { requirements.insert(function.name.text) }
            if decl.is(InitializerDeclSyntax.self) { requirements.insert("init") }
            if decl.is(SubscriptDeclSyntax.self) { requirements.insert("subscript") }
            if let variable = decl.as(VariableDeclSyntax.self) {
                for binding in variable.bindings {
                    guard let identifier = binding.pattern.as(IdentifierPatternSyntax.self) else { continue }
                    requirements.insert(identifier.identifier.text)
                    if identifier.identifier.text == "unownedExecutor" { declaresExecutor = true }
                }
            }
        }
        let qualified = (typeNames + [name.text]).joined(separator: ".")
        types.append(TypeInfo(kind: kind, name: name.text, qualified: qualified, attributes: names(attributes),
                              modifiers: names(modifiers), inherits: inherited(inheritance), frame: frames.count,
                              requirements: kind == .protocol ? requirements : [], declaresExecutor: declaresExecutor,
                              isLocal: bodyDepth > 0))
        push(.type(types.count - 1), at: node)
        typeNames.append(name.text)
    }

    func leaveType() {
        typeNames.removeLast()
        pop()
    }

    override func visit(_ node: ClassDeclSyntax) -> SyntaxVisitorContinueKind {
        enterType(.class, name: node.name, attributes: node.attributes, modifiers: node.modifiers,
                  inheritance: node.inheritanceClause, members: node.memberBlock, node: node)
        return .visitChildren
    }
    override func visitPost(_ node: ClassDeclSyntax) { leaveType() }

    override func visit(_ node: StructDeclSyntax) -> SyntaxVisitorContinueKind {
        enterType(.struct, name: node.name, attributes: node.attributes, modifiers: node.modifiers,
                  inheritance: node.inheritanceClause, members: node.memberBlock, node: node)
        return .visitChildren
    }
    override func visitPost(_ node: StructDeclSyntax) { leaveType() }

    override func visit(_ node: EnumDeclSyntax) -> SyntaxVisitorContinueKind {
        enterType(.enum, name: node.name, attributes: node.attributes, modifiers: node.modifiers,
                  inheritance: node.inheritanceClause, members: node.memberBlock, node: node)
        return .visitChildren
    }
    override func visitPost(_ node: EnumDeclSyntax) { leaveType() }

    override func visit(_ node: ActorDeclSyntax) -> SyntaxVisitorContinueKind {
        enterType(.actor, name: node.name, attributes: node.attributes, modifiers: node.modifiers,
                  inheritance: node.inheritanceClause, members: node.memberBlock, node: node)
        return .visitChildren
    }
    override func visitPost(_ node: ActorDeclSyntax) { leaveType() }

    override func visit(_ node: ProtocolDeclSyntax) -> SyntaxVisitorContinueKind {
        enterType(.protocol, name: node.name, attributes: node.attributes, modifiers: node.modifiers,
                  inheritance: node.inheritanceClause, members: node.memberBlock, node: node)
        return .visitChildren
    }
    override func visitPost(_ node: ProtocolDeclSyntax) { leaveType() }

    override func visit(_ node: ExtensionDeclSyntax) -> SyntaxVisitorContinueKind {
        let extended = typeName(node.extendedType)
        extensions.append(ExtensionInfo(extended: extended, attributes: names(node.attributes), modifiers: names(node.modifiers),
                                        inherits: inherited(node.inheritanceClause), frame: frames.count))
        push(.ext(extensions.count - 1), at: node)
        typeNames.append(extended)
        return .visitChildren
    }
    override func visitPost(_ node: ExtensionDeclSyntax) { leaveType() }

    override func visit(_ node: TypeAliasDeclSyntax) -> SyntaxVisitorContinueKind {
        typealiases.append(TypeAlias(name: node.name.text, target: node.genericParameterClause == nil ? declaredType(node.initializer.value) : nil))
        return .visitChildren
    }

    // MARK: Functions

    func parameters(_ clause: FunctionParameterClauseSyntax) -> [Parameter] {
        clause.parameters.map { parameter in
            let first = parameter.firstName.text
            let type = parameter.type
            let text = type.trimmedDescription
            let function = isFunctionType(type)
            parameterBindings.append((parameter.secondName?.text ?? first, parameter.ellipsis != nil ? "Array" : declaredType(type)))
            return Parameter(label: first == "_" ? nil : first, name: parameter.secondName?.text ?? first,
                             hasDefault: parameter.defaultValue != nil, variadic: parameter.ellipsis != nil,
                             escaping: text.contains("@escaping") || (!function && text.contains("->")),
                             mainActorClosure: text.contains("@MainActor"), isFunctionType: function || text.contains("->"))
        }
    }

    func isFunctionType(_ type: TypeSyntax) -> Bool {
        if type.is(FunctionTypeSyntax.self) { return true }
        if let attributed = type.as(AttributedTypeSyntax.self) { return isFunctionType(attributed.baseType) }
        return false
    }

    /// How many arguments a value of this type takes, when it is a function type (optional or attributed).
    func functionArity(_ type: TypeSyntax) -> Int? {
        if let function = type.as(FunctionTypeSyntax.self) { return function.parameters.count }
        if let attributed = type.as(AttributedTypeSyntax.self) { return functionArity(attributed.baseType) }
        if let optional = type.as(OptionalTypeSyntax.self) { return functionArity(optional.wrappedType) }
        if let optional = type.as(ImplicitlyUnwrappedOptionalTypeSyntax.self) { return functionArity(optional.wrappedType) }
        if let tuple = type.as(TupleTypeSyntax.self), tuple.elements.count == 1, let only = tuple.elements.first {
            return functionArity(only.type)
        }
        return nil
    }

    func isolatedParameter(_ clause: FunctionParameterClauseSyntax) -> Bool {
        clause.parameters.contains { parameter in
            let text = parameter.type.trimmedDescription
            return text.hasPrefix("isolated ") || text.contains(" isolated ") || parameter.defaultValue?.value.trimmedDescription == "#isolation"
        }
    }

    func enterFunction(_ info: FunctionInfo, node: some SyntaxProtocol) {
        if info.parameters.contains(where: \.escaping) { escapingFunctions.insert(info.name) }
        push(.function(info), at: node)
        for (name, type) in parameterBindings { bind(name, type) }
        parameterBindings = []
        bodyDepth += 1
    }

    func leaveFunction() {
        bodyDepth -= 1
        pop()
    }

    func isStatic(_ modifiers: Set<String>) -> Bool { modifiers.contains("static") || modifiers.contains("class") }

    override func visit(_ node: FunctionDeclSyntax) -> SyntaxVisitorContinueKind {
        let modifiers = names(node.modifiers)
        let isOperator: Bool
        if case .binaryOperator = node.name.tokenKind { isOperator = true }
        else if case .prefixOperator = node.name.tokenKind { isOperator = true }
        else if case .postfixOperator = node.name.tokenKind { isOperator = true }
        else { isOperator = false }
        enterFunction(FunctionInfo(kind: .function, name: node.name.text, parameters: parameters(node.signature.parameterClause),
                                   isAsync: node.signature.effectSpecifiers?.asyncSpecifier != nil, isStatic: isStatic(modifiers),
                                   attributes: names(node.attributes), modifiers: modifiers,
                                   hasIsolatedParameter: isolatedParameter(node.signature.parameterClause), isOperator: isOperator,
                                   returns: node.signature.returnClause.map { declaredType($0.type) } ?? "Void"),
                      node: node)
        return .visitChildren
    }
    override func visitPost(_ node: FunctionDeclSyntax) { leaveFunction() }

    override func visit(_ node: InitializerDeclSyntax) -> SyntaxVisitorContinueKind {
        let modifiers = names(node.modifiers)
        enterFunction(FunctionInfo(kind: .initializer, name: "init", parameters: parameters(node.signature.parameterClause),
                                   isAsync: node.signature.effectSpecifiers?.asyncSpecifier != nil, isStatic: false,
                                   attributes: names(node.attributes), modifiers: modifiers,
                                   hasIsolatedParameter: isolatedParameter(node.signature.parameterClause), isOperator: false,
                                   returns: nil),
                      node: node)
        return .visitChildren
    }
    override func visitPost(_ node: InitializerDeclSyntax) { leaveFunction() }

    override func visit(_ node: DeinitializerDeclSyntax) -> SyntaxVisitorContinueKind {
        enterFunction(FunctionInfo(kind: .deinitializer, name: "deinit", parameters: [], isAsync: false, isStatic: false,
                                   attributes: names(node.attributes), modifiers: names(node.modifiers),
                                   hasIsolatedParameter: false, isOperator: false, returns: "Void"), node: node)
        return .visitChildren
    }
    override func visitPost(_ node: DeinitializerDeclSyntax) { leaveFunction() }

    override func visit(_ node: SubscriptDeclSyntax) -> SyntaxVisitorContinueKind {
        let modifiers = names(node.modifiers)
        enterFunction(FunctionInfo(kind: .subscripted, name: "subscript", parameters: parameters(node.parameterClause),
                                   isAsync: false, isStatic: isStatic(modifiers), attributes: names(node.attributes),
                                   modifiers: modifiers, hasIsolatedParameter: false, isOperator: false,
                                   returns: declaredType(node.returnClause.type)), node: node)
        return .visitChildren
    }
    override func visitPost(_ node: SubscriptDeclSyntax) { leaveFunction() }

    /// A parameter's default value runs at the call site, whatever the function is isolated to.
    override func visit(_ node: FunctionParameterSyntax) -> SyntaxVisitorContinueKind {
        if node.defaultValue != nil { push(.defaultArgument, at: node) }
        return .visitChildren
    }
    override func visitPost(_ node: FunctionParameterSyntax) {
        if node.defaultValue != nil { pop() }
    }

    // MARK: Properties

    /// The frame a variable declaration opens, if any: a member or a global gets one (its value or its accessors run
    /// in the isolation the declaration has); a local variable runs with the code around it, except `async let`.
    var variableFrames: [Bool] = []

    override func visit(_ node: VariableDeclSyntax) -> SyntaxVisitorContinueKind {
        let modifiers = names(node.modifiers)
        let isLocal = bodyDepth > 0
        var names: [String] = []
        var arities: [Int?] = []
        var computed = false
        for binding in node.bindings {
            if binding.accessorBlock != nil { computed = true }
            noteQueue(binding, specifier: node.bindingSpecifier)
            guard let identifier = binding.pattern.as(IdentifierPatternSyntax.self) else { continue }
            names.append(identifier.identifier.text)
            arities.append(binding.typeAnnotation.flatMap { functionArity($0.type) })
            let type = binding.typeAnnotation.map { declaredType($0.type) } ?? binding.initializer.flatMap { valueType($0.value) }
            // `var x = x`: a copy of what the name already is.
            let copy = isLocal && binding.typeAnnotation == nil
                && binding.initializer?.value.as(DeclReferenceExprSyntax.self)?.baseName.text == identifier.identifier.text
            let from = type == nil ? binding.initializer.flatMap { source($0.value) } : nil
            if !copy { bind(identifier.identifier.text, type, from: from) }
            if !isLocal {
                properties.append(PropertyType(name: identifier.identifier.text, owner: typeNames.last,
                                               type: type == "Self" ? typeNames.last : type, from: from, frame: current))
            }
        }
        if isLocal {
            let asyncLet = modifiers.contains("async")
            if asyncLet { push(.asyncLet, at: node) }
            variableFrames.append(asyncLet)
            return .visitChildren
        }
        push(.property(PropertyInfo(names: names, isStatic: isStatic(modifiers), attributes: self.names(node.attributes),
                                    modifiers: modifiers, isComputed: computed, isLazy: modifiers.contains("lazy"),
                                    arities: arities)), at: node)
        bodyDepth += 1
        variableFrames.append(true)
        return .visitChildren
    }

    override func visitPost(_ node: VariableDeclSyntax) {
        guard variableFrames.removeLast() else { return }
        if case .property = frames[current].kind { bodyDepth -= 1 }
        pop()
    }

    func noteQueue(_ binding: PatternBindingSyntax, specifier: TokenSyntax) {
        guard let identifier = binding.pattern.as(IdentifierPatternSyntax.self) else { return }
        let name = identifier.identifier.text
        var isQueue = false
        if specifier.tokenKind == .keyword(.let), let call = binding.initializer?.value.as(FunctionCallExprSyntax.self),
           let path = calleePath(call.calledExpression), path == ["DispatchQueue"] || path == ["Dispatch", "DispatchQueue"],
           call.arguments.first?.label?.text == "label", !call.arguments.contains(where: { $0.label?.text == "target" }) {
            isQueue = true
        }
        privateQueues[name] = (privateQueues[name] ?? true) && isQueue
    }

    // MARK: Closures

    override func visit(_ node: ClosureExprSyntax) -> SyntaxVisitorContinueKind {
        push(.closure(ClosureInfo(role: role(of: node), attributes: node.signature.map { names($0.attributes) } ?? [],
                                  inAsyncSequence: isInAsyncSequence(node))), at: node)
        bodyDepth += 1
        return .visitChildren
    }

    override func visitPost(_ node: ClosureExprSyntax) {
        bodyDepth -= 1
        pop()
    }

    /// `for await x in stream.map({ … })`: the closure is part of the expression an asynchronous loop iterates.
    func isInAsyncSequence(_ closure: ClosureExprSyntax) -> Bool {
        var node = Syntax(closure)
        while let parent = node.parent {
            if let loop = parent.as(ForStmtSyntax.self) { return loop.awaitKeyword != nil && Syntax(loop.sequence).id == node.id }
            if parent.is(CodeBlockItemSyntax.self) || parent.is(ClosureExprSyntax.self) { return false }
            node = parent
        }
        return false
    }

    override func visit(_ node: ImportDeclSyntax) -> SyntaxVisitorContinueKind {
        if let module = node.path.first?.name.text { imports.insert(module) }
        return .skipChildren
    }

    func role(of closure: ClosureExprSyntax) -> ClosureInfo.Role {
        guard let parent = closure.parent else { return .stored }
        func receiver(_ call: FunctionCallExprSyntax) -> Receiver {
            if let member = bareCallee(call.calledExpression).as(MemberAccessExprSyntax.self) { return self.receiver(of: member) }
            return .none
        }
        if let call = parent.as(FunctionCallExprSyntax.self) {
            if call.calledExpression.id == closure.id { return .immediate }
            if call.trailingClosure?.id == closure.id, let path = closureCalleePath(call.calledExpression) {
                return .argument(callee: path, receiver: receiver(call), label: nil, trailingIndex: 0, argumentIndex: nil)
            }
            return .stored
        }
        if let element = parent.as(MultipleTrailingClosureElementSyntax.self),
           let list = element.parent?.as(MultipleTrailingClosureElementListSyntax.self),
           let call = list.parent?.as(FunctionCallExprSyntax.self), let path = closureCalleePath(call.calledExpression) {
            let index = list.firstIndex(where: { $0.id == element.id }).map { list.distance(from: list.startIndex, to: $0) } ?? 0
            return .argument(callee: path, receiver: receiver(call), label: element.label.text, trailingIndex: index + 1, argumentIndex: nil)
        }
        if let argument = parent.as(LabeledExprSyntax.self), let list = argument.parent?.as(LabeledExprListSyntax.self),
           let call = list.parent?.as(FunctionCallExprSyntax.self), let path = closureCalleePath(call.calledExpression) {
            let index = list.firstIndex(where: { $0.id == argument.id }).map { list.distance(from: list.startIndex, to: $0) }
            return .argument(callee: path, receiver: receiver(call), label: argument.label?.text, trailingIndex: nil, argumentIndex: index)
        }
        if let list = parent.as(ExprListSyntax.self), let sequence = list.parent?.as(SequenceExprSyntax.self) {
            let elements = Array(sequence.elements)
            if elements.count == 3, elements[1].is(AssignmentExprSyntax.self), elements[2].id == closure.id {
                if let member = elements[0].as(MemberAccessExprSyntax.self) {
                    return .assigned(name: member.declName.baseName.text, receiver: self.receiver(of: member))
                }
                if let reference = elements[0].as(DeclReferenceExprSyntax.self) {
                    return .assigned(name: reference.baseName.text, receiver: .none)
                }
            }
        }
        return .stored
    }

    // MARK: Calls and references

    func receiver(of member: MemberAccessExprSyntax) -> Receiver {
        guard let base = member.base else { return .implicit }
        return receiver(base: base)
    }

    /// What the expression before the dot is.
    func receiver(base expr: ExprSyntax) -> Receiver {
        var base = expr
        if base.is(SuperExprSyntax.self) { return .superInstance }
        // `x?.f()`, `x!.f()`: the same value.
        while true {
            if let optional = base.as(OptionalChainingExprSyntax.self) { base = optional.expression; continue }
            if let forced = base.as(ForceUnwrapExprSyntax.self) { base = forced.expression; continue }
            break
        }
        if let call = base.as(FunctionCallExprSyntax.self) {
            if let type = constructedType(call) { return type == "(metatype)" ? .metatype : .construct(type) }
            let callee = bareCallee(call.calledExpression)
            if let reference = callee.as(DeclReferenceExprSyntax.self) { return .result(reference.baseName.text, of: .none) }
            if let member = callee.as(MemberAccessExprSyntax.self) { return .result(member.declName.baseName.text, of: receiver(of: member)) }
            return .other
        }
        if let tried = base.as(TryExprSyntax.self) { return receiver(base: tried.expression) }
        if let awaited = base.as(AwaitExprSyntax.self) { return receiver(base: awaited.expression) }
        if let tuple = base.as(TupleExprSyntax.self), tuple.elements.count == 1, let only = tuple.elements.first, only.label == nil {
            return receiver(base: only.expression)
        }
        if let reference = base.as(DeclReferenceExprSyntax.self) {
            let text = reference.baseName.text
            if text == "self" { return .selfInstance }
            if text == "Self" { return .selfType }
            return text.first?.isUppercase == true ? .type(text) : .variable(text)
        }
        if let generic = base.as(GenericSpecializationExprSyntax.self), let name = lastName(generic.expression),
           name.first?.isUppercase == true {
            return .type(name)
        }
        if let inner = base.as(MemberAccessExprSyntax.self) {
            let name = inner.declName.baseName.text
            guard let before = inner.base else { return .other }   // `.shared.f()`: the type comes from the context
            if name == "self" {
                // `T.self.f()` is the metatype; `x.self` is `x`.
                if let type = lastName(before), type.first?.isUppercase == true { return .metatype }
                return receiver(base: before)
            }
            if name.first?.isUppercase == true, calleePath(base) != nil { return .type(name) }
            return .member(name, of: receiver(base: before))
        }
        return .other
    }

    /// The callee without `?`, `!` and generic arguments: `f?()`, `f!()`, `f<T>()`.
    func bareCallee(_ expr: ExprSyntax) -> ExprSyntax {
        var callee = expr
        while true {
            if let generic = callee.as(GenericSpecializationExprSyntax.self) { callee = generic.expression; continue }
            if let optional = callee.as(OptionalChainingExprSyntax.self) { callee = optional.expression; continue }
            if let forced = callee.as(ForceUnwrapExprSyntax.self) { callee = forced.expression; continue }
            return callee
        }
    }

    override func visit(_ node: FunctionCallExprSyntax) -> SyntaxVisitorContinueKind {
        if let rule = Rules.match(node) {
            let location = node.startLocation(converter: converter)
            findings.append(RawFinding(rule: rule, line: location.line, column: location.column, frame: current))
        }
        let labels = node.arguments.map { $0.label?.text }
        let trailing = (node.trailingClosure == nil ? 0 : 1) + node.additionalTrailingClosures.count
        let callee = bareCallee(node.calledExpression)
        var chained = false
        var outer = Syntax(node)
        while let parent = outer.parent {
            if parent.is(OptionalChainingExprSyntax.self) || parent.is(ForceUnwrapExprSyntax.self) { outer = parent; continue }
            if let member = parent.as(MemberAccessExprSyntax.self), member.base?.id == outer.id { chained = true }
            break
        }
        if let reference = callee.as(DeclReferenceExprSyntax.self) {
            calls.append(CallSite(name: reference.baseName.text, labels: labels, trailingClosures: trailing, receiver: .none,
                                  frame: current, line: line(node), chained: chained))
        } else if let member = callee.as(MemberAccessExprSyntax.self) {
            calls.append(CallSite(name: member.declName.baseName.text, labels: labels, trailingClosures: trailing,
                                  receiver: receiver(of: member), frame: current, line: line(node), chained: chained))
        }
        return .visitChildren
    }

    func isCallee(_ node: some SyntaxProtocol) -> Bool {
        var child = Syntax(node)
        while let parent = child.parent {
            if parent.is(GenericSpecializationExprSyntax.self) || parent.is(OptionalChainingExprSyntax.self)
                || parent.is(ForceUnwrapExprSyntax.self) {
                child = parent
                continue
            }
            if let call = parent.as(FunctionCallExprSyntax.self) { return call.calledExpression.id == child.id }
            return false
        }
        return false
    }

    /// The callee's path when the node is an argument of a call (not a trailing closure).
    func argumentOf(_ node: some SyntaxProtocol) -> [String]? {
        guard let labeled = node.parent?.as(LabeledExprSyntax.self), let list = labeled.parent?.as(LabeledExprListSyntax.self),
              let call = list.parent?.as(FunctionCallExprSyntax.self) else { return nil }
        return calleePath(bareCallee(call.calledExpression)) ?? ["(expression)"]
    }

    /// The reference only reads a value here: a function cannot be passed on from these places.
    func readsValue(_ node: some SyntaxProtocol) -> Bool {
        guard let parent = node.parent else { return false }
        if let member = parent.as(MemberAccessExprSyntax.self) { return member.base?.id == node.id }
        if let call = parent.as(SubscriptCallExprSyntax.self) { return call.calledExpression.id == node.id }
        if parent.is(OptionalChainingExprSyntax.self) || parent.is(ForceUnwrapExprSyntax.self) { return readsValue(parent) }
        if parent.is(ExpressionSegmentSyntax.self) || parent.is(ConditionElementSyntax.self) { return true }
        // `!x.name`, `-x.name`: a Bool or a number.
        if let prefix = parent.as(PrefixOperatorExprSyntax.self), ["!", "-", "~"].contains(prefix.operator.text) { return true }
        if let loop = parent.as(ForStmtSyntax.self) { return loop.sequence.id == node.id }
        if let list = parent.as(ExprListSyntax.self), let sequence = list.parent?.as(SequenceExprSyntax.self) {
            // What `=` assigns to is written, not passed on.
            let elements = Array(sequence.elements)
            if let at = elements.firstIndex(where: { $0.id == node.id }), at + 1 < elements.count,
               elements[at + 1].is(AssignmentExprSyntax.self) { return true }
            // `x ?? []`, `x ?? ""`: what a literal can stand in for is not a function.
            if elements.count == 3, elements[1].as(BinaryOperatorExprSyntax.self)?.operator.text == "??",
               let other = elements.first(where: { $0.id != node.id && !$0.is(BinaryOperatorExprSyntax.self) }),
               other.is(ArrayExprSyntax.self) || other.is(DictionaryExprSyntax.self) || other.is(StringLiteralExprSyntax.self)
                || other.is(IntegerLiteralExprSyntax.self) || other.is(FloatLiteralExprSyntax.self) || other.is(BooleanLiteralExprSyntax.self) {
                return true
            }
            // An operand of arithmetic, comparison or logic; not of `=`, `? :`, `as` or `??`, which pass a value on.
            return !sequence.elements.contains { element in
                if element.is(AssignmentExprSyntax.self) || element.is(UnresolvedTernaryExprSyntax.self)
                    || element.is(UnresolvedAsExprSyntax.self) { return true }
                return element.as(BinaryOperatorExprSyntax.self)?.operator.text == "??"
            }
        }
        if let labeled = parent.as(LabeledExprSyntax.self), labeled.parent?.parent?.is(StringLiteralExprSyntax.self) == true {
            return true
        }
        return false
    }

    /// The node is what the function around it returns: `return x.name`, or the only expression of its body.
    func isReturned(_ node: some SyntaxProtocol) -> Bool {
        guard case .function = frames[current].kind else { return false }
        var child = Syntax(node)
        while let parent = child.parent {
            if parent.is(OptionalChainingExprSyntax.self) || parent.is(ForceUnwrapExprSyntax.self) || parent.is(TryExprSyntax.self)
                || parent.is(AwaitExprSyntax.self) {
                child = parent
                continue
            }
            if parent.is(ReturnStmtSyntax.self) { return true }
            if let item = parent.as(CodeBlockItemSyntax.self), let list = item.parent?.as(CodeBlockItemListSyntax.self), list.count == 1,
               let block = list.parent?.as(CodeBlockSyntax.self), block.parent?.is(FunctionDeclSyntax.self) == true {
                return true
            }
            return false
        }
        return false
    }

    var selectorDepth = 0

    override func visit(_ node: MacroExpansionExprSyntax) -> SyntaxVisitorContinueKind {
        if node.macroName.text == "selector" || node.macroName.text == "keyPath" { selectorDepth += 1 }
        return .visitChildren
    }
    override func visitPost(_ node: MacroExpansionExprSyntax) {
        if node.macroName.text == "selector" || node.macroName.text == "keyPath" { selectorDepth -= 1 }
    }

    override func visit(_ node: KeyPathPropertyComponentSyntax) -> SyntaxVisitorContinueKind {
        references.append(NameReference(name: node.declName.baseName.text, receiver: .other, frame: current, line: line(node),
                                        indirect: .keyPath, readsValue: false, argumentOf: nil))
        return .skipChildren
    }

    override func visit(_ node: MemberAccessExprSyntax) -> SyntaxVisitorContinueKind {
        if node.declName.baseName.text == "self", let type = node.base.flatMap(lastName), type.first?.isUppercase == true {
            metatypes.insert(type == "Self" ? (typeNames.last ?? type) : type)
        }
        if let rule = Rules.match(unapplied: node), !isCallee(node) {
            let location = node.startLocation(converter: converter)
            findings.append(RawFinding(rule: rule, line: location.line, column: location.column, frame: current))
        }
        if !isCallee(node) {
            references.append(NameReference(name: node.declName.baseName.text, receiver: receiver(of: node), frame: current,
                                            line: line(node), indirect: selectorDepth > 0 ? .selector : .no, readsValue: readsValue(node),
                                            argumentOf: argumentOf(node), returned: isReturned(node)))
        }
        // The member's own name is recorded above; only the base is walked.
        if let base = node.base { walk(base) }
        return .skipChildren
    }

    override func visit(_ node: DeclReferenceExprSyntax) -> SyntaxVisitorContinueKind {
        if !isCallee(node) {
            references.append(NameReference(name: node.baseName.text, receiver: .none, frame: current, line: line(node),
                                            indirect: selectorDepth > 0 ? .selector : .no, readsValue: readsValue(node),
                                            argumentOf: argumentOf(node), returned: isReturned(node)))
        }
        return .visitChildren
    }
}
