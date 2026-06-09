#!/bin/bash
# Demo script used to record docs/demo.gif (via asciinema + agg).
set -u
cd "$(dirname "$0")/.."

show() { printf '\033[1;32m$\033[0m \033[1m%s\033[0m\n' "$1"; sleep 0.5; }

sleep 0.3
printf '\033[1;36m# agent-microvm-sandbox  —  judge / code-execution sandbox\033[0m\n\n'; sleep 0.6

# (1) feed test input via stdin (judge style); footer shows exit code + runtime
show 'echo "5 3" | ./microvm "read a b; echo $((a+b))"'
echo "5 3" | ./microvm 'read a b; echo $((a+b))'
echo; sleep 0.6

# (2) per-run time limit -> TLE
show './microvm -timeout 2000 "echo start; sleep 10"'
./microvm -timeout 2000 'echo start; sleep 10'
echo; sleep 0.6

# (3) untrusted code is isolated: read-only base image + no network
show './microvm "echo pwned > /etc/hacked 2>&1; routes=$(ip route | wc -l)"'
./microvm 'echo pwned > /etc/hacked 2>&1; echo routes=$(ip route 2>/dev/null | wc -l)'
echo; sleep 0.6

# (4) autograder: each test case runs in its own fresh, isolated microVM
show './examples/autograder.sh'
./examples/autograder.sh
echo; sleep 1.2
