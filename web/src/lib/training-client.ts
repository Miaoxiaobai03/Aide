import type { InterviewSnapshot, InterviewReport } from '@/types/interview';

export interface HistoryItem { id: string; type: string; state: string; startedAt: string; answered: number; evaluated: number; feedbackHidden: boolean; reportStatus: string; area: string; scoringVersion: string; needsReview?: number }
export interface HistoryPage { items: HistoryItem[]; nextCursor: string; warnings: { id: string; reason: string }[]; unassigned: string[]; profileId: string }
export interface HistoryDetail { item: HistoryItem; snapshot: InterviewSnapshot; report?: InterviewReport }
export interface TrainingStats { sessions: number; completed: number; answered: number; evaluated: number; hiddenEvaluations: number; invalidEvaluations: number; retestFailures: number; practiceNeedsReview?: number; unassigned: number; areas: Record<string, number>; warnings: { id: string; reason: string }[]; groups: { key: string; version: string; kind: string; difficulty: string; count: number; scores: Record<string, number> }[] }
export async function trainingRequest<T>(query: Record<string, string> = {}, body?: unknown): Promise<T> {
  const response = await fetch(`/api/training?${new URLSearchParams(query)}`, { method: body === undefined ? 'GET' : 'POST', headers: { 'Content-Type': 'application/json' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }), cache: 'no-store' });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error?.message ?? '训练请求失败');
  return data as T;
}

export interface RetestTarget { id: string; label: string; criteria: string[] }
export interface WeaknessSource { interviewId: string; questionId: string; question: string; answer: string; feedback: string; evidence: { sourceId: string; anchorId: string; locator: string; quote: string }[]; at: string }
export interface Weakness { id: string; label: string; scope: string; issue: string; status: string; criteria: string[]; sources: WeaknessSource[]; revision: number; updatedAt: string }
export interface WeaknessPage { items: Weakness[]; state: string; reason?: string }
export interface RetestTask { id: string; targets: RetestTarget[]; originalQuestionId: string; question: { id: string; text: string; difficulty: string }; status: string; error?: string; batch: number; criteriaVersion: string; bindingReason?: string }
export interface CriterionResult { targetId: string; criterion: string; status: 'met' | 'unmet' | 'unassessed'; reason: string; answerQuote: string }
export interface RetestEvaluation { assessment: { correctness: number; depth: number; specificity: number; ownership: number; metrics: number; tradeoffs: number; strengths: string[]; gaps: string[] }; results: CriterionResult[] }
export interface RetestAttempt { id: string; taskId: string; answer: { text: string; inputMode: 'text' | 'voice'; durationMs?: number }; status: string; error?: string; createdAt: string; leaseUntil: string; retries: number; evaluation?: RetestEvaluation }
export interface RetestPlan { id: string; sourceId: string; mode: string; createdAt: string; status: string; error?: string; version: number; materialVersion: string; scoringVersion: string; tasks: RetestTask[]; attempts: RetestAttempt[]; leaseUntil: string }
export interface AttemptObservation { planId: string; task: RetestTask; attempt: RetestAttempt; materialVersion: string; scoringVersion: string }
export interface AttemptComparison { left: AttemptObservation; right: AttemptObservation; scoreComparable: boolean; scoreDelta?: number; reason: string }
