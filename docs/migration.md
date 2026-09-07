# Migrate the dotfiles consumer

After publishing a verified revision, replace the local `dotfiles-image-viewport` spec
with `ochairo/image-viewport.nvim` pinned to that commit. Keep the exact image.nvim pin
from dependencies.json and call `require("image_viewport").setup(...)` before image.nvim
internals load. Personal settings belong in that call, not in the plugin repository.

Remove the embedded Lua/runtime files only after the replacement is available. Move
processor behavior checks to this repository; keep package installation and plugin-pin
integration checks in dotfiles. Run both the dotfiles quality gate and native image tests
before applying configuration. Roll back the consumer revision if required.

Cache directory markers and the worker protocol deliberately retain their original
`dotfiles-image-processor` names for safe ownership recognition. They are internal storage
identities, not a dependency on dotfiles. Do not rename or adopt foreign cache directories.
