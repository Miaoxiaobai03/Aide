'use client';
import { useEffect, useRef, useState } from 'react';
import { createClientAnswerId } from '@/lib/client-answer-id';
import { trainingRequest, type RetestPlan, type RetestTask } from '@/lib/training-client';
import { EvaluationView, TRAINING_STATUS } from './WeaknessPanel';

interface Draft { text: string; id: string }
const draftKey = (planId: string, taskId: string) => `offerpilot.retest.draft.${planId}.${taskId}`;
function readDraft(plan: RetestPlan, task: RetestTask): Draft { const existing = plan.attempts.find(a => a.taskId === task.id); if (existing) return { text: existing.answer.text, id: existing.id }; try { const value = JSON.parse(localStorage.getItem(draftKey(plan.id, task.id)) ?? 'null'); if (typeof value?.text === 'string' && typeof value?.id === 'string') return value; } catch {} return { text: '', id: `attempt-${createClientAnswerId()}` }; }
export function RetestPanel({ plan, onPlan, onBack, onSource }: { plan: RetestPlan; onPlan: (plan: RetestPlan) => void; onBack: () => void; onSource: (id: string) => void }) {
  const [batch, setBatch] = useState(0); const [taskId, setTaskId] = useState(''); const [draft, setDraft] = useState<Draft>({ text: '', id: '' }); const [busy, setBusy] = useState(false); const [error, setError] = useState('');
  const generation = useRef(0);
  const tasks = plan.tasks.filter(t => t.batch === batch); const task = tasks.find(t => t.id === taskId) ?? tasks.find(t => !plan.attempts.some(a => a.taskId === t.id && a.status === 'succeeded')) ?? tasks[0];
  const attempt = plan.attempts.find(a => a.taskId === task?.id); const allReady = tasks.every(t => t.status === 'ready');
  useEffect(() => { setTaskId(''); setBatch(0); setError(''); setBusy(false); generation.current++; }, [plan.id]);
  useEffect(() => { if (task) setDraft(readDraft(plan, task)); }, [plan.id, task?.id, attempt?.id]);
  useEffect(() => { if (task && draft.id) { try { localStorage.setItem(draftKey(plan.id, task.id), JSON.stringify(draft)); } catch { /* Backend retains submitted inputs even if local draft storage is unavailable. */ } } }, [plan.id, task?.id, draft]);
  async function action(name: string) { setBusy(true); setError(''); const current = generation.current; try { const result = await trainingRequest<RetestPlan>({}, { action: name, planId: plan.id, batch }); if (current === generation.current) onPlan(result); } catch (e) { if (current === generation.current) setError((e as Error).message); } finally { if (current === generation.current) setBusy(false); } }
  async function refresh() { setBusy(true); setError(''); try { onPlan(await trainingRequest<RetestPlan>({ resource: 'plan', id: plan.id })); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function submit() { if (!task || !draft.text.trim()) return; setBusy(true); setError(''); try { onPlan(await trainingRequest<RetestPlan>({}, { action: 'answer_retest', planId: plan.id, taskId: task.id, attemptId: draft.id, answer: { text: draft.text, inputMode: 'text' }, retry: !!attempt })); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  useEffect(() => {
    if (plan.status !== 'queued' && plan.status !== 'preparing' && !plan.attempts.some(a => a.status === 'running')) return;
    let active = true; let fetching = false;
    const timer = window.setInterval(async () => { if (fetching) return; fetching = true; try { const latest = await trainingRequest<RetestPlan>({ resource: 'plan', id: plan.id }); if (active) onPlan(latest); } catch (e) { if (active) setError((e as Error).message); } finally { fetching = false; } }, 3000);
    return () => { active = false; window.clearInterval(timer); };
  }, [plan.id, plan.status, plan.attempts.some(a => a.status === 'running')]);
  return <section className="space-y-4"><div className="flex flex-wrap gap-3"><button disabled={busy} onClick={onBack}>← 返回历史</button><button disabled={busy} onClick={() => onSource(plan.sourceId)}>查看来源与历次对比</button><button disabled={busy} onClick={refresh}>刷新状态／恢复记录</button></div><h3 className="text-lg font-semibold">{plan.mode === 'variant' ? '变式题复测' : '原题重答'} · {TRAINING_STATUS[plan.status]}</h3><p>{plan.tasks.length} 道任务，已完成 {plan.attempts.filter(a => a.status === 'succeeded').length} 道。题目、材料和标准已固定。</p>
    {error && <p className="text-red-600" role="alert">{error}；输入已保留。若提交结果不确定，请先刷新状态。</p>}{plan.error && <p role="alert">{plan.error}</p>}
    {(plan.status === 'preparing' || plan.status === 'queued') && <p>正在后台准备本批题目；请刷新查看结果。若服务中断，可在处理租约到期（最长4分钟）后重试。</p>}
    <div className="flex flex-wrap gap-3"><select aria-label="复测批次" disabled={busy} value={batch} onChange={e => { setBatch(Number(e.target.value)); setTaskId(''); }}>{Array.from({ length: Math.ceil(plan.tasks.length / 20) }, (_, i) => <option key={i} value={i}>第 {i + 1} 批</option>)}</select>{!allReady && <button disabled={busy || plan.status === 'paused'} onClick={() => action('prepare_retest')}>准备／重试本批题目</button>}{plan.status !== 'completed' && <button disabled={busy} onClick={() => action(plan.status === 'paused' ? 'resume_retest' : 'pause_retest')}>{plan.status === 'paused' ? '继续复测' : '暂停复测'}</button>}</div>
    {!allReady && <p>本批题目尚未全部准备好，准备完成后可回答。</p>}
    {plan.status === 'ready' && plan.tasks.some(t => t.status !== 'ready') && <p>已有批次可练习，其他批次仍待准备。</p>}
    <div className="flex flex-wrap gap-2">{tasks.map((t, i) => <button key={t.id} disabled={busy} onClick={() => setTaskId(t.id)} className={`rounded border px-3 py-2 ${task?.id === t.id ? 'border-blue-500' : ''}`}>{batch * 20 + i + 1} · {plan.attempts.some(a => a.taskId === t.id && a.status === 'succeeded') ? '已完成' : TRAINING_STATUS[t.status]}</button>)}</div>
    {task && <article className="space-y-3 rounded-xl border bg-white p-4"><h4>{task.status === 'ready' ? task.question.text : '题目待准备'}</h4>{task.targets.map(t => <div key={t.id}><strong>{t.label}</strong>{t.criteria.map((c, i) => <p key={i}>验收：{c}</p>)}</div>)}{task.error && <p role="alert">{task.error}</p>}
      {attempt ? <><EvaluationView observation={{ planId: plan.id, task, attempt, materialVersion: plan.materialVersion, scoringVersion: plan.scoringVersion }} />{attempt.status !== 'succeeded' && <button disabled={busy || plan.status === 'paused' || !allReady} onClick={submit}>查询／重试这条已保存回答</button>}<p>已提交回答不可原地修改；新的回答请回到来源新建复测。</p></> : <><label className="block">你的回答<textarea className="mt-2 block min-h-40 w-full rounded border p-3" value={draft.text} disabled={busy || !allReady || plan.status === 'paused'} onChange={e => setDraft(prev => ({ ...prev, text: e.target.value }))} /></label><button disabled={busy || !allReady || plan.status === 'paused' || !draft.text.trim()} onClick={submit}>{busy ? '正在评价…' : '提交回答并评价'}</button></>}
    </article>}
    {plan.status === 'completed' && <p>本计划已完成。答错也会保存为有效尝试；要再次练习，请从来源薄弱点创建新计划。</p>}
  </section>;
}
