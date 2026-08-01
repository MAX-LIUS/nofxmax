import {
  ShieldCheck,
  TrendingDown,
  Layers,
  Activity,
  RotateCcw,
  Plus,
  Trash2,
} from 'lucide-react'
import type {
  ProtectionConfig,
  FullTPSLConfig,
  LadderTPSLConfig,
  StructuralSLConfig,
  DrawdownTakeProfitConfig,
  BreakEvenStopConfig,
  LadderTPSLRule,
  DrawdownTakeProfitRule,
  BreakEvenStopRule,
  ProtectionMode,
  ProtectionValueSource,
  GivebackGuardConfig,
  TrendReversalConfig,
  ATRProtectionConfig,
  ProtectionDistanceUnit,
} from '../../types'
import { ProtectionArbitrationBanner } from './ProtectionArbitrationBanner'

interface ProtectionEditorProps {
  config: ProtectionConfig
  onChange: (config: ProtectionConfig) => void
  disabled?: boolean
  language: string
  atrConfig?: ATRProtectionConfig
  onAtrChange?: (config: ATRProtectionConfig) => void
}

export const defaultProtectionConfig: ProtectionConfig = {
  full_tp_sl: {
    enabled: false,
    mode: 'manual',
    take_profit_enabled: true,
    stop_loss_enabled: true,
    fallback_max_loss_enabled: false,
    take_profit: { mode: 'manual', value: 0 },
    stop_loss: { mode: 'manual', value: 0 },
    fallback_max_loss: { mode: 'disabled', value: 0 },
  },
  ladder_tp_sl: {
    enabled: false,
    mode: 'manual',
    take_profit_enabled: false,
    stop_loss_enabled: false,
    take_profit_price: { mode: 'manual', value: 0 },
    take_profit_size: { mode: 'manual', value: 0 },
    stop_loss_price: { mode: 'manual', value: 0 },
    stop_loss_size: { mode: 'manual', value: 0 },
    fallback_max_loss: { mode: 'disabled', value: 0 },
    rules: [],
  },
  drawdown_take_profit: {
    enabled: false,
    mode: 'manual',
    engine_mode: 'manual',
    runner_enabled: true,
    min_runner_keep_pct: 20,
    max_first_reduce_pct: 60,
    break_even_runner_policy: 'fallback_only',
    rules: [
      {
        min_profit_pct: 5,
        max_drawdown_pct: 40,
        close_ratio_pct: 100,
        poll_interval_seconds: 60,
      },
    ],
  },
  break_even_stop: {
    enabled: false,
    mode: 'manual',
    trigger_mode: 'profit_pct',
    trigger_value: 3,
    offset_pct: 0.1,
    rules: [
      {
        trigger_mode: 'profit_pct',
        trigger_value: 0.7,
        offset_pct: 0.3,
        close_ratio_pct: 100,
        stage_name: 'BE1',
      },
    ],
  },
  regime_filter: {
    enabled: false,
    allowed_regimes: ['narrow', 'standard', 'wide'],
    block_high_funding: false,
    max_funding_rate_abs: 0.01,
    block_high_volatility: false,
    max_atr14_pct: 3,
    block_low_volatility: false,
    min_atr14_pct: 0.3,
    require_trend_alignment: false,
  },
  giveback_guard: {
    enabled: false,
    dry_run: false,
    // Breadth breaker — cross-validated production preset (min4 / f70% / ATR0.9
    // / cut100 / no cooldown). Per-symbol monitoring + majority-retrace gate,
    // cuts only losing positions (winners ride break-even). Leverage-free, so
    // 0.5%-at-10x noise can't knock the book out.
    breadth_enabled: false,
    breadth_min_pos: 4,
    breadth_frac: 0.7,
    breadth_loser_cut_pct: 100,
    breadth_use_atr: true,
    breadth_atr_mult: 0.9,
    breadth_giveback_pct: 3,
    breadth_vel_eps: 0,
    breadth_vel_window: 6,
    breadth_cooldown_bars: 0,
    breadth_cut_winners: false,
  },
  // Trend reversal: fleet default is enabled + live for every trader. The UI shows
  // these as overrides; defaults mirror the fleet so the toggles read true on a
  // fresh strategy. min_confidence 75 / min_position_age_hours 6 are the backtest
  // optima. disabled / force_dry_run are the per-trader escape hatches.
  trend_reversal: {
    disabled: false,
    force_dry_run: false,
    min_confidence: 75,
    min_position_age_hours: 6,
  },
}

export const normalizeProtectionConfig = (
  config?: Partial<ProtectionConfig> | null
): ProtectionConfig => ({
  ...defaultProtectionConfig,
  ...config,
  full_tp_sl: {
    ...defaultProtectionConfig.full_tp_sl,
    ...(config?.full_tp_sl || {}),
    take_profit: {
      ...defaultProtectionConfig.full_tp_sl.take_profit,
      ...(config?.full_tp_sl?.take_profit || {}),
    },
    stop_loss: {
      ...defaultProtectionConfig.full_tp_sl.stop_loss,
      ...(config?.full_tp_sl?.stop_loss || {}),
    },
    fallback_max_loss: {
      ...defaultProtectionConfig.full_tp_sl.fallback_max_loss,
      ...(config?.full_tp_sl?.fallback_max_loss || {}),
    },
  },
  ladder_tp_sl: {
    ...defaultProtectionConfig.ladder_tp_sl,
    ...(config?.ladder_tp_sl || {}),
    take_profit_price: {
      ...defaultProtectionConfig.ladder_tp_sl.take_profit_price,
      ...(config?.ladder_tp_sl?.take_profit_price || {}),
    },
    take_profit_size: {
      ...defaultProtectionConfig.ladder_tp_sl.take_profit_size,
      ...(config?.ladder_tp_sl?.take_profit_size || {}),
    },
    stop_loss_price: {
      ...defaultProtectionConfig.ladder_tp_sl.stop_loss_price,
      ...(config?.ladder_tp_sl?.stop_loss_price || {}),
    },
    stop_loss_size: {
      ...defaultProtectionConfig.ladder_tp_sl.stop_loss_size,
      ...(config?.ladder_tp_sl?.stop_loss_size || {}),
    },
    fallback_max_loss: {
      ...defaultProtectionConfig.ladder_tp_sl.fallback_max_loss,
      ...(config?.ladder_tp_sl?.fallback_max_loss || {}),
    },
    rules:
      config?.ladder_tp_sl?.rules || defaultProtectionConfig.ladder_tp_sl.rules,
  },
  drawdown_take_profit: {
    ...defaultProtectionConfig.drawdown_take_profit,
    ...(config?.drawdown_take_profit || {}),
    engine_mode:
      config?.drawdown_take_profit?.engine_mode ||
      (config?.drawdown_take_profit?.mode === 'ai' ? 'ai' : 'manual'),
    runner_enabled:
      config?.drawdown_take_profit?.runner_enabled ??
      defaultProtectionConfig.drawdown_take_profit.runner_enabled,
    min_runner_keep_pct:
      config?.drawdown_take_profit?.min_runner_keep_pct ??
      defaultProtectionConfig.drawdown_take_profit.min_runner_keep_pct,
    max_first_reduce_pct:
      config?.drawdown_take_profit?.max_first_reduce_pct ??
      defaultProtectionConfig.drawdown_take_profit.max_first_reduce_pct,
    break_even_runner_policy:
      config?.drawdown_take_profit?.break_even_runner_policy ||
      defaultProtectionConfig.drawdown_take_profit.break_even_runner_policy,
    rules:
      config?.drawdown_take_profit?.rules ||
      defaultProtectionConfig.drawdown_take_profit.rules,
  },
  break_even_stop: {
    ...defaultProtectionConfig.break_even_stop,
    ...(config?.break_even_stop || {}),
    rules:
      config?.break_even_stop?.rules ||
      defaultProtectionConfig.break_even_stop.rules,
  },
  regime_filter: {
    ...defaultProtectionConfig.regime_filter,
    ...(config?.regime_filter || {}),
  },
  giveback_guard: {
    ...defaultProtectionConfig.giveback_guard,
    ...(config?.giveback_guard || {}),
  },
  trend_reversal: {
    ...defaultProtectionConfig.trend_reversal,
    ...(config?.trend_reversal || {}),
  },
})

export function ProtectionEditor({
  config,
  onChange,
  disabled,
  language,
  atrConfig,
  onAtrChange,
}: ProtectionEditorProps) {
  const isZh = language === 'zh'

  // Global ATR settings (timeframe / period / clamp). Individual TP/SL/DD/BE
  // fields opt into ATR via their own per-field unit toggle.
  const atr: ATRProtectionConfig = atrConfig || {
    enabled: false,
    timeframe: '1h',
    atr_period: 14,
    min_eff_pct: 0.3,
    max_eff_pct: 20,
  }
  const updateAtr = <K extends keyof ATRProtectionConfig>(
    key: K,
    value: ATRProtectionConfig[K]
  ) => {
    if (disabled || !onAtrChange) return
    onAtrChange({ ...atr, [key]: value })
  }

  const inputStyle = {
    background: '#1E2329',
    border: '1px solid #2B3139',
    color: '#EAECEF',
  }

  const sectionStyle = {
    background: '#0B0E11',
    border: '1px solid #2B3139',
  }

  const helpCardStyle = {
    background: '#11161C',
    border: '1px solid #2B3139',
  }

  const updateSection = <K extends keyof ProtectionConfig>(
    key: K,
    value: ProtectionConfig[K]
  ) => {
    if (!disabled) onChange({ ...config, [key]: value })
  }

  const updateFull = <K extends keyof FullTPSLConfig>(
    key: K,
    value: FullTPSLConfig[K]
  ) => {
    updateSection('full_tp_sl', { ...config.full_tp_sl, [key]: value })
  }

  const updateLadder = <K extends keyof LadderTPSLConfig>(
    key: K,
    value: LadderTPSLConfig[K]
  ) => {
    let next: LadderTPSLConfig = { ...config.ladder_tp_sl, [key]: value }
    if (key === 'mode' && value === 'ai') {
      next = {
        ...next,
        enabled: true,
        take_profit_enabled: true,
        stop_loss_enabled: true,
        take_profit_price: updateValueSource(next.take_profit_price, {
          mode: 'ai',
          value: 0,
        }),
        take_profit_size: updateValueSource(next.take_profit_size, {
          mode: 'ai',
          value: 0,
        }),
        stop_loss_price: updateValueSource(next.stop_loss_price, {
          mode: 'ai',
          value: 0,
        }),
        stop_loss_size: updateValueSource(next.stop_loss_size, {
          mode: 'ai',
          value: 0,
        }),
      }
    }
    updateSection('ladder_tp_sl', next)
  }

  const updateDrawdown = <K extends keyof DrawdownTakeProfitConfig>(
    key: K,
    value: DrawdownTakeProfitConfig[K]
  ) => {
    updateSection('drawdown_take_profit', {
      ...config.drawdown_take_profit,
      [key]: value,
    })
  }

  const updateBreakEven = <K extends keyof BreakEvenStopConfig>(
    key: K,
    value: BreakEvenStopConfig[K]
  ) => {
    updateSection('break_even_stop', {
      ...config.break_even_stop,
      [key]: value,
    })
  }

  const giveback = config.giveback_guard || {}

  const updateGiveback = <K extends keyof GivebackGuardConfig>(
    key: K,
    value: GivebackGuardConfig[K]
  ) => {
    if (disabled) return
    onChange({
      ...config,
      giveback_guard: { ...giveback, [key]: value },
    })
  }

  const flip = config.trend_reversal || {}

  const updateFlip = <K extends keyof TrendReversalConfig>(
    key: K,
    value: TrendReversalConfig[K]
  ) => {
    if (disabled) return
    onChange({
      ...config,
      trend_reversal: { ...flip, [key]: value },
    })
  }

  const drawdownRules = config.drawdown_take_profit.rules || []
  const ladderRules = config.ladder_tp_sl.rules || []
  const breakEvenRules = config.break_even_stop.rules || []

  const structuralSL = config.ladder_tp_sl.structural_sl || {}
  const anySLStructural = ladderRules.some(
    (r) => r.stop_loss_unit === 'structural'
  )
  const updateStructuralSL = (patch: Partial<StructuralSLConfig>) => {
    updateLadder('structural_sl', { ...structuralSL, ...patch })
  }

  const addLadderRule = () => {
    const nextRule: LadderTPSLRule = {
      take_profit_pct: 3,
      take_profit_close_ratio_pct: 30,
      stop_loss_pct: 2,
      stop_loss_close_ratio_pct: 50,
    }
    updateLadder('rules', [...ladderRules, nextRule])
  }

  const updateLadderRule = (index: number, patch: Partial<LadderTPSLRule>) => {
    const nextRules = [...ladderRules]
    nextRules[index] = { ...nextRules[index], ...patch }
    updateLadder('rules', nextRules)
  }

  const removeLadderRule = (index: number) => {
    updateLadder(
      'rules',
      ladderRules.filter((_, i) => i !== index)
    )
  }

  const addDrawdownRule = () => {
    const nextRule: DrawdownTakeProfitRule = {
      min_profit_pct: 5,
      max_drawdown_pct: 40,
      close_ratio_pct: drawdownRules.length === 0 ? 50 : 30,
      poll_interval_seconds: 60,
      close_ratio_mode: 'manual',
      min_profit_mode: 'manual',
      max_drawdown_mode: 'manual',
    }
    updateDrawdown('rules', [...drawdownRules, nextRule])
  }

  const updateDrawdownRule = (
    index: number,
    patch: Partial<DrawdownTakeProfitRule>
  ) => {
    const nextRules = [...drawdownRules]
    nextRules[index] = { ...nextRules[index], ...patch }
    updateDrawdown('rules', nextRules)
  }

  const removeDrawdownRule = (index: number) => {
    updateDrawdown(
      'rules',
      drawdownRules.filter((_, i) => i !== index)
    )
  }

  const addBreakEvenRule = () => {
    const nextRule: BreakEvenStopRule = {
      trigger_mode: config.break_even_stop.trigger_mode || 'profit_pct',
      trigger_value:
        breakEvenRules.length === 0
          ? config.break_even_stop.trigger_value || 0.7
          : (breakEvenRules[breakEvenRules.length - 1].trigger_value || 0) +
            0.5,
      offset_pct: config.break_even_stop.offset_pct || 0.3,
      close_ratio_pct: breakEvenRules.length === 0 ? 100 : 50,
      stage_name: `BE${breakEvenRules.length + 1}`,
    }
    updateBreakEven('rules', [...breakEvenRules, nextRule])
  }

  const updateBreakEvenRule = (
    index: number,
    patch: Partial<BreakEvenStopRule>
  ) => {
    const nextRules = [...breakEvenRules]
    nextRules[index] = { ...nextRules[index], ...patch }
    updateBreakEven('rules', nextRules)
  }

  const removeBreakEvenRule = (index: number) => {
    updateBreakEven(
      'rules',
      breakEvenRules.filter((_, i) => i !== index)
    )
  }

  const protectionModeOptions: ProtectionMode[] = ['manual', 'ai']
  const valueModeOptions: ProtectionMode[] = ['disabled', 'manual', 'ai']

  // cardStyle only dims visually — never blocks pointer events on the card root,
  // so the enabled checkbox (which lives at the card level) stays clickable.
  // Individual controls use their own `disabled` prop instead.
  const cardStyle = (active = true) => ({
    ...sectionStyle,
    opacity: active ? 1 : 0.6,
  })

  const compactHintStyle = { color: '#848E9C', fontSize: '11px' }

  const modeLabel = (mode: ProtectionMode) => {
    if (mode === 'disabled') return isZh ? '禁用' : 'Disabled'
    return mode === 'ai'
      ? isZh
        ? 'AI 动态保护模式'
        : 'AI Dynamic Protection'
      : isZh
        ? '手动阈值模式'
        : 'Manual Threshold Mode'
  }

  const updateValueSource = (
    current: ProtectionValueSource,
    patch: Partial<ProtectionValueSource>
  ): ProtectionValueSource => ({
    ...current,
    ...patch,
  })

  const triggerModeLabel = (mode: 'profit_pct' | 'r_multiple') =>
    mode === 'r_multiple'
      ? isZh
        ? '按 R 倍数触发'
        : 'Trigger by R Multiple'
      : isZh
        ? '按盈利百分比触发'
        : 'Trigger by Profit %'

  const infoBlock = (title: string, description: string, recommend: string) => (
    <div className="p-3 rounded-lg space-y-1" style={helpCardStyle}>
      <div className="text-sm font-medium" style={{ color: '#EAECEF' }}>
        {title}
      </div>
      <div className="text-xs" style={{ color: '#AAB2BD' }}>
        {description}
      </div>
      <div className="text-xs" style={{ color: '#F0B90B' }}>
        {recommend}
      </div>
    </div>
  )

  // unitToggle renders a compact percent|ATR-multiple selector for a single
  // distance field. ATR mode resolves at runtime as value × ATR / entry × 100.
  const unitToggle = (
    value: ProtectionDistanceUnit | undefined,
    onSelect: (unit: ProtectionDistanceUnit) => void,
    allowStructural = false
  ) => {
    const current: ProtectionDistanceUnit =
      value === 'atr'
        ? 'atr'
        : value === 'structural'
          ? 'structural'
          : 'percent'
    return (
      <select
        value={current}
        onChange={(e) => onSelect(e.target.value as ProtectionDistanceUnit)}
        disabled={disabled}
        className="rounded px-1.5 py-1 text-xs"
        style={inputStyle}
        title={
          isZh
            ? '单位：% = 固定百分比；ATR = ATR倍数（按波动自适应）；结构位 = 锚定入场前震荡区边界'
            : 'Unit: % = fixed percent; ATR = ATR multiple; Structural = anchored to pre-entry range boundary'
        }
      >
        <option value="percent">%</option>
        <option value="atr">ATR×</option>
        {allowStructural && (
          <option value="structural">{isZh ? '结构位' : 'Struct'}</option>
        )}
      </select>
    )
  }

  const statusChip = (active: boolean, label: string) => (
    <span
      className="inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium"
      style={{
        background: active
          ? 'rgba(14, 203, 129, 0.12)'
          : 'rgba(132, 142, 156, 0.12)',
        color: active ? '#0ECB81' : '#848E9C',
        border: active
          ? '1px solid rgba(14, 203, 129, 0.25)'
          : '1px solid rgba(132, 142, 156, 0.2)',
      }}
    >
      {label}
    </span>
  )

  const fullStateSummary = isZh
    ? `执行开关：${config.full_tp_sl.enabled ? '已启用' : '未启用'} · 整体模式：${modeLabel(config.full_tp_sl.mode)} · TP侧：${config.full_tp_sl.take_profit_enabled !== false ? modeLabel(config.full_tp_sl.take_profit.mode) : '关闭'} · SL侧：${config.full_tp_sl.stop_loss_enabled !== false ? modeLabel(config.full_tp_sl.stop_loss.mode) : '关闭'} · 兜底：${config.full_tp_sl.fallback_max_loss_enabled ? modeLabel(config.full_tp_sl.fallback_max_loss.mode) : '关闭'}`
    : `Execution: ${config.full_tp_sl.enabled ? 'enabled' : 'disabled'} · Global mode: ${modeLabel(config.full_tp_sl.mode)} · TP side: ${config.full_tp_sl.take_profit_enabled !== false ? modeLabel(config.full_tp_sl.take_profit.mode) : 'off'} · SL side: ${config.full_tp_sl.stop_loss_enabled !== false ? modeLabel(config.full_tp_sl.stop_loss.mode) : 'off'} · Fallback: ${config.full_tp_sl.fallback_max_loss_enabled ? modeLabel(config.full_tp_sl.fallback_max_loss.mode) : 'off'}`

  const ladderStateSummary = isZh
    ? `执行开关：${config.ladder_tp_sl.enabled ? '已启用' : '未启用'} · 整体模式：${modeLabel(config.ladder_tp_sl.mode)} · TP侧：${config.ladder_tp_sl.take_profit_enabled ? '开启' : '关闭'} · SL侧：${config.ladder_tp_sl.stop_loss_enabled ? '开启' : '关闭'}`
    : `Execution: ${config.ladder_tp_sl.enabled ? 'enabled' : 'disabled'} · Global mode: ${modeLabel(config.ladder_tp_sl.mode)} · TP side: ${config.ladder_tp_sl.take_profit_enabled ? 'on' : 'off'} · SL side: ${config.ladder_tp_sl.stop_loss_enabled ? 'on' : 'off'}`

  const drawdownStateSummary = isZh
    ? `执行开关：${config.drawdown_take_profit.enabled ? '已启用' : '未启用'} · 模式：${modeLabel(config.drawdown_take_profit.mode)} · 规则来源：${config.drawdown_take_profit.mode === 'ai' ? 'AI 结构化输出' : config.drawdown_take_profit.mode === 'manual' ? '手动维护规则' : '未接管'} · 当前规则数：${drawdownRules.length}`
    : `Execution: ${config.drawdown_take_profit.enabled ? 'enabled' : 'disabled'} · Mode: ${modeLabel(config.drawdown_take_profit.mode)} · Rule source: ${config.drawdown_take_profit.mode === 'ai' ? 'AI structured output' : config.drawdown_take_profit.mode === 'manual' ? 'manually maintained rules' : 'not owning TP'} · Rules: ${drawdownRules.length}`

  const fullModeMismatch =
    !config.full_tp_sl.enabled && config.full_tp_sl.mode === 'ai'
  const ladderModeMismatch =
    !config.ladder_tp_sl.enabled && config.ladder_tp_sl.mode === 'ai'
  const drawdownModeMismatch =
    !config.drawdown_take_profit.enabled &&
    config.drawdown_take_profit.mode === 'ai'

  const fullEnabled = config.full_tp_sl.enabled
  const fullTpActive =
    fullEnabled && config.full_tp_sl.take_profit_enabled !== false
  const fullSlActive =
    fullEnabled && config.full_tp_sl.stop_loss_enabled !== false
  const fullFallbackActive =
    fullEnabled && !!config.full_tp_sl.fallback_max_loss_enabled

  const ladderEnabled = config.ladder_tp_sl.enabled
  const ladderTpActive =
    ladderEnabled && config.ladder_tp_sl.take_profit_enabled
  const ladderSlActive = ladderEnabled && config.ladder_tp_sl.stop_loss_enabled
  const ladderFallbackActive =
    ladderEnabled && config.ladder_tp_sl.fallback_max_loss.mode !== 'disabled'

  const drawdownEnabled = config.drawdown_take_profit.enabled

  const drawdownOwnsTp =
    drawdownEnabled &&
    config.drawdown_take_profit.mode !== 'disabled' &&
    (config.drawdown_take_profit.mode === 'ai' ||
      (config.drawdown_take_profit.rules || []).length > 0)
  const ladderTpEnabled = ladderTpActive
  const fullTpEnabled = fullTpActive

  const protectionLayerCards = [
    {
      title: isZh ? '开仓前门禁' : 'Pre-entry gate',
      subtitle: isZh ? '决定“能不能开”' : 'Decides whether a trade may open',
      body: isZh
        ? 'Regime Filter + Entry Structure + Strategy Control Policy 共同决定开仓是否被允许；它们负责证据与门禁，不负责挂保护单。'
        : 'Regime Filter + Entry Structure + Strategy Control Policy decide whether an entry is allowed. They own evidence and gating, not live protection orders.',
      tone: '#38BDF8',
    },
    {
      title: isZh ? '持仓保护委托层' : 'Live order protection layer',
      subtitle: isZh
        ? '决定“开仓后先挂哪些单”'
        : 'Decides which exchange orders are placed after entry',
      body: isZh
        ? 'Full TP/SL 与 Ladder TP/SL 负责尽快把止损/止盈委托挂到交易所；当 Drawdown 接管止盈侧时，这一层通常只保留止损侧。'
        : 'Full TP/SL and Ladder TP/SL place exchange-side protection orders quickly after entry. When Drawdown owns the TP side, this layer usually keeps only stop-loss protection.',
      tone: '#0ECB81',
    },
    {
      title: isZh ? '运行态盈利控制层' : 'Runtime profit-control layer',
      subtitle: isZh
        ? '决定“盈利后怎么锁利润 / 怎么减仓”'
        : 'Decides how gains are locked and reduced after profit appears',
      body: isZh
        ? 'Drawdown Take Profit 是主盈利控制链；Break-even Stop 是附加止损层，不替代 Drawdown，也不替代长期止损。'
        : 'Drawdown Take Profit is the primary profit-control path. Break-even Stop is an extra stop layer; it does not replace Drawdown or the long-lived stop-loss structure.',
      tone: '#A855F7',
    },
  ]

  return (
    <div className="space-y-6">
      <div
        className="p-4 rounded-lg"
        style={{ background: '#0B0E11', border: '1px solid #F0B90B33' }}
      >
        <div className="flex items-start gap-3">
          <ShieldCheck
            className="w-5 h-5 mt-0.5"
            style={{ color: '#F0B90B' }}
          />
          <div>
            <h3 className="font-medium mb-1" style={{ color: '#EAECEF' }}>
              {isZh
                ? '交易保护 / 盈利控制'
                : 'Trading Protection / Profit Control'}
            </h3>
            <p className="text-xs" style={{ color: '#848E9C' }}>
              {isZh
                ? '这里同时包含两类能力：一类是开仓后尽快挂到交易所的保护委托，另一类是系统在持仓期间持续监控并动态执行的运行态保护。'
                : 'This section contains both exchange-order-based protections and runtime-monitored protections enforced by the system while positions are open.'}
            </p>
            <p className="text-xs mt-2" style={{ color: '#AAB2BD' }}>
              {isZh
                ? '它不负责证明“为什么能开这笔单”。那部分由 Entry Structure + entry_protection_rationale 提供，并进入审计卡片/面板；这里负责的是“开了以后怎么保、怎么止盈、怎么减仓”。'
                : 'This section does not prove why a trade is allowed. That evidence comes from Entry Structure + entry_protection_rationale and flows into the audit cards/panels. Protection is about what happens after the position is opened: how to defend, take profit, and reduce risk.'}
            </p>
          </div>
        </div>
      </div>

      <ProtectionArbitrationBanner config={config} language={language} />

      <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
        {protectionLayerCards.map((item) => (
          <div
            key={item.title}
            className="p-3 rounded-lg"
            style={{
              background: '#11161C',
              border: `1px solid ${item.tone}33`,
            }}
          >
            <div className="text-sm font-medium" style={{ color: '#EAECEF' }}>
              {item.title}
            </div>
            <div className="text-[11px] mt-1" style={{ color: item.tone }}>
              {item.subtitle}
            </div>
            <div className="text-xs mt-2" style={{ color: '#AAB2BD' }}>
              {item.body}
            </div>
          </div>
        ))}
      </div>

      <div
        className="p-3 rounded-lg"
        style={{ background: '#11161C', border: '1px solid #2B3139' }}
      >
        <div className="text-xs" style={{ color: '#C9D1D9' }}>
          {isZh
            ? '建议阅读顺序：先确认开仓门禁是否清楚，再配置委托型保护，最后决定运行态盈利控制链（通常是 Drawdown 主链 + Break-even 辅助层）。'
            : 'Recommended reading order: first confirm the pre-entry gates, then configure exchange-order protection, and finally decide the runtime profit-control path (usually Drawdown as primary + Break-even as auxiliary).'}
        </div>
      </div>

      <div>
        <div className="space-y-4">
          {drawdownOwnsTp && fullTpEnabled && (
            <div
              className="p-3 rounded-lg text-xs"
              style={{
                background: '#2B1619',
                border: '1px solid #41272B',
                color: '#F0B90B',
              }}
            >
              {isZh
                ? 'Drawdown Take Profit 已接管止盈侧，Full TP 会被抑制；Full SL 仍保留为长期止损。'
                : 'Drawdown Take Profit owns the take-profit side, so Full TP is suppressed while Full SL remains active as long-lived stop-loss.'}
            </div>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {statusChip(
              config.full_tp_sl.enabled,
              isZh ? '执行开关' : 'Execution'
            )}
            {statusChip(
              config.full_tp_sl.mode === 'ai',
              isZh ? '整体 AI' : 'Global AI'
            )}
            {statusChip(
              config.full_tp_sl.take_profit_enabled !== false,
              isZh ? 'TP 侧开启' : 'TP side on'
            )}
            {statusChip(
              config.full_tp_sl.stop_loss_enabled !== false,
              isZh ? 'SL 侧开启' : 'SL side on'
            )}
            {statusChip(
              !!config.full_tp_sl.fallback_max_loss_enabled,
              isZh ? '兜底开启' : 'Fallback on'
            )}
            {statusChip(
              config.full_tp_sl.take_profit.mode === 'ai',
              isZh ? 'TP 由 AI' : 'TP via AI'
            )}
            {statusChip(
              config.full_tp_sl.stop_loss.mode === 'ai',
              isZh ? 'SL 由 AI' : 'SL via AI'
            )}
          </div>
          <div className="text-xs" style={{ color: '#848E9C' }}>
            {fullStateSummary}
          </div>
          {fullModeMismatch && (
            <div
              className="p-3 rounded-lg text-xs"
              style={{
                background: '#11161C',
                border: '1px solid #2B3139',
                color: '#F0B90B',
              }}
            >
              {isZh
                ? '注意：当前 Full 的“整体模式”是 AI，但“执行开关”仍关闭。页面会保留 AI 模式配置，但运行时不会实际挂 Full 保护单，直到你打开执行开关。'
                : 'Note: Full global mode is AI, but execution is still disabled. The page preserves the AI setting, but runtime will not place Full protection orders until execution is enabled.'}
            </div>
          )}

          <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
            <div className="p-4 rounded-lg" style={cardStyle(fullEnabled)}>
              <div className="flex items-center justify-between gap-3 mb-2">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? '执行' : 'Execution'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh ? '启用后才会真正挂单' : 'Required to place orders'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={config.full_tp_sl.enabled}
                  onChange={(e) => updateFull('enabled', e.target.checked)}
                  disabled={disabled}
                  className="h-4 w-4 accent-yellow-500"
                />
              </div>
              <select
                value={config.full_tp_sl.mode}
                onChange={(e) =>
                  updateFull('mode', e.target.value as FullTPSLConfig['mode'])
                }
                disabled={disabled || !fullEnabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {protectionModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
            </div>

            <div
              className="p-4 rounded-lg space-y-2"
              style={cardStyle(fullTpActive)}
            >
              <div className="flex items-center justify-between gap-3">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? 'TP 侧' : 'TP Side'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh ? '侧边开关 + 模式' : 'Side toggle + mode'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={config.full_tp_sl.take_profit_enabled !== false}
                  onChange={(e) =>
                    updateFull('take_profit_enabled', e.target.checked)
                  }
                  disabled={disabled || !fullEnabled}
                  className="h-4 w-4 accent-green-500"
                />
              </div>
              <select
                value={config.full_tp_sl.take_profit.mode}
                onChange={(e) =>
                  updateFull(
                    'take_profit',
                    updateValueSource(config.full_tp_sl.take_profit, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !fullTpActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {valueModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
              {config.full_tp_sl.take_profit.mode === 'manual' &&
                fullTpActive && (
                  <>
                    <input
                      type="number"
                      min={0}
                      step={0.1}
                      value={config.full_tp_sl.take_profit.value}
                      onChange={(e) =>
                        updateFull(
                          'take_profit',
                          updateValueSource(config.full_tp_sl.take_profit, {
                            value: parseFloat(e.target.value) || 0,
                          })
                        )
                      }
                      disabled={disabled}
                      className="w-full px-3 py-2 rounded"
                      style={inputStyle}
                    />
                    <div className="text-[11px]" style={{ color: '#848E9C' }}>
                      {isZh
                        ? '单位：Price Move % from Entry'
                        : 'Unit: Price Move % from Entry'}
                    </div>
                  </>
                )}
            </div>

            <div
              className="p-4 rounded-lg space-y-2"
              style={cardStyle(fullSlActive)}
            >
              <div className="flex items-center justify-between gap-3">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? 'SL 侧' : 'SL Side'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh ? '侧边开关 + 模式' : 'Side toggle + mode'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={config.full_tp_sl.stop_loss_enabled !== false}
                  onChange={(e) =>
                    updateFull('stop_loss_enabled', e.target.checked)
                  }
                  disabled={disabled || !fullEnabled}
                  className="h-4 w-4 accent-red-500"
                />
              </div>
              <select
                value={config.full_tp_sl.stop_loss.mode}
                onChange={(e) =>
                  updateFull(
                    'stop_loss',
                    updateValueSource(config.full_tp_sl.stop_loss, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !fullSlActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {valueModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
              {config.full_tp_sl.stop_loss.mode === 'manual' &&
                fullSlActive && (
                  <>
                    <input
                      type="number"
                      min={0}
                      step={0.1}
                      value={config.full_tp_sl.stop_loss.value}
                      onChange={(e) =>
                        updateFull(
                          'stop_loss',
                          updateValueSource(config.full_tp_sl.stop_loss, {
                            value: parseFloat(e.target.value) || 0,
                          })
                        )
                      }
                      disabled={disabled}
                      className="w-full px-3 py-2 rounded"
                      style={inputStyle}
                    />
                    <div className="text-[11px]" style={{ color: '#848E9C' }}>
                      {isZh
                        ? '单位：Price Move % from Entry'
                        : 'Unit: Price Move % from Entry'}
                    </div>
                  </>
                )}
            </div>

            <div
              className="p-4 rounded-lg space-y-2"
              style={cardStyle(fullFallbackActive)}
            >
              <div className="flex items-center justify-between gap-3">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? '兜底' : 'Fallback'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh ? '额外最大损失保护' : 'Extra max-loss guard'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={!!config.full_tp_sl.fallback_max_loss_enabled}
                  onChange={(e) =>
                    updateFull('fallback_max_loss_enabled', e.target.checked)
                  }
                  disabled={disabled || !fullEnabled}
                  className="h-4 w-4 accent-yellow-500"
                />
              </div>
              <select
                value={config.full_tp_sl.fallback_max_loss.mode}
                onChange={(e) =>
                  updateFull(
                    'fallback_max_loss',
                    updateValueSource(config.full_tp_sl.fallback_max_loss, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !fullFallbackActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {(['disabled', 'manual'] as const).map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
              {config.full_tp_sl.fallback_max_loss.mode === 'manual' &&
                fullFallbackActive && (
                  <>
                    <input
                      type="number"
                      min={0}
                      step={0.1}
                      value={config.full_tp_sl.fallback_max_loss.value}
                      onChange={(e) =>
                        updateFull(
                          'fallback_max_loss',
                          updateValueSource(
                            config.full_tp_sl.fallback_max_loss,
                            { value: parseFloat(e.target.value) || 0 }
                          )
                        )
                      }
                      disabled={disabled}
                      className="w-full px-3 py-2 rounded"
                      style={inputStyle}
                    />
                    <div className="text-[11px]" style={{ color: '#848E9C' }}>
                      {isZh
                        ? '单位：Price Move % from Entry'
                        : 'Unit: Price Move % from Entry'}
                    </div>
                  </>
                )}
            </div>
          </div>
        </div>
      </div>

      <div>
        <div className="flex items-center gap-2 mb-4">
          <Layers className="w-5 h-5" style={{ color: '#60A5FA' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {isZh
              ? 'Ladder TP/SL（分批委托型保护）'
              : 'Ladder TP/SL (Ladder Order Protection)'}
          </h3>
        </div>

        <div className="space-y-4">
          {ladderTpEnabled && drawdownOwnsTp && (
            <div
              className="p-3 rounded-lg text-xs"
              style={{
                background: '#2B1619',
                border: '1px solid #41272B',
                color: '#F0B90B',
              }}
            >
              {isZh
                ? 'Drawdown Take Profit 已接管止盈侧，Ladder TP 会被抑制；Ladder SL 继续保留。'
                : 'Drawdown Take Profit owns the take-profit side, so Ladder TP is suppressed while Ladder SL remains active.'}
            </div>
          )}

          <div className="flex flex-wrap items-center gap-2">
            {statusChip(
              config.ladder_tp_sl.enabled,
              isZh ? '执行开关' : 'Execution'
            )}
            {statusChip(
              config.ladder_tp_sl.mode === 'ai',
              isZh ? '整体 AI' : 'Global AI'
            )}
            {statusChip(
              config.ladder_tp_sl.take_profit_enabled,
              isZh ? 'TP 侧开启' : 'TP side on'
            )}
            {statusChip(
              config.ladder_tp_sl.stop_loss_enabled,
              isZh ? 'SL 侧开启' : 'SL side on'
            )}
          </div>
          <div className="text-xs" style={{ color: '#848E9C' }}>
            {ladderStateSummary}
          </div>
          {ladderModeMismatch && (
            <div
              className="p-3 rounded-lg text-xs"
              style={{
                background: '#11161C',
                border: '1px solid #2B3139',
                color: '#F0B90B',
              }}
            >
              {isZh
                ? '注意：当前 Ladder 的“整体模式”是 AI，但“执行开关”仍关闭。页面会保留 AI 模式配置，但运行时不会实际挂 Ladder 保护单，直到你打开执行开关。'
                : 'Note: Ladder global mode is AI, but execution is still disabled. The page preserves the AI setting, but runtime will not place Ladder protection orders until execution is enabled.'}
            </div>
          )}

          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div className="p-4 rounded-lg" style={cardStyle(ladderEnabled)}>
              <div className="flex items-center justify-between gap-3 mb-2">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? '执行' : 'Execution'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh
                      ? '启用后才会生成委托'
                      : 'Required to generate orders'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={config.ladder_tp_sl.enabled}
                  onChange={(e) => updateLadder('enabled', e.target.checked)}
                  disabled={disabled}
                  className="h-4 w-4 accent-blue-500"
                />
              </div>
              <select
                value={config.ladder_tp_sl.mode}
                onChange={(e) =>
                  updateLadder(
                    'mode',
                    e.target.value as LadderTPSLConfig['mode']
                  )
                }
                disabled={disabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {protectionModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
            </div>
            <div
              className="p-4 rounded-lg space-y-2"
              style={cardStyle(ladderTpActive)}
            >
              <div className="flex items-center justify-between gap-3 mb-2">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? 'TP 侧' : 'TP Side'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh ? '价格 / 仓位模式' : 'Price / size mode'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={config.ladder_tp_sl.take_profit_enabled}
                  onChange={(e) =>
                    updateLadder('take_profit_enabled', e.target.checked)
                  }
                  disabled={disabled || !ladderEnabled}
                  className="h-4 w-4 accent-green-500"
                />
              </div>
              <select
                value={config.ladder_tp_sl.take_profit_price.mode}
                onChange={(e) =>
                  updateLadder(
                    'take_profit_price',
                    updateValueSource(config.ladder_tp_sl.take_profit_price, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !ladderTpActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {valueModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {isZh
                      ? `价格：${modeLabel(mode)}`
                      : `Price: ${modeLabel(mode)}`}
                  </option>
                ))}
              </select>
              <select
                value={config.ladder_tp_sl.take_profit_size.mode}
                onChange={(e) =>
                  updateLadder(
                    'take_profit_size',
                    updateValueSource(config.ladder_tp_sl.take_profit_size, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !ladderTpActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {valueModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {isZh
                      ? `仓位：${modeLabel(mode)}`
                      : `Size: ${modeLabel(mode)}`}
                  </option>
                ))}
              </select>
            </div>
            <div
              className="p-4 rounded-lg space-y-2"
              style={cardStyle(ladderSlActive)}
            >
              <div className="flex items-center justify-between gap-3 mb-2">
                <div>
                  <label className="block text-sm" style={{ color: '#EAECEF' }}>
                    {isZh ? 'SL 侧' : 'SL Side'}
                  </label>
                  <div style={compactHintStyle}>
                    {isZh ? '价格 / 仓位模式' : 'Price / size mode'}
                  </div>
                </div>
                <input
                  type="checkbox"
                  checked={config.ladder_tp_sl.stop_loss_enabled}
                  onChange={(e) =>
                    updateLadder('stop_loss_enabled', e.target.checked)
                  }
                  disabled={disabled || !ladderEnabled}
                  className="h-4 w-4 accent-red-500"
                />
              </div>
              <select
                value={config.ladder_tp_sl.stop_loss_price.mode}
                onChange={(e) =>
                  updateLadder(
                    'stop_loss_price',
                    updateValueSource(config.ladder_tp_sl.stop_loss_price, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !ladderSlActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {valueModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {isZh
                      ? `价格：${modeLabel(mode)}`
                      : `Price: ${modeLabel(mode)}`}
                  </option>
                ))}
              </select>
              <select
                value={config.ladder_tp_sl.stop_loss_size.mode}
                onChange={(e) =>
                  updateLadder(
                    'stop_loss_size',
                    updateValueSource(config.ladder_tp_sl.stop_loss_size, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !ladderSlActive}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {valueModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {isZh
                      ? `仓位：${modeLabel(mode)}`
                      : `Size: ${modeLabel(mode)}`}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div
              className="p-4 rounded-lg"
              style={cardStyle(ladderFallbackActive)}
            >
              <label
                className="block text-sm mb-2"
                style={{ color: '#EAECEF' }}
              >
                {isZh ? 'Ladder 兜底' : 'Ladder Fallback'}
              </label>
              <select
                value={config.ladder_tp_sl.fallback_max_loss.mode}
                onChange={(e) =>
                  updateLadder(
                    'fallback_max_loss',
                    updateValueSource(config.ladder_tp_sl.fallback_max_loss, {
                      mode: e.target.value as ProtectionMode,
                    })
                  )
                }
                disabled={disabled || !ladderEnabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {(['disabled', 'manual'] as const).map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
              {config.ladder_tp_sl.fallback_max_loss.mode === 'manual' &&
                ladderEnabled && (
                  <input
                    type="number"
                    min={0}
                    step={0.1}
                    value={config.ladder_tp_sl.fallback_max_loss.value}
                    onChange={(e) =>
                      updateLadder(
                        'fallback_max_loss',
                        updateValueSource(
                          config.ladder_tp_sl.fallback_max_loss,
                          { value: parseFloat(e.target.value) || 0 }
                        )
                      )
                    }
                    disabled={disabled}
                    className="w-full mt-2 px-3 py-2 rounded"
                    style={inputStyle}
                  />
                )}
            </div>
          </div>

          {infoBlock(
            isZh ? 'Ladder 参数说明' : 'Ladder Parameter Guide',
            isZh
              ? '每一档都是一组“触发幅度 + 平仓比例”。只有配置了规则，Ladder 才会生成多档委托；当 Drawdown 接管止盈侧时，仅 Ladder SL 保留。'
              : 'Each ladder level is a trigger plus close ratio. Ladder generates multi-level orders only when rules exist; if Drawdown owns the TP side, only Ladder SL remains active.',
            isZh
              ? '建议：先控制总平仓比例，再决定每档分配。'
              : 'Recommendation: control the total close ratio first, then distribute it across levels.'
          )}

          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <div className="text-sm font-medium" style={{ color: '#EAECEF' }}>
                {isZh ? '分批规则' : 'Ladder Rules'}
              </div>
              <button
                type="button"
                onClick={addLadderRule}
                disabled={disabled}
                className="inline-flex items-center gap-1 px-3 py-1.5 rounded text-sm bg-[#1E2329] border border-[#2B3139] text-[#EAECEF] hover:border-[#F0B90B]"
              >
                <Plus className="w-4 h-4" />
                {isZh ? '新增一档' : 'Add Level'}
              </button>
            </div>

            {ladderRules.length === 0 && (
              <div className="p-3 rounded-lg text-xs" style={helpCardStyle}>
                {isZh
                  ? '当前还没有配置任何 Ladder 规则，所以不会真正生成分批止盈/止损委托。请至少新增 1 档规则。'
                  : 'No ladder rules configured yet, so no ladder protection orders will be generated.'}
              </div>
            )}

            {ladderRules.map((rule, index) => (
              <div
                key={index}
                className="p-4 rounded-lg space-y-3"
                style={sectionStyle}
              >
                <div className="flex items-center justify-between">
                  <div
                    className="text-sm font-medium"
                    style={{ color: '#EAECEF' }}
                  >
                    {isZh ? `第 ${index + 1} 档` : `Level ${index + 1}`}
                  </div>
                  <button
                    type="button"
                    onClick={() => removeLadderRule(index)}
                    disabled={disabled}
                    className="inline-flex items-center gap-1 px-2 py-1 rounded text-xs text-[#F6465D] border border-[#41272B] hover:bg-[#2B1619]"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                    {isZh ? '删除' : 'Remove'}
                  </button>
                </div>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <div>
                    <div className="flex items-center justify-between mb-1">
                      <label
                        className="block text-xs"
                        style={{ color: '#848E9C' }}
                      >
                        {rule.take_profit_unit === 'atr'
                          ? isZh
                            ? '止盈触发 (ATR倍数)'
                            : 'TP Trigger (ATR×)'
                          : isZh
                            ? '止盈触发 %'
                            : 'TP Trigger %'}
                      </label>
                      {unitToggle(rule.take_profit_unit, (u) =>
                        updateLadderRule(index, { take_profit_unit: u })
                      )}
                    </div>
                    {config.ladder_tp_sl.take_profit_price.mode === 'manual' ? (
                      <input
                        type="number"
                        min={0}
                        step={0.1}
                        value={rule.take_profit_pct || 0}
                        onChange={(e) =>
                          updateLadderRule(index, {
                            take_profit_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    ) : (
                      <div
                        className="px-3 py-2 rounded text-xs"
                        style={helpCardStyle}
                      >
                        {isZh
                          ? '由 AI 生成或已禁用'
                          : 'Generated by AI or disabled'}
                      </div>
                    )}
                  </div>
                  <div>
                    <label
                      className="block text-xs mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '止盈平仓比例 %' : 'TP Close Ratio %'}
                    </label>
                    {config.ladder_tp_sl.take_profit_size.mode === 'manual' ? (
                      <input
                        type="number"
                        min={0}
                        max={100}
                        step={1}
                        value={rule.take_profit_close_ratio_pct || 0}
                        onChange={(e) =>
                          updateLadderRule(index, {
                            take_profit_close_ratio_pct:
                              parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    ) : (
                      <div
                        className="px-3 py-2 rounded text-xs"
                        style={helpCardStyle}
                      >
                        {isZh
                          ? '由 AI 生成或已禁用'
                          : 'Generated by AI or disabled'}
                      </div>
                    )}
                  </div>
                  <div>
                    <div className="flex items-center justify-between mb-1">
                      <label
                        className="block text-xs"
                        style={{ color: '#848E9C' }}
                      >
                        {rule.stop_loss_unit === 'structural'
                          ? isZh
                            ? '止损触发 (结构位·倍数为兜底)'
                            : 'SL Trigger (Structural · value=fallback)'
                          : rule.stop_loss_unit === 'atr'
                            ? isZh
                              ? '止损触发 (ATR倍数)'
                              : 'SL Trigger (ATR×)'
                            : isZh
                              ? '止损触发 %'
                              : 'SL Trigger %'}
                      </label>
                      {unitToggle(
                        rule.stop_loss_unit,
                        (u) => updateLadderRule(index, { stop_loss_unit: u }),
                        true
                      )}
                    </div>
                    {config.ladder_tp_sl.stop_loss_price.mode === 'manual' ? (
                      <input
                        type="number"
                        min={0}
                        step={0.1}
                        value={rule.stop_loss_pct || 0}
                        onChange={(e) =>
                          updateLadderRule(index, {
                            stop_loss_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    ) : (
                      <div
                        className="px-3 py-2 rounded text-xs"
                        style={helpCardStyle}
                      >
                        {isZh
                          ? '由 AI 生成或已禁用'
                          : 'Generated by AI or disabled'}
                      </div>
                    )}
                  </div>
                  <div>
                    <label
                      className="block text-xs mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '止损平仓比例 %' : 'SL Close Ratio %'}
                    </label>
                    {config.ladder_tp_sl.stop_loss_size.mode === 'manual' ? (
                      <input
                        type="number"
                        min={0}
                        max={100}
                        step={1}
                        value={rule.stop_loss_close_ratio_pct || 0}
                        onChange={(e) =>
                          updateLadderRule(index, {
                            stop_loss_close_ratio_pct:
                              parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    ) : (
                      <div
                        className="px-3 py-2 rounded text-xs"
                        style={helpCardStyle}
                      >
                        {isZh
                          ? '由 AI 生成或已禁用'
                          : 'Generated by AI or disabled'}
                      </div>
                    )}
                  </div>
                </div>
              </div>
            ))}

            {/* Structural SL config — shown when any SL rule uses the structural unit */}
            {anySLStructural && (
              <div
                className="mt-3 p-3 rounded-lg space-y-3"
                style={helpCardStyle}
              >
                <div
                  className="text-xs font-semibold"
                  style={{ color: '#EAECEF' }}
                >
                  {isZh
                    ? '结构位止损设置（锚定入场前震荡区边界）'
                    : 'Structural Stop-Loss (anchored to pre-entry range boundary)'}
                </div>
                <div className="text-xs" style={{ color: '#AAB2BD' }}>
                  {isZh
                    ? '止损放在入场前震荡区边界外（多头=区间低点，空头=区间高点），按下方倍数钳制。窄幅震荡里更紧（突破损失更小），地板防插针。回测：在现有TP/BE/DD上替换固定4.5ATR止损,全盘约+60、震荡区约+58,突破捕获不变。'
                    : 'Places the stop just beyond the pre-entry range boundary (swing low for long / high for short), clamped by the multiples below. Backtest: replacing the fixed 4.5 ATR stop on the current stack gains ~+60 overall / ~+58 in the ranging regime, breakout unchanged.'}
                </div>
                <label
                  className="flex items-center gap-2 text-xs"
                  style={{ color: '#EAECEF' }}
                >
                  <input
                    type="checkbox"
                    checked={structuralSL.enabled ?? false}
                    onChange={(e) =>
                      updateStructuralSL({ enabled: e.target.checked })
                    }
                    disabled={disabled}
                  />
                  {isZh ? '启用结构位止损' : 'Enable structural stop-loss'}
                </label>
                <div className="grid grid-cols-3 gap-2">
                  <div>
                    <label
                      className="block text-[11px] mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '地板 (ATR倍数)' : 'Floor (ATR×)'}
                    </label>
                    <input
                      type="number"
                      min={0}
                      step={0.1}
                      value={structuralSL.floor_atr_mul ?? 1.5}
                      onChange={(e) =>
                        updateStructuralSL({
                          floor_atr_mul: parseFloat(e.target.value) || 0,
                        })
                      }
                      disabled={disabled}
                      className="w-full px-2 py-1.5 rounded text-xs"
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label
                      className="block text-[11px] mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '兜底上限 (ATR倍数)' : 'Backstop (ATR×)'}
                    </label>
                    <input
                      type="number"
                      min={0}
                      step={0.1}
                      value={structuralSL.backstop_atr_mul ?? 4.5}
                      onChange={(e) =>
                        updateStructuralSL({
                          backstop_atr_mul: parseFloat(e.target.value) || 0,
                        })
                      }
                      disabled={disabled}
                      className="w-full px-2 py-1.5 rounded text-xs"
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label
                      className="block text-[11px] mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '回看K线数' : 'Lookback bars'}
                    </label>
                    <input
                      type="number"
                      min={1}
                      step={1}
                      value={structuralSL.lookback_bars ?? 24}
                      onChange={(e) =>
                        updateStructuralSL({
                          lookback_bars: parseInt(e.target.value, 10) || 0,
                        })
                      }
                      disabled={disabled}
                      className="w-full px-2 py-1.5 rounded text-xs"
                      style={inputStyle}
                    />
                  </div>
                </div>
                <div className="grid grid-cols-3 gap-2">
                  <div>
                    <label
                      className="block text-[11px] mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '摆动强度 (分形)' : 'Pivot strength'}
                    </label>
                    <input
                      type="number"
                      min={1}
                      step={1}
                      value={structuralSL.pivot_strength ?? 2}
                      onChange={(e) =>
                        updateStructuralSL({
                          pivot_strength: parseInt(e.target.value, 10) || 0,
                        })
                      }
                      disabled={disabled}
                      className="w-full px-2 py-1.5 rounded text-xs"
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label
                      className="block text-[11px] mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '无结构兜底 (ATR倍数)' : 'Fallback (ATR×)'}
                    </label>
                    <input
                      type="number"
                      min={0}
                      step={0.1}
                      value={structuralSL.fallback_atr_mul ?? 3.0}
                      onChange={(e) =>
                        updateStructuralSL({
                          fallback_atr_mul: parseFloat(e.target.value) || 0,
                        })
                      }
                      disabled={disabled}
                      className="w-full px-2 py-1.5 rounded text-xs"
                      style={inputStyle}
                    />
                  </div>
                  <div>
                    <label
                      className="block text-[11px] mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '兜底RR上限 (×TP)' : 'Fallback RR cap (×TP)'}
                    </label>
                    <input
                      type="number"
                      min={0}
                      max={1}
                      step={0.05}
                      value={structuralSL.fallback_rr_cap_ratio ?? 0.8}
                      onChange={(e) =>
                        updateStructuralSL({
                          fallback_rr_cap_ratio:
                            parseFloat(e.target.value) || 0,
                        })
                      }
                      disabled={disabled}
                      className="w-full px-2 py-1.5 rounded text-xs"
                      style={inputStyle}
                    />
                  </div>
                </div>
                <div className="text-[11px]" style={{ color: '#848E9C' }}>
                  {isZh
                    ? '摆动强度：判定摆动高/低点时两侧各比较的K线数（越大越显著）。无结构兜底：入场价附近找不到近处止损结构时，用这个更紧的ATR倍数代替宽兜底。兜底RR上限：兜底止损距离必须 < 该比例×TP目标涨幅（0.8 → RR≥1.25），地板仍为硬下限。'
                    : 'Pivot strength: bars compared on each side to qualify a swing high/low. Fallback: tighter ATR multiple used instead of the wide backstop when no near structure exists. RR cap: fallback stop must stay below ratio × TP target (0.8 → RR ≥ 1.25); floor remains a hard minimum.'}
                </div>
                <label
                  className="flex items-start gap-2 text-xs"
                  style={{ color: '#EAECEF' }}
                >
                  <input
                    type="checkbox"
                    checked={structuralSL.close_confirm ?? false}
                    onChange={(e) =>
                      updateStructuralSL({ close_confirm: e.target.checked })
                    }
                    disabled={disabled}
                    className="mt-0.5"
                  />
                  <span>
                    {isZh
                      ? '收盘确认（第二期）：仅当K线收盘跌破结构位才平仓（防插针）。交易所挂单移到兜底上限做宕机保命，紧止损由引擎轮询执行。'
                      : 'Close-confirm (Phase 2): exit only when a bar CLOSES beyond the boundary (anti stop-hunt). Resting stop parks at the backstop as a downtime net; the tight stop runs via engine poll.'}
                  </span>
                </label>

                {/* Method 4: asset-adaptive confirmation timeframe — requires close-confirm */}
                <label
                  className="flex items-start gap-2 text-xs"
                  style={{ color: '#EAECEF' }}
                >
                  <input
                    type="checkbox"
                    checked={structuralSL.asset_adaptive_confirm_tf ?? false}
                    onChange={(e) =>
                      updateStructuralSL({
                        asset_adaptive_confirm_tf: e.target.checked,
                      })
                    }
                    disabled={
                      disabled || !(structuralSL.close_confirm ?? false)
                    }
                    className="mt-0.5"
                  />
                  <span>
                    {isZh
                      ? '资产自适应确认周期（方案4）：加密货币在更细一档（约2×，如1h→30m）确认收盘跌破，回测更优（+10.81 PnL，回撤88→77）；股票/商品保持原生周期（细周期插针最严重）。需先开启收盘确认。'
                      : 'Asset-adaptive confirm TF (Method 4): crypto confirms the close-confirm breach ~2× finer (e.g. 1h→30m) — backtest +10.81 PnL, drawdown 88→77; stocks/commodities keep native (fine TFs whipsaw worst). Requires close-confirm.'}
                  </span>
                </label>

                {/* Market Structure Map: prefer proven order-block levels over raw fractals */}
                <label
                  className="flex items-start gap-2 text-xs"
                  style={{ color: '#EAECEF' }}
                >
                  <input
                    type="checkbox"
                    checked={structuralSL.prefer_proven_levels ?? true}
                    onChange={(e) =>
                      updateStructuralSL({
                        prefer_proven_levels: e.target.checked,
                      })
                    }
                    disabled={disabled}
                    className="mt-0.5"
                  />
                  <span>
                    {isZh
                      ? '优先采用验证过的结构位（市场结构图）：止损优先锚定入场保护侧的订单块边缘（多单用需求块高点/空单用供应块低点），而非裸摆动枢轴——订单块是脉冲突破的起点，是市场真正守住的位置。仅当不比最近枢轴更宽时才采用（“不放宽”保证净结果非负）。回测：claude 1h +1.68 PnL/回撤持平，Claude-R 15m +0.62 PnL/回撤-0.62。'
                      : 'Prefer proven structural levels (Market Structure Map): anchor the stop to a protective-side order-block edge (demand-block high for a long / supply-block low for a short) over the raw fractal swing — an order block is the origin of an impulsive break, a level the market defended. Adopted only when it does not widen the stop past the nearest pivot (never-widen → non-negative). Backtest: claude 1h +1.68 PnL / DD flat, Claude-R 15m +0.62 PnL / DD -0.62.'}
                  </span>
                </label>

                {/* Ratcheting (trailing) structural stop — requires close-confirm */}
                <label
                  className="flex items-start gap-2 text-xs"
                  style={{ color: '#EAECEF' }}
                >
                  <input
                    type="checkbox"
                    checked={structuralSL.trail_enabled ?? false}
                    onChange={(e) =>
                      updateStructuralSL({ trail_enabled: e.target.checked })
                    }
                    disabled={
                      disabled || !(structuralSL.close_confirm ?? false)
                    }
                    className="mt-0.5"
                  />
                  <span>
                    {isZh
                      ? '结构位跟随（棘轮）：每根收盘K线把止损边界向锁盈方向收紧（只紧不松），以入场结构位为起点。需先开启收盘确认。'
                      : 'Ratcheting trail: each closed bar tightens the stop boundary toward locking profit (never looser), starting from the entry structural level. Requires close-confirm.'}
                  </span>
                </label>
                {(structuralSL.trail_enabled ?? false) && (
                  <div className="pl-6 space-y-3">
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <label
                          className="block text-xs mb-1"
                          style={{ color: '#B7BDC6' }}
                        >
                          {isZh ? '跟随周期模式' : 'Trail mode'}
                        </label>
                        <select
                          value={structuralSL.trail_mode ?? 'current'}
                          onChange={(e) =>
                            updateStructuralSL({ trail_mode: e.target.value })
                          }
                          disabled={disabled}
                          className="w-full px-2 py-1 rounded text-xs"
                          style={{
                            background: '#0B0E11',
                            color: '#EAECEF',
                            border: '1px solid #2B3139',
                          }}
                        >
                          <option value="current">
                            {isZh ? '当前周期（最紧）' : 'Current (tightest)'}
                          </option>
                          <option value="higher">
                            {isZh
                              ? '高周期（最抗震）'
                              : 'Higher (whipsaw-resistant)'}
                          </option>
                          <option value="both">
                            {isZh ? '两者取松（折中）' : 'Both (looser of two)'}
                          </option>
                        </select>
                      </div>
                      <div>
                        <label
                          className="block text-xs mb-1"
                          style={{ color: '#B7BDC6' }}
                        >
                          {isZh ? '高周期聚合倍数' : 'Higher-TF mult'}
                        </label>
                        <input
                          type="number"
                          step="1"
                          min="2"
                          value={structuralSL.trail_higher_mult ?? 4}
                          onChange={(e) =>
                            updateStructuralSL({
                              trail_higher_mult: parseInt(e.target.value) || 0,
                            })
                          }
                          disabled={disabled}
                          className="w-full px-2 py-1 rounded text-xs"
                          style={{
                            background: '#0B0E11',
                            color: '#EAECEF',
                            border: '1px solid #2B3139',
                          }}
                        />
                      </div>
                    </div>
                    <div className="grid grid-cols-3 gap-3">
                      <div>
                        <label
                          className="block text-xs mb-1"
                          style={{ color: '#B7BDC6' }}
                        >
                          {isZh ? '容差(ATR)' : 'Tolerance (ATR)'}
                        </label>
                        <input
                          type="number"
                          step="0.1"
                          min="0"
                          value={structuralSL.trail_tol_atr ?? 0.5}
                          onChange={(e) =>
                            updateStructuralSL({
                              trail_tol_atr: parseFloat(e.target.value) || 0,
                            })
                          }
                          disabled={disabled}
                          className="w-full px-2 py-1 rounded text-xs"
                          style={{
                            background: '#0B0E11',
                            color: '#EAECEF',
                            border: '1px solid #2B3139',
                          }}
                        />
                      </div>
                      <div>
                        <label
                          className="block text-xs mb-1"
                          style={{ color: '#B7BDC6' }}
                        >
                          {isZh ? '启动盈利(ATR)' : 'Min profit (ATR)'}
                        </label>
                        <input
                          type="number"
                          step="0.5"
                          min="0"
                          value={structuralSL.trail_min_profit_atr ?? 1.0}
                          onChange={(e) =>
                            updateStructuralSL({
                              trail_min_profit_atr:
                                parseFloat(e.target.value) || 0,
                            })
                          }
                          disabled={disabled}
                          className="w-full px-2 py-1 rounded text-xs"
                          style={{
                            background: '#0B0E11',
                            color: '#EAECEF',
                            border: '1px solid #2B3139',
                          }}
                        />
                      </div>
                      <div>
                        <label
                          className="block text-xs mb-1"
                          style={{ color: '#B7BDC6' }}
                        >
                          {isZh ? '最多追几次(0=不限)' : 'Max ratchets (0=∞)'}
                        </label>
                        <input
                          type="number"
                          step="1"
                          min="0"
                          value={structuralSL.trail_max_ratchets ?? 0}
                          onChange={(e) =>
                            updateStructuralSL({
                              trail_max_ratchets: parseInt(e.target.value) || 0,
                            })
                          }
                          disabled={disabled}
                          className="w-full px-2 py-1 rounded text-xs"
                          style={{
                            background: '#0B0E11',
                            color: '#EAECEF',
                            border: '1px solid #2B3139',
                          }}
                        />
                      </div>
                    </div>
                    <div className="flex items-center gap-4">
                      <label
                        className="flex items-center gap-2 text-xs"
                        style={{ color: '#EAECEF' }}
                      >
                        <input
                          type="checkbox"
                          checked={structuralSL.trail_on_profit ?? true}
                          onChange={(e) =>
                            updateStructuralSL({
                              trail_on_profit: e.target.checked,
                            })
                          }
                          disabled={disabled}
                        />
                        <span>{isZh ? '盈利侧生效' : 'Ratchet in profit'}</span>
                      </label>
                      <label
                        className="flex items-center gap-2 text-xs"
                        style={{ color: '#EAECEF' }}
                      >
                        <input
                          type="checkbox"
                          checked={structuralSL.trail_on_loss ?? false}
                          onChange={(e) =>
                            updateStructuralSL({
                              trail_on_loss: e.target.checked,
                            })
                          }
                          disabled={disabled}
                        />
                        <span>{isZh ? '亏损侧生效' : 'Ratchet in loss'}</span>
                      </label>
                    </div>
                    <div className="text-xs" style={{ color: '#848E9C' }}>
                      {isZh
                        ? '两侧都开=全状态生效；仅开一侧=该侧生效；都关=不追。默认只开盈利侧：亏损中锁紧无法锁住任何利润，只会把失效位拉进噪音区。'
                        : 'Both on = every state; one on = that side only; both off = never. Profit side only by default: a ratchet fired while underwater cannot lock any profit, it only drags the invalidation level into the noise band.'}
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div>
          <div className="flex items-center gap-2 mb-4">
            <Activity className="w-5 h-5" style={{ color: '#A855F7' }} />
            <h3 className="font-medium" style={{ color: '#EAECEF' }}>
              {isZh
                ? 'Drawdown Take Profit（运行态保护）'
                : 'Drawdown Take Profit (Runtime Protection)'}
            </h3>
          </div>
          <div className="p-4 rounded-lg space-y-3" style={sectionStyle}>
            <div className="flex items-center justify-between">
              <label className="block text-sm" style={{ color: '#EAECEF' }}>
                {isZh ? '启用回撤止盈' : 'Enable Drawdown TP'}
              </label>
              <input
                type="checkbox"
                checked={config.drawdown_take_profit.enabled}
                onChange={(e) => updateDrawdown('enabled', e.target.checked)}
                disabled={disabled}
                className="h-4 w-4 accent-purple-500"
              />
            </div>
            <div className="flex flex-wrap items-center gap-2">
              {statusChip(
                config.drawdown_take_profit.enabled,
                isZh ? '执行开关' : 'Execution'
              )}
              {statusChip(
                config.drawdown_take_profit.mode === 'manual',
                isZh ? '手动规则' : 'Manual rules'
              )}
              {statusChip(
                config.drawdown_take_profit.mode === 'ai',
                isZh ? 'AI 规则' : 'AI rules'
              )}
              {statusChip(drawdownOwnsTp, isZh ? '接管止盈侧' : 'Owns TP side')}
            </div>
            <div className="text-xs" style={{ color: '#848E9C' }}>
              {drawdownStateSummary}
            </div>
            {drawdownModeMismatch && (
              <div
                className="p-3 rounded-lg text-xs"
                style={{
                  background: '#11161C',
                  border: '1px solid #2B3139',
                  color: '#F0B90B',
                }}
              >
                {isZh
                  ? '注意：当前 Drawdown 模式是 AI，但“执行开关”仍关闭。页面会保留 AI 规则语义，但运行时不会启用 Drawdown 接管，直到你打开执行开关。'
                  : 'Note: Drawdown mode is AI, but execution is still disabled. The page preserves the AI rule semantics, but runtime will not enable drawdown ownership until execution is turned on.'}
              </div>
            )}
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? '模式' : 'Mode'}
              </label>
              <select
                value={config.drawdown_take_profit.mode}
                onChange={(e) =>
                  updateDrawdown(
                    'mode',
                    e.target.value as DrawdownTakeProfitConfig['mode']
                  )
                }
                disabled={disabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {protectionModeOptions.map((mode) => (
                  <option key={mode} value={mode}>
                    {modeLabel(mode)}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? '引擎语义' : 'Engine Semantics'}
              </label>
              <select
                value={config.drawdown_take_profit.engine_mode || 'manual'}
                onChange={(e) =>
                  updateDrawdown(
                    'engine_mode',
                    e.target.value as DrawdownTakeProfitConfig['engine_mode']
                  )
                }
                disabled={
                  disabled || config.drawdown_take_profit.mode === 'disabled'
                }
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                <option value="manual">
                  {isZh ? '手动固定规则' : 'Manual fixed rules'}
                </option>
                <option value="ai">
                  {isZh ? 'AI 结构驱动' : 'AI structure-driven'}
                </option>
              </select>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {isZh ? '启用 Runner' : 'Runner Enabled'}
                </label>
                <input
                  type="checkbox"
                  checked={Boolean(config.drawdown_take_profit.runner_enabled)}
                  onChange={(e) =>
                    updateDrawdown('runner_enabled', e.target.checked)
                  }
                  disabled={
                    disabled || config.drawdown_take_profit.mode === 'disabled'
                  }
                  className="h-4 w-4 accent-purple-500"
                />
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {isZh ? '最少保留 Runner %' : 'Min Runner Keep %'}
                </label>
                <input
                  type="number"
                  value={config.drawdown_take_profit.min_runner_keep_pct ?? 20}
                  min={0}
                  max={100}
                  step={1}
                  onChange={(e) =>
                    updateDrawdown(
                      'min_runner_keep_pct',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={
                    disabled || config.drawdown_take_profit.mode === 'disabled'
                  }
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {isZh ? '第一阶段最大减仓 %' : 'Max First Reduce %'}
                </label>
                <input
                  type="number"
                  value={config.drawdown_take_profit.max_first_reduce_pct ?? 60}
                  min={0}
                  max={100}
                  step={1}
                  onChange={(e) =>
                    updateDrawdown(
                      'max_first_reduce_pct',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={
                    disabled || config.drawdown_take_profit.mode === 'disabled'
                  }
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
              </div>
            </div>
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? 'Runner 与 BE 关系' : 'Runner vs Break-even Policy'}
              </label>
              <select
                value={
                  config.drawdown_take_profit.break_even_runner_policy ||
                  'fallback_only'
                }
                onChange={(e) =>
                  updateDrawdown(
                    'break_even_runner_policy',
                    e.target
                      .value as DrawdownTakeProfitConfig['break_even_runner_policy']
                  )
                }
                disabled={
                  disabled || config.drawdown_take_profit.mode === 'disabled'
                }
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                <option value="primary">
                  {isZh ? 'BE 仍为主止损' : 'BE remains primary'}
                </option>
                <option value="fallback_only">
                  {isZh ? 'BE 只做兜底' : 'BE fallback only'}
                </option>
                <option value="disabled_for_runner">
                  {isZh ? 'Runner 下禁用 BE' : 'Disable BE for runner'}
                </option>
              </select>
            </div>
            {infoBlock(
              isZh ? '盈利控制主链' : 'Primary profit-control path',
              isZh
                ? 'Drawdown / Native Trailing 接管止盈侧。达到最小利润门槛后，系统按回撤阈值动态保护利润；不再同时依赖 Full / Ladder TP。AI 模式下应由 AI 输出结构化 drawdown 规则，且不得忽略。'
                : 'Drawdown / Native Trailing owns the take-profit side. After the minimum profit gate is reached, the system protects gains using drawdown thresholds instead of relying on Full / Ladder TP at the same time. In AI mode, the model should output structured drawdown rules and must not ignore them.',
              isZh
                ? '建议：把它当成主止盈链路，只保留 Full / Ladder 的止损侧。'
                : 'Recommendation: treat this as the main take-profit path and keep only the stop-loss side from Full / Ladder.'
            )}
            <div className="text-[11px]" style={{ color: '#848E9C' }}>
              {isZh
                ? '每级仓位在开仓时固定分配，各级独立追踪峰值和回撤。T1 触发后其仓位不再参与后续级别。未达到的级别由 Break-even 兜底。每级的仓位/峰值/回撤支持 AI 或手动独立控制。'
                : "Each tier's position is fixed at open. Tiers track peaks independently. After T1 triggers, its allocation exits and does not participate in later tiers. Unreached tiers are covered by break-even. Each dimension (ratio/trigger/drawdown) supports independent AI or manual control."}
            </div>
            {config.drawdown_take_profit.mode === 'ai' && (
              <div className="p-3 rounded-lg text-xs" style={helpCardStyle}>
                {isZh
                  ? 'AI 模式下，这里的规则列表作为结构化占位/回退参考，实际语义是“必须由 AI 输出 drawdown 规则”，而不是由页面固定阈值直接主导。'
                  : 'In AI mode, this rule list acts as structured placeholder/fallback reference data. The intended semantics are “AI must output drawdown rules”, not “the page-owned thresholds directly drive behavior”.'}
              </div>
            )}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <div
                  className="text-sm font-medium"
                  style={{ color: '#EAECEF' }}
                >
                  {isZh ? '回撤止盈规则' : 'Drawdown Rules'}
                </div>
                <button
                  type="button"
                  onClick={addDrawdownRule}
                  disabled={
                    disabled || config.drawdown_take_profit.mode === 'disabled'
                  }
                  className="inline-flex items-center gap-1 px-3 py-1.5 rounded text-sm bg-[#1E2329] border border-[#2B3139] text-[#EAECEF] hover:border-[#F0B90B]"
                >
                  <Plus className="w-4 h-4" />
                  {isZh ? '新增规则' : 'Add Rule'}
                </button>
              </div>

              {drawdownRules.length === 0 && (
                <div className="p-3 rounded-lg text-xs" style={helpCardStyle}>
                  {config.drawdown_take_profit.mode === 'ai'
                    ? isZh
                      ? '当前未填写任何 drawdown 规则占位。建议至少保留 1 条结构化示例/回退规则，方便 AI 配置审阅与后续兼容。'
                      : 'No drawdown placeholder rules are set. In AI mode, keeping at least one structured example/fallback rule is recommended for config review and future compatibility.'
                    : isZh
                      ? '当前还没有配置任何回撤止盈规则。请至少新增 1 条规则。'
                      : 'No drawdown rules configured yet. Add at least one rule.'}
                </div>
              )}

              {drawdownRules.map((rule, index) => (
                <div
                  key={index}
                  className="p-4 rounded-lg space-y-3"
                  style={sectionStyle}
                >
                  <div className="flex items-center justify-between">
                    <div
                      className="text-sm font-medium"
                      style={{ color: '#EAECEF' }}
                    >
                      {isZh
                        ? `T${index + 1} — 第 ${index + 1} 级回撤`
                        : `T${index + 1} — Tier ${index + 1}`}
                    </div>
                    <button
                      type="button"
                      onClick={() => removeDrawdownRule(index)}
                      disabled={
                        disabled ||
                        drawdownRules.length <= 1 ||
                        config.drawdown_take_profit.mode === 'disabled'
                      }
                      className="inline-flex items-center gap-1 px-2 py-1 rounded text-xs text-[#F6465D] border border-[#41272B] hover:bg-[#2B1619]"
                    >
                      <Trash2 className="w-3.5 h-3.5" />
                      {isZh ? '删除' : 'Remove'}
                    </button>
                  </div>

                  {/* Row 1: Close Ratio (position allocation for this tier) */}
                  <div
                    className="p-3 rounded-lg space-y-2"
                    style={{ ...cardStyle(true), borderColor: '#2B3139' }}
                  >
                    <div className="flex items-center justify-between">
                      <label
                        className="block text-xs"
                        style={{ color: '#848E9C' }}
                      >
                        {isZh
                          ? '仓位比例 %（开仓时固定分配）'
                          : 'Position Ratio % (fixed at open)'}
                      </label>
                      <select
                        value={rule.close_ratio_mode || 'manual'}
                        onChange={(e) =>
                          updateDrawdownRule(index, {
                            close_ratio_mode: e.target.value as 'manual' | 'ai',
                          })
                        }
                        disabled={
                          disabled ||
                          config.drawdown_take_profit.mode === 'disabled'
                        }
                        className="px-2 py-1 rounded text-xs"
                        style={inputStyle}
                      >
                        <option value="manual">
                          {isZh ? '手动' : 'Manual'}
                        </option>
                        <option value="ai">{isZh ? 'AI 决定' : 'AI'}</option>
                      </select>
                    </div>
                    {(rule.close_ratio_mode || 'manual') === 'manual' && (
                      <input
                        type="number"
                        value={rule.close_ratio_pct}
                        min={0}
                        max={100}
                        step={1}
                        onChange={(e) =>
                          updateDrawdownRule(index, {
                            close_ratio_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={
                          disabled ||
                          config.drawdown_take_profit.mode === 'disabled'
                        }
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    )}
                    {(rule.close_ratio_mode || 'manual') === 'ai' && (
                      <div className="text-[11px]" style={{ color: '#848E9C' }}>
                        {isZh
                          ? `AI 决定仓位比例（手动参考值：${rule.close_ratio_pct}%）`
                          : `AI decides ratio (manual reference: ${rule.close_ratio_pct}%)`}
                      </div>
                    )}
                  </div>

                  {/* Row 2: Min Profit (peak trigger threshold) */}
                  <div
                    className="p-3 rounded-lg space-y-2"
                    style={{ ...cardStyle(true), borderColor: '#2B3139' }}
                  >
                    <div className="flex items-center justify-between">
                      <label
                        className="block text-xs"
                        style={{ color: '#848E9C' }}
                      >
                        {rule.min_profit_unit === 'atr'
                          ? isZh
                            ? '峰值触发 (ATR倍数，利润达到此值开始追踪)'
                            : 'Peak Trigger (ATR×, start tracking at this profit)'
                          : isZh
                            ? '峰值触发 %（利润达到此值开始追踪）'
                            : 'Peak Trigger % (start tracking at this profit)'}
                      </label>
                      <div className="flex items-center gap-1">
                        {unitToggle(rule.min_profit_unit, (u) =>
                          updateDrawdownRule(index, { min_profit_unit: u })
                        )}
                        <select
                          value={rule.min_profit_mode || 'manual'}
                          onChange={(e) =>
                            updateDrawdownRule(index, {
                              min_profit_mode: e.target.value as
                                | 'manual'
                                | 'ai',
                            })
                          }
                          disabled={
                            disabled ||
                            config.drawdown_take_profit.mode === 'disabled'
                          }
                          className="px-2 py-1 rounded text-xs"
                          style={inputStyle}
                        >
                          <option value="manual">
                            {isZh ? '手动' : 'Manual'}
                          </option>
                          <option value="ai">{isZh ? 'AI 决定' : 'AI'}</option>
                        </select>
                      </div>
                    </div>
                    {(rule.min_profit_mode || 'manual') === 'manual' && (
                      <input
                        type="number"
                        value={rule.min_profit_pct}
                        min={0}
                        step={0.1}
                        onChange={(e) =>
                          updateDrawdownRule(index, {
                            min_profit_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={
                          disabled ||
                          config.drawdown_take_profit.mode === 'disabled'
                        }
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    )}
                    {(rule.min_profit_mode || 'manual') === 'ai' && (
                      <div className="text-[11px]" style={{ color: '#848E9C' }}>
                        {isZh
                          ? `AI 决定峰值触发（手动参考值：${rule.min_profit_pct}%）`
                          : `AI decides trigger (manual reference: ${rule.min_profit_pct}%)`}
                      </div>
                    )}
                  </div>

                  {/* Row 3: Max Drawdown (drawdown threshold from tier peak) */}
                  <div
                    className="p-3 rounded-lg space-y-2"
                    style={{ ...cardStyle(true), borderColor: '#2B3139' }}
                  >
                    <div className="flex items-center justify-between">
                      <label
                        className="block text-xs"
                        style={{ color: '#848E9C' }}
                      >
                        {rule.max_drawdown_unit === 'atr'
                          ? isZh
                            ? '回撤幅度 (ATR倍数，从该级峰值回撤此距离触发平仓)'
                            : 'Drawdown (ATR×, close when giveback from tier peak exceeds this)'
                          : isZh
                            ? '回撤幅度 %（从该级峰值回撤此比例触发平仓）'
                            : 'Drawdown % (close when drawdown from tier peak exceeds this)'}
                      </label>
                      <div className="flex items-center gap-1">
                        {unitToggle(rule.max_drawdown_unit, (u) =>
                          updateDrawdownRule(index, { max_drawdown_unit: u })
                        )}
                        <select
                          value={rule.max_drawdown_mode || 'manual'}
                          onChange={(e) =>
                            updateDrawdownRule(index, {
                              max_drawdown_mode: e.target.value as
                                | 'manual'
                                | 'ai',
                            })
                          }
                          disabled={
                            disabled ||
                            config.drawdown_take_profit.mode === 'disabled'
                          }
                          className="px-2 py-1 rounded text-xs"
                          style={inputStyle}
                        >
                          <option value="manual">
                            {isZh ? '手动' : 'Manual'}
                          </option>
                          <option value="ai">{isZh ? 'AI 决定' : 'AI'}</option>
                        </select>
                      </div>
                    </div>
                    {(rule.max_drawdown_mode || 'manual') === 'manual' && (
                      <input
                        type="number"
                        value={rule.max_drawdown_pct}
                        min={0}
                        step={0.1}
                        onChange={(e) =>
                          updateDrawdownRule(index, {
                            max_drawdown_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={
                          disabled ||
                          config.drawdown_take_profit.mode === 'disabled'
                        }
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    )}
                    {(rule.max_drawdown_mode || 'manual') === 'ai' && (
                      <div className="text-[11px]" style={{ color: '#848E9C' }}>
                        {isZh
                          ? `AI 决定回撤幅度（手动参考值：${rule.max_drawdown_pct}%）`
                          : `AI decides drawdown (manual reference: ${rule.max_drawdown_pct}%)`}
                      </div>
                    )}
                  </div>

                  {/* Poll interval */}
                  <div>
                    <label
                      className="block text-xs mb-1"
                      style={{ color: '#848E9C' }}
                    >
                      {isZh ? '轮询秒数' : 'Poll Seconds'}
                    </label>
                    <input
                      type="number"
                      value={rule.poll_interval_seconds}
                      min={5}
                      step={5}
                      onChange={(e) =>
                        updateDrawdownRule(index, {
                          poll_interval_seconds: parseInt(e.target.value) || 60,
                        })
                      }
                      disabled={
                        disabled ||
                        config.drawdown_take_profit.mode === 'disabled'
                      }
                      className="w-full px-3 py-2 rounded"
                      style={inputStyle}
                    />
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>

        <div>
          <div className="flex items-center gap-2 mb-4">
            <RotateCcw className="w-5 h-5" style={{ color: '#F97316' }} />
            <h3 className="font-medium" style={{ color: '#EAECEF' }}>
              {isZh
                ? 'Break-even Stop（运行态保护）'
                : 'Break-even Stop (Runtime Protection)'}
            </h3>
          </div>
          <div className="p-4 rounded-lg space-y-3" style={sectionStyle}>
            <div className="flex items-center justify-between">
              <label className="block text-sm" style={{ color: '#EAECEF' }}>
                {isZh ? '启用保本止损' : 'Enable Break-even Stop'}
              </label>
              <input
                type="checkbox"
                checked={config.break_even_stop.enabled}
                onChange={(e) => updateBreakEven('enabled', e.target.checked)}
                disabled={disabled}
                className="h-4 w-4 accent-orange-500"
              />
            </div>
            {infoBlock(
              isZh ? 'Break-even 独立管理' : 'Break-even is independent',
              isZh
                ? 'Break-even 只负责把止损抬到保本附近，不接管 Drawdown 的盈利控制，也不替代 Full / Ladder 的长期止损结构。'
                : 'Break-even only raises stop-loss toward breakeven. It does not take over Drawdown profit control or replace the long-lived stop-loss structure from Full / Ladder.',
              isZh
                ? '建议：把它当成盈利后附加的一层止损保护。'
                : 'Recommendation: use it as an extra stop-loss layer after profit appears.'
            )}
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {isZh ? '模式' : 'Mode'}
                </label>
                <select
                  value={config.break_even_stop.mode || 'manual'}
                  onChange={(e) =>
                    updateBreakEven(
                      'mode',
                      e.target.value as BreakEvenStopConfig['mode']
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                >
                  <option value="manual">{modeLabel('manual')}</option>
                  <option value="ai">{modeLabel('ai')}</option>
                </select>
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {isZh ? '触发模式' : 'Trigger Mode'}
                </label>
                <select
                  value={config.break_even_stop.trigger_mode}
                  onChange={(e) =>
                    updateBreakEven(
                      'trigger_mode',
                      e.target.value as BreakEvenStopConfig['trigger_mode']
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                >
                  <option value="profit_pct">
                    {triggerModeLabel('profit_pct')}
                  </option>
                  <option value="r_multiple">
                    {triggerModeLabel('r_multiple')}
                  </option>
                </select>
              </div>
            </div>
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <div
                  className="text-sm font-medium"
                  style={{ color: '#EAECEF' }}
                >
                  {isZh ? '多级手动 BE' : 'Multi-level Manual BE'}
                </div>
                <button
                  type="button"
                  onClick={addBreakEvenRule}
                  disabled={disabled}
                  className="inline-flex items-center gap-1 px-2 py-1 rounded text-xs"
                  style={{
                    background: '#1E2329',
                    color: '#EAECEF',
                    border: '1px solid #2B3139',
                  }}
                >
                  <Plus className="w-3 h-3" /> {isZh ? '增加一档' : 'Add Level'}
                </button>
              </div>
              {breakEvenRules.map((rule, index) => (
                <div
                  key={index}
                  className="p-3 rounded-lg space-y-3"
                  style={helpCardStyle}
                >
                  <div className="flex items-center justify-between">
                    <span
                      className="text-xs font-medium"
                      style={{ color: '#EAECEF' }}
                    >
                      {rule.stage_name || `BE${index + 1}`}
                    </span>
                    <button
                      type="button"
                      onClick={() => removeBreakEvenRule(index)}
                      disabled={disabled || breakEvenRules.length <= 1}
                      className="p-1 rounded"
                      style={{ color: '#F6465D' }}
                    >
                      <Trash2 className="w-4 h-4" />
                    </button>
                  </div>
                  <div className="grid grid-cols-2 md:grid-cols-5 gap-3">
                    <div>
                      <label
                        className="block text-xs mb-1"
                        style={{ color: '#848E9C' }}
                      >
                        {isZh ? '名称' : 'Name'}
                      </label>
                      <input
                        value={rule.stage_name || ''}
                        onChange={(e) =>
                          updateBreakEvenRule(index, {
                            stage_name: e.target.value,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    </div>
                    <div>
                      <label
                        className="block text-xs mb-1"
                        style={{ color: '#848E9C' }}
                      >
                        {isZh ? '触发模式' : 'Trigger Mode'}
                      </label>
                      <select
                        value={
                          rule.trigger_mode ||
                          config.break_even_stop.trigger_mode
                        }
                        onChange={(e) =>
                          updateBreakEvenRule(index, {
                            trigger_mode: e.target
                              .value as BreakEvenStopConfig['trigger_mode'],
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      >
                        <option value="profit_pct">
                          {triggerModeLabel('profit_pct')}
                        </option>
                        <option value="r_multiple">
                          {triggerModeLabel('r_multiple')}
                        </option>
                      </select>
                    </div>
                    <div>
                      <div className="flex items-center justify-between mb-1">
                        <label
                          className="block text-xs"
                          style={{ color: '#848E9C' }}
                        >
                          {rule.trigger_unit === 'atr'
                            ? isZh
                              ? '触发值 (ATR倍数)'
                              : 'Trigger (ATR×)'
                            : isZh
                              ? '触发值'
                              : 'Trigger Value'}
                        </label>
                        {(rule.trigger_mode ||
                          config.break_even_stop.trigger_mode) ===
                          'profit_pct' &&
                          unitToggle(rule.trigger_unit, (u) =>
                            updateBreakEvenRule(index, { trigger_unit: u })
                          )}
                      </div>
                      <input
                        type="number"
                        value={rule.trigger_value}
                        min={0}
                        step={0.1}
                        onChange={(e) =>
                          updateBreakEvenRule(index, {
                            trigger_value: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    </div>
                    <div>
                      <label
                        className="block text-xs mb-1"
                        style={{ color: '#848E9C' }}
                      >
                        {rule.trigger_unit === 'atr'
                          ? isZh
                            ? '止损偏移 (ATR倍数,可负)'
                            : 'Stop Offset (ATR×, can be negative)'
                          : isZh
                            ? '止损偏移 %(可负)'
                            : 'Stop Offset % (can be negative)'}
                      </label>
                      <input
                        type="number"
                        value={rule.offset_pct}
                        step={0.1}
                        onChange={(e) =>
                          updateBreakEvenRule(index, {
                            offset_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    </div>
                    <div>
                      <label
                        className="block text-xs mb-1"
                        style={{ color: '#848E9C' }}
                      >
                        {isZh ? '保护仓位 %' : 'Size %'}
                      </label>
                      <input
                        type="number"
                        value={rule.close_ratio_pct || 100}
                        min={0}
                        max={100}
                        step={1}
                        onChange={(e) =>
                          updateBreakEvenRule(index, {
                            close_ratio_pct: parseFloat(e.target.value) || 0,
                          })
                        }
                        disabled={disabled}
                        className="w-full px-3 py-2 rounded"
                        style={inputStyle}
                      />
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>

      {/* Global ATR settings — shared by every field switched to ATR× units */}
      {onAtrChange && (
        <div className="p-4 rounded-lg space-y-3" style={sectionStyle}>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Activity className="w-4 h-4" style={{ color: '#F0B90B' }} />
              <span
                className="text-sm font-medium"
                style={{ color: '#EAECEF' }}
              >
                {isZh ? 'ATR 全局设置' : 'ATR Global Settings'}
              </span>
            </div>
            <label
              className="flex items-center gap-2 text-xs"
              style={{ color: '#848E9C' }}
            >
              {isZh ? '启用 ATR 自适应' : 'Enable ATR-adaptive'}
              <input
                type="checkbox"
                checked={!!atr.enabled}
                onChange={(e) => updateAtr('enabled', e.target.checked)}
                disabled={disabled}
                className="h-4 w-4 accent-orange-500"
              />
            </label>
          </div>
          {infoBlock(
            isZh
              ? '按波动率自适应的保护距离'
              : 'Volatility-adaptive protection distances',
            isZh
              ? '这些是公共参数：当某个 TP/SL/DD/BE 字段的单位切到「ATR×」时，系统按 该字段值 × ATR(周期) / 开仓价 × 100 计算出有效百分比，并裁剪到下方上下限之间。保持「%」的字段不受影响。'
              : 'Shared parameters: when any TP/SL/DD/BE field is switched to "ATR×", its effective percent is computed as fieldValue × ATR(period) / entryPrice × 100, clamped to the min/max below. Fields left on "%" are unaffected.',
            isZh
              ? '推荐：1h 周期、ATR(14)、有效区间 0.3% ~ 20%。需先在上方把对应字段切到 ATR×。'
              : 'Recommended: 1h timeframe, ATR(14), effective range 0.3%–20%. Switch a field to ATR× above to use it.'
          )}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? 'K线周期' : 'Timeframe'}
              </label>
              <select
                value={atr.timeframe || '1h'}
                onChange={(e) => updateAtr('timeframe', e.target.value)}
                disabled={disabled || !atr.enabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              >
                {['5m', '15m', '30m', '1h', '4h', '1d'].map((tf) => (
                  <option key={tf} value={tf}>
                    {tf}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? 'ATR 周期' : 'ATR Period'}
              </label>
              <input
                type="number"
                min={1}
                step={1}
                value={atr.atr_period ?? 14}
                onChange={(e) =>
                  updateAtr('atr_period', parseInt(e.target.value) || 14)
                }
                disabled={disabled || !atr.enabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
            </div>
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? '有效下限 %' : 'Min Eff %'}
              </label>
              <input
                type="number"
                min={0}
                step={0.1}
                value={atr.min_eff_pct ?? 0.3}
                onChange={(e) =>
                  updateAtr('min_eff_pct', parseFloat(e.target.value) || 0)
                }
                disabled={disabled || !atr.enabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
            </div>
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? '有效上限 %' : 'Max Eff %'}
              </label>
              <input
                type="number"
                min={0}
                step={0.1}
                value={atr.max_eff_pct ?? 20}
                onChange={(e) =>
                  updateAtr('max_eff_pct', parseFloat(e.target.value) || 0)
                }
                disabled={disabled || !atr.enabled}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
            </div>
          </div>
        </div>
      )}

      {/* Breadth breaker — the sole portfolio guard */}
      <div className="p-4 rounded-lg space-y-3" style={sectionStyle}>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Activity className="w-4 h-4" style={{ color: '#0ECB81' }} />
            <label className="block text-sm" style={{ color: '#EAECEF' }}>
              {isZh ? '广度熔断（按币种监控）' : 'Breadth Breaker (per-symbol)'}
            </label>
          </div>
          <input
            type="checkbox"
            checked={!!giveback.breadth_enabled}
            onChange={(e) =>
              updateGiveback('breadth_enabled', e.target.checked)
            }
            disabled={disabled}
            className="h-4 w-4 accent-orange-500"
          />
        </div>
        {infoBlock(
          isZh
            ? '相关性逆转探测 + 只砍亏损仓'
            : 'Correlation-reversal gate + cut losers only',
          isZh
            ? '逐个币种监控自身回撤（杠杆无关），当大多数持仓同时回撤时判定为相关性逆转：只砍正在回撤的亏损仓，盈利仓不动、由保本止损(BE)保护继续跑。这是唯一的组合级熔断，取代了旧的账户权益熔断——后者用账户权益%触发，10倍杠杆下权益跌5%只是单币0.5%波动，噪声就被打掉。'
            : 'Monitors each symbol’s own retracement (leverage-free). When a MAJORITY of positions retrace together it reads a correlated reversal: cut only the LOSING retracing positions; winners are left to ride their break-even stop. This is the sole portfolio breaker, replacing the old account-equity breakers, which triggered on account-equity% where a 5% equity drop at 10x is only a 0.5% price move (noise).',
          isZh
            ? '交叉验证预设：最少持仓4 / 回撤占比70% / ATR回撤0.9倍 / 亏损仓全砍 / 无冷却。先 dry-run 观察。'
            : 'Cross-validated preset: min4 / frac70% / ATR0.9 / full loser cut / no cooldown. Use dry-run first.'
        )}
        <div className="flex items-center justify-between">
          <label className="block text-xs" style={{ color: '#848E9C' }}>
            {isZh ? 'Dry-run（只记录不下单）' : 'Dry-run (log only, no orders)'}
          </label>
          <input
            type="checkbox"
            checked={!!giveback.dry_run}
            onChange={(e) => updateGiveback('dry_run', e.target.checked)}
            disabled={disabled}
            className="h-4 w-4 accent-orange-500"
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '最少持仓数(法定人数)' : 'Min positions (quorum)'}
            </label>
            <input
              type="number"
              step="1"
              min="2"
              value={giveback.breadth_min_pos ?? 4}
              onChange={(e) =>
                updateGiveback('breadth_min_pos', Number(e.target.value))
              }
              disabled={disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '回撤占比触发(0-1)' : 'Retrace fraction (0-1)'}
            </label>
            <input
              type="number"
              step="0.05"
              min="0.1"
              max="1"
              value={giveback.breadth_frac ?? 0.7}
              onChange={(e) =>
                updateGiveback('breadth_frac', Number(e.target.value))
              }
              disabled={disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
        </div>
        {/* BREADTH_ROW_2 */}
        <div className="flex items-center justify-between">
          <label className="block text-xs" style={{ color: '#848E9C' }}>
            {isZh
              ? '回撤按 ATR 计(否则按盈亏%)'
              : 'Measure retrace in ATR (else pnl%)'}
          </label>
          <input
            type="checkbox"
            checked={!!giveback.breadth_use_atr}
            onChange={(e) =>
              updateGiveback('breadth_use_atr', e.target.checked)
            }
            disabled={disabled}
            className="h-4 w-4 accent-orange-500"
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          {giveback.breadth_use_atr ? (
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? 'ATR回撤倍数(从峰值)' : 'ATR-from-peak mult'}
              </label>
              <input
                type="number"
                step="0.1"
                min="0.1"
                value={giveback.breadth_atr_mult ?? 0.9}
                onChange={(e) =>
                  updateGiveback('breadth_atr_mult', Number(e.target.value))
                }
                disabled={disabled}
                className="w-full px-2 py-2 rounded"
                style={inputStyle}
              />
            </div>
          ) : (
            <div>
              <label
                className="block text-xs mb-1"
                style={{ color: '#848E9C' }}
              >
                {isZh ? '回吐%(从峰值)' : 'Giveback% (from peak)'}
              </label>
              <input
                type="number"
                step="0.5"
                min="0.5"
                value={giveback.breadth_giveback_pct ?? 3}
                onChange={(e) =>
                  updateGiveback('breadth_giveback_pct', Number(e.target.value))
                }
                disabled={disabled}
                className="w-full px-2 py-2 rounded"
                style={inputStyle}
              />
            </div>
          )}
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '亏损仓砍%(全砍=100)' : 'Loser cut% (100=full)'}
            </label>
            <input
              type="number"
              step="5"
              min="1"
              max="100"
              value={giveback.breadth_loser_cut_pct ?? 100}
              onChange={(e) =>
                updateGiveback('breadth_loser_cut_pct', Number(e.target.value))
              }
              disabled={disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '速率窗口(根K线)' : 'Velocity window (bars)'}
            </label>
            <input
              type="number"
              step="1"
              min="2"
              value={giveback.breadth_vel_window ?? 6}
              onChange={(e) =>
                updateGiveback('breadth_vel_window', Number(e.target.value))
              }
              disabled={disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '冷却(根K线,0=关)' : 'Cooldown (bars, 0=off)'}
            </label>
            <input
              type="number"
              step="1"
              min="0"
              value={giveback.breadth_cooldown_bars ?? 0}
              onChange={(e) =>
                updateGiveback('breadth_cooldown_bars', Number(e.target.value))
              }
              disabled={disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '速率阈值(ATR/根,0=关)' : 'Velocity eps (ATR/bar, 0=off)'}
            </label>
            <input
              type="number"
              step="0.05"
              min="0"
              value={giveback.breadth_vel_eps ?? 0}
              onChange={(e) =>
                updateGiveback('breadth_vel_eps', Number(e.target.value))
              }
              disabled={disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
        </div>
        <div
          className="flex items-start justify-between gap-3 p-3 rounded-lg"
          style={{ background: '#1E2329', border: '1px solid #F6465D44' }}
        >
          <div>
            <label
              className="block text-xs font-medium"
              style={{ color: '#F6465D' }}
            >
              {isZh
                ? '盈利盘也杀（全局清仓熔断）'
                : 'Cut winners too (full deleverage breaker)'}
            </label>
            <p className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {isZh
                ? '默认关闭：熔断只砍回撤中的亏损仓，盈利仓由 BE 保护继续跑。开启后熔断触发时连回撤中的盈利仓一起砍，等于在相关性暴跌时全局清仓避险——保护力度最强，但会牺牲盈利仓的后续机会。建议先 dry-run 观察触发频率再实盘开启。'
                : 'Default off: only retracing losers are cut; winners ride break-even. When on, a fired gate also cuts retracing winners — a system-wide "go to cash on a correlated crash" policy. Strongest protection but sacrifices winner upside. Dry-run first.'}
            </p>
          </div>
          <input
            type="checkbox"
            checked={!!giveback.breadth_cut_winners}
            onChange={(e) =>
              updateGiveback('breadth_cut_winners', e.target.checked)
            }
            disabled={disabled}
            className="h-4 w-4 mt-0.5 accent-red-500"
          />
        </div>
      </div>

      {/* Trend-reversal flip — fleet-default ON + LIVE, per-trader overrides here */}
      <div className="p-4 rounded-lg space-y-3" style={sectionStyle}>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <RotateCcw className="w-4 h-4" style={{ color: '#F0B90B' }} />
            <label className="block text-sm" style={{ color: '#EAECEF' }}>
              {isZh ? '趋势反转翻仓' : 'Trend-Reversal Flip'}
            </label>
          </div>
          <input
            type="checkbox"
            checked={!flip.disabled}
            onChange={(e) => updateFlip('disabled', !e.target.checked)}
            disabled={disabled}
            className="h-4 w-4 accent-orange-500"
          />
        </div>
        {infoBlock(
          isZh
            ? 'AI 高确信反向信号 → 平原仓 + 反向开仓'
            : 'High-conviction opposite AI signal → close + reverse',
          isZh
            ? '当 AI 在已持仓币种上给出方向相反的高确信信号、且该仓位已持有足够时长时，系统平掉原仓并反向开仓（单次翻仓，翻后不再被反向翻，天然抗来回打脸）。回测在三个实盘 trader 上均为正向、6 小时持仓门、确信度 75 为最优组合。原设计的广度/枯竭门经回测证明有害或无效已移除。'
            : 'When the AI issues a high-conviction opposite-direction signal on an already-held symbol that has aged enough, the system closes the original and opens the reverse (single flip per entry — a flipped position is never re-flipped, inherently anti-whipsaw). Backtest is net-positive across all three live traders; 6h hold gate + confidence 75 is the optimum. The original breadth/exhaustion gates were dropped (backtest showed them harmful or inert).',
          isZh
            ? '舰队默认：全员开启 + 实盘。这里是按 trader 的覆盖项。回测最优：确信度≥75 / 持仓≥6h。'
            : 'Fleet default: ON + LIVE for all traders. These are per-trader overrides. Backtest optimum: confidence ≥75 / hold ≥6h.'
        )}
        {/* FLIP_BODY_PLACEHOLDER */}
        <div
          className="flex items-start justify-between gap-3 p-3 rounded-lg"
          style={{ background: '#1E2329', border: '1px solid #F0B90B44' }}
        >
          <div>
            <label
              className="block text-xs font-medium"
              style={{ color: '#F0B90B' }}
            >
              {isZh
                ? 'Dry-run（只观察不下单）'
                : 'Dry-run (observe only, no orders)'}
            </label>
            <p className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {isZh
                ? '开启后此 trader 只记录翻仓判定到观察账本、不真正平仓/反向开仓，用于隔离单个 trader 观察 AI 反转信号质量，而无需关闭整个功能。舰队默认实盘（关闭此项）。'
                : 'When on, this trader only logs flip decisions to the observation ledger without closing/reversing — used to quarantine one trader and review AI reversal-signal quality without disabling the feature. Fleet default is live (this off).'}
            </p>
          </div>
          <input
            type="checkbox"
            checked={!!flip.force_dry_run}
            onChange={(e) => updateFlip('force_dry_run', e.target.checked)}
            disabled={disabled || !!flip.disabled}
            className="h-4 w-4 mt-0.5 accent-orange-500"
          />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '确信度门槛(0-100)' : 'Confidence floor (0-100)'}
            </label>
            <input
              type="number"
              step="1"
              min="0"
              max="100"
              value={flip.min_confidence ?? 75}
              onChange={(e) =>
                updateFlip('min_confidence', Number(e.target.value))
              }
              disabled={disabled || !!flip.disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
          <div>
            <label className="block text-xs mb-1" style={{ color: '#848E9C' }}>
              {isZh ? '最短持仓时长(小时)' : 'Min hold age (hours)'}
            </label>
            <input
              type="number"
              step="0.5"
              min="0"
              value={flip.min_position_age_hours ?? 6}
              onChange={(e) =>
                updateFlip('min_position_age_hours', Number(e.target.value))
              }
              disabled={disabled || !!flip.disabled}
              className="w-full px-2 py-2 rounded"
              style={inputStyle}
            />
          </div>
        </div>
      </div>

      <div
        className="p-4 rounded-lg"
        style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
      >
        <div className="flex items-center gap-2 mb-2">
          <TrendingDown className="w-4 h-4" style={{ color: '#F6465D' }} />
          <span className="text-sm font-medium" style={{ color: '#EAECEF' }}>
            {isZh ? '当前执行说明' : 'Execution Notes'}
          </span>
        </div>
        <ul
          className="text-xs space-y-1 list-disc pl-4"
          style={{ color: '#848E9C' }}
        >
          <li>
            {isZh
              ? 'Full TP/SL 与 Ladder TP/SL 属于“委托型保护”，目标是在开仓后尽快挂到交易所并做校验。'
              : 'Full TP/SL and Ladder TP/SL are order-based protections that should be posted and verified after opening.'}
          </li>
          <li>
            {isZh
              ? 'Drawdown / Break-even 属于“运行态保护”，由系统在持仓期间持续监控并动态执行。'
              : 'Drawdown and Break-even are runtime protections enforced continuously while a position is live.'}
          </li>
          <li>
            {isZh
              ? 'Regime Filter / 开仓门禁已移至独立的“Pre-Entry Gate”页面。'
              : 'Regime Filter / Pre-Entry Gate has moved to a dedicated section.'}
          </li>
          <li>
            {isZh
              ? '若交易所能力不满足要求或保护校验失败，系统应进入 fail-safe 处理，避免长期裸仓。'
              : 'If exchange capability is insufficient or verification fails, the system should enter fail-safe handling to avoid naked exposure.'}
          </li>
        </ul>
      </div>
    </div>
  )
}
