// Actual Go SQLite/services/routes + built Next BFF. Provider responses are
// deterministic fixtures, never reported as model quality or speed results.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createServer } from 'node:net';
import { resolve } from 'node:path';
import { writeFile } from 'node:fs/promises';
import { setTimeout as delay } from 'node:timers/promises';
const binary=process.argv[2];if(!binary)throw new Error('Pass the compiled aide-coach-check executable');
const go=spawn(resolve(binary),[],{windowsHide:true,stdio:['ignore','pipe','pipe']});
let logs='',readyLine='';go.stderr.on('data',v=>logs+=v);go.stdout.on('data',v=>readyLine+=v);
let next;const checks=[];
try {
  for(let i=0;i<100&&!readyLine.includes('\n');i++){if(go.exitCode!==null)break;await delay(50)}
  const info=JSON.parse(readyLine.split('\n')[0]);assert.equal(info.fixture,true);assert.equal(info.gradingVersion,'knowledge-evidence-v2','Rebuild the local fixture binary before this smoke test');
  const reservation=createServer();reservation.listen(0,'127.0.0.1');await once(reservation,'listening');const port=reservation.address().port;await new Promise(r=>reservation.close(r));
  next=spawn(process.execPath,[resolve('node_modules/next/dist/bin/next'),'start','-p',String(port)],{windowsHide:true,env:{...process.env,BACKEND_URL:info.url,AIDE_API_KEY:'fixture-only-key',AIDE_USE_MOCK:'false'},stdio:['ignore','pipe','pipe']});
  next.stdout.on('data',v=>logs+=v);next.stderr.on('data',v=>logs+=v);
  const base=`http://127.0.0.1:${port}`;
  let online=false;for(let i=0;i<100;i++){try{if((await fetch(base)).ok){online=true;break}}catch{};if(next.exitCode!==null)break;await delay(100)}assert.ok(online,'Next failed to start');
  const home=await(await fetch(base)).text();assert.ok(home.includes('八股练习'));assert.ok(home.includes('对话历史'));checks.push('production entry HTML includes practice/history');
  async function read(query){const r=await fetch(`${base}/api/coach?${new URLSearchParams(query)}`);assert.equal(r.headers.get('cache-control'),'no-store');return {status:r.status,body:await r.json()}}
  const capabilities=await read({resource:'capabilities'});assert.equal(capabilities.status,200);assert.equal(capabilities.body.gradingVersion,'knowledge-evidence-v2');assert.equal(capabilities.body.contextPolicyVersion,'practice-scoped-context-v2');checks.push('actual BFF exposes current grading and context capabilities without a conversation');
  async function mutate(body,status=200){const r=await fetch(`${base}/api/coach`,{method:'POST',headers:{'Content-Type':'application/json',Authorization:'Bearer never-forward-browser-key'},body:JSON.stringify(body)});assert.equal(r.status,status,JSON.stringify(await r.clone().json()));return r.json()}
  await mutate({action:'practice',count:0},400);await mutate({action:'practice',count:1.5},400);await mutate({action:'practice',count:13},409);checks.push('invalid count / insufficient pool preserved as HTTP errors');
  let c=await mutate({action:'practice',count:2});const key=c.id;assert.equal(c.plan.questions.length,2);assert.ok(c.plan.questions.every(q=>!q.reference));
  await mutate({action:'next',id:key,ordinal:1},409);
  const first={action:'answer',id:key,submissionId:'check-first-answer',message:'使用提交ID并保留结果'};
  c=await mutate(first);assert.equal(c.plan.attempts[0].status,'succeeded');assert.ok(c.plan.questions[0].reference);assert.ok(!c.plan.questions[1].reference);
  assert.ok(!c.plan.viewedAnswers?.[1], 'ordinary feedback must not mark explicit reveal');
  c=await mutate(first);assert.equal(c.plan.attempts.length,1);await mutate({...first,message:'不同内容'},409);checks.push('public/private frozen references + answer idempotency/conflict');
  const stream=await fetch(`${base}/api/chat`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sessionId:key,submissionId:'check-followup',message:'请解释恢复',model:'fixture-model',references:[c.messages[1].id]})});assert.equal(stream.status,200);const events=await stream.text();assert.ok(events.includes('text_delta'));assert.ok(events.includes('"conversation"'));checks.push('BFF forwards submission ID / references / actual durable SSE');
  c=(await read({id:key})).body;assert.equal(c.plan.current,0);assert.equal(c.plan.attempts.length,1);
  c=await mutate({action:'next',id:key,ordinal:1});c=await mutate({action:'next',id:key,ordinal:1});assert.equal(c.plan.current,1);
  c=await mutate({action:'answer',id:key,submissionId:'check-second-fail',message:'FIXTURE_INVALID_GRADE'});assert.equal(c.plan.attempts[1].status,'failed');assert.equal(c.plan.attempts[1].evaluation,undefined);await mutate({action:'finish',id:key,ordinal:2},409);checks.push('followup does not advance; Next replay once; invalid grade is stored failure, not zero');
  c=await mutate({action:'answer',id:key,submissionId:'check-second-pass',message:'重新独立回答',reanswer:true});c=await mutate({action:'finish',id:key,ordinal:2});assert.equal(c.plan.status,'completed');
  const training=await(await fetch(`${base}/api/training?resource=history&kind=practice`)).json();assert.ok(training.items.some(item=>item.id===key));const stats=await(await fetch(`${base}/api/training?resource=stats`)).json();assert.equal(stats.sessions,1);assert.equal(stats.evaluated,2);const group=stats.groups.find(g=>g.version==='knowledge-v1');assert.ok(group);assert.equal(group.scores.ownership,undefined);checks.push('practice result persists into real training history / knowledge-only stats');
  const restored=(await read({id:key})).body;assert.deepEqual(restored.messages,c.messages);assert.deepEqual(restored.plan.questions,c.plan.questions);checks.push('complete raw messages / order restored through BFF');
  let retest=await mutate({action:'retest',id:key,original:true});assert.equal(retest.plan.sourceConversation,key);assert.ok(retest.plan.questions.length>0);
  let variant=await mutate({action:'retest',id:key,original:false});assert.notEqual(variant.plan.questions[0].text,c.plan.questions[0].text);checks.push('original and same-capability variant retest snapshots');
  c=(await read({id:key})).body;await mutate({action:'delete',id:key,version:c.version,removeTraining:false});assert.equal((await read({id:key})).status,404);const kept=(await read({id:key,resource:'training'})).body;assert.equal(kept.trainingRetained,true);assert.equal(kept.messages.length,0);assert.equal(kept.plan.attempts.length,3);checks.push('delete raw conversation / retain chosen training facts; deleted sources cannot be recalled');
  await mutate({action:'delete',id:key,version:kept.version,removeTraining:true});assert.equal((await read({id:key,resource:'training'})).status,404);checks.push('purge retained training on explicit selection');
  const beforeRevealStats=await(await fetch(`${base}/api/training?resource=stats`)).json();
  let viewed=await mutate({action:'practice',count:1});const viewedKey=viewed.id;
  viewed=await mutate({action:'learn',id:viewedKey,ordinal:1});const revealMessages=viewed.messages;
  viewed=await mutate({action:'learn',id:viewedKey,ordinal:1});assert.deepEqual(viewed.messages,revealMessages);assert.ok(viewed.plan.viewedAnswers[1]);
  await mutate({action:'finish',id:viewedKey,ordinal:0},400);
  viewed=await mutate({action:'finish',id:viewedKey,ordinal:1});assert.equal(viewed.plan.status,'completed');assert.equal(viewed.plan.attempts.length,0);assert.equal(viewed.review.completed,0);assert.equal(viewed.review.skipped,1);assert.equal(viewed.review.viewedAnswers,1);assert.equal(viewed.review.needsReview[0].ordinal,1);
  const viewedRestored=(await read({id:viewedKey})).body;assert.deepEqual(viewedRestored.plan,viewed.plan);checks.push('explicit reveal is idempotent; viewed-only finish persists review flag without fabricated answer or score');
  const viewedHistory=await(await fetch(`${base}/api/training?resource=history&kind=practice`)).json();assert.equal(viewedHistory.items.find(item=>item.id===viewedKey).needsReview,1);
  const viewedStats=await(await fetch(`${base}/api/training?resource=stats`)).json();assert.equal(viewedStats.evaluated,beforeRevealStats.evaluated);assert.equal(viewedStats.answered,beforeRevealStats.answered);assert.equal(viewedStats.practiceNeedsReview,(beforeRevealStats.practiceNeedsReview??0)+1);
  const viewedRetest=await mutate({action:'retest',id:viewedKey,original:true});assert.equal(viewedRetest.plan.questions.length,1);assert.equal(viewedRetest.plan.sourceConversation,viewedKey);assert.ok(!viewedRetest.plan.viewedAnswers[1]);assert.ok(!viewedRetest.plan.questions[0].reference);checks.push('viewed-only question appears in history/stats and produces a fresh independent retest');
  let assisted=await mutate({action:'practice',count:1});assisted=await mutate({action:'learn',id:assisted.id,ordinal:1});assisted=await mutate({action:'answer',id:assisted.id,submissionId:'check-assist-after-view',message:'看过题解后用自己的话回答'});assert.equal(assisted.plan.attempts[0].assisted,true);
  assisted=await mutate({action:'finish',id:assisted.id,ordinal:1});assert.equal(assisted.review.independent,0);assert.equal(assisted.review.assisted,1);assert.equal(assisted.review.viewedAnswers,1);assert.equal(assisted.review.skipped,0);checks.push('own answer after reveal is evaluated separately and retains the review flag');
  let copied=await mutate({action:'practice',count:1});copied=await mutate({action:'learn',id:copied.id,ordinal:1});
  const copiedSubmission={action:'answer',id:copied.id,submissionId:'exact-copy',message:copied.plan.questions[0].reference};
  copied=await mutate(copiedSubmission);assert.equal(copied.plan.attempts[0].evaluation.correctness,5);assert.equal(copied.plan.attempts[0].evaluation.coverage,5);assert.equal(copied.plan.attempts[0].evaluation.explanation,5);assert.equal(copied.plan.attempts[0].evaluation.method,'exact_frozen_reference');assert.equal(copied.plan.attempts[0].assisted,true);assert.equal(copied.plan.viewedAnswers[1],true);checks.push('complete frozen reference copy earns three full content scores and retains assisted/retest flags');
  let missing=await mutate({action:'practice',count:1});missing=await mutate({action:'answer',id:missing.id,submissionId:'missing-answer-claim',message:'FIXTURE_MISSING_ANSWER'});assert.equal(missing.plan.attempts[0].status,'failed');assert.equal(missing.plan.attempts[0].errorCode,'assessment_evidence_conflict');assert.equal(missing.plan.attempts[0].evaluation,undefined);assert.equal(missing.plan.status,'evaluation_failed');checks.push('missing-answer claim cannot masquerade as score one through actual BFF');
  const beforeRevision=copied.plan.attempts[0].gradingRevision;const regrade={...copiedSubmission,regrade:true,gradingRevision:beforeRevision};copied=await mutate(regrade);assert.equal(copied.plan.attempts[0].gradingHistory.length,1);assert.equal(copied.plan.attempts[0].gradingRevision,beforeRevision+1);copied=await mutate(regrade);assert.equal(copied.plan.attempts[0].gradingHistory.length,1);checks.push('explicit regrade archives original evaluation and repeated request does not regrade again');
  const bad=await fetch(`${base}/api/coach`,{method:'POST',headers:{'Content-Type':'application/json'},body:'{'});assert.equal(bad.status,400);checks.push('BFF invalid JSON rejection; server-only auth passed actual Go key boundary');
  const result={passed:true,fixture:true,at:new Date().toISOString(),checks,realModelQuality:false,browserInteractionVerified:false};
  if(process.argv[3])await writeFile(resolve(process.argv[3]),JSON.stringify(result,null,2)+'\n');console.log(JSON.stringify(result));
}catch(e){console.error(String(e));console.error(logs.slice(-4000));process.exitCode=1}
finally{for(const child of [next,go])if(child){child.kill();if(child.exitCode===null)await once(child,'exit')}}
