import { NextRequest, NextResponse } from 'next/server';
import { MAX_RESUME_DIAGNOSIS_BODY_BYTES, readJsonBody } from '@/lib/api-security';

const BACKEND_URL = process.env.BACKEND_URL ?? 'http://localhost:3001';
const API_KEY = process.env.AIDE_API_KEY;

export async function POST(req: NextRequest) {
  const parsed = await readJsonBody<{ content?: string; images?: string[] }>(
    req,
    MAX_RESUME_DIAGNOSIS_BODY_BYTES,
    'resume diagnosis request body',
  );
  if (parsed.response) return parsed.response;

  const content = parsed.data.content?.trim() ?? '';
  const images = parsed.data.images ?? [];
  if (!content) {
    return NextResponse.json(
      { error: { code: 'validation_error', message: 'resume content is required', retryable: false } },
      { status: 400 },
    );
  }

  try {
    const response = await fetch(`${BACKEND_URL}/api/v1/resume/diagnose`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        ...(API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {}),
      },
      body: JSON.stringify({ content, images }),
      signal: req.signal,
      cache: 'no-store',
    });
    const text = await response.text();
    return new Response(text, {
      status: response.status,
      headers: {
        'Content-Type': response.headers.get('content-type') ?? 'application/json; charset=utf-8',
        'Cache-Control': 'no-store',
      },
    });
  } catch {
    return NextResponse.json(
      { error: { code: 'backend_unavailable', message: '简历诊断服务暂时无法连接；简历原文已保留，请稍后重试。', retryable: true } },
      { status: 502 },
    );
  }
}
