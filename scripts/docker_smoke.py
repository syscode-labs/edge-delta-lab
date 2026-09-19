#!/usr/bin/env python3
"""Opt-in real Docker build/save -> delta stage/load -> offline payload test.

Uses FROM scratch on eligible local Unix daemons only, with unique owned tags.
No remote base-image pull, privileged container, or Docker socket mount.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid
from demo import ROOT, available_port, dump, get
from docker_demo import file_hash, random_file
from docker_identity import LocalDocker, normalize_image, add_transport_arguments, transport_settings, retry_arguments


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work', type=Path)
    p.add_argument('--size-mib', type=int, default=16)
    p.add_argument('--rate-kbit', type=int, default=5000)
    p.add_argument('--sender-context', default='')
    p.add_argument('--receiver-context', default='')
    add_transport_arguments(p, interruption=False)
    args = p.parse_args()
    if args.size_mib < 4:
        p.error('size must be >=4 MiB')
    faults, retry = transport_settings(args)
    token = uuid.uuid4().hex[:12]
    work = (args.work or ROOT / 'work' / ('docker-smoke-' + token)).resolve()
    if work.exists():
        p.error('choose a fresh work directory; evidence is never overwritten')
    context = work / 'context'
    context.mkdir(parents=True)
    dump(work / 'invocation.json', dict(argv=sys.argv, fault_plan=faults, retry_policy=retry))
    sender, receiver = LocalDocker(args.sender_context), LocalDocker(args.receiver_context)
    if sender.platform != receiver.platform:
        p.error('sender and receiver platform must match')
    topology = dict(sender=sender.record, receiver=receiver.record, classification='same-daemon local integration' if sender.record['daemon_id'] == receiver.record['daemon_id'] else 'distinct-local-daemon integration')
    dump(work / 'topology.json', topology)
    binary = ROOT / 'bin' / 'edgelab'
    subprocess.run(['go', 'build', '-trimpath', '-o', str(binary), './cmd/edgelab'], cwd=ROOT, check=True)
    source = context / 'probe.go'
    source.write_text('package main\nimport("crypto/sha256";"encoding/hex";"encoding/json";"io";"os";"strings")\nfunc main(){b,e:=os.ReadFile("/release.txt");if e!=nil{panic(e)};f,e:=os.Open("/payload.bin");if e!=nil{panic(e)};defer f.Close();h:=sha256.New();if _,e=io.Copy(h,f);e!=nil{panic(e)};json.NewEncoder(os.Stdout).Encode(map[string]string{"release":strings.TrimSpace(string(b)),"payload_sha256":hex.EncodeToString(h.Sum(nil))})}\n')
    env = dict(os.environ, GOOS='linux', GOARCH=sender.platform.split('/')[1], CGO_ENABLED='0')
    subprocess.run(['go', 'build', '-trimpath', '-o', str(context / 'probe'), str(source)], env=env, check=True)
    (context / 'Dockerfile').write_text(f'FROM scratch\nLABEL edge-delta-smoke="{token}"\nCOPY probe payload.bin release.txt /\nENTRYPOINT ["/probe"]\n')
    payload = context / 'payload.bin'
    random_file(payload, args.size_mib << 20, b'smoke-payload')
    keys, origin, edge = work / 'keys', work / 'origin', work / 'edge'
    subprocess.run([str(binary), 'keygen', '--out', str(keys)], check=True)
    images, owned_tags = [], []
    server = log = None
    try:
        for seq in (1, 2):
            tag = f'edge-delta-smoke-{token}:v{seq}'
            if seq == 2:
                with payload.open('r+b') as stream:
                    stream.seek(payload.stat().st_size // 2)
                    stream.write(hashlib.shake_256(b'small-edit').digest(32 << 10))
            (context / 'release.txt').write_text(f'edge-delta-smoke-v{seq}\n')
            # Pin mtimes per release (1700000000+seq): Docker's client-side
            # context change detection keys on size+mtime; identical size+mtime
            # across releases re-sends stale context bytes (006) which a warm
            # COPY cache then folds into one layer (004/005).
            stamp = 1700000000 + seq
            for name in ('release.txt', 'payload.bin'):
                os.utime(context / name, (stamp, stamp))
            with (work / f'build-v{seq}.log').open('w') as build_log:
                # --no-cache: v2 payload bytes change in place while size and
                # pinned mtimes stay equal; a warm COPY cache has been observed
                # to serve the stale layer (docker-validation-004/005).
                sender.run('build', '--network', 'none', '--pull=false', '--no-cache', '-t', tag, str(context), stdout=build_log, stderr=build_log)
            owned_tags.append(tag)
            dump(work / 'owned-tags.json', owned_tags)
            inspected = sender.inspect(tag)
            dump(work / f'source-inspect-v{seq}.json', inspected)
            saved, normalized = work / f'v{seq}.saved.tar', work / f'v{seq}.tar'
            sender.run('image', 'save', '--output', str(saved), tag)
            identity = normalize_image(saved, normalized, inspected)
            if any(entry['identity']['config_id'] == identity['config_id'] for entry in images):
                raise AssertionError(f'v{seq} reused the config digest of an earlier release; cached build output replaced its payload')
            if any(entry['identity']['diff_ids'] == identity['diff_ids'] for entry in images):
                raise AssertionError(f'v{seq} reproduced the layer digests of an earlier release; the build did not receive new payload bytes')
            with (work / f'publish-v{seq}.json').open('w') as publish_log:
                subprocess.run([str(binary), 'publish', '--input', str(normalized), '--root', str(origin), '--key', str(keys / 'publisher.key'), '--release', f'v{seq}', '--sequence', str(seq), '--kind', 'docker-archive', '--image-ids', identity['config_id']], stdout=publish_log, check=True)
            sender.run('image', 'rm', tag)
            absence = receiver.assert_absent(identity['config_id'], identity['source_inspect_id'], tag)
            images.append(dict(seq=seq, tag=tag, identity=identity, receiver_absence=absence, expected=dict(release=f'edge-delta-smoke-v{seq}', payload_sha256=file_hash(payload))))
            dump(work / 'images.json', images)
        port = available_port()
        base = f'http://127.0.0.1:{port}'
        plan = work / 'faults.json'
        dump(plan, faults)
        log = (work / 'origin.log').open('w')
        server = subprocess.Popen([str(binary), 'serve', '--root', str(origin), '--listen', f'127.0.0.1:{port}', '--faults', str(plan)], stdout=log, stderr=log)
        for _ in range(100):
            try:
                get(base + '/stats'); break
            except OSError:
                time.sleep(0.05)
        else:
            raise RuntimeError('origin did not become ready')
        summaries = []
        for image in images:
            seq, identity = image['seq'], image['identity']
            with (work / f'sync-v{seq}.json').open('w') as out, (work / f'sync-v{seq}.events.jsonl').open('w') as events:
                subprocess.run([str(binary), 'sync', '--manifest', f'{base}/releases/v{seq}.json', '--base', base, '--state', str(edge), '--pub', str(keys / 'publisher.pub'), '--allow-http', '--docker-load', *retry_arguments(retry)], stdout=out, stderr=events, env=receiver.env, check=True)
            identity['stored_reference'] = receiver.stored_reference(identity['config_id'], image['tag'])
            image['loaded_identity'] = receiver.verify_loaded(identity)
            actual = json.loads(receiver.text('run', '--rm', '--pull', 'never', '--network', 'none', identity['stored_reference']))
            assert actual == image['expected']
            image['actual_probe'] = actual
            summaries.append(json.loads((work / f'sync-v{seq}.json').read_text()))
        stats = get(base + '/stats')
        server.terminate(); server.wait(timeout=10)
        for image in images:
            assert json.loads(receiver.text('run', '--rm', '--pull', 'never', '--network', 'none', image['identity']['stored_reference'])) == image['expected']
        dump(work / 'docker-smoke-results.json', dict(status='PASSED_REAL_DOCKER_SMOKE', topology=topology, images=images, staged_and_loaded=summaries, old_and_new_images_available_offline=True, fault_plan=faults, retry_policy=retry, origin_stats=stats))
        print(f"Real Docker smoke passed: {work / 'docker-smoke-results.json'}")
    except BaseException as exc:
        dump(work / 'failure.json', dict(status='FAILED_REAL_DOCKER_SMOKE', error_type=type(exc).__name__, error=str(exc), owned_tags=owned_tags))
        raise
    finally:
        if server and server.poll() is None:
            server.terminate()
            try: server.wait(timeout=10)
            except subprocess.TimeoutExpired: server.kill(); server.wait()
        if log: log.close()
        cleanup = []
        for tag in owned_tags:
            for which, daemon in [('sender', sender), ('receiver', receiver)]:
                try:
                    result = daemon.run('image', 'rm', tag, check=False, capture_output=True, text=True, timeout=30)
                    cleanup.append(dict(which=which, tag=tag, exit_code=result.returncode, stdout=result.stdout, stderr=result.stderr))
                except subprocess.TimeoutExpired as exc:
                    cleanup.append(dict(which=which, tag=tag, error=str(exc)))
        dump(work / 'cleanup.json', cleanup)


if __name__ == '__main__':
    main()
