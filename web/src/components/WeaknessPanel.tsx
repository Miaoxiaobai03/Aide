'use client';
import { useEffect, useRef, useState } from 'react';
import { createClientAnswerId } from '@/lib/client-answer-id';
import { trainingRequest, type Weakness, type WeaknessPage, type RetestPlan, type AttemptObservation, type AttemptComparison } from '@/lib/training-client';

export const TRAINING_STATUS: Record<string, string> = { candidate: '待确认', confirmed: '待练习', not_applicable: '已标记不适用', needs_practice: '仍需练习', passed_once: '本次通过', succeeded: '评价完成', failed: '评价失败', running: '评价处理中', pending: '待准备', queued: '准备已排队', ready: '可练习', preparation_failed: '准备失败', preparing: '正在准备', completed: '已完成', paused: '已暂停' };
export function EvaluationView({ observation }: { observation: AttemptObservation }) {
  return <article className="space-y-2 rounded border p-3"><p>{new Date(observation.attempt.createdAt).toLocaleString()} · {TRAINING_STATUS[observation.attempt.status] ?? observation.attempt.status}</p><h4>{observation.task.question.text}</h4><p className="whitespace-pre-wrap">{observation.attempt.answer.text}</p>{observation.attempt.error && <p role="alert">{observation.attempt.error}</p>}{observation.attempt.evaluation?.results.map(r => <p key={`${r.targetId}/${r.criterion}`}><strong>{r.status === 'met' ? '满足' : r.status === 'unmet' ? '未满足' : '待核对'}</strong> · {r.criterion}：{r.reason}{r.answerQuote && <span>（回答原文：“{r.answerQuote}”）</span>}</p>)}{observation.attempt.evaluation?.results.length === 0 && <p>初次训练反馈：{observation.attempt.evaluation.assessment.gaps?.join('；') || '见原始评价'}。这次尚未使用复测标准评分。</p>}</article>;
}
function AttemptTimeline({ sourceId, weaknessId, onOpenPlan }: { sourceId: string; weaknessId: string; onOpenPlan: (id: string) => void }) {
  const [items, setItems] = useState<AttemptObservation[]>([]);
  const [error, setError] = useState('');
  const [left, setLeft] = useState(''); const [right, setRight] = useState('');
  const [comparison, setComparison] = useState<AttemptComparison | null>(null);
  async function load() { setError(''); try { const data = await trainingRequest<AttemptObservation[]>({ resource: 'attempts', sourceId, id: weaknessId }); setItems(data); setLeft(data[0]?.attempt.id ?? ''); setRight(data.at(-1)?.attempt.id ?? ''); setComparison(null); } catch (e) { setError((e as Error).message); } }
  useEffect(() => { void load(); }, [sourceId, weaknessId]);
  async function compare() { setError(''); try { setComparison(await trainingRequest<AttemptComparison>({ resource: 'compare', sourceId, id: weaknessId, left, right })); } catch (e) { setError((e as Error).message); } }
  return <div className="space-y-3"><button onClick={load}>刷新历次回答</button>{error && <p role="alert">{error}</p>}{items.length > 1 && <div className="flex flex-wrap gap-2"><select aria-label="对比左侧" value={left} onChange={e => { setLeft(e.target.value); setComparison(null); }}>{items.map((o, i) => <option key={o.attempt.id} value={o.attempt.id}>第 {i + 1} 次 · {TRAINING_STATUS[o.attempt.status]}</option>)}</select><select aria-label="对比右侧" value={right} onChange={e => { setRight(e.target.value); setComparison(null); }}>{items.map((o, i) => <option key={o.attempt.id} value={o.attempt.id}>第 {i + 1} 次 · {TRAINING_STATUS[o.attempt.status]}</option>)}</select><button disabled={left === right} onClick={compare}>对比两次回答</button></div>}
    {comparison && <><p>{comparison.reason}{comparison.scoreComparable && `；回答表现分变化：${comparison.scoreDelta! > 0 ? '+' : ''}${comparison.scoreDelta} 分（100分制）`}</p><div className="grid gap-3 md:grid-cols-2"><EvaluationView observation={comparison.left} /><EvaluationView observation={comparison.right} /></div></>}
    {items.map(o => <div key={o.attempt.id}><EvaluationView observation={o} />{!o.attempt.id.startsWith('initial-') && <button onClick={() => onOpenPlan(o.planId)}>打开这次复测</button>}</div>)}
  </div>;
}
function WeaknessCard({ item, sourceId, selected, disabled, onSelect, onSave, onOpenPlan }: { item: Weakness; sourceId: string; selected: boolean; disabled: boolean; onSelect: () => void; onSave: (item: Weakness, label: string, criteria: string[], status: string) => Promise<void>; onOpenPlan: (id: string) => void }) {
  const [label, setLabel] = useState(item.label); const [criteria, setCriteria] = useState(item.criteria.join('\n')); const [timeline, setTimeline] = useState(false);
  useEffect(() => { setLabel(item.label); setCriteria(item.criteria.join('\n')); }, [item.label, item.criteria.join('\n'), item.revision]);
  return <article className="space-y-3 rounded-xl border bg-white p-4"><div className="flex gap-2"><input type="checkbox" aria-label={`选择 ${item.label}`} checked={selected} disabled={disabled || item.status === 'candidate' || item.status === 'not_applicable'} onChange={onSelect} /><strong>{item.label}</strong><span>{TRAINING_STATUS[item.status]}</span></div>
    <label className="block">薄弱点名称<input className="block w-full rounded border p-2" value={label} onChange={e => setLabel(e.target.value)} disabled={disabled} /></label><label className="block">复测标准（每行一条，可先修正再确认）<textarea className="block w-full rounded border p-2" value={criteria} onChange={e => setCriteria(e.target.value)} disabled={disabled} /></label>
    <div className="flex gap-3">{[['confirmed', '确认／保存修正'], ['candidate', '取消确认'], ['not_applicable', '不适用']].map(([status, text]) => <button key={status} disabled={disabled} onClick={() => onSave(item, label, criteria.split('\n').map(c => c.trim()).filter(Boolean), status)}>{text}</button>)}</div>
    <details><summary>原题、回答与评价依据（{item.sources.length} 条）</summary>{item.sources.map((s, i) => <div key={i} className="mt-2 border-l pl-3"><p>{s.question}</p><p className="whitespace-pre-wrap">原回答：{s.answer}</p><p>原反馈：{s.feedback}</p>{s.evidence.map(e => <p key={e.anchorId}>来源：{e.locator} · {e.quote}</p>)}</div>)}</details>
    <button onClick={() => setTimeline(v => !v)}>{timeline ? '收起历次回答' : '查看历次回答与对比'}</button>{timeline && <AttemptTimeline sourceId={sourceId} weaknessId={item.id} onOpenPlan={onOpenPlan} />}
  </article>;
}
export function WeaknessPanel({ sourceId, onPlan, onOpenPlan }: { sourceId: string; onPlan: (plan: RetestPlan) => void; onOpenPlan: (id: string) => void }) {
  const [data, setData] = useState<WeaknessPage | null>(null); const [selected, setSelected] = useState<string[]>([]); const [mode, setMode] = useState('variant'); const [error, setError] = useState(''); const [busy, setBusy] = useState(false);
  const creation = useRef<{ hash: string; id: string } | null>(null);
  async function load() { setError(''); setBusy(true); try { setData(await trainingRequest<WeaknessPage>({ resource: 'weaknesses', sourceId })); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  useEffect(() => { setData(null); setSelected([]); creation.current = null; void load(); }, [sourceId]);
  async function save(item: Weakness, label: string, criteria: string[], status: string) { setError(''); setBusy(true); try { setData(await trainingRequest<WeaknessPage>({}, { action: 'edit_weakness', sourceId, id: item.id, revision: item.revision, label, criteria, status })); setSelected(prev => prev.filter(id => id !== item.id)); creation.current = null; } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  async function create() { setError(''); setBusy(true); const hash = JSON.stringify([sourceId, mode, [...selected].sort()]); if (creation.current?.hash !== hash) creation.current = { hash, id: `plan-${createClientAnswerId()}` }; try { onPlan(await trainingRequest<RetestPlan>({}, { action: 'create_retest', sourceId, mode, ids: selected, planId: creation.current.id })); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }
  return <section className="space-y-4"><h3 className="font-semibold">薄弱点与针对性复测</h3><p className="text-sm text-slate-500">先核对来源和复测标准，再确认并选择。一次通过只代表本次回答达标。</p>{error && <p role="alert">{error} <button disabled={busy} onClick={load}>重新读取</button></p>}{!data && !error && <p>正在整理已有评价…</p>}{data?.reason && <p>{data.reason}</p>}
    {data?.items.map(item => <WeaknessCard key={item.id} item={item} sourceId={sourceId} selected={selected.includes(item.id)} disabled={busy} onSelect={() => setSelected(prev => prev.includes(item.id) ? prev.filter(id => id !== item.id) : [...prev, item.id])} onSave={save} onOpenPlan={onOpenPlan} />)}
    {data && data.items.length > 0 && <div className="flex flex-wrap gap-3"><select aria-label="复测题型" value={mode} onChange={e => { setMode(e.target.value); creation.current = null; }} disabled={busy}><option value="variant">同能力变式题（默认）</option><option value="original">原题重答</option></select><button disabled={busy || selected.length === 0} onClick={create}>为 {selected.length} 个选中目标创建复测</button><span>变式题每项目标一题；原题模式合并同题，超过20题自动分批。</span></div>}
  </section>;
}
