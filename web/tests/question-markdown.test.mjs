import {readFileSync} from 'node:fs';
import {createRequire} from 'node:module';
import {pathToFileURL} from 'node:url';
import {it} from 'node:test';
import assert from 'node:assert/strict';
import ts from 'typescript';
import {createElement} from 'react';
import {renderToStaticMarkup} from 'react-dom/server';

// Render the actual component without a browser or a separate UI library.
// This verifies semantic Markdown output, not layout or interaction.
const require=createRequire(import.meta.url);
const source=readFileSync(new URL('../src/components/MarkdownContent.tsx',import.meta.url),'utf8');
let code=ts.transpileModule(source,{compilerOptions:{module:ts.ModuleKind.ESNext,jsx:ts.JsxEmit.ReactJSX}}).outputText;
for(const name of ['react/jsx-runtime','react-markdown','remark-gfm'])code=code.replaceAll('"'+name+'"','"'+pathToFileURL(require.resolve(name)).href+'"').replaceAll("'"+name+"'",'"'+pathToFileURL(require.resolve(name)).href+'"');
const {MarkdownContent}=await import('data:text/javascript,'+encodeURIComponent(code));
it('question references render real headings, lists, tables and readable code',()=>{
 const text='## 参考题解\n\n1. 保存原文\n2. 检查权限\n\n| 字段 | 意义 |\n| --- | --- |\n| ID | 来源 |\n\n```json\n{"tool":"query"}\n```';
 const html=renderToStaticMarkup(createElement(MarkdownContent,{children:text}));
 for(const tag of ['<h2>','<ol>','<table>','<pre>'])assert.ok(html.includes(tag));
 assert.ok(html.includes('保存原文'));assert.ok(!html.includes('| --- |'));
});
