export interface CoachMessage {
  id: string; seq: number; revision: number; role: 'user' | 'assistant'; kind: string; content: string; status: string;
  ordinal?: number; questionId?: string; submissionId?: string; at: string;
}
export interface CoachEvaluation {
	version?:string;status?:string;method?:string;answerQuote?:string;
  correctness: number; coverage: number; explanation: number; strengths: string[];
  gaps: { point: string; quote: string; reason: string }[]; advice: string;
}
export interface CoachAttempt {
  id: string; ordinal: number; answerId: string; answer: string; status: string; assisted: boolean;
  reanswerOf?: string; evaluation?: CoachEvaluation; error?: string;
	errorCode?:string;gradingRevision?:number;gradingHistory?:{revision:number;evaluation?:CoachEvaluation;error?:string;errorCode?:string;model?:string;at:string}[];
}
export interface CoachConversation {
 practiceId?: string; pageOrdinal?: number;
	deleted?: boolean; trainingRetained?: boolean;
  id: string; mode: string; title: string; version: number; messages: CoachMessage[];
	questionGroups?: {sessionId:string;questionId:string;ordinal:number;index:string;title:string;archived:boolean;viewedAnswer:boolean;state:string;gaps:string[];messageIds:string[]}[];
  review?: { completed: number; independent: number; assisted: number; failed: number; viewedAnswers: number; skipped: number; needsReview: {ordinal:number;question:string;reason:string}[]; gaps: { ordinal: number; point: string; quote: string; answerId: string }[] };
  plan?: { questions: { id: string; text: string; source: string; hash: string; reference?: string; referenceMarkdown?: string }[];
    current: number; discussionOrdinal?:number; status: string; taught: Record<string, boolean>; viewedAnswers?: Record<string, boolean>; skipped?: Record<string, boolean>; attempts: CoachAttempt[]; sourceConversation?: string; };
}
export interface CoachListing { id: string; title: string; mode: string; updatedAt: string; completed: number; target: number }
export class CoachRequestError extends Error {
  constructor(message:string,public status:number,public code?:string){super(message);this.name='CoachRequestError';}
}
export async function coachRequest<T>(query: Record<string, string> = {}, body?: Record<string, unknown>, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api/coach?${new URLSearchParams(query)}`, { method: body ? 'POST' : 'GET', cache: 'no-store', signal,
    ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) });
  const value = await response.json();
  if (!response.ok) throw new CoachRequestError(typeof value.error === 'string' ? value.error : value.error?.message ?? '问答请求失败',response.status,value.error?.code);
  return value as T;
}

export interface PracticePageInfo {id:string;questionId:string;ordinal:number;question:string;state:string;viewedAnswer:boolean;graded:boolean;visited:boolean;busy:boolean}
export interface PracticeInfo {id:string;updatedAt:string;title:string;version:number;ended:boolean;completed:number;sourceConversation?:string;pages:PracticePageInfo[]}
export interface PracticePageView {practice:PracticeInfo;conversation:CoachConversation}


// Page reads contain only the selected question conversation. Directories carry no reference bodies.
export function readPracticeDirectory(practiceId:string,signal?:AbortSignal) {
 return coachRequest<PracticeInfo>({resource:'directory',id:practiceId},undefined,signal);
}
export function readPracticePage(practiceId:string,questionId:string,signal?:AbortSignal) {
 return coachRequest<PracticePageView>({resource:'page',id:practiceId,questionId},undefined,signal);
}
export function visitPracticePage(practiceId:string,questionId:string,signal?:AbortSignal) {
 return coachRequest<PracticePageView>({}, {action:'visitpage',practiceId,questionId},signal);
}
export function createPracticePages(count:number,creationId:string,signal?:AbortSignal) {
 return coachRequest<CoachConversation>({}, {action:'practice',count,submissionId:creationId},signal);
}
