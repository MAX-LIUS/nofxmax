import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { EntryProtectionPlan } from './PositionHistory'
import type { PlanItem } from './protectionPlan'

// The real ETHUSDT SHORT that exposed the defect: entry 1862.63, BE1 arms at
// +1.3% profit and then parks the stop at entry −0.3% = 1856.69. The old render
// put those two numbers side by side as "+1.3%@1856.69", which reads as "1.3% is
// 1856.69" — it is not, and 1856.69 is not even 1.3% away from entry.
const be1: PlanItem = {
  mechanism: 'break_even_stop',
  kind: 'be',
  label: 'BE:BE1',
  triggerPct: 1.3,
  triggerPrice: 1856.69,
  note: 'arms +1.3% → stop @ +0.3%',
}
const be2: PlanItem = {
  mechanism: 'break_even_stop',
  kind: 'be',
  label: 'BE:BE2',
  triggerPct: 2.5,
  triggerPrice: 1843.66,
}
const tp4: PlanItem = {
  mechanism: 'ladder_tp',
  kind: 'tp',
  label: 'TP4',
  triggerPct: 0.42,
  triggerPrice: 1854.37,
  closeRatioPct: 12,
}

describe('EntryProtectionPlan break-even rendering', () => {
  it('labels the BE percent as the arm threshold, separated from the stop price', () => {
    render(
      <EntryProtectionPlan
        plan={[be1]}
        language="zh"
        firedKeys={new Set<string>()}
      />
    )
    // The percent must be qualified as the arm threshold...
    expect(screen.getByText(/激活\+1\.3%/)).toBeTruthy()
    // ...and the price must be qualified as the stop, not glued to the percent.
    expect(screen.getByText(/止损@/)).toBeTruthy()
    // The old ambiguous form must be gone.
    expect(screen.queryByText('@1856.69')).toBeNull()
  })

  it('uses English qualifiers when the panel is in English', () => {
    render(
      <EntryProtectionPlan
        plan={[be2]}
        language="en"
        firedKeys={new Set<string>()}
      />
    )
    expect(screen.getByText(/arm\+2\.5%/)).toBeTruthy()
    expect(screen.getByText(/stop@/)).toBeTruthy()
  })

  it('leaves non-BE tiers in the plain form, where pct and price agree', () => {
    // TP4's +0.42% and 1854.37 ARE the same level, so qualifying them would only
    // add noise. This guards against the fix leaking into every tier.
    render(
      <EntryProtectionPlan
        plan={[tp4]}
        language="zh"
        firedKeys={new Set<string>()}
      />
    )
    expect(screen.getByText('@1854.37')).toBeTruthy()
    expect(screen.queryByText(/激活/)).toBeNull()
  })

  it('keeps the fired-tier mark and close ratio intact for BE rows', () => {
    const fired = new Set<string>()
    render(
      <EntryProtectionPlan
        plan={[{ ...be1, closeRatioPct: 100 }]}
        language="zh"
        firedKeys={fired}
      />
    )
    expect(screen.getByText('·100%')).toBeTruthy()
  })

  it('renders a BE tier that has no resolved price without crashing', () => {
    render(
      <EntryProtectionPlan
        plan={[{ ...be1, triggerPrice: undefined }]}
        language="zh"
        firedKeys={new Set<string>()}
      />
    )
    expect(screen.getByText(/激活\+1\.3%/)).toBeTruthy()
    expect(screen.queryByText(/止损@/)).toBeNull()
  })
})
