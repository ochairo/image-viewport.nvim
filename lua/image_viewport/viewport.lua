local M = {}

local maximum_cached_frames = 8
local maximum_controllers = 128
local maximum_output_pixels = 16 * 1024 * 1024
local image_window_percentage = 100

local function positive_round(value)
  return math.max(1, math.floor(value + 0.5))
end

local function zero_round(value)
  return math.max(0, math.floor(value + 0.5))
end

local function valid_cell_size(size)
  return size
    and type(size.cell_width) == "number"
    and size.cell_width > 0
    and type(size.cell_height) == "number"
    and size.cell_height > 0
end

local function direct_cell_zoom(initial, previous, current)
  local function changed(value, baseline)
    return math.abs(value / baseline - 1) >= 0.01
  end
  local current_changed = changed(current.cell_width, initial.cell_width)
    or changed(current.cell_height, initial.cell_height)
  local previous_changed = changed(previous.cell_width, initial.cell_width)
    or changed(previous.cell_height, initial.cell_height)
  if not current_changed and not previous_changed then
    return nil
  end
  return math.sqrt((current.cell_width / initial.cell_width) * (current.cell_height / initial.cell_height))
end

local function proportional_grid_step(previous, current)
  if
    type(previous.screen_cols) ~= "number"
    or previous.screen_cols <= 0
    or type(previous.screen_rows) ~= "number"
    or previous.screen_rows <= 0
    or type(current.screen_cols) ~= "number"
    or current.screen_cols <= 0
    or type(current.screen_rows) ~= "number"
    or current.screen_rows <= 0
  then
    return nil
  end
  local width_step = previous.screen_cols / current.screen_cols
  local height_step = previous.screen_rows / current.screen_rows
  local width_direction = width_step - 1
  local height_direction = height_step - 1
  if math.abs(width_direction) < 0.01 or math.abs(height_direction) < 0.01 then
    return nil
  end
  if width_direction * height_direction <= 0 then
    return nil
  end
  if math.abs(width_step - height_step) / math.max(width_step, height_step) > 0.15 then
    return nil
  end
  return math.sqrt(width_step * height_step)
end

local function viewport_spec(controller, state)
  local window = controller.render_window
  if not window or not vim.api.nvim_win_is_valid(window) then
    return nil
  end
  local terminal = state.get_terminal_size()
  local window_size = state.get_window_size(window)
  if not valid_cell_size(terminal) or not window_size or window_size.width < 1 or window_size.height < 1 then
    return nil
  end

  local width_limit = window_size.width * controller.base_width_percentage / 100 * state.zoom
  local height_limit = window_size.height * controller.base_height_percentage / 100 * state.zoom
  local scale = math.min(1, width_limit / controller.base_width, height_limit / controller.base_height)
  local logical_width = positive_round(controller.base_width * scale)
  local logical_height = positive_round(controller.base_height * scale)
  local visible_width = math.min(window_size.width, logical_width)
  local maximum_pan = math.max(0, logical_width - visible_width)
  local window_state = state.windows[controller.control_window] or { pan = 0 }
  state.windows[controller.control_window] = window_state
  local pan = math.max(0, math.min(window_state.pan, maximum_pan))

  local source_width = controller.source.image_width
  local source_height = controller.source.image_height
  local crop_width = math.max(1, math.min(source_width, positive_round(source_width * visible_width / logical_width)))
  local crop_x = 0
  if maximum_pan > 0 then
    crop_x = zero_round((source_width - crop_width) * pan / maximum_pan)
  end
  crop_x = math.max(0, math.min(crop_x, source_width - crop_width))

  return {
    crop_width = crop_width,
    crop_x = crop_x,
    logical_height = logical_height,
    logical_width = logical_width,
    maximum_pan = maximum_pan,
    output_height = positive_round(logical_height * terminal.cell_height),
    output_width = positive_round(visible_width * terminal.cell_width),
    overflow_height = logical_height > window_size.height,
    overflow_width = logical_width > window_size.width,
    pan = pan,
    source_height = source_height,
    visible_width = visible_width,
  }
end

M.viewport_spec = viewport_spec

local function notify_once(state, key, message)
  if state.notifications[key] then
    return
  end
  state.notifications[key] = true
  vim.notify(message, vim.log.levels.WARN, { title = "Images" })
end

local function controller_owned(controller, state)
  return not controller.disposed
    and state.controllers[controller.id] == controller
    and state.owners[controller.id] == controller.source
end

local function raw_clear(controller, instance, shallow)
  if not instance then
    return
  end
  local clear = instance == controller.source and controller.source_clear or controller.derived_clear
  if clear then
    pcall(clear, instance, shallow)
  end
end

local function unpin_controller(controller, state)
  state.processor.unpin(controller.token)
end

local function pin_active(controller, state)
  unpin_controller(controller, state)
  local active = controller.active
  if not active then
    return
  end
  for _, path in ipairs({ active.path, active.resized_path, active.cropped_path }) do
    if path then
      state.processor.pin(path, controller.token)
    end
  end
end

local function deactivate_derived(controller, state)
  if not controller.derived or controller.active ~= controller.derived then
    return
  end
  raw_clear(controller, controller.derived, false)
  controller.active = controller.source
  controller.active_key = nil
  unpin_controller(controller, state)
end

local function visible_source_owner(state, source_path)
  local wanted = vim.uv.fs_realpath(source_path) or source_path
  for _, candidate in pairs(state.controllers) do
    if candidate.requested_visible then
      local candidate_path = candidate.source.path or candidate.source.original_path
      if (vim.uv.fs_realpath(candidate_path) or candidate_path) == wanted then
        return true
      end
    end
  end
  return false
end

local function release_source_jobs(controller, state)
  local source_path = controller.source.path or controller.source.original_path
  if source_path and not visible_source_owner(state, source_path) then
    state.processor.cancel_for_source(source_path)
  end
end

local function dispose_controller(controller, state, options)
  if not controller_owned(controller, state) then
    return
  end
  options = options or {}
  controller.disposed = true
  controller.generation = controller.generation + 1
  controller.requested_visible = false
  if state.active_transform and state.active_transform.controller == controller then
    state.processor.cancel(state.active_transform.job)
  end
  controller.pending = nil
  controller.queued = false
  if not options.already_cleared then
    raw_clear(controller, controller.active or controller.source, false)
  end
  unpin_controller(controller, state)
  if state.controllers[controller.id] == controller then
    state.controllers[controller.id] = nil
  end
  release_source_jobs(controller, state)
end

local function controller_clear(controller, state, shallow)
  if not controller_owned(controller, state) then
    return
  end
  if controller.in_source_render then
    return raw_clear(controller, controller.source, shallow)
  end
  controller.generation = controller.generation + 1
  if state.active_transform and state.active_transform.controller == controller then
    state.processor.cancel(state.active_transform.job)
  end
  controller.pending = nil
  raw_clear(controller, controller.active or controller.source, shallow)
  if shallow then
    return
  end
  -- image.nvim uses a full clear to retire document images. Remove the whole
  -- controller so long sessions do not retain one strong reference per image;
  -- the source proxy can bootstrap a fresh controller if its handle is reused.
  dispose_controller(controller, state, { already_cleared = true })
end

local function call_source_render(controller, state, geometry)
  controller.in_source_render = true
  local ok, result = pcall(controller.source_render, controller.source, geometry)
  controller.in_source_render = false
  if not ok then
    error(result)
  end
  controller.active = controller.source
  pin_active(controller, state)
  return result
end

local function recalculate_base(controller, state)
  local natural_width = controller.source.image_width / state.initial_size.cell_width
  local natural_height = controller.source.image_height / state.initial_size.cell_height
  if controller.explicit_width and controller.explicit_height then
    controller.base_width = controller.explicit_width
    controller.base_height = controller.explicit_height
  elseif controller.explicit_width then
    controller.base_width = controller.explicit_width
    controller.base_height = natural_height * controller.explicit_width / natural_width
  elseif controller.explicit_height then
    controller.base_height = controller.explicit_height
    controller.base_width = natural_width * controller.explicit_height / natural_height
  else
    controller.base_width = natural_width
    controller.base_height = natural_height
  end
end

local function refresh_source(controller, state)
  local signature = state.processor.source_identity(controller.source.original_path)
  if not signature then
    return false
  end
  if controller.source_signature == signature then
    return true
  end
  controller.generation = controller.generation + 1
  if state.active_transform and state.active_transform.controller == controller then
    state.processor.cancel(state.active_transform.job)
  end
  controller.pending = nil
  deactivate_derived(controller, state)
  for _ = 1, 2 do
    state.processor.invalidate_source(controller.source.original_path)
    controller.source.last_modified = -1
    local before = state.processor.source_identity(controller.source.original_path)
    if not before then
      break
    end
    call_source_render(controller, state, controller.source.geometry)
    local after = state.processor.source_identity(controller.source.original_path)
    if before == after then
      controller.source_signature = after
      recalculate_base(controller, state)
      controller.cache = {}
      controller.cache_order = {}
      controller.active_key = nil
      return true
    end
  end
  controller.source_signature = nil
  return false
end

local function activate_source(controller, state, spec)
  if not controller_owned(controller, state) or not spec then
    return
  end
  deactivate_derived(controller, state)
  controller.source.ignore_global_max_size = true
  controller.last_spec = spec
  call_source_render(controller, state, {
    height = spec.logical_height,
    width = spec.logical_width,
  })
end

local function mark_mru(controller, key)
  for index = #controller.cache_order, 1, -1 do
    if controller.cache_order[index] == key then
      table.remove(controller.cache_order, index)
    end
  end
  controller.cache_order[#controller.cache_order + 1] = key
  while #controller.cache_order > maximum_cached_frames do
    local eviction_index
    for index, candidate in ipairs(controller.cache_order) do
      if candidate ~= controller.active_key then
        eviction_index = index
        break
      end
    end
    if not eviction_index then
      break
    end
    local evicted = table.remove(controller.cache_order, eviction_index)
    controller.cache[evicted] = nil
  end
end

local function activate_derived(controller, state, spec, key, path)
  if not controller_owned(controller, state) or not controller.requested_visible then
    return
  end
  raw_clear(controller, controller.active or controller.source, false)
  unpin_controller(controller, state)
  local source = controller.source
  local derived = controller.derived
  if not derived then
    -- Keep the source's public ID stable while an overflow frame is active. A
    -- dedicated vendor instance avoids image.nvim's existing-ID fast path and
    -- is reused for every frame instead of growing its image registry.
    derived = state.from_file(path, {
      buffer = controller.render_buffer,
      height = spec.logical_height,
      inline = source.inline or controller.force_virtual_padding,
      namespace = source.namespace,
      overlap = source.overlap,
      render_offset_top = source.render_offset_top,
      width = spec.visible_width,
      window = controller.render_window,
      with_virtual_padding = source.with_virtual_padding or controller.force_virtual_padding,
      x = source.geometry.x,
      y = source.geometry.y,
    })
    if not derived or derived == source then
      activate_source(controller, state, spec)
      return
    end
    controller.derived = derived
    controller.derived_clear = derived.clear
    controller.derived_render = derived.render
    derived.clear = function(_, shallow)
      return controller_clear(controller, state, shallow)
    end
    derived.render = function(_, geometry)
      if not controller_owned(controller, state) then
        return
      end
      if geometry then
        return controller.source:render(geometry)
      end
      -- image.nvim's scroll lifecycle moves the registry-visible derived
      -- instance, then calls render() without geometry. Carry that placement
      -- back to the logical source so the next viewport frame stays anchored.
      controller.source.geometry.x = derived.geometry.x
      controller.source.geometry.y = derived.geometry.y
      controller.requested_visible = true
      return controller.derived_render(derived)
    end
  end
  derived.id = controller.id
  derived.buffer = controller.render_buffer
  derived.cropped_path = path
  derived.geometry = {
    height = spec.logical_height,
    width = spec.visible_width,
    x = source.geometry.x,
    y = source.geometry.y,
  }
  derived.image_height = spec.output_height
  derived.image_width = spec.output_width
  derived.inline = source.inline or controller.force_virtual_padding
  derived.last_modified = vim.fn.getftime(path)
  derived.namespace = source.namespace
  derived.original_path = path
  derived.overlap = source.overlap
  derived.path = path
  derived.pending_transform_key = nil
  derived.render_offset_top = source.render_offset_top
  derived.resized_path = path
  derived.source_format = "png"
  derived.transform_key = nil
  derived.transform_signature = nil
  derived.window = controller.render_window
  derived.with_virtual_padding = source.with_virtual_padding or controller.force_virtual_padding
  derived.ignore_global_max_size = true
  controller.active = derived
  controller.active_key = key
  controller.last_spec = spec
  controller.cache[key] = path
  mark_mru(controller, key)
  state.processor.pin(path, controller.token)
  controller.derived_render(derived, {
    height = spec.logical_height,
    width = spec.visible_width,
  })
end

local dispatch

dispatch = function(state)
  if state.active_transform then
    return
  end
  local controller = table.remove(state.queue, 1)
  while controller and (not controller_owned(controller, state) or not controller.pending) do
    controller.queued = false
    controller = table.remove(state.queue, 1)
  end
  if not controller then
    return
  end
  controller.queued = false
  local request = controller.pending
  controller.pending = nil
  local active = { controller = controller, request = request }
  state.active_transform = active
  active.job = state.processor.viewport(controller.source.path or controller.source.original_path, {
    crop = {
      x = request.spec.crop_x,
      y = 0,
      width = request.spec.crop_width,
      height = request.spec.source_height,
    },
    target_height = request.spec.output_height,
    target_width = request.spec.output_width,
  }, function(result)
    vim.schedule(function()
      if state.active_transform == active then
        state.active_transform = nil
      end
      local valid = controller_owned(controller, state)
        and request.generation == controller.generation
        and controller.requested_visible
      if valid and result.ok then
        controller.cache[request.key] = result.path
        activate_derived(controller, state, request.spec, request.key, result.path)
      elseif valid then
        notify_once(state, "transform", "Image zoom reached a safe rendering limit; keeping the last valid view")
        if not controller.active or not controller.active.is_rendered then
          activate_source(controller, state, request.fit_spec)
        end
      end
      dispatch(state)
    end)
  end)
end

local function queue_transform(controller, state, spec, fit_spec, generation)
  if spec.output_width * spec.output_height > maximum_output_pixels then
    notify_once(state, "pixels", "Image zoom reached a safe rendering limit; keeping the last valid view")
    if not controller.active or not controller.active.is_rendered then
      activate_source(controller, state, fit_spec)
    end
    return
  end
  local signature = state.processor.source_identity(controller.source.path or controller.source.original_path)
  if not signature then
    notify_once(state, "source", "Image source is unavailable; keeping the last valid view")
    return
  end
  local key = vim.fn.sha256(table.concat({
    signature,
    spec.crop_x,
    spec.crop_width,
    spec.output_width,
    spec.output_height,
  }, ":"))
  local cached = controller.cache[key]
  if cached and vim.fn.filereadable(cached) == 1 then
    mark_mru(controller, key)
    activate_derived(controller, state, spec, key, cached)
    return
  end
  controller.pending = {
    fit_spec = fit_spec,
    generation = generation,
    key = key,
    spec = spec,
  }
  if not controller.queued then
    controller.queued = true
    state.queue[#state.queue + 1] = controller
  end
  dispatch(state)
end

local function fit_spec(controller, state)
  local zoom = state.zoom
  state.zoom = 1
  local spec = viewport_spec(controller, state)
  state.zoom = zoom
  return spec
end

local function render_controller(controller, state, geometry)
  if not controller_owned(controller, state) then
    return
  end
  controller.generation = controller.generation + 1
  if state.active_transform and state.active_transform.controller == controller then
    state.processor.cancel(state.active_transform.job)
  end
  controller.pending = nil
  if geometry then
    if not controller.seen_external_geometry then
      if geometry.width and geometry.width > 0 then
        controller.explicit_width = geometry.width
      end
      if geometry.height and geometry.height > 0 then
        controller.explicit_height = geometry.height
      end
      controller.seen_external_geometry = true
      recalculate_base(controller, state)
    end
    controller.source.geometry = vim.tbl_deep_extend("force", controller.source.geometry, geometry)
  end
  controller.render_window = controller.source.window
  controller.render_buffer = controller.source.buffer
  controller.force_virtual_padding = controller.render_buffer
    and vim.api.nvim_buf_is_valid(controller.render_buffer)
    and vim.bo[controller.render_buffer].filetype == "image_nvim"
  controller.requested_visible = true
  if not refresh_source(controller, state) then
    notify_once(state, "source", "Image source is unavailable; keeping the last valid view")
    return
  end
  local generation = controller.generation
  if not controller.render_window or not controller.control_window then
    return call_source_render(controller, state, geometry)
  end
  local spec = viewport_spec(controller, state)
  if not spec then
    return
  end
  local baseline = fit_spec(controller, state)
  if not spec.overflow_width and not (controller.force_virtual_padding and spec.overflow_height) then
    activate_source(controller, state, spec)
    return
  end
  queue_transform(controller, state, spec, baseline, generation)
end

local function create_controller(instance, state, options, logical_id)
  if vim.tbl_count(state.controllers) >= maximum_controllers then
    notify_once(state, "controllers", "Image viewport limit reached; keeping the standard image view")
    return nil
  end
  local controller = {
    active = instance,
    base_height_percentage = instance.max_height_window_percentage or image_window_percentage,
    base_width_percentage = instance.max_width_window_percentage or image_window_percentage,
    cache = {},
    cache_order = {},
    control_buffer = options and options.buffer or instance.buffer,
    control_window = options and options.window or instance.window,
    disposed = false,
    explicit_height = instance._dotfiles_explicit_height,
    explicit_width = instance._dotfiles_explicit_width,
    generation = 0,
    id = logical_id or instance.id,
    in_source_render = false,
    render_buffer = instance.buffer,
    render_window = instance.window,
    requested_visible = false,
    seen_external_geometry = false,
    source = instance,
    source_clear = instance._dotfiles_original_clear,
    source_render = instance._dotfiles_original_render,
    source_signature = state.processor.source_identity(instance.original_path),
    token = {},
  }
  recalculate_base(controller, state)
  instance.geometry.width = positive_round(controller.base_width)
  instance.geometry.height = positive_round(controller.base_height)
  state.controllers[controller.id] = controller
  return controller
end

local function install_proxy(instance, state, options, logical_id)
  if not instance then
    return nil
  end
  logical_id = logical_id or instance._dotfiles_viewport_id or instance.id
  local current_owner = state.owners[logical_id]
  if current_owner and current_owner ~= instance then
    local previous = state.controllers[logical_id]
    if previous then
      dispose_controller(previous, state)
    end
  end
  state.owners[logical_id] = instance
  if not instance._dotfiles_viewport_proxy then
    instance._dotfiles_original_clear = instance.clear
    instance._dotfiles_original_render = instance.render
    instance._dotfiles_explicit_width = instance.geometry and instance.geometry.width or nil
    instance._dotfiles_explicit_height = instance.geometry and instance.geometry.height or nil
    instance._dotfiles_viewport_id = logical_id
    instance._dotfiles_viewport_proxy = true
    instance.render = function(self, geometry)
      if state.owners[self._dotfiles_viewport_id] ~= self then
        return
      end
      local controller = state.controllers[self._dotfiles_viewport_id]
        or create_controller(self, state, nil, self._dotfiles_viewport_id)
      if not controller then
        return self._dotfiles_original_render(self, geometry)
      end
      return render_controller(controller, state, geometry)
    end
    instance.clear = function(self, shallow)
      if state.owners[self._dotfiles_viewport_id] ~= self then
        return
      end
      local controller = state.controllers[self._dotfiles_viewport_id]
      if controller then
        return controller_clear(controller, state, shallow)
      end
      return self._dotfiles_original_clear(self, shallow)
    end
    instance._dotfiles_dispose = function()
      local controller = state.controllers[instance._dotfiles_viewport_id]
      if controller and controller.source == instance then
        dispose_controller(controller, state)
      end
      if state.owners[instance._dotfiles_viewport_id] == instance then
        state.owners[instance._dotfiles_viewport_id] = nil
      end
    end
  end
  local controller = state.controllers[logical_id]
  if not controller then
    create_controller(instance, state, options, logical_id)
  elseif options then
    controller.control_window = options.window or controller.control_window
    controller.control_buffer = options.buffer or controller.control_buffer
    controller.source.window = options.window or controller.source.window
    controller.source.buffer = options.buffer or controller.source.buffer
  end
  return instance
end

local function controllers_for_window(state, window)
  local controllers = {}
  local maximum_pan = 0
  for _, controller in pairs(state.controllers) do
    if controller.control_window == window and controller.requested_visible then
      local spec = viewport_spec(controller, state)
      if spec and spec.maximum_pan > 0 then
        controllers[#controllers + 1] = controller
        maximum_pan = math.max(maximum_pan, spec.maximum_pan)
      end
    end
  end
  return controllers, maximum_pan
end

local function clamp_pan_states(state)
  for window, window_state in pairs(state.windows) do
    local _, maximum_pan = controllers_for_window(state, window)
    window_state.pan = math.max(0, math.min(window_state.pan, maximum_pan))
  end
end

local function pan_window(state, direction, amount)
  local window = vim.api.nvim_get_current_win()
  local controllers, maximum_pan = controllers_for_window(state, window)
  if #controllers == 0 then
    return false
  end
  local window_state = state.windows[window] or { pan = 0 }
  state.windows[window] = window_state
  window_state.pan = math.max(0, math.min(maximum_pan, window_state.pan + direction * amount))
  for _, controller in ipairs(controllers) do
    render_controller(controller, state, nil)
  end
  return true
end

local function setup_mappings(state)
  local mappings = {
    {
      "zh",
      -1,
      function()
        return vim.v.count1
      end,
    },
    {
      "zl",
      1,
      function()
        return vim.v.count1
      end,
    },
    {
      "zH",
      -1,
      function()
        return math.max(1, math.floor(vim.api.nvim_win_get_width(0) / 2)) * vim.v.count1
      end,
    },
    {
      "zL",
      1,
      function()
        return math.max(1, math.floor(vim.api.nvim_win_get_width(0) / 2)) * vim.v.count1
      end,
    },
  }
  for _, mapping in ipairs(mappings) do
    vim.keymap.set("n", mapping[1], function()
      if not pan_window(state, mapping[2], mapping[3]()) then
        vim.cmd(("normal! %d%s"):format(vim.v.count1, mapping[1]))
      end
    end, { desc = "Pan an overflowing image or scroll normally", silent = true })
  end
  vim.keymap.set("n", "<ScrollWheelLeft>", function()
    if not pan_window(state, -1, 3) then
      vim.api.nvim_feedkeys(vim.keycode("<ScrollWheelLeft>"), "n", false)
    end
  end, { desc = "Pan an overflowing image left", silent = true })
  vim.keymap.set("n", "<ScrollWheelRight>", function()
    if not pan_window(state, 1, 3) then
      vim.api.nvim_feedkeys(vim.keycode("<ScrollWheelRight>"), "n", false)
    end
  end, { desc = "Pan an overflowing image right", silent = true })
end

function M.setup(image, get_terminal_size, options)
  if image._image_viewport_configured then
    return true
  end
  local initial_size = get_terminal_size()
  if not valid_cell_size(initial_size) then
    return false
  end
  options = options or {}
  local state = {
    active_transform = nil,
    controllers = {},
    from_file = image.from_file,
    get_terminal_size = get_terminal_size,
    get_window_size = options.get_window_size or function(window)
      return { height = vim.api.nvim_win_get_height(window), width = vim.api.nvim_win_get_width(window) }
    end,
    initial_size = initial_size,
    notifications = {},
    owners = setmetatable({}, { __mode = "v" }),
    previous_size = initial_size,
    processor = assert(options.processor, "image viewport requires its plugin processor"),
    queue = {},
    windows = {},
    zoom = 1,
  }

  local from_file = image.from_file
  image.from_file = function(path, factory_options)
    local id = factory_options and factory_options.id
    local existing = id and state.controllers[id]
    local owner = id and state.owners[id]
    if owner and (not existing or owner == existing.source) then
      local requested = vim.uv.fs_realpath(path) or vim.fn.fnamemodify(path, ":p")
      local current = vim.uv.fs_realpath(owner.original_path) or owner.original_path
      if requested == current then
        return install_proxy(owner, state, factory_options, id)
      end
    end
    local vendor_options = factory_options and vim.deepcopy(factory_options) or {}
    if id then
      vendor_options.id = nil
    end
    local instance = from_file(path, vendor_options)
    if id then
      if instance then
        instance.id = id
      end
    end
    return install_proxy(instance, state, factory_options, id)
  end
  local from_url = image.from_url
  image.from_url = function(url, factory_options, callback)
    local id = factory_options and factory_options.id
    local vendor_options = factory_options and vim.deepcopy(factory_options) or {}
    if id then
      vendor_options.id = nil
    end
    return from_url(url, vendor_options, function(instance)
      if id then
        if instance then
          instance.id = id
        end
      end
      callback(install_proxy(instance, state, factory_options, id))
    end)
  end

  image._image_viewport_configured = true
  image._image_viewport_state = state
  state.pan_window = function(direction, amount)
    return pan_window(state, direction, amount)
  end
  if options.mappings ~= false then
    setup_mappings(state)
  end

  local group = vim.api.nvim_create_augroup("ImageViewport", { clear = true })
  local resize_scheduled = false
  vim.api.nvim_create_autocmd("VimResized", {
    callback = function()
      if resize_scheduled then
        return
      end
      resize_scheduled = true
      vim.schedule(function()
        resize_scheduled = false
        local current_size = get_terminal_size()
        if valid_cell_size(current_size) then
          local direct_zoom = direct_cell_zoom(initial_size, state.previous_size, current_size)
          if direct_zoom then
            state.zoom = math.max(0.01, direct_zoom)
          else
            local grid_step = proportional_grid_step(state.previous_size, current_size)
            if grid_step then
              state.zoom = math.max(0.01, state.zoom * grid_step)
            end
          end
          state.previous_size = current_size
        end
        clamp_pan_states(state)
        for _, controller in pairs(state.controllers) do
          if controller.requested_visible then
            render_controller(controller, state, nil)
          end
        end
      end)
    end,
    desc = "Scale and viewport images with terminal font zoom",
    group = group,
  })
  vim.api.nvim_create_autocmd("WinClosed", {
    callback = function(event)
      local window = tonumber(event.match)
      for _, controller in ipairs(vim.tbl_values(state.controllers)) do
        if window and controller.control_window == window then
          dispose_controller(controller, state)
        end
      end
      state.windows[window] = nil
    end,
    desc = "Retire image viewports for closed windows",
    group = group,
  })
  vim.api.nvim_create_autocmd({ "BufDelete", "BufWipeout" }, {
    callback = function(event)
      for _, controller in ipairs(vim.tbl_values(state.controllers)) do
        if controller.control_buffer == event.buf then
          dispose_controller(controller, state)
        end
      end
    end,
    desc = "Retire image viewports for deleted buffers",
    group = group,
  })
  vim.api.nvim_create_autocmd("VimLeavePre", {
    callback = function()
      for _, controller in ipairs(vim.tbl_values(state.controllers)) do
        dispose_controller(controller, state)
      end
      state.processor.shutdown()
    end,
    desc = "Stop sandboxed image processing and remove private outputs",
    group = group,
  })
  return true
end

return M
