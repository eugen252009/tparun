# Agent Instructions

These rules govern AI-assisted implementation, review, planning, and refactoring in this repository and its Bloom-related work.

## Bloom North Star

Turn temporary agent reasoning into tested, reusable executable capability. Prefer accumulated contracts, plans, artifacts, and validated execution paths over repeated one-off code generation.

## Mandatory Discovery Before Creation

Before implementing, proposing, or manually recreating any non-trivial capability:

1. Search Bloom's capability discovery interface first:
   ```bash
   bloom search "<semantic capability need>"
   ```
2. If a plausible result exists, inspect it with:
   ```bash
   bloom describe <capability>
   ```
3. Prefer reuse, then composition, then coherent generalization.
4. Only after those fail, record the genuine gap and consider the smallest general-purpose addition.

Search semantic needs, not predetermined implementation names. Do not spam queries or include secrets/payloads. A search miss is useful telemetry and does not by itself justify creating a primitive. If discovery is unavailable, report that explicitly.

## Architecture Rules

- Prefer composition over domain-specific executables and one-off helpers.
- Keep primitives coherent, orthogonal, typed, deterministic where practical, and independently testable.
- Keep capability identity separate from artifact/package identity.
- Keep vBuf-compatible data in vBuf form through internal pipelines where practical; avoid unnecessary parsing, serialization, copying, and materialization.
- Distinguish streamable, bounded-lookahead, seek-dependent, materializing, and global operations honestly.
- Keep vBuf Core minimal; policy belongs above Core.
- Maintain one canonical persistent authority; do not persist cheaply derivable duplicates.
- Separate persistent, runtime, backend, source, artifact, and active-runtime representations.
- Declare external effects explicitly: filesystem, network, process, clock, randomness, and external services.
- Prefer deterministic behavior and reproducible validation.
- Prevent semantically duplicate capabilities and capability accretion.
- Treat generated code as untrusted until isolated build, validation, tests, immutable artifact, registry publication, and explicit activation.
- Do not claim roadmap or proposed behavior is implemented.
- Preserve invariants over implementation convenience; stop and document conflicts instead of weakening contracts.

## Evidence and Testing

Tests must exercise the real production path. When a reusable invariant or guard exists:

```text
positive scenario → same predicate → accept
negative scenario → same predicate → reject
```

Do not use helper-only copies of validation, planning, ABI, registry, or runtime logic as evidence. Effects and ordering must be machine-observed. PASS labels are valid only when all promised primitive assertions have succeeded.

## Change Discipline

Before non-trivial changes, ask:

- Does this already exist?
- Can it be composed or generalized?
- Is it a capability, plan, or artifact?
- Does it add duplicated authority or unnecessary data movement?
- Are effects and determinism explicit?
- Does it belong in Core, a superset, Bloom, or runtime?
- Does the test use the same production predicate?
- Is the feature implemented now rather than merely proposed?

Keep source revision, build artifact, publication, and runtime activation separate. Do not push, deploy, or mutate production unless explicitly authorized. Freeze exact review SHAs and do not create commits afterward when a review handoff requires immutability.

## Privileged operations

- If `mayi` is available on the current machine, use it for privileged operations; run unprivileged commands normally.
- If Mayi is unavailable, follow the machine/project's existing documented privilege workflow.
- Do not bypass Mayi with `sudo`, `su`, `pkexec`, an improvised root shell, or another escalation path.
- For current syntax and behavior, consult `man mayi` and `man mayi-workflow`.
- If Mayi rejects an operation, do not evade the restriction with shell wrappers or by moving/copying scripts or executables. Report the blocker or find a narrower legitimate operation.
- After an approved privileged operation, verify the result with unprivileged/read-only commands where possible.
