'use client';
import {ChatMessage} from './ChatMessage';
import {MarkdownContent} from './MarkdownContent';
import type {CoachConversation,CoachEvaluation} from '@/lib/coach-client';
type Fragment={messageId:string;start:number;end:number;revision:number};
function Feedback({value}:{value:CoachEvaluation}){
 return <section className="space-y-2 rounded-xl border border-sky-200 bg-white p-4">
  <p>正确性 {value.correctness}/5 · 要点覆盖 {value.coverage}/5 · 解释清晰度 {value.explanation}/5</p>
  {value.method==='exact_frozen_reference'&&<p className="text-xs text-slate-500">与完整参考一致，内容评分满分；看过答案仍需独立复测。</p>}
  {value.strengths?.map((s,i)=><p key={i}>优势：{s}</p>)}
  {value.gaps?.map((g,i)=><div key={i}><p>薄弱点：{g.point} · {g.reason}</p><blockquote className="border-l-2 pl-3 text-sm text-slate-500">{g.quote}</blockquote></div>)}
  <p>建议：{value.advice}</p>
 </section>;
}
export function QuestionConversation({c,questionId,collapsed,busy,onCollapse,onReference,onFragment,onError}:{c:CoachConversation;questionId:string;collapsed:boolean;busy:boolean;onCollapse:(value:boolean)=>void;onReference:(id:string)=>void;onFragment:(fragment:Fragment)=>void;onError:(text:string)=>void}){
 const q=c.plan!.questions[0];const attempts=c.plan!.attempts;const reference=q.referenceMarkdown||q.reference||'';const teachingAvailable=!!c.plan!.taught?.[1];
 const latest=[...attempts].reverse().find(a=>a.status==='succeeded'&&a.evaluation);
 const independent=[...attempts].reverse().find(a=>a.status==='succeeded'&&!a.assisted&&a.evaluation);
 return <>
     {latest&&<div className="rounded-xl border bg-white p-3 text-sm"><p>最新有效内容评价{latest.assisted?'（看过答案后的辅助作答）':'（独立作答）'}：正确性 {latest.evaluation!.correctness}/5 · 覆盖 {latest.evaluation!.coverage}/5 · 清晰度 {latest.evaluation!.explanation}/5</p><p>最近有效独立评价：{independent?`正确性 ${independent.evaluation!.correctness}/5 · 覆盖 ${independent.evaluation!.coverage}/5 · 清晰度 ${independent.evaluation!.explanation}/5`:'暂无；辅助分数不代替独立掌握度'}</p></div>}
     {c.messages.map(m=><article key={m.id} className="space-y-2">
      {m.kind==='evaluation'?(()=>{try{return <Feedback value={JSON.parse(m.content) as CoachEvaluation}/>}catch{return <p>本条评价无法显示，请恢复本题。</p>}})():m.kind==='teaching'&&!m.submissionId&&m.content===q?.reference?
       <details open={!collapsed} onToggle={event=>{const close=!event.currentTarget.open;onCollapse(close)}}><summary className="cursor-pointer text-sm text-sky-700">本题题解（展开／收起）</summary><div className="rounded-xl bg-white p-4"><MarkdownContent>{reference}</MarkdownContent></div></details>:
       m.content.trim()?<ChatMessage message={{id:m.id,role:m.role,content:m.content,status:m.status}}/>:null}
      {m.status==='superseded'&&<p className="text-xs text-slate-500">历史评分版本，供回看；不作为当前统计。</p>}
      {m.status!=='complete'&&m.status!=='superseded'&&m.content.trim()&&<p className="text-xs text-amber-700">此条输出未完成，已保存的部分仍可查看。</p>}
      {!!m.content.trim()&&<div className="flex flex-wrap gap-3 text-xs text-slate-500">
       <details><summary className="cursor-pointer">查看原文</summary><pre className="mt-2 whitespace-pre-wrap break-words font-sans">{m.content}</pre></details>
       <button disabled={busy||!teachingAvailable} onClick={()=>{onReference(m.id)}}>引用本条追问</button>
       <button disabled={busy||!teachingAvailable} onClick={()=>{const text=window.prompt('粘贴本条消息中的连续原文片段',window.getSelection()?.toString()??'');if(!text)return;const offset=m.content.indexOf(text);if(offset<0){onError('未找到该原文片段，请重新复制');return}const start=Array.from(m.content.slice(0,offset)).length;onFragment({messageId:m.id,start,end:start+Array.from(text).length,revision:m.revision})}}>引用片段</button>
      </div>}
      {attempts.find(a=>a.answerId===m.id)?.assisted&&<p className="text-xs text-amber-700">看过答案后的作答，保留内容评分，待独立复测。</p>}
     </article>)}

 </>;
}
