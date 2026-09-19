#!/usr/bin/env python3
"""Collect and inspect delivery evidence from the SENDER only; never SSH/scrape the edge."""
from __future__ import annotations
import argparse
import csv
from datetime import datetime
import json
import math
import signal
import sqlite3
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path


DEFAULT_STALE_AFTER = 30.0
IDENTITY_TRUST = 'unauthenticated_lab'


def open_db(path: Path) -> sqlite3.Connection:
    path.parent.mkdir(parents=True, exist_ok=True)
    db = sqlite3.connect(path, timeout=10)
    db.execute('PRAGMA journal_mode=WAL')
    db.execute('''CREATE TABLE IF NOT EXISTS samples (
        id INTEGER PRIMARY KEY, observed_at REAL NOT NULL, origin TEXT NOT NULL,
        reachable INTEGER NOT NULL, stats TEXT, error TEXT)''')
    db.execute('''CREATE TABLE IF NOT EXISTS receipts (
        origin TEXT NOT NULL, id TEXT NOT NULL, first_observed_at REAL NOT NULL,
        body TEXT NOT NULL, PRIMARY KEY(origin,id))''')
    db.commit()
    return db


def fetch(url: str):
    with urllib.request.urlopen(url, timeout=3) as response:
        raw = response.read((32 << 20) + 1)
        if len(raw) > 32 << 20:
            raise ValueError('sender response exceeds 32 MiB limit')
        return json.loads(raw)


def sample(db: sqlite3.Connection, origin: str) -> dict:
    now = time.time()
    try:
        stats = fetch(origin + '/stats')
        if not isinstance(stats, dict) or 'chunk_requests' not in stats:
            raise ValueError('not an edge-delta-lab /stats response')
    except (OSError, ValueError) as exc:
        db.execute('INSERT INTO samples VALUES(NULL,?,?,0,NULL,?)', (now, origin, str(exc)))
        db.commit()
        return {'reachable': False, 'error': str(exc)}
    sample_id = db.execute('INSERT INTO samples VALUES(NULL,?,?,1,?,NULL)',
                           (now, origin, json.dumps(stats))).lastrowid
    receipt_error = None
    try:
        for receipt in fetch(origin + '/receipts'):
            db.execute('INSERT OR IGNORE INTO receipts VALUES(?,?,?,?)',
                       (origin, receipt['id'], now, json.dumps(receipt)))
    except (OSError, ValueError, KeyError, TypeError) as exc:
        # Transfer observations remain useful when acknowledgement collection fails.
        receipt_error = str(exc)
        db.execute('UPDATE samples SET error=? WHERE id=?',
                   ('Receipt collection failed: ' + receipt_error, sample_id))
    db.commit()
    return {'reachable': True, 'receipt_error': receipt_error}


def recorded_time(receipt: dict):
    try:
        at = datetime.fromisoformat(receipt['recorded_at'].replace('Z', '+00:00'))
        return at.timestamp() if at.tzinfo is not None else None
    except (KeyError, TypeError, AttributeError, ValueError, OverflowError):
        return None


def acknowledgements(db: sqlite3.Connection, origin: str, now: float, stale_after: float):
    # Phase ordering applies ONLY within the same immutable identity. Never let
    # response/file order choose a winner between conflicting sequence claims.
    releases = {}
    for at, body in db.execute('SELECT first_observed_at,body FROM receipts WHERE origin=?', (origin,)):
        receipt = json.loads(body)
        receipt['first_observed_at'] = at
        receipt['observation_age_seconds'] = max(0, now - at)
        reported = recorded_time(receipt)
        reported_age = now - reported if reported is not None and reported <= now else None
        receipt['reported_age_seconds'] = reported_age
        # Persistent log replay must not turn an old acknowledgement into a new
        # heartbeat. Device timestamps are unauthenticated, including their age.
        receipt['freshness'] = ('stale' if now - at > stale_after or
                                (reported_age is not None and reported_age > stale_after)
                                else 'unknown' if reported_age is None or at > now else 'recent')
        receipt['identity_trust'] = IDENTITY_TRUST
        key = (receipt['device_id'], receipt['summary']['sequence'],
               receipt['release'], receipt['artifact_sha256'])
        def rank(r):
            recorded = recorded_time(r)
            return ({'staged': 0, 'loaded': 1}[r['phase']],
                    recorded if recorded is not None else float('-inf'), r['id'])
        old = releases.get(key)
        if old is None or rank(receipt) > rank(old):
            releases[key] = receipt
    sequences = {}
    for key in sorted(releases):
        sequences.setdefault(key[:2], []).append(releases[key])
    conflicts = []
    devices = {}
    for (device, sequence), candidates in sorted(sequences.items()):
        conflict = len(candidates) > 1
        for candidate in candidates:
            candidate['identity_conflict'] = conflict
        if conflict:
            conflicts.append({'device_id': device, 'sequence': sequence,
                              'identities': [{'release': r['release'], 'artifact_sha256': r['artifact_sha256']}
                                             for r in candidates]})
        # Ascending sequence order: a conflicting newest sequence must not fall
        # back to an older, apparently successful acknowledgement for this device.
        devices[device] = None if conflict else candidates[0]
    return ([r for r in devices.values() if r is not None],
            [releases[key] for key in sorted(releases)], conflicts)


def snapshot(db: sqlite3.Connection, stale_after: float = DEFAULT_STALE_AFTER) -> dict:
    if not math.isfinite(stale_after) or stale_after <= 0:
        raise ValueError('stale-after must be a finite positive number of seconds')
    now = time.time()
    row = db.execute('SELECT observed_at,origin,reachable,stats,error FROM samples ORDER BY id DESC LIMIT 1').fetchone()
    if row is None:
        return {'state': 'no samples', 'observation_freshness': 'unknown',
                'device_acknowledgements': [], 'release_acknowledgements': [],
                'acknowledgement_conflicts': [], 'identity_trust': IDENTITY_TRUST,
                'runtime_health': 'unknown', 'stale_after_seconds': stale_after}
    at, origin, reachable, raw, error = row
    last_good = db.execute('SELECT observed_at,stats FROM samples WHERE origin=? AND reachable=1 ORDER BY id DESC LIMIT 2', (origin,)).fetchall()
    stats = json.loads(last_good[0][1]) if last_good else {}
    bps = None
    rate_window = None
    if len(last_good) == 2:
        previous = json.loads(last_good[1][1])
        seconds = last_good[0][0] - last_good[1][0]
        if stats.get('origin_started') == previous.get('origin_started') and seconds > 0:
            delta = sum(stats.get(k, 0) - previous.get(k, 0)
                        for k in ('chunk_response_body_bytes','metadata_response_body_bytes'))
            if delta >= 0:
                bps = delta * 8 / seconds
                rate_window = {'start': last_good[1][0], 'end': last_good[0][0]}
    age = max(0, now - at)
    good_age = max(0, now - last_good[0][0]) if last_good else None
    freshness = 'stale' if age > stale_after else 'recent'
    observation_state = 'reachable' if reachable else 'unreachable'
    rate_current = (rate_window is not None and reachable and freshness == 'recent'
                    and now - rate_window['start'] <= stale_after)
    devices, releases, conflicts = acknowledgements(db, origin, now, stale_after)
    return {'state': 'stale' if freshness == 'stale' else observation_state, 'origin': origin,
            'last_observation_state': observation_state, 'last_sample_at': at,
            'observation_freshness': freshness, 'stale_after_seconds': stale_after,
            'last_sample_age_seconds': age,
            'last_successful_sample_at': last_good[0][0] if last_good else None,
            'last_successful_sample_age_seconds': good_age,
            'observed_freshness': 'unknown' if good_age is None else 'stale' if good_age > stale_after else 'recent',
            'last_error': error, 'observed': stats,
            'response_body_bits_per_second': bps if rate_current else None,
            'last_measured_response_body_bits_per_second': bps, 'rate_window': rate_window,
            'repeated_chunk_requests': sum(max(0,n-1) for n in stats.get('object_requests',{}).values()),
            'device_acknowledgements': devices, 'release_acknowledgements': releases,
            'acknowledgement_conflicts': conflicts,
            'identity_trust': IDENTITY_TRUST, 'runtime_health': 'unknown',
            'measurement': 'HTTP response bodies written by origin; not TCP/IP wire bytes, durable progress, or runtime health'}


def render(state: dict) -> str:
    if state['state'] == 'no samples':
        return 'No sender samples yet. Reachability, delivery and runtime health are UNKNOWN. Start sender.py collect first.'
    stats = state['observed']
    age = state['last_sample_age_seconds']
    rate = state['response_body_bits_per_second']
    lines = [f"SENDER  {state['origin']}  state: {state['state'].upper()}  sample age {age:.1f}s",
             f"  Last observation: {state['last_observation_state']} at {state['last_sample_at']:.3f} Unix seconds; stale after {state['stale_after_seconds']:g}s",
             'Observed at the sender (not proof of delivery):']
    good_age = state['last_successful_sample_age_seconds']
    if good_age is None:
        lines.append('  Last successful counters: UNKNOWN (no successful sample).')
    else:
        lines += [f"  Last successful counters: {state['observed_freshness'].upper()}, age {good_age:.1f}s (retained process totals)",
                  f"  Chunk body bytes: {stats.get('chunk_response_body_bytes',0):,}   Metadata body bytes: {stats.get('metadata_response_body_bytes',0):,}",
                  f"  Chunk requests: {stats.get('chunk_requests',0):,}   Repeated object requests: {state['repeated_chunk_requests']:,}",
                  f"  Disconnects: {stats.get('injected_disconnects',0)}   HTTP 503s: {stats.get('injected_503s',0)}   Corruptions: {stats.get('injected_corruptions',0)}"]
    if rate is not None:
        lines.append(f"  Last measured response-body rate: {rate/1000:.1f} kbit/s (recent sample interval, not instantaneous)")
    else:
        lines.append('  Current response-body rate: UNKNOWN (stale/unavailable or no comparable samples).')
        historical_rate = state['last_measured_response_body_bits_per_second']
        if historical_rate is not None:
            window = state['rate_window']
            lines.append(f"  Historical response-body rate: {historical_rate/1000:.1f} kbit/s over Unix {window['start']:.3f}–{window['end']:.3f}; NOT current throughput.")
    lines += ['', 'Device acknowledgements received at the sender:',
              '  UNAUTHENTICATED lab identity and device-reported timestamps; not independently verified progress.',
              '  Receipts are historical events, not heartbeats. RECENT does not prove current device state.']
    if not state['device_acknowledgements']:
        lines.append('  None unambiguous. Delivery, Docker import and runtime health are UNKNOWN.')
    for conflict in state['acknowledgement_conflicts']:
        identities = ', '.join(r['release'] + '/' + r['artifact_sha256'] for r in conflict['identities'])
        lines.append(f"  CONFLICT {conflict['device_id']} sequence {conflict['sequence']}: {identities}; progress UNKNOWN.")
    for receipt in state['device_acknowledgements']:
        summary = receipt['summary']
        lines.append(f"  {receipt['device_id']}  {receipt['release']}  {receipt['phase'].upper()}  reused {summary['reused_chunks']}/{summary['unique_chunks']} chunks")
        reported_age = receipt['reported_age_seconds']
        reported = f'{reported_age:.1f}s' if reported_age is not None else 'UNKNOWN (missing/invalid/future device time)'
        lines.append(f"    {receipt['freshness'].upper()} receipt; first observed {receipt['first_observed_at']:.3f} Unix seconds ({receipt['observation_age_seconds']:.1f}s ago); device recorded {receipt.get('recorded_at', 'UNKNOWN')} (reported age {reported})")
    lines += ['', 'LOADED acknowledges image import only. Running/healthy is NOT inferred.',
              'Collector polls only the sender; the receiver has no metrics listener.']
    if state['last_error']:
        lines.append('Last collection error: '+state['last_error'])
    return '\n'.join(lines)


def export_report(db: sqlite3.Connection, out: Path, stale_after: float) -> None:
    # One read transaction keeps status and every history export consistent even
    # while a separate collector is appending observations to the WAL database.
    with db:
        db.execute('BEGIN')
        state = snapshot(db, stale_after)
        samples = [dict(id=id_, observed_at=at, origin=origin, reachable=reachable,
                        stats=json.loads(raw) if raw is not None else None, error=error)
                   for id_, at, origin, reachable, raw, error in db.execute('SELECT * FROM samples ORDER BY id')]
        receipts = [dict(origin=origin, id=id_, first_observed_at=at, body=json.loads(body))
                    for origin, id_, at, body in db.execute('SELECT * FROM receipts ORDER BY first_observed_at,origin,id')]
    out.mkdir(parents=True, exist_ok=True)
    (out / 'status.json').write_text(json.dumps(state, indent=2) + '\n')
    (out / 'status.txt').write_text(render(state) + '\n')
    (out / 'history.json').write_text(json.dumps(dict(status=state, samples=samples, receipts=receipts), indent=2) + '\n')
    # Keep the original sample CSV schema for existing consumers. The new full
    # CSV includes every stats field, including maps/future fields, as JSON.
    with (out / 'samples.csv').open('w', newline='') as stream:
        writer = csv.writer(stream)
        writer.writerow(['observed_unix_seconds','origin','reachable','chunk_body_bytes','metadata_body_bytes','chunk_requests','disconnects','http_503s','error'])
        for row in samples:
            stats = row['stats'] or {}
            writer.writerow([row['observed_at'], row['origin'], row['reachable'], stats.get('chunk_response_body_bytes'), stats.get('metadata_response_body_bytes'), stats.get('chunk_requests'), stats.get('injected_disconnects'), stats.get('injected_503s'), row['error']])
    with (out / 'samples-full.csv').open('w', newline='') as stream:
        writer = csv.writer(stream)
        writer.writerow(['id', 'observed_at', 'origin', 'reachable', 'stats_json', 'error'])
        for row in samples:
            writer.writerow([row['id'], row['observed_at'], row['origin'], row['reachable'], json.dumps(row['stats']), row['error']])
    with (out / 'receipts.csv').open('w', newline='') as stream:
        writer = csv.writer(stream)
        writer.writerow(['origin', 'id', 'first_observed_at', 'device_id', 'release', 'sequence', 'phase', 'artifact_sha256', 'recorded_at', 'body_json'])
        for row in receipts:
            body = row['body']
            writer.writerow([row['origin'], row['id'], row['first_observed_at'], body['device_id'], body['release'], body['summary']['sequence'], body['phase'], body['artifact_sha256'], body.get('recorded_at'), json.dumps(body)])


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    collect = sub.add_parser('collect', help='poll the sender into a sender-side SQLite database')
    collect.add_argument('--origin', required=True)
    collect.add_argument('--db', type=Path, default=Path('work/sender.sqlite'))
    collect.add_argument('--interval', type=float, default=0.5)
    collect.add_argument('--duration', type=float, default=0, help='seconds; 0 means until interrupted')
    collect.add_argument('--once', action='store_true')
    status = sub.add_parser('status', help='read the locally collected sender database')
    status.add_argument('--db', type=Path, default=Path('work/sender.sqlite'))
    status.add_argument('--watch', action='store_true')
    status.add_argument('--json', action='store_true')
    report = sub.add_parser('report', help='export collected sender evidence as JSON and CSV')
    report.add_argument('--db', type=Path, default=Path('work/sender.sqlite'))
    report.add_argument('--out', type=Path, default=Path('work/sender-report'))
    for command in (status, report):
        command.add_argument('--stale-after', type=float, default=DEFAULT_STALE_AFTER,
                             help='evidence age limit in seconds (default: 30); not a device health check')
    args = parser.parse_args()
    if args.command != 'collect' and (not math.isfinite(args.stale_after) or args.stale_after <= 0):
        parser.error('--stale-after must be a finite positive number of seconds')
    if args.command != 'collect' and not args.db.exists():
        parser.error(f'no database at {args.db}; start the collector first')
    db = open_db(args.db)
    stopping = False
    def stop(*_):
        nonlocal stopping
        stopping = True
    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    try:
        if args.command == 'collect':
            if args.interval <= 0 or args.duration < 0:
                parser.error('interval must be positive and duration non-negative')
            origin = args.origin.rstrip('/')
            if not origin.startswith(('http://','https://')):
                parser.error('--origin must be an HTTP(S) sender URL')
            print(f'Collecting from sender {origin} into {args.db}', flush=True)
            started = time.monotonic()
            while not stopping:
                sample(db, origin)
                if args.once or (args.duration and time.monotonic()-started >= args.duration):
                    break
                time.sleep(args.interval)
        elif args.command == 'status':
            while not stopping:
                value = snapshot(db, args.stale_after)
                if args.watch and sys.stdout.isatty():
                    print('\033[2J\033[H', end='')
                print(json.dumps(value,indent=2) if args.json else render(value), flush=True)
                if not args.watch:
                    break
                time.sleep(1)
        else:
            export_report(db, args.out, args.stale_after)
            print(f'Sender report: {args.out}')
    finally:
        db.close()

if __name__ == '__main__':
    main()
