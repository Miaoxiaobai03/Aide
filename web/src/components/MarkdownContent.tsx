'use client';

import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

// The library's linear Mermaid flows can be read as ordered steps, without
// executing diagram scripts or converting arbitrary diagrams incorrectly.
function linearSteps(code: string): string[] | null {
  const lines = code.trim().split(/\r?\n/).map(line => line.trim()).filter(Boolean);
  if (!/^(?:flowchart|graph)\s+(?:LR|RL|TD|TB|BT)$/.test(lines[0] ?? '')) return null;
  const nodes = new Map<string, string>();
  const edges: [string, string][] = [];
  for (const line of lines.slice(1)) {
    const match = /^([\w]+)(?:\["([^"]+)"\])?\s*-->\s*([\w]+)(?:\["([^"]+)"\])?$/.exec(line);
    if (!match) return null;
    const [, from, fromLabel, to, toLabel] = match;
    for (const [id, label] of [[from, fromLabel], [to, toLabel]]) {
      if (label) { if (nodes.has(id) && nodes.get(id) !== label) return null; nodes.set(id, label); }
    }
    edges.push([from, to]);
  }
  if (!edges.length || edges.length !== nodes.size - 1) return null;
  const next = new Map<string, string>();
  const targets = new Set<string>();
  for (const [from, to] of edges) {
    if (next.has(from) || targets.has(to)) return null;
    next.set(from, to); targets.add(to);
  }
  const starts = [...nodes.keys()].filter(id => !targets.has(id));
  if (starts.length !== 1) return null;
  const steps: string[] = []; const visited = new Set<string>();
  let current: string | undefined = starts[0];
  while (current) {
    if (visited.has(current) || !nodes.has(current)) return null;
    visited.add(current); steps.push(nodes.get(current)!.replace(/\\n/g, '\n')); current = next.get(current);
  }
  return visited.size === nodes.size ? steps : null;
}

export function MarkdownContent({ children }: { children: string }) {
  return <ReactMarkdown remarkPlugins={[remarkGfm]} components={{
    pre({ children, node: _node, ...props }) { return <div className="overflow-x-auto"><pre {...props}>{children}</pre></div>; },
    code({ children, className, node: _node, ...props }) {
      const steps = className === 'language-mermaid' ? linearSteps(String(children)) : null;
      if (steps) return <code className="readable-flow"><span className="block font-semibold">流程步骤</span><span className="mt-2 block space-y-2">{steps.map((step, index) => <span key={index} className="flex gap-3"><span className="shrink-0">{index + 1}.</span><span className="whitespace-pre-wrap">{step}</span></span>)}</span></code>;
      return <code className={className} {...props}>{children}</code>;
    },
  }}>{children}</ReactMarkdown>;
}
