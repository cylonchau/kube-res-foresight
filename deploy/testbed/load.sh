#!/bin/bash
# Set the demand of load-web (millicores of CPU for the whole workload) to simulate pressure.
#   load.sh low | mid | high | spike | <millicores>
# With the module's capacity of 100m request x 60% target, the needed replicas are about demand x headroom / 60m.
case "$1" in
  low) m=120 ;; mid) m=300 ;; high) m=480 ;; spike) m=700 ;;
  ''|*[!0-9]*) echo "usage: $0 low|mid|high|spike|<millicores>"; exit 1 ;;
  *) m=$1 ;;
esac
kubectl -n forecast-test patch cm load-web-load --type merge -p "{\"data\":{\"millicores\":\"$m\"}}"
echo "load-web demand set to ${m}m; the pods pick it up within about a minute"
