import { describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const messages: Record<string, string> = {
        'admin.accounts.oauth.grok.failedToExchangeCode': 'Grok 授权码兑换失败',
        'admin.accounts.oauth.grok.errors.GROK_OAUTH_INVALID_STATE':
          'Grok OAuth state 与当前会话不匹配。请粘贴同一次生成的授权链接返回的回调 URL。'
      }
      return messages[key] ?? key
    }
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshGrokToken: vi.fn()
    }
  }
}))

import { useGrokOAuth } from '@/composables/useGrokOAuth'
import { adminAPI } from '@/api/admin'

describe('useGrokOAuth.exchangeAuthCode', () => {
  it('shows a state mismatch recovery hint from structured backend errors', async () => {
    vi.mocked(adminAPI.grok.exchangeCode).mockRejectedValueOnce({
      status: 400,
      reason: 'GROK_OAUTH_INVALID_STATE',
      message: 'invalid oauth state'
    })
    const oauth = useGrokOAuth()

    const tokenInfo = await oauth.exchangeAuthCode({
      code: 'code',
      sessionId: 'session-id',
      state: 'wrong-state'
    })

    expect(tokenInfo).toBeNull()
    expect(oauth.error.value).toBe(
      'Grok OAuth state 与当前会话不匹配。请粘贴同一次生成的授权链接返回的回调 URL。'
    )
  })
})

describe('useGrokOAuth.validateRefreshToken', () => {
  it('returns credentials only after a successful chat preflight', async () => {
    vi.mocked(adminAPI.grok.refreshGrokToken).mockResolvedValueOnce({
      token_info: { access_token: 'access-token', refresh_token: 'refresh-token' },
      preflight: { usable: true, model: 'grok-4.5', status_code: 200, reason: 'GROK_PREFLIGHT_OK' }
    })
    const oauth = useGrokOAuth()

    const tokenInfo = await oauth.validateRefreshToken('refresh-token')

    expect(tokenInfo?.access_token).toBe('access-token')
    expect(oauth.buildCredentials(tokenInfo!)).toMatchObject({
      base_url: 'https://cli-chat-proxy.grok.com/v1',
      model_mapping: { 'grok-4.5': 'grok-4.5' }
    })
  })

  it('rejects an OAuth identity without chat permission', async () => {
    vi.mocked(adminAPI.grok.refreshGrokToken).mockResolvedValueOnce({
      token_info: { access_token: 'access-token' },
      preflight: {
        usable: false,
        model: 'grok-4.5',
        status_code: 403,
        reason: 'GROK_PREFLIGHT_CHAT_PERMISSION_DENIED'
      }
    })
    const oauth = useGrokOAuth()

    expect(await oauth.validateRefreshToken('refresh-token')).toBeNull()
    expect(oauth.error.value).toContain('GROK_PREFLIGHT_CHAT_PERMISSION_DENIED')
  })
})
