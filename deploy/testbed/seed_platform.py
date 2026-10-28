#!/usr/bin/env python3
"""Seed a kube-resource-fcst platform for the local test bed, through its API (never the database).

  python deploy/testbed/seed_platform.py [--platform http://127.0.0.1:20001] [--prometheus http://127.0.0.1:30090]
         [--password 123456] [--modules load-web,load-api,load-batch,load-cache] [--mode auto|admission|recommend]
         [--credentials-dir DIR] [--modules-only] [--scale-up-max-step N --scale-down-max-step N --scale-up-cooldown S --scale-down-cooldown S]

Creates the Prometheus data source, the metric mappings (container-label-free queries: the cAdvisor series of a Docker
Desktop cluster have no container label), the modules for the test workloads, enables a set of algorithms (with one
formula algorithm), opens forecast.allow_auto_mode / allow_admission_mode, and creates two developer credentials:
  operator-forecast-test    only "Operator Decisions (read, report)"      -> file operator.cred
  kyverno-forecast-test     only "Admission Recommendations (read)"       -> file admission.cred
(each file holds "<access key>:<access secret>"; the secret is shown by the platform only once). Re-running is safe: what
exists is kept, credentials are rotated.
"""
import argparse, json, os, sys, urllib.request, urllib.error

ap = argparse.ArgumentParser()
ap.add_argument('--platform', default='http://127.0.0.1:20001')
ap.add_argument('--prometheus', default='http://127.0.0.1:30090')
ap.add_argument('--password', default='123456')
ap.add_argument('--modules', default='load-web,load-api,load-batch,load-cache')
ap.add_argument('--mode', default='auto')
ap.add_argument('--namespace', default='forecast-test')
ap.add_argument('--credentials-dir', default='.')
ap.add_argument('--modules-only', action='store_true', help='only the data source, mappings and modules: no algorithm, mode setting or credential is touched')
ap.add_argument('--limit-cpu-ratio', type=float, default=0)
ap.add_argument('--limit-memory-ratio', type=float, default=0)
ap.add_argument('--scale-up-max-step', type=int, default=0, help='replicas one scale-up action may add (0 = no limit)')
ap.add_argument('--scale-down-max-step', type=int, default=0)
ap.add_argument('--scale-up-cooldown', type=int, default=0, help='seconds between scale-ups (0 = none)')
ap.add_argument('--scale-down-cooldown', type=int, default=0)
a = ap.parse_args()
B = a.platform.rstrip('/') + '/api/v1'
token = ''


def call(method, path, body=None, ok=True):
    req = urllib.request.Request(B + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={'Content-Type': 'application/json', **({'Authorization': 'Bearer ' + token} if token else {})})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            out = json.load(r)
    except urllib.error.HTTPError as e:
        out = json.loads(e.read() or b'{}')
    if ok and out.get('code') != 10000:
        sys.exit(f'{method} {path}: {out}')
    return out


token = call('POST', '/auth/login', {'username': 'admin', 'password': a.password, 'force': True})['data']['token']

# ---- data source and mappings
ds = next((d for d in call('GET', '/datasources?page=1&page_size=100')['data']['list'] if d['name'] == 'k8s-prometheus'), None)
if not ds:
    ds = call('POST', '/datasources', {'name': 'k8s-prometheus', 'base_url': a.prometheus})['data']
print('datasource', ds['id'], ds['base_url'])
existing = {m['name']: m for m in call('GET', '/metric-mappings?page=1&page_size=200')['data']['list']}
pod = 'namespace="{{namespace}}",pod=~"{{workload}}-.*"'
want = {
    'cpu-workload': ('cpu', f'sum(rate(container_cpu_usage_seconds_total{{{pod}}}[{{{{range}}}}]))'),
    'memory-workload': ('memory', f'sum(container_memory_working_set_bytes{{{pod}}})'),
    'replicas-ready': ('replicas', 'sum(kube_deployment_status_replicas_available{namespace="{{namespace}}",deployment="{{workload}}"})'),
}
ids = {}
for name, (metric, query) in want.items():
    m = existing.get(name)
    if m is None:
        m = call('POST', '/metric-mappings', {'datasource_id': ds['id'], 'name': name, 'metric': metric, 'level': 'workload', 'query': query,
                                              'remark': 'no container filter: cAdvisor series carry no container label on this cluster'})['data']
    ids[metric] = m['id']
print('mappings', ids)

if not a.modules_only:
    # ---- algorithms
    for alg, body in {
        'naive': {'enabled': True}, 'ewma': {'enabled': True, 'params': {'alpha': 0.4}},
        'seasonal_naive': {'enabled': True, 'params': {'season_length_steps': 30}},
        'hist_percentile': {'enabled': True}, 'holt_winters': {'enabled': True, 'params': {'season_length_steps': 30}},
    }.items():
        call('PUT', f'/algorithms/{alg}', body)
    if call('GET', '/algorithms/peak_margin', ok=False).get('code') != 10000:
        call('POST', '/algorithms', {
            'id': 'peak_margin', 'category': 'technical', 'description': 'peak of the last 30 steps times a margin (formula)',
            'formula': 'smax(x, w) * margin', 'min_samples': 10, 'enabled': True, 'params': {'w': 30, 'margin': 1.1},
            'param_schema': [{'name': 'w', 'type': 'int', 'default': 30, 'min': 2, 'max': 500, 'unit': 'steps', 'description': 'window of the peak'},
                             {'name': 'margin', 'type': 'float', 'default': 1.1, 'min': 1, 'max': 3, 'description': 'headroom over the peak'}]})

    # ---- modes
    for key in ('forecast.allow_auto_mode', 'forecast.allow_admission_mode'):
        call('PUT', f'/configs/{key}', {'config_value': 'true'})

# ---- modules: replicas follow the CPU forecast (100m request x 60% target = 60m per pod)
specs = {'load-web': (2, 10), 'load-api': (2, 10), 'load-batch': (2, 10), 'load-cache': (2, 10)}
have = {m['name']: m for m in call('GET', '/modules?page=1&page_size=200')['data']['list']}
for name in [n for n in a.modules.split(',') if n]:
    if name in have:
        print('module', name, 'exists', have[name]['id'])
        continue
    fallback, top = specs.get(name, (2, 8))
    m = call('POST', '/modules', {
        'name': name, 'namespace': a.namespace, 'workload_kind': 'Deployment', 'workload': name, 'mode': a.mode,
        'lookback_hours': 3, 'horizon_minutes': 10, 'step_seconds': 60, 'min_points': 6, 'ttl_seconds': 600, 'headroom_ratio': 1.2,
        'min_replicas': 1, 'max_replicas': top, 'fallback_replicas': fallback, 'fallback_cpu_milli': 200, 'fallback_memory_bytes': 64 << 20,
        'min_cpu_milli': 50, 'max_cpu_milli': 2000, 'min_memory_bytes': 32 << 20, 'max_memory_bytes': 256 << 20,
        'limit_cpu_ratio': a.limit_cpu_ratio, 'limit_memory_ratio': a.limit_memory_ratio,
        'scale_up_max_step': a.scale_up_max_step, 'scale_down_max_step': a.scale_down_max_step,
        'scale_up_cooldown_seconds': a.scale_up_cooldown, 'scale_down_cooldown_seconds': a.scale_down_cooldown,
        'replica_mode': 'dynamic', 'capacity_mode': 'manual', 'pod_cpu_request_milli': 100, 'target_utilization': 0.6,
        'sources': [{'name': 'cpu', 'kind': 'cpu', 'mapping_id': ids['cpu'], 'stat': 'max', 'weight': 2},
                    {'name': 'memory', 'kind': 'memory', 'mapping_id': ids['memory'], 'stat': 'max'},
                    {'name': 'replicas', 'kind': 'replicas', 'mapping_id': ids['replicas']}]})['data']
    print('module', name, m['id'], a.mode)

if a.modules_only:
    sys.exit(0)

# ---- credentials
perms = {}


def walk(items):
    for p in items:
        perms[p.get('code')] = p['id']
        walk(p.get('children') or [])


data = call('GET', '/permissions?page=1&page_size=500')['data']
walk(data['list'] if isinstance(data, dict) else data)
keys = {k['name']: k for k in call('GET', '/secret-keys?page=1&page_size=100')['data']['list']}
os.makedirs(a.credentials_dir, exist_ok=True)
for name, perm, fname in (('operator-forecast-test', 'forecast:operator:use', 'operator.cred'), ('kyverno-forecast-test', 'forecast:admission:read', 'admission.cred')):
    if name in keys:
        k = call('PUT', f'/secret-keys/{keys[name]["id"]}', {'rotate_secret': True})['data']
    else:
        k = call('POST', '/secret-keys', {'name': name, 'key_type': 'developer_access', 'server_enabled': True, 'permission_ids': [perms[perm]],
                                          'remark': 'local test bed'})['data']
    with open(os.path.join(a.credentials_dir, fname), 'w') as f:
        f.write(k['access_key'] + ':' + k['access_secret'])
    print('credential', name, '->', fname)
