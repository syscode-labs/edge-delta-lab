# Production readiness

**Edge Delta is an experiment, not an unattended production fleet updater.**

The maintained readiness ledger now lives in [TESTING.md — limits and missing evidence](../TESTING.md#limits-and-missing-evidence), beside the exact tests and measurements that support it. This page is retained for existing links rather than maintaining a second, drifting list.

Before adoption, [measure representative images and your real link](../TESTING.md#measure-a-real-site-safely). Read the [trust/integrity model](ARCHITECTURE.md) and [operational security and storage guidance](../deploy/compose/README.md). Loading an image is not activating it, and image rollback is not database rollback.
