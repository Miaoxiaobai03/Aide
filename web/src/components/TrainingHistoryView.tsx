'use client';
import { useEffect, useRef, useState } from 'react';
import { trainingRequest, type HistoryPage, type HistoryDetail, type RetestPlan } from '@/lib/training-client';
import { AREA_LABELS } from './DashboardView';
import { WeaknessPanel } from './WeaknessPanel';
import { RetestPanel } from './RetestPanel';
import type { InterviewResumeRequest } from './InterviewView';
export function TrainingHistoryView({ onResumeInterview, onOpenPractice }: { onResumeInterview?: (request: InterviewResumeRequest) => void; onOpenPractice?: (id: string) => void }) {
  const [page, setPage] = useState<HistoryPage | null>(null);
  const [detail, setDetail] = useState<HistoryDetail | null>(null);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [state, setState] = useState('');
  const [area, setArea] = useState('');
  const [kind, setKind] = useState('');
  const [plan, setPlan] = useState<RetestPlan | null>(null);
  const requests = useRef(0);
  function acceptPlan(value: RetestPlan) { setPlan(value); setDetail(null); try { localStorage.setItem('offerpilot.retest.active', value.id); } catch {} }
  async function load(cursor = '') { const seq = ++requests.current; setBusy(true); setError(''); try { const result = await trainingRequest<HistoryPage>({ resource: 'history', cursor, state, area, kind }); if (seq === requests.current) setPage(prev => cursor && prev ? { ...result, items: [...prev.items, ...result.items] } : result); } catch (e) { if (seq === requests.current) setError((e as Error).message); } finally { if (seq === requests.current) setBusy(false); } }
  useEffect(() => { void load(); }, [state, area, kind]);
  async function open(id: string) { const seq = ++requests.current; setBusy(true); setError(''); try { const value = await trainingRequest<HistoryDetail>({ resource: 'detail', id }); if (seq === requests.current) { setDetail(value); setPlan(null); try { localStorage.removeItem('offerpilot.retest.active'); } catch {} } } catch (e) { if (seq === requests.current) setError((e as Error).message); } finally { if (seq === requests.current) setBusy(false); } }
  async function openPlan(id: string) { const seq = ++requests.current; setBusy(true); setError(''); try { const value = await trainingRequest<RetestPlan>({ resource: 'plan', id }); if (seq === requests.current) acceptPlan(value); } catch (e) { if (seq === requests.current) setError((e as Error).message); } finally { if (seq === requests.current) setBusy(false); } }
  useEffect(() => { try { const id = localStorage.getItem('offerpilot.retest.active'); if (id) void openPlan(id); } catch {} }, []);
  function back() { setPlan(null); setDetail(null); try { localStorage.removeItem('offerpilot.retest.active'); } catch {} void load(); }
  async function importOld(id: string) { setBusy(true); setError(''); try { await trainingRequest({}, { action: 'import', ids: [id] }); await load(); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  return <div className="flex-1 overflow-y-auto p-6"><div className="mx-auto max-w-4xl space-y-4">
    <h2 className="text-lg font-semibold">训练历史与复习</h2>
    {error && <p role="alert" className="text-red-600">{error} <button disabled={busy} onClick={() => plan ? openPlan(plan.id) : detail ? open(detail.item.id) : load()}>重试</button></p>}
    {busy && <p role="status">正在读取…</p>}
    {plan ? <RetestPanel plan={plan} onPlan={acceptPlan} onBack={back} onSource={open} /> : detail ? <><button onClick={back}>← 返回列表</button><h3>{new Date(detail.item.startedAt).toLocaleString()} · {detail.snapshot.state === 'completed' ? '已完成' : '未完成'}</h3>
      {detail.snapshot.state !== 'completed' && detail.snapshot.currentQuestion && onResumeInterview && <button disabled={busy} onClick={() => onResumeInterview({ interviewId: detail.item.id, questionId: detail.snapshot.currentQuestion!.id })}>继续这场模拟面试</button>}
      {detail.snapshot.turns.map(turn => <article key={turn.question.id} className="space-y-2 rounded-xl border bg-white p-4"><h4>{turn.question.text}</h4><p className="whitespace-pre-wrap">我的回答：{turn.answer}</p>{detail.item.feedbackHidden ? <p>本场采用结束后反馈，评价尚未公开。</p> : turn.feedback.unavailable ? <p>{turn.feedback.summary}</p> : <><p>已有评价：{turn.feedback.score} / 100 · {turn.feedback.summary}</p><p>优势：{turn.feedback.strengths.join('；') || '暂无'}</p><p>差距：{turn.feedback.gaps.join('；') || '现有评价未记录差距'}</p>{turn.feedback.correction && <p>事实纠正：{turn.feedback.correction}</p>}<p>建议：{turn.feedback.coachTip}</p>{turn.feedback.evidenceRefs?.map(e => <p key={e.id}>依据：{e.label} · {e.excerpt}</p>)}</>}</article>)}
      {detail.report ? <section className="space-y-2 rounded-xl border bg-white p-4"><h3>已有报告 · {detail.report.overallScore} / 100</h3><p>{detail.report.summary}</p><p>优势：{detail.report.strengths.join('；')}</p><p>差距与风险：{detail.report.risks.join('；')}</p><p>练习建议：{detail.report.nextDrills.join('；')}</p><details><summary>报告维度与覆盖情况</summary>{detail.report.dimensions.map(d => <p key={d.key}>{d.label}：{d.score} / 100 · {d.summary}</p>)}{detail.report.jdCoverage.map((c, i) => <p key={i}>{c.requirement}：{c.status}</p>)}{detail.report.projectCoverage.map((c, i) => <p key={i}>{c.project}：{c.risks.join('；')}</p>)}</details></section> : <p>{detail.item.reportStatus === 'not_ready' ? '训练尚未结束。' : '报告尚未生成；原始题答已保留。'}</p>}
      <WeaknessPanel sourceId={detail.item.id} onPlan={acceptPlan} onOpenPlan={openPlan} />
    </> : <>
      <div className="flex flex-wrap gap-3"><select aria-label="完成状态" value={state} onChange={e => setState(e.target.value)}><option value="">全部状态</option><option value="completed">已完成</option><option value="awaiting_answer">未完成</option></select><select aria-label="训练范围" value={area} onChange={e => setArea(e.target.value)}><option value="">全部范围</option>{Object.entries(AREA_LABELS).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select><select aria-label="训练类型" value={kind} onChange={e => setKind(e.target.value)}><option value="">全部类型</option><option value="interview">模拟面试</option><option value="retest">薄弱点复测</option><option value="practice">八股练习</option></select><button disabled={busy} onClick={() => load()}>刷新</button></div>
      {page?.warnings.map(w => <p key={w.id} role="alert">{w.id}：{w.reason}</p>)}
      {page?.unassigned.map(id => <div key={id} className="rounded border border-amber-300 p-3">旧记录 {id} 归属未知。<button disabled={busy} onClick={() => importOld(id)}>确认这是我的训练并导入</button></div>)}
      {page && page.items.length === 0 && !error && <p>当前筛选下暂无训练记录。</p>}
      {page?.items.map(item => <button key={item.id} disabled={busy} onClick={() => item.type === 'practice' ? onOpenPractice?.(item.id) : item.type === 'retest' ? openPlan(item.id) : open(item.id)} className="block w-full rounded-xl border bg-white p-4 text-left">{new Date(item.startedAt).toLocaleString()} · {item.type === 'practice' ? '八股练习' : item.type === 'retest' ? '薄弱点复测' : '模拟面试'} · {item.state === 'completed' ? '已完成' : '未完成'}<p>{item.answered} 条回答 / {item.evaluated} 条可见有效评价 · {AREA_LABELS[item.area] ?? item.area}</p>{!!item.needsReview && <p className="text-amber-700">{item.needsReview}题看过答案，待独立复测加强</p>}</button>)}
      {page?.nextCursor && <button disabled={busy} onClick={() => load(page.nextCursor)}>加载更多</button>}
    </>}
  </div></div>;
}


