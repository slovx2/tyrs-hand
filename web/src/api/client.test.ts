import { http, HttpResponse } from 'msw'
import { describe, expect, it } from 'vitest'
import { APIError, api, jsonBody } from './client'
import { useUI } from '../state'
import { server } from '../test/server'

describe('API client', () => {
  it('发送 CSRF 并解析成功资源', async () => {
    useUI.getState().setCSRFToken('csrf-token')
    server.use(
      http.post('/api/v1/example', ({ request }) => {
        expect(request.headers.get('X-CSRF-Token')).toBe('csrf-token')
        return HttpResponse.json({ id: 'resource-1' })
      }),
    )
    await expect(
      api<{ id: string }>('/example', { method: 'POST', body: '{}' }),
    ).resolves.toEqual({ id: 'resource-1' })
  })

  it('把 Problem Details 转成带详情的错误', async () => {
    server.use(
      http.get('/api/v1/failure', () =>
        HttpResponse.json(
          { title: '拒绝', status: 403, detail: '权限不足' },
          {
            status: 403,
            headers: { 'Content-Type': 'application/problem+json' },
          },
        ),
      ),
    )
    const error = await api('/failure').catch((value: unknown) => value)
    expect(error).toBeInstanceOf(APIError)
    expect((error as APIError).message).toBe('权限不足')
  })

  it('支持 204 响应', async () => {
    server.use(
      http.post('/api/v1/empty', () => new HttpResponse(null, { status: 204 })),
    )
    await expect(api('/empty', { method: 'POST' })).resolves.toBeUndefined()
  })

  it('生成 JSON 请求体', () => {
    expect(jsonBody({ value: 1 })).toEqual({ body: '{"value":1}' })
  })
})
