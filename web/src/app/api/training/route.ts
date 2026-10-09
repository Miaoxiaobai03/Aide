import { NextRequest, NextResponse } from 'next/server';
import { MAX_INTERVIEW_BODY_BYTES, readJsonBody } from '@/lib/api-security';

// Same server-only authentication boundary as the interview BFF. Never send
// backend credentials to the browser or cache one installation's history.
async function forward(req: NextRequest, body?: unknown) {
  try {
    const response = await fetch(`${process.env.BACKEND_URL ?? 'http://localhost:3001'}/api/v1/training${req.nextUrl.search}`, {
      method: req.method,
      headers: { 'Content-Type': 'application/json', ...(process.env.OFFERPILOT_API_KEY ? { Authorization: `Bearer ${process.env.OFFERPILOT_API_KEY}` } : {}) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      signal: req.signal, cache: 'no-store',
    });
    return new Response(await response.text(), { status: response.status, headers: { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' } });
  } catch {
    return NextResponse.json({ error: { code: 'backend_unavailable', message: '训练服务暂不可用，请重试。', retryable: true } }, { status: 502 });
  }
}
export async function GET(req: NextRequest) { return forward(req); }
export async function POST(req: NextRequest) {
  const parsed = await readJsonBody<Record<string, unknown>>(req, MAX_INTERVIEW_BODY_BYTES, 'training request');
  if (parsed.response) return parsed.response;
  return forward(req, parsed.data);
}
