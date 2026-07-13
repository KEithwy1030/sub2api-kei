import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import { createFromSSO, getGrokSSOImportTimeout, parseGrokSSOInput } from '@/api/admin/grok'

describe('admin Grok SSO import API', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: { created: [], failed: [] } })
  })

  it.each([
    [1, 215_000],
    [3, 215_000],
    [4, 340_000],
    [7, 465_000],
  ])('uses a timeout sized for %i keys', async (keyCount, expectedTimeout) => {
    expect(getGrokSSOImportTimeout(keyCount)).toBe(expectedTimeout)

    await createFromSSO({
      sso_tokens: Array.from({ length: keyCount }, (_, index) => `sso-${index + 1}`),
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/grok/sso-to-oauth',
      expect.objectContaining({ sso_tokens: expect.any(Array) }),
      { timeout: expectedTimeout },
    )
  })

  it('extracts only the SSO token from grok-register account lines', () => {
    const token = 'eyJheader.eyJpayload.signature'
    expect(parseGrokSSOInput(`user@example.com----password-ending-----${token}\n${token}`)).toEqual([
      token,
      token,
    ])
  })
})
