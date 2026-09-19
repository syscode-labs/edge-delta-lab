#!/usr/bin/env python3
"""Real Docker build -> chunked faulty transfer -> Docker load -> offline payload verification.

Produces measured sender data. Refuses to substitute synthetic fixtures when Docker is absent.
Only uniquely tagged images created by this invocation are removed. No container is
privileged and no Docker socket is mounted into a container.
"""
from __future__ import annotations
import argparse
import base64
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tarfile
import time
import uuid
from demo import ROOT, available_port, dump, get
from docker_identity import LocalDocker, normalize_image, add_transport_arguments, transport_settings, retry_arguments
from sender import open_db, sample, snapshot, render

class CountWriter:
    def __init__(self): self.count = 0
    def write(self, body): self.count += len(body); return len(body)
    def flush(self): pass

def layer_sizes(archive: Path) -> dict[str, int]:
    """Computed conventional whole-layer gzip baseline; NOT a registry pull measurement."""
    with tarfile.open(archive) as source:
        manifest = json.load(source.extractfile('manifest.json'))
        result = {}
        for name in manifest[0]['Layers']:
            count = CountWriter()
            with source.extractfile(name) as raw, gzip.GzipFile(fileobj=count, mode='wb',mtime=0,compresslevel=6) as encoded:
                shutil.copyfileobj(raw,encoded,1<<20)
            result[name] = count.count
        return result

def random_file(path: Path, size: int, label: bytes) -> str:
    digest=hashlib.sha256()
    with path.open('wb') as out:
        for index in range((size+65535)//65536):
            block=hashlib.shake_256(label+str(index).encode()).digest(min(65536,size-index*65536))
            out.write(block);digest.update(block)
    os.utime(path,(1700000000,1700000000))
    return digest.hexdigest()

def file_hash(path: Path) -> str:
    h=hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda:stream.read(1<<20),b''): h.update(block)
    return h.hexdigest()

def main() -> None:
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--work',type=Path,default=Path('work/docker-demo'))
    p.add_argument('--size-mib',type=int,default=16,help='base + application payload size, not final image size')
    p.add_argument('--rate-kbit',type=int,default=5000)
    p.add_argument('--sender-context',default='',help='optional existing Docker context for building')
    p.add_argument('--receiver-context',default='',help='optional existing Docker context for loading/running')
    add_transport_arguments(p)
    args=p.parse_args()
    if args.size_mib<4 or args.rate_kbit<=0:p.error('size must be >=4 MiB and rate positive')
    faults,retry=transport_settings(args)
    if not shutil.which('docker'):
        raise SystemExit('NOT RUN: Docker CLI and a reachable Linux Docker daemon are required. No synthetic substitution was made.')
    work=args.work.resolve()
    if work.exists():p.error(f'{work} already exists; choose a fresh --work directory to preserve evidence')
    work.mkdir(parents=True);(work/'logs').mkdir();(work/'sender').mkdir()
    dump(work/'invocation.json',dict(argv=sys.argv,settings={k:str(v) if isinstance(v,Path) else v for k,v in vars(args).items()},
                                  fault_plan=faults,retry_policy=retry,fixture_seeds=['shared-base','A','B','32KiB-update']))
    daemons={'sender':LocalDocker(args.sender_context),'receiver':LocalDocker(args.receiver_context)}
    topology=dict(sender=daemons['sender'].record,receiver=daemons['receiver'].record)
    sender_daemon=topology['sender']['daemon_id'];receiver_daemon=topology['receiver']['daemon_id']
    topology['classification']='distinct-local-daemon integration' if sender_daemon!=receiver_daemon else 'same-daemon local integration'
    topology['wan_scope']='aggregate throttled origin HTTP response bodies over loopback; no physical WAN or isolated edge claim'
    dump(work/'topology.json',topology)
    def docker(which,*command,**kwargs):
        return daemons[which].run(*command,**kwargs)
    def docker_text(which,*command):
        return docker(which,*command,stdout=subprocess.PIPE,text=True).stdout.strip()
    sender_platform=daemons['sender'].platform
    receiver_platform=daemons['receiver'].platform
    if sender_platform!=receiver_platform or sender_platform not in ('linux/amd64','linux/arm64'):
        p.error(f'matching Linux amd64/arm64 Docker daemons required: {sender_platform}, {receiver_platform}')

    context=work/'build';context.mkdir()
    token=uuid.uuid4().hex[:12]
    binary=ROOT/'bin/edgelab'
    subprocess.run(['go','build','-trimpath','-o',str(binary),'./cmd/edgelab'],cwd=ROOT,check=True)
    probe=context/'probe.go'
    probe.write_text('''package main
import("crypto/sha256";"encoding/hex";"encoding/json";"io";"os";"strings")
func hash(path string) string {f,e:=os.Open(path);if e!=nil{panic(e)};defer f.Close();h:=sha256.New();if _,e=io.Copy(h,f);e!=nil{panic(e)};return hex.EncodeToString(h.Sum(nil))}
func main(){b,e:=os.ReadFile("/release.txt");if e!=nil{panic(e)};json.NewEncoder(os.Stdout).Encode(map[string]string{"release":strings.TrimSpace(string(b)),"base_sha256":hash("/base.bin"),"payload_sha256":hash("/payload.bin")})}
''')
    env=dict(os.environ,GOOS='linux',GOARCH=sender_platform.split('/')[1],CGO_ENABLED='0')
    subprocess.run(['go','build','-trimpath','-o',str(context/'probe'),str(probe)],env=env,check=True)
    (context/'Dockerfile').write_text(f'FROM scratch\nLABEL edge-delta-demo="{token}"\nCOPY probe /probe\nCOPY base.bin /base.bin\nCOPY payload.bin release.txt /\nENTRYPOINT ["/probe"]\n')
    size=args.size_mib<<20;base_size=size*3//5;app_size=size-base_size
    base_hash=random_file(context/'base.bin',base_size,b'shared-base')
    keys=work/'keys';origin=work/'origin';edge=work/'edge'
    subprocess.run([str(binary),'keygen','--out',str(keys)],check=True)
    images=[];owned_tags=[];server=None;collector=None;opened=[]
    try:
        for seq,release in enumerate(('app-a-v1','app-b-v1','app-a-v2'),1):
            random_file(context/'payload.bin',app_size,b'B' if release=='app-b-v1' else b'A')
            if release=='app-a-v2':
                with (context/'payload.bin').open('r+b') as out:
                    out.seek(app_size//2);out.write(hashlib.shake_256(b'32KiB-update').digest(32<<10))
            (context/'release.txt').write_text(release+'\n')
            # Pin mtimes, but per release: Docker's client-side context change
            # detection keys on size+mtime, so identical size+mtime across
            # releases makes the client treat the context as unchanged and
            # re-send the previous bytes (docker-validation-006 reproduced
            # release 1's layers with --no-cache because the context never
            # changed as far as the daemon could tell).
            stamp=1700000000+seq
            for filename in ('payload.bin','release.txt'):os.utime(context/filename,(stamp,stamp))
            payload_hash=file_hash(context/'payload.bin')
            tag=f'edge-delta-demo-{token}:{release}'
            with (work/'logs'/f'build-{release}.txt').open('w') as log:
                # --no-cache: payload bytes change per release while size and
                # pinned mtimes stay equal; a warm per-run COPY cache has been
                # observed (OrbStack/BuildKit, docker-validation-004/005) to
                # serve the stale layer and fold releases into one image.
                docker('sender','build','--network','none','--pull=false','--no-cache','-t',tag,str(context),stdout=log,stderr=log)
            # Register immediately: later inspect/save/normalization can fail.
            owned_tags.append(tag);dump(work/'owned-tags.json',owned_tags)
            source_inspect=daemons['sender'].inspect(tag)
            dump(work/'logs'/f'source-inspect-{release}.json',source_inspect)
            saved=work/f'{release}.saved.tar';archive=work/f'{release}.tar'
            docker('sender','image','save','-o',str(saved),tag)
            identity=normalize_image(saved,archive,source_inspect)
            image_id=identity['config_id']
            if any(previous['id']==image_id for previous in images):
                raise AssertionError(f'release {release} reused the config digest of an earlier release; cached build output replaced its payload')
            if any(previous['identity']['diff_ids']==identity['diff_ids'] for previous in images):
                raise AssertionError(f'release {release} reproduced the layer digests of an earlier release; the build did not receive new payload bytes')
            dump(work/f'{release}-identity.json',identity)
            with (work/'logs'/f'publish-{release}.json').open('w') as log:
                subprocess.run([str(binary),'publish','--input',str(archive),'--root',str(origin),'--key',str(keys/'publisher.key'),
                    '--release',release,'--sequence',str(seq),'--kind','docker-archive','--image-ids',image_id],stdout=log,check=True)
            images.append(dict(release=release,tag=tag,id=image_id,identity=identity,layers=layer_sizes(archive),archive_bytes=archive.stat().st_size,
                               archive_sha256=file_hash(archive),
                               expected=dict(release=release,base_sha256=base_hash,payload_sha256=payload_hash)))
            docker('sender','image','rm',tag,stdout=subprocess.DEVNULL)
            # A real image ID must be absent at the receiver before the transfer.
            images[-1]['receiver_absence']=daemons['receiver'].assert_absent(image_id,identity['source_inspect_id'],tag)
            dump(work/'images.json',images)
        # Both app-a revisions and app-b have actual shared whole layers.
        common=set(images[0]['layers'])&set(images[1]['layers'])&set(images[2]['layers'])
        assert common,'Docker images unexpectedly have no common layers'
        port=available_port();base=f'http://127.0.0.1:{port}'
        plan=work/'faults.json';dump(plan,faults)
        log=(work/'logs/origin.log').open('w');opened.append(log)
        server=subprocess.Popen([str(binary),'serve','--root',str(origin),'--listen',f'127.0.0.1:{port}',
                                 '--faults',str(plan),'--receipts-dir',str(work/'sender/receipts')],stdout=log,stderr=log)
        for _ in range(100):
            try:get(base+'/stats');break
            except OSError:time.sleep(.05)
        else:raise RuntimeError('origin did not become ready')
        dbpath=work/'sender/collector.sqlite'
        clog=(work/'logs/collector.log').open('w');opened.append(clog)
        collector=subprocess.Popen(['python3',str(ROOT/'scripts/sender.py'),'collect','--origin',base,'--db',str(dbpath),'--interval','.25'],stdout=clog,stderr=clog)
        print(f'Live sender view: python3 scripts/sender.py status --db {dbpath} --watch',flush=True)
        rows=[];layers_present=set();receiver_env=daemons['receiver'].env
        def command(release):return [str(binary),'sync','--manifest',base+'/releases/'+release+'.json','--base',base,'--state',str(edge),
                  '--pub',str(keys/'publisher.pub'),'--allow-http','--docker-load','--receipt-url',base+'/receipts','--device-id','demo-edge',
                  *retry_arguments(retry)]
        preserved_count=0
        for index,image in enumerate(images):
            release=image['release'];before=get(base+'/stats');started=time.monotonic()
            if index==0:
                # Deterministically terminate the real agent after durable chunk commits.
                events=work/'logs/cold-killed.jsonl'
                with events.open('w') as err,(work/'logs/cold-killed.out').open('w') as out:
                    proc=subprocess.Popen(command(release),stdout=out,stderr=err,env=receiver_env)
                    try:
                        until=time.monotonic()+args.kill_timeout_seconds
                        while time.monotonic()<until:
                            if events.read_text().count('"event":"chunk_committed"')>=args.kill_after_commits:break
                            if proc.poll() is not None:raise RuntimeError('agent finished before SIGKILL point')
                            time.sleep(.01)
                        else:raise RuntimeError('no durable progress before SIGKILL deadline')
                    finally:
                        if proc.poll() is None:proc.kill()
                        proc.wait(timeout=10)
                time.sleep(.15)
                envelope=json.loads((origin/'releases'/f'{release}.json').read_text())
                manifest=json.loads(base64.b64decode(envelope['payload']))
                preserved=set()
                for chunk in manifest['chunks']:
                    cached=edge/'cache'/chunk['sha256'][:2]/chunk['sha256']
                    if cached.exists() and file_hash(cached)==chunk['sha256']:
                        h=chunk['encoded_sha256'];preserved.add(f'chunks/{h[:2]}/{h}.gz')
                assert 0<len(preserved)<len({c['encoded_sha256'] for c in manifest['chunks']}),'SIGKILL must interrupt a proper subset'
                resume_before=get(base+'/stats');preserved_count=len(preserved)
                dump(work/'sigkill-before-resume.json',dict(preserved_objects=sorted(preserved),origin_stats=resume_before,returncode=proc.returncode))
            with (work/'logs'/f'{release}.json').open('w') as out,(work/'logs'/f'{release}.jsonl').open('w') as err:
                subprocess.run(command(release),stdout=out,stderr=err,env=receiver_env,check=True)
            after=get(base+'/stats')
            if index==0:
                assert all(after['object_requests'].get(k,0)==resume_before['object_requests'].get(k,0) for k in preserved),'committed chunks downloaded again'
                dump(work/'sigkill-after-resume.json',dict(preserved_objects=sorted(preserved),origin_stats=after,re_requested=0))
            image['stored_reference']=daemons['receiver'].stored_reference(image['id'],image['tag'])
            identity=dict(image['identity'],stored_reference=image['stored_reference'])
            loaded_identity=daemons['receiver'].verify_loaded(identity)
            actual=json.loads(docker_text('receiver','run','--rm','--pull','never','--network','none',image['stored_reference']))
            assert actual==image['expected'],f'runtime bytes differ for {release}'
            # Authoritative transfer measurements are read from the sender.
            body=after['chunk_response_body_bytes']-before['chunk_response_body_bytes']
            meta=after['metadata_response_body_bytes']-before['metadata_response_body_bytes']
            baseline=sum(n for h,n in image['layers'].items() if h not in layers_present)
            layers_present.update(image['layers'])
            row=dict(release=release,image_id=image['id'],archive_bytes=image['archive_bytes'],sender_chunk_body_bytes=body,
                     sender_metadata_body_bytes=meta,computed_missing_layer_gzip_bytes=baseline,
                     saving_vs_computed_missing_layers=1-(body+meta)/baseline if baseline else None,
                     elapsed_seconds=time.monotonic()-started,docker_run_verified=True,probe=actual,
                     identity=image['identity'],loaded_identity=loaded_identity,receiver_absence=image['receiver_absence'],
                     archive_sha256=image['archive_sha256'])
            rows.append(row)
            dump(work/'rows.partial.json',rows)
            db=open_db(dbpath);sample(db,base);state=snapshot(db);db.close()
            (work/'sender'/f'{release}-status.txt').write_text(render(state)+'\n')
            print(f"{release:12s} body={body:,}B metadata={meta:,}B whole-missing-layers={baseline:,}B Docker payload verified",flush=True)
        assert rows[2]['sender_chunk_body_bytes']+rows[2]['sender_metadata_body_bytes']<rows[2]['computed_missing_layer_gzip_bytes'],'delta did not beat changed-layer transfer'
        before=get(base+'/stats')
        with (work/'logs/repeat.json').open('w') as out,(work/'logs/repeat.jsonl').open('w') as err:
            subprocess.run(command('app-a-v2'),stdout=out,stderr=err,env=receiver_env,check=True)
        after=get(base+'/stats')
        assert after['chunk_requests']==before['chunk_requests'],'unchanged release fetched chunks'
        assert after['metadata_response_body_bytes']==before['metadata_response_body_bytes'],'unchanged manifest fetched response body'
        if faults['drop_every']:assert after['injected_disconnects']>0
        if faults['fail_first']:assert after['injected_503s']>0
        if faults['corrupt_first']:assert after['injected_corruptions']>0
        server.terminate();server.wait(timeout=10)
        for image in (images[0],images[2]):
            assert json.loads(docker_text('receiver','run','--rm','--pull','never','--network','none',image['stored_reference']))==image['expected']
        time.sleep(.4)
        collector.terminate();collector.wait(timeout=10)
        results=dict(status='PASSED_REAL_DOCKER',sender_platform=sender_platform,receiver_platform=receiver_platform,
                     topology=topology,fault_plan=faults,retry_policy=retry,
                     distinct_docker_daemons=sender_daemon!=receiver_daemon,rate_kbit=args.rate_kbit,size_mib=args.size_mib,
                     changes='32 KiB changed within the existing large payload layer, plus release metadata',
                     measurement='origin HTTP response-body bytes; excludes headers, TCP/TLS overhead and outbound receipts',
                     baseline='computed gzip size of missing whole layers; not a measured ordinary registry pull',
                     rows=rows,sigkill_durable_chunks_preserved=preserved_count,sigkill_preserved_chunks_re_requested=0,
                     unchanged_chunk_requests=0,unchanged_manifest_body_bytes=0,offline_old_and_new_images_verified=True,origin_stats=after)
        dump(work/'results.json',results)
        lines=['# Real Docker end-to-end results','',f"Status: **{results['status']}**. Limit: {args.rate_kbit} kbit/s.",'',
               '| Release | Chunk body bytes | Manifest body bytes | Computed missing-layer gzip bytes | Docker run |',
               '|---|---:|---:|---:|---|']
        for row in rows:lines.append(f"| {row['release']} | {row['sender_chunk_body_bytes']:,} | {row['sender_metadata_body_bytes']:,} | {row['computed_missing_layer_gzip_bytes']:,} | Payload hashes verified |")
        lines+=['',f'{preserved_count} chunks survived SIGKILL without another request. Unchanged release: zero chunk requests.',
                'Old and new images ran with the origin stopped and container networking disabled.',
                '',results['measurement'],results['baseline'],'',
                f"Topology: {topology['classification']}. Source config ID, source index ID and tag were absent before load.",
                'Signed image IDs are preserved config digests; source OCI index/manifest IDs are recorded separately as provenance.']
        (work/'RESULTS.md').write_text('\n'.join(lines)+'\n')
        subprocess.run(['python3',str(ROOT/'scripts/sender.py'),'report','--db',str(dbpath),'--out',str(work/'sender/report')],check=True)
        # SQLite backup API yields a consistent snapshot, including WAL contents.
        with sqlite3.connect(dbpath) as source,sqlite3.connect(work/'sender/collector.backup.sqlite') as target:
            source.backup(target)
            assert target.execute('PRAGMA integrity_check').fetchone()[0]=='ok'
        print(f'PASSED: {work}/RESULTS.md',flush=True)
    except BaseException as exc:
        dump(work/'failure.json',dict(status='FAILED_REAL_DOCKER_ATTEMPT',error_type=type(exc).__name__,error=str(exc),owned_tags=owned_tags))
        raise
    finally:
        for proc in (collector,server):
            if proc and proc.poll() is None:
                proc.terminate()
                try:proc.wait(timeout=10)
                except subprocess.TimeoutExpired:proc.kill();proc.wait()
        for log in opened:log.close()
        cleanup=[]
        for tag in owned_tags:
            for which in ('sender','receiver'):
                try:
                    result=docker(which,'image','rm',tag,check=False,capture_output=True,text=True,timeout=30)
                    cleanup.append(dict(which=which,tag=tag,exit_code=result.returncode,stdout=result.stdout,stderr=result.stderr))
                except subprocess.TimeoutExpired as exc:
                    cleanup.append(dict(which=which,tag=tag,error=str(exc)))
        dump(work/'cleanup.json',cleanup)

if __name__=='__main__':main()
