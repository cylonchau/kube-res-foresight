#!/bin/bash
# Stress test the scaling of a load service: raise the demand, watch the replicas follow, drop the demand, watch them come back.
# It only patches the ConfigMap of the service (the same as load.sh) and reads the Deployment: it
# never calls the platform, so it does not sign anyone out.
#
#   stress.sh [options]
#
#   --mode spike|ramp    spike (default): the demand jumps to --high at once, for an instant test.
#                        ramp: the demand climbs from --low to --high in --steps increases, one every --step-every seconds.
#   --low N              demand in millicores before and after the test                 (default 120)
#   --high N             demand in millicores at the top                                (default 700)
#   --hold S             seconds to watch at the high demand (after the last increase)  (default 120)
#   --recover S          seconds to watch after the demand is dropped back to --low;
#                        0 = do not drop it, stop at the end of the hold                (default 120)
#   --steps N            ramp only: number of increases from low to high                (default 5)
#   --step-every S       ramp only: seconds between two increases                       (default 30)
#   --baseline S         seconds to watch before the demand is touched                  (default 10)
#   --sample S           seconds between two samples of the replicas                    (default 10)
#   --name NAME          load service (default load-web)       --ns NAMESPACE (default forecast-test)
#   --dry-run            read and sample only: print what would be patched, change nothing
#
# Every number is a parameter, so a run is as short or as long as you ask: `stress.sh --hold 60 --recover 60` is a four
# minute spike test, `stress.sh --mode ramp --steps 4 --step-every 20 --hold 60 --recover 0` a three minute climb.
# The old positional form still works: stress.sh [high] [low] [minutes_per_phase] [name] [namespace].
#
# How fast the replicas follow is not set here: they follow the platform decision (refreshed by its scheduled task), then are
# limited by the maxStep and cooldown of the scaler. Each line of the log:
#   time  ready/desired-replicas
# At the end: the replicas before, the peak, the end, and how many seconds after the demand changed the first scale-up,
# the peak and the first scale-down appeared. Needed replicas = demand x headroom / 60m (100m request x 60% target).

# find kubectl even when it is only an alias or a function of the interactive shell (scripts do not inherit those):
# $KUBECTL, kubectl, kubectl.exe, then the usual Windows install paths seen from WSL
for c in "$KUBECTL" kubectl kubectl.exe "/mnt/d/Program Files/Docker/resources/bin/kubectl.exe" "/mnt/c/Program Files/Docker/Docker/resources/bin/kubectl.exe"; do
  # the full path: a bare name would call the kubectl() function below again and again
  [ -n "$c" ] && K=$(command -v "$c" 2> /dev/null) && [ -n "$K" ] && break
done
[ -n "$K" ] || { echo "kubectl not found: run 'type kubectl' in your shell and pass the path, e.g. KUBECTL=/path/to/kubectl $0 ..." >&2; exit 1; }
kubectl() { "$K" "$@"; }

MODE=spike; LOW=120; HIGH=700; HOLD=120; RECOVER=120; STEPS=5; STEP_EVERY=30; BASELINE=10; SAMPLE=10
NAME=load-web; NS=forecast-test; DRY=0

# the old form: stress.sh [high] [low] [minutes_per_phase] [name] [namespace]
if [[ -n "$1" && "$1" != -* ]]; then
  HIGH=${1:-$HIGH}; LOW=${2:-$LOW}
  [ -n "$3" ] && { HOLD=$(( $3 * 60 )); RECOVER=$(( $3 * 60 )); BASELINE=60; SAMPLE=30; MODE=spike; }
  NAME=${4:-$NAME}; NS=${5:-$NS}
else
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode) MODE=$2; shift 2 ;;
      --low) LOW=$2; shift 2 ;;
      --high) HIGH=$2; shift 2 ;;
      --hold) HOLD=$2; shift 2 ;;
      --recover) RECOVER=$2; shift 2 ;;
      --steps) STEPS=$2; shift 2 ;;
      --step-every) STEP_EVERY=$2; shift 2 ;;
      --baseline) BASELINE=$2; shift 2 ;;
      --sample) SAMPLE=$2; shift 2 ;;
      --name) NAME=$2; shift 2 ;;
      --ns) NS=$2; shift 2 ;;
      --dry-run) DRY=1; shift ;;
      -h|--help) sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
      *) echo "unknown option: $1 (see --help)" >&2; exit 1 ;;
    esac
  done
fi

for v in LOW HIGH HOLD RECOVER STEPS STEP_EVERY BASELINE SAMPLE; do
  [[ "${!v}" =~ ^[0-9]+$ ]] || { echo "$v must be a whole number, got '${!v}'" >&2; exit 1; }
done
[ "$SAMPLE" -ge 1 ] || { echo "--sample must be at least 1 second" >&2; exit 1; }
[ "$STEPS" -ge 1 ] || { echo "--steps must be at least 1" >&2; exit 1; }
[ "$MODE" = spike ] || [ "$MODE" = ramp ] || { echo "--mode must be spike or ramp" >&2; exit 1; }
[ "$HIGH" -gt "$LOW" ] || { echo "--high ($HIGH) must be above --low ($LOW)" >&2; exit 1; }

LOG=${LOG:-stress-$(date +%H%M%S).log}
START=$(date +%s)
now() { echo $(( $(date +%s) - START )); }

set_load() {
  if [ "$DRY" = 1 ]; then
    echo "$(date +%T) [dry-run] demand -> $1m (not patched)" | tee -a "$LOG"
  else
    kubectl -n "$NS" patch cm "$NAME-load" --type merge -p "{\"data\":{\"millicores\":\"$1\"}}" > /dev/null \
      || { echo "patch failed: demand NOT changed" >&2; exit 1; }
    echo "$(date +%T) demand -> $1m" | tee -a "$LOG"
  fi
}

BASE_R=; PEAK=0; PEAK_AT=; LAST=; UP_AT=; DOWN_AT=; CHANGED_AT=; PHASE_UP=1; LAST_PEAK=0
sample() {
  local desired ready
  desired=$(kubectl -n "$NS" get deploy "$NAME" -o jsonpath='{.spec.replicas}' 2> /dev/null)
  ready=$(kubectl -n "$NS" get deploy "$NAME" --no-headers 2> /dev/null | awk '{print $2}')
  echo "$(date +%T) $ready/$desired" | tee -a "$LOG"
  [[ "$desired" =~ ^[0-9]+$ ]] || return 0
  [ -z "$BASE_R" ] && BASE_R=$desired
  if [ "$desired" -gt "$PEAK" ]; then PEAK=$desired; PEAK_AT=$(now); fi
  if [ -n "$CHANGED_AT" ]; then
    if [ "$PHASE_UP" = 1 ] && [ -z "$UP_AT" ] && [ "$desired" -gt "$BASE_R" ]; then UP_AT=$(( $(now) - CHANGED_AT )); fi
    if [ "$PHASE_UP" = 0 ] && [ -z "$DOWN_AT" ] && [ -n "$LAST" ] && [ "$desired" -lt "$LAST_PEAK" ]; then DOWN_AT=$(( $(now) - CHANGED_AT )); fi
  fi
  LAST=$desired
}

phase() {  # phase <label> <seconds>
  echo "== $1 (${2}s) ==" | tee -a "$LOG"
  local end=$(( $(date +%s) + $2 ))
  while :; do
    sample
    [ "$(date +%s)" -ge "$end" ] && break
    local left=$(( end - $(date +%s) ))
    sleep $(( left < SAMPLE ? left : SAMPLE ))
  done
}

echo "log: $LOG   mode: $MODE   $NAME in $NS   low ${LOW}m  high ${HIGH}m  hold ${HOLD}s  recover ${RECOVER}s  sample ${SAMPLE}s$([ "$MODE" = ramp ] && echo "  steps $STEPS every ${STEP_EVERY}s")"
phase "baseline" "$BASELINE"

if [ "$MODE" = ramp ]; then
  CHANGED_AT=$(now)
  for i in $(seq 1 "$STEPS"); do
    set_load $(( LOW + (HIGH - LOW) * i / STEPS ))
    [ "$i" -lt "$STEPS" ] && phase "climbing: step $i of $STEPS" "$STEP_EVERY"
  done
else
  set_load "$HIGH"; CHANGED_AT=$(now)
fi
phase "high demand: the replicas should climb" "$HOLD"

LAST_PEAK=$PEAK
if [ "$RECOVER" -gt 0 ]; then
  PHASE_UP=0; set_load "$LOW"; CHANGED_AT=$(now)
  phase "low demand: the replicas should fall (limited by the scale-down step and cooldown)" "$RECOVER"
fi

{
  echo "== summary =="
  echo "replicas: before ${BASE_R:-?}   peak ${PEAK} (after ${PEAK_AT:-?}s)   at the end ${LAST:-?}"
  if [ -n "$UP_AT" ]; then echo "first scale-up: ${UP_AT}s after the demand rose"; else echo "first scale-up: not seen within ${HOLD}s"; fi
  if [ "$RECOVER" -gt 0 ]; then
    if [ -n "$DOWN_AT" ]; then echo "first scale-down: ${DOWN_AT}s after the demand dropped"; else echo "first scale-down: not seen within ${RECOVER}s"; fi
  fi
} | tee -a "$LOG"
[ "$DRY" = 1 ] || [ "$RECOVER" -gt 0 ] || echo "note: the demand was left at ${HIGH}m (--recover 0); set it back with load.sh $NAME $LOW" | tee -a "$LOG"
