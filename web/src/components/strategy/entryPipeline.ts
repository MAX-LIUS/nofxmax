// Read-only summary of the entry-decision pipeline. The backend evaluates entry gates
// in three ordered stages (trader/entry_gate.go: market_state -> structural_fit ->
// confidence_risk). Entry controls are spread across risk_control and
// protection.regime_filter in the UI; this consolidates them into the actual evaluation
// order so operators can see the full gauntlet a candidate trade must pass.
import type {
  RiskControlConfig,
  RegimeFilterConfig,
} from '../../types/strategy'

export type PipelineStage =
  | 'market_state'
  | 'structural_fit'
  | 'confidence_risk'

export interface PipelineGate {
  stage: PipelineStage
  label: [string, string] // [zh, en]
  active: boolean
  value?: string // human-readable threshold
  source: string // which config block owns it
}

const fmtPct = (v?: number) => (v === undefined ? '' : `${v}`)

export function buildEntryPipeline(
  rc?: RiskControlConfig,
  rf?: RegimeFilterConfig
): PipelineGate[] {
  const gates: PipelineGate[] = []

  // ---- Stage 1: market_state (regime / funding / volatility / trend / momentum) ----
  gates.push({
    stage: 'market_state',
    label: ['市场状态过滤', 'Regime filter'],
    active: !!rf?.enabled,
    value: rf?.allowed_regimes?.length
      ? rf.allowed_regimes.join(',')
      : undefined,
    source: 'regime_filter',
  })
  gates.push({
    stage: 'market_state',
    label: ['高资金费率拦截', 'High funding block'],
    active: !!rf?.enabled && !!rf?.block_high_funding,
    value: rf?.max_funding_rate_abs ? `±${rf.max_funding_rate_abs}` : undefined,
    source: 'regime_filter',
  })
  gates.push({
    stage: 'market_state',
    label: ['高波动拦截', 'High volatility block'],
    active: !!rf?.enabled && !!rf?.block_high_volatility,
    value: rf?.max_atr14_pct ? `ATR14<=${rf.max_atr14_pct}%` : undefined,
    source: 'regime_filter',
  })
  gates.push({
    stage: 'market_state',
    label: ['趋势对齐', 'Trend alignment'],
    active: !!rf?.enabled && !!rf?.require_trend_alignment,
    value: rf?.trend_alignment_mode,
    source: 'regime_filter',
  })
  gates.push({
    stage: 'market_state',
    label: ['动量门', 'Momentum gate'],
    active: !!rf?.enabled && !!rf?.momentum_gate_enabled,
    source: 'regime_filter',
  })

  // ---- Stage 3: confidence_risk (confidence / RR / deviation / cooldown) ----
  const minConf = rf?.min_confidence ?? rc?.min_confidence
  gates.push({
    stage: 'confidence_risk',
    label: ['最低置信度', 'Min confidence'],
    active: (minConf ?? 0) > 0,
    value: minConf !== undefined ? `>=${minConf}` : undefined,
    source: rf?.min_confidence !== undefined ? 'regime_filter' : 'risk_control',
  })
  const minRR = rf?.min_risk_reward_ratio ?? rc?.min_risk_reward_ratio
  gates.push({
    stage: 'confidence_risk',
    label: ['最低盈亏比', 'Min risk:reward'],
    active: (minRR ?? 0) > 0,
    value: minRR !== undefined ? `>=${minRR}` : undefined,
    source:
      rf?.min_risk_reward_ratio !== undefined
        ? 'regime_filter'
        : 'risk_control',
  })
  gates.push({
    stage: 'confidence_risk',
    label: ['进场偏离上限', 'Max entry deviation'],
    active: (rc?.max_entry_deviation_pct ?? 0) > 0,
    value: rc?.max_entry_deviation_pct
      ? `<=${fmtPct(rc.max_entry_deviation_pct)}%`
      : undefined,
    source: 'risk_control',
  })
  gates.push({
    stage: 'confidence_risk',
    label: ['亏损后冷却', 'Post-loss cooldown'],
    active: (rc?.entry_cooldown_minutes ?? 0) > 0,
    value: rc?.entry_cooldown_minutes
      ? `${rc.entry_cooldown_minutes}min`
      : undefined,
    source: 'risk_control',
  })

  return gates
}

export const STAGE_META: Record<
  PipelineStage,
  { zh: string; en: string; order: number }
> = {
  market_state: { zh: '① 市场状态', en: '① Market state', order: 1 },
  structural_fit: { zh: '② 结构匹配', en: '② Structural fit', order: 2 },
  confidence_risk: {
    zh: '③ 置信度与风险',
    en: '③ Confidence & risk',
    order: 3,
  },
}
