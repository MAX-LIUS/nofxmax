import { describe, it, expect } from 'vitest'
import { resolveProtectionOwnership } from './protectionArbitration'
import type { ProtectionConfig } from '../../types/strategy'

const vs = (mode = 'manual', value = 0) => ({ mode, value }) as any

function base(): ProtectionConfig {
  return {
    full_tp_sl: {
      enabled: false,
      mode: 'manual',
      take_profit: vs('disabled'),
      stop_loss: vs('disabled'),
      fallback_max_loss: vs('disabled'),
    },
    ladder_tp_sl: {
      enabled: false,
      mode: 'manual',
      take_profit_enabled: false,
      stop_loss_enabled: false,
      take_profit_price: vs(),
      take_profit_size: vs(),
      stop_loss_price: vs(),
      stop_loss_size: vs(),
      fallback_max_loss: vs(),
      rules: [],
    },
    drawdown_take_profit: { enabled: false } as any,
    break_even_stop: { enabled: false } as any,
  } as ProtectionConfig
}

describe('resolveProtectionOwnership', () => {
  it('ladder wins stop over full + break_even', () => {
    const p = base()
    p.ladder_tp_sl.enabled = true
    p.ladder_tp_sl.stop_loss_enabled = true
    p.full_tp_sl.enabled = true
    p.full_tp_sl.stop_loss = vs('fixed', 2)
    p.break_even_stop.enabled = true
    const o = resolveProtectionOwnership(p)
    expect(o.stopOwner).toBe('ladder')
    expect(o.shadowedStops).toContain('full')
    expect(o.shadowedStops).toContain('break_even')
  })

  it('drawdown wins TP and suppresses static TP', () => {
    const p = base()
    p.drawdown_take_profit.enabled = true
    p.ladder_tp_sl.enabled = true
    p.ladder_tp_sl.take_profit_enabled = true
    p.full_tp_sl.enabled = true
    p.full_tp_sl.take_profit = vs('fixed', 5)
    const o = resolveProtectionOwnership(p)
    expect(o.profitOwner).toBe('drawdown')
    expect(o.suppressStaticTP).toBe(true)
    expect(o.shadowedProfits).toEqual(
      expect.arrayContaining(['ladder', 'full'])
    )
  })

  it('full owns when only full enabled', () => {
    const p = base()
    p.full_tp_sl.enabled = true
    p.full_tp_sl.stop_loss = vs('fixed', 2)
    p.full_tp_sl.take_profit = vs('fixed', 5)
    const o = resolveProtectionOwnership(p)
    expect(o.stopOwner).toBe('full')
    expect(o.profitOwner).toBe('full')
    expect(o.shadowedStops).toEqual([])
  })

  it('ai-mode ladder does not claim manual ownership', () => {
    const p = base()
    p.ladder_tp_sl.enabled = true
    p.ladder_tp_sl.mode = 'ai'
    p.ladder_tp_sl.stop_loss_enabled = true
    const o = resolveProtectionOwnership(p)
    expect(o.stopOwner).toBe('none')
  })

  it('break-even owns stop when nothing else', () => {
    const p = base()
    p.break_even_stop.enabled = true
    const o = resolveProtectionOwnership(p)
    expect(o.stopOwner).toBe('break_even_dynamic')
  })

  it('all disabled -> none', () => {
    const o = resolveProtectionOwnership(base())
    expect(o.stopOwner).toBe('none')
    expect(o.profitOwner).toBe('none')
  })
})
