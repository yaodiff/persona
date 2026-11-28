"""Only bounded, identity-checked, idempotent node calls may be replayed."""
import base64
import copy
import unittest
import urllib.error
from unittest.mock import patch

import node_relay_retry as retry
import promote_node_release as promoter
import test_pinned_node_release as fixture


def http_error(code):
    return urllib.error.HTTPError('http://fixture', code, 'fixture', {}, None)


class NodeReconnectTests(unittest.TestCase):
    setUp = fixture.PinnedNodeTests.setUp
    relay = fixture.PinnedNodeTests.relay
    promote = fixture.PinnedNodeTests.promote

    def fault(self, handler):
        original = self.relay
        def call(root, target, action, params=None, **kwargs):
            self.assertEqual(target, self.payload['target'])
            self.assertGreater(kwargs.get('timeout', 50), 0)
            limit = promoter.COMMIT_WAIT if action == 'commit' else 50
            self.assertLessEqual(kwargs.get('timeout', 50), limit)
            return handler(original, root, target, action, params, kwargs)
        self.relay = call

    def test_chunk_lost_reply_replays_exact_transaction_offset_and_bytes(self):
        seen = []
        def handler(original, root, target, action, params, kwargs):
            if action == 'chunk':
                seen.append(copy.deepcopy(params))
                if len(seen) == 1:
                    original(root, target, action, params)
                    raise http_error(502)
                self.assertEqual(params, seen[0])
                # Resident checks these bytes before acknowledging an old offset.
                self.assertEqual(self.transferred[params['offset']:], base64.b64decode(params['data']))
                self.calls.append(action)
                return {'offset': len(self.transferred)}
            return original(root, target, action, params)
        self.fault(handler)
        with patch.object(retry.time, 'sleep'):
            self.assertEqual(self.promote(True)['phase'], 'confirmed')
        self.assertEqual(len(seen), 2)
        self.assertEqual(self.calls.count('begin'), 1)
        self.assertNotIn('abort', self.calls)

    def test_identity_conflict_does_not_retry_or_commit(self):
        attempts = []
        def handler(original, root, target, action, params, kwargs):
            if action == 'chunk':
                attempts.append(params)
                raise http_error(409)
            return original(root, target, action, params)
        self.fault(handler)
        with self.assertRaises(urllib.error.HTTPError):
            self.promote(True)
        self.assertEqual(len(attempts), 1)
        self.assertEqual(self.calls[-1], 'abort')
        self.assertNotIn('commit', self.calls)

    def test_persistent_disconnect_bounded_without_new_transaction(self):
        attempts = []
        def handler(original, root, target, action, params, kwargs):
            if action == 'chunk':
                attempts.append(params)
                raise http_error(503)
            return original(root, target, action, params)
        self.fault(handler)
        with patch.object(retry.time, 'sleep'), self.assertRaises(urllib.error.HTTPError):
            self.promote(True)
        self.assertEqual(len(attempts), retry.RECONNECT_ATTEMPTS)
        self.assertTrue(all(p == attempts[0] for p in attempts))
        self.assertEqual(self.calls.count('begin'), 1)
        self.assertNotIn('commit', self.calls)

    def test_commit_lost_reply_never_replays_commit_or_aborts_accepted_transaction(self):
        commits = []
        def handler(original, root, target, action, params, kwargs):
            result = original(root, target, action, params)
            if action == 'commit':
                commits.append(params)
                raise http_error(504)
            return result
        self.fault(handler)
        self.assertEqual(self.promote(True)['phase'], 'confirmed')
        self.assertEqual(len(commits), 1)
        self.assertNotIn('abort', self.calls)
        self.assertEqual(self.calls[-1], 'confirm')

    def test_commit_is_read_as_long_as_the_relay_holds_it(self):
        # The node answers commit after its first-run self-checks (60 s each); a 50 s read took that for a lost
        # reply (#12670). The read is still bounded by the original deadline.
        for deadline, want in ((900, promoter.COMMIT_WAIT), (120, 120)):
            with self.subTest(deadline=deadline):
                self.setUp()
                timeouts = {}
                def handler(original, root, target, action, params, kwargs):
                    timeouts.setdefault(action, kwargs.get('timeout', 50))
                    return original(root, target, action, params)
                self.fault(handler)
                self.payload['timeout'] = deadline
                with patch.object(promoter.time, 'monotonic', return_value=1000.0):
                    self.assertEqual(self.promote(True)['phase'], 'confirmed')
                self.assertEqual(timeouts['commit'], want)
                self.assertLessEqual(timeouts['chunk'], 50)
        self.assertGreater(promoter.COMMIT_WAIT, 60)

    def test_ambiguous_commit_without_matching_handshake_never_confirms(self):
        clock = [0]
        def handler(original, root, target, action, params, kwargs):
            if action == 'commit':
                self.calls.append(action)
                raise http_error(504)
            if action == 'info' and 'commit' in self.calls:
                clock[0] += 1000
            return original(root, target, action, params)
        self.fault(handler)
        with patch.object(promoter.time, 'monotonic', side_effect=lambda: clock[0]), \
                patch.object(promoter.time, 'sleep'):
            with self.assertRaisesRegex(TimeoutError, 'not confirmed'):
                self.promote(True)
        self.assertEqual(self.calls.count('commit'), 1)
        self.assertNotIn('abort', self.calls)
        self.assertNotIn('confirm', self.calls)

    def test_interrupted_transfer_still_aborts_original_transaction(self):
        def handler(original, root, target, action, params, kwargs):
            if action == 'chunk':
                raise KeyboardInterrupt()
            return original(root, target, action, params)
        self.fault(handler)
        with self.assertRaises(KeyboardInterrupt):
            self.promote(True)
        self.assertEqual(self.calls[-1], 'abort')
        self.assertNotIn('commit', self.calls)

    def test_abort_failure_preserves_original_transfer_failure(self):
        def handler(original, root, target, action, params, kwargs):
            if action == 'chunk':
                raise http_error(409)
            if action == 'abort':
                raise OSError('abort disconnected')
            return original(root, target, action, params)
        self.fault(handler)
        with self.assertRaisesRegex(RuntimeError, '409.*abort also failed: abort disconnected'):
            self.promote(True)

    def test_exhausted_deadline_prevents_any_call(self):
        with patch.object(retry.time, 'monotonic', return_value=10):
            with self.assertRaises(TimeoutError):
                retry.request(lambda *a, **k: self.fail('sent after deadline'), None, {}, 'chunk', 10, retry=True)

    def test_retry_timeout_and_delay_consume_original_deadline(self):
        clock = [0.0]
        timeouts = []
        def relay(*args, **kwargs):
            timeouts.append(kwargs['timeout'])
            clock[0] += 0.5
            raise http_error(503)
        def sleep(duration):
            clock[0] += duration
        with patch.object(retry.time, 'monotonic', side_effect=lambda: clock[0]), \
                patch.object(retry.time, 'sleep', side_effect=sleep):
            with self.assertRaises(urllib.error.HTTPError):
                retry.request(relay, None, {}, 'chunk', 3, retry=True)
        self.assertEqual(timeouts, [3, 0.5])
        self.assertEqual(clock[0], 3)


if __name__ == '__main__':
    unittest.main()
