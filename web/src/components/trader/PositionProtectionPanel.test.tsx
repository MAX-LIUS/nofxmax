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

  it('renders 4-colour exchange_light status text (yellow=phantom/no-activation)', async () => {
    const positions: Position[] = [
      {
        symbol: 'WLDUSDT',
        side: 'short',
        entry_price: 100,
        mark_price: 92,
        quantity: 10,
        leverage: 5,
        unrealized_pnl: 8,
        unrealized_pnl_pct: 8,
        liquidation_price: 130,
        margin_used: 20,
        protection_state: 'exchange_protection_verified',
        break_even_state: 'idle',
        drawdown_execution_mode: 'native_trailing_full',
        protection_runtime: {
          current_pnl_pct: 8,
          drawdown_peak_pnl_pct: 8,
          current_drawdown_pct: 0,
          scheduled_tiers: [
            {
              index: 1,
              min_profit_pct: 6,
              max_drawdown_pct: 30,
              close_ratio_pct: 100,
              activation_price: 94,
              callback_rate: 0.3,
              planned_quantity: 10,
              source: 'native',
              execution_mode: 'native_trailing_full',
              is_satisfied: true,
              is_triggered: false,
              // Real exchange state: dangerous no-activation order → yellow with the
              // urgent "will mis-close · cancelling" reason text.
              exchange_light: 'yellow',
              exchange_light_reason: 'no_activation',
            },
          ],
        },
      } as unknown as Position,
    ]

    render(
      <PositionProtectionPanel
        traderId="t-2"
        positions={positions}
        language="en"
        exchange="okx"
      />
    )

    expect(await screen.findByText('DD-1')).toBeInTheDocument()
    // exchange_light=yellow + reason=no_activation → the urgent mis-close warning.
    // The tile/dot title carries the full buildTierReason tooltip ("...it would
    // mis-close on any retrace..."), so match that wording.
    const danger = await screen.findAllByTitle(
      /would mis-close on any retrace/i
    )
    expect(danger.length).toBeGreaterThanOrEqual(1)
  })

  // A drawdown tier has two prices: the activation price (where trailing starts
  // tracking — nothing fills there) and the execution price (peak × (1 - cb),
  // where it actually closes). The ladder is ordered by "which level does price
  // reach next", so a tier that activates at 3ATR but gives back 1.8ATR must land
  // between the 1.1ATR and 1.7ATR ladder rungs — not out at the 3ATR slot.
  it('orders a DD tier by its execution price, between the ladder rungs', async () => {
    const { api } = await import('../../lib/api')
    // entry 100, ATR 2 => 1.1ATR = 102.2, 1.7ATR = 103.4
    vi.mocked(api.getOpenOrders).mockResolvedValueOnce([
      {
        order_id: 'tp1',
        symbol: 'BTCUSDT',
        type: 'TAKE_PROFIT_MARKET',
        side: 'SELL',
        position_side: 'LONG',
        stop_price: 102.2,
        quantity: 0.4,
        client_order_id: 'ladder_tp1',
      },
      {
        order_id: 'tp2',
        symbol: 'BTCUSDT',
        type: 'TAKE_PROFIT_MARKET',
        side: 'SELL',
        position_side: 'LONG',
        stop_price: 103.4,
        quantity: 0.35,
        client_order_id: 'ladder_tp2',
      },
    ] as never)

    const positions = [
      {
        symbol: 'BTCUSDT',
        side: 'long',
        entry_price: 100,
        mark_price: 101,
        quantity: 1,
        leverage: 5,
        unrealized_pnl: 1,
        unrealized_pnl_pct: 1,
        liquidation_price: 70,
        margin_used: 20,
        protection_state: 'exchange_protection_verified',
        break_even_state: 'idle',
        drawdown_execution_mode: 'native_trailing_full',
        protection_runtime: {
          current_pnl_pct: 1,
          drawdown_peak_pnl_pct: 0,
          current_drawdown_pct: 0,
          atr_at_entry: 2,
          scheduled_tiers: [
            {
              index: 1,
              min_profit_pct: 6,
              max_drawdown_pct: 30,
              close_ratio_pct: 100,
              // activates at 3ATR ...
              activation_price: 106,
              callback_rate: 0.0339622641509434,
              // ... but fills at 1.2ATR (backend-computed, single source of truth)
              execution_price: 102.4,
              planned_quantity: 1,
              source: 'native',
              execution_mode: 'native_trailing_full',
              is_satisfied: false,
              is_triggered: false,
              exchange_light: 'green',
            },
          ],
        },
      },
    ] as unknown as Position[]

    render(
      <PositionProtectionPanel
        traderId="t-4"
        positions={positions}
        language="en"
        exchange="binance"
      />
    )

    expect(await screen.findByText('DD-1')).toBeInTheDocument()
    // The DD row must show the EXECUTION price, not the 106 activation price.
    expect(screen.getByText('102.40')).toBeInTheDocument()
    expect(screen.queryByText('106.00')).not.toBeInTheDocument()

    // DOM order = ladder order (LONG: furthest upside first).
    const prices = screen
      .getAllByText(/^10[0-9]\.[0-9]{2}$/)
      .map((el) => el.textContent)
    const idx = (p: string) => prices.indexOf(p)
    expect(idx('103.40')).toBeGreaterThanOrEqual(0)
    expect(idx('102.40')).toBeGreaterThan(idx('103.40'))
    expect(idx('102.20')).toBeGreaterThan(idx('102.40'))
  })

  it('renders phantom yellow light distinctly from no-activation danger', async () => {
    const positions: Position[] = [
      {
        symbol: 'BTCUSDT',
        side: 'long',
        entry_price: 100,
        mark_price: 108,
        quantity: 1,
        leverage: 5,
        unrealized_pnl: 8,
        unrealized_pnl_pct: 8,
        liquidation_price: 70,
        margin_used: 20,
        protection_state: 'exchange_protection_verified',
        break_even_state: 'idle',
        drawdown_execution_mode: 'native_trailing_full',
        protection_runtime: {
          current_pnl_pct: 8,
          drawdown_peak_pnl_pct: 8,
          current_drawdown_pct: 0,
          scheduled_tiers: [
            {
              index: 1,
              min_profit_pct: 6,
              max_drawdown_pct: 30,
              close_ratio_pct: 100,
              activation_price: 106,
              callback_rate: 0.3,
              planned_quantity: 1,
              source: 'native',
              execution_mode: 'native_trailing_full',
              is_satisfied: true,
              is_triggered: false,
              exchange_light: 'yellow',
              exchange_light_reason: 'phantom',
            },
          ],
        },
      } as unknown as Position,
    ]

    render(
      <PositionProtectionPanel
        traderId="t-3"
        positions={positions}
        language="en"
        exchange="okx"
      />
    )

    expect(await screen.findByText('DD-1')).toBeInTheDocument()
    // phantom reason → "...dead (no protection, but cannot mis-close)...", which
    // is distinct from the no_activation "would mis-close on any retrace" danger.
    const phantom = await screen.findAllByTitle(
      /no protection, but cannot mis-close/i
    )
    expect(phantom.length).toBeGreaterThanOrEqual(1)
    expect(
      screen.queryAllByTitle(/would mis-close on any retrace/i)
    ).toHaveLength(0)
  })
})
