import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

from android_compose_budget import CAPS, baseline_of, code_of, functions, main, regressions, scan, update_raises

PAGE = 'android/feature/demo/src/main/kotlin/demo/Page.kt'


def composable(name, lines=1, remembers=0, effects=0, head='@Composable\nfun'):
    body = ['    val a = remember { 1 }'] * remembers + ['    LaunchedEffect(Unit) { }'] * effects
    # The opening line and the closing brace count too.
    body += ['    Text("x")'] * max(0, lines - 2 - len(body))
    return f'{head} {name}() {{\n' + '\n'.join(body) + '\n}\n'


class CountTest(unittest.TestCase):
    def test_counts_code_lines_remembers_and_effects(self):
        source = '''
            @Composable
            internal fun Page(state: State, onBack: () -> Unit = {}) {
                // remember { the reason } and LaunchedEffect in a comment count for nothing
                val a = remember { mutableStateOf(0) }
                var b by rememberSaveable(key) { mutableStateOf("") }
                val c by rememberUpdatedState(onBack)
                val d = remember<List<Pair<String, Int>>>(key) { emptyList() }

                /* a block comment
                   over two lines */
                LaunchedEffect(key) { b = "remember { }" }
                DisposableEffect(Unit) { onDispose { } }
                SideEffect { }
                Text("LaunchedEffect(x) remember { }")
            }
        '''
        self.assertEqual([('Page', {'lines': 10, 'remembers': 4, 'effects': 3})], functions(source))

    def test_finds_annotated_extension_generic_and_expression_bodied_functions(self):
        source = '''
            @OptIn(ExperimentalMaterial3Api::class)
            @Composable
            private fun ColumnScope.Header(title: String) { Text(title) }
            @Composable @ReadOnlyComposable
            internal fun <T : Any> rememberThing(value: T): Thing<T> = remember(value) { Thing(value) }
            @Composable fun Sheet(content: @Composable () -> Unit) = content()
            @Composable
            fun Wrapped(): Unit = Column {
                val x = remember { 1 }
                Text("$x")
            }
        '''
        self.assertEqual(['Header', 'rememberThing', 'Sheet', 'Wrapped'], [name for name, _ in functions(source)])
        counts = dict(functions(source))
        self.assertEqual(1, counts['rememberThing']['remembers'])
        self.assertEqual({'lines': 4, 'remembers': 1, 'effects': 0}, counts['Wrapped'])

    def test_counts_the_runtime_package_qualified_annotation_and_calls_once_each(self):
        source = '''
            @androidx.compose.runtime.Composable
            internal fun itemPins(): Pins {
                var draft by androidx.compose.runtime.saveable.rememberSaveable { mutableStateOf("") }
                val onDone by androidx.compose.runtime.rememberUpdatedState(done)
                val scope = androidx.compose.runtime.rememberCoroutineScope()
                androidx.compose.runtime.LaunchedEffect(key) { }
                androidx.compose.runtime.SideEffect { }
                return remember { Pins() }
            }
            @androidx.compose.runtime.Composable private fun FormProbe() { DisposableEffect(Unit) { onDispose { } } }
        '''
        self.assertEqual([('itemPins', {'lines': 8, 'remembers': 4, 'effects': 2}),
                          ('FormProbe', {'lines': 1, 'remembers': 0, 'effects': 1})], functions(source))

    def test_another_objects_remember_or_effect_method_is_no_call(self):
        source = '''
            @Composable
            fun Page(cache: Cache, state: Host) {
                cache.remember(key) { 1 }
                state.rememberScroll { }
                state.LaunchedEffect(key) { }
                other.androidx.compose.runtime.remember { 1 }
                val text = remember { "androidx.compose.runtime.remember { }" }
            }
        '''
        self.assertEqual({'lines': 7, 'remembers': 1, 'effects': 0}, dict(functions(source))['Page'])

    def test_a_qualified_composable_type_is_no_function(self):
        source = '''
            typealias Slot = @androidx.compose.runtime.Composable () -> Unit
            fun Host(trailing: (@androidx.compose.runtime.Composable (Modifier) -> Unit)? = null) { remember { 1 } }
        '''
        self.assertEqual([], functions(source))

    def test_a_composable_type_or_lambda_is_no_function(self):
        source = '''
            val slot: @Composable () -> Unit = { val a = remember { 1 } }
            class Host(val content: @Composable (Modifier) -> Unit)
            fun plain() { remember { 1 } }
        '''
        self.assertEqual([], functions(source))

    def test_braces_and_quotes_in_strings_and_comments_do_not_end_the_body(self):
        source = '''
            @Composable
            fun Page() {
                Text("}") // }
                Text("""
                    } "quoted" {
                """)
                val c = '}'
                val a = remember { 1 }
            }
        '''
        self.assertEqual(1, dict(functions(source))['Page']['remembers'])

    def test_code_of_keeps_offsets_and_lines(self):
        source = 'val a = "x // y" // c\n/* b\n */ val b = 1\n'
        code = code_of(source)
        self.assertEqual(len(source), len(code))
        self.assertEqual(source.count('\n'), code.count('\n'))
        self.assertNotIn('//', code)
        self.assertIn('val b = 1', code)


class RatchetTest(unittest.TestCase):
    def tree(self, files):
        root = Path(tempfile.mkdtemp())
        for rel, text in files.items():
            path = root / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        return root

    def test_scans_production_source_sets_only_and_numbers_a_second_name(self):
        big = composable('Big', lines=3)
        root = self.tree({PAGE: big + big,
                          'android/feature/demo/src/test/kotlin/demo/PageTest.kt': composable('Test', lines=500)})
        self.assertEqual({f'{PAGE}::Big', f'{PAGE}::Big#2'}, set(scan(root)))

    def test_scans_a_file_that_only_names_the_qualified_annotation(self):
        root = self.tree({PAGE: composable('Probe', lines=3, head='@androidx.compose.runtime.Composable\nfun')})
        self.assertEqual({f'{PAGE}::Probe': {'lines': 3, 'remembers': 0, 'effects': 0}}, scan(root))

    def test_only_counts_over_a_cap_need_an_entry_and_a_listed_one_may_only_fall(self):
        current = {'a::Big': {'lines': 500, 'remembers': 40, 'effects': 2}, 'a::Small': {'lines': 10, 'remembers': 1,
                                                                                         'effects': 0}}
        baseline = baseline_of(current)
        self.assertEqual({'functions': {'a::Big': {'lines': 500, 'remembers': 40}}, 'version': 1}, baseline)
        self.assertEqual([], regressions(current, baseline))
        # A count the baseline does not list may rise to its cap, not above it.
        current['a::Big']['effects'] = CAPS['effects']
        self.assertEqual([], regressions(current, baseline))
        current['a::Big'].update(lines=501, effects=CAPS['effects'] + 1)
        self.assertEqual([f'a::Big: lines 501 (limit 500), effects {CAPS["effects"] + 1} (limit {CAPS["effects"]})'],
                         regressions(current, baseline))

    def test_a_new_function_over_a_cap_fails(self):
        current = {'a::New': {'lines': CAPS['lines'] + 1, 'remembers': 0, 'effects': 0}}
        self.assertEqual([f'a::New: lines {CAPS["lines"] + 1} (limit {CAPS["lines"]})'],
                         regressions(current, {'version': 1, 'functions': {}}))

    def test_update_lowers_and_drops_but_refuses_a_rise_or_a_new_entry(self):
        baseline = {'version': 1, 'functions': {'a::Big': {'lines': 500, 'remembers': 40}}}
        lower = {'a::Big': {'lines': 450, 'remembers': 20, 'effects': 0}}
        self.assertEqual([], update_raises(lower, baseline))
        self.assertEqual({'version': 1, 'functions': {'a::Big': {'lines': 450}}}, baseline_of(lower))
        self.assertEqual(['a::Big: lines 501, baseline 500', 'a::New: effects 13, not in the baseline'],
                         update_raises({'a::Big': {'lines': 501, 'remembers': 40, 'effects': 0},
                                        'a::New': {'lines': 1, 'remembers': 0, 'effects': 13}}, baseline))

    def test_main_fails_a_rise_and_update_refuses_it(self):
        root = self.tree({PAGE: composable('Page', lines=CAPS['lines'] + 10)})
        baseline = root / 'baseline.json'
        run = lambda *args: main(['--root', str(root), '--baseline', str(baseline), *args])
        with redirect_stdout(io.StringIO()):
            self.assertEqual(0, run('--update-baseline'))
        self.assertEqual({f'{PAGE}::Page': {'lines': CAPS['lines'] + 10}}, json.loads(baseline.read_text())['functions'])
        with redirect_stdout(io.StringIO()):
            self.assertEqual(0, run())
        (root / PAGE).write_text(composable('Page', lines=CAPS['lines'] + 11))
        out = io.StringIO()
        with redirect_stdout(out):
            self.assertEqual(1, run())
            self.assertEqual(1, run('--update-baseline'))
        self.assertIn(f'{PAGE}::Page: lines {CAPS["lines"] + 11} (limit {CAPS["lines"] + 10})', out.getvalue())
        self.assertIn('split the function', out.getvalue())
        (root / PAGE).write_text(composable('Page', lines=5))
        out = io.StringIO()
        with redirect_stdout(out):
            self.assertEqual(0, run())
        self.assertIn('1 baseline entry above the current counts', out.getvalue())


if __name__ == '__main__':
    unittest.main()
