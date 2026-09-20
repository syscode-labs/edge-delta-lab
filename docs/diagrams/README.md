# Diagram sources

**Current:** [architecture.html](architecture.html) is the single editable source; [architecture.svg](architecture.svg) is its accessible README preview. Edit the HTML, run `python3 scripts/diagrams.py`, then `python3 scripts/diagrams.py --check`. `make diagrams` calls the same exporter. It never regenerates legacy diagrams.

Design: 1280×720, six nodes, light editorial layout. Google Fonts (Chakra Petch / IBM Plex Mono) are optional: image-embedded SVGs and offline viewers use Arial/monospace fallbacks. Inspect both font modes after edits.

## Fidelity and retirement ledger

- Kept the full Registry → publisher → hub → optional removable mTLS proxy → receiver → Docker delivery path.
- Exposed private signing authority at the publisher and pinned public verification at the receiver. Transport certificates are a different trust system.
- Folded receiver cache/reconstruction into one receiver node; added an existing-HTTPS bypass so the optional proxy is not mistaken for a core dependency.
- Omitted collector, receipts, admin sockets, notification hints and monitoring from the overview. Those details remain in [architecture](../ARCHITECTURE.md) and [monitoring](../GRAFANA.md).
- **Retired:** the three [legacy Excalidraw/SVG pairs](legacy/) depict the earlier Docker-demo experiment, not the current packaged product. The old architecture starts with `Docker build + save`, omits the registry watcher and transport boundary, and foregrounds unauthenticated receipts. The other two illustrate fixture chunk reuse and the legacy acknowledgement sequence. They are frozen historical illustrations, not installation or security guidance.
- The former generator was replaced by the HTML-to-SVG exporter, so `make diagrams` cannot resurrect an obsolete current architecture. Historical OpenSpec remains intact; its original Excalidraw requirement refers to those preserved experiment artifacts.

## Validation

`--check` checks XML/accessibility, HTML/SVG identity, the six-node inventory, 4px grid, node bounds, straight-edge ports, non-overlap, the orthogonal bypass and its 8px label gap. It is intentionally tied to this canonical layout; update its assertions when changing the layout. It does not claim browser font measurement. Visually inspect actual HTML and the SVG as an image after edits; preserve readable fallback text and the 40px safe margin.
