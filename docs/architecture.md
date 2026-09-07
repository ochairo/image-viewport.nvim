# Architecture

`init.lua` owns initialization order and readiness. `config.lua` resolves caller options
and enforces the processor/remote-download invariants. `viewport.lua` owns geometry,
per-window controllers, visible frames, panning, and cleanup. `processor.lua` owns bounded
jobs, shared transforms, cache ownership, cancellation, and the image.nvim adapter.

Runtime assets are discovered relative to the actual Lua module source. They are validated
before execution; installation under `stdpath("config")` is not required. `runtime/launch.py`
seals input snapshots, supervises Bubblewrap, and publishes verified private output.
`worker.py` performs bounded conversions under `policy.xml`. No processing fallback bypasses
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
