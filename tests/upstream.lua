local root = assert(vim.env.IMAGE_VIEWPORT_TEST_ROOT)
local upstream = assert(vim.env.IMAGE_VIEWPORT_UPSTREAM)
vim.opt.runtimepath:prepend(upstream)
vim.opt.runtimepath:prepend(root)

-- Synthetic effects exercise real pinned upstream module loading and setup.
-- They do not replace the native processor/backend acceptance checks.
local processor = {}
for _, name in ipairs({ "brightness", "convert_to_png", "crop", "get_dimensions", "get_format", "hue", "resize", "saturation", "transform" }) do
  processor[name] = function()
    error("unexpected image processing during adapter setup: " .. name)
  end
end
package.preload["image/processors/magick_cli"] = function()
  return processor
end
local magic = {
  detect_format = function() return "png" end,
  is_image = function() return true end,
}
package.preload["image/utils/magic"] = function()
  return magic
end
package.preload["image/backends/kitty"] = function()
  return {
    setup = function() end,
    clear = function() end,
    render = function() error("unexpected terminal render during adapter setup") end,
  }
end
local image = require("image")
for _, method in ipairs({ "setup", "from_file", "from_url" }) do
  assert(type(image[method]) == "function", "upstream API missing: " .. method)
end
assert(require("image/utils").magic == magic, "upstream bypassed the magic preload seam")
assert(require("image/processors/magick_cli") == processor)
image.setup({
  backend = "kitty",
  processor = "magick_cli",
  integrations = {
    markdown = { enabled = false },
    asciidoc = { enabled = false },
    typst = { enabled = false },
    neorg = { enabled = false },
    syslang = { enabled = false },
    html = { enabled = false },
    css = { enabled = false },
    org = { enabled = false },
  },
  hijack_file_patterns = {},
})
local original = image.from_file
assert(require("image_viewport.viewport").setup(image, function()
  return { cell_width = 8, cell_height = 16, screen_cols = 80, screen_rows = 24 }
end, { mappings = false, processor = { shutdown = function() end } }))
assert(image.from_file ~= original and image._image_viewport_configured)
vim.api.nvim_exec_autocmds("VimLeavePre", {})
print("locked image.nvim adapter loading/setup: ok (synthetic effects)")
