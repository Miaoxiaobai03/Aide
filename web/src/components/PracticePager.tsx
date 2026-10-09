'use client';

import {useEffect,useRef,useState} from 'react';
import {QuestionComposer} from './QuestionComposer';
import {mergeQuestionSnapshot,validatePracticeEvent,clearPracticeStorage} from '@/lib/practice-page-state';
import {ChatMessage} from './ChatMessage';
import {QuestionConversation} from './QuestionConversation';
import {ExportButton} from './ExportButton';
import {coachRequest,type CoachConversation,type PracticeInfo,type PracticePageView,type CoachAttempt,CoachRequestError} from '@/lib/coach-client';

export function PracticePager({practiceId,model,onExit,onState,onPractice}:{practiceId:string;model:string;onExit:()=>void;onState?:(id:string,count:number)=>void;onPractice?:(c:CoachConversation)=>void}){
 const [info,setInfo]=useState<PracticeInfo|null>(null);
 const [active,setActive]=useState('');
 const [pages,setPages]=useState<Record<string,CoachConversation>>({});
 const [drafts,setDrafts]=useState<Record<string,string>>({});
 const [modes,setModes]=useState<Record<string,'answer'|'followup'>>({});
 const [collapsed,setCollapsed]=useState<Record<string,boolean>>({});
 const [errors,setErrors]=useState<Record<string,string>>({});
 const [references,setReferences]=useState<Record<string,string>>({});
 const [fragments,setFragments]=useState<Record<string,{messageId:string;start:number;end:number;revision:number}|undefined>>({});
 const [partials,setPartials]=useState<Record<string,string>>({});
 const [budgetBlocked,setBudgetBlocked]=useState<Record<string,boolean>>({});
 const [thinking,setThinking]=useState<Record<string,string>>({});
 const [loading,setLoading]=useState(false);
 const [navError,setNavError]=useState('');
 const [operation,setOperation]=useState('');
 const [deleting,setDeleting]=useState(false);
 const [transcribing,setTranscribing]=useState(false);
 const navSeq=useRef(0);
 const opRef=useRef('');
 const controller=useRef<AbortController|null>(null);
 const audioController=useRef<AbortController|null>(null);
 const live=useRef(true);
 const activeRef=useRef('');
 const scroll=useRef<HTMLDivElement>(null);
 const positions=useRef<Record<string,number>>({});
 const infoRef=useRef<PracticeInfo|null>(null);
 const backendReady=useRef(false);
 const prefix='offerpilot.practice.'+practiceId+'.';
 function read(key:string){try{return localStorage.getItem(prefix+key)}catch{return null}}
 function write(key:string,value:string){try{localStorage.setItem(prefix+key,value);return true}catch{return false}}
 function forget(key:string){try{localStorage.removeItem(prefix+key)}catch{}}
 function accept(c:CoachConversation,qid:string){
  // Reject stale asynchronous page responses.
  if(c.plan?.questions[0]?.id!==qid||c.practiceId!==practiceId)return;
  setPages(p=>({...p,[qid]:mergeQuestionSnapshot(p[qid],c,practiceId,qid)!}));
  setModes(p=>qid in p?p:{...p,[qid]:read('mode.'+qid)==='answer'?'answer':c.plan?.taught?.[1]&&(read('mode.'+qid)==='followup'||c.plan?.attempts.some(a=>a.status==='succeeded'))?'followup':'answer'});
  setCollapsed(p=>qid in p?p:{...p,[qid]:read('collapsed.'+qid)==='true'});
 }
 function acceptInfo(value:PracticeInfo){const prior=infoRef.current;if(prior&&(prior.version>value.version||prior.version===value.version&&Date.parse(prior.updatedAt)>Date.parse(value.updatedAt)))return;infoRef.current=value;setInfo(value);onState?.(practiceId,value.completed)}
 function saveLocation(qid:string){
  write('page',qid);
  try{const url=new URL(window.location.href);url.searchParams.set('practiceId',practiceId);url.searchParams.set('questionId',qid);window.history.replaceState(null,'',url)}catch{}
 }
 async function openPage(qid=''){
  if(!live.current)return;
  if(scroll.current&&activeRef.current){positions.current[activeRef.current]=scroll.current.scrollTop;write('scroll.'+activeRef.current,String(scroll.current.scrollTop))}
  const seq=++navSeq.current;
  activeRef.current=qid;setActive(qid);setLoading(true);setNavError('');
  try{
   if(!backendReady.current){const caps=await coachRequest<{pageStreamVersion?:string;contextPolicyVersion?:string;pageSummaryTemplate?:string}>({resource:'capabilities'});if(caps.pageStreamVersion!=='practice-page-events-v1'||caps.contextPolicyVersion!=='independent-question-pages-v5'||caps.pageSummaryTemplate!=='page-teaching-notes-v2')throw new Error('Go后端尚未加载新版题页请求协议，请重启后端后重试。');backendReady.current=true;}
   const view=await coachRequest<PracticePageView>({}, {action:'visitpage',id:practiceId,questionId:qid});
   if(!live.current||seq!==navSeq.current)return;
   const resolved=view.conversation.plan?.questions[0]?.id;
   if(!resolved)throw new Error('本题读取未完成，请重试');
   activeRef.current=resolved;setActive(resolved);acceptInfo(view.practice);accept(view.conversation,resolved);
   setDrafts(p=>resolved in p?p:{...p,[resolved]:read('draft.'+resolved)??''});
   saveLocation(resolved);
  }catch(e){if(live.current&&seq===navSeq.current){if(await missingPractice(e))return;setNavError((e as Error).message)}}
  finally{if(live.current&&seq===navSeq.current)setLoading(false)}
 }
 async function reload(qid:string){
  let view:PracticePageView;try{view=await coachRequest<PracticePageView>({resource:'page',id:practiceId,questionId:qid})}catch(e){await missingPractice(e);throw e}
  if(!live.current)return;
  acceptInfo(view.practice);accept(view.conversation,qid);
 }
 useEffect(()=>{
  live.current=true;
  let qid=read('page')??'';
  try{const url=new URL(window.location.href);if(url.searchParams.get('practiceId')===practiceId)qid=url.searchParams.get('questionId')??qid;localStorage.setItem('offerpilot.coach.active',practiceId)}catch{}
  void openPage(qid);
  return()=>{live.current=false;navSeq.current++;controller.current?.abort();audioController.current?.abort()};
 },[practiceId]);
 useEffect(()=>{if(scroll.current&&!loading)scroll.current.scrollTop=positions.current[active]??Number(read('scroll.'+active)??0)},[active,loading]);
 useEffect(()=>{Object.entries(modes).forEach(([qid,value])=>write('mode.'+qid,value));},[modes]);
 useEffect(()=>{Object.entries(collapsed).forEach(([qid,value])=>write('collapsed.'+qid,String(value)));},[collapsed]);
 // A refresh may arrive just before the cancelled server run saves its final
 // interrupted state. Poll persisted state only; never retry a model call.
 useEffect(()=>{
  if(operation||!info?.pages.some(p=>p.busy))return;
  const timer=setInterval(()=>{void reload(active).catch(()=>{})},1500);
  return()=>clearInterval(timer);
 },[info,active,operation]);
 useEffect(()=>{
  if(typeof BroadcastChannel==='undefined')return;
  const channel=new BroadcastChannel('offerpilot.coach.events');
  channel.onmessage=event=>{if(event.data?.type==='deleted'&&event.data.id===practiceId){invalidatePractice()}};
  return()=>channel.close();
 },[practiceId]);
 function invalidatePractice(){
  live.current=false;navSeq.current++;controller.current?.abort();audioController.current?.abort();clearLocal();
  infoRef.current=null;positions.current={};setInfo(null);setPages({});setDrafts({});setModes({});setCollapsed({});setErrors({});setReferences({});setFragments({});setPartials({});setThinking({});setBudgetBlocked({});setActive('');activeRef.current='';onState?.('',0);onExit();
 }
 async function missingPractice(e:unknown){
  if(!(e instanceof CoachRequestError)||e.status!==404)return false;
  try{await coachRequest({resource:'directory',id:practiceId});return false}catch(check){if(check instanceof CoachRequestError&&check.status===404){invalidatePractice();return true}return false}
 }
 // Focus revalidates persisted state even where cross-tab broadcasting is unavailable.
 useEffect(()=>{const refresh=()=>{if(live.current&&activeRef.current)void reload(activeRef.current).catch(()=>{})};window.addEventListener('focus',refresh);return()=>window.removeEventListener('focus',refresh)},[practiceId]);
 function changeDraft(qid:string,value:string){
  setDrafts(p=>({...p,[qid]:value}));
  if(!write('draft.'+qid,value))setErrors(p=>({...p,[qid]:'当前浏览器无法保存草稿，请保留输入后再刷新。'}));
 }
 function clearLocal(){
  try{clearPracticeStorage(localStorage,practiceId)}catch{}
  try{const url=new URL(window.location.href);url.searchParams.delete('practiceId');url.searchParams.delete('questionId');window.history.replaceState(null,'',url)}catch{}
 }
 function leave(){
  try{localStorage.removeItem('offerpilot.coach.active');const url=new URL(window.location.href);url.searchParams.delete('practiceId');url.searchParams.delete('questionId');window.history.replaceState(null,'',url)}catch{}
  onExit();
 }
 function setError(qid:string,text:string){setErrors(p=>({...p,[qid]:text}))}
 async function learn(){
  const qid=active; if(opRef.current||!pages[qid])return;
  opRef.current=qid;setOperation(qid);setError(qid,'');
  try{
   const c=await coachRequest<CoachConversation>({}, {action:'learn',practiceId,questionId:qid});
   if(!live.current)return;accept(c,qid);setCollapsed(p=>({...p,[qid]:false}));setModes(p=>({...p,[qid]:'answer'}));await reload(qid);
  }catch(e){if(live.current)setError(qid,(e as Error).message)}
  finally{opRef.current='';if(live.current)setOperation('')}
 }
 async function send(text:string,retry?:CoachAttempt,regrade=false,retryCompression=false){
  const qid=active;const c=pages[qid];
  if(opRef.current||!c||!text.trim()||loading||deleting)return;
  const action=retry||modes[qid]!=='followup'?'answer':'chat';
  let submissionId=retry?.id??crypto.randomUUID();
  if(action==='answer'&&!retry){
   try{const pending=JSON.parse(read('pending.'+qid)??'null');if(pending?.message===text&&pending?.action===action)submissionId=pending.submissionId}catch{}
   write('pending.'+qid,JSON.stringify({submissionId,message:text,action}));
  }
  opRef.current=qid;setOperation(qid);setError(qid,'');setBudgetBlocked(p=>({...p,[qid]:false}));setPartials(p=>({...p,[qid]:''}));setThinking(p=>({...p,[qid]:''}));
  const abort=new AbortController();controller.current=abort;
  try{
   if(action==='answer'){
    const result=await coachRequest<CoachConversation>({}, {action:'answer',practiceId,questionId:qid,submissionId,message:text,model,reanswer:!retry&&!!c.plan?.attempts.length,...(regrade?{regrade:true,gradingRevision:retry?.gradingRevision??0}:{})},abort.signal);
    if(!live.current)return;accept(result,qid);
    const a=result.plan?.attempts.find(a=>a.id===submissionId);
    if(a?.status!=='succeeded')throw new Error(a?.error??'评分未完成；原回答已保留，可重试原提交');
    setModes(p=>({...p,[qid]:'followup'}));forget('pending.'+qid);
   }else{
    const response=await fetch('/api/chat',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sessionId:c.id,practiceId,questionId:qid,submissionId,message:text,model,retryCompression,references:references[qid]?[references[qid]]:[],fragments:fragments[qid]?[fragments[qid]]:[]}),signal:abort.signal});
    if(!response.ok){const value=await response.json();throw new Error(typeof value.error==='string'?value.error:value.error?.message??'本题回答未完成')}
    if(!response.body)throw new Error('没有收到回答');
    const reader=response.body.getReader();const decoder=new TextDecoder();let buffer='';let doneEvent=false;let streamError='';let sequence=0;
    while(true){
     const {done,value}=await reader.read();buffer+=decoder.decode(value,{stream:!done});
     const lines=buffer.split('\n');buffer=lines.pop()??'';
     for(const line of lines){
      if(!line.startsWith('data: ')||line.slice(6)==='[DONE]')continue;
      const event=JSON.parse(line.slice(6));
      sequence=validatePracticeEvent(event,practiceId,qid,submissionId,sequence);
      if(!live.current)return;
      if(event.type==='text_delta')setPartials(p=>({...p,[qid]:(p[qid]??'')+event.content}));
      if(event.type==='thinking_delta')setThinking(p=>({...p,[qid]:(p[qid]??'')+event.content}));
      if(event.type==='done'&&event.conversation){accept(event.conversation,qid);doneEvent=true}
      if(event.type==='error'){streamError=event.message;if(event.code==='context_budget')setBudgetBlocked(p=>({...p,[qid]:true}))}
     }
     if(done)break;
    }
    if(streamError||!doneEvent)throw new Error(streamError||'连接中断；已保存的内容可恢复查看');
   }
   if(!live.current)return;
   setDrafts(p=>{if(p[qid]!==text)return p;forget('draft.'+qid);return {...p,[qid]:''}});
   setReferences(p=>({...p,[qid]:''}));setFragments(p=>({...p,[qid]:undefined}));
   await reload(qid);
  }catch(e){
   if(live.current){setError(qid,(e as Error).message);setDrafts(p=>{const value=p[qid]||text;write('draft.'+qid,value);return {...p,[qid]:value}});try{await reload(qid)}catch{}}
  }finally{
   opRef.current='';controller.current=null;
   if(live.current){setOperation('');setPartials(p=>({...p,[qid]:''}));setThinking(p=>({...p,[qid]:''}))}
  }
 }
 async function audio(blob:Blob,name='answer.wav'){
  if(opRef.current||transcribing)return;
  const qid=active;const abort=new AbortController();audioController.current=abort;setTranscribing(true);
  try{const response=await fetch('/api/transcribe',{method:'POST',headers:{'Content-Type':blob.type||'audio/wav','X-File-Name':encodeURIComponent(name)},body:blob,signal:abort.signal});const value=await response.json();if(!response.ok)throw new Error(value.error?.message??'语音转写失败');if(live.current)changeDraft(qid,value.text??value.transcript??'')}
  catch(e){if(live.current)setError(qid,(e as Error).message)}
  finally{audioController.current=null;if(live.current)setTranscribing(false)}
 }
 async function finish(){
  if(!info||opRef.current)return;
  if(!window.confirm(`结束练习？当前有效评分${info.completed}／${info.pages.length}题，未评分题不会自动记分；结束后仍可回看和重答。`))return;
  try{const view=await coachRequest<PracticePageView>({}, {action:'endpages',id:practiceId,version:info.version});if(live.current)acceptInfo(view.practice)}
  catch(e){if(live.current)setNavError((e as Error).message)}
 }
 async function remove(){
  if(!info||deleting||!window.confirm('删除本轮全部题目、对话、作答和评分？'))return;
  setDeleting(true);controller.current?.abort();audioController.current?.abort();
  try{
   await coachRequest({}, {action:'deletepages',id:practiceId,version:info.version});
   invalidatePractice();
   if(typeof BroadcastChannel!=='undefined'){const channel=new BroadcastChannel('offerpilot.coach.events');channel.postMessage({type:'deleted',id:practiceId});channel.close()}

  }catch(e){if(live.current){setNavError((e as Error).message);setDeleting(false)}}
 }
 async function retest(original:boolean){
  if(opRef.current)return;
  opRef.current=active;setOperation(active);
  try{const next=await coachRequest<CoachConversation>({}, {action:'retest',id:practiceId,model,original});if(live.current)onPractice?.(next)}
  catch(e){if(live.current)setNavError((e as Error).message)}
  finally{opRef.current='';if(live.current)setOperation('')}
 }
 const index=info?.pages.findIndex(p=>p.questionId===active)??-1;
 const c=pages[active];
 const attempts=c?.plan?.attempts??[];
 const failed=[...attempts].reverse().find(a=>a.status==='failed');
 const graded=attempts.some(a=>a.status==='succeeded');
 const lastGraded=[...attempts].reverse().find(a=>a.status==='succeeded');
 const viewed=!!c?.plan?.viewedAnswers?.[1];
 const teachingAvailable=!!c?.plan?.taught?.[1];
 const mode=modes[active]??'answer';
 const inputDisabled=!!operation||!!info?.pages.some(p=>p.busy)||loading||!!navError||deleting||!c||transcribing;
 return <div className="flex min-h-0 flex-1 flex-col">
  <header className="flex flex-wrap items-center gap-3 border-b bg-white/70 px-5 py-3 text-sm">
   <button onClick={leave}>返回面试诊断</button>
   <span>{info?.title??'八股练习'}</span>
   <button onClick={()=>void openPage(active)}>恢复／刷新本题</button>
   <button disabled={deleting} onClick={()=>void remove()}>删除整轮练习</button>
   {c&&<ExportButton sessionId={c.id} messages={c.messages.map(m=>({id:m.id,role:m.role,content:m.content,status:m.status}))}/>}
  </header>
  <nav aria-label="题目翻页" className="grid grid-cols-[auto_1fr_auto] items-center gap-3 border-b bg-white px-2 py-4 sm:px-5">
   <button aria-label="返回上一题" disabled={index<=0||deleting} className="rounded-lg border px-2 py-2 sm:px-3 disabled:opacity-40" onClick={()=>void openPage(info!.pages[index-1].questionId)}>← 上一题</button>
   <div className="text-center text-sm"><strong>第{index>=0?index+1:'—'}／{info?.pages.length??'—'}题</strong><p className="mt-1 text-slate-500">有效评分{info?.completed??0}题{info?.ended?' · 本场已结束':' · 追问不消耗题数'}</p></div>
   {index>=0&&info&&index<info.pages.length-1?<button aria-label="进入下一题" disabled={deleting} className="rounded-lg bg-accent px-3 py-2 text-white" onClick={()=>void openPage(info.pages[index+1].questionId)}>下一题 →</button>:<button disabled={!info||!!operation||deleting||info.ended} className="rounded-lg bg-accent px-3 py-2 text-white disabled:opacity-40" onClick={()=>void finish()}>{info?.ended?'已结束':'结束练习'}</button>}
  </nav>
  {info&&<div aria-label="题目目录" className="flex flex-wrap gap-2 border-b bg-white/60 px-5 py-2">{info.pages.map(p=><button key={p.questionId} disabled={deleting} aria-current={p.questionId===active?'page':undefined} title={p.question} className={'rounded px-2 py-1 text-xs '+(p.questionId===active?'bg-sky-100 text-sky-800':'border')} onClick={()=>void openPage(p.questionId)}>{p.ordinal}{p.busy?' · 处理中':p.viewedAnswer?' · 待加强':p.graded?' · 已评分':''}</button>)}</div>}
  {navError&&<div role="alert" className="m-4 rounded bg-amber-50 p-3 text-amber-800">{navError}<button className="ml-3 underline" onClick={()=>void openPage(active)}>重试读取本题</button></div>}
  {operation&&operation!==active&&<p className="px-5 py-2 text-sm text-slate-500">另一题正在处理，你可以继续翻页浏览，完成后再提交。</p>}
  <div ref={scroll} className="min-h-0 flex-1 overflow-y-auto px-5 py-5">
   <div key={active} className="mx-auto max-w-3xl space-y-5" data-question-id={active}>
    {loading?<p role="status">正在读取本题记录…</p>:!navError&&c&&<>
     <p className="text-sm text-slate-500">{viewed?'本题已看答案，待独立复测':graded?'本题已有有效评价':attempts.length?'本题作答已保存':'本题尚未作答，翻页不会自动记分'}</p>
     <QuestionConversation c={c} questionId={active} collapsed={!!collapsed[active]} busy={!!operation} onCollapse={close=>setCollapsed(p=>({...p,[active]:close}))} onReference={id=>{setReferences(p=>({...p,[active]:id}));setFragments(p=>({...p,[active]:undefined}));setModes(p=>({...p,[active]:'followup'}))}} onFragment={fragment=>{setFragments(p=>({...p,[active]:fragment}));setReferences(p=>({...p,[active]:''}));setModes(p=>({...p,[active]:'followup'}))}} onError={text=>setError(active,text)}/>
     {operation===active&&partials[active]&&<ChatMessage message={{id:'stream-'+active,role:'assistant',content:partials[active],thinking:thinking[active]}} isStreaming/>}
     {operation===active&&!partials[active]&&<p className="text-sm text-slate-500">正在处理本题，可以翻页查看其他题…</p>}
     {errors[active]&&<div role="alert" className="rounded border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800">{errors[active]}{budgetBlocked[active]&&mode==='followup'&&<button disabled={!!operation} className="ml-3 underline" onClick={()=>void send(drafts[active]??'',undefined,false,true)}>显式重试本题整理（可能调用一次摘要）</button>}</div>}
     <div className="flex flex-wrap gap-3 rounded-xl border bg-white p-4 text-sm">
      {!viewed&&<button disabled={!!operation} onClick={()=>void learn()}>查看答案（标记待加强）</button>}
      {viewed&&<button disabled={!!operation} onClick={()=>{setCollapsed(p=>({...p,[active]:true}));setModes(p=>({...p,[active]:'answer'}))}}>收起题解，自己作答</button>}
      <button disabled={!!operation} onClick={()=>setModes(p=>({...p,[active]:'answer'}))}>{attempts.length?'重答本题（保留历次回答）':'独立作答'}</button>
      {teachingAvailable&&<button disabled={!!operation} onClick={()=>setModes(p=>({...p,[active]:'followup'}))}>继续追问</button>}
      {failed&&<button disabled={!!operation} onClick={()=>void send(failed.answer,failed)}>重试原回答评分</button>}
      {lastGraded&&<button disabled={!!operation} onClick={()=>void send(lastGraded.answer,lastGraded,true)}>重评原回答（保留评分版本）</button>}
     </div>
    </>}
    {info?.ended&&<section className="rounded-xl border bg-white p-4 text-sm"><p>本场已结束：有效评分{info.completed}／{info.pages.length}题 · 未评分{info.pages.length-info.completed}题 · 看答案待加强{info.pages.filter(p=>p.viewedAnswer).length}题</p><p className="mt-2 text-slate-500">仍可逐题回看、追问或明确重答，原记录会保留。</p><div className="mt-3 flex gap-4"><button disabled={!!operation} onClick={()=>void retest(false)}>薄弱点变式复测</button><button disabled={!!operation} onClick={()=>void retest(true)}>薄弱点原题重答</button></div></section>}
   </div>
  </div>
  {(references[active]||fragments[active])&&<p className="px-5 text-center text-xs text-sky-700">已选择本题原文{fragments[active]?'片段':''}<button className="ml-3 underline" onClick={()=>{setReferences(p=>({...p,[active]:''}));setFragments(p=>({...p,[active]:undefined}))}}>取消引用</button></p>}
  <QuestionComposer key={active} mode={mode} viewed={viewed} hasAttempts={!!attempts.length} value={drafts[active]??''} onChange={value=>changeDraft(active,value)} onSend={text=>void send(text)} onAudioAnswer={audio} transcribing={transcribing} disabled={inputDisabled}/>
 </div>;
}
