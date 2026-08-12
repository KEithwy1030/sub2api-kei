import { describe, expect, it, vi } from 'vitest'

const account = (id: number) => ({ id }) as any

describe('usageLoadQueue', () => {
  it('deduplicates concurrent requests for the same account', async () => {
    vi.resetModules()
    const { enqueueUsageRequest } = await import('./usageLoadQueue')
    let finish!: (value: string) => void
    const fetchUsage = vi.fn(() => new Promise<string>((resolve) => { finish = resolve }))

    const first = enqueueUsageRequest(account(1), fetchUsage)
    const second = enqueueUsageRequest(account(1), fetchUsage)

    expect(second).toBe(first)
    expect(fetchUsage).toHaveBeenCalledTimes(0)
    await Promise.resolve()
    expect(fetchUsage).toHaveBeenCalledTimes(1)

    finish('ok')
    await expect(first).resolves.toBe('ok')
    await expect(second).resolves.toBe('ok')
  })

  it('runs at most three account usage requests at once', async () => {
    vi.resetModules()
    const { enqueueUsageRequest } = await import('./usageLoadQueue')
    const finishes: Array<() => void> = []
    let active = 0
    let peak = 0
    let started = 0

    const requests = Array.from({ length: 5 }, (_, index) =>
      enqueueUsageRequest(account(index + 1), () => {
        started += 1
        active += 1
        peak = Math.max(peak, active)
        return new Promise<void>((resolve) => {
          finishes.push(() => {
            active -= 1
            resolve()
          })
        })
      })
    )

    await Promise.resolve()
    expect(started).toBe(3)
    expect(peak).toBe(3)

    finishes.shift()?.()
    await vi.waitFor(() => expect(started).toBe(4))
    expect(peak).toBe(3)

    while (finishes.length > 0) {
      finishes.shift()?.()
      await Promise.resolve()
      await Promise.resolve()
    }
    await Promise.all(requests)
    expect(peak).toBe(3)
  })
})
