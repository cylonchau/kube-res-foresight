#!/bin/bash
# Stress test the scaling of load-web: raise the demand, watch the replicas follow, drop the demand, watch them come back.
# It only patches the ConfigMap of load-web (the same as load.sh) and reads the Deployment and the PredictiveScaler: it
# never calls the platform, so it does not sign anyone out.
#
#   stress.sh [high_millicores=700] [low_millicores=120] [minutes_per_phase=12] [name=load-web] [namespace=forecast-test]
#
# The replicas follow the platform decision, which is refreshed by the platform's scheduled task (about every 5 minutes),
# then limited by the maxStep and cooldown of the scaler: expect a staircase, not a jump. Each line of the log:
#   time  ready/desired-replicas  scaler desired / decision replicas / decision version
# Needed replicas = demand x headroom / 60m (100m request x 60% target), capped by maxReplicas.
HIGH=${1:-700}; LOW=${2:-120}; MIN=${3:-12}; NAME=${4:-load-web}; NS=${5:-forecast-test}
LOG=${LOG:-stress-$(date +%H%M%S).log}

set_load() { kubectl -n "$NS" patch cm "$NAME-load" --type merge -p "{\"data\":{\"millicores\":\"$1\"}}" > /dev/null; echo "$(date +%T) demand -> $1m" | tee -a "$LOG"; }
sample() {
  ready=$(kubectl -n "$NS" get deploy "$NAME" --no-headers | awk '{print $2}')
  scaler=$(kubectl -n "$NS" get predictivescaler "$NAME" -o jsonpath='{.status.desiredReplicas} {.status.lastDecision.replicas} v{.status.lastDecision.version}' 2>/dev/null)
  echo "$(date +%T) $ready $scaler" | tee -a "$LOG"
}
phase() {  # phase <label> <minutes>
  echo "== $1 ==" | tee -a "$LOG"
  for _ in $(seq 1 $(( $2 * 2 ))); do sample; sleep 30; done
}

echo "log: $LOG"
sample
phase "baseline" 1
set_load "$HIGH"; phase "high demand: replicas should climb" "$MIN"
set_load "$LOW";  phase "low demand: replicas should fall (one at a time, 2 min apart)" "$MIN"
echo "done; peak and final replicas are in $LOG"
