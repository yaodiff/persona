#!/usr/bin/env python3
"""Run a serialized Hub validation/build/publication cycle from a configured source."""
import argparse
import datetime as dt
import fcntl
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import time
import shutil
import sys
import uuid

from manager_artifacts import manager_manifest
from node_artifacts import node_manifest

FAILURE_BACKOFF_SECONDS = 15 * 60
FAILURE_BACKOFF_MAX_SECONDS = 2 * 60 * 60
DEFAULT_GO_CACHE_LIMIT_GIB = 20
KEPT_ATTEMPTS = 5


def run(args, cwd, **kwargs):
    return subprocess.run(args, cwd=cwd, check=True, **kwargs)


def output(args, cwd):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def failure_backoff(failures):
    """Seconds to wait after the given number of consecutive failed attempts: 15 min, doubling, at most 2 h."""
    return min(FAILURE_BACKOFF_SECONDS * 2 ** (max(failures, 1) - 1), FAILURE_BACKOFF_MAX_SECONDS)


def directory_size(path):
    total = 0
    for parent, _, files in os.walk(path):
        for name in files:
            try:
                total += os.lstat(os.path.join(parent, name)).st_size
            except OSError:
                pass
    return total


def trim_go_cache(checkout, limit_gib):
    """Go only drops cache entries unused for five days; a worker validating every new head fills tens of GB a day."""
    cache = output(['go', 'env', 'GOCACHE'], checkout / 'server')
    if not cache or not os.path.isabs(cache) or not os.path.isdir(cache):
        return
    size = directory_size(cache)
    if size > limit_gib * 2 ** 30:
        print(f'Go build cache is {size / 2 ** 30:.1f} GiB, over {limit_gib} GiB; cleaning it', flush=True)
        run(['go', 'clean', '-cache'], checkout / 'server')


def prune_attempts(target_state, keep=KEPT_ATTEMPTS):
    """Keep recent attempts for diagnosis; installed/rollback binaries live on the Hub."""
    attempts = sorted((p for p in target_state.iterdir() if p.is_dir() and (p / 'validation.log').exists()),
                      key=lambda p: p.name)
    for old in attempts[:-keep]:
        shutil.rmtree(old)


def atomic_json(path, value):
    temp = path.with_suffix('.tmp')
    temp.write_text(json.dumps(value, indent=2) + '\n')
    os.replace(temp, path)


def cache_ready(attempt, publish_nodes, publish_managers=False, source=None, build=None):
    latest = Path(attempt) / 'latest.json'
    if not latest.exists():
        return False
    manifest = json.loads(latest.read_text())
    if publish_nodes and node_manifest(attempt, manifest) is None:
        return False
    if publish_managers:
        if manager_manifest(attempt, source, build) is None:
            return False
    return True


def publication_target(cfg, scope, manager_id):
    """Resolve one explicit target; no manager may inherit the Hub publisher."""
    if scope == 'hub':
        if manager_id is not None:
            raise ValueError('--manager-id requires --scope manager')
        return cfg['publish_command'], None
    if not manager_id or not re.fullmatch(r'[a-z][a-z0-9-]{0,31}', manager_id):
        raise ValueError('Manager publication requires an explicit valid --manager-id')
    targets = cfg.get('manager_targets', {})
    if manager_id not in targets:
        raise ValueError('Manager publication target is not configured')
    target = targets[manager_id]
    command = target.get('publish_command') if isinstance(target, dict) else None
    if (not isinstance(command, list) or not command
            or any(not isinstance(part, str) or not part for part in command)):
        raise ValueError('Manager target publish_command must be a nonempty argument list')
    if command.count('--config') != 1 or command.index('--config') + 1 >= len(command):
        raise ValueError('Manager target publisher requires one explicit --config file')
    config = Path(command[command.index('--config') + 1]).expanduser()
    if not config.is_absolute():
        config = Path(cfg['checkout']) / config
    if config.is_symlink() or not config.is_file():
        raise ValueError('Manager target publisher config must be a regular file')
    identity_input = dict(target=target, config_sha256=hashlib.sha256(config.read_bytes()).hexdigest())
    identity = hashlib.sha256(json.dumps(identity_input, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return command, identity




def repository_identity(remote):
    match = re.fullmatch(
        r'(?:https://github\.com/|ssh://git@github\.com/|git@github\.com:)'
        r'([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?', remote)
    if not match:
        raise ValueError('origin must be a canonical GitHub repository URL')
    return match.group(1).casefold()

def full_commit(value):
    if not re.fullmatch(r'[0-9a-f]{40}', value):
        raise argparse.ArgumentTypeError('commit must be a full lowercase Git SHA')
    return value

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--retry', action='store_true')
    parser.add_argument('--scope', choices=('hub', 'manager', 'node'))
    parser.add_argument('--node-id', help='one explicit target from node_targets')
    parser.add_argument('--artifact-dir', type=Path, help='existing validated full release; node scope only')
    parser.add_argument('--manager-id', help='explicit manager target from manager_targets')
    parser.add_argument('--commit', type=full_commit,
                        help='pin a full commit that must belong to the configured source branch')
    parser.add_argument('--validate-only', action='store_true',
                        help='run complete build gates without publishing')
    args = parser.parse_args()
    cfg = json.loads(args.config.read_text())
    args.scope = args.scope or cfg.get('scope', 'hub')
    if args.scope == 'node':
        args.node_id = args.node_id or cfg.get('node_id')
        if args.commit is None and cfg.get('node_commit') is not None:
            args.commit = full_commit(cfg['node_commit'])
        if args.artifact_dir is None and cfg.get('node_artifact_dir') is not None:
            args.artifact_dir = Path(cfg['node_artifact_dir'])
        args.validate_only = args.validate_only or cfg.get('node_apply') is not True
        from pinned_node_release import run_pinned_node
        run_pinned_node(args, cfg)
        return
    if args.node_id is not None or args.artifact_dir is not None:
        parser.error('--node-id and --artifact-dir require --scope node')
    if args.scope not in ('hub', 'manager'):
        parser.error('Configured scope must be hub or manager')
    if args.scope == 'manager':
        args.manager_id = args.manager_id or cfg.get('manager_id')
        if args.commit is None and cfg.get('manager_commit') is not None:
            try:
                args.commit = full_commit(cfg['manager_commit'])
            except (argparse.ArgumentTypeError, TypeError) as error:
                parser.error(str(error))
    if args.scope == 'manager' and not args.commit:
        parser.error('--scope manager requires --commit with a full source SHA')
    publish_command, target_identity = publication_target(cfg, args.scope, args.manager_id)
    go_cache_limit = cfg.get('go_cache_limit_gib', DEFAULT_GO_CACHE_LIMIT_GIB)
    if isinstance(go_cache_limit, bool) or not isinstance(go_cache_limit, (int, float)) or go_cache_limit <= 0:
        raise ValueError('go_cache_limit_gib must be a positive number')
    publish_nodes = args.scope == 'hub' and cfg.get('publish_nodes', False)
    publish_managers = args.scope == 'manager'
    checkout, state = Path(cfg['checkout']), Path(cfg['state_dir'])
    branch = cfg.get('source_branch', 'main')
    source_ref = f'refs/heads/{branch}'
    tracking_ref = f'refs/remotes/origin/{branch}'
    run(['git', 'check-ref-format', source_ref], checkout)
    origin = output(['git', 'remote', 'get-url', 'origin'], checkout)
    if repository_identity(origin) != cfg['repository'].casefold():
        raise ValueError('Configured status repository does not match source origin')
    state.mkdir(parents=True, exist_ok=True)
    with (state / 'lock').open('w') as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print('Another Hub release owns the publication window.')
            if publish_managers and not args.validate_only:
                raise SystemExit(3)
            return
        run(['git', 'fetch', '--no-tags', 'origin', f'{source_ref}:{tracking_ref}'], checkout)
        sha = args.commit or output(['git', 'rev-parse', tracking_ref], checkout)
        if args.commit:
            run(['git', 'merge-base', '--is-ancestor', sha, tracking_ref], checkout)

        def status(value, description):
            context = ('manager-release/validation' if publish_managers and args.validate_only else
                       'manager-release/worker' if publish_managers else
                       'hub-release/validation' if args.validate_only else 'hub-release/worker')
            if cfg.get('report_commit_status') is not True:
                print(f'GitHub commit status disabled: {context} {value}', flush=True)
                return
            run(['gh', 'api', f'repos/{cfg["repository"]}/statuses/{sha}',
                 '-f', f'state={value}', '-f', f'context={context}',
                 '-f', f'description={description}'], checkout, stdout=subprocess.DEVNULL)

        target_state = state / 'manager' / args.manager_id if publish_managers else state
        target_state.mkdir(parents=True, exist_ok=True)
        record = target_state / 'last.json'
        previous = json.loads(record.read_text()) if record.exists() else {}

        def ready(attempt, build):
            try:
                return cache_ready(attempt, publish_nodes, publish_managers, sha, build)
            except (ValueError, OSError, KeyError):
                if publish_managers:
                    previous.update(result='failure', retry_after=time.time() + 900)
                    atomic_json(record, previous)
                    status('failure', 'Manager release cache failed integrity verification')
                raise

        source_matches = (previous.get('repository') == cfg['repository'] and
                          previous.get('source_branch') == branch and
                          previous.get('update_url') == cfg['update_url'])
        same = source_matches and previous.get('sha') == sha
        artifact_requirements_match = (previous.get('publish_nodes', False) == publish_nodes
                                       and previous.get('scope', 'hub') == args.scope)
        requirements_match = (artifact_requirements_match
                              and previous.get('target_identity') == target_identity)
        if same and not args.retry and requirements_match:
            success_ready = (previous.get('result') == 'success' and
                             (not (publish_nodes or publish_managers) or
                              ready(previous.get('evidence', ''), previous.get('build'))))
            # A manager artifact cache is not a live activation receipt. Every
            # invocation must obtain a fresh run-bound publisher confirmation.
            if ((success_ready and not publish_managers) or
                    (previous.get('result') != 'success' and time.time() < previous.get('retry_after', 0))):
                print(f'{sha}: {previous["result"]}; no new attempt needed')
                if publish_managers and not success_ready and not args.validate_only:
                    raise SystemExit(3)
                return
        # Consecutive failures in a row, carried through an interrupted (pending) attempt and reset by a success.
        failures = (previous.get('failures', 1 if previous.get('result') == 'failure' else 0)
                    if source_matches and previous.get('result') != 'success' else 0)
        # While main stays red each new head would be a full rebuild; following the head waits out the backoff
        # of the last failure, whichever commit it was. --retry and a pinned --commit run now.
        if (args.scope == 'hub' and not args.commit and not args.retry and not same and source_matches
                and previous.get('result') == 'failure' and time.time() < previous.get('retry_after', 0)):
            print(f'{sha}: waiting after {failures} consecutive failed attempts '
                  f'(last {previous.get("sha", "")[:12]}) until {time.ctime(previous["retry_after"])}')
            return
        if output(['git', 'status', '--porcelain', '--untracked-files=no'], checkout):
            raise RuntimeError('Release checkout has tracked changes')
        run(['git', 'checkout', '--detach', sha], checkout)
        now = dt.datetime.now(dt.timezone.utc).replace(second=0, microsecond=0)
        reusable_manager_artifact = (publish_managers and same and artifact_requirements_match
                                     and previous.get('evidence')
                                     and ready(previous['evidence'], previous.get('build')))
        missing_required_cache = ((publish_managers and same and not reusable_manager_artifact)
                                  or (same and publish_nodes and previous.get('evidence')
                                      and (Path(previous['evidence']) / 'latest.json').exists()
                                      and not ready(previous['evidence'], previous.get('build'))))
        if (same and (requirements_match or reusable_manager_artifact)
                and not missing_required_cache):
            build = previous['build']
        else:
            last = dt.datetime.strptime(previous.get('build', '200001010000'), '%Y%m%d%H%M').replace(tzinfo=dt.timezone.utc)
            build = max(now, last + dt.timedelta(minutes=1)).strftime('%Y%m%d%H%M')
        attempt = target_state / f'{build}-{sha[:12]}'
        attempt.mkdir(exist_ok=True)
        result = {'repository': cfg['repository'], 'source_branch': branch,
                  'update_url': cfg['update_url'],
                  'sha': sha, 'build': build, 'result': 'pending', 'evidence': str(attempt),
                  'publish_nodes': publish_nodes, 'scope': args.scope,
                  'report_commit_status': cfg.get('report_commit_status') is True,
                  'run_id': uuid.uuid4().hex,
                  'manifest_source': sha, 'failures': failures}
        if publish_managers:
            result.update(manager_id=args.manager_id, target_identity=target_identity)
        atomic_json(record, result)

        try:
            label = 'Manager' if publish_managers else 'Hub'
            status('pending', 'Validating release' if args.validate_only else f'Validating and publishing {label} release')
            with (attempt / 'validation.log').open('a') as log:
                if not ready(attempt, build):
                    command = [sys.executable, 'scripts/release/build_hub_release.py', '--out', str(attempt),
                               '--build', build, '--update-url', cfg['update_url']]
                    if publish_nodes:
                        command.append('--publish-nodes')
                    if publish_managers:
                        command.append('--publish-managers')
                    run(command, checkout, stdout=log, stderr=subprocess.STDOUT)
            # A force-push or branch replacement must never publish an orphan.
            run(['git', 'fetch', '--no-tags', 'origin', f'{source_ref}:{tracking_ref}'], checkout)
            run(['git', 'merge-base', '--is-ancestor', sha, tracking_ref], checkout)
            if publish_managers:
                manifest = manager_manifest(attempt, sha, build)
                if manifest is None:
                    raise ValueError('Manager manifest is missing after validation')
                result['manifest_source'] = manifest['commit']
                current = json.loads(args.config.read_text())
                if publication_target(current, args.scope, args.manager_id)[1] != target_identity:
                    raise ValueError('Manager publication target changed during validation')
            if args.validate_only:
                result['result'] = 'validated'
                status('success', 'Complete release validation passed; publication was not requested')
                return
            with (attempt / 'publication.log').open('a') as log:
                atomic_json(attempt / 'worker-receipt.json', result)
                command = [*publish_command, '--artifact-dir', str(attempt)]
                if publish_managers:
                    (attempt / 'manager-receipt.json').unlink(missing_ok=True)
                    command.extend(['--run-id', result['run_id'], '--manager-id', args.manager_id])
                run(command, checkout,
                    stdout=log, stderr=subprocess.STDOUT)
            if publish_managers:
                receipt = json.loads((attempt / 'manager-receipt.json').read_text())
                if any(receipt.get(key) != expected for key, expected in (
                        ('manager_id', args.manager_id), ('run_id', result['run_id']), ('source', sha))):
                    raise ValueError('Manager publisher receipt does not match this transaction')
                if receipt.get('phase') == 'checked':
                    result.update(result='checked', retry_after=time.time() + 900)
                    status('pending', 'Manager package checked; activation has not been confirmed')
                    raise SystemExit(3)
                if receipt.get('phase') != 'confirmed':
                    raise ValueError('Manager publisher did not confirm activation')
            result.update(result='success', failures=0)
            status('success', f'Validated {label} release publisher completed')
        except (subprocess.CalledProcessError, OSError, RuntimeError, ValueError):
            result['result'] = 'failure'
            result['failures'] = failures + 1
            result['retry_after'] = time.time() + failure_backoff(failures + 1)
            status('failure', 'Release failed; see publisher validation logs')
            raise
        finally:
            atomic_json(record, result)
            atomic_json(attempt / 'worker-receipt.json', result)
            # Failed attempts too: a red main must not grow the state directory or the build cache without bound.
            # Cleanup problems are reported but never replace the attempt's own result.
            try:
                prune_attempts(target_state)
                trim_go_cache(checkout, go_cache_limit)
            except (OSError, subprocess.CalledProcessError) as error:
                print(f'Release cleanup failed: {error}', file=sys.stderr, flush=True)


if __name__ == '__main__':
    main()
