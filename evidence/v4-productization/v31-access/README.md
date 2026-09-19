# External Linux access probe — 2026-09-19

Read-only probes from the macOS coordinating workstation:

| Probe | Observed result |
|---|---|
| Existing SSH name `remote-linux-host`, BatchMode, 10-second connection timeout | TCP port 22 timed out (exit 255) |
| Existing peer Tailscale address, SSH as ubuntu, same timeout | TCP port 22 timed out (exit 255) |
| Previously documented private-subnet address, SSH with existing OCI identity | Network is unreachable (exit 255); historical address not revalidated |
| Tailscale peer status | Peer reports Online |
| Two Tailscale pings | DERP(lhr) replies, 16 ms and 11 ms; direct connection not established |

Online/DERP reachability is **not SSH access**, workload safety, or a direct dataplane proof.
Guest OS, architecture, current workload/route role, free space, installed runtime and
owned temporary scope could not be established. No files/processes were deployed there.
No bastion recovery, OCI resource, firewall, route, system service, or Tailscale ACL/account
changes were attempted. A key was referenced by SSH but never printed or copied.

**Standalone Linux host validation: NOT RUN.**
**Linux Tailscale delivery/overhead comparison: NOT RUN.**

Fallback measurements used an owned ephemeral Alpine 3.20 container on the local
OrbStack Linux VM, Linux x86_64 kernel `7.0.14-orbstack-00380-ga7e0a2dc9535`.
The initial container filesystem reported 273 GiB available. Python 3 and iproute2
were installed only in that disposable container. NET_ADMIN was limited to its network
namespace; no host network or Docker socket was mounted. Hub and clients ran the matching
static Linux amd64 binary as continuously running daemon processes. This is local
Linux-VM evidence, not standalone Linux acceptance.
