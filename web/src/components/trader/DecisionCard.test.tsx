import { render, screen, fireEvent } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { DecisionCard } from './DecisionCard'
import type { DecisionRecord } from '../../types'

const baseDecision: DecisionRecord = {
  timestamp: '2026-04-22T12:14:19Z',
  cycle_number: 4796,
  system_prompt: '',
  input_prompt: '',
  cot_trace: '',
  decision_json: '',
  account_state: {} as never,
  positions: [],
  candidate_coins: [],
  ai_decision_mode: 'balanced',
  allow_ai_open: true,
  allow_ai_stop_close: false,
  allow_ai_take_profit: false,
  decisions: [
    {
      action: 'open_long',
      symbol: 'TRUMPUSDT',
      quantity: 10,
      leverage: 3,
      price: 3.0,
      stop_loss: 2.984,
      take_profit: 3.061,
      confidence: 78,
      order_id: 0,
      timestamp: '2026-04-22T12:14:16Z',
      success: false,
      error: 'regime filter blocked open_long for TRUMPUSDT',
      review_context: {
        control: { decision: 'rejected', no_order_placed: true },
        quality_gate: { decision: 'rejected', quality_total: 42, net_rr: 1.4 },
        risk_reward: { net_estimated_rr: 1.4, passed: false },
      },
    },
  ],
  execution_log: ['🚫 TRUMPUSDT open_long blocked by regime gate'],
  success: true,
}

describe('DecisionCard', () => {
  it('renders a compact one-line summary for a trade action', () => {
    render(<DecisionCard decision={baseDecision} language="en" />)
    // symbol shown without the USDT suffix
    expect(screen.getByText('TRUMP')).toBeInTheDocument()
    // this was an open_long, so no SHORT tag should be present
    expect(screen.queryByText('📉 SHORT')).toBeNull()
  })

  it('shows direction tag and leverage', () => {
    render(<DecisionCard decision={baseDecision} language="en" />)
    expect(screen.getByText('📈 LONG')).toBeInTheDocument()
    expect(screen.getByText('3x')).toBeInTheDocument()
  })

  it('expands to reveal the execution log', () => {
    render(<DecisionCard decision={baseDecision} language="en" />)
    // header row toggles expansion
    fireEvent.click(screen.getByText('🤖 #4796'))
    expect(screen.getByText(/blocked by regime gate/i)).toBeInTheDocument()
  })
})
