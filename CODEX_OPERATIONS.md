# Codex Repository Operations

## Purpose

This repository is routinely maintained, built, and released with Codex. This
document defines the standing repository-scoped authority and the safety gates
that make unattended operations predictable.

## Standing Authorization

When the user asks Codex to manage, build, publish, or release this repository,
Codex may perform the following without asking for repeated confirmation:

- Create and update short-lived branches.
- Commit and push repository-scoped changes.
- Open, update, and merge pull requests after required checks pass.
- Run GitHub Actions, builds, tests, packaging, and release workflows.
- Create a new version tag and a GitHub Release protected by GitHub Immutable
  Releases after release gates pass.
- Update repository settings, labels, topics, and branch policies.
- Delete merged short-lived branches after a verified backup exists.

This authorization does not permit exposing credentials, bypassing failed
checks, force-pushing protected history, rewriting or deleting published tags,
overwriting release assets, or mutating production data without a validated
backup and migration plan.

## Branch Roles

- `main`: protected default branch for active `v0.2` development.
- `support/v0.1`: protected maintenance branch for `v0.1.6`-compatible fixes.
- `archive/channel-routing-v0.1.14`: locked historical branch; never use it as
  a development or release source.
- Short-lived work branches: created from the intended target branch and
  deleted automatically after a successful squash merge.

## Required Operating Flow

1. Confirm the worktree and remote state.
2. Create or reuse a correctly named branch from `main`, or from
   `support/v0.1` for an explicitly scoped maintenance fix.
3. Make the smallest scoped change.
4. Run local checks when the toolchain is available.
5. Push and wait for GitHub checks.
6. Fix failures before merge or release.
7. Merge only after all required checks pass.
8. Publish only from the protected release line and only with new publish-once
   version tags.
9. Verify release assets, checksums, image manifests, signatures, and status
   endpoints after publication.

## v0.1.6 Compatibility Guard

- The `v0.1.6` tag, release assets, and published images are the preservation
  baseline and must never be modified or replaced. The Git tag is protected by
  repository rules; retain recorded checksums and image digests because the
  historical assets and image tags predate platform-enforced immutability.
- The `v0.2` line starts from the exact `v0.1.6` commit.
- Repository-governance work must not alter runtime source or deployment
  defaults.
- Runtime changes require the acceptance criteria in `COMPATIBILITY.md`.
- Channel-routing changes from `v0.1.7` through `v0.1.14` must not be imported
  wholesale.

## Release Guard

Codex must not promote `latest` merely because a tag exists. Promotion requires
successful builds, tests, smoke checks, complete assets, and release notes. A
failed or incomplete workflow leaves the release unpublished or pre-release and
must never be described as production-ready.

`v0.2.0` must not move the global container `latest` alias while direct upgrades
from `v0.1.7` through `v0.1.14` remain unsupported. Publish and deploy the v0.2
line by exact version tag and verified manifest digest.

Run-scoped container candidate references may exist only as release-pipeline
intermediates for smoke, vulnerability, attestation, and signature gates. They
are not stable deployment aliases and must never be documented or promoted as
the release image. A previously published version digest may be resumed only
when its tag-bound signature and version/commit-bound attestations verify; it
must never be replaced by a fresh candidate.

Every multi-architecture image build must pass the target operating system and
architecture explicitly from its native-runner matrix, then run the version and
startup smoke on that architecture before signing or combining manifests. A
release recovery may use a later protected workflow commit to repair release
orchestration only when the build context and provenance remain bound to the
immutable release tag. The exact workflow commit identity used for keyless
signing must be verified and recorded in the container manifest asset.

If stable publication completed but the workflow lost its final confirmation,
a recovery run may resume only the exact immutable public Release after
re-verifying its tag, body, complete asset names and digests, checksums,
container manifest, signatures, and attestations. It must not recreate a
draft, upload over an immutable asset, or move a version tag.

GitHub draft Releases must be discovered by enumerating authenticated Release
list results and requiring exactly one matching tag. The public
`releases/tags/{tag}` endpoint, tag-based `gh release` asset commands, and
independent workflow writers are not valid draft coordination mechanisms.
After discovery, all draft reads and mutations use the fixed Release ID and
asset IDs, never overwrite an existing asset, and reconcile an uncertain write
by reading it back instead of blindly retrying the mutation.
