#!/bin/bash
# Demo script used to record docs/demo.gif (via asciinema + agg).
set -u
cd "$(dirname "$0")/.."

show() {
  printf '\033[1;32m$\033[0m \033[1m%s\033[0m\n' "$1"
  sleep 0.6
}

sleep 0.5
show './microvm "uname -a"'
./microvm "uname -a"
echo; sleep 0.8

show './microvm "echo to-stdout; echo to-stderr 1>&2; exit 7"'
./microvm "echo to-stdout; echo to-stderr 1>&2; exit 7"
echo; sleep 0.8

show './microvm "ip route | wc -l; echo isolated: no routes"'
./microvm "ip route 2>/dev/null | wc -l; echo 'isolated: no network routes'"
echo; sleep 1.2
