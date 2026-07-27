import type { Account } from '@/types'

const credentialFailureMarkers = [
  'grok_oauth_token_refresh_failed',
  'invalid_grant',
  'token refresh failed (non-retryable)',
  'grok oauth access token is expired',
  'grok oauth refresh token is missing'
]

export const isGrokOAuthCredentialInvalid = (
  account: Pick<Account, 'platform' | 'type' | 'status' | 'error_message'>
): boolean => {
  if (account.platform !== 'grok' || account.type !== 'oauth' || account.status !== 'error') {
    return false
  }
  const message = (account.error_message || '').toLowerCase()
  return credentialFailureMarkers.some((marker) => message.includes(marker))
}
