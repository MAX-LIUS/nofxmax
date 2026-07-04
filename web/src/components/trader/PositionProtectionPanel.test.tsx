import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

vi.mock('../../lib/api', () => ({
  api: {
    getOpenOrders: vi.fn(async () => []),
  },
}))

import { PositionProtectionPanel } from './PositionProtectionPanel'
import type { Position } from '../../types'

describe('PositionProtectionPanel price ladder', () => {
  it('renders color-coded close-level rows (runner/liq/DD) with legend', async () => {
    const positions: Position[] = [
      {
        symbol: 'BTCUSDT',
        side: 'long',
        entry_price: 100,
        mark_price: 104,
        quantity: 1,
        leverage: 5,
        unrealized_pnl: 4,
        unrealized_pnl_pct: 4,
        liquidation_price: 70,
        margin_used: 20,
        protection_state: 'exchange_protection_verified',
        break_even_state: 'idle',
        drawdown_execution_mode: 'native_partial_trailing',
        entry_structure_audit: {
          audit_primary_timeframe: true,
          audit_adjacent_timeframes: true,
          audit_support_resistance: true,
          audit_structural_anchors: true,
          audit_fibonacci: true,
          require_invalidation_target_linkage: true,
        },
        entry_review_summary: {
          timeframe_context: { primary: '15m', lower: ['5m'], higher: ['1h'] },
          risk_reward: { entry: 100, invalidation: 95, first_target: 110 },
          key_levels: {
            support: [99, 97],
            resistance: [110, 112],
            swing_highs: [111],
            swing_lows: [96],
            fibonacci: { swing_low: 95, swing_high: 110, levels: [101, 109] },
          },
        },
        protection_runtime: {
          current_pnl_pct: 4,
          drawdown_peak_pnl_pct: 6,
          current_drawdown_pct: 1.2,
          drawdown_config_source: 'strategy',
          current_drawdown_stage_min_profit_pct: 3,
          current_drawdown_stage_rule_count: 1,
          current_drawdown_stage: 'post_breakout_runner',
          drawdown_structure_stage: 'near_primary_target',
          drawdown_structure_stop_source: 'primary_target_pullback',
          drawdown_structure_target_source: 'primary_resistance',
          drawdown_structure_target_progress: 0.92,
          drawdown_structure_primary_timeframe: '15m',
          drawdown_structure_evidence: [
            'first_target',
            'primary_resistance',
            'fibonacci',
          ],
          drawdown_structure_trace: [
            'tf=15m',
            'stage=near_primary_target',
            'progress=0.92',
            'stop_source=primary_target_pullback',
            'target_source=primary_resistance',
          ],
          structure_protection_health: 'partially_degraded',
          structure_protection_drift_reason: 'ladder_degraded',
          structure_protection_detached: false,
          runner_mode_active: true,
          runner_keep_pct: 30,
          runner_stop_mode: 'structure',
          runner_stop_price: 102.5,
          runner_stop_source: 'adjacent_support_flip',
          runner_target_mode: 'structure',
          runner_target_price: 109,
          runner_target_source: 'primary_resistance',
          break_even_suppressed_by_runner: true,
          planned_ladder_stop_count: 2,
          planned_ladder_take_profit_count: 2,
          live_ladder_stop_count: 0,
          live_ladder_take_profit_count: 1,
          live_full_stop_count: 1,
          live_full_take_profit_count: 0,
          fallback_order_detected: true,
          live_fallback_stop_count: 1,
          full_stop_planned: false,
          full_take_profit_planned: false,
          fallback_planned: true,
          ladder_stop_degraded: true,
          ladder_take_profit_degraded: true,
          ladder_stop_degraded_to_full: true,
          ladder_take_profit_degraded_to_full: false,
          scheduled_tiers: [
            {
              index: 1,
              min_profit_pct: 3,
              max_drawdown_pct: 1,
              close_ratio_pct: 50,
              activation_price: 103,
              callback_rate: 0.4,
              planned_quantity: 0.5,
              source: 'native',
              execution_mode: 'native_partial_trailing',
              drawdown_stage: 'post_breakout_runner',
              runner_mode_active: true,
              runner_keep_pct: 30,
              runner_stop_mode: 'structure',
              runner_stop_source: 'adjacent_support_flip',
              runner_target_mode: 'structure',
              runner_target_source: 'primary_resistance',
              break_even_suppressed_by_runner: true,
              is_satisfied: true,
              is_triggered: true,
            },
          ],
        },
      },
    ]

    render(
      <PositionProtectionPanel
        traderId="t-1"
        positions={positions}
        language="en"
        exchange="okx"
      />
    )

    // Legend must render all 6 color families
    expect(await screen.findByText('TP')).toBeInTheDocument()
    expect(screen.getByText('BE')).toBeInTheDocument()
    expect(screen.getByText('SL/Fallback')).toBeInTheDocument()
    expect(screen.getByText('Trailing/DD')).toBeInTheDocument()
    expect(screen.getByText('Structural')).toBeInTheDocument()
    // 'Liq' appears in both the legend and the liquidation row zone label
    expect(screen.getAllByText('Liq').length).toBeGreaterThanOrEqual(1)

    // Position header
    expect(screen.getByText('BTCUSDT')).toBeInTheDocument()
    expect(screen.getByText('LONG')).toBeInTheDocument()

    // Current price "Now" marker (also appears in the Full/Now header)
    expect(screen.getAllByText('Now').length).toBeGreaterThanOrEqual(1)

    // DD-1 row from scheduled_tiers[0] (is_triggered=true → red dot)
    expect(screen.getByText('DD-1')).toBeInTheDocument()

    // Runner row from runner_mode_active + runner_stop_price=102.5
    expect(screen.getByText('Runner')).toBeInTheDocument()

    // Liquidation row from liquidation_price=70
    // Text appears in legend ("Liq") and as zone label in the row
    const liqEls = screen.getAllByText('Liq')
    expect(liqEls.length).toBeGreaterThanOrEqual(2) // legend item + row label
  })
})
