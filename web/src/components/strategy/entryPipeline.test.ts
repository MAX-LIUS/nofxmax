import { describe, it, expect } from 'vitest'
import { buildEntryPipeline } from './entryPipeline'

describe('buildEntryPipeline', () => {
  it('marks regime gates active only when regime_filter enabled', () => {
    const gates = buildEntryPipeline(
      {} as any,
      {
        enabled: true,
        block_high_funding: true,
        max_funding_rate_abs: 0.0005,
      } as any
    )
    const funding = gates.find((g) => g.label[1] === 'High funding block')
    expect(funding?.active).toBe(true)
    expect(funding?.value).toBe('±0.0005')
  })

  it('regime gates inactive when regime_filter disabled', () => {
    const gates = buildEntryPipeline(
      {} as any,
      { enabled: false, block_high_funding: true } as any
    )
    expect(gates.find((g) => g.label[1] === 'High funding block')?.active).toBe(
      false
    )
  })

  it('prefers regime_filter min_confidence over risk_control', () => {
    const gates = buildEntryPipeline(
      { min_confidence: 60 } as any,
      { enabled: true, min_confidence: 70 } as any
    )
    const conf = gates.find((g) => g.label[1] === 'Min confidence')
    expect(conf?.value).toBe('>=70')
    expect(conf?.source).toBe('regime_filter')
  })

  it('falls back to risk_control min_confidence', () => {
    const gates = buildEntryPipeline({ min_confidence: 66 } as any, {} as any)
    const conf = gates.find((g) => g.label[1] === 'Min confidence')
    expect(conf?.value).toBe('>=66')
    expect(conf?.source).toBe('risk_control')
  })

  it('cooldown + deviation come from risk_control', () => {
    const gates = buildEntryPipeline(
      { entry_cooldown_minutes: 45, max_entry_deviation_pct: 1.0 } as any,
      {} as any
    )
    expect(gates.find((g) => g.label[1] === 'Post-loss cooldown')?.active).toBe(
      true
    )
    expect(gates.find((g) => g.label[1] === 'Max entry deviation')?.value).toBe(
      '<=1%'
    )
  })

  it('low volatility block stays active with regime_filter disabled', () => {
    // Unlike every other market_state row, this one must NOT follow the parent
    // switch: the backend evaluates the floor before the regime_filter.enabled
    // early-return, so showing it as inactive would misreport a live rejection.
    const gates = buildEntryPipeline(
      {} as any,
      { enabled: false, block_low_volatility: true, min_atr14_pct: 0.3 } as any
    )
    const low = gates.find((g) => g.label[1] === 'Low volatility block')
    expect(low?.active).toBe(true)
    expect(low?.value).toBe('ATR14>=0.3%')
  })

  it('low volatility block inactive when its own switch is off', () => {
    const gates = buildEntryPipeline(
      {} as any,
      { enabled: true, block_low_volatility: false, min_atr14_pct: 0.3 } as any
    )
    expect(
      gates.find((g) => g.label[1] === 'Low volatility block')?.active
    ).toBe(false)
  })

  it('high volatility block still follows the parent switch', () => {
    // Guards the asymmetry above from being "fixed" into consistency: the ceiling
    // really is gated on regime_filter.enabled in the backend.
    const gates = buildEntryPipeline(
      {} as any,
      { enabled: false, block_high_volatility: true, max_atr14_pct: 3 } as any
    )
    expect(
      gates.find((g) => g.label[1] === 'High volatility block')?.active
    ).toBe(false)
  })
})
