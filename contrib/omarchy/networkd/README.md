# Automatic AirPlay firewall allowances on Omarchy

`doubletake-networkd` installs temporary, receiver-specific UDP allowances for
AirPlay sessions. The screen capture, pairing and media processes remain
unprivileged. The initial supported configuration is **IPv4, active UFW with
allow-outgoing policy, systemd, D-Bus and Polkit**. It uses the existing LAN.

## Installation

From the repository root:

```sh
make doubletake-networkd
sudo python3 contrib/omarchy/networkd/install.py
```

Required host tools: Python 3, UFW, iptables/iptables-restore, systemd and Polkit.
The installer does not install dependencies or enable a disabled firewall.
Installation reloads UFW, ending any existing managed casting sessions
(including Waycast sessions). It preserves unrelated UFW rules and Waycast's hook.

The CLI enables the helper by default, including in daemon mode. After the
one-time installation, active local desktop users can cast without a password
prompt. Missing helper or denied authorization fails setup with an error.
For another firewall, IPv6, loopback test receivers, or manually configured hosts:

```sh
bin/doubletake -network-helper=false -target 192.168.3.122 -port-range 60000-60010
```

In manual mode, configure any necessary firewall allowances yourself. The
library's zero-value `StreamConfig` also retains manual networking.
`-port-range` remains optional in managed mode: the helper uses actual sockets,
including when the OS chooses ephemeral ports.

Inspect or restart the helper:

```sh
systemctl status doubletake-networkd
journalctl -u doubletake-networkd
sudo systemctl restart doubletake-networkd
```

Uninstall only this integration:

```sh
sudo python3 contrib/omarchy/networkd/install.py --uninstall
```

Stage installation without changing the host (supply an existing UFW rules file
under the staging root first):

```sh
python3 contrib/omarchy/networkd/install.py --destdir /tmp/doubletake-package
```

## Privilege boundary and session lifetime

The root-owned systemd service exposes `org.doubletake.Network1` at
`/org/doubletake/Network1` on the system bus:

- `Begin(control FD, timing FD, audio-control FD, audio-data FD)`: authorize the bus sender with Polkit; validate a
  connected IPv4 TCP socket and three consecutive unprivileged UDP sockets owned
  by that UID; derive the receiver and local addresses from the control socket.
- `Alive()`: report whether this unique bus sender still owns a lease.
- `End()`: remove only this sender's rules before releasing retained descriptors.

Each stream uses a dedicated bus connection and may own one lease. Up to 32
leases can coexist. The helper retains descriptors so ports cannot be recycled
before cleanup. Permission is scoped to the receiver's source IPv4 address,
local destination IPv4 address, local interface and three UDP destination ports.
Remote UDP source ports are not constrained because the initial timing probe
can precede the receiver's SETUP response. This validates network endpoints,
not the receiver's cryptographic AirPlay identity.

The client reserves sockets and obtains permission **before advertising the
ports in SETUP**. It releases the lease on setup failure and normal teardown.
The helper polls caller ownership and interface/address validity every second;
client disappearance removes that session's rules. The client polls its lease
and closes the stream when the helper loses it. Cleanup is bounded by polling
and command/D-Bus timeouts, rather than instantaneous.

The installer adds an empty `doubletake-input` chain and early jump inside
UFW's `before.rules`, plus a flush so UFW reloads discard temporary permissions.
Runtime updates use `iptables-restore --noflush` with the xtables lock and only
replace this dedicated chain. All active leases are included in each update.
No per-session rules are persisted. An external rule change ends the helper;
permissions are not reconstructed from stale sessions. Reconnect afterwards.
Startup and systemd `ExecStopPost` clear leftovers. Cleanup failure is logged
and requires administrator attention if recovery cleanup also fails.

The service limits capabilities to `CAP_NET_ADMIN`, makes persistent system
paths read-only, and uses fixed absolute command paths, validated arguments,
a clean environment and command timeouts. It does not manage NetworkManager,
DHCP, routing, DNS, P2P interfaces, or discovery firewall policy. UFW's standard
mDNS allowance remains necessary for automatic discovery. Custom overlapping
firewalls/VPN policies remain outside this backend's scope. Rule checks detect
changes to this helper's chain and removal of its hook, not every possible host
firewall change. LAN interface identity is checked by index and local address;
it is not tagged or exclusively owned by the helper.

## Verification

```sh
go test ./...
go test -race ./internal/networkhelper ./internal/networkd/...
python3 -m unittest discover -s contrib/omarchy/networkd
python3 contrib/omarchy/networkd/run_network_test.py
```

The last command creates a disposable user/network namespace and refuses to
operate in the host namespace. It verifies socket validation and actual UDP
blocking, allowance and cleanup. Private D-Bus tests exercise descriptor passing
and lease-loss notification. A real receiver session is still needed to verify
pairing, media playback and the installed host's Polkit/systemd integration.

### Apple TV validation (2026-09-15)

Installed this integration on Omarchy/UFW and paired with AppleTV5,3 over the
existing Wi-Fi LAN. Monitor mirroring used H.264/VA-API at 1280×720, 30 fps.
The user confirmed responsive desktop updates and, in a preceding attempt,
audio from the TV speakers. The helper authorized session setup and installed
three receiver/address/interface-scoped UDP rules; stopping the sender removed
the rules. This receiver did not exercise the inbound UDP allowances during the
observed counters check, so the isolated firewall tests remain the evidence for
actual inbound packet filtering.

The live test also exposed separate capture problems: forcing CPU copies of
portal DMA-BUF memory produced empty buffers. The VA-API capture path now imports
those buffers without CPU copying, forces postprocessing into separate output buffers, requests at
least eight portal buffers, and leaves idle-frame repetition to the compositor
when present. The software capture path retains its source-copy behavior.

An intermittent video freeze remained after those buffer changes. Native
thread stacks from a frozen run showed both the PipeWire source and compositor
waiting on the pipeline clock. Disabling the source clock with
`provide-clock=false` lets the pipeline use the GStreamer system clock and
resolved the observed stall. The option is detected before use so older
PipeWire plugins without this property still launch.

With the clock fix, the monitor stream ran for roughly four minutes and sent
over 6,000 frames; the user confirmed that window and cursor movement remained
responsive after a 15-second idle period. A second run of the final build with
default codec options also sent over 4,000 frames continuously. The full Go
race-test suite, all executable builds, and `go vet ./...` passed.
Final teardown left `doubletake-input` empty and `doubletake-networkd` active.
