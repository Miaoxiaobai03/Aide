import { NextRequest, NextResponse } from 'next/server';
import { readJsonBody } from '@/lib/api-security';

// Only the server carries the backend key; never cache installation history.
async function forward(req: NextRequest, body?: unknown) {
  try {
    const response = await fetch(`${process.env.BACKEND_URL ?? 'http://localhost:3001'}/api/v1/coach${req.nextUrl.search}`, {
      method: req.method, headers: { 'Content-Type': 'application/json', ...(process.env.OFFERPILOT_API_KEY ? { Authorization: `Bearer ${process.env.OFFERPILOT_API_KEY}` } : {}) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }), signal: req.signal, cache: 'no-store',
    });
    return new Response(await response.text(), { status: response.status, headers: { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' } });
  } catch {
    return NextResponse.json({ error: { message: '问答服务暂不可用，草稿已保留，请重试。' } }, { status: 502 });
  }
}
export async function GET(req: NextRequest) { return forward(req); }
export async function POST(req: NextRequest) {
  const parsed = await readJsonBody<Record<string, unknown>>(req);
  if (parsed.response) return parsed.response;
  return forward(req, parsed.data);
}
