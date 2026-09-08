# Architecture

`init.lua` owns initialization order and readiness. `config.lua` resolves caller options
and enforces the processor/remote-download invariants. `viewport.lua` owns geometry,
per-window controllers, visible frames, panning, and cleanup. `processor.lua` owns bounded
jobs, shared transforms, cache ownership, cancellation, and the image.nvim adapter.

Runtime assets are discovered relative to the actual Lua module source. They are validated
before execution; installation under `stdpath("config")` is not required. `tools/imageprocessor`
owns the Go launcher and worker: sealed input snapshots, Bubblewrap supervision,
bounded conversions under `policy.xml`, and verified private output publication. No processing fallback bypasses
that boundary when dependencies are missing.

The existing adapter intercepts image.nvim's private processor and magic module loaders.
Keep that dependency-sensitive seam inside processor.lua and pinned to dependencies.json.
The upstream project does not become part of this repository; do not patch its files.
An upstream supported custom-processor API would justify revisiting the adapter.

Setup can wait for terminal dimensions without reinstalling image.nvim. It marks the
viewport configured only after factories and autocmds are installed, and repeats do not
multiply wrappers. A failure while loading or setting up upstream is terminal for the session: processing
is shut down, the error is surfaced and later setup calls require a restart. This avoids
retrying after unknown partial upstream mutations.

`make build` compiles static Linux binaries and publishes a content-addressed release
under `runtime/`. A relative `current` symlink selects only a complete release, so
relocating the whole checkout preserves discovery. Runtime admission validates exact
inventory, owner/modes, canonical ancestry, marker, content hash, and current build
source digest before processing. The source digest includes the complete Go source
inventory, module, build entrypoint, and image policy; changed sources require a rebuild.
The original private cache identities and worker protocol remain stable.

The build lock serializes publishers. Exclusive staging and no-replace release rename
preserve foreign entries; selection changes only after successful compilation and source
revalidation. Failed builds retain the previous selector. Old releases remain in place;
there is no automatic release garbage collection or home-directory installation.
Hashes establish consistency of trusted source and build outputs, not publisher identity
or protection against arbitrary code running as the same user.
