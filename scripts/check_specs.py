#!/usr/bin/env python3
"""Offline structural checks only; NOT a replacement for the official OpenSpec CLI."""
import json
import re
import subprocess
import sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
def main():
    change=ROOT/'openspec/changes/docker-e2e-sender-observability'
    for name in ('proposal.md','design.md','tasks.md','.openspec.yaml'):
        assert (change/name).is_file(),name
    count=0
    for path in (ROOT/'openspec').rglob('spec.md'):
        text=path.read_text();requirements=re.split(r'^### Requirement: ',text,flags=re.M)[1:]
        assert requirements,f'no requirements: {path}'
        for requirement in requirements:
            assert 'SHALL' in requirement or 'MUST' in requirement,path
            assert '#### Scenario:' in requirement,path
            assert '**WHEN**' in requirement and '**THEN**' in requirement,path
            count+=1
    tasks=(change/'tasks.md').read_text()
    assert re.search(r'- \[([ x])\] 1\.7 Execute the real Docker',tasks),'Keep an explicit Docker execution task'
    if '- [x] 1.7 Execute the real Docker' in tasks:
        assert any('PASSED_REAL_DOCKER' in p.read_text() for p in (ROOT/'evidence').rglob('*.json')),'Docker validation requires retained execution evidence'
    scenes=list((ROOT/'docs/diagrams/legacy').glob('*.excalidraw'))
    assert len(scenes)==3, 'retain the three historical editable experiment scenes'
    for path in scenes:
        scene=json.loads(path.read_text())
        assert scene['type']=='excalidraw' and scene['version']==2
        ids=[e['id'] for e in scene['elements']];assert len(ids)==len(set(ids))
        assert any(e['type']=='arrow' for e in scene['elements'])
        assert any(e['type']=='text' for e in scene['elements'])
        assert path.with_suffix('.svg').exists()
    subprocess.run([sys.executable,str(ROOT/'scripts/diagrams.py'),'--check'],check=True)
    print(f'Local structural checks passed: {count} requirements, {len(scenes)} historical editable scenes and canonical HTML/SVG. Official OpenSpec CLI validation is separate.')
if __name__=='__main__':main()
