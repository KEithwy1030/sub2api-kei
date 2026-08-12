/** Usage request scheduler for account-list quota cells. */

import type { Account } from '@/types'

const MAX_CONCURRENT_REQUESTS = 3

let activeRequests = 0
const pendingRequests: Array<() => void> = []
const inFlightRequests = new Map<number, Promise<unknown>>()

function drainQueue(): void {
  while (activeRequests < MAX_CONCURRENT_REQUESTS && pendingRequests.length > 0) {
    pendingRequests.shift()?.()
  }
}

export function enqueueUsageRequest<T>(
  account: Account,
  fn: () => Promise<T>
): Promise<T> {
  const existing = inFlightRequests.get(account.id)
  if (existing) return existing as Promise<T>

  let startRequest: () => void
  const request = new Promise<T>((resolve, reject) => {
    startRequest = () => {
      activeRequests += 1
      Promise.resolve()
        .then(fn)
        .then(resolve, reject)
        .finally(() => {
          activeRequests -= 1
          if (inFlightRequests.get(account.id) === request) {
            inFlightRequests.delete(account.id)
          }
          drainQueue()
        })
    }
  })

  inFlightRequests.set(account.id, request)
  pendingRequests.push(startRequest!)
  drainQueue()
  return request
}
