#!/bin/sh
set -eu
umask 077
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P)
fixture=$(mktemp -d "${TMPDIR:-/tmp}/go-supervise-test.XXXXXX")
wrapper=
cleanup() {
  if [ -n "$wrapper" ]; then kill -TERM "$wrapper" 2>/dev/null || :; wait "$wrapper" 2>/dev/null || :; fi
  rm -rf -- "$fixture"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
cat > "$fixture/wrapper" <<'WRAPPER'
#!/bin/sh
set -eu
temporary=$1
source=$2
shift 2
. "$source"
die() { exit 1; }
trap go_runtime_cleanup EXIT
trap 'exit 143' TERM
trap 'exit 129' HUP
go_runtime_run "$@"
WRAPPER
cat > "$fixture/command" <<'COMMAND'
#!/bin/sh
sleep 60 &
child=$!
printf '%s %s\n' "$$" "$child" > "$1"
wait "$child"
COMMAND
stopped() {
  [ ! -e "/proc/$1/stat" ] && return 0
  state=$(ps -o stat= -p "$1" 2>/dev/null) || return 0
  case "$state" in Z*|'') return 0 ;; *) return 1 ;; esac
}
# Both compilation and the built tool use this same bootstrap boundary.
for phase in compiler tool; do
  work=$fixture/$phase
  mkdir "$work"
  sh "$fixture/wrapper" "$work" "$root/scripts/go-supervise.sh" sh "$fixture/command" "$fixture/ready" &
  wrapper=$!
  tries=0
  while [ ! -s "$fixture/ready" ]; do
    tries=$((tries + 1)); [ "$tries" -lt 100 ] || exit 1
    sleep 0.05
  done
  read -r command child < "$fixture/ready"
  group=$(ps -o pgid= -p "$command" | tr -d ' ')
  kill -KILL "$wrapper"
  wait "$wrapper" 2>/dev/null || :
  wrapper=
  tries=0
  until stopped "$group" && stopped "$command" && stopped "$child"; do
    tries=$((tries + 1))
    if [ "$tries" -ge 100 ]; then
      kill -KILL "-$group" 2>/dev/null || :
      printf '%s\n' "$phase: orphaned process after wrapper death" >&2
      exit 1
    fi
    sleep 0.05
  done
  rm -f "$fixture/ready"
done
for status in 0 7; do
  work=$fixture/status-$status
  mkdir "$work"
  actual=0
  sh "$fixture/wrapper" "$work" "$root/scripts/go-supervise.sh" sh -c 'exit "$1"' fixture "$status" || actual=$?
  [ "$actual" -eq "$status" ]
  [ ! -e "$work" ]
done
printf '%s\n' 'bootstrap supervision: wrapper death, descendants, status and cleanup passed'
