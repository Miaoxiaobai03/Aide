import {describe,it} from 'node:test';
import assert from 'node:assert/strict';
import {mergeQuestionSnapshot,validatePracticeEvent,clearPracticeStorage} from '../src/lib/practice-page-state.ts';

const page=(version,qid='q2',practiceId='p')=>({id:practiceId+'::'+qid,practiceId,version,plan:{questions:[{id:qid}]}});
describe('practice page response ownership',()=>{
 it('a delayed read cannot replace a newer persisted page',()=>{
  const current=page(8);assert.equal(mergeQuestionSnapshot(current,page(5),'p','q2'),current);
  assert.equal(mergeQuestionSnapshot(current,page(9),'p','q2').version,9);
  assert.equal(mergeQuestionSnapshot(current,page(10,'q1'),'p','q2'),current);
  assert.equal(mergeQuestionSnapshot(current,page(10,'q2','other'),'p','q2'),current);
 });
 it('a stream remains owned by its originating question after navigation',()=>{
  const e={practiceId:'p',questionId:'q2',operationId:'op',sequence:1,type:'text_delta',content:'原题回复'};
  assert.equal(validatePracticeEvent(e,'p','q2','op',0),1);
  assert.throws(()=>validatePracticeEvent(e,'p','q3','op',0),/归属/);
  assert.throws(()=>validatePracticeEvent({...e,operationId:'stale'},'p','q2','op',0),/归属/);
  assert.throws(()=>validatePracticeEvent({...e,sequence:3},'p','q2','op',1),/不连续/);
  assert.throws(()=>validatePracticeEvent(e,'p','q2','op',1),/不连续/);
  assert.equal(validatePracticeEvent({...e,type:'error',sequence:2},'p','q2','op',1),2);
 });
});

it('deletion clears every question draft and marker while retaining other practices and free chat',()=>{
 const data=new Map([['offerpilot.practice.p.draft.q1','a'],['offerpilot.practice.p.draft.q10','b'],['offerpilot.practice.p.page','q10'],['offerpilot.practice.p.pending.q3','partial'],['offerpilot.practice.p2.draft.q1','keep'],['offerpilot.coach.active','p'],['free-chat','keep']]);
 const storage={get length(){return data.size},key:i=>[...data.keys()][i]??null,getItem:key=>data.get(key)??null,removeItem:key=>data.delete(key)};
 clearPracticeStorage(storage,'p');assert.deepEqual([...data.keys()],['offerpilot.practice.p2.draft.q1','free-chat']);clearPracticeStorage(storage,'p');assert.equal(data.size,2);
 data.set('offerpilot.coach.active','p2');clearPracticeStorage(storage,'p');assert.equal(data.get('offerpilot.coach.active'),'p2');
});
