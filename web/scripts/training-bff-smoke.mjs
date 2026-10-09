// Production BFF transport smoke test. The upstream fixture never calls a
// real model or opens the user's database; this does not replace UI testing.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as delay } from 'node:timers/promises';
import { resolve } from 'node:path';
import { buildInterviewReviewHtml } from '../src/lib/interview-review-export.ts';

const exportFixture = { review: { interviewId: 'review-fixture', schemaVersion: '1.0', generatedAt: new Date().toISOString(), turns: [{ question: { id: 'q', text: '原题', topic: '事务' }, answer: '原始回答', inputMode: 'text', feedback: { score: 0, deferred: true, summary: '结束后反馈', strengths: [], gaps: [], coachTip: '' }, references: [{ title: '参考', answer: 'PRIVATE_REFERENCE_SENTINEL' }] }] }, recordings: [], executionRuns: [] };
const deferredExport = buildInterviewReviewHtml(exportFixture); assert.ok(!deferredExport.includes('PRIVATE_REFERENCE_SENTINEL')); assert.ok(!deferredExport.includes('0 / 100')); assert.ok(deferredExport.includes('结束后反馈'));
exportFixture.review.turns[0].feedback.deferred = false; exportFixture.review.turns[0].feedback.unavailable = true; exportFixture.review.turns[0].references = [];
const invalidExport = buildInterviewReviewHtml(exportFixture); assert.ok(invalidExport.includes('未计分，待核对')); assert.ok(!invalidExport.includes('0 / 100'));

const fixtureKey = 'training-smoke-test-key';
let upstreamCalls = 0;
const upstream = createServer(async (req, res) => {
  assert.equal(req.headers.authorization, `Bearer ${fixtureKey}`);
  upstreamCalls++;
  const url = new URL(req.url, 'http://localhost');
  assert.equal(url.pathname, '/api/v1/training');
  res.setHeader('Content-Type', 'application/json');
  if (url.searchParams.get('resource') === 'missing') { res.statusCode = 404; res.end(JSON.stringify({ error: { message: 'missing fixture' } })); return; }
  if (req.method === 'POST') {
    let body = ''; for await (const chunk of req) body += chunk;
    const data = JSON.parse(body); assert.equal(data.action, 'import'); assert.deepEqual(data.ids, ['old-fixture']);
    res.end(JSON.stringify({ imported: 1 })); return;
  }
  res.end(JSON.stringify({ resource: url.searchParams.get('resource'), cursor: url.searchParams.get('cursor'), items: [] }));
});
upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
const reservation = createServer(); reservation.listen(0, '127.0.0.1'); await once(reservation, 'listening'); const port = reservation.address().port; await new Promise(r => reservation.close(r));
const child = spawn(process.execPath, [resolve('node_modules/next/dist/bin/next'), 'start', '-p', String(port)], {
  cwd: process.cwd(), windowsHide: true,
  env: { ...process.env, BACKEND_URL: `http://127.0.0.1:${upstream.address().port}`, AIDE_API_KEY: fixtureKey },
  stdio: ['ignore', 'pipe', 'pipe'],
});
let serverOutput = ''; child.stdout.on('data', c => { serverOutput += c; }); child.stderr.on('data', c => { serverOutput += c; });
try {
  const base = `http://127.0.0.1:${port}`;
  let ready = false;
  for (let i = 0; i < 100; i++) { try { const r = await fetch(base); if (r.ok) { ready = true; break; } } catch {} if (child.exitCode !== null) break; await delay(100); }
  assert.ok(ready, `Next.js server failed to start: ${serverOutput}`);
  const home = await (await fetch(base)).text(); assert.ok(home.includes('训练历史'));
  const response = await fetch(`${base}/api/training?resource=history&cursor=cursor-fixture`, { headers: { Authorization: 'Bearer browser-must-not-forward-this' } });
  assert.equal(response.status, 200); assert.equal(response.headers.get('cache-control'), 'no-store'); assert.deepEqual(await response.json(), { resource: 'history', cursor: 'cursor-fixture', items: [] });
  const imported = await fetch(`${base}/api/training`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: 'import', ids: ['old-fixture'] }) }); assert.equal(imported.status, 200); assert.deepEqual(await imported.json(), { imported: 1 });
  const bad = await fetch(`${base}/api/training`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{' }); assert.equal(bad.status, 400);
  const missing = await fetch(`${base}/api/training?resource=missing`); assert.equal(missing.status, 404); assert.equal((await missing.json()).error.message, 'missing fixture');
  assert.equal(upstreamCalls, 3);
  console.log(JSON.stringify({ passed: true, checks: ['production navigation HTML', 'GET query forwarding', 'server-only auth', 'POST forwarding', 'invalid JSON rejection', 'backend status propagation', 'no-store cache', 'deferred/invalid review export privacy and scoring'] }));
} finally {
  child.kill(); if (child.exitCode === null) await once(child, 'exit');
  await new Promise(r => upstream.close(r));
}
