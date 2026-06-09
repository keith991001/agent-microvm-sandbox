#!/bin/bash
# Demo script used to record docs/demo.gif (via asciinema + agg).
set -u
cd "$(dirname "$0")/.."
pkill -f 'microvm -serve' 2>/dev/null

show() { printf '\033[1;32m$\033[0m \033[1m%s\033[0m\n' "$1"; sleep 0.5; }

sleep 0.3
printf '\033[1;36m# agent-microvm-sandbox  —  1 command = 1 microVM\033[0m\n\n'; sleep 0.6

# (1) a real Linux microVM — and you can see the latency
show 'time ./microvm "uname -a"'
time ./microvm "uname -a"
echo; sleep 0.6

# (2) ephemeral: write a file, gone on the next (brand-new) VM
show './microvm "echo hi > /tmp/x && cat /tmp/x"'
./microvm "echo hi > /tmp/x && cat /tmp/x"
show './microvm "cat /tmp/x  # was written last run"'
./microvm "cat /tmp/x 2>/dev/null || echo '(gone: each run is a brand-new VM)'"
echo; sleep 0.6

# (3) isolation: read-only base image + no network
show './microvm "echo x > /etc/test ; routes=$(ip route|wc -l)"'
./microvm "echo x > /etc/test 2>&1; echo routes=\$(ip route 2>/dev/null | wc -l)"
echo; sleep 0.6

# (4) warm pool over HTTP: tens of milliseconds
show './microvm -serve -pool 2 &   # warming up...'
./microvm -serve -pool 2 -addr :8080 >/tmp/svc.log 2>&1 &
SVC=$!
sleep 14                                   # let the pool warm (compressed in the GIF)
curl -s -m 5 :8080/run -d '{"cmd":"true"}' >/dev/null 2>&1   # ensure a warm hit
show 'time curl -s :8080/run -d {"cmd":"echo warm-pool hit"}'
time curl -s :8080/run -d '{"cmd":"echo warm-pool hit"}'; echo
sleep 1.0
kill "$SVC" 2>/dev/null
