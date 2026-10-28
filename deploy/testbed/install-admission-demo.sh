#!/bin/bash
# One command that sets up the admission demo ("no module, no deployment") end to end. Run it yourself: it creates a
# credential on the platform and writes a Secret and two policies to the cluster.
#
#   install-admission-demo.sh [platform=http://127.0.0.1:20001] [password=123456] [platform_url_in_cluster=http://host.docker.internal:20001]
#
# What it does, in order (each step prints what it did; it stops at the first failure):
#   1. platform: a developer credential "kyverno" whose only permission is "Admission Recommendations (read)"
#      (an existing one is kept and rotated), and the system setting forecast.allow_admission_mode = true
#   2. cluster: the Secret kyverno/kube-resource-fcst-admission ("<access key>:<access secret>"), the RBAC that lets Kyverno read it,
#      and the two ClusterPolicies kube-resource-fcst-requests and kube-resource-fcst-require-module
#   3. removes the old ClusterPolicy kubeforecast-resources (the previous name of the requests policy) so two policies do not
#      change the same Pods
#   4. checks: the demo Deployment is REFUSED, then a module with its name is created on the platform, and it is ADMITTED
# The credential file is written to target/admission.cred (git-ignored); the secret is never printed.
set -e
PLATFORM=${1:-http://127.0.0.1:20001}; PASSWORD=${2:-123456}; IN_CLUSTER=${3:-http://host.docker.internal:20001}
HERE=$(cd "$(dirname "$0")" && pwd); ROOT=$(cd "$HERE/../.." && pwd)
for c in "$KUBECTL" kubectl kubectl.exe "/mnt/d/Program Files/Docker/resources/bin/kubectl.exe"; do
  [ -n "$c" ] && K=$(command -v "$c" 2> /dev/null) && [ -n "$K" ] && break
done
[ -n "$K" ] || { echo "kubectl not found: KUBECTL=/path/to/kubectl $0" >&2; exit 1; }
api() { python3 - "$@" << 'PY'
import json, sys, urllib.request
method, path, token = sys.argv[1], sys.argv[2], sys.argv[3]
body = json.loads(sys.argv[4]) if len(sys.argv) > 4 and sys.argv[4] else None
req = urllib.request.Request(sys.argv[5] + '/api/v1' + path if len(sys.argv) > 5 else '', method=method,
    data=json.dumps(body).encode() if body is not None else None,
    headers={'Content-Type': 'application/json', **({'Authorization': 'Bearer ' + token} if token else {})})
print(urllib.request.urlopen(req, timeout=30).read().decode())
PY
}
TOKEN=$(api POST /auth/login "" "{\"username\":\"admin\",\"password\":\"$PASSWORD\",\"force\":true}" "$PLATFORM" | python3 -c "import sys,json;print(json.load(sys.stdin)['data']['token'])")
echo "1. platform: signed in"

PERM=$(api GET /secret-keys/permission-options "$TOKEN" "" "$PLATFORM" | python3 -c "
import sys,json
print(next(x['id'] for x in json.load(sys.stdin)['data'] if x['code']=='forecast:admission:read'))")
EXISTING=$(api GET "/secret-keys?page=1&page_size=100" "$TOKEN" "" "$PLATFORM" | python3 -c "
import sys,json
print(next((x['id'] for x in json.load(sys.stdin)['data']['list'] if x['name']=='kyverno'),''))")
if [ -n "$EXISTING" ]; then
  OUT=$(api PUT "/secret-keys/$EXISTING" "$TOKEN" '{"rotate_secret":true}' "$PLATFORM")
else
  OUT=$(api POST /secret-keys "$TOKEN" "{\"name\":\"kyverno\",\"server_enabled\":true,\"permission_ids\":[$PERM],\"status\":true,\"remark\":\"Kyverno admission policies read the recommendations\"}" "$PLATFORM")
fi
mkdir -p "$ROOT/target"
echo "$OUT" | python3 -c "
import sys,json
x=json.load(sys.stdin)['data']
open(sys.argv[1],'w',newline='').write(x['access_key']+':'+x['access_secret'])" "$ROOT/target/admission.cred"
echo "   credential kyverno ready, written to target/admission.cred"
api GET /configs/forecast.allow_admission_mode "$TOKEN" "" "$PLATFORM" > /dev/null
api PUT /configs/forecast.allow_admission_mode "$TOKEN" '{"config_value":"true"}' "$PLATFORM" > /dev/null
echo "   forecast.allow_admission_mode = true"

# (kubectl.exe, found from WSL, cannot open /mnt/... paths: the credential and every manifest are passed through stdin / as a value)
CRED=$(tr -d '\r\n' < "$ROOT/target/admission.cred")
"$K" -n kyverno create secret generic kube-resource-fcst-admission --from-literal=credential="$CRED" --dry-run=client -o yaml | "$K" apply -f - > /dev/null
echo "2. cluster: Secret kyverno/kube-resource-fcst-admission written"
"$K" apply -f - < "$HERE/kyverno-credential.yaml" > /dev/null
for f in kube-resource-fcst-requests kube-resource-fcst-require-module; do
  sed -e "s#PLATFORM_URL#$IN_CLUSTER#" -e 's#CREDENTIAL_SECRET#kube-resource-fcst-admission#' "$ROOT/deploy/kyverno/$f.yaml" | "$K" apply -f - 2>&1 | grep -v "^Warning"
done
"$K" delete clusterpolicy kubeforecast-resources --ignore-not-found 2>&1 | grep -v "^Warning" || true
echo "3. old policy kubeforecast-resources removed (if it was there)"
sleep 8

echo "4. checks"
"$K" delete -f - --ignore-not-found < "$HERE/admission-demo.yaml" > /dev/null 2>&1 || true
if OUT=$("$K" apply -f - < "$HERE/admission-demo.yaml" 2>&1); then
  echo "   FAIL: demo-unmanaged was admitted although no module manages it"; echo "$OUT"; exit 1
else
  echo "   refused, as it should be:"; echo "$OUT" | sed 's/^/     /'
fi
api POST /modules "$TOKEN" '{"name":"demo-unmanaged","namespace":"forecast-test","workload_kind":"Deployment","workload":"demo-unmanaged","mode":"admission"}' "$PLATFORM" > /dev/null \
  && echo "   module demo-unmanaged created on the platform"
"$K" apply -f - < "$HERE/admission-demo.yaml" 2>&1 | sed 's/^/     /' && echo "   admitted now (a module manages it)"
echo "done. The platform page \"准入决策记录\" has the decisions. Clean up: kubectl delete deploy demo-unmanaged -n forecast-test"
