import importlib.util
import json
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[3]
SPEC = importlib.util.spec_from_file_location('package_report', ROOT / 'native/app-core/scripts/android-package-report.py')
report = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(report)
RUNNER = importlib.util.spec_from_file_location('device_runner', ROOT / 'native/app-core/scripts/android-device-test.py')
runner = importlib.util.module_from_spec(RUNNER)
RUNNER.loader.exec_module(runner)


class AndroidPackageReportTests(unittest.TestCase):
    def test_elf_machine_and_every_load_alignment_are_required(self):
        data = bytearray(0x100)
        data[:8] = b'\x7fELF\x02\x01\x01\x00'
        struct.pack_into('<HHIQQQIHHHHHH', data, 16, 2, 62, 1, 0, 0x40, 0, 0, 64, 56, 2, 0, 0, 0)
        struct.pack_into('<IIQQQQQQ', data, 0x40, 1, 0, 0, 0, 0, 0, 0, 0x4000)
        struct.pack_into('<IIQQQQQQ', data, 0x40 + 56, 1, 0, 0, 0, 0, 0, 0, 0x4000)
        self.assertEqual(report.inspect_elf(bytes(data), 'x86_64'), 2)
        with self.assertRaisesRegex(ValueError, 'wrong ELF machine'):
            report.inspect_elf(bytes(data), 'arm64-v8a')
        struct.pack_into('<Q', data, 0x40 + 56 + 48, 4096)
        with self.assertRaisesRegex(ValueError, 'LOAD alignments'):
            report.inspect_elf(bytes(data), 'x86_64')

    def test_apk_must_store_both_libraries_at_aligned_data_offsets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            apk = root / 'test.apk'
            with zipfile.ZipFile(apk, 'w') as archive:
                for abi in report.ABIS:
                    info = zipfile.ZipInfo(f'lib/{abi}/libpersona_core.so')
                    info.compress_type = zipfile.ZIP_STORED
                    data = bytearray(0x100)
                    data[:8] = b'\x7fELF\x02\x01\x01\x00'
                    struct.pack_into('<HHIQQQIHHHHHH', data, 16, 2, 62 if abi == 'x86_64' else 183,
                                     1, 0, 0x40, 0, 0, 64, 56, 1, 0, 0, 0)
                    struct.pack_into('<IIQQQQQQ', data, 0x40, 1, 0, 0, 0, 0, 0, 0, 0x4000)
                    archive.writestr(info, data)
            # This check uses a test zipalign stand-in; no SDK or build is claimed.
            align = root / 'zipalign'
            align.write_text('#!/bin/sh\nprintf "Verification successful\\n"\n')
            align.chmod(0o700)
            with self.assertRaisesRegex(ValueError, 'data offset'):
                report.inspect_apk(apk, root)


class DeviceBenchmarkGateTests(unittest.TestCase):
    def test_instrumentation_nonzero_is_captured_before_the_budget_failure(self):
        calls = []
        failure = subprocess.CalledProcessError(1, ['am', 'instrument'], output='INSTRUMENTATION_CODE: -1\n',
                                                stderr='FAILURES!!!\n')
        def captures_nonzero(command, **kwargs):
            calls.append(command)
            raise failure
        original = runner.run
        runner.run = captures_nonzero
        try:
            self.assertEqual(runner.instrumentation_output(['am', 'instrument'], 30),
                             'INSTRUMENTATION_CODE: -1\nFAILURES!!!\n')
        finally:
            runner.run = original
        self.assertEqual(calls, [['am', 'instrument']])

    def test_debug_benchmark_apk_is_rejected_before_adb(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            apk = root / 'android/core/rustcore-android/build/outputs/apk/androidTest/debug/rustcore-android-debug-androidTest.apk'
            apk.parent.mkdir(parents=True)
            apk.write_bytes(b'not an installed test')
            result = subprocess.run([
                'python3', '-I', str(ROOT / 'native/app-core/scripts/android-device-test.py'),
                '--serial', 'emulator-test', '--suite', 'benchmark', '--apk', str(apk),
                '--output', str(root / 'result'),
            ], env=dict(os.environ, ANDROID_HOME=str(root)), capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('non-debuggable release androidTest APK', result.stderr)
            self.assertNotIn('adb', result.stdout)
    def test_release_benchmark_mapping_is_under_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            apk = Path(directory) / 'build/outputs/apk/androidTest/release/rustcore-android-release-androidTest.apk'
            mapping = apk.parents[3] / 'mapping/releaseAndroidTest/mapping.txt'
            self.assertEqual(mapping, apk.parents[3] / 'mapping/releaseAndroidTest/mapping.txt')
            self.assertNotIn('apk', mapping.parts[-4])

    def test_interrupted_junit_is_rejected(self):
        expected = [('persona.core.rustcore.RustCoreDeviceBenchmark', 'dispatchAndFrameCosts')]
        incomplete = ('INSTRUMENTATION_STATUS: class=persona.core.rustcore.RustCoreDeviceBenchmark\n'
                      'INSTRUMENTATION_STATUS: test=dispatchAndFrameCosts\n'
                      'INSTRUMENTATION_STATUS_CODE: 1\n'
                      'INSTRUMENTATION_CODE: -1\n')
        with self.assertRaisesRegex(ValueError, 'incomplete or unexpected'):
            runner.parse_junit(incomplete, expected)
        complete = incomplete.replace('INSTRUMENTATION_CODE: -1\n', '') + 'INSTRUMENTATION_STATUS_CODE: 0\nINSTRUMENTATION_CODE: -1\n'
        runner.parse_junit(complete, expected)
        with self.assertRaisesRegex(ValueError, 'duplicate JUnit completion'):
            runner.parse_junit(incomplete.replace('INSTRUMENTATION_CODE: -1\n', '')
                               + 'INSTRUMENTATION_STATUS_CODE: 0\nINSTRUMENTATION_STATUS_CODE: 0\n', expected)
        evidence = incomplete.replace('INSTRUMENTATION_CODE: -1\n', '') + '''INSTRUMENTATION_STATUS_CODE: 2
INSTRUMENTATION_STATUS: rustCoreBenchmarkReport={"raw":true}
INSTRUMENTATION_STATUS_CODE: 0
INSTRUMENTATION_CODE: -1
'''
        runner.parse_junit(evidence, expected)
        with self.assertRaisesRegex(ValueError, 'incomplete or unexpected'):
            runner.parse_junit(evidence + 'INSTRUMENTATION_STATUS_CODE: 0\n', expected)

    def test_failed_benchmark_keeps_numeric_raw_samples_and_original_budgets(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            nonce = 'synthetic-nonce'
            dispatch = [1_000] * 1000
            dispatch[-1] = 51_000
            frame = [2_000] * 1000
            report = {
                'nonce': nonce, 'samples': 1000, 'warmUp': 100,
                'model': 'example-emulator', 'abi': 'x86_64', 'api': 35,
                'build': 'example-build', 'kernelPageBytes': 4096,
                'dispatchMicros': {'p50': 1.0, 'p99': 1.0, 'max': 51.0,
                                   'budgetMicros': 50.0, 'passesBudget': True},
                'drainDecodeApplyMicros': {'p50': 2.0, 'p99': 2.0, 'max': 2.0,
                                           'budgetMicros': 1000.0, 'passesBudget': True},
                'rawSamples': {'unit': 'ns', 'dispatch': dispatch, 'drainDecodeApply': frame},
            }
            instrumentation = 'INSTRUMENTATION_STATUS: rustCoreBenchmarkReport=' + json.dumps(report, separators=(',', ':')) + '\r\n'
            class Args:
                suite = 'benchmark'
            Args.output = output
            runner.benchmark_report(instrumentation, nonce, Args, ['adb'], {
                'ro.product.model': 'example-emulator', 'ro.product.cpu.abi': 'x86_64',
                'ro.build.version.sdk': '35', 'ro.build.display.id': 'example-build', 'kernelPageBytes': '4096',
            })
            saved = json.loads((output / 'benchmark.json').read_text())
            self.assertEqual(saved['rawSamples']['dispatch'][-1], 51_000)
            self.assertIsInstance(saved['rawSamples']['dispatch'][-1], int)
            self.assertEqual(saved['dispatchMicros']['budgetMicros'], 50.0)
            report['dispatchMicros']['p99'] = 999.0
            with self.assertRaisesRegex(ValueError, 'aggregates do not match'):
                runner.benchmark_report('INSTRUMENTATION_STATUS: rustCoreBenchmarkReport=' + json.dumps(report) + '\r\n',
                                        nonce, Args, ['adb'], {
                    'ro.product.model': 'example-emulator', 'ro.product.cpu.abi': 'x86_64',
                    'ro.build.version.sdk': '35', 'ro.build.display.id': 'example-build', 'kernelPageBytes': '4096',
                })
            report['dispatchMicros']['p99'] = 1.0
            report['dispatchMicros']['budgetMicros'] = 100.0
            with self.assertRaisesRegex(ValueError, 'original budget'):
                runner.benchmark_report('INSTRUMENTATION_STATUS: rustCoreBenchmarkReport=' + json.dumps(report) + '\r\n',
                                        nonce, Args, ['adb'], {
                    'ro.product.model': 'example-emulator', 'ro.product.cpu.abi': 'x86_64',
                    'ro.build.version.sdk': '35', 'ro.build.display.id': 'example-build', 'kernelPageBytes': '4096',
                })

    def test_control_requires_complete_raw_arrays_and_matching_aggregates(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            samples = [1_000] * 1000
            report = {
                'nonce': 'synthetic-control', 'samples': 1000, 'warmUp': 100,
                'model': 'example-emulator', 'abi': 'x86_64', 'api': 35,
                'build': 'example-build', 'kernelPageBytes': 4096,
                'cpuLoopMicros': {'p50': 1.0, 'p99': 1.0, 'max': 1.0},
                'encodingMicros': {'p50': 1.0, 'p99': 1.0, 'max': 1.0},
                'dispatchMicros': {'p50': 1.0, 'p99': 1.0, 'max': 1.0},
                'drainDecodeApplyMicros': {'p50': 1.0, 'p99': 1.0, 'max': 1.0},
                'rawSamples': {'unit': 'ns', 'cpu': samples, 'encoding': samples,
                               'dispatch': samples, 'drainDecodeApply': samples},
            }
            class Args:
                suite = 'control'
            Args.output = output
            props = {'ro.product.model': 'example-emulator', 'ro.product.cpu.abi': 'x86_64',
                     'ro.build.version.sdk': '35', 'ro.build.display.id': 'example-build', 'kernelPageBytes': '4096'}
            runner.benchmark_report('INSTRUMENTATION_STATUS: rustCoreControlReport=' + json.dumps(report) + '\r\n',
                                    'synthetic-control', Args, ['adb'], props)
            report['cpuLoopMicros']['p99'] = 0.0
            with self.assertRaisesRegex(ValueError, 'control aggregates do not match'):
                runner.benchmark_report('INSTRUMENTATION_STATUS: rustCoreControlReport=' + json.dumps(report) + '\r\n',
                                        'synthetic-control', Args, ['adb'], props)
            report['cpuLoopMicros']['p99'] = 1.0
            report['rawSamples']['cpu'] = samples[:999]
            with self.assertRaisesRegex(ValueError, '1000 raw nanosecond'):
                runner.benchmark_report('INSTRUMENTATION_STATUS: rustCoreControlReport=' + json.dumps(report) + '\r\n',
                                        'synthetic-control', Args, ['adb'], props)

    def test_fixture_names_are_validated_like_the_device_reader(self):
        with tempfile.TemporaryDirectory() as directory:
            apk = Path(directory) / 'fixtures.apk'
            with zipfile.ZipFile(apk, 'w') as package:
                package.writestr('assets/continuity/a.json',
                                 '{"version":1,"cases":[{"name":"same"},{"name":"same"}]}')
            with self.assertRaisesRegex(ValueError, 'duplicate packaged continuity'):
                runner.fixture_identities(apk)
            with zipfile.ZipFile(apk, 'w') as package:
                package.writestr('assets/continuity/a.json', '{broken')
            with self.assertRaisesRegex(ValueError, 'invalid packaged continuity'):
                runner.fixture_identities(apk)

    def test_truncated_elf_is_a_bounded_failure(self):
        data = bytearray(64)
        data[:8] = b'\x7fELF\x02\x01\x01\x00'
        struct.pack_into('<HHIQQQIHHHHHH', data, 16, 2, 62, 1, 0, 0x40, 0, 0, 64, 56, 2, 0, 0, 0)
        with self.assertRaisesRegex(ValueError, 'truncated ELF'):
            report.inspect_elf(bytes(data), 'x86_64')


if __name__ == '__main__':
    unittest.main()
