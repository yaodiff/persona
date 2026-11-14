#!/usr/bin/env python3
"""Install and run an already assembled packaged-JNI test APK with bounded waits and real JUnit evidence."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid
import zipfile


def run(command, timeout=120, **kwargs):
    return subprocess.run(command, timeout=timeout, check=True, text=True, **kwargs)


def parse_junit(output, expected_identities):
    """Require a start and exactly one successful completion for every selected test, not just a final count."""
    current_class = None
    current_test = None
    started = set()
    completed = set()
    terminal_seen = False
    for line in output.splitlines():
        if line.startswith('INSTRUMENTATION_CODE:'):
            terminal_seen = True
            continue
        if line.startswith('INSTRUMENTATION_STATUS') and terminal_seen:
            raise ValueError('incomplete or unexpected JUnit tests: status after terminal instrumentation code')
        if line.startswith('INSTRUMENTATION_STATUS: class='):
            current_class = line.split('=', 1)[1].strip()
        elif line.startswith('INSTRUMENTATION_STATUS: test='):
            current_test = line.split('=', 1)[1].strip()
        elif line.startswith('INSTRUMENTATION_STATUS_CODE:'):
            status = int(line.split(':', 1)[1].strip())
            identity = (current_class, current_test)
            if status == 1:
                started.add(identity)
            elif status == 0:
                if identity in completed:
                    raise ValueError('duplicate JUnit completion: ' + str(identity))
                completed.add(identity)
            else:
                if status == 2:
                    # Report-only evidence is not a JUnit failure or a successful test completion.
                    continue
                raise ValueError('JUnit test did not complete successfully: ' + str(identity))
    if set(expected_identities) != started or set(expected_identities) != completed:
        raise ValueError('incomplete or unexpected JUnit tests: expected ' + str(sorted(expected_identities))
                         + ', started ' + str(sorted(started)) + ', completed ' + str(sorted(completed)))
    if 'FAILURES!!!' in output or 'INSTRUMENTATION_CODE: 0' in output:
        raise ValueError('instrumentation reported failures or no executed tests')


def fixture_identities(apk):
    """The same file/version/nonempty/unique-name contract as DeviceFixtureReader, with exact test names."""
    names = []
    try:
        with zipfile.ZipFile(apk) as package:
            files = sorted(name for name in package.namelist()
                           if name.startswith('assets/continuity/') and name.endswith('.json'))
            if not files:
                raise ValueError('no packaged continuity fixtures')
            for name in files:
                fixture = json.loads(package.read(name))
                if fixture.get('version') != 1:
                    raise ValueError('unsupported fixture version: ' + name)
                cases = fixture.get('cases')
                if not isinstance(cases, list) or not cases:
                    raise ValueError('empty or missing fixture cases: ' + name)
                names.extend(case['name'] for case in cases)
    except (KeyError, TypeError, json.JSONDecodeError) as error:
        raise ValueError('invalid packaged continuity fixture: ' + str(error)) from error
    if len(names) != len(set(names)):
        raise ValueError('duplicate packaged continuity case names')
    return [('persona.core.rustcore.ContinuityBindingFixtureTest', 'replay[' + name + ']') for name in names]


def build_output(result):
    return (result.stdout or '') + (result.stderr or '')


def save_build_failure(output, error):
    if isinstance(error, subprocess.TimeoutExpired):
        (output / 'gradle.txt').write_text(build_output(error))
        raise ValueError(f'Gradle build TIMEOUT after {error.timeout}s') from error
    (output / 'gradle.txt').write_text(build_output(error))
    raise ValueError('Gradle build failed with exit ' + str(error.returncode) + '; see gradle.txt') from error


def instrumentation_output(command, timeout):
    """A nonzero JUnit process carries evidence; the caller still enforces its real test result."""
    try:
        result = run(command, timeout=timeout, capture_output=True)
    except subprocess.CalledProcessError as error:
        return build_output(error)
    return result.stdout + result.stderr


def benchmark_report(output, nonce, args, adb, props):
    """Recover the deliberate FAIL's raw evidence before enforcing its unchanged performance budgets."""
    if args.suite not in ('benchmark', 'attribution', 'control'):
        return
    report_key = {'attribution': 'rustCoreAttributionReport', 'control': 'rustCoreControlReport'}.get(
        args.suite, 'rustCoreBenchmarkReport')
    statuses = re.findall(r'^INSTRUMENTATION_STATUS: ' + report_key + r'=(\{.*\})\r?$', output, re.MULTILINE)
    if len(statuses) > 1:
        raise ValueError(args.suite + ' must publish at most one report status')
    report_path = {'attribution': 'rust-core-attribution.json', 'control': 'rust-core-control.json'}.get(
        args.suite, 'rust-core-benchmark.json')
    try:
        report = run(adb + ['shell', 'cat',
                            '/sdcard/Android/data/persona.core.rustcore.android.test/files/' + report_path],
                     timeout=30, capture_output=True)
        file_output = report.stdout + report.stderr
        file_report = json.loads(report.stdout)
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired, json.JSONDecodeError) as error:
        file_output = build_output(error) if isinstance(error, (subprocess.CalledProcessError, subprocess.TimeoutExpired)) else str(error) + '\n'
        file_report = None
    if file_report is not None and file_report.get('nonce') == nonce:
        (args.output / 'benchmark.json').write_text(report.stdout)
        measurements = file_report
    else:
        if len(statuses) != 1:
            raise ValueError(args.suite + ' report file is unreadable and no fresh report status was published')
        # A read-only emulated /sdcard must not discard measurements already completed on the original timed spans.
        # The status report carries the same JSON, including the file error; JUnit still fails below when appropriate.
        (args.output / 'external-report.txt').write_text(file_output)
        measurements = json.loads(statuses[0])
        (args.output / 'benchmark.json').write_text(json.dumps(measurements, indent=2) + '\n')
    if measurements.get('nonce') != nonce:
        raise ValueError('benchmark report does not belong to this run')
    expected_device = {'model': props['ro.product.model'], 'abi': props['ro.product.cpu.abi'],
                       'api': int(props['ro.build.version.sdk']),
                       'build': props['ro.build.display.id'], 'kernelPageBytes': int(props['kernelPageBytes'])}
    for key, expected_value in expected_device.items():
        if measurements.get(key) != expected_value:
            raise ValueError(f'benchmark device mismatch for {key}: {measurements.get(key)!r} != {expected_value!r}')
    if measurements.get('samples') != 1000 or measurements.get('warmUp') != 100:
        raise ValueError('benchmark must contain 1000 measured samples after 100 warm-ups')
    raw = measurements.get('rawSamples', {})
    if raw.get('unit') != 'ns':
        raise ValueError('benchmark raw samples must use nanoseconds')
    if args.suite == 'benchmark':
        for metric, key, budget in (('dispatchMicros', 'dispatch', 50.0),
                                    ('drainDecodeApplyMicros', 'drainDecodeApply', 1000.0)):
            if not all(isinstance(measurements[metric].get(field), (int, float))
                       for field in ('p50', 'p99', 'max')):
                raise ValueError('benchmark is missing p50/p99/max: ' + metric)
            samples = raw.get(key)
            if not isinstance(samples, list) or len(samples) != 1000 or not all(
                    isinstance(value, int) and value >= 0 for value in samples):
                raise ValueError('benchmark must contain 1000 raw nanosecond samples: ' + key)
            ordered = sorted(samples)
            calculated = {'p50': ordered[499] / 1000.0, 'p99': ordered[989] / 1000.0, 'max': ordered[-1] / 1000.0}
            if any(measurements[metric][field] != value for field, value in calculated.items()):
                raise ValueError('benchmark aggregates do not match its raw samples: ' + metric)
            if measurements[metric].get('budgetMicros') != budget or measurements[metric].get('passesBudget') is not (
                    calculated['p99'] <= budget):
                raise ValueError('benchmark does not retain its original budget: ' + metric)
    if args.suite == 'control':
        for metric, key in (('cpuLoopMicros', 'cpu'), ('encodingMicros', 'encoding'),
                            ('dispatchMicros', 'dispatch'), ('drainDecodeApplyMicros', 'drainDecodeApply')):
            if not all(isinstance(measurements[metric].get(field), (int, float))
                       for field in ('p50', 'p99', 'max')):
                raise ValueError('control is missing p50/p99/max: ' + metric)
            samples = raw.get(key)
            if not isinstance(samples, list) or len(samples) != 1000 or not all(
                    isinstance(value, int) and value >= 0 for value in samples):
                raise ValueError('control must contain 1000 raw nanosecond samples: ' + key)
            ordered = sorted(samples)
            expected = {'p50': ordered[499] / 1000.0, 'p99': ordered[989] / 1000.0, 'max': ordered[-1] / 1000.0}
            if any(measurements[metric][field] != value for field, value in expected.items()):
                raise ValueError('control aggregates do not match raw samples: ' + metric)
    if args.suite == 'attribution':
        for key, raw_key in (('dispatchAttribution', 'dispatch'), ('frameAttribution', 'drainDecodeApply')):
            details = measurements.get(key)
            if not isinstance(details, list) or len(details) != 1000:
                raise ValueError('attribution must contain 1000 detailed raw samples: ' + key)
            for index, detail in enumerate(details):
                if detail.get('runtimeMeasurementAvailable') is not True:
                    raise ValueError('native attribution counters were not recorded: ' + key)
                if detail.get('durationNs') != raw[raw_key][index]:
                    raise ValueError('attribution duration differs from its raw sample: ' + key)
                delta = detail['runtimeDelta']
                if any(not isinstance(value, int) or value < 0 for field, value in delta.items() if field != 'workerTid'):
                    raise ValueError('negative native attribution delta: ' + key)
                for field in ('vmBefore', 'vmAfter'):
                    if not detail[field].get('schedulerAvailable') or not detail[field].get('gcAvailable'):
                        raise ValueError('attribution scheduler/GC counters are unavailable: ' + key)
                if delta.get('workerTid', 0) <= 0 or delta['workerTid'] == detail['vmBefore']['tid']:
                    raise ValueError('invalid worker thread identity: ' + key)
                if key == 'dispatchAttribution':
                    if delta['workerActions'] != 1 or detail['kotlinEncodingNs'] + delta['queueNs'] > detail['durationNs']:
                        raise ValueError('incoherent dispatch attribution: ' + str(index))
                else:
                    if (delta['patchLockCalls'] != 1 or delta['patchHeldWorkNs'] <= 0
                            or detail['kotlinEncodingNs'] + detail['kotlinDecodeNs'] + detail['applyNs']
                            != detail['durationNs']):
                        raise ValueError('incoherent drain attribution: ' + str(index))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--serial', required=True, help='an explicitly selected emulator or hardware device')
    parser.add_argument('--suite', choices=('replay', 'benchmark', 'attribution', 'control'), default='replay')
    parser.add_argument('--apk', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--timeout', type=int, default=600)
    args = parser.parse_args()
    if args.timeout < 1:
        raise ValueError('--timeout must be positive')
    if not args.apk.is_file():
        raise ValueError('missing assembled test APK: ' + str(args.apk))
    args.output.mkdir(parents=True, exist_ok=True)
    sdk = Path(os.environ['ANDROID_HOME'])
    if args.suite in ('benchmark', 'attribution', 'control'):
        # Never label a debug APK as optimized just because it contains the benchmark class. The release test
        # has its own R8 mapping and is non-debuggable; both must be true for this evidence to be accepted.
        if '/androidTest/release/' not in str(args.apk):
            raise ValueError('benchmark requires the non-debuggable release androidTest APK')
        mapping = args.apk.parents[3] / 'mapping' / 'releaseAndroidTest' / 'mapping.txt'
        android = args.apk.parents[5].parents[1]
        repository = android.parent
        # R8 writes mapping after packaging the test APK, so mapping's mtime is normally later. Rebuild that
        # exact variant before installing rather than accepting an unrelated debug or stale release artifact.
        try:
            result = run(['bash', str(android / 'gradlew'), '--no-daemon', '--no-watch-fs', '--console=plain', '--max-workers=2',
                          '-PpersonaApplicationId=com.example.persona', '-PpersonaGoogleOAuthScheme=persona.oauth.unset',
                          '-PpersonaRepoRoot=' + str(repository),
                          ':core:rustcore-android:minifyReleaseAndroidTestWithR8',
                          ':core:rustcore-android:assembleReleaseAndroidTest'],
                         cwd=android, timeout=args.timeout, capture_output=True)
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            save_build_failure(args.output, error)
        (args.output / 'gradle.txt').write_text(build_output(result))
        if not mapping.is_file():
            raise ValueError('benchmark requires the matching releaseAndroidTest R8 mapping')
        tools = sdk / 'build-tools/37.0.0'
        badging = run([str(tools / 'aapt2'), 'dump', 'badging', str(args.apk)],
                      timeout=30, capture_output=True).stdout
        if 'application-debuggable' in badging:
            raise ValueError('benchmark APK must be non-debuggable')
        (args.output / 'r8-mapping.txt').write_bytes(mapping.read_bytes())
        source_revision = run(['git', '-C', str(repository), 'rev-parse', 'HEAD'], timeout=30, capture_output=True).stdout.strip()
        (args.output / 'artifact.json').write_text(json.dumps({
            'apkSha256': hashlib.sha256(args.apk.read_bytes()).hexdigest(),
            'mappingSha256': hashlib.sha256(mapping.read_bytes()).hexdigest(),
            'sourceRevision': source_revision,
        }, indent=2) + '\n')
    adb = [str(sdk / 'platform-tools/adb')]
    if os.environ.get('ANDROID_ADB_SERVER_PORT'):
        adb += ['-P', os.environ['ANDROID_ADB_SERVER_PORT']]
    adb += ['-s', args.serial]
    run(adb + ['wait-for-device'], timeout=min(120, args.timeout))
    props = {}
    for name in ('ro.product.model', 'ro.product.cpu.abi', 'ro.build.version.sdk', 'ro.build.display.id'):
        props[name] = run(adb + ['shell', 'getprop', name], timeout=30, capture_output=True).stdout.strip()
    props['kernelPageBytes'] = run(adb + ['shell', 'getconf', 'PAGE_SIZE'], timeout=30, capture_output=True).stdout.strip()
    (args.output / 'device.txt').write_text('\n'.join(key + '=' + value for key, value in props.items()) + '\n')
    run(adb + ['install', '-r', str(args.apk)], timeout=min(300, args.timeout))
    if args.suite in ('benchmark', 'attribution', 'control'):
        # Clear this synthetic test package's state, not merely an old report path on /sdcard (whose FUSE
        # transport may be unavailable). No hub, pairing or user data is involved in this test-only package.
        cleared = run(adb + ['shell', 'pm', 'clear', 'persona.core.rustcore.android.test'],
                      timeout=30, capture_output=True).stdout.strip()
        if cleared != 'Success':
            raise ValueError('cannot clear the synthetic benchmark package: ' + cleared)
    test = 'persona.core.rustcore.ContinuityBindingFixtureTest'
    if args.suite == 'benchmark':
        test = 'persona.core.rustcore.RustCoreDeviceBenchmark'
    elif args.suite == 'control':
        test = 'persona.core.rustcore.RustCoreDeviceControl'
    elif args.suite == 'attribution':
        test = 'persona.core.rustcore.RustCoreDeviceAttribution'
    nonce = uuid.uuid4().hex if args.suite in ('benchmark', 'attribution', 'control') else ''
    command = adb + ['shell', 'am', 'instrument', '-w', '-r']
    if nonce:
        argument = {'attribution': 'rustCoreAttributionNonce', 'control': 'rustCoreControlNonce'}.get(
            args.suite, 'rustCoreBenchmarkNonce')
        command += ['-e', argument, nonce]
    command += ['-e', 'class', test,
                'persona.core.rustcore.android.test/androidx.test.runner.AndroidJUnitRunner']
    started = time.monotonic()
    try:
        output = instrumentation_output(command, args.timeout)
    except subprocess.TimeoutExpired as error:
        output = build_output(error)
        (args.output / 'instrumentation.txt').write_text(output)
        benchmark_report(output, nonce, args, adb, props)
        raise ValueError(f'instrumentation TIMEOUT after {args.timeout}s')
    (args.output / 'instrumentation.txt').write_text(output)
    tests = re.findall(r'INSTRUMENTATION_STATUS: numtests=(\d+)', output)
    counts = {'tests': int(tests[-1]) if tests else 0,
              'failures': len(re.findall(r'INSTRUMENTATION_STATUS_CODE: -2', output)),
              'instrumentationCode': int(re.search(r'INSTRUMENTATION_CODE: (-?\d+)', output).group(1))
              if re.search(r'INSTRUMENTATION_CODE: (-?\d+)', output) else None}
    (args.output / 'counts.json').write_text(json.dumps(counts, indent=2) + '\n')
    expected = 1 if args.suite in ('benchmark', 'attribution', 'control') else None
    if args.suite == 'replay':
        identities = fixture_identities(args.apk)
        expected = len(identities)
        (args.output / 'fixture-count.txt').write_text(str(expected) + '\n')
    elif args.suite == 'attribution':
        identities = [('persona.core.rustcore.RustCoreDeviceAttribution', 'encodingJniQueueLockDecodeApplyAndGc')]
    elif args.suite == 'control':
        identities = [('persona.core.rustcore.RustCoreDeviceControl', 'cpuAndJniWithoutProcInstrumentation')]
    else:
        identities = [('persona.core.rustcore.RustCoreDeviceBenchmark', 'dispatchAndFrameCosts')]
    if counts != {'tests': expected, 'failures': 0, 'instrumentationCode': -1}:
        benchmark_report(output, nonce, args, adb, props)
        raise ValueError('real JUnit result is not a pass (original benchmark budgets and report failures remain enforced): '
                         + str(counts) + '; see instrumentation.txt')
    parse_junit(output, identities)
    benchmark_report(output, nonce, args, adb, props)
    print('Android core device: %s (%s; %s; %s; %.1fs)' % (
        'DIAGNOSTIC CAPTURED' if args.suite in ('attribution', 'control') else 'PASS',
        args.serial, args.suite, counts, time.monotonic() - started))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, TypeError, json.JSONDecodeError, subprocess.CalledProcessError) as error:
        sys.exit('Android core device: FAILED (' + str(error) + ')')
