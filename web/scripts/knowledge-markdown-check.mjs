// Render the actual component and the reported knowledge question, without a
// model or browser. This verifies semantic HTML, not pixel/interaction layout.
import assert from 'node:assert/strict';
import { readFile, writeFile, unlink } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import ts from 'typescript';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';

const generated = resolve(`.knowledge-markdown-check-${process.pid}.mjs`);
try {
  const source = await readFile(resolve('src/components/MarkdownContent.tsx'), 'utf8');
  const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText;
  await writeFile(generated, compiled, 'utf8');
  const { MarkdownContent } = await import(pathToFileURL(generated).href);
  const markdown = await readFile(resolve('../knowledge/02-tool-management/index.md'), 'utf8');
  const heading = '## Q：没有 MCP 之前大模型调用工具走的是什么流程？MCP 本身有什么缺点或者挑战？';
  const start = markdown.indexOf(heading); assert.ok(start >= 0);
  const end = markdown.indexOf('\n## Q', start + heading.length);
  const reference = markdown.slice(start + heading.length, end < 0 ? undefined : end);
  const render = text => renderToStaticMarkup(React.createElement(MarkdownContent, null, text));
  const html = render(reference);
  assert.ok(html.includes('<table>') && html.includes('<th>维度</th>'));
  assert.ok(html.includes('<ol>') && html.includes('<strong>冷启动开销</strong>'));
  assert.ok(html.includes('<blockquote>') && html.includes('<strong>新手答</strong>'));
  assert.ok(html.includes('流程步骤') && html.includes('定义 JSON Schema') && html.includes('结果拼回上下文'));
  assert.ok(!html.includes('flowchart LR') && !html.includes('JSON Schema\\n'));
  const ordinary = render('```js\nconst value = "\\n";\n```');
  assert.ok(ordinary.includes('const value = &quot;\\n&quot;;'));
  const branching = render('```mermaid\nflowchart LR\nA["开始"] --> B["是"]\nA --> C["否"]\n```');
  assert.ok(branching.includes('flowchart LR'), 'unsupported diagram must remain readable source');
  assert.ok(!render('<script>alert(1)</script>').includes('<script>'));
  const result = { passed: true, source: 'knowledge/02-tool-management/index.md#reported-mcp-question', checks: ['table', 'ordered-list-and-bold', 'paragraph-and-source-quote', 'linear-flow-as-numbered-steps', 'code-escape-preserved', 'branching-diagram-safe-fallback', 'raw-html-not-executed'], browserVisualVerified: false, at: new Date().toISOString() };
  if (process.argv[2]) await writeFile(resolve(process.argv[2]), JSON.stringify(result, null, 2) + '\n', 'utf8');
  console.log(JSON.stringify(result));
} finally { await unlink(generated).catch(() => {}); }
