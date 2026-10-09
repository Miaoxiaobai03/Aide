'use client';

import { useEffect, useRef, useState } from 'react';
import { ChatMessage } from './ChatMessage';
import { MarkdownContent } from './MarkdownContent';
import { ChatInput } from './ChatInput';
import { ExportButton } from './ExportButton';
import { PracticePager } from './PracticePager';
import { coachRequest, CoachRequestError, type CoachConversation, type CoachListing, type CoachEvaluation, type CoachAttempt } from '@/lib/coach-client';

function EvaluationCard({ value }: { value: CoachEvaluation }) {
  return <div className="space-y-2 rounded-xl border border-sky-200 bg-white p-4">
    <p>正确性 {value.correctness}/5 · 要点覆盖 {value.coverage}/5 · 解释清晰度 {value.explanation}/5</p>
		{value.method==='exact_frozen_reference'&&<p className="text-xs text-slate-500">与本题完整参考答案一致，内容评分满分；仍需独立复测验证掌握。</p>}
    {(value.strengths ?? []).map((text, i) => <p key={i}>优势：{text}</p>)}
    {(value.gaps ?? []).map((gap, i) => <div key={i}><p>薄弱点：{gap.point} · {gap.reason}</p><blockquote className="border-l-2 pl-3 text-sm text-slate-500">知识依据：{gap.quote}</blockquote></div>)}
    <p>建议：{value.advice}</p>
  </div>;
}

export function CoachChat({ model, resetKey, openId, onState }: { model: string; resetKey: number; openId?: string; onState?: (id: string, questions: number) => void }) {
  const [conversation, setConversation] = useState<CoachConversation | null>(null);
  const [list, setList] = useState<CoachListing[]>([]);
  const [draft, setDraft] = useState('');
  const [count, setCount] = useState('10');
  const [error, setError] = useState('');
	const [backendWarning,setBackendWarning]=useState('');
  const [busy, setBusy] = useState(false);
  const [transcribing, setTranscribing] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);
	const [deleteDialog,setDeleteDialog]=useState(false);
  const [reference, setReference] = useState('');
  const [fragment,setFragment]=useState<{messageId:string;start:number;end:number;revision:number}|null>(null);
  const [corrects, setCorrects] = useState('');
  const [reanswer, setReanswer] = useState(false);
  const [practiceInputMode, setPracticeInputMode] = useState<'answer'|'followup'>('answer');
  const [collapsedAnswers, setCollapsedAnswers] = useState<Record<number, boolean>>({});
  const [partial, setPartial] = useState('');
  const [thinking, setThinking] = useState('');
  const end = useRef<HTMLDivElement>(null);
  const requestSeq = useRef(0);
  const busyRef = useRef(false);
  const controller = useRef<AbortController | null>(null);
  const audioController = useRef<AbortController | null>(null);
  const resetSeen = useRef(resetKey);
  const autoScroll = useRef(true);
  const deleteInFlight = useRef(false);
  const restorePending = useRef<{ submissionId: string; message: string; action: string } | null>(null);
	async function requireCurrentBackend() {
		try {
			const versions=await coachRequest<{gradingVersion?:string;contextPolicyVersion?:string}>({resource:'capabilities'});
			if(versions.gradingVersion!=='knowledge-evidence-v2'||versions.contextPolicyVersion!=='independent-question-pages-v4')throw new Error('version');
			setBackendWarning('');
		} catch {
			const text='无法确认Go后端已加载新版评分与上下文策略。请重启后端，再点击恢复／刷新；仅刷新网页不会更新后端。';setBackendWarning(text);throw new Error(text);
		}
	}

  function accept(c: CoachConversation) {
    setConversation(c);
    onState?.(c.id, c.plan ? new Set(c.plan.attempts.filter(a => a.status === 'succeeded').map(a => a.ordinal)).size : c.messages.filter(m => m.role === 'user').length);
    try { localStorage.setItem('offerpilot.coach.active', c.id); } catch {}
  }
  function clearMissingRecord(id:string) {
    try {
      if(localStorage.getItem('offerpilot.coach.active')===id)localStorage.removeItem('offerpilot.coach.active');
      const prefix='offerpilot.practice.'+id+'.';
      const keys:string[]=[];
      for(let i=0;i<localStorage.length;i++){const key=localStorage.key(i);if(key?.startsWith(prefix))keys.push(key)}
      keys.forEach(key=>localStorage.removeItem(key));
      const url=new URL(window.location.href);
      if(url.searchParams.get('practiceId')===id){url.searchParams.delete('practiceId');url.searchParams.delete('questionId');window.history.replaceState(null,'',url)}
    }catch{}
  }
  async function refreshList() { const seq=requestSeq.current;const result = await coachRequest<{ items: CoachListing[] }>({ resource: 'list' }); if(seq===requestSeq.current)setList(result.items); }
  async function open(id: string) {
		void requireCurrentBackend().catch(()=>{});
    const seq = ++requestSeq.current; setError('');
    try { let c:CoachConversation;try{c=await coachRequest<CoachConversation>({ id });}catch{c=await coachRequest<CoachConversation>({id,resource:'training'});} if (seq === requestSeq.current) { accept(c); setReference(''); setFragment(null); setCorrects(''); setReanswer(false); setDraft(''); restorePending.current=null; } }
    catch (e) { if (seq === requestSeq.current) {
      if(e instanceof CoachRequestError&&e.status===404){clearMissingRecord(id);setConversation(null);onState?.('',0);setError('这条记录已清除，可以开始新的练习。');}
      else setError((e as Error).message);
    } }
  }
  async function create() {
    if (busyRef.current) return;
    setBusy(true); busyRef.current = true; setError('');
    const seq=++requestSeq.current;controller.current=new AbortController();
    try { const c=await coachRequest<CoachConversation>({}, { action: 'create' },controller.current.signal);if(seq!==requestSeq.current)return;accept(c); setDraft(''); setReference(''); setFragment(null); setCorrects(''); restorePending.current=null; await refreshList(); }
    catch (e) { if(seq===requestSeq.current)setError((e as Error).message); } finally { if(seq===requestSeq.current){setBusy(false); busyRef.current = false;controller.current=null;} }
  }
  useEffect(() => {
    let mounted = true;
		void requireCurrentBackend().catch(()=>{});
    void (async () => { try { const result = await coachRequest<{ items: CoachListing[] }>({ resource: 'list' }); if (!mounted) return; setList(result.items); let key = openId; try { key ||= new URL(window.location.href).searchParams.get('practiceId') ?? undefined; key ||= localStorage.getItem('offerpilot.coach.active') ?? undefined; } catch {} if (key && result.items.some(c => c.id === key)) await open(key); else if(key) clearMissingRecord(key);
 else if(key){try{localStorage.removeItem('offerpilot.coach.active')}catch{}} } catch (e) { if (mounted) setError((e as Error).message); } })();
    return () => { mounted = false; requestSeq.current++; controller.current?.abort();audioController.current?.abort(); };
  }, []);
  useEffect(() => { if (openId && conversation?.id !== openId) void open(openId); }, [openId]);
  useEffect(() => { if (resetSeen.current !== resetKey) { resetSeen.current = resetKey; void create(); } }, [resetKey]);
  useEffect(() => { if (autoScroll.current) end.current?.scrollIntoView({ behavior: 'auto' }); }, [conversation, partial]);
  useEffect(()=>{setReference('');setCorrects('');setFragment(null);setPracticeInputMode(conversation?.plan?.discussionOrdinal&&conversation.plan.discussionOrdinal!==conversation.plan.current+1?'followup':'answer');setReanswer(false);restorePending.current=null;},[conversation?.id,conversation?.plan?.current,conversation?.plan?.discussionOrdinal]);
  useEffect(()=>{setCollapsedAnswers({});},[conversation?.id]);
  useEffect(()=>{
    // Cross-tab deletion notifications carry only an ID and leave no persisted
    // tombstone or transcript. A stale tab must also drop its working context.
    if(typeof BroadcastChannel==='undefined')return;
    const channel=new BroadcastChannel('offerpilot.coach.events');
    channel.onmessage=event=>{
      if(event.data?.type!=='deleted')return;
      setList(items=>items.filter(item=>item.id!==event.data.id));
      if(event.data.id!==conversation?.id)return;
      requestSeq.current++;controller.current?.abort();audioController.current?.abort();controller.current=null;busyRef.current=false;setTranscribing(false);
      setBusy(false);setConversation(null);onState?.('',0);setDraft('');setPartial('');setThinking('');setReference('');setFragment(null);setCorrects('');setReanswer(false);setCollapsedAnswers({});restorePending.current=null;setDeleteDialog(false);setError('');
    };
    return()=>channel.close();
  },[conversation?.id]);

  async function mutate(action: string, extra: Record<string, unknown> = {}) {
    if (busyRef.current) return;
    setBusy(true); busyRef.current = true; setError(''); autoScroll.current=true;
    const seq=++requestSeq.current; controller.current=new AbortController();
    try {
      const c=await coachRequest<CoachConversation>({}, { action, id: conversation?.id, ordinal: conversation?.plan?.discussionOrdinal || (conversation?.plan?.current ?? 0) + 1, model, ...extra },controller.current.signal);
      if(seq!==requestSeq.current)return;accept(c);
      setReference(''); setFragment(null); setCorrects(''); setReanswer(false); await refreshList();
			if(action==='learn'){setCollapsedAnswers(prev=>({...prev,[(conversation?.plan?.current??0)+1]:false}));setPracticeInputMode('answer');}
			if(['next','finish'].includes(action))setCollapsedAnswers(prev=>({...prev,[(conversation?.plan?.current??0)+1]:true}));
			if(action==='practice'){try{localStorage.removeItem('offerpilot.coach.creation')}catch{}}
			if(['practice','next','finish','retest','focus'].includes(action)){setDraft('');restorePending.current=null;}
    } catch (e) { if(seq===requestSeq.current)setError((e as Error).message); } finally { if(seq===requestSeq.current){setBusy(false); busyRef.current = false;controller.current=null;} }
  }
  async function startPractice() {
    if (!/^\d+$/.test(count) || Number(count) < 1 || Number(count) > 100) { setError('请输入1—100的整数题数'); return; }
    const pendingKey='offerpilot.coach.creation';let submissionId=crypto.randomUUID();try{const old=JSON.parse(localStorage.getItem(pendingKey)??'null');if(old?.count===Number(count))submissionId=old.submissionId;localStorage.setItem(pendingKey,JSON.stringify({count:Number(count),submissionId}));}catch{}
    await mutate('practice', { count: Number(count),submissionId });
  }
  const plan = conversation?.plan;
  const progressOrdinal = (plan?.current ?? 0) + 1;
  const ordinal = plan?.discussionOrdinal || progressOrdinal;
  const reviewing = !!plan && ordinal!==progressOrdinal;
  const attempts = plan?.attempts.filter(a => a.ordinal === ordinal) ?? [];
  const ready = attempts.some(a => a.status === 'succeeded');
  const lastFailed = [...attempts].reverse().find(a => a.status === 'failed');
  const viewedAnswer = !!plan?.viewedAnswers?.[ordinal];
  const isAnswer = !!plan && !reviewing && plan.status !== 'completed' && (!ready || reanswer) && practiceInputMode==='answer';
  function returnToAnswer() {
    setCollapsedAnswers(prev=>({...prev,[ordinal]:true}));setPracticeInputMode('answer');setReanswer(attempts.length>0);
  }
  const referenceFor = (questionOrdinal: number) => {
    const question = plan?.questions[questionOrdinal - 1];
    return question?.referenceMarkdown || question?.reference || '';
  };

  async function send(message: string, retry?: CoachAttempt) {
    if (!message.trim() || busyRef.current) return;
    setBusy(true); busyRef.current = true; setError(''); setPartial(''); setThinking(''); autoScroll.current=true;
    const seq=++requestSeq.current;controller.current=new AbortController();
    let key = conversation?.id;
    const originalDraft = message;
    try {
			await requireCurrentBackend();
      if(seq!==requestSeq.current)return;
      if (!key) { const c = await coachRequest<CoachConversation>({}, { action: 'create' },controller.current!.signal);if(seq!==requestSeq.current)return; accept(c); key=c.id; }
      const pending = restorePending.current?.message === message && restorePending.current.action === (retry || isAnswer ? 'answer' : 'chat') ? restorePending.current : null;
      const submissionId = retry?.id ?? pending?.submissionId ?? crypto.randomUUID();
      const action = retry || isAnswer ? 'answer' : 'chat';
      restorePending.current = { submissionId, message, action };
      if (action === 'answer') {
        const c = await coachRequest<CoachConversation>({}, { action: 'answer', id: key, conversationId: key, submissionId, message, model, reanswer,questionId:plan?.questions[(retry?.ordinal??ordinal)-1]?.id,
          ...(retry?.errorCode==='assessment_legacy_invalid'?{regrade:true,gradingRevision:retry.gradingRevision??0}:{}) },controller.current!.signal);
        if(seq!==requestSeq.current)return;
        accept(c);
        const a = c.plan?.attempts.find(a => a.id === submissionId);
        if (a?.status !== 'succeeded') throw new Error(a?.error ?? '评价尚未完成，可恢复或重试原提交');
      } else {
        const response = await fetch('/api/chat', { method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ sessionId: key, submissionId, message, model, questionId:plan?.questions[ordinal-1]?.id, references: reference ? [reference] : [], fragments:fragment?[fragment]:[],corrects }), signal: controller.current!.signal });
        if (!response.ok) { const body=await response.json(); throw new Error(typeof body.error === 'string' ? body.error : body.error?.message ?? '提交失败'); }
        if (!response.body) throw new Error('没有收到模型数据');
        const reader=response.body.getReader(); const decoder=new TextDecoder(); let buffer=''; let finished=false; let streamError='';
        while (true) {
          const { done, value }=await reader.read(); if (done) break;
          if(seq!==requestSeq.current){await reader.cancel();return;}
          buffer += decoder.decode(value, { stream: true }); const lines=buffer.split('\n'); buffer=lines.pop() ?? '';
          for (const line of lines) { if (!line.startsWith('data: ') || line.slice(6)==='[DONE]') continue;
            const event=JSON.parse(line.slice(6));
            if (event.type==='text_delta') setPartial(p => p+event.content);
            if (event.type==='thinking_delta') setThinking(p => p+event.content);
            if (event.type==='done' && event.conversation) { accept(event.conversation); finished=true; }
            if (event.type==='error') streamError=event.message;
          }
        }
        if (streamError || !finished) throw new Error(streamError || '连接中断，已保存的原文可恢复');
      }
      if(seq!==requestSeq.current)return;
      setDraft(''); setReference(''); setFragment(null); setCorrects(''); setReanswer(false); restorePending.current=null; setPartial(''); await refreshList();
      setFragment(null);
    } catch (e) {
      if(seq!==requestSeq.current)return;
      setDraft(originalDraft);setError((e as Error).message);
      if (key) { try { const c=await coachRequest<CoachConversation>({ id:key });if(seq!==requestSeq.current)return;accept(c);if(c.messages.some(m=>m.role==='assistant'&&m.submissionId===restorePending.current?.submissionId&&['interrupted','failed'].includes(m.status)))restorePending.current=null; } catch {} }
    } finally { if(seq===requestSeq.current){setBusy(false); busyRef.current=false; controller.current=null; setThinking('');} }
  }
  async function audio(blob: Blob, name = 'answer.wav') {
    if (busyRef.current||transcribing) return; setTranscribing(true); setError('');
    const seq=requestSeq.current;const audioAbort=new AbortController();audioController.current=audioAbort;
    try { const response=await fetch('/api/transcribe', { method:'POST', headers:{ 'Content-Type':blob.type || 'audio/wav','X-File-Name':encodeURIComponent(name) }, body:blob,signal:audioAbort.signal }); const value=await response.json(); if(seq!==requestSeq.current)return;if (!response.ok) throw new Error(value.error?.message ?? value.error ?? '转写失败'); setDraft(value.text ?? value.transcript ?? ''); }
    catch(e) { if(seq===requestSeq.current)setError((e as Error).message); } finally { if(audioController.current===audioAbort){audioController.current=null;setTranscribing(false);} }
  }
  async function remove(removeTraining?:boolean) {
    if (!conversation || deleteInFlight.current) return;
    const linked=!!conversation.plan;
    if(linked&&removeTraining===undefined){setDeleteDialog(true);return;}
    if (!linked&&!window.confirm('删除此会话及全部消息？')) return;
    deleteInFlight.current=true;setDeleteDialog(false);
    const key=conversation.id;const seq=++requestSeq.current;controller.current?.abort();audioController.current?.abort();setTranscribing(false);controller.current=null;setBusy(true);busyRef.current=true;
    try {
      // Checkpoints may have advanced the version since the displayed snapshot.
      for(let attempt=0;attempt<3;attempt++){
        let c=conversation;
        try {c=await coachRequest<CoachConversation>({id:key});}
        catch(e){if(!(e instanceof CoachRequestError)||e.status!==404)throw e;}
        try { await coachRequest({}, {action:'delete',id:key,version:c.version,removeTraining:true});break; }
        catch(e){if(attempt===2||!(e instanceof CoachRequestError)||e.code!=='conflict')throw e;}
      }
      if(seq!==requestSeq.current)return;
      if(typeof BroadcastChannel!=='undefined'){const channel=new BroadcastChannel('offerpilot.coach.events');channel.postMessage({type:'deleted',id:key});channel.close();}
      setConversation(null);onState?.('',0);setDraft('');setPartial('');setThinking('');setReference('');setFragment(null);setCorrects('');setReanswer(false);setCollapsedAnswers({});restorePending.current=null;setDeleteDialog(false);setError('');try{localStorage.removeItem('offerpilot.coach.active')}catch{};await refreshList();
    } catch(e){if(seq===requestSeq.current)setError((e as Error).message)}finally{deleteInFlight.current=false;if(seq===requestSeq.current){setBusy(false);busyRef.current=false;}}
  }
  async function editMessage(messageId:string, current:string, deleted=false) {
    if (!conversation || busy) return;const text=deleted ? '' : window.prompt('修改原消息会使相关摘要失效；训练原始作答请使用重答。',current);if(text===null)return;
    await mutate('edit',{ messageId,text,deleted,version:conversation.version });
  }
  if (conversation?.plan && !conversation.practiceId) return <PracticePager key={conversation.id} model={model} practiceId={conversation.id} onExit={()=>{setConversation(null);onState?.('',0);void refreshList();}} onState={onState} onPractice={accept} />;
  return <div className="flex min-h-0 flex-1 flex-col">
		{backendWarning&&<div className="bg-amber-50 px-5 py-3 text-sm text-amber-800">{backendWarning}</div>}
    <div className="flex flex-wrap items-center gap-3 border-b bg-white/70 px-5 py-3 text-sm">
      <button disabled={busy} onClick={() => { setHistoryOpen(!historyOpen);void refreshList().catch(e=>setError(e.message)); }}>对话历史</button>
      <button disabled={busy} onClick={create}>新对话</button>
      <label>题数 <input aria-label="八股题数" type="number" min="1" max="100" value={count} onChange={e=>setCount(e.target.value)} className="w-16 rounded border p-1" /></label>
      <button disabled={busy} onClick={startPractice} className="rounded-lg bg-accent px-3 py-2 text-white">八股练习</button>
      {conversation && <button disabled={busy} onClick={()=>open(conversation.id)}>恢复／刷新</button>}
      {conversation && <button onClick={()=>remove()}>删除会话</button>}
      {conversation && <ExportButton sessionId={conversation.id} messages={conversation.messages.map(m=>({id:m.id,role:m.role,content:m.content,status:m.status}))} />}
    </div>
    {historyOpen && <div className="max-h-48 overflow-y-auto border-b bg-white px-5 py-2">{list.length ? list.map(item=><button key={item.id} disabled={busy} className="block w-full py-2 text-left text-sm" onClick={()=>{void open(item.id);setHistoryOpen(false);}}>{item.title} · {new Date(item.updatedAt).toLocaleString()}{item.mode==='practice' && ` · ${item.completed}/${item.target}题`}</button>):<p>暂无历史对话</p>}</div>}
    {error && <div className="mx-5 my-2 rounded border border-amber-200 bg-amber-50 p-3 text-sm" role="alert">{error}</div>}
		{deleteDialog&&<div role="dialog" aria-label="删除整轮练习" className="m-5 rounded-xl border bg-white p-4"><p>删除本场全部题目、对话、作答、评分、摘要和索引；能力统计同步移除本场结果。已独立创建的其他复测保留，但不再链接到本场。</p><div className="mt-3 flex flex-wrap gap-4"><button onClick={()=>remove(true)}>删除整轮练习及全部数据</button><button onClick={()=>setDeleteDialog(false)}>取消</button></div></div>}
    <div className="flex-1 overflow-y-auto px-5 py-5" onWheel={e=>{if(e.deltaY<0)autoScroll.current=false;}} onScroll={e=>{const el=e.currentTarget;if(el.scrollHeight-el.scrollTop-el.clientHeight<80)autoScroll.current=true;}}>
      <div className="mx-auto max-w-3xl space-y-5">
        {!conversation?.messages.length && <div className="py-16 text-center"><img src="/brand/offerpilot-icon-192.png" alt="OfferPilot" className="mx-auto mb-6 h-20 w-20 rounded-2xl" /><h1 className="text-2xl font-bold">面试诊断 Agent</h1><p className="mt-3 text-slate-500">自由提问并检索知识资料，或开始八股练习。<br />默认10题，作答后反馈，可追问再进入下一题。</p><button disabled={busy} onClick={startPractice} className="mt-6 rounded-xl bg-accent px-5 py-3 text-white">进入八股练习</button></div>}
        {plan && conversation && <nav aria-label="练习题目回看" className="flex flex-wrap gap-2 rounded-xl border bg-white p-3 text-sm">{conversation.questionGroups?.map(group=><button key={group.questionId} disabled={busy} aria-pressed={group.ordinal===ordinal} className={group.ordinal===ordinal?'rounded bg-sky-100 px-3 py-2':'rounded border px-3 py-2'} title={`${group.title} · ${group.viewedAnswer?'已看答案，待加强':group.state}`} onClick={()=>mutate('focus',{questionId:group.questionId})}>第{group.ordinal}题{group.viewedAnswer?' · 待加强':''}</button>)}{reviewing&&<button disabled={busy} onClick={()=>mutate('focus',{questionId:plan.questions[plan.current].id})}>返回当前第{progressOrdinal}题</button>}</nav>}
        {plan && conversation && <div className="sticky top-0 z-10 rounded-xl border bg-white/95 p-3 text-sm"><p>{reviewing?`回看第${ordinal}题 · 当前进度第${progressOrdinal}题`:`第${ordinal}/${plan.questions.length}题`} · 有效评分{new Set(plan?.attempts.filter(a=>a.status==='succeeded').map(a=>a.ordinal)).size}题{plan?.status==='completed'?' · 本场已结束':' · 追问不消耗题数'}{viewedAnswer&&' · 本题已看答案，待加强'}</p>{!conversation.deleted && !reviewing && plan?.status!=='completed' && <div className="mt-2 flex flex-wrap gap-3">{viewedAnswer && <><button disabled={busy} onClick={returnToAnswer}>收起题解，自己作答</button><button disabled={busy} onClick={()=>{setCollapsedAnswers(prev=>({...prev,[ordinal]:false}));setPracticeInputMode('followup');setReanswer(false);}}>查看题解／追问</button></>}{!reviewing&&(ready||viewedAnswer)&&<button disabled={busy} className="rounded bg-accent px-3 py-2 text-white" onClick={()=>mutate(ordinal===plan.questions.length?'finish':'next')}>{ordinal===plan.questions.length?'结束并查看总结':'下一题'}</button>}{viewedAnswer&&!ready&&<span className="text-xs text-amber-700">直接下一题会保留待加强标记，不记作答成绩</span>}</div>}</div>}
        {conversation?.messages.filter(message=>(!plan||message.ordinal===ordinal||message.questionId===plan.questions[ordinal-1]?.id)&&!(message.role==='assistant'&&message.status!=='complete'&&!message.content.trim())).map(message=><article key={message.id} className="space-y-2">
          {message.kind==='evaluation' ? message.status==='superseded' ? <p className="text-sm text-amber-700">此条为无效或已替代的历史评分，保留原文备查，不计入成绩。</p> : <><EvaluationCard value={JSON.parse(message.content) as CoachEvaluation} /><details className="text-sm"><summary>本题知识参考（当时快照）</summary><div className="markdown-body mt-2"><MarkdownContent>{referenceFor(message.ordinal??1)}</MarkdownContent></div></details></> : message.kind==='teaching' && message.content===plan?.questions[(message.ordinal??1)-1]?.reference ? <details open={!collapsedAnswers[message.ordinal??1]} onToggle={event=>{const collapsed=!event.currentTarget.open;if(collapsed&&(message.ordinal??1)===ordinal&&!ready){setPracticeInputMode('answer');setReanswer(attempts.length>0);}setCollapsedAnswers(prev=>prev[message.ordinal??1]===collapsed?prev:{...prev,[message.ordinal??1]:collapsed});}}><summary className="cursor-pointer text-sm text-sky-700">本题题解（点击展开／收起）· 已标记待加强</summary><ChatMessage message={{id:message.id,role:message.role,content:referenceFor(message.ordinal??1),status:message.status}} /></details> : <ChatMessage message={{id:message.id,role:message.role,content:message.content,status:message.status}} />}
          {message.status!=='complete' && message.status!=='superseded' && <p className="text-sm text-amber-700">输出未完成；当前片段已保存，可以引用继续问。</p>}
          {plan?.attempts.find(a=>a.answerId===message.id)?.assisted && <p className="text-xs text-amber-700">已看过讲解／辅助重答，单独保留，不覆盖独立首次作答。</p>}
          <details className="text-xs text-slate-500"><summary>查看原文（可复制片段引用）</summary><pre className="mt-2 whitespace-pre-wrap break-words font-sans">{message.content}</pre></details><div className="flex flex-wrap gap-3 text-xs text-slate-500"><button disabled={busy} onClick={()=>{setReference(message.id);setFragment(null);autoScroll.current=true;end.current?.scrollIntoView();}}>引用继续问</button>
            <button disabled={busy} onClick={()=>{const text=window.prompt('粘贴要引用的连续原文片段（必须与原消息完全一致）',window.getSelection()?.toString()??'');if(!text)return;const offset=message.content.indexOf(text);if(offset<0){setError('未找到这个连续片段，请从原文复制后再试');return;}const start=Array.from(message.content.slice(0,offset)).length;setFragment({messageId:message.id,start,end:start+Array.from(text).length,revision:message.revision});setReference('');setCorrects('');}}>引用片段</button>
            {message.role==='user' && <button disabled={busy} onClick={()=>{setCorrects(message.id);setReference(message.id);setFragment(null);}}>追加纠正</button>}
            {['chat','followup','teaching'].includes(message.kind) && <><button disabled={busy} onClick={()=>editMessage(message.id,message.content)}>编辑</button><button disabled={busy} onClick={()=>editMessage(message.id,message.content,true)}>删除</button></>}
          </div>
        </article>)}
				{conversation?.deleted&&plan&&<section className="space-y-4"><p>对话原文已删除，下列为你选择保留的训练结果，不能再作为原消息回查。</p>{plan.attempts.map(a=><article key={a.id}><h3>第{a.ordinal}题 · {(a.assisted||a.reanswerOf)?'辅助／重答':'独立首次作答'}</h3><p>{plan.questions[a.ordinal-1]?.text}</p><p className="my-2 whitespace-pre-wrap">{a.answer}</p>{a.evaluation&&<EvaluationCard value={a.evaluation}/>}</article>)}</section>}
        {busy && partial && <ChatMessage message={{id:'stream',role:'assistant',content:partial,thinking}} isStreaming />}
        {busy && !partial && <p className="text-sm text-slate-500">正在处理，请稍候…</p>}
        {conversation?.review&&<section className="space-y-3 rounded-xl border bg-white p-4"><h2 className="font-semibold">本场训练总结</h2><p>完成{conversation.review.completed}题 · 独立首次有效回答{conversation.review.independent}次 · 辅助／重答{conversation.review.assisted}次 · 未完成评分{conversation.review.failed}次</p><p>看过答案{conversation.review.viewedAnswers??0}题 · 未评分直接推进{conversation.review.skipped??0}题</p>{conversation.review.needsReview?.map(item=><div key={item.ordinal} className="text-amber-700"><p>第{item.ordinal}题待加强：{item.reason}</p><p>{item.question}</p></div>)}{conversation.review.gaps.map((g,i)=><div key={i}><p>第{g.ordinal}题薄弱点：{g.point}</p><blockquote className="border-l-2 pl-3 text-sm text-slate-500">{g.quote}</blockquote></div>)}{!conversation.review.gaps.length&&<p>本场独立首次有效评价未记录具体薄弱点；看过答案的题仍列为待加强。</p>}</section>}
        {plan && <div className="flex flex-wrap gap-3 rounded-xl border bg-white p-4 text-sm">
          {!conversation?.deleted&&!ready&&!viewedAnswer && plan.status!=='completed' && <button disabled={busy} onClick={()=>mutate('learn')}>查看答案（本题将标记待加强）</button>}
          {!conversation?.deleted&&!reviewing&&viewedAnswer && plan.status!=='completed' && <button disabled={busy} onClick={returnToAnswer}>收起题解，自己作答</button>}
          {!conversation?.deleted&&lastFailed&&!ready && <button disabled={busy} onClick={()=>send(lastFailed.answer,lastFailed)}>重试原回答评分</button>}
					{!conversation?.deleted&&plan.attempts.filter(a=>a.errorCode==='assessment_legacy_invalid'&&a.id!==lastFailed?.id).map(a=><button key={a.id} disabled={busy} onClick={()=>send(a.answer,a)}>重新评分第{a.ordinal}题原回答</button>)}
					{plan.attempts.filter(a=>a.gradingHistory?.length).map(a=><details key={a.id}><summary>第{a.ordinal}题评分修订记录（当前第{a.gradingRevision}版）</summary>{a.gradingHistory?.map((h,i)=><p key={i}>旧第{h.revision}版 · {h.error||'历史评价已替代，不计入当前成绩'}{h.evaluation?` · 原分数 ${h.evaluation.correctness}/${h.evaluation.coverage}/${h.evaluation.explanation}`:''}</p>)}</details>)}
          {!conversation?.deleted&&!reviewing&&ready && plan.status!=='completed' && <button disabled={busy} onClick={()=>{setReanswer(true);setPracticeInputMode('answer');}}>重答本题（保留首次结果）</button>}
          {(plan.status==='completed'||conversation?.deleted) && <><p className="w-full">首次独立回答与辅助重答分别保存，薄弱点可用于复测。</p><button disabled={busy} onClick={()=>mutate('retest',{original:false})}>薄弱点变式复测</button><button disabled={busy} onClick={()=>mutate('retest',{original:true})}>薄弱点原题重答</button></>}
          {plan.sourceConversation && <button disabled={busy} onClick={()=>open(plan.sourceConversation!)}>回看复测来源</button>}
        </div>}
        <div ref={end} />
      </div>
    </div>
    <div className="px-5 text-center text-xs text-slate-500">{reference ? '已引用选定消息；' : ''}{fragment?'已选定原文片段；':''}{corrects ? '本次输入将追加纠正；' : ''}{isAnswer ? (viewedAnswer?'已看答案，本次为辅助作答；待加强标记保留':reanswer?'本次为重答，保留原回答':'本次提交独立作答，评分不会读取后续讲解') : '自由提问／教学追问，不增加完成题数'}{(reference||fragment||corrects||reanswer)&&<button onClick={()=>{setReference('');setFragment(null);setCorrects('');setReanswer(false);}}> 取消</button>}</div>
    <ChatInput placeholder={isAnswer?(viewedAnswer?"收起题解后，尝试用自己的话回答本题…":"输入你对本题的回答…"):(plan?"对本题题解有什么疑问？":"自由提问，例如系统设计、Agent 或 Workflow…")} onSend={text=>void send(text)} onAudioAnswer={audio} disabled={busy||conversation?.deleted} isTranscribing={transcribing} value={draft} onChange={setDraft} keepDraft />
  </div>;
}





