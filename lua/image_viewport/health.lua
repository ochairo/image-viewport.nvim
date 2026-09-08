local M = {}

---@return nil
function M.check()
  vim.health.start("Image viewport")
  if vim.uv.os_uname().sysname ~= "Linux" then
    vim.health.error("The sandboxed image processor currently requires Linux")
  end
  if vim.fn.has("nvim-0.12") ~= 1 then
    vim.health.error("Neovim 0.12 or newer is required")
  end
  for _, executable in ipairs({ "/usr/bin/bwrap", "/usr/bin/magick-im7.q16", "/usr/bin/gs" }) do
    if vim.fn.executable(executable) == 1 then
      vim.health.ok(executable .. " is executable")
    else
      vim.health.error("Missing runtime dependency: " .. executable)
    end
  end
  if #vim.api.nvim_get_runtime_file("lua/image/init.lua", false) == 0 then
    vim.health.error("Install the image.nvim revision recorded in dependencies.json")
  end
  vim.health.info("This check does not execute the sandbox; setup runs its mandatory policy probe")
  vim.health.info("Load image_viewport.setup before image.nvim internals; terminal pixel dimensions are required")
end

return M
