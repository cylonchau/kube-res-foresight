// A stand-in Prometheus for the browser check: a deterministic daily-seasonal series per metric, with a
// deliberate hole every day between 03:00 and 03:30 UTC so the gap view has something to show.
import http from 'node:http';

const port = Number(process.env.PORT || 19090);
// minute of the UTC day where the daily 30 minute hole starts; a run whose lookback holds the hole sees too little contiguous
// history, so set HOLE_MINUTE to a time far from now (for example the opposite side of the clock) for the chain checks
const holeStart = Number(process.env.HOLE_MINUTE || 180);

function value(query, ts) {
  const minute = (ts % 86400) / 60;
  const phase = Math.sin((2 * Math.PI * minute) / 1440);
  // OOM, JVM and Kubernetes configuration series of the redesign checks: one OOM kill two hours ago, a heap at 95%
  if (query.includes('container_oom_events_total')) {
    const killedAt = Math.floor(Date.now() / 1000 / 300) * 300 - 7200;
    return Math.abs(ts - killedAt) < 150 ? 1 : 0;
  }
  if (query.includes('max_over_time(container_memory_working_set_bytes')) return 3.2e9 + 2e8 * phase;
  if (query.includes('kube_pod_container_resource_limits{resource="memory"')) return 2.5e9;
  if (query.includes('jvm_memory_used_bytes{area="heap"')) return 1.9e9;
  if (query.includes('jvm_memory_max_bytes{area="heap"')) return 2e9;
  // GPU, network and a custom queue-depth series of the multi-metric checks
  if (query.includes('DCGM_FI_DEV_GPU_UTIL')) return 0.5 + 0.3 * phase; // cards, after the template divides utilization by 100
  if (query.includes('DCGM_FI_DEV_FB_USED')) return 6.3e9 + 1.5e9 * phase;
  if (query.includes('container_network_receive_bytes_total')) return 2e6 + 1e6 * phase;
  if (query.includes('queue_depth')) return 40 + 20 * phase;
  if (query.includes('cpu')) return 1 + 0.5 * phase;
  if (query.includes('mem')) return 2e9 + 3e8 * phase;
  return 300 + 100 * phase;
}

http
  .createServer((req, res) => {
    let body = '';
    req.on('data', chunk => (body += chunk));
    req.on('end', () => {
      const url = new URL(req.url, 'http://x');
      // the Prometheus client sends query_range as a form-encoded POST
      const form = new URLSearchParams(body);
      const param = name => url.searchParams.get(name) ?? form.get(name);
      const send = payload => {
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify(payload));
      };
      if (url.pathname === '/api/v1/status/buildinfo') return send({ status: 'success', data: { version: '2.53.0-fake' } });
      if (url.pathname === '/api/v1/query') {
        return send({ status: 'success', data: { resultType: 'vector', result: [{ metric: {}, value: [Date.now() / 1000, '1'] }] } });
      }
      if (url.pathname === '/api/v1/query_range') {
        const query = param('query') || '';
        const start = Math.floor(Number(param('start')));
        const end = Math.floor(Number(param('end')));
        const step = Math.floor(Number(param('step')));
        if (!(step > 0) || !Number.isFinite(start) || !Number.isFinite(end) || end - start > 400 * 86400) {
          res.statusCode = 400;
          return send({ status: 'error', errorType: 'bad_data', error: 'invalid range' });
        }
        const values = [];
        for (let ts = start; ts <= end; ts += step) {
          const minuteOfDay = (ts % 86400) / 60;
          // the daily hole; the OOM series is exempt so its single kill is never swallowed by it
          if (minuteOfDay >= holeStart && minuteOfDay < holeStart + 30 && !query.includes('container_oom_events_total')) continue;
          values.push([ts, String(value(query, ts))]);
        }
        return send({ status: 'success', data: { resultType: 'matrix', result: values.length ? [{ metric: {}, values }] : [] } });
      }
      res.statusCode = 404;
      send({ status: 'error', errorType: 'bad_data', error: 'not found' });
    });
  })
  .listen(port, '127.0.0.1', () => console.log('fake prometheus on', port));
