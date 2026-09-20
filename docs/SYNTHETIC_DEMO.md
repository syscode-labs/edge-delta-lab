# Synthetic transport demonstration

This developer exercise does not deliver runnable Docker images. For the intended-use installation, start with [the README](../README.md#install-a-publisher-and-hub).

From the source checkout, with Go, make and port 8080 available, use a fresh `work/quickstart` directory.

Terminal 1:

```sh
make build
./bin/edgelab keygen --out work/quickstart/keys
./bin/edgelab fixtures --out work/quickstart/fixtures --size-mib 2
./bin/edgelab publish --input work/quickstart/fixtures/app-a-v1.tar \
  --root work/quickstart/origin --key work/quickstart/keys/publisher.key \
  --release app-v1 --sequence 1
./bin/edgelab promote --root work/quickstart/origin --release app-v1
./bin/edgelab serve --root work/quickstart/origin --listen 127.0.0.1:8080 --events
```

Terminal 2 (leave this client running):

```sh
./bin/edgelab watch --manifest http://127.0.0.1:8080/releases/desired.json \
  --base http://127.0.0.1:8080 --state work/quickstart/client \
  --pub work/quickstart/keys/publisher.pub --allow-http \
  --events-url ws://127.0.0.1:8080/events
```

Wait for the first `app-v1` summary with `phase: staged`; its `reused_chunks` should be zero. In Terminal 3:

```sh
./bin/edgelab publish --input work/quickstart/fixtures/app-a-v2.tar \
  --root work/quickstart/origin --key work/quickstart/keys/publisher.key \
  --release app-v2 --sequence 2
./bin/edgelab promote --root work/quickstart/origin --release app-v2
```

The first `app-v2` summary should reuse chunks and download fewer chunk-response-body bytes than the cold release. The fixture changes 32 KiB. Later summaries describe repeated checks, not original transfers. These counters exclude metadata and network overhead.

Stop processes with Ctrl-C. Preserve client state for reuse; never share it between running clients. Keep release names unique and sequences increasing. HTTP here is only for isolated loopback testing. **Do not add `--docker-load`: these synthetic archives are not runnable Docker images.**

The root `compose.yaml` and `scripts/prepare_compose.py` remain a separate fault-injection simulation. They are not the installation entrypoint.
