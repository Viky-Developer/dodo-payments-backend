# AI Usage

## Tools used

OpenAI Codex was used to inspect the repository, identify consistency and correctness issues, apply explicitly approved edits, and run local verification commands. GitNexus was used for read-only code-flow and change-impact analysis. AI assistance was also used for Docker scaffolding and documentation wording.

## How AI contributed

- Reviewed the repository structure, design documents, migration, handlers, generated database code, and tests.
- Identified invalid duplicate declarations in the initial migration.
- Applied the approved migration syntax repair while retaining the PostgreSQL enum design.
- Made Gin authentication context values explicitly `int64` as requested.
- Added the initial Dockerfile and Docker Compose services for PostgreSQL, migrations, the API, and the mock PSP.
- Ran formatting, migration validation, diff checks, and the Go test suite after changes.

## Decisions retained by the human author

The human author explicitly retained these decisions even when alternatives were identified:

1. Preserve the existing repository folder structure.
2. Preserve the existing physical schema structure and PostgreSQL enum approach.
3. Do not create commits or make unapproved changes.

The human author remains responsible for the architecture and final engineering decisions.

## Corrections and independent verification

AI initially identified a conflict between `DATABASE.md`, which describes `VARCHAR + CHECK`, and the enum-based migration. The human author clarified that the enum-based migration is intentional and must remain. The repair was therefore limited to duplicate declarations and invalid SQL syntax rather than replacing enums.

Correctness was independently checked with the repository's Go tests, `git diff --check`, Goose migration-file validation, targeted source inspection, and GitNexus impact analysis. Container startup still depends on implementing the currently placeholder mock PSP, payment flow, and webhook worker.
