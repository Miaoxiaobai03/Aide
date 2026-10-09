import { NextRequest, NextResponse } from 'next/server';
import { readJsonBody } from '@/lib/api-security';

const BACKEND_URL = process.env.BACKEND_URL ?? 'http://localhost:3001';
const API_KEY = process.env.AIDE_API_KEY;
const MAX_BODY_BYTES = 11 * 1024 * 1024;

export async function POST(req: NextRequest) {
  const parsed = await readJsonBody<{ image?: string }>(req, MAX_BODY_BYTES, 'JD image');
  if (parsed.response) return parsed.response;
  if (typeof parsed.data.image !== 'string') {
    return NextResponse.json({ error: { code: 'invalid_image', message: '请选择 PNG 或 JPG 图片' } }, { status: 400 });
  }
  try {
    const response = await fetch(`${BACKEND_URL}/api/v1/jobs/extract-image`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
        ...(API_KEY ? { Authorization: `Bearer ${API_KEY}` } : {}),
      },
      body: JSON.stringify({ image: parsed.data.image }),
      signal: req.signal,
      cache: 'no-store',
    });
    return new Response(await response.text(), {
      status: response.status,
      headers: {
        'Content-Type': response.headers.get('content-type') ?? 'application/json; charset=utf-8',
        'Cache-Control': 'no-store',
      },
    });
  } catch {
    return NextResponse.json({ error: { code: 'backend_unavailable', message: '图片解析服务暂时不可用' } }, { status: 502 });
  }
}
