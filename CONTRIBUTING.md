# Contributing

## Development Model

Long-lived branches have one clear role:

- `main` is the active `v0.2` development line based on `v0.1.6`.
- `support/v0.1` is the production-compatible maintenance line for critical
  fixes that must remain safe for `v0.1.6` deployments.
- `archive/channel-routing-v0.1.14` is a locked historical branch for the
  intentionally skipped channel-routing releases. Do not develop from it.

The repository otherwise uses short-lived branches:

- `feat/<topic>` for features.
- `fix/<topic>` for bug fixes.
- `chore/<topic>` for repository and dependency maintenance.
- `docs/<topic>` for documentation.
- `refactor/<topic>` for behavior-preserving restructuring.
- `hotfix/<topic>` for urgent supported-release fixes.

Create normal work from and target it back to `main`. Create a production
hotfix from `support/v0.1` only when the same fix must be released on the
supported `v0.1` line. Read `COMPATIBILITY.md` before making runtime changes and
`UPSTREAM.md` before porting upstream code.

## Pull Requests

- Keep each pull request focused on one outcome.
- Use a Conventional Commit-style title such as `feat:`, `fix:`, `chore:`, or
  `docs:`.
- Complete the repository pull request template.
- Include the exact tests, builds, or manual flows used for verification.
- Do not include credentials, production data, raw tokens, or private logs.

AI-assisted and Codex-managed contributions are allowed. They must be reviewed
against the actual code, tested, and disclosed in the pull request when the
repository policy requires it. Unverified generated output is not acceptable.

## Required Validation

For backend changes:

- Run the affected Go tests and the complete backend test suite.
- Verify SQLite, MySQL, and PostgreSQL compatibility for database changes.
- Trace billing changes through validation, pre-consume, settlement, refund,
  and logging.

For default frontend changes:

- Run type checking, linting, formatting checks, and the production build.
- Preserve i18n, accessibility, loading, empty, and error states.

For classic frontend changes:

- Run the production build and verify the affected interaction manually.

For release or workflow changes:

- Pin every external GitHub Action to a full commit SHA.
- Keep permissions least-privileged.
- Verify that failed builds cannot update stable or `latest` tags.

## Protected Project Information

New API and QuantumNous attribution, licensing, module paths, and other
protected project identity information must remain intact as required by
`AGENTS.md`.
