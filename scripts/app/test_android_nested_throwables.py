import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

from android_nested_throwables import declarations, keep_rules, main, matches, scan, throwables, uncovered

READER = 'android/feature/files/src/main/kotlin/demo/files/DocumentText.kt'
RULES = 'android/app/proguard-rules.pro'


class DeclarationTest(unittest.TestCase):
    def nested(self, source):
        return {binary: nested for binary, _, nested, _ in declarations(source)}

    def test_members_nest_through_class_bodies_and_companions_only(self):
        source = '''
            package demo
            internal object Reader {
                private class TooBig : SAXException() {
                    override fun fillInStackTrace(): Throwable = this
                }
                companion object { class Inner : Exception() }
                sealed class Failure(message: String) : Exception(message) {
                    class Gap : Failure("gap")
                }
                fun read() {
                    class Local : Exception()
                    val o = object : Runnable { override fun run() { } }
                    val r = Reader::class
                }
                init { class AlsoLocal }
            }
        '''
        self.assertEqual({'demo.Reader': False, 'demo.Reader$TooBig': True, 'demo.Reader$Companion': True,
                          'demo.Reader$Companion$Inner': True, 'demo.Reader$Failure': True,
                          'demo.Reader$Failure$Gap': True, 'demo.Reader$Local': False,
                          'demo.Reader$AlsoLocal': False}, self.nested(source))

    def test_headers_over_several_lines_and_strings_do_not_mislead(self):
        source = '''
            package demo
            class Outer(
                val a: Int = run { 1 },
            ) : Base(), Marker {
                class Refused(
                    val code: Int,
                )
                    : HubFailure("refused class Fake {")
                val text = "class NotOne : Exception() {"
            }
        '''
        found = {binary: supertypes for binary, supertypes, _, _ in declarations(source)}
        self.assertEqual({'demo.Outer': ['Base', 'Marker'], 'demo.Outer$Refused': ['HubFailure']}, found)

    def test_throwables_follow_the_repository_classes_and_library_names(self):
        classes = {'a.HubFailure': (['RuntimeException'], False, ''), 'a.Client$Refused': (['HubFailure'], True, ''),
                   'a.Doc$Stop': (['SAXException'], True, ''), 'a.Doc$Error': (['ErrorBody'], True, ''),
                   'a.ErrorBody': (['Parcelable'], False, ''), 'a.Doc': ([], False, '')}
        self.assertEqual({'a.HubFailure', 'a.Client$Refused', 'a.Doc$Stop'}, throwables(classes))


class RuleTest(unittest.TestCase):
    def test_wildcards_follow_proguard(self):
        self.assertTrue(matches('persona.feature.files.Xlsx', 'persona.feature.files.Xlsx'))
        self.assertTrue(matches('persona.feature.**', 'persona.feature.files.Xlsx'))
        self.assertTrue(matches('persona.feature.files.*', 'persona.feature.files.Xlsx'))
        self.assertFalse(matches('persona.feature.*', 'persona.feature.files.Xlsx'))
        self.assertFalse(matches('persona.feature.files.Xls', 'persona.feature.files.Xlsx'))
        self.assertTrue(matches('*', 'persona.feature.files.Xlsx'))

    def test_reads_keep_and_keepnames_rules_from_the_app_and_consumer_files(self):
        root = Path(tempfile.mkdtemp())
        (root / 'android/app').mkdir(parents=True)
        (root / RULES).write_text('# -keepnames class commented.Out\n-keepnames class a.B\n'
                                  '-keepnames class * extends java.lang.Throwable\n'
                                  '-keep class c.D, c.E { <init>(); }\n-keepclassmembers class f.G { int x(); }\n')
        (root / 'android/core/x').mkdir(parents=True)
        (root / 'android/core/x/consumer-rules.pro').write_text('-keep,allowoptimization class h.I { *; }\n')
        self.assertEqual([('a.B', None), ('*', 'java.lang.Throwable'), ('c.D', None), ('c.E', None), ('h.I', None)],
                         keep_rules(root))

    def test_an_outer_class_is_covered_by_a_rule_or_by_being_a_throwable(self):
        classes = {'a.Doc': ([], False, 'Doc.kt:1'), 'a.Doc$Stop': (['SAXException'], True, 'Doc.kt:2'),
                   'a.Failure': (['Exception'], False, 'F.kt:1'), 'a.Failure$Gap': (['Failure'], True, 'F.kt:2')}
        throwable_rule = [('*', 'java.lang.Throwable')]
        self.assertEqual({'a.Doc$Stop': ('a.Doc', 'Doc.kt:2')}, uncovered(classes, throwable_rule))
        self.assertEqual({}, uncovered(classes, throwable_rule + [('a.Doc', None)]))
        self.assertEqual({}, uncovered(classes, throwable_rule + [('a.**', None)]))


class MainTest(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        (self.root / READER).parent.mkdir(parents=True)
        (self.root / READER).write_text('package demo.files\ninternal object DocumentText {\n'
                                        '    private class CompareTooBigException : SAXException()\n}\n')
        (self.root / 'android/app').mkdir(parents=True)
        (self.root / RULES).write_text('-keepnames class * extends java.lang.Throwable\n')
        self.baseline = self.root / 'baseline.json'

    def run_main(self, *args):
        out = io.StringIO()
        with redirect_stdout(out):
            code = main(['--root', str(self.root), '--baseline', str(self.baseline), *args])
        return code, out.getvalue()

    def test_a_new_nested_exception_without_its_rule_fails_and_names_the_rule(self):
        self.baseline.write_text(json.dumps({'version': 1, 'uncovered': []}))
        code, out = self.run_main()
        self.assertEqual(1, code)
        self.assertIn(f'{READER}:3: demo.files.DocumentText$CompareTooBigException is nested in demo.files.DocumentText',
                      out)
        self.assertIn('`-keepnames class demo.files.DocumentText`', out)
        self.assertEqual(1, self.run_main('--update-baseline')[0])

    def test_the_baseline_holds_old_ones_until_the_rule_lands(self):
        self.assertEqual(0, self.run_main('--update-baseline')[0])
        self.assertEqual(['demo.files.DocumentText$CompareTooBigException'],
                         json.loads(self.baseline.read_text())['uncovered'])
        self.assertEqual(0, self.run_main()[0])
        (self.root / RULES).write_text('-keepnames class * extends java.lang.Throwable\n'
                                       '-keepnames class demo.files.DocumentText\n')
        code, out = self.run_main()
        self.assertEqual(0, code)
        self.assertIn('1 baseline entry now covered or gone', out)
        self.assertEqual(0, self.run_main('--update-baseline')[0])
        self.assertEqual([], json.loads(self.baseline.read_text())['uncovered'])

    def test_scans_production_sources_only(self):
        test_source = self.root / 'android/feature/files/src/test/kotlin/demo/files/Fixture.kt'
        test_source.parent.mkdir(parents=True)
        test_source.write_text('package demo.files\nobject Fixture { class Boom : Exception() }\n')
        self.assertNotIn('demo.files.Fixture$Boom', scan(self.root))


if __name__ == '__main__':
    unittest.main()
