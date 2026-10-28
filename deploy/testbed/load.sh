#!/bin/bash
# Set the demand of a load service (millicores of CPU for the whole workload) to simulate pressure.
#   load.sh [name] low|mid|high|spike|<millicores>     name: load-web (default), load-api, load-batch or load-cache
# With a module capacity of 100m request x 60% target, the needed replicas are about demand x headroom / 60m.
NS=${NS:-forecast-test}
if [[ "$1" == load-* ]]; then NAME=$1; shift; else NAME=load-web; fi
case "$1" in
  low) m=120 ;; mid) m=300 ;; high) m=480 ;; spike) m=700 ;;
  ''|*[!0-9]*) echo "usage: $0 [load-web|load-api|load-batch|load-cache] low|mid|high|spike|<millicores>"; exit 1 ;;
  *) m=$1 ;;
esac
# find kubectl even when it is only an alias or a function of the interactive shell (scripts do not inherit those):
# $KUBECTL, kubectl, kubectl.exe, then the usual Windows install paths seen from WSL
for c in "$KUBECTL" kubectl kubectl.exe "/mnt/d/Program Files/Docker/resources/bin/kubectl.exe" "/mnt/c/Program Files/Docker/Docker/resources/bin/kubectl.exe"; do
  [ -n "$c" ] && command -v "$c" > /dev/null 2>&1 && { K=$c; break; }
done
[ -n "$K" ] || { echo "kubectl not found: run 'type kubectl' in your shell and pass the path, e.g. KUBECTL=/path/to/kubectl $0 ..." >&2; exit 1; }
"$K" -n "$NS" patch cm "$NAME-load" --type merge -p "{\"data\":{\"millicores\":\"$m\"}}" || { echo "patch failed: demand NOT changed" >&2; exit 1; }
echo "$NAME demand set to ${m}m; the pods pick it up within about a minute"
