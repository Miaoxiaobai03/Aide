'use client';
import {ChatInput} from './ChatInput';

// This component is remounted for each question; recorder and composition
// state cannot survive a page switch. The parent owns persisted drafts.
export function QuestionComposer({mode,viewed,hasAttempts,value,onChange,onSend,onAudioAnswer,transcribing,disabled}:{mode:'answer'|'followup';viewed:boolean;hasAttempts:boolean;value:string;onChange:(value:string)=>void;onSend:(value:string)=>void;onAudioAnswer:(blob:Blob,name?:string)=>Promise<void>;transcribing:boolean;disabled:boolean}){
 return <>
  <p className="px-5 py-2 text-center text-xs text-slate-500">{mode==='answer'?(viewed?'本次提交辅助作答，待加强标记保留':hasAttempts?'本次新增重答，保留以前回答':'本次提交本题独立作答'):'本题教学追问，不增加完成题数'}</p>
  <ChatInput value={value} onChange={onChange} onSend={onSend} onAudioAnswer={onAudioAnswer} isTranscribing={transcribing} disabled={disabled} keepDraft preserveWhitespace placeholder={mode==='answer'?'输入你对本题的回答…':'继续追问本题…'}/>
 </>;
}
