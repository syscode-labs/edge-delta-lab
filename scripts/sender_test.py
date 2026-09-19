import csv
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
import tempfile
import subprocess
import sys
import time
import unittest
from unittest.mock import patch

from sender import open_db, sample, snapshot, render


def receipt(phase, sequence=2, release='v2', artifact='a' * 64, at=None):
    """Schema-valid synthetic UNIT fixture, not Docker execution evidence."""
    summary = dict(release=release, sequence=sequence, phase=phase,
                   artifact='archive.tar', artifact_sha256=artifact,
                   artifact_bytes=1024, unique_chunks=4, reused_chunks=2,
                   reused_raw_bytes=512, downloaded_chunks=2, downloaded_raw_bytes=512,
                   chunk_response_body_bytes=256, metadata_response_body_bytes=64,
                   useful_encoded_bytes=256, retry_overhead_body_bytes=0,
                   chunk_requests=2, metadata_requests=1, retries=0,
                   integrity_failures=0, elapsed_seconds=1)
    value = dict(id='', device_id='edge', release=release, phase=phase,
                 artifact_sha256=artifact,
                 recorded_at=datetime.fromtimestamp(at if at is not None else time.time(), timezone.utc).isoformat().replace('+00:00', 'Z'),
                 summary=summary)
    value['id'] = hashlib.sha256(json.dumps(value, separators=(',', ':')).encode()).hexdigest()
    return value


class SenderTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name) / 'sender.sqlite'
        self.db = open_db(self.path)

    def tearDown(self):
        self.db.close()
        self.temp.cleanup()

    def insert(self, at, epoch, count, reachable=1):
        stats = dict(origin_started=epoch, chunk_response_body_bytes=count,
                     metadata_response_body_bytes=0, object_requests={})
        self.db.execute('INSERT INTO samples VALUES(NULL,?,?,?, ?,NULL)',
                        (at, 'http://sender', reachable, json.dumps(stats)))
        self.db.commit()

    def collect(self, receipts, at=None, origin='http://sender'):
        stats = dict(origin_started='epoch1', chunk_requests=2,
                     chunk_response_body_bytes=256, metadata_response_body_bytes=64,
                     object_requests={'chunk-a': 2})
        with patch('sender.fetch', side_effect=[stats, receipts]) as fetch:
            with patch('sender.time.time', return_value=at if at is not None else time.time()):
                result = sample(self.db, origin)
        self.assertEqual([call.args[0] for call in fetch.call_args_list],
                         [origin + '/stats', origin + '/receipts'])
        return result

    def test_sender_bytes_do_not_claim_delivery(self):
        self.insert(time.time(), 'epoch1', 9000)
        value = snapshot(self.db)
        self.assertEqual(value['device_acknowledgements'], [])
        self.assertIn('UNKNOWN', render(value))

    def test_rate_is_reset_aware(self):
        now = time.time()
        self.insert(now - 1, 'one', 1000)
        self.insert(now, 'two', 2000)
        self.assertIsNone(snapshot(self.db)['response_body_bits_per_second'])
        self.insert(now + 1, 'two', 3000)
        self.assertEqual(snapshot(self.db)['response_body_bits_per_second'], 8000)

    def test_collector_retains_offline_history(self):
        self.insert(time.time() - 2, 'one', 6000)
        self.db.execute('INSERT INTO samples VALUES(NULL,?,?,0,NULL,?)',
                        (time.time(), 'http://sender', 'offline'))
        self.db.commit()
        result = snapshot(self.db)
        self.assertEqual(result['state'], 'unreachable')
        self.assertEqual(result['observed']['chunk_response_body_bytes'], 6000)

    def test_loaded_wins_both_receipt_orders_and_delayed_staged(self):
        now = time.time()
        staged = receipt('staged', at=now - 2)
        loaded = receipt('loaded', at=now - 1)
        # /receipts is filename ordered, not event ordered. Exercise tied local
        # observation times as well as a staged receipt first seen on a later poll.
        for order in ([loaded, staged], [staged, loaded]):
            with self.subTest(order=[r['phase'] for r in order]):
                self.db.execute('DELETE FROM receipts')
                self.collect(order, at=now)
                self.assertEqual(snapshot(self.db)['device_acknowledgements'][0]['phase'], 'loaded')
        self.db.execute('DELETE FROM receipts')
        self.collect([loaded], at=now)
        self.collect([staged], at=now + 1)
        self.assertEqual(snapshot(self.db)['device_acknowledgements'][0]['phase'], 'loaded')
        self.assertIn('Running/healthy is NOT inferred', render(snapshot(self.db)))

    def test_same_sequence_conflicts_do_not_claim_a_winning_identity(self):
        now = time.time()
        original = receipt('loaded', at=now - 2)
        for other in (receipt('staged', release='other-v2', at=now - 1),
                      receipt('staged', artifact='b' * 64, at=now - 1)):
            for order in ([original, other], [other, original]):
                with self.subTest(release=other['release'], artifact=other['artifact_sha256'],
                                  order=[r['phase'] for r in order]):
                    self.db.execute('DELETE FROM receipts')
                    self.collect(order)
                    state = snapshot(self.db)
                    self.assertEqual(state['device_acknowledgements'], [])
                    conflicts = state['acknowledgement_conflicts']
                    self.assertEqual(len(conflicts), 1)
                    self.assertEqual(conflicts[0]['device_id'], 'edge')
                    self.assertEqual(conflicts[0]['sequence'], 2)
                    self.assertEqual(len(conflicts[0]['identities']), 2)
                    self.assertTrue(all(r['identity_conflict'] for r in state['release_acknowledgements']))
                    self.assertIn('CONFLICT', render(state))
                    self.assertIn('UNKNOWN', render(state))

    def test_release_history_survives_newer_sequence_and_reopen(self):
        now = time.time()
        old = receipt('loaded', sequence=1, release='v1', at=now - 3)
        loaded = receipt('loaded', at=now - 1)
        staged = receipt('staged', at=now - 2)
        self.collect([loaded, old, staged])
        self.db.close()
        self.db = open_db(self.path)
        state = snapshot(self.db)
        self.assertEqual(state['device_acknowledgements'][0]['id'], loaded['id'])
        self.assertEqual([(r['release'], r['phase']) for r in state['release_acknowledgements']],
                         [('v1', 'loaded'), ('v2', 'loaded')])

    def test_stale_samples_keep_history_but_not_current_reachability_or_rate(self):
        now = time.time()
        self.insert(now - 86401, 'one', 1000)
        self.insert(now - 86400, 'one', 2000)
        with patch('sender.time.time', return_value=now):
            value = snapshot(self.db)
        self.assertEqual(value['state'], 'stale')
        self.assertEqual(value['observation_freshness'], 'stale')
        self.assertEqual(value['last_observation_state'], 'reachable')
        self.assertEqual(value['last_successful_sample_age_seconds'], 86400)
        self.assertEqual(value['observed']['chunk_response_body_bytes'], 2000)
        self.assertIsNone(value['response_body_bits_per_second'])
        self.assertEqual(value['last_measured_response_body_bits_per_second'], 8000)
        self.assertEqual(value['rate_window'], {'start': now - 86401, 'end': now - 86400})
        self.assertIn('STALE', render(value))
        self.assertIn('Historical', render(value))
        with patch('sender.time.time', return_value=now):
            self.assertEqual(snapshot(self.db, stale_after=90000)['state'], 'reachable')

    def test_recent_failure_does_not_make_old_counters_or_rate_current(self):
        now = time.time()
        self.insert(now - 86401, 'one', 1000)
        self.insert(now - 86400, 'one', 2000)
        with patch('sender.time.time', return_value=now), patch('sender.fetch', side_effect=OSError('offline')):
            sample(self.db, 'http://sender')
        value = snapshot(self.db)
        self.assertEqual(value['state'], 'unreachable')
        self.assertEqual(value['observed_freshness'], 'stale')
        self.assertIsNone(value['response_body_bits_per_second'])
        self.assertIn('Last successful counters', render(value))
        self.assertIn('STALE', render(value))

    def test_receipt_freshness_uses_first_observation_and_reported_event_age(self):
        now = time.time()
        old = receipt('loaded', at=now - 86400)
        self.collect([old], at=now - 86400)
        # Re-polling the persistent sender log is NOT a new device heartbeat.
        self.collect([old], at=now)
        with patch('sender.time.time', return_value=now):
            value = snapshot(self.db)
        ack = value['device_acknowledgements'][0]
        self.assertEqual(ack['first_observed_at'], now - 86400)
        self.assertEqual(ack['observation_age_seconds'], 86400)
        self.assertAlmostEqual(ack['reported_age_seconds'], 86400, places=5)
        self.assertEqual(ack['freshness'], 'stale')
        self.assertEqual(ack['identity_trust'], 'unauthenticated_lab')
        self.assertEqual(value['runtime_health'], 'unknown')
        self.assertIn('UNAUTHENTICATED', render(value))
        self.assertIn('STALE', render(value))
        self.assertIn('first observed', render(value))
        # Even a newly collected receipt may describe a very old event.
        self.db.execute('DELETE FROM receipts')
        self.collect([old], at=now)
        ack = snapshot(self.db)['device_acknowledgements'][0]
        self.assertEqual(ack['first_observed_at'], now)
        self.assertEqual(ack['freshness'], 'stale')

    def test_receipt_unknown_and_recent_freshness_are_not_health_or_identity_trust(self):
        now = time.time()
        for at, expected in ((now - 1, 'recent'), (now + 3600, 'unknown')):
            self.db.execute('DELETE FROM receipts')
            self.collect([receipt('loaded', at=at)], at=now)
            value = snapshot(self.db)
            ack = value['device_acknowledgements'][0]
            self.assertEqual(ack['freshness'], expected)
            self.assertEqual(ack['identity_trust'], 'unauthenticated_lab')
            self.assertEqual(value['runtime_health'], 'unknown')
        self.db.execute('DELETE FROM receipts')
        self.assertEqual(snapshot(self.db)['device_acknowledgements'], [])
        self.assertIn('UNKNOWN', render(snapshot(self.db)))

    def test_no_samples_and_never_successful_are_unknown_not_zero_counters(self):
        value = snapshot(self.db)
        self.assertEqual(value['observation_freshness'], 'unknown')
        self.assertEqual(value['runtime_health'], 'unknown')
        self.assertIn('UNKNOWN', render(value))
        with patch('sender.fetch', side_effect=OSError('offline')):
            sample(self.db, 'http://sender')
        value = snapshot(self.db)
        self.assertEqual(value['observed_freshness'], 'unknown')
        self.assertEqual(value['observed'], {})
        self.assertIn('counters: UNKNOWN', render(value))
        self.assertNotIn('Chunk body bytes: 0', render(value))

    def cli(self, *args):
        return subprocess.run([sys.executable, str(Path(__file__).with_name('sender.py')),
                               *args, '--db', str(self.path)],
                              capture_output=True, text=True, check=True)

    def test_full_history_exports_survive_reopen_and_preserve_legacy_files(self):
        now = time.time()
        old = receipt('loaded', sequence=1, release='v1', at=now - 80)
        staged = receipt('staged', at=now - 62)
        loaded = receipt('loaded', at=now - 61)
        conflict = receipt('staged', artifact='b' * 64, at=now - 61)
        expected = [old, loaded, staged, conflict]
        self.collect(expected, at=now - 60)
        self.collect(expected, at=now)  # Retries/poll duplicates remain one receipt.
        self.collect([loaded], at=now, origin='http://other-sender')
        with patch('sender.fetch', side_effect=OSError('offline, retry\nlater')):
            sample(self.db, 'http://sender')
        self.db.close()
        self.db = open_db(self.path)
        out = Path(self.temp.name) / 'report'
        self.cli('report', '--out', str(out))
        state = json.loads((out / 'status.json').read_text())
        history = json.loads((out / 'history.json').read_text())
        self.assertEqual(history['status'], state)
        self.assertEqual((out / 'status.txt').read_text(), render(state) + '\n')
        self.assertEqual(state['state'], 'unreachable')
        self.assertEqual(state['device_acknowledgements'], [])
        self.assertEqual(len(state['acknowledgement_conflicts']), 1)
        self.assertEqual(len(history['samples']), 4)
        self.assertEqual(len(history['receipts']), 5)
        originals = [r for r in history['receipts'] if r['origin'] == 'http://sender']
        self.assertEqual({r['id']: r['body'] for r in originals}, {r['id']: r for r in expected})
        self.assertTrue(all(r['first_observed_at'] == now - 60 for r in originals))
        with (out / 'samples.csv').open(newline='') as stream:
            legacy = csv.DictReader(stream)
            self.assertEqual(legacy.fieldnames, ['observed_unix_seconds', 'origin', 'reachable',
                             'chunk_body_bytes', 'metadata_body_bytes', 'chunk_requests',
                             'disconnects', 'http_503s', 'error'])
            self.assertEqual(len(list(legacy)), 4)
        with (out / 'samples-full.csv').open(newline='') as stream:
            rows = list(csv.DictReader(stream))
        self.assertEqual(len(rows), 4)
        for row, original in zip(rows, history['samples']):
            self.assertEqual(int(row['id']), original['id'])
            self.assertEqual(float(row['observed_at']), original['observed_at'])
            self.assertEqual(row['origin'], original['origin'])
            self.assertEqual(int(row['reachable']), original['reachable'])
            self.assertEqual(json.loads(row['stats_json']), original['stats'])
            self.assertEqual(row['error'], original['error'] or '')
        self.assertEqual(history['samples'][0]['stats']['object_requests'], {'chunk-a': 2})
        with (out / 'receipts.csv').open(newline='') as stream:
            rows = list(csv.DictReader(stream))
        self.assertEqual(len(rows), 5)
        for row, original in zip(rows, history['receipts']):
            self.assertEqual(row['origin'], original['origin'])
            self.assertEqual(row['id'], original['id'])
            self.assertEqual(float(row['first_observed_at']), original['first_observed_at'])
            self.assertEqual(json.loads(row['body_json']), original['body'])

    def test_receipt_poll_error_is_persistent_without_discarding_stats(self):
        result = self.collect(OSError('receipts disabled'))
        self.assertTrue(result['reachable'])
        self.assertEqual(result['receipt_error'], 'receipts disabled')
        self.db.close()
        self.db = open_db(self.path)
        state = snapshot(self.db)
        self.assertEqual(state['state'], 'reachable')
        self.assertEqual(state['observed']['chunk_requests'], 2)
        self.assertIn('receipts disabled', state['last_error'])
        self.assertIn('UNKNOWN', render(state))

    def test_cli_stale_threshold_is_configurable_and_rejects_invalid_values(self):
        self.insert(time.time() - 120, 'one', 1000)
        default = json.loads(self.cli('status', '--json').stdout)
        extended = json.loads(self.cli('status', '--json', '--stale-after', '3600').stdout)
        self.assertEqual(default['state'], 'stale')
        self.assertEqual(extended['state'], 'reachable')
        for value in ('0', '-1', 'nan', 'inf'):
            with self.subTest(value=value), self.assertRaises(subprocess.CalledProcessError):
                self.cli('status', '--stale-after', value)

    def test_higher_sequence_staged_is_not_promoted_by_previous_loaded_release(self):
        now = time.time()
        older = receipt('loaded', sequence=1, release='v1', at=now - 2)
        newer = receipt('staged', at=now - 1)
        self.collect([newer, older])
        state = snapshot(self.db)
        self.assertEqual(state['device_acknowledgements'][0]['id'], newer['id'])
        self.assertEqual(state['device_acknowledgements'][0]['phase'], 'staged')
        self.assertEqual(len(state['release_acknowledgements']), 2)

    def test_same_phase_event_selection_does_not_depend_on_poll_order(self):
        now = time.time()
        old = receipt('loaded', at=now - 60)
        new = receipt('loaded', at=now - 1)
        late_staged = receipt('staged', at=now)
        for order in ([old, new, late_staged], [late_staged, new, old]):
            self.db.execute('DELETE FROM receipts')
            self.collect(order)
            self.assertEqual(snapshot(self.db)['device_acknowledgements'][0]['id'], new['id'])

    def test_origin_identity_and_receipt_histories_are_isolated(self):
        now = time.time()
        first = receipt('loaded', at=now - 2)
        second = receipt('staged', artifact='b' * 64, at=now - 1)
        self.collect([first], origin='http://sender')
        self.collect([second], origin='http://other-sender')
        state = snapshot(self.db)
        self.assertEqual(state['device_acknowledgements'][0]['id'], second['id'])
        self.assertEqual(state['acknowledgement_conflicts'], [])
        self.collect([], origin='http://sender')
        self.assertEqual(snapshot(self.db)['device_acknowledgements'][0]['id'], first['id'])

    def test_rate_rejects_counter_regression_and_zero_time_window(self):
        now = time.time()
        self.insert(now - 1, 'one', 1000)
        self.insert(now, 'one', 500)
        self.assertIsNone(snapshot(self.db)['response_body_bits_per_second'])
        self.insert(now, 'one', 1000)
        self.assertIsNone(snapshot(self.db)['response_body_bits_per_second'])

    def test_rate_with_recent_end_but_old_start_is_historical(self):
        now = time.time()
        self.insert(now - 120, 'one', 1000)
        self.insert(now, 'one', 2000)
        state = snapshot(self.db)
        self.assertEqual(state['state'], 'reachable')
        self.assertIsNone(state['response_body_bits_per_second'])
        self.assertIsNotNone(state['last_measured_response_body_bits_per_second'])
        self.assertIn('NOT current throughput', render(state))

    def test_empty_report_and_no_ack_history_remain_unknown(self):
        out = Path(self.temp.name) / 'empty-report'
        self.cli('report', '--out', str(out))
        history = json.loads((out / 'history.json').read_text())
        self.assertEqual(history['samples'], [])
        self.assertEqual(history['receipts'], [])
        self.assertIn('UNKNOWN', (out / 'status.txt').read_text())
        self.collect([])
        self.assertEqual(snapshot(self.db)['repeated_chunk_requests'], 1)
        self.cli('report', '--out', str(out))
        self.assertIn('UNKNOWN', (out / 'status.txt').read_text())


if __name__ == '__main__':
    unittest.main()
