# Plugin working agreement

Read README.md and docs/architecture.md before changing behavior. Preserve unrelated
work. Do not commit, publish, deploy, or access live provider data without explicit authority.

## Strict Lua typing

Use LuaLS LuaCATS comments with LuaJIT-compatible runtime code. Public API parameters and
returns must be explicitly annotated, including nil/error alternatives. Use named classes
for options and structured values crossing module boundaries; type callback arguments and
results. Annotate new or behaviorally changed functions where inference is ambiguous.
Unchanged internal algorithms may use inference; this is not a fully annotated legacy tree.

Never use `any`, bare `table`, bare `function`, `unknown`, blanket diagnostic suppression,
or fabricated type declarations to make checks pass. Refine runtime values after validation.
Generics and concrete key/value maps are appropriate when the contract is generic.
A cast requires a verified runtime invariant and an adjacent explanation; it must not hide
a mismatch. Keep type definitions with the owning module or its namespace's types.lua.

`make typecheck` is mandatory and analyzes owned runtime Lua with LuaLS. Keep inference,
strict nil/union/table checks and type diagnostics enabled. Never add a diagnostic baseline,
disable checks, or exclude runtime files to get green results. `make static` also checks
annotation policy for supported API/configuration modules; that is not semantic type proof.
Changes to other internal contracts still require review and LuaLS evidence.

## Verification and boundaries

Run `make check`; also run documented native/integration checks for affected boundaries.
Missing tools or reports are failures, not successful skips. Report unavailable evidence.
Keep provider processes, filesystem ownership, sandbox policy, output limits and cancellation
checks intact. Tests use synthetic data and private state; do not load personal config, capture
credentials, enable telemetry, or make live GitHub mutations. No durable domain knowledge
change is needed for mechanical extraction or type-only maintenance.
