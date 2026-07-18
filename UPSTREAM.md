# Upstream and Tag Policy

## Relationship

This repository is a maintained fork of `QuantumNous/new-api`. The `v0.2`
product line intentionally starts from the fork's `v0.1.6` release rather than
from later channel-routing releases.

## Branch Synchronization

- Upstream changes are reviewed and ported selectively.
- Do not merge upstream `main` directly into the `v0.2` line.
- Do not cherry-pick mixed commits that combine desired fixes with channel
  routing, billing migrations, or unrelated runtime changes.
- Each ported change must retain its upstream attribution and license notices.

## Tag Isolation

The fork and upstream already contain same-named tags that point to different
commits. Upstream also has its own `v0.2.0` tag.

To avoid local tag corruption:

- Never run `git fetch --all --tags` in this repository.
- Configure the upstream remote with `tagOpt = --no-tags`.
- Fetch upstream branches without importing upstream tags.
- If an upstream tag must be inspected, fetch it into a separate namespace such
  as `refs/upstream-tags/*`, not `refs/tags/*`.
- Fork release tags are immutable after publication.

Example safe synchronization:

```bash
git config remote.upstream.tagOpt --no-tags
git fetch upstream main
```

## Release Naming

The product version may remain `v0.2.0`. Release notes must state that it is the
fork's line based on `v0.1.6` and is not the same artifact as the upstream
project's `v0.2.0` tag.
