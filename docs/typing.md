# Lua type contracts

Use [LuaLS LuaCATS annotations](https://luals.github.io/wiki/annotations/), not a
transpiler or generated runtime checks. LuaLS's current annotations are not fully
compatible with EmmyLua. AGENTS.md owns the mandatory contribution rule.

```lua
---@class Example.Options
---@field enabled? boolean

---@param opts? Example.Options
---@return boolean ready
---@return string? reason
local function setup(opts)
  return opts == nil or opts.enabled ~= false, nil
end
```

The current migration annotates supported APIs, configuration and selected process/state
boundaries. Unchanged internals retain inference; no claim of complete manual annotation
coverage is made. api-contracts.json lists modules whose complete exported signatures are
checked by the portable policy gate. The runtime-wide semantic gate has no baseline or
suppression list; diagnostics must be repaired before release.

Run `make typecheck` with trusted `NVIM` and `LUA_LS` executables available. The runner
loads Neovim's annotation library, checks a deliberate type-error canary, then analyzes
runtime source using strict nil/union/table checking and explicit parameter inference.
It rejects missing/malformed reports and any warning/error diagnostic. Temporary HOME,
XDG, logs and generated metadata are private and removed. No tool is downloaded, no user
configuration is loaded, and telemetry/third-party autodiscovery are disabled.

Failures include bounded project-relative locations, diagnostic codes, and messages. External
library details and private tool logs are not printed. The canary still must detect a known
bad typed call before a source report can be trusted.

See [LuaLS diagnostic reports](https://luals.github.io/wiki/diagnosis-report/) and
[settings](https://luals.github.io/wiki/settings/) for checker semantics. The
[pinned development environment](development.md) supplies the required tools; CI runs
`make check`, including semantic typing. The image has not yet built in the current
restricted environment, so no passing semantic baseline is claimed. Resolve diagnostics
from a real LuaLS run before release; source annotations alone do not establish correctness.
