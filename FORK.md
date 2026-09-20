# Fork maintenance

`main` is the maintained fork baseline. `upstream/main` tracks the original
project through the `upstream` remote; it is not the target for local fixes.
Develop changes on feature branches and integrate independently reviewable
commits. Preserve contributor authorship and the existing license notices.

## Initial integration

Based on upstream `ae067228d76df011375164814b729932ed55ca2f`:

- Upstream [PR #43](https://github.com/omarroth/doubletake/pull/43), original
  commit `79a494f`: VA-memory Wayland capture for VA-API H.264. The author's
  commit is retained unchanged.
- `88ba5aa`: daemon shared-video queues tolerate short encoder bursts, with a
  250 ms nominal-duration ceiling and existing byte/count limits. This changes
  capacity, not scheduled playback delay; sustained slow receivers are still
  detached independently.
- `8bc85ed`: retry missing PTP clock identity once using a fresh authenticated
  NTP session. Includes LG receiver simulation and failure-path coverage.

The original capture experiment on `fix/wayland-capture-freeze` is retained for
reference, not included wholesale. Its clock override and eight-buffer request
were not necessary in the tested PR #43 H.264 pipeline.

The privileged Omarchy/UFW helper remains on `networkd`. It is not required or
installed by this baseline. That branch retains the old capture experiment in
its ancestry: integrate its dedicated helper commit, not its entire branch,
after reviewing optional-by-default behavior and compatibility with NTP retry.

## Validation evidence and limits

Hardware results from September 16–17, 2026:

| Configuration | Evidence | Limitations |
| --- | --- | --- |
| PR #43, direct CLI, Hyprland/VA-API H.264, AppleTV5,3 | Over five minutes / 9,000 frames; user confirmed proportions, idle/resume responsiveness, and TV audio | No long-duration sync or alternate-GPU coverage |
| PR #43 plus daemon queue fix, Apple TV | Reproduced two-frame queue overflow before fix; responsive video after fix, including plugin launcher | Full multi-receiver hardware test and long-duration run remain pending |
| LG OLED55B9PLA, sourceVersion 377.25.06, NTP retry | Forced NTP and automatic retry produced responsive video; automatic run sent over 5,000 frames | TV audio failed; this hardware test used the earlier capture implementation |

The combined baseline requires fresh end-to-end verification on both receivers.
Automated receiver tests exercise negotiation, authenticated reconnects, normal
PTP, bounded retry failure, and isolated slow consumers. They cannot establish
real GPU/portal interoperability or audible, synchronized TV playback.

## Before tagging a fork release

- Run `go test -race ./...`, `go vet ./...`, and `make all`.
- Check direct CLI and daemon/plugin operation separately.
- Check connect/disconnect/reconnect, repeated idle/resume, and video and audio
  independently on Apple TV and LG.
- Run a 30–60-minute audio/video synchronization test and a multi-receiver test
  when hardware is available; record unavailable coverage explicitly.
- Record the exact commit, compositor, GPU/driver, PipeWire/GStreamer versions,
  receiver model/firmware, and remaining limitations.
- Use a clearly identified fork tag such as `v0.4.0-alchemy.1`; publish concise
  release notes distinguishing observed fixes from unresolved issues.

No release tag is implied by establishing this integration baseline. LG audio
and extend mode are subsequent work, not claimed features of this baseline.
