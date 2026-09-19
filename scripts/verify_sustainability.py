#!/usr/bin/env python3
"""Fail-closed checks for retained v3.1 Linux daemon evidence (not production readiness)."""
import json
from pathlib import Path
import sys

root = Path(sys.argv[1] if len(sys.argv) > 1 else 'evidence/v4-productization/v31-linux')
load = lambda p: json.loads((root / p).read_text())
idle, link, soak = load('idle.json'), load('link.json'), load('soak/soak.json')
assert load('environment.json')['os'] == 'Linux'
assert load('soak/environment.json')['binary_sha256'] == load('environment.json')['binary_sha256']
assert idle['window_seconds'] >= 600 and idle['all_daemons_alive_entire_window']
assert set(idle['devices']) == {'push-1', 'push-2', 'push-3', 'poll-1'}
for name, d in idle['devices'].items():
    assert all(v >= 0 for v in d.values())
    assert d['bytes_served'] <= 2048 and d['chunk_requests'] == 0
    assert (19 <= d['requests'] <= 21) if name == 'poll-1' else d['requests'] == 0
assert idle['after']['origin']['injected_503s'] == idle['before']['origin']['injected_503s']
assert link['all_daemons_alive'] and len(link['rows']) == 3
assert 'rate 5Mbit' in link['qdisc'] and 'delay 100ms' in link['qdisc']
assert 'Sent 0 bytes' not in link['qdisc']
rows = []
for row, baseline in zip(link['rows'], [17072868, 6819307, 249778]):
    s, d = row['summary'], row['server_delta']
    body = d['chunk_response_body_bytes']
    difference = (body - baseline) / baseline * 100
    assert abs(difference) <= 2
    assert s['phase'] == 'staged' and s['integrity_failures'] == 0 and s['retries'] == 0
    assert s['chunk_response_body_bytes'] == body
    assert row['elapsed_seconds'] >= (body + d['metadata_response_body_bytes']) * 8 / 5e6
    rows.append({'release': row['release'], 'chunk_bytes': body,
                 'metadata_bytes': d['metadata_response_body_bytes'], 'seconds': row['elapsed_seconds'],
                 'baseline_chunk_bytes': baseline, 'baseline_difference_percent': difference,
                 'body_mbit_per_second': (body + d['metadata_response_body_bytes']) * 8 / row['elapsed_seconds'] / 1e6})
assert link['unchanged_5s']['chunk_response_body_bytes'] == 0
assert link['unchanged_5s']['metadata_response_body_bytes'] == 0
assert soak['gate_pass'] and soak['all_daemons_alive'] and len(soak['outages']) == 2
assert soak['summary']['phase'] == 'staged' and soak['summary']['integrity_failures'] == 0
mapping = load('soak/soak-chunk-map.json')
valid_paths = {'chunks/' + c['encoded_sha256'][:2] + '/' + c['encoded_sha256'] + '.gz' for c in mapping.values()}
for outage in soak['outages']:
    paths = outage['verified_committed_paths']
    assert paths and set(paths) <= valid_paths
    assert outage['duration_seconds'] >= 30
    assert outage['after_outage_stats']['injected_503s'] > outage['before_503s']
    assert not outage['redownloaded_committed']
    for path in paths:
        assert outage['before_object_requests'][path] >= 1
        assert outage['before_object_requests'][path] == soak['after']['object_requests'][path]
retries = soak['retry_observations']
assert retries and all(1 <= r['attempt'] < 12 and 0 < r['delay_ms'] <= 5000 for r in retries)
for filename in ['cleanup.json', 'soak/cleanup.json']:
    cleanup = load(filename)
    assert cleanup['daemon_children_reaped'] and cleanup['temporary_workdir_removed']
    assert 'netem' not in cleanup['qdisc']
print(json.dumps({'status': 'PASS_LOCAL_LINUX_VM_ONLY', 'idle_seconds': idle['window_seconds'],
                  'idle_body_kib_per_device_hour': {k: v['bytes_served'] / 1024 * 3600 / idle['window_seconds'] for k, v in idle['devices'].items()},
                  'link': rows, 'soak_seconds': soak['elapsed_seconds'], 'observed_retry_events': len(retries),
                  'max_retry_attempt': max(r['attempt'] for r in retries),
                  'max_retry_delay_ms': max(r['delay_ms'] for r in retries),
                  'verified_committed_counts_at_outage': [len(o['verified_committed_paths']) for o in soak['outages']],
                  'theoretical_1gib_seconds_at_5mbit_no_overhead': 2**30 * 8 / 5e6,
                  'standalone_linux': 'NOT RUN', 'tailscale_overhead': 'NOT RUN',
                  'mock_alert_delivery_during_soak': 'NOT RUN'}, indent=2))
