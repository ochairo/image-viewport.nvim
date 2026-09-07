local root = assert(vim.env.IMAGE_VIEWPORT_TEST_ROOT)
vim.opt.runtimepath:prepend(root)
local setups, installs, shutdowns = 0, 0, 0
package.loaded["image_viewport.processor"] = {
  install = function()
    installs = installs + 1
    return {}
  end,
  shutdown = function()
    shutdowns = shutdowns + 1
  end,
}
package.loaded["image"] = {
  setup = function()
    setups = setups + 1
    error("partial upstream mutation")
  end,
}
local viewport = require("image_viewport")
local ok, err = pcall(viewport.setup)
assert(not ok and tostring(err):find("partial upstream mutation", 1, true))
assert(setups == 1 and installs == 1 and shutdowns == 1)
ok, err = pcall(viewport.setup)
assert(not ok and tostring(err):find("restart Neovim", 1, true))
assert(setups == 1 and installs == 1, "failed initialization must not be retried")
vim.api.nvim_exec_autocmds("VimLeavePre", {})
assert(shutdowns == 1, "failed startup cleanup must have one owner")
print("image viewport partial initialization failure: ok")
