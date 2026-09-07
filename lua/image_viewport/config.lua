local M = {}

---@type ImageViewport.Config
local defaults = {
  mappings = true,
  image = {
    backend = "kitty",
    processor = "magick_cli",
    integrations = {
      markdown = {
        enabled = true,
        clear_in_insert_mode = false,
        download_remote_images = false,
        only_render_image_at_cursor = true,
        only_render_image_at_cursor_mode = "popup",
        filetypes = { "markdown" },
      },
    },
    max_height_window_percentage = 100,
    max_width_window_percentage = 100,
    window_overlap_clear_enabled = true,
    editor_only_render_when_focused = true,
    hijack_file_patterns = {
      "*.png", "*.jpg", "*.jpeg", "*.webp", "*.gif", "*.bmp", "*.heic",
      "*.heif", "*.avif", "*.xpm", "*.ico", "*.svg", "*.xml", "*.pdf",
    },
  },
}

---@param opts? ImageViewport.Options
---@return ImageViewport.Config
function M.resolve(opts)
  if opts == nil then
    opts = {}
  end
  if type(opts) ~= "table" then
    error("image_viewport.setup: opts must be a table")
  end
  for key in pairs(opts) do
    if key ~= "image" and key ~= "mappings" then
      error("image_viewport.setup: unknown option " .. tostring(key))
    end
  end
  if opts.image ~= nil and type(opts.image) ~= "table" then
    error("image_viewport.setup: image must be a table")
  end
  if opts.mappings ~= nil and type(opts.mappings) ~= "boolean" then
    error("image_viewport.setup: mappings must be a boolean")
  end
  local resolved = vim.tbl_deep_extend("force", vim.deepcopy(defaults), opts)
  if resolved.image.processor ~= "magick_cli" then
    error("image_viewport.setup: processor must be magick_cli")
  end
  local integrations = resolved.image.integrations
  if type(integrations) ~= "table" or type(integrations.markdown) ~= "table" then
    error("image_viewport.setup: integrations and markdown must be tables")
  end
  if integrations.markdown.download_remote_images ~= false then
    error("image_viewport.setup: remote Markdown downloads must remain disabled")
  end
  return resolved
end

return M
