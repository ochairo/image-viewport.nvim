local M = {}
---@type ImageViewport.Config?
local initialized_options
local configured = false
local initialization_failed = false

---@param opts? ImageViewport.Options
---@return boolean ready
---@return string? reason
function M.setup(opts)
  if initialization_failed then
    error("image_viewport.setup: restart Neovim after failed upstream initialization")
  end
  local options = require("image_viewport.config").resolve(opts)
  if initialized_options and not vim.deep_equal(initialized_options, options) then
    error("image_viewport.setup: restart Neovim to change options after initialization")
  end
  if configured then
    return true
  end
  local processor_module = require("image_viewport.processor")
  local processor, processor_error = processor_module.install()
  if not processor then
    vim.notify("Images are disabled: " .. processor_error, vim.log.levels.WARN, { title = "Image viewport" })
    return false, processor_error
  end
  -- Clean up even if image.nvim loading or terminal-size discovery fails.
  local group = vim.api.nvim_create_augroup("ImageViewportStartup", { clear = true })
  vim.api.nvim_create_autocmd("VimLeavePre", {
    group = group,
    callback = function()
      if not configured then
        processor_module.shutdown()
      end
    end,
  })
  -- Requiring or setting up the dependency can mutate module state before throwing.
  -- A retry cannot roll those effects back; keep the failure terminal for this session.
  local ok, image, get_terminal_size = pcall(function()
    local upstream = require("image")
    if not initialized_options then
      upstream.setup(options.image)
      initialized_options = vim.deepcopy(options)
    end
    return upstream, require("image/utils").term.get_size
  end)
  if not ok then
    initialization_failed = true
    processor_module.shutdown()
    vim.api.nvim_clear_autocmds({ group = group })
    error("image_viewport.setup: upstream initialization failed; restart Neovim: " .. tostring(image))
  end
  configured = require("image_viewport.viewport").setup(image, get_terminal_size, {
    processor = processor_module,
    mappings = options.mappings,
  })
  if not configured then
    return false, "terminal cell dimensions are unavailable; retry setup after the UI attaches"
  end
  return true
end

return M
