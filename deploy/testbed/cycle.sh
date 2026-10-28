#!/bin/bash
# Run the platform pipeline for the test modules every INTERVAL seconds (default 60) instead of waiting for the 5 minute
# scheduled task: collect + predict + recommend, and every 5th round backtest and score. Needs the default admin login.
#   cycle.sh [module ids...]     e.g. cycle.sh 5     (default: 5; pass the ids of the load modules)
B=${PLATFORM:-http://127.0.0.1:20001}/api/v1
IDS=${*:-5}
n=0
while true; do
  TOKEN=$(curl -s -m 10 -X POST $B/auth/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"'${PASSWORD:-123456}'","force":true}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
  for id in $IDS; do
    call() { curl -s -m 120 -X POST "$B/modules/$id/$1" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' ${2:+-d "$2"} > /dev/null; }
    call run
    if [ $((n % 5)) -eq 0 ]; then call backtest '{"metric":"cpu","cutoffs":4}'; call score; fi
    call recommend
  done
  echo "$(date +%T) cycle $n done for: $IDS"
  n=$((n+1)); sleep ${INTERVAL:-60}
done
