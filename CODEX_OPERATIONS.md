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
- Create a new immutable version tag and GitHub Release after release gates pass.
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
8. Publish only from the protected release line and only with new immutable
   tags.
9. Verify release assets, checksums, image manifests, signatures, and status
   endpoints after publication.

## v0.1.6 Compatibility Guard

- The `v0.1.6` tag, release, and published images are immutable.
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
