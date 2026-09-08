#!/bin/sh
# Keep an anchor alive in its own session until the complete command group is stopped.
# This handles ordinary vendor descendants, not processes deliberately escaping sessions.
active_group=
go_runtime_stop_group() {
  [ -n "$active_group" ] || return 0
  kill -TERM "-$active_group" 2> /dev/null || :
  sleep "${go_runtime_grace:-0.2}"
  kill -KILL "-$active_group" 2> /dev/null || :
  wait "$active_group" 2> /dev/null || :
  active_group=
  exec 9>&-
}
go_runtime_cleanup() {
  trap '' HUP INT TERM
  go_runtime_stop_group
  rm -rf -- "$temporary"
}
go_runtime_run() {
  rm -f -- "$temporary/command-status"
  rm -f -- "$temporary/owner-lifetime"
  mkfifo -m 600 "$temporary/owner-lifetime"
  # Only the wrapper retains a writer. The anchor opens its reader before
  # dropping the inherited writer, so wrapper death cannot race FIFO admission.
  exec 9<> "$temporary/owner-lifetime"
  # This noninteractive shell starts background commands in its existing group;
  # setsid can therefore retain the child PID as the new session/group leader.
  setsid /bin/sh -c '
    status=$1; lifetime=$2; shift 2
    exec 8< "$lifetime"
    exec 9>&-
    group=$$
    (
      cat <&8 > /dev/null
      kill -KILL "-$group"
    ) &
    trap ":" HUP INT TERM
    "$@" 8<&-
    result=$?
    printf "%s\n" "$result" > "$status.tmp"
    mv "$status.tmp" "$status"
    while :; do sleep 60; done 2>/dev/null
  ' go-runtime "$temporary/command-status" "$temporary/owner-lifetime" "$@" &
  active_group=$!
  polls=0
  while [ ! -f "$temporary/command-status" ]; do
    kill -0 "$active_group" 2> /dev/null || die 'command supervisor exited unexpectedly'
    polls=$((polls + 1))
    [ "$polls" -lt 6000 ] || die 'runtime command exceeded its time limit'
    sleep 0.05
  done
  result=$(cat "$temporary/command-status")
  go_runtime_stop_group
  case $result in '' | *[!0-9]*) die 'invalid command exit status' ;; esac
  [ "$result" -le 255 ] || die 'invalid command exit status'
  return "$result"
}
