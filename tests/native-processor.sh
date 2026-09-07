#!/bin/sh
set -eu

source_plugin=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/image-processor.XXXXXX") || exit 1
trap 'rm -rf -- "$test_root"' EXIT HUP INT TERM

fail() {
  printf 'Image processor test: FAIL: %s\n' "$*" >&2
  exit 1
}

if [ -f /run/.containerenv ]; then
  fail 'requires a configured native Linux guest, not the quality container'
fi

for dependency in /usr/bin/bwrap /usr/bin/gs /usr/bin/magick-im7.q16 /usr/bin/python3; do
  [ -x "$dependency" ] || fail "missing reviewed runtime dependency: $dependency"
done

mkdir -m 0700 "$test_root/cache" "$test_root/config" "$test_root/output" "$test_root/state"
mkdir -p "$test_root/relocated plugin"
cp -R "$source_plugin/lua" "$source_plugin/runtime" "$test_root/relocated plugin/"
find "$test_root/relocated plugin" -type d -exec chmod 0755 {} +
find "$test_root/relocated plugin" -type f -exec chmod 0644 {} +
runtime="$test_root/relocated plugin/runtime"

run_processor() {
  XDG_CONFIG_HOME=$test_root/config /usr/bin/python3 -I -S "$runtime/launch.py" "$@"
}

run_processor probe |
  grep -Fqx dotfiles-image-processor-probe-v1 || fail 'sandbox policy probe failed'

/usr/bin/magick-im7.q16 -size 16x8 'xc:#406080' "$test_root/source.png"
for extension in jpg webp gif bmp heic xpm ico avif; do
  /usr/bin/magick-im7.q16 "$test_root/source.png" "$test_root/source.$extension" ||
    fail "cannot create $extension fixture"
done
cat > "$test_root/source.svg" << 'EOF'
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="8">
  <rect width="16" height="8" fill="red"/>
</svg>
EOF
cat > "$test_root/source.xml" << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="8">
  <rect width="16" height="8" fill="red"/>
</svg>
EOF
cat > "$test_root/source.ps" << 'EOF'
%!PS-Adobe-3.0
<< /PageSize [16 8] >> setpagedevice
1 0 0 setrgbcolor
0 0 16 8 rectfill
showpage
EOF
/usr/bin/gs -q -dSAFER -dBATCH -dNOPAUSE -sDEVICE=pdfwrite \
  -sOutputFile="$test_root/source.pdf" "$test_root/source.ps" || fail 'cannot create PDF fixture'

for fixture in \
  png:png jpg:jpeg webp:webp gif:gif bmp:bmp heic:heic xpm:xpm ico:ico avif:avif \
  svg:svg xml:xml pdf:pdf; do
  extension=${fixture%%:*}
  format=${fixture#*:}
  [ "$(run_processor detect "$test_root/source.$extension")" = "$format" ] ||
    fail "$format sandboxed magic detection failed"
  dimensions=$(run_processor identify "$format" "$test_root/source.$extension") ||
    fail "$format identification failed"
  printf '%s\n' "$dimensions" | grep -Eq '^[1-9][0-9]* [1-9][0-9]*$' ||
    fail "$format dimensions were invalid"
  digest=$(printf '%s' "$fixture" | sha256sum)
  output=$test_root/output/${digest%% *}.png
  run_processor transform "$format" "$test_root/source.$extension" \
    "$output" none 32 16 0 0 0 0 none 0 png > /dev/null || fail "$format transform failed"
  [ "$(stat -c '%a' "$output")" = 600 ] || fail "$format output is not private"
  [ "$(/usr/bin/magick-im7.q16 identify -format '%m %wx%h' "$output")" = 'PNG 32x16' ] ||
    fail "$format output was not the expected PNG"
done

cat > "$test_root/hostile.svg" << 'EOF'
<?xml version="1.0"?>
<!DOCTYPE svg [<!ENTITY leak SYSTEM "file:///etc/hostname">]>
<svg xmlns="http://www.w3.org/2000/svg" width="16" height="8"><text>&leak;</text></svg>
EOF
if run_processor identify svg "$test_root/hostile.svg" > /dev/null 2>&1; then
  fail 'SVG entity declaration was accepted'
fi
if run_processor identify jpeg "$test_root/source.png" > /dev/null 2>&1; then
  fail 'mismatched image magic was accepted'
fi
mkfifo "$test_root/source.fifo"
fifo_status=0
timeout 2 env XDG_CONFIG_HOME=$test_root/config /usr/bin/python3 -I -S "$runtime/launch.py" \
  detect "$test_root/source.fifo" > /dev/null 2>&1 || fifo_status=$?
[ "$fifo_status" -ne 0 ] || fail 'special-file image source was accepted'
[ "$fifo_status" -ne 124 ] || fail 'special-file image source blocked before the sandbox boundary'

sed 's/domain="delegate" rights="none"/domain="delegate" rights="read | write"/' \
  "$runtime/policy.xml" > "$runtime/policy.xml.new"
mv "$runtime/policy.xml.new" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"
if run_processor probe > /dev/null 2>&1; then
  fail 'weakened ImageMagick delegate policy passed the startup probe'
fi
cp "$source_plugin/runtime/policy.xml" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"

sed 's/domain="module" rights="none"/domain="module" rights="read | write"/' \
  "$runtime/policy.xml" > "$runtime/policy.xml.new"
mv "$runtime/policy.xml.new" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"
if run_processor probe > /dev/null 2>&1; then
  fail 'weakened ImageMagick module deny policy passed the startup probe'
fi
cp "$source_plugin/runtime/policy.xml" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"

sed 's/ICON,SVG,MVG}/ICON,SVG,MVG,TXT}/' \
  "$runtime/policy.xml" > "$runtime/policy.xml.new"
mv "$runtime/policy.xml.new" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"
if run_processor probe > /dev/null 2>&1; then
  fail 'expanded ImageMagick module allowlist passed the startup probe'
fi
cp "$source_plugin/runtime/policy.xml" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"

sed 's/domain="coder" rights="none"/domain="coder" rights="read | write"/' \
  "$runtime/policy.xml" > "$runtime/policy.xml.new"
mv "$runtime/policy.xml.new" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"
if run_processor probe > /dev/null 2>&1; then
  fail 'weakened ImageMagick coder deny policy passed the startup probe'
fi
cp "$source_plugin/runtime/policy.xml" "$runtime/policy.xml"
chmod 0644 "$runtime/policy.xml"

for policy_domain in delegate filter module coder; do
  sed "s#</policymap>#  <policy domain=\"$policy_domain\" rights=\"read | write\" pattern=\"*\"/>\n</policymap>#" \
    "$runtime/policy.xml" > "$runtime/policy.xml.new"
  mv "$runtime/policy.xml.new" "$runtime/policy.xml"
  chmod 0644 "$runtime/policy.xml"
  if run_processor probe > /dev/null 2>&1; then
    fail "additional ImageMagick $policy_domain override passed the startup probe"
  fi
  cp "$source_plugin/runtime/policy.xml" "$runtime/policy.xml"
  chmod 0644 "$runtime/policy.xml"
done

victim=$test_root/victim
printf '%s\n' unchanged > "$victim"
link_digest=$(printf unsafe-output | sha256sum)
unsafe_output=$test_root/output/${link_digest%% *}.png
ln -s "$victim" "$unsafe_output"
if run_processor transform png "$test_root/source.png" \
  "$unsafe_output" none 32 16 0 0 0 0 none 0 png > /dev/null 2>&1; then
  fail 'unsafe existing output was replaced'
fi
[ "$(cat "$victim")" = unchanged ] || fail 'unsafe output target changed its symlink destination'

cancel_digest=$(printf cancelled-output | sha256sum)
cancel_output=$test_root/output/${cancel_digest%% *}.png
XDG_CONFIG_HOME=$test_root/config /usr/bin/python3 -I -S "$runtime/launch.py" \
  transform png "$test_root/source.png" "$cancel_output" none 4096 4096 0 0 0 0 none 0 png > /dev/null &
cancel_pid=$!
staging_seen=false
for _ in $(seq 1 100); do
  if find "$test_root/output" -maxdepth 1 -name ".${cancel_digest%% *}.png.*.new" | grep -q .; then
    staging_seen=true
    break
  fi
  sleep 0.01
done
[ "$staging_seen" = true ] || fail 'cancellation probe did not observe owned staging output'
kill -TERM "$cancel_pid"
cancel_status=0
wait "$cancel_pid" || cancel_status=$?
[ "$cancel_status" -ne 0 ] || fail 'cancelled sandbox transform reported success'
[ ! -e "$cancel_output" ] || fail 'cancelled sandbox transform published a final output'
if find "$test_root/output" -maxdepth 1 -name ".${cancel_digest%% *}.png.*.new" | grep -q .; then
  fail 'cancelled sandbox transform retained staging output'
fi

stale_root=$test_root/cache/nvim/dotfiles-image-processor/session-999999-deadbeef
mkdir -p "$stale_root"
chmod 0700 "$test_root/cache/nvim" "$test_root/cache/nvim/dotfiles-image-processor" "$stale_root"
printf '%s\n' dotfiles-image-processor-v1 999999 > "$stale_root/.owned"
printf '%s\n' stale > "$stale_root/.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png.999999.deadbeef.new"
chmod 0600 "$stale_root/.owned" "$stale_root/.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.png.999999.deadbeef.new"

cat > "$test_root/plugin-api.lua" << 'EOF'
local plugin_root = assert(vim.env.DOTFILES_IMAGE_PLUGIN_ROOT)
local source = assert(vim.env.DOTFILES_IMAGE_TEST_SOURCE)
local advisory = assert(vim.env.DOTFILES_IMAGE_TEST_ADVISORY)
package.path = plugin_root .. "/lua/?.lua;" .. plugin_root .. "/lua/?/init.lua;" .. package.path

local cleared = 0
package.preload["image/renderer"] = function()
  return { clear_cache_for_path = function() cleared = cleared + 1 end }
end

local module = require("image_viewport.processor")
local processor = assert(module.install())
local magic = require("image/utils/magic")
assert(magic.detect_format(source) == "png" and magic.is_image(source),
  "vendor magic seam did not use sandboxed detection")
assert(require("image/processors/magick_cli") == processor, "processor preload did not return the plugin adapter")
for _, method in ipairs({
  "brightness", "convert_to_png", "crop", "get_dimensions", "get_format",
  "hue", "resize", "saturation", "transform",
}) do
  assert(type(processor[method]) == "function", "processor method is missing: " .. method)
end
assert(processor.get_format(source) == "png", "processor format adapter changed the detected format")
local size = processor.get_dimensions(source)
assert(size.width == 16 and size.height == 8, "processor dimension adapter returned the wrong size")
local function sample(path)
  local result = vim.system({
    "/usr/bin/magick-im7.q16", path, "-format", "%[pixel:p{0,0}]", "info:",
  }, { text = true }):wait()
  assert(result.code == 0, "cannot sample managed image pixels")
  return result.stdout
end

local converted = processor.convert_to_png(source, advisory)
local resized = processor.resize(source, 8, 4, advisory)
local cropped = processor.crop(source, 0, 0, 8, 4, advisory)
local bright = processor.brightness(source, 120, advisory)
local saturated = processor.saturation(bright, 80, advisory)
local hue = processor.hue(saturated, 110, advisory)
for _, output in ipairs({ converted, resized, cropped, bright, saturated, hue }) do
  assert(output ~= advisory and vim.fn.filereadable(output) == 1, "managed output did not replace advisory output")
end
assert(sample(source) ~= sample(bright), "brightness did not change the source pixels")
assert(sample(bright) ~= sample(saturated), "saturation did not consume and change the brightness output")
assert(sample(saturated) ~= sample(hue), "hue did not consume and change the saturation output")
assert(vim.fn.filereadable(advisory) == 0, "vendor advisory output path was written")

local transformed
processor.transform(source, {
  output_format = "png",
  source_format = "png",
  target_height = 4,
  target_width = 8,
}, advisory, function(result)
  transformed = result
end)
assert(vim.wait(5000, function() return transformed ~= nil end, 10), "async adapter transform timed out")
assert(transformed.ok and transformed.path ~= advisory, "async adapter used the advisory output")

local cache_owner = {}
for path in pairs(module._state().entries) do
  module.pin(path, cache_owner)
end
local cache_index = 0
while vim.tbl_count(module._state().entries) < 32 do
  cache_index = cache_index + 1
  local output = processor.resize(source, 8, 4, advisory .. ".cache-" .. cache_index)
  module.pin(output, cache_owner)
end
local saturated_ok = pcall(processor.resize, source, 8, 4, advisory .. ".saturated")
assert(not saturated_ok, "a fully pinned global cache accepted an unowned frame beyond its limit")
assert(vim.tbl_count(module._state().entries) == 32, "global cache exceeded its file limit")

local real_system = vim.system
local pending
local starts = 0
vim.system = function(_, _, callback)
  starts = starts + 1
  pending = callback
  return {
    is_closing = function() return true end,
    kill = function() error("shared processor was killed before the final subscriber cancelled") end,
    wait = function() return { code = 143 } end,
  }
end
local first_calls = 0
local second_calls = 0
local request = { crop = { x = 0, y = 0, width = 8, height = 8 }, target_height = 8, target_width = 8 }
local first = module.viewport(source, request, function(result)
  first_calls = first_calls + 1
  assert(not result.ok, "cancelled first subscriber reported success")
end)
local second = module.viewport(source, request, function(result)
  second_calls = second_calls + 1
  assert(not result.ok, "cancelled second subscriber reported success")
end)
assert(starts == 1 and first.job == second.job, "identical viewport work was not shared")
module.cancel(first)
assert(first_calls == 1 and second_calls == 0, "first subscriber cancellation affected its peer")
local killed = 0
first.job.process.kill = function(_, signal)
  assert(signal == "sigterm", "final subscriber cancellation used the wrong first signal")
  killed = killed + 1
end
module.cancel(second)
assert(second_calls == 1 and killed == 1, "final subscriber did not cancel the shared process exactly once")
local cancelled_pending = pending
local retry_calls = 0
local retry = module.viewport(source, request, function(result)
  retry_calls = retry_calls + 1
  assert(not result.ok, "failed retry reported success")
end)
assert(retry and starts == 1 and retry.job ~= first.job, "retry joined the cancelled shared work")
cancelled_pending({ code = 143 })
assert(vim.wait(1000, function() return starts == 2 and cleared == 1 end, 10),
  "cancelled work did not reap before starting its successor")
assert(first_calls == 1 and second_calls == 1, "cancelled job completed a callback more than once")
pending({ code = 70 })
assert(vim.wait(1000, function() return retry_calls == 1 end, 10), "retried work did not complete")
vim.system = real_system
module.shutdown()
vim.cmd("qall!")
EOF

DOTFILES_IMAGE_PLUGIN_ROOT="$test_root/relocated plugin" \
  DOTFILES_IMAGE_TEST_SOURCE=$test_root/source.png \
  DOTFILES_IMAGE_TEST_ADVISORY=$test_root/advisory.png XDG_CONFIG_HOME=$test_root/config \
  XDG_CACHE_HOME=$test_root/cache XDG_STATE_HOME=$test_root/state \
  "${NVIM:-nvim}" --headless -u NONE -i NONE -l "$test_root/plugin-api.lua" ||
  fail 'local plugin processor API contract failed'
[ ! -e "$stale_root" ] || fail 'owned stale image processor session was not recovered'

printf '%s\n' 'Image processor test: PASS'
