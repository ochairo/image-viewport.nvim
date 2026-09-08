local M = {}

local bit = require("bit")
local module_source = debug.getinfo(1, "S").source
local uv = vim.uv
local maximum_cached_bytes = 128 * 1024 * 1024
local maximum_cached_files = 32
local maximum_output_pixels = 16 * 1024 * 1024
local required_methods = {
  "brightness",
  "convert_to_png",
  "crop",
  "get_dimensions",
  "get_format",
  "hue",
  "resize",
  "saturation",
  "transform",
}

---@type ImageViewport.ProcessorState
local state = {
  access = 0,
  bytes = 0,
  entries = {},
  active_job = nil,
  formats = {},
  installed = false,
  jobs = {},
  launcher = nil,
  queue = {},
  root = nil,
  shutting_down = false,
}

local function private_directory(path)
  local metadata = uv.fs_lstat(path)
  return metadata and metadata.type == "directory" and metadata.uid == uv.getuid() and metadata.mode % 512 == 448
end

local function owned_regular(path)
  local metadata = uv.fs_lstat(path)
  return metadata
    and metadata.type == "file"
    and metadata.uid == uv.getuid()
    and metadata.mode % 512 == 384
    and metadata.nlink == 1
end

local function remove_owned(path)
  if owned_regular(path) then
    return uv.fs_unlink(path) ~= nil
  end
  return false
end

local function owned_session_entries(path, pid)
  if not private_directory(path) then
    return nil
  end
  local marker = vim.fs.joinpath(path, ".owned")
  if not owned_regular(marker) then
    return nil
  end
  local lines = vim.fn.readfile(marker)
  if #lines ~= 2 or lines[1] ~= "dotfiles-image-processor-v1" or lines[2] ~= tostring(pid) then
    return nil
  end
  local entries = {}
  local scanner = uv.fs_scandir(path)
  if not scanner then
    return nil
  end
  while true do
    local name = uv.fs_scandir_next(scanner)
    if not name then
      break
    end
    local final = name:match("^[0-9a-f]+%.png$")
    local staging = name:match("^%.[0-9a-f]+%.png%.%d+%.[0-9a-f]+%.new$")
    if name ~= ".owned" and not final and not staging then
      return nil
    end
    local candidate = vim.fs.joinpath(path, name)
    if not owned_regular(candidate) then
      return nil
    end
    entries[#entries + 1] = candidate
  end
  return entries
end

local function remove_owned_session(path, pid)
  local entries = owned_session_entries(path, pid)
  if not entries then
    return false
  end
  for _, entry in ipairs(entries) do
    if not remove_owned(entry) then
      return false
    end
  end
  return uv.fs_rmdir(path) ~= nil
end

local function clean_stale_sessions(parent)
  local scanner = uv.fs_scandir(parent)
  if not scanner then
    return
  end
  while true do
    local name, entry_type = uv.fs_scandir_next(scanner)
    if not name then
      break
    end
    local pid = tonumber(name:match("^session%-(%d+)%-[0-9a-f]+$"))
    if pid and entry_type == "directory" and pid ~= uv.os_getpid() and uv.kill(pid, 0) == nil then
      remove_owned_session(vim.fs.joinpath(parent, name), pid)
    end
  end
end

local function ensure_root()
  if state.root and private_directory(state.root) then
    return true
  end
  if state.shutting_down then
    return false
  end
  local cache = vim.fn.stdpath("cache")
  local cache_metadata = uv.fs_lstat(cache)
  if not cache_metadata and not uv.fs_mkdir(cache, 448) then
    return false
  end
  cache_metadata = uv.fs_lstat(cache)
  if not cache_metadata or cache_metadata.type ~= "directory" or cache_metadata.uid ~= uv.getuid() then
    return false
  end
  local parent = vim.fs.joinpath(cache, "dotfiles-image-processor")
  if not uv.fs_lstat(parent) and not uv.fs_mkdir(parent, 448) then
    return false
  end
  if not private_directory(parent) then
    return false
  end
  clean_stale_sessions(parent)
  local nonce = vim.fn.sha256(("%d:%d"):format(uv.os_getpid(), uv.hrtime())):sub(1, 16)
  local root = vim.fs.joinpath(parent, ("session-%d-%s"):format(uv.os_getpid(), nonce))
  if not uv.fs_mkdir(root, 448) or not private_directory(root) then
    return false
  end
  local marker = vim.fs.joinpath(root, ".owned")
  vim.fn.writefile({ "dotfiles-image-processor-v1", tostring(uv.os_getpid()) }, marker)
  uv.fs_chmod(marker, 384)
  if not owned_regular(marker) then
    uv.fs_rmdir(root)
    return false
  end
  state.root = root
  return true
end

---@param path string
---@return string? identity
---@return string? canonical
local function source_identity(path)
  local canonical = uv.fs_realpath(path)
  local metadata = canonical and uv.fs_stat(canonical) or nil
  if not metadata or metadata.type ~= "file" then
    return nil
  end
  local mtime = metadata.mtime or {}
  local ctime = metadata.ctime or {}
  return table.concat({
    canonical,
    metadata.dev or 0,
    metadata.ino or 0,
    metadata.size or 0,
    mtime.sec or 0,
    mtime.nsec or 0,
    ctime.sec or 0,
    ctime.nsec or 0,
  }, ":"),
    canonical
end

M.source_identity = source_identity

local allowed_formats = {
  avif = true,
  bmp = true,
  gif = true,
  heic = true,
  ico = true,
  jpeg = true,
  pdf = true,
  png = true,
  svg = true,
  webp = true,
  xml = true,
  xpm = true,
}

local function trusted_path(path, expected_type, executable, system_path)
  if type(path) ~= "string" or not vim.startswith(path, "/") then
    return false
  end
  local canonical = uv.fs_realpath(path)
  if canonical ~= path then
    return false
  end
  local uid = uv.getuid()
  local current = path
  local first = true
  while true do
    local metadata = uv.fs_lstat(current)
    if not metadata then
      return false
    end
    local sticky_directory = metadata.type == "directory" and bit.band(metadata.mode, 512) ~= 0
    if first then
      if
        metadata.type ~= expected_type
        or not system_path and metadata.uid ~= uid and metadata.uid ~= 0
        or executable and bit.band(metadata.mode, 64) == 0
      then
        return false
      end
      first = false
    elseif metadata.type ~= "directory" then
      return false
    end
    local writable = bit.band(metadata.mode, 18) ~= 0
    if writable and not sticky_directory then
      return false
    end
    if current == "/" then
      break
    end
    current = vim.fs.dirname(current)
  end
  return true
end

---@return string? runtime
local function trusted_runtime()
  if module_source:sub(1, 1) ~= "@" then
    return nil
  end
  local source = uv.fs_realpath(module_source:sub(2))
  if not source then
    return nil
  end
  local root = vim.fs.dirname(vim.fs.dirname(vim.fs.dirname(source)))
  local parent = vim.fs.joinpath(root, "runtime")
  local runtime = uv.fs_realpath(vim.fs.joinpath(parent, "current"))
  if not runtime or vim.fs.dirname(runtime) ~= parent then
    return nil
  end
  if not trusted_path(runtime, "directory", false) then
    return nil
  end
  for _, name in ipairs({ "image-launch", "image-worker", "policy.xml", "source.sha256", ".dotfiles-managed-version" }) do
    if not trusted_path(vim.fs.joinpath(runtime, name), "file", name == "image-launch" or name == "image-worker") then
      return nil
    end
  end
  return runtime
end

---@param ... string
---@return string[]
local function launcher_arguments(...)
  local arguments = vim.deepcopy(assert(state.launcher, "image processor is not initialized"))
  vim.list_extend(arguments, { ... })
  return arguments
end

---@param path string
---@return string? format
local function format_for(path)
  local identity, canonical = source_identity(path)
  if not identity or not canonical then
    return nil
  end
  local cached = state.formats[canonical]
  if cached and cached.identity == identity then
    return cached.format
  end
  local result = vim.system(launcher_arguments("detect", canonical), { text = true, timeout = 12000 }):wait(12000)
  local format = result.code == 0 and (result.stdout or ""):match("^([a-z]+)\n?$") or nil
  if not allowed_formats[format] then
    return nil
  end
  state.formats[canonical] = { format = format, identity = identity }
  return format
end

local safe_magic = {
  detect_format = format_for,
  is_image = function(path)
    return format_for(path) ~= nil
  end,
}

local function touch(path)
  local entry = state.entries[path]
  if entry then
    state.access = state.access + 1
    entry.access = state.access
  end
end

---@param entry ImageViewport.CacheEntry
---@return boolean
local function pinned(entry)
  return entry.pending or next(entry.owners) ~= nil
end

local function trim_cache()
  while vim.tbl_count(state.entries) > maximum_cached_files or state.bytes > maximum_cached_bytes do
    local oldest_path
    local oldest_access
    for path, entry in pairs(state.entries) do
      if not pinned(entry) and (not oldest_access or entry.access < oldest_access) then
        oldest_path = path
        oldest_access = entry.access
      end
    end
    if not oldest_path then
      return false
    end
    local entry = state.entries[oldest_path]
    if not remove_owned(oldest_path) then
      return false
    end
    state.bytes = math.max(0, state.bytes - entry.bytes)
    state.entries[oldest_path] = nil
  end
  return true
end

local function register_output(path, source)
  local metadata = uv.fs_lstat(path)
  if
    not metadata
    or metadata.type ~= "file"
    or metadata.uid ~= uv.getuid()
    or metadata.mode % 512 ~= 384
    or metadata.nlink ~= 1
    or metadata.size < 8
    or metadata.size > 32 * 1024 * 1024
  then
    return false
  end
  local current = state.entries[path]
  if current then
    state.bytes = state.bytes - current.bytes
  end
  state.access = state.access + 1
  state.entries[path] = {
    access = state.access,
    bytes = metadata.size,
    owners = current and current.owners or {},
    pending = true,
    source = source,
  }
  state.bytes = state.bytes + metadata.size
  if trim_cache() and state.entries[path] then
    return true
  end
  local entry = state.entries[path]
  if entry and next(entry.owners) == nil then
    entry.pending = false
    remove_owned(path)
    state.bytes = math.max(0, state.bytes - entry.bytes)
    state.entries[path] = nil
  end
  return false
end

local function output_path(key)
  if not ensure_root() then
    error("sandboxed image cache is unavailable")
  end
  return vim.fs.joinpath(state.root, vim.fn.sha256(key) .. ".png")
end

local function discard_empty_root()
  if state.root and private_directory(state.root) then
    remove_owned_session(state.root, uv.os_getpid())
  end
  state.root = nil
end

local function release_reservation(path)
  vim.schedule(function()
    local entry = state.entries[path]
    if entry then
      entry.pending = false
      trim_cache()
    end
  end)
end

---@param subscriber ImageViewport.Subscriber
---@param result ImageViewport.TransformResult
---@return nil
local function complete_subscriber(subscriber, result)
  if subscriber.completed then
    return
  end
  subscriber.completed = true
  subscriber.job.subscribers[subscriber] = nil
  subscriber.callback(result)
end

local start_next

---@param job ImageViewport.Job
---@param result {code: integer}
---@return nil
local function complete_job(job, result)
  if job.completed then
    return
  end
  job.completed = true
  if state.jobs[job.path] == job then
    state.jobs[job.path] = nil
  end
  if state.active_job == job then
    state.active_job = nil
  end
  local ok = not job.cancelled and result.code == 0 and register_output(job.path, job.source)
  local completion = ok and { ok = true, path = job.path }
    or {
      ok = false,
      error = job.cancelled and "sandboxed image transform was cancelled" or "sandboxed image transform failed",
    }
  for _, subscriber in ipairs(vim.tbl_keys(job.subscribers)) do
    complete_subscriber(subscriber, completion)
  end
  if ok then
    release_reservation(job.path)
  end
  if job.cancelled then
    local entry = state.entries[job.path]
    if entry then
      state.bytes = math.max(0, state.bytes - entry.bytes)
      state.entries[job.path] = nil
    end
    remove_owned(job.path)
    vim.schedule(function()
      require("image/renderer").clear_cache_for_path(job.source)
    end)
  end
  start_next()
end

---@param job ImageViewport.Job
---@return nil
local function terminate_job(job)
  if job.completed or job.cancelled then
    return
  end
  job.cancelled = true
  for _, subscriber in ipairs(vim.tbl_keys(job.subscribers)) do
    complete_subscriber(subscriber, { ok = false, error = "sandboxed image transform was cancelled" })
  end
  if not job.process then
    job.completed = true
    state.jobs[job.path] = nil
    vim.schedule(function()
      require("image/renderer").clear_cache_for_path(job.source)
    end)
    return
  end
  pcall(job.process.kill, job.process, "sigterm")
  vim.defer_fn(function()
    if not job.completed and not job.process:is_closing() then
      pcall(job.process.kill, job.process, "sigkill")
    end
  end, 2000)
end

start_next = function()
  if state.active_job or state.shutting_down then
    return
  end
  local job = table.remove(state.queue, 1)
  while job and (job.completed or job.cancelled) do
    job = table.remove(state.queue, 1)
  end
  if not job then
    return
  end
  state.active_job = job
  local ok, process = pcall(vim.system, job.arguments, {
    stderr = false,
    stdout = false,
    timeout = 12000,
  }, function(result)
    complete_job(job, result)
  end)
  if not ok then
    complete_job(job, { code = 70 })
    return
  end
  job.process = process
end

---@param path string
---@param target string
---@param request ImageViewport.TransformRequest
---@return string[]?
local function transform_arguments(path, target, request)
  local format = format_for(path)
  if not format then
    return nil
  end
  local width = math.floor(tonumber(request.target_width) or 0)
  local height = math.floor(tonumber(request.target_height) or 0)
  if width < 1 or height < 1 or width * height > maximum_output_pixels then
    return nil
  end
  local crop = request.crop
  local crop_stage = request.crop_stage or (crop and "after" or "none")
  crop = crop or { x = 0, y = 0, width = 0, height = 0 }
  return vim.list_extend(vim.deepcopy(assert(state.launcher, "image processor is not initialized")), {
    "transform",
    format,
    path,
    target,
    crop_stage,
    tostring(width),
    tostring(height),
    tostring(math.floor(crop.x or 0)),
    tostring(math.floor(crop.y or 0)),
    tostring(math.floor(crop.width or 0)),
    tostring(math.floor(crop.height or 0)),
    request.effect or "none",
    tostring(request.effect_value or 0),
    "png",
  })
end

---@param path string
---@param request ImageViewport.TransformRequest
---@param _advisory_output string?
---@param callback fun(result: ImageViewport.TransformResult): nil
---@return ImageViewport.Subscriber?
local function transform(path, request, _advisory_output, callback)
  local identity, canonical = source_identity(path)
  if not identity or not canonical then
    callback({ ok = false, error = "image source is unavailable" })
    return
  end
  local key = identity .. ":" .. vim.inspect(request)
  local target = output_path(key)
  local existing = state.jobs[target]
  if existing and not existing.cancelled then
    local subscriber = { callback = callback, completed = false, job = existing }
    existing.subscribers[subscriber] = true
    return subscriber
  end
  if owned_regular(target) and state.entries[target] then
    touch(target)
    callback({ ok = true, path = target })
    return nil
  end
  local arguments = transform_arguments(canonical, target, request)
  if not arguments then
    callback({ ok = false, error = "image transform request was rejected" })
    return
  end
  local job = {
    arguments = arguments,
    cancelled = false,
    completed = false,
    path = target,
    source = canonical,
    subscribers = {},
  }
  local subscriber = { callback = callback, completed = false, job = job }
  job.subscribers[subscriber] = true
  state.jobs[target] = job
  state.queue[#state.queue + 1] = job
  start_next()
  return subscriber
end

---@param path string
---@param request ImageViewport.TransformRequest
---@param advisory_output string?
---@return string
local function transform_sync(path, request, advisory_output)
  local identity, canonical = source_identity(path)
  if not identity or not canonical then
    error("image source is unavailable")
  end
  local target = output_path(identity .. ":sync:" .. tostring(advisory_output) .. ":" .. vim.inspect(request))
  if owned_regular(target) then
    touch(target)
    return target
  end
  local arguments = transform_arguments(canonical, target, request)
  if not arguments then
    error("image transform request was rejected")
  end
  local result = vim.system(arguments, { stderr = false, stdout = false, timeout = 12000 }):wait(12000)
  if result.code ~= 0 or not register_output(target, canonical) then
    error("sandboxed image transform failed")
  end
  release_reservation(target)
  return target
end

---@class ImageViewport.Processor
local processor = {}

---@param path string
---@return string
function processor.get_format(path)
  return format_for(path) or error("image format is unsupported")
end

---@param path string
---@return ImageViewport.Dimensions
function processor.get_dimensions(path)
  local format = processor.get_format(path)
  local result = vim.system(launcher_arguments("identify", format, path), { text = true, timeout = 12000 }):wait(12000)
  if result.code ~= 0 then
    error("sandboxed image dimension probe failed")
  end
  local width, height = (result.stdout or ""):match("^(%d+) (%d+)\n?$")
  width, height = tonumber(width), tonumber(height)
  if not width or not height or width * height > maximum_output_pixels then
    error("image dimensions exceed the reviewed limit")
  end
  return { width = width, height = height }
end

---@param path string
---@param request ImageViewport.TransformRequest
---@param output string?
---@param callback fun(result: ImageViewport.TransformResult): nil
---@return nil
function processor.transform(path, request, output, callback)
  transform(path, request, output, callback)
end

---@param path string
---@param output? string
---@return string
function processor.convert_to_png(path, output)
  local size = processor.get_dimensions(path)
  return transform_sync(path, { target_width = size.width, target_height = size.height }, output)
end

---@param path string
---@param width integer
---@param height integer
---@param output? string
---@return string
function processor.resize(path, width, height, output)
  return transform_sync(path, { target_width = width, target_height = height }, output)
end

---@param path string
---@param x integer
---@param y integer
---@param width integer
---@param height integer
---@param output? string
---@return string
function processor.crop(path, x, y, width, height, output)
  return transform_sync(path, {
    crop = { x = x, y = y, width = width, height = height },
    crop_stage = "before",
    target_width = width,
    target_height = height,
  }, output)
end

local function effect(path, name, value, output)
  local size = processor.get_dimensions(path)
  return transform_sync(path, {
    effect = name,
    effect_value = value,
    target_width = size.width,
    target_height = size.height,
  }, output)
end

---@param path string
---@param value number
---@param output? string
---@return string
function processor.brightness(path, value, output)
  return effect(path, "brightness", value, output)
end

---@param path string
---@param value number
---@param output? string
---@return string
function processor.saturation(path, value, output)
  return effect(path, "saturation", value, output)
end

---@param path string
---@param value number
---@param output? string
---@return string
function processor.hue(path, value, output)
  return effect(path, "hue", value, output)
end

---@param options? {}
---@return ImageViewport.Processor? processor
---@return string? error
function M.install(options)
  if state.installed then
    return processor
  end
  if options and next(options) ~= nil then
    return nil, "image processor runtime overrides are not permitted"
  end
  local runtime = trusted_runtime()
  if not runtime then
    return nil, "image viewport runtime is unavailable; run make build in the plugin checkout"
  end
  state.launcher = { vim.fs.joinpath(runtime, "image-launch") }
  if
    package.loaded["image/processors/magick_cli"]
    or package.loaded["image/utils"]
    or package.loaded["image/utils/magic"]
  then
    return nil, "image.nvim internals loaded before the viewport plugin"
  end
  if not ensure_root() then
    return nil, "private image cache is unavailable"
  end
  local probe = vim.system(launcher_arguments("probe"), { text = true, timeout = 12000 }):wait(12000)
  if probe.code ~= 0 or probe.stdout ~= "dotfiles-image-processor-probe-v1\n" then
    discard_empty_root()
    return nil, "sandboxed image processor probe failed"
  end
  for _, method in ipairs(required_methods) do
    assert(type(processor[method]) == "function", "missing image processor method: " .. method)
  end
  -- image.nvim does not yet expose a public custom-processor registration API.
  -- Keep this pin-specific preload seam inside the plugin and replace it when
  -- upstream offers one; loading after image.nvim would silently bypass it.
  package.preload["image/processors/magick_cli"] = function()
    return processor
  end
  package.preload["image/utils/magic"] = function()
    return safe_magic
  end
  state.installed = true
  return processor
end

---@param path string
---@param request ImageViewport.TransformRequest
---@param callback fun(result: ImageViewport.TransformResult): nil
---@return ImageViewport.Subscriber? subscriber
function M.viewport(path, request, callback)
  return transform(path, vim.tbl_extend("force", request, { crop_stage = "before" }), nil, callback)
end

---@param subscriber? ImageViewport.Subscriber
---@return nil
function M.cancel(subscriber)
  if subscriber and not subscriber.completed then
    local job = subscriber.job
    complete_subscriber(subscriber, { ok = false, error = "sandboxed image transform was cancelled" })
    if not job.completed and next(job.subscribers) == nil then
      terminate_job(job)
    end
  end
end

---@param path string
---@return nil
function M.cancel_for_source(path)
  local canonical = uv.fs_realpath(path) or path
  for _, job in pairs(state.jobs) do
    if job.source == canonical then
      terminate_job(job)
    end
  end
end

---@param path string
---@param owner ImageViewport.OwnerToken
---@return nil
function M.pin(path, owner)
  local entry = state.entries[path]
  if entry then
    entry.owners[owner] = true
    touch(path)
  end
end

---@param owner ImageViewport.OwnerToken
---@return nil
function M.unpin(owner)
  for _, entry in pairs(state.entries) do
    entry.owners[owner] = nil
  end
  trim_cache()
end

---@param path string
---@return nil
function M.invalidate_source(path)
  local canonical = uv.fs_realpath(path) or path
  M.cancel_for_source(canonical)
  for output, entry in pairs(state.entries) do
    if entry.source == canonical and not pinned(entry) then
      remove_owned(output)
      state.bytes = math.max(0, state.bytes - entry.bytes)
      state.entries[output] = nil
    end
  end
  require("image/renderer").clear_cache_for_path(canonical)
end

---@return nil
function M.shutdown()
  state.shutting_down = true
  local jobs = vim.tbl_values(state.jobs)
  if state.active_job and not vim.tbl_contains(jobs, state.active_job) then
    jobs[#jobs + 1] = state.active_job
  end
  for _, job in ipairs(jobs) do
    terminate_job(job)
  end
  for _, job in ipairs(jobs) do
    if job.process then
      pcall(job.process.wait, job.process, 2500)
    end
  end
  state.active_job = nil
  state.queue = {}
  for path, entry in pairs(state.entries) do
    remove_owned(path)
    state.bytes = math.max(0, state.bytes - entry.bytes)
    state.entries[path] = nil
  end
  discard_empty_root()
end

---@return ImageViewport.ProcessorState
function M._state()
  return state
end

return M
