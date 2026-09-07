local root = assert(vim.env.IMAGE_VIEWPORT_TEST_ROOT)
vim.opt.runtimepath:prepend(root)
local config = require("image_viewport.config")
local function rejects(options, message)
  local ok, err = pcall(config.resolve, options)
  assert(not ok and tostring(err):find(message, 1, true), tostring(err))
end
rejects(false, "opts must be a table")
rejects({ surprise = true }, "unknown option")
rejects({ image = false }, "image must be a table")
rejects({ mappings = "yes" }, "mappings must be a boolean")
rejects({ image = { processor = "magick_rock" } }, "processor must be magick_cli")
rejects({ image = { integrations = { markdown = { download_remote_images = true } } } }, "must remain disabled")
local first = config.resolve()
first.image.backend = "changed"
assert(config.resolve().image.backend == "kitty", "defaults must not be shared mutable state")

local installs, setups, shutdowns = 0, 0, 0
local ready = false
local image = {
  setup = function() setups = setups + 1 end,
  from_file = function() return nil end,
  from_url = function(_, _, callback) callback(nil) end,
}
package.loaded["image"] = image
package.loaded["image/utils"] = { term = { get_size = function()
  return ready and { cell_width = 8, cell_height = 16, screen_cols = 80, screen_rows = 24 } or nil
end } }
package.loaded["image_viewport.processor"] = {
  install = function() installs = installs + 1; return {} end,
  shutdown = function() shutdowns = shutdowns + 1 end,
}
local viewport = require("image_viewport")
local original_factory = image.from_file
local original_mapping = vim.fn.maparg("zh", "n")
local ok, reason = viewport.setup({ mappings = false })
assert(not ok and reason:find("dimensions", 1, true))
assert(image.from_file == original_factory and setups == 1)
ready = true
assert(viewport.setup({ mappings = false }))
assert(image.from_file ~= original_factory)
local wrapped_factory = image.from_file
local events = #vim.api.nvim_get_autocmds({ group = "ImageViewport" })
assert(viewport.setup({ mappings = false }))
assert(image.from_file == wrapped_factory and setups == 1 and installs == 2)
assert(#vim.api.nvim_get_autocmds({ group = "ImageViewport" }) == events)
assert(vim.fn.maparg("zh", "n") == original_mapping)
local changed, error_message = pcall(viewport.setup, { mappings = true })
assert(not changed and tostring(error_message):find("restart Neovim", 1, true))
vim.api.nvim_exec_autocmds("VimLeavePre", {})
assert(shutdowns == 1, "one owner must shut down the processor")
print("image viewport setup, option validation and lifecycle: ok")
