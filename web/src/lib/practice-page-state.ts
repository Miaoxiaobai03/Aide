import type {CoachConversation} from './coach-client';

export function mergeQuestionSnapshot(previous:CoachConversation|undefined,next:CoachConversation,practiceId:string,questionId:string){
 if(next.practiceId!==practiceId||next.plan?.questions.length!==1||next.plan.questions[0].id!==questionId)return previous;
 return previous&&previous.version>next.version?previous:next;
}

export function validatePracticeEvent(event:Record<string,unknown>,practiceId:string,questionId:string,operationId:string,previousSequence:number):number{
 if(event.practiceId!==practiceId||event.questionId!==questionId||event.operationId!==operationId)throw new Error('回答归属不匹配，已停止显示');
 if(!Number.isInteger(event.sequence)||event.sequence!==previousSequence+1)throw new Error('回答事件不连续，请恢复本题查看已保存内容');
 return event.sequence as number;
}

// Delete only this practice's local state; another practice/free chat is retained.
export function clearPracticeStorage(storage:Storage,practiceId:string){
 const prefix='offerpilot.practice.'+practiceId+'.';const keys:string[]=[];
 for(let i=0;i<storage.length;i++){const key=storage.key(i);if(key?.startsWith(prefix))keys.push(key)}
 for(const key of keys)storage.removeItem(key);
 if(storage.getItem('offerpilot.coach.active')===practiceId)storage.removeItem('offerpilot.coach.active');
}
