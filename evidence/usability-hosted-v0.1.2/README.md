# Hosted v0.1.2 selected raw evidence

Interpretation, measurements and limitations live in [TESTING.md](../../TESTING.md#hosted-v012-acceptance-and-complete-release-delivery).

- [result.json](result.json): acceptance gates, hosted archive/binary identity and teardown outcome.
- [summary-1.json](summary-1.json), [summary-2.json](summary-2.json), [summary-2-restart.json](summary-2-restart.json): first loaded completions.
- [cleanup-verification.json](cleanup-verification.json): post-run owned-guest absence check; `result_sha256` identifies the original private capture, not the sanitized copy.
- [anonymous-assets.json](anonymous-assets.json), [SHA256SUMS](SHA256SUMS): public download HTTP status and hashes, plus original release checksum file.
- [archive-inspection.json](archive-inspection.json): archive members, binary formats/architectures and chart metadata. Inspection itself does not execute binaries.
- [Release workflow](workflow-35511807780.json), [source test workflow](workflow-35511696941.json), [tag test workflow](workflow-35511807775.json): GitHub CLI JSON responses with source commit, job/step conclusions and public URLs.
- [provenance.json](provenance.json): original work-record paths and SHA-256 hashes, retained-copy hashes and exact transformation per file.

Only the personal guest home prefix in `result.json` was replaced with `$HOME`; this is a placeholder, not a literal executed path. All other selected work records are byte-for-byte copies. Original artifact hashes, measurements and guest identifiers remain unchanged. Full transcripts, private keys, credential contents, signed download redirects and unrelated host inventory are not included. See the [redaction policy](../REDACTION.md).
