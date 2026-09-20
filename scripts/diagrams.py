#!/usr/bin/env python3
"""Export the canonical HTML figure; reject drift and unsafe diagram geometry.

No legacy scene generator remains: historical Excalidraw/SVG pairs are frozen in
legacy/. This exporter deliberately does not redraw or regenerate those scenes.
"""
from __future__ import annotations

import argparse
from pathlib import Path
import re
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / 'docs/diagrams/architecture.html'
TARGET = SOURCE.with_suffix('.svg')
NS = {'s': 'http://www.w3.org/2000/svg'}


def box(element):
    return tuple(float(element.get(k, '0')) for k in ('x', 'y', 'width', 'height'))


def overlaps(a, b):
    x, y, w, h = a
    xx, yy, ww, hh = b
    return x < xx + ww and xx < x + w and y < yy + hh and yy < y + h


def validate(svg):
    root = ET.fromstring(svg)
    assert root.get('viewBox') == '0 0 1280 720', 'doc-wide frame changed'
    assert root.get('role') == 'img'
    assert root[0].tag.endswith('}title') and root[0].text
    ids = [e.get('id') for e in root.iter() if e.get('id')]
    assert len(ids) == len(set(ids)), 'duplicate IDs'
    for ident in root.get('aria-labelledby', '').split():
        assert ident.startswith('architecture-') and ident in ids
        assert next(e for e in root.iter() if e.get('id') == ident).text
    assert len(root.get('aria-labelledby', '').split()) == 2
    nodes = {g.get('data-node'): box(g.find('s:rect', NS))
             for g in root.findall('s:g[@data-node]', NS)}
    assert set(nodes) == {'registry', 'publisher', 'hub', 'proxy', 'receiver', 'docker'}
    for name, bounds in nodes.items():
        x, y, w, h = bounds
        assert 40 <= x and x + w <= 1240 and 40 <= y and y + h <= 660
        for other, theirs in nodes.items():
            assert name == other or not overlaps(bounds, theirs), (name, other)
    # Grid rule (marker geometry is separately fixed at 8px).
    for e in root.iter():
        for key in ('x', 'y', 'width', 'height', 'x1', 'x2', 'y1', 'y2', 'font-size'):
            if key in e.attrib:
                assert float(e.get(key)) % 4 == 0, (e.tag, key, e.get(key))
    segments = []
    for e in root.findall('.//s:line[@data-edge]', NS):
        src, dst = e.get('data-edge').split()
        x1, y1, x2, y2 = (float(e.get(k)) for k in ('x1', 'y1', 'x2', 'y2'))
        assert y1 == y2 and x1 < x2, 'off-axis/reversed content connector'
        sx, sy, sw, sh = nodes[src]
        dx, dy, dw, dh = nodes[dst]
        assert x1 == sx + sw and x2 == dx and sy < y1 < sy + sh and dy < y2 < dy + dh
        for name, (x, y, w, h) in nodes.items():
            assert name in (src, dst) or not (y < y1 < y + h and x < x2 and x + w > x1)
        for ax, ay, bx, by in segments:
            assert ay != y1 or max(ax, x1) >= min(bx, x2), 'overlapping connectors'
        segments.append((x1, y1, x2, y2))
    assert len(segments) == 5
    # Fixed orthogonal bypass with 8px corners; all segments clear non-endpoint nodes.
    bypass = root.find('.//s:path[@data-edge]', NS)
    assert bypass.get('d') == 'M536 404 V480 Q536 488 544 488 H936 Q944 488 944 480 V404'
    assert bypass.get('data-edge') == 'hub receiver'
    assert nodes['hub'] == (464, 300, 144, 104)
    assert nodes['receiver'] == (864, 300, 160, 104)
    assert all(y + h < 480 for x, y, w, h in nodes.values())
    mask = box(root.find('.//s:rect[@data-label]', NS))
    assert all(not overlaps(mask, b) for b in nodes.values()), 'label overlaps node'
    assert 6 <= 488 - (mask[1] + mask[3]) <= 10, 'label/connector gap'
    assert 544 < mask[0] and mask[0] + mask[2] < 936
    for forbidden in ('<script', '<foreignObject', 'box-shadow', 'JetBrains'):
        assert forbidden not in svg, forbidden
    print('PASS: accessible SVG, six nodes, grid, clear connectors and label geometry')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check', action='store_true', help='verify committed SVG without writing')
    args = parser.parse_args()
    match = re.search(r'<svg\b.*?</svg>', SOURCE.read_text(), re.S)
    assert match, 'missing inline SVG'
    svg = match.group(0)
    validate(svg)
    result = '<?xml version="1.0" encoding="UTF-8"?>\n' + svg + '\n'
    if args.check:
        assert TARGET.read_text() == result, 'SVG drift: run python3 scripts/diagrams.py'
        print('PASS: canonical HTML and exported SVG agree')
    else:
        TARGET.write_text(result)
        print(f'Exported {TARGET.relative_to(ROOT)}')


if __name__ == '__main__':
    main()
