#!/bin/bash
# Minimal autograder demo.
# Runs an (untrusted) "submission" against test cases, each in its own
# fresh, network-less, time-limited microVM, and prints AC / WA per case.
#
#   ./examples/autograder.sh
set -u
cd "$(dirname "$0")/.."

# The submission under test (untrusted): read two integers, print their sum.
SUBMISSION='read a b; echo $((a + b))'

# Test cases:  "<stdin input>|||<expected stdout>"
CASES=(
  "5 3|||8"
  "10 20|||30"
  "100 -1|||99"
)

pass=0
for tc in "${CASES[@]}"; do
  input="${tc%%|||*}"
  expected="${tc##*|||}"
  got=$(printf '%s\n' "$input" | ./microvm -timeout 2000 "$SUBMISSION" 2>/dev/null)
  if [ "$got" = "$expected" ]; then
    echo "[$input] -> AC"
    pass=$((pass + 1))
  else
    echo "[$input] -> WA (got '$got', want '$expected')"
  fi
done

echo "score: $pass/${#CASES[@]}"
