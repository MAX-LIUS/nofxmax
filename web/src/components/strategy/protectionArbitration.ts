// TypeScript port of backend trader/protection_owner_policy.go.
// Surfaces which protection mechanism actually OWNS stop-loss and take-profit when
// multiple are enabled, so the UI can show the user the real resolved behavior instead
// of leaving the precedence hidden in backend code.
import type { ProtectionConfig } from '../../types/strategy'

export type StopOwner = 'ladder' | 'full' | 'break_even_dynamic' | 'none'
export type ProfitOwner = 'drawdown' | 'ladder' | 'full' | 'none'

export interface ProtectionOwnership {
  stopOwner: StopOwner
  profitOwner: ProfitOwner
  suppressStaticTP: boolean
  // mechanisms that are enabled in config but overridden (won't actually run)
  shadowedStops: string[]
  shadowedProfits: string[]
}

// Mirrors protectionFeatureUsesManual: a feature participates in manual ownership unless
// its mode is explicitly 'ai' (AI-managed protections are owned by the runtime engine).
function usesManual(mode?: string): boolean {
  return mode !== 'ai'
}

const valueEnabled = (mode?: string): boolean =>
  mode !== undefined && mode !== 'disabled'

export function resolveProtectionOwnership(
  p?: ProtectionConfig
): ProtectionOwnership {
  const out: ProtectionOwnership = {
    stopOwner: 'none',
    profitOwner: 'none',
    suppressStaticTP: false,
    shadowedStops: [],
    shadowedProfits: [],
  }
  if (!p) return out

  const ladder = p.ladder_tp_sl
  const full = p.full_tp_sl
  const drawdown = p.drawdown_take_profit
  const be = p.break_even_stop

  // ---- Stop-loss owner: ladder > full > break_even ----
  const ladderStop =
    !!ladder?.enabled && usesManual(ladder?.mode) && !!ladder?.stop_loss_enabled
  const fullStop =
    !!full?.enabled &&
    usesManual(full?.mode) &&
    valueEnabled(full?.stop_loss?.mode)
  const beStop = !!be?.enabled

  if (ladderStop) {
    out.stopOwner = 'ladder'
    if (fullStop) out.shadowedStops.push('full')
    if (beStop) out.shadowedStops.push('break_even')
  } else if (fullStop) {
    out.stopOwner = 'full'
    if (beStop) out.shadowedStops.push('break_even')
  } else if (beStop) {
    out.stopOwner = 'break_even_dynamic'
  }

  // ---- Take-profit owner: drawdown > ladder > full ----
  const drawdownTP = !!drawdown?.enabled
  const ladderTP =
    !!ladder?.enabled &&
    usesManual(ladder?.mode) &&
    !!ladder?.take_profit_enabled
  const fullTP =
    !!full?.enabled &&
    usesManual(full?.mode) &&
    valueEnabled(full?.take_profit?.mode)

  if (drawdownTP) {
    out.profitOwner = 'drawdown'
    out.suppressStaticTP = true
    if (ladderTP) out.shadowedProfits.push('ladder')
    if (fullTP) out.shadowedProfits.push('full')
  } else if (ladderTP) {
    out.profitOwner = 'ladder'
    if (fullTP) out.shadowedProfits.push('full')
  } else if (fullTP) {
    out.profitOwner = 'full'
  }

  return out
}

export function ownerLabel(
  owner: StopOwner | ProfitOwner,
  isZh: boolean
): string {
  const map: Record<string, [string, string]> = {
    ladder: ['阶梯 TP/SL', 'Ladder TP/SL'],
    full: ['固定 TP/SL', 'Fixed TP/SL'],
    drawdown: ['峰值回撤止盈', 'Drawdown TP'],
    break_even_dynamic: ['动态保本止损', 'Break-even (dynamic)'],
    break_even: ['保本止损', 'Break-even'],
    none: ['未设置', 'None'],
  }
  const e = map[owner] || map.none
  return isZh ? e[0] : e[1]
}
