# image-viewport.nvim

Viewport-aware image scaling and horizontal panning for `3rd/image.nvim`, with a
sandboxed image processor. This is an extension; it does not replace image.nvim.

## Requirements and limits

- Neovim **0.12+** on Linux, with terminal pixel/cell dimensions and a Kitty graphics backend.
- `3rd/image.nvim` at the exact revision in `dependencies.json`.
- `/usr/bin/python3`, `/usr/bin/bwrap`, `/usr/bin/magick-im7.q16`, and `/usr/bin/gs`.
- Bubblewrap user namespaces, sealed memfd support, `/etc/ld.so.cache`, `/etc/fonts`,
  and `/var/cache/fontconfig`. The required ImageMagick codecs/policy must pass the probe.

These are concrete Linux runtime requirements inherited from the original implementation.
macOS, native Windows, and alternate executable layouts are not supported by this extraction.
Startup fails closed if the sandbox or policy is unavailable; there is no unsandboxed fallback.
The inherited image.nvim pin has not been revalidated in this extraction environment.

## Install

With lazy.nvim:

```lua
{
  "ochairo/image-viewport.nvim",
  event = { "BufReadPre", "BufNewFile" },
  dependencies = {
    { "3rd/image.nvim", commit = "5c6f29a5069e1f7bd5773ce5907454063b0f125d" },
  },
  config = function()
    require("image_viewport").setup({})
  end,
}
```

Do not separately call `require("image").setup()` or load its internals first. This
plugin must install its processor before image.nvim loads the processor/magic modules.
It owns image.nvim setup. Run `:checkhealth image_viewport` for dependency availability;
setup performs the actual sandbox probe.

## Configure

```lua
require("image_viewport").setup({
  mappings = true, -- horizontal scrolling keys pan overflowing images
  image = {
    backend = "kitty",
    max_height_window_percentage = 100,
    max_width_window_percentage = 100,
    integrations = { markdown = { only_render_image_at_cursor = true } },
  },
})
```

`image` overrides image.nvim defaults owned by this extension. The processor must remain
`magick_cli` and remote Markdown downloads must remain disabled. Default hijack formats
include common raster images, SVG/XML, and PDF. Set `mappings = false` to preserve all
your existing horizontal-scroll mappings. With mappings enabled, standard scrolling is
used when there is no overflowing viewport.

`setup()` returns `true` on success or `false, reason` for processor/terminal readiness.
Repeating successful setup with the same options is harmless. If cell dimensions are not
yet available, repeat setup once the UI is attached. Changing initialized options requires
a Neovim restart. Invalid options raise an error. A failed upstream initialization is terminal for that
session: processing is shut down and subsequent setup calls require a restart. Window/buffer removal retires viewports;
editor exit cancels processing and cleans private outputs.

## Development

Use the repository-owned [pinned development environment](docs/development.md).
CI builds the same image and runs the complete portable gate offline.


```sh
make static       # Python boundary tests and source checks
make test         # isolated Neovim setup/configuration tests
make native-test  # actual sandbox, formats, hostile inputs, cache and cancellation
make upstream-test # locked image.nvim loader/setup seam, with synthetic effects
make check        # static, Neovim, formatting, lint, LuaLS and upstream seam
```

Use Python 3.11+, Make, StyLua, Luacheck and LuaLS; set `NVIM` to select Neovim.
Native tests require the Linux dependencies above and are separate from portable checks.
They test a relocated checkout with spaces in its path, outside the Neovim config tree.

[Architecture](docs/architecture.md) · [Migration](docs/migration.md)

## Provenance and release status

Original revision and file hashes are in `docs/extraction-source.json`. Existing private
cache markers and worker protocol remain stable. The plugin is licensed under [MIT](LICENSE); dependencies retain their own licenses.
Runtime integration and LuaLS verification are required before the first release.

[Typing contracts](docs/typing.md) · [Verification status](docs/verification.md)
