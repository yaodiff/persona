import base64
import copy
import unittest
from promote_hub_release import validate_next


class ReleaseOrderTests(unittest.TestCase):
    def setUp(self):
        self.old = {'build':202611070001, 'commit':'abcdef123456'+'0'*28}
        self.new = {'build':202611070002, 'commit':'0123456789ab'+'0'*28,
                    'ancestors':base64.b64encode(bytes.fromhex('abcdef1234560123456789ab')).decode(),
                    'files':{'linux-amd64':{}}}

    def test_newer_descendant(self):
        validate_next(self.old,self.new)

    def test_out_of_order_or_same_number(self):
        for number in [202611070000,202611070001]:
            with self.subTest(number=number), self.assertRaises(ValueError):
                self.new['build']=number
                validate_next(self.old,self.new)

    def test_other_branch_and_malformed_ancestry(self):
        for data in [b'xxxxxx',b'abcdefg']:
            with self.subTest(data=data), self.assertRaises(ValueError):
                self.new['ancestors']=base64.b64encode(data).decode()
                validate_next(self.old,self.new)

    def test_no_other_platform_or_programs(self):
        for key,value in [('files',{'darwin-arm64':{}}),('programs',{'other':{}})]:
            with self.subTest(key=key),self.assertRaises(ValueError):
                changed=copy.deepcopy(self.new);changed[key]=value
                validate_next(None,changed)

    def test_identical_publication_can_be_reconciled(self):
        validate_next(self.new,self.new)

class WorkerRecoveryTests(unittest.TestCase):
    def test_interrupted_publication_reuses_build_and_validated_artifact(self):
        import json
        from pathlib import Path
        import tempfile
        from unittest.mock import patch
        import hub_release
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / 'state'
            state.mkdir()
            sha = 'a' * 40
            build = '202611070001'
            attempt = state / f'{build}-{sha[:12]}'
            attempt.mkdir()
            (attempt / 'latest.json').write_text('{}')
            (attempt / 'validation.log').write_text('passed\n')
            (state / 'last.json').write_text(json.dumps({'sha':sha,'build':build,'result':'pending',
                'repository':'example/project','source_branch':'main','update_url':'file:///example'}))
            cfg = root / 'config.json'
            cfg.write_text(json.dumps({'checkout':directory,'state_dir':str(state),
                                       'repository':'example/project','update_url':'file:///example',
                                       'publish_command':['publisher']}))
            calls = []
            def output(argv, cwd):
                return sha if argv[1] == 'rev-parse' else ('https://github.com/example/project.git' if argv[1] == 'remote' else '')
            def run(argv, cwd, **kwargs):
                calls.append(argv)
            with patch('sys.argv',['hub_release','--config',str(cfg)]), patch.object(hub_release,'output',output), patch.object(hub_release,'run',run):
                hub_release.main()
            self.assertFalse(any('build_hub_release.py' in ' '.join(c) for c in calls))
            self.assertTrue(any(c[0]=='publisher' for c in calls))
            self.assertEqual(json.loads((state/'last.json').read_text())['build'],build)
            self.assertEqual((attempt/'validation.log').read_text(),'passed\n')

    def test_failed_validation_never_invokes_publisher(self):
        import json
        from pathlib import Path
        import subprocess
        import tempfile
        from unittest.mock import patch
        import hub_release
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cfg = root / 'config.json'
            cfg.write_text(json.dumps({'checkout':directory,'state_dir':str(root/'state'),
                                       'repository':'example/project','update_url':'file:///example',
                                       'publish_command':['publisher']}))
            calls = []
            def run(argv,cwd,**kwargs):
                calls.append(argv)
                if 'scripts/release/build_hub_release.py' in argv:
                    raise subprocess.CalledProcessError(1,argv)
            with patch('sys.argv',['hub_release','--config',str(cfg)]), patch.object(hub_release,'output',lambda argv,cwd:'b'*40 if argv[1]=='rev-parse' else ('https://github.com/example/project.git' if argv[1]=='remote' else '')), patch.object(hub_release,'run',run):
                with self.assertRaises(subprocess.CalledProcessError):
                    hub_release.main()
            self.assertFalse(any(c[0]=='publisher' for c in calls))
            result = json.loads((root/'state/last.json').read_text())
            self.assertEqual(result['result'],'failure')
            self.assertGreater(result['retry_after'],0)


class NodeCacheTests(unittest.TestCase):
    def test_hub_only_cache_does_not_satisfy_node_publication(self):
        import json
        import tempfile
        from pathlib import Path
        from hub_release import cache_ready
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'latest.json').write_text(json.dumps({'version': 'fixture'}))
            self.assertTrue(cache_ready(root, False))
            self.assertFalse(cache_ready(root, True))

    def test_enabling_nodes_revisits_successful_hub_only_release(self):
        import json
        import tempfile
        from pathlib import Path
        from unittest.mock import patch
        import hub_release
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state = root / 'state'
            state.mkdir()
            sha, build = 'a' * 40, '202611070001'
            attempt = state / f'{build}-{sha[:12]}'
            attempt.mkdir()
            (attempt / 'latest.json').write_text('{}')
            (state / 'last.json').write_text(json.dumps({'sha': sha, 'build': build,
                'result': 'success', 'evidence': str(attempt)}))
            config = root / 'config.json'
            config.write_text(json.dumps({'checkout': directory, 'state_dir': str(state),
                'repository': 'example/project', 'update_url': 'file:///fixture',
                'publish_command': ['publisher'], 'publish_nodes': True}))
            calls = []
            with patch('sys.argv', ['worker', '--config', str(config)]), \
                    patch.object(hub_release, 'output', lambda args, cwd: sha if args[1] == 'rev-parse' else ('https://github.com/example/project.git' if args[1] == 'remote' else '')), \
                    patch.object(hub_release, 'run', lambda args, cwd, **kwargs: calls.append(args)):
                hub_release.main()
            builds = [args for args in calls if 'scripts/release/build_hub_release.py' in args]
            self.assertEqual(len(builds), 1)
            self.assertIn('--publish-nodes', builds[0])
            self.assertTrue(any(args[0] == 'publisher' for args in calls))


class FailureBackoffTests(unittest.TestCase):
    """A red main must not turn every timer firing into a full rebuild of the newest head (#7288)."""

    def setUp(self):
        import json
        import tempfile
        from pathlib import Path
        self.json = json
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.state = self.root / 'state'
        self.config = self.root / 'config.json'
        self.config.write_text(json.dumps({'checkout': self.directory.name, 'state_dir': str(self.state),
                                           'repository': 'example/project', 'update_url': 'file:///example',
                                           'publish_command': ['publisher']}))

    def attempt(self, sha, fails, *flags, now=None):
        """Run the worker against head sha; returns the build commands it ran."""
        import subprocess
        import time
        from unittest.mock import patch
        import hub_release
        calls = []
        clock = time.time if now is None else (lambda: now)

        def run(argv, cwd, **kwargs):
            calls.append(argv)
            if fails and 'scripts/release/build_hub_release.py' in argv:
                raise subprocess.CalledProcessError(1, argv)

        def output(argv, cwd):
            return sha if argv[1] == 'rev-parse' else (
                'https://github.com/example/project.git' if argv[1] == 'remote' else '')
        with patch('sys.argv', ['hub_release', '--config', str(self.config), *flags]), \
                patch.object(hub_release, 'output', output), patch.object(hub_release, 'run', run), \
                patch.object(hub_release.time, 'time', clock):
            try:
                hub_release.main()
            except subprocess.CalledProcessError:
                pass
        return [c for c in calls if 'scripts/release/build_hub_release.py' in c]

    def last(self):
        return self.json.loads((self.state / 'last.json').read_text())

    def test_backoff_grows_across_new_heads_and_caps(self):
        from hub_release import failure_backoff
        self.assertEqual([failure_backoff(n) // 60 for n in range(1, 7)], [15, 30, 60, 120, 120, 120])
        start = 2_000_000_000
        self.assertEqual(len(self.attempt('a' * 40, True, now=start)), 1)
        self.assertEqual((self.last()['failures'], self.last()['retry_after']), (1, start + 15 * 60))
        # A new head 12 minutes later waits instead of rebuilding.
        self.assertEqual(self.attempt('b' * 40, True, now=start + 12 * 60), [])
        self.assertEqual(self.last()['sha'], 'a' * 40)
        # Once the backoff has passed the newest head is built, and the next wait doubles.
        self.assertEqual(len(self.attempt('b' * 40, True, now=start + 16 * 60)), 1)
        self.assertEqual((self.last()['sha'], self.last()['failures']), ('b' * 40, 2))
        self.assertEqual(self.last()['retry_after'], start + 16 * 60 + 30 * 60)
        self.assertEqual(self.attempt('c' * 40, False, now=start + 40 * 60), [])

    def test_retry_runs_now_and_success_resets_the_count(self):
        start = 2_000_000_000
        self.attempt('a' * 40, True, now=start)
        self.assertEqual(len(self.attempt('b' * 40, False, '--retry', now=start + 60)), 1)
        self.assertEqual((self.last()['result'], self.last()['failures']), ('success', 0))
        self.assertEqual(len(self.attempt('c' * 40, True, now=start + 120)), 1)
        self.assertEqual((self.last()['failures'], self.last()['retry_after']), (1, start + 120 + 15 * 60))

    def test_interrupted_attempt_keeps_the_count(self):
        start = 2_000_000_000
        self.state.mkdir()
        (self.state / 'last.json').write_text(self.json.dumps({
            'repository': 'example/project', 'source_branch': 'main', 'update_url': 'file:///example',
            'sha': 'a' * 40, 'build': '202611070001', 'result': 'pending', 'failures': 3}))
        self.assertEqual(len(self.attempt('b' * 40, True, now=start)), 1)
        self.assertEqual((self.last()['failures'], self.last()['retry_after']), (4, start + 2 * 60 * 60))

    def test_failed_attempts_are_pruned(self):
        for index in range(8):
            self.attempt(f'{index:x}' * 40, True, '--retry')
        attempts = sorted(p.name for p in self.state.iterdir() if p.is_dir())
        self.assertEqual(len(attempts), 5)
        self.assertTrue(attempts[-1].endswith('7' * 12))

    def test_go_cache_over_the_limit_is_cleaned(self):
        from unittest.mock import patch
        import hub_release
        cache = self.root / 'go-build'
        (cache / 'aa').mkdir(parents=True)
        with (cache / 'aa' / 'entry-d').open('wb') as entry:
            entry.truncate(3 * 2 ** 20)
        for limit, cleaned in ((1 / 1024, True), (4 / 1024, False)):
            calls = []
            with patch.object(hub_release, 'output', lambda argv, cwd: str(cache)), \
                    patch.object(hub_release, 'run', lambda argv, cwd, **kwargs: calls.append(argv)):
                hub_release.trim_go_cache(self.root, limit)
            self.assertEqual(calls, [['go', 'clean', '-cache']] if cleaned else [])

    def test_go_cache_limit_must_be_positive(self):
        config = self.json.loads(self.config.read_text())
        self.config.write_text(self.json.dumps(dict(config, go_cache_limit_gib=0)))
        with self.assertRaises(ValueError):
            self.attempt('a' * 40, False)


if __name__ == '__main__':
    unittest.main()
