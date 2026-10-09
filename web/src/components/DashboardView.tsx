'use client';
import { useEffect, useState } from 'react';
import { RadarChart } from './RadarChart';
import { trainingRequest, type TrainingStats, type HistoryPage } from '@/lib/training-client';
export const SCORE_LABELS: Record<string, string> = { correctness: '正确性', depth: '深度', specificity: '具体性', ownership: '职责边界', metrics: '指标口径', tradeoffs: '方案取舍', coverage: '要点覆盖', explanation: '解释清晰度' };
export const AREA_LABELS: Record<string, string> = { mixed: '综合训练', knowledge: '知识训练', projects: '项目训练', unclassified: '未分类' };
export function DashboardView({onOpenPractice}:{onOpenPractice?:(id:string)=>void}={}) {
  const [data, setData] = useState<TrainingStats | null>(null);
  const [history,setHistory]=useState<HistoryPage|null>(null);
  const [error, setError] = useState('');
  const [groupKey, setGroupKey] = useState('');
  async function load() { setError(''); try { const [stats,records]=await Promise.all([trainingRequest<TrainingStats>({resource:'stats'}),trainingRequest<HistoryPage>({resource:'history'})]);setData(stats);setHistory(records); } catch (e) { setError((e as Error).message); } }
  useEffect(() => { void load(); }, []);
  if (error) return <div className="p-6" role="alert">{error} <button onClick={load}>重试</button></div>;
  if (!data) return <div className="p-6">正在读取训练统计…</div>;
  const group = data.groups.find(g => g.key === groupKey) ?? data.groups[0];
  const dimensions = group ? Object.entries(group.scores).map(([key, value]) => ({ label: SCORE_LABELS[key] ?? key, value, max: 5 })) : [];
  return <div className="flex-1 overflow-y-auto p-6"><div className="mx-auto max-w-5xl space-y-6">
    <h2 className="text-lg font-semibold">真实训练统计</h2>
    <div className="grid grid-cols-2 gap-4 md:grid-cols-4">{[['训练场次', data.sessions], ['已完成', data.completed], ['已提交回答', data.answered], ['可见有效评价', data.evaluated]].map(([label, value]) => <div key={label} className="rounded-xl border bg-white p-4"><p>{label}</p><strong className="text-2xl">{value}</strong></div>)}</div>
    {data.hiddenEvaluations > 0 && <p>另有 {data.hiddenEvaluations} 条延迟反馈，训练结束后公开。</p>}
    {!!data.practiceNeedsReview && <p className="text-amber-700">八股训练中有 {data.practiceNeedsReview} 题看过答案，已标记待独立复测加强；不作为零分计入雷达。</p>}
    {data.invalidEvaluations > 0 && <p>有 {data.invalidEvaluations} 条评价不完整，未纳入评分。</p>}
    {data.retestFailures > 0 && <p>有 {data.retestFailures} 条复测评价失败，回答已保存，未计分；可在训练历史重试。</p>}
    {data.unassigned > 0 && <p>有 {data.unassigned} 场旧训练尚未导入，请前往训练历史确认归属。</p>}
    {data.warnings.map(w => <p key={w.id} role="alert">{w.id}：{w.reason}</p>)}
    <div className="rounded-xl border bg-white p-5"><h3>回答表现</h3><p className="text-sm text-slate-500">按评分版本、题型、难度分别统计，原始量表为 1—5 分。知识题的职责、指标分数仅供回看，不用于判断知识薄弱点。</p>
      {group ? <><select aria-label="评分分组" value={group.key} onChange={e => setGroupKey(e.target.value)} className="my-3 border p-2">{data.groups.map(g => <option key={g.key} value={g.key}>{g.version} / {g.kind} / {g.difficulty}{g.key.includes('/assisted')?' / 辅助作答':g.key.includes('/independent')?' / 独立作答':''}（{g.count} 题）</option>)}</select><RadarChart dimensions={dimensions} />{dimensions.map(d => <p key={d.label}>{d.label}：{d.value.toFixed(2)} / 5</p>)}</> : <p className="mt-4">暂无可见有效评价，暂不展示分数。</p>}
    </div>
    {onOpenPractice&&<div className="rounded-xl border bg-white p-5"><h3>选择真实八股训练记录</h3>{history?.items.filter(item=>item.type==='practice').map(item=><button key={item.id} className="my-2 block text-left text-sky-700" onClick={()=>onOpenPractice(item.id)}>{new Date(item.startedAt).toLocaleString()} · 有效评价 {item.evaluated} 题 · 待加强 {item.needsReview??0} 题 → 查看本场题目、分析与复测</button>)}</div>}
    <div className="rounded-xl border bg-white p-5"><h3>训练范围</h3>{Object.entries(data.areas).map(([area, count]) => <p key={area}>{AREA_LABELS[area] ?? area}：{count} 场</p>)}<p className="text-sm text-slate-500">这是训练类型分布，不代表七个知识领域的能力分数。具体薄弱点与复测请查看训练历史。</p></div>
  </div></div>;
}
