---@meta

---@class ImageViewport.MarkdownOptions
---@field enabled? boolean
---@field clear_in_insert_mode? boolean
---@field download_remote_images? false
---@field only_render_image_at_cursor? boolean
---@field only_render_image_at_cursor_mode? 'popup'|'inline'
---@field filetypes? string[]

---@class ImageViewport.ImageOptions
---@field backend? 'kitty'
---@field processor? 'magick_cli'
---@field integrations? {markdown?: ImageViewport.MarkdownOptions}
---@field max_height_window_percentage? number
---@field max_width_window_percentage? number
---@field window_overlap_clear_enabled? boolean
---@field editor_only_render_when_focused? boolean
---@field hijack_file_patterns? string[]

---@class ImageViewport.Options
---@field mappings? boolean
---@field image? ImageViewport.ImageOptions

---@class ImageViewport.Config
---@field mappings boolean
---@field image ImageViewport.ImageOptions

---@class ImageViewport.CellSize
---@field cell_width number
---@field cell_height number
---@field screen_cols? integer
---@field screen_rows? integer

---@class ImageViewport.Dimensions
---@field width integer
---@field height integer

---@class ImageViewport.TransformRequest
---@field target_width integer
---@field target_height integer
---@field crop_stage? 'before'|'after'|'none'
---@field crop? {x: integer, y: integer, width: integer, height: integer}
---@field effect? 'none'|'brightness'|'saturation'|'hue'
---@field effect_value? number

---@class ImageViewport.TransformResult
---@field ok boolean
---@field path? string
---@field error? string
---@field width? integer
---@field height? integer

---An empty identity token used only as a cache ownership key.
---@class ImageViewport.OwnerToken

---@class ImageViewport.Subscriber
---@field callback fun(result: ImageViewport.TransformResult): nil
---@field completed boolean
---@field job ImageViewport.Job

---@class ImageViewport.Job
---@field arguments string[]
---@field cancelled boolean
---@field completed boolean
---@field path string
---@field source string
---@field subscribers table<ImageViewport.Subscriber, boolean>
---@field process? vim.SystemObj

---@class ImageViewport.CacheEntry
---@field access integer
---@field bytes integer
---@field owners table<ImageViewport.OwnerToken, boolean>
---@field pending boolean
---@field source string

---@class ImageViewport.ProcessorState
---@field access integer
---@field bytes integer
---@field entries table<string, ImageViewport.CacheEntry>
---@field active_job? ImageViewport.Job
---@field formats table<string, {format: string, identity: string}>
---@field installed boolean
---@field jobs table<string, ImageViewport.Job>
---@field launcher? string[]
---@field queue ImageViewport.Job[]
---@field root? string
---@field shutting_down boolean
