# OpenSpec in this repository

The existing transport contract is under `specs/delta-transport/`. The active change
is `changes/docker-e2e-sender-observability/`: proposal (why), design (how), specs
(required behavior and scenarios), and tasks (implementation and validation).

Read those files directly; the demo does not depend on the OpenSpec CLI. The local
`make openspec-check` performs a deliberately limited structural check only.

With the official CLI installed, run:

```sh
openspec validate --all --strict --no-interactive
openspec show docker-e2e-sender-observability --type change
```

The official CLI was not available in the authoring sandbox; do not confuse the
local structural checker with official validation. Keep the change active until
its real Docker execution and outstanding acceptance tasks have been reviewed.
See the [official CLI reference](https://github.com/Fission-AI/OpenSpec/blob/main/docs/cli.md).
