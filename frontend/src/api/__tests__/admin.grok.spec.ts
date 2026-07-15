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
  ])('calculates a timeout sized for %i keys', (keyCount, expectedTimeout) => {
    expect(getGrokSSOImportTimeout(keyCount)).toBe(expectedTimeout)
  })

  it('splits large imports into bounded chunks and preserves global indexes', async () => {
    post
      .mockResolvedValueOnce({
        data: { created: [{ index: 1 }, { index: 6 }], failed: [] },
      })
      .mockResolvedValueOnce({
        data: { created: [{ index: 1 }], failed: [{ index: 2, error: 'denied' }] },
      })

    const result = await createFromSSO({
      sso_tokens: Array.from({ length: 8 }, (_, index) => `sso-${index + 1}`),
    })

    expect(post).toHaveBeenCalledTimes(2)
    expect(post).toHaveBeenNthCalledWith(
      1,
      '/admin/grok/sso-to-oauth',
      expect.objectContaining({ sso_tokens: ['sso-1', 'sso-2', 'sso-3', 'sso-4', 'sso-5', 'sso-6'] }),
      { timeout: getGrokSSOImportTimeout(6) },
    )
    expect(post).toHaveBeenNthCalledWith(
      2,
      '/admin/grok/sso-to-oauth',
      expect.objectContaining({ sso_tokens: ['sso-7', 'sso-8'] }),
      { timeout: getGrokSSOImportTimeout(2) },
    )
    expect(result.created.map((item) => item.index)).toEqual([1, 6, 7])
    expect(result.failed.map((item) => item.index)).toEqual([8])
  })

  it('extracts only the SSO token from grok-register account lines', () => {
    const token = 'eyJheader.eyJpayload.signature'
    expect(parseGrokSSOInput(`user@example.com----password-ending-----${token}\n${token}`)).toEqual([
      token,
      token,
    ])
  })

  it('keeps completed chunk results when a later chunk fails', async () => {
    post
      .mockResolvedValueOnce({ data: { created: [{ index: 1 }], failed: [] } })
      .mockRejectedValueOnce(new Error('network timeout'))

    const result = await createFromSSO({
      sso_tokens: Array.from({ length: 8 }, (_, index) => `sso-${index + 1}`),
    })

    expect(result.created.map((item) => item.index)).toEqual([1])
    expect(result.failed.map((item) => item.index)).toEqual([7, 8])
    expect(result.failed.every((item) => item.error === 'network timeout')).toBe(true)
  })
})
