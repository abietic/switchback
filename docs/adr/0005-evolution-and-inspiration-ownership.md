# Evolution And Inspiration Ownership

Evolution owns candidate generation, immutable revisions, risk-gated publication, lifecycle, audit, user review, and reading in Switchback RDBMS. OpenViking remains a required serving dependency for active Truth and Resource retrieval, but it is a projection and snapshot/restore target rather than the governance source of truth. Low-risk simple MemoryTruth may auto-publish after deterministic checks; InstructionPolicy and SkillPackage generation or modification require human review.

Inspiration uses a protected Git repository with one YAML InspirationEntry per resource, Markdown context pages, and a deterministically generated `INSPIRATION_INDEX.md`. Semantic changes from humans, sync automation, or models go through branch/PR and CODEOWNER review. PostgreSQL ResourceRecord and OpenViking L0/L1 are rebuildable projections; full Feishu/Wiki content remains in the source system and is read through lark-cli and the relevant Skill. This keeps long-term assertions, executable behavior, and external document discovery from collapsing into one ungoverned context source.

## Status

Accepted as draft baseline.
