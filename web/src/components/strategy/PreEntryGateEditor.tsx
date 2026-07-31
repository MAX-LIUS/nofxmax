import { type ReactNode } from 'react'
import {
  Filter,
  Layers,
  Target,
  Activity,
  SlidersHorizontal,
} from 'lucide-react'
import type {
  RegimeFilterConfig,
  EntryStructureConfig,
  EntryGateConfig,
  StrategyControlPolicyMode,
} from '../../types'
import { EntryStructureEditor } from './EntryStructureEditor'
import { preEntryGate, ts } from '../../i18n/strategy-translations'

interface PreEntryGateEditorProps {
  config: RegimeFilterConfig
  onChange: (config: RegimeFilterConfig) => void
  disabled?: boolean
  language: string
}

const inputStyle = {
  background: '#1E2329',
  border: '1px solid #2B3139',
  color: '#EAECEF',
}

const regimeOptions = [
  { value: 'narrow', zh: '窄波动', en: 'Narrow' },
  { value: 'standard', zh: '标准波动', en: 'Standard' },
  { value: 'wide', zh: '宽波动', en: 'Wide' },
  { value: 'trending', zh: '趋势（双向）', en: 'Trending (Both)' },
  { value: 'trending_up', zh: '上涨趋势', en: 'Uptrend' },
  { value: 'trending_down', zh: '下跌趋势', en: 'Downtrend' },
  { value: 'volatile', zh: '极端波动', en: 'Volatile' },
]

const policyModes: {
  value: StrategyControlPolicyMode
  labelKey: 'strict' | 'auditOnly' | 'recommendOnly'
  descKey: 'strictDesc' | 'auditOnlyDesc' | 'recommendOnlyDesc'
  color: string
}[] = [
  {
    value: 'strict',
    labelKey: 'strict',
    descKey: 'strictDesc',
    color: '#F6465D',
  },
  {
    value: 'audit_only',
    labelKey: 'auditOnly',
    descKey: 'auditOnlyDesc',
    color: '#F0B90B',
  },
  {
    value: 'recommend_only',
    labelKey: 'recommendOnly',
    descKey: 'recommendOnlyDesc',
    color: '#0ECB81',
  },
]

function EntryGateGroup({
  enabled,
  onToggle,
  masterDisabled,
  title,
  description,
  example,
  color,
  children,
}: {
  enabled: boolean
  onToggle: (v: boolean) => void
  masterDisabled: boolean
  title: string
  description: string
  example: string
  color: string
  children: ReactNode
}) {
  return (
    <div
      className="p-3 rounded-lg space-y-2"
      style={{
        background: '#11161C',
        border: `1px solid ${masterDisabled ? '#2B3139' : color}33`,
        opacity: masterDisabled ? 0.5 : 1,
      }}
    >
      <label
        className="flex items-center gap-2 text-sm cursor-pointer"
        style={{ color: '#EAECEF' }}
      >
        <input
          type="checkbox"
          checked={enabled}
          onChange={(e) => onToggle(e.target.checked)}
          disabled={masterDisabled}
          className="h-4 w-4 accent-amber-500"
        />
        <span className="font-medium">{title}</span>
      </label>
      <div className="text-[11px] leading-relaxed" style={{ color: '#848E9C' }}>
        {description}
      </div>
      <div
        className="text-[11px] leading-relaxed italic"
        style={{ color: '#5E6673' }}
      >
        {example}
      </div>
      {enabled && !masterDisabled && (
        <div className="grid grid-cols-2 gap-3 pt-1">{children}</div>
      )}
    </div>
  )
}

function EntryGateInput({
  label,
  value,
  step,
  min = 0,
  max,
  disabled,
  onChange,
}: {
  label: string
  value: number
  step: number
  // min/max are optional bounds surfaced to the browser's number input. min
  // defaults to 0 (the prior hard-coded behaviour). Server-side WithDefaults
  // clamps regardless — these only stop obviously invalid typing earlier.
  min?: number
  max?: number
  disabled: boolean
  onChange: (v: number) => void
}) {
  return (
    <div>
      <label className="block text-[11px] mb-1" style={{ color: '#848E9C' }}>
        {label}
      </label>
      <input
        type="number"
        value={value}
        step={step}
        min={min}
        max={max}
        onChange={(e) => onChange(parseFloat(e.target.value) || 0)}
        disabled={disabled}
        className="w-full px-3 py-2 rounded text-sm"
        style={inputStyle}
      />
    </div>
  )
}

// Stage — a top-level numbered category (①..⑤). Gives every stage a consistent
// header (icon + title) and a subtle accent border so the 5 stages read as
// distinct blocks. Visual only; contains whatever gate cards belong to it.
function Stage({
  icon,
  title,
  accent,
  children,
}: {
  icon: ReactNode
  title: string
  accent: string
  children: ReactNode
}) {
  return (
    <div
      className="p-3 rounded-lg space-y-3"
      style={{ background: '#0B0E11', border: `1px solid ${accent}44` }}
    >
      <div className="flex items-center gap-2">
        <span style={{ color: accent }}>{icon}</span>
        <h4 className="text-sm font-semibold" style={{ color: '#EAECEF' }}>
          {title}
        </h4>
      </div>
      {children}
    </div>
  )
}

// SubCard — a labelled sub-group inside a Stage. Bundles a title + one-line
// beginner description with its own controls so related params stay together.
// `tag` shows whether the group is a hard gate or a score penalty.
function SubCard({
  title,
  description,
  tag,
  children,
}: {
  title: string
  description?: string
  tag?: { text: string; color: string }
  children: ReactNode
}) {
  return (
    <div
      className="p-3 rounded-lg space-y-2"
      style={{ background: '#11161C', border: '1px solid #2B3139' }}
    >
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-sm font-medium" style={{ color: '#EAECEF' }}>
          {title}
        </span>
        {tag && (
          <span
            className="px-1.5 py-0.5 rounded text-[10px] font-medium"
            style={{ background: `${tag.color}22`, color: tag.color }}
          >
            {tag.text}
          </span>
        )}
      </div>
      {description && (
        <div
          className="text-[11px] leading-relaxed"
          style={{ color: '#848E9C' }}
        >
          {description}
        </div>
      )}
      {children}
    </div>
  )
}

export function PreEntryGateEditor({
  config,
  onChange,
  disabled,
  language,
}: PreEntryGateEditorProps) {
  const isZh = language === 'zh'
  // Shared hard-gate / penalty tags for SubCard headers.
  const tagHard = {
    text: isZh ? '硬门·拒单' : 'hard gate',
    color: '#F6465D',
  }
  const tagPenalty = {
    text: isZh ? '降分·缩仓' : 'penalty',
    color: '#F0B90B',
  }

  const update = <K extends keyof RegimeFilterConfig>(
    key: K,
    value: RegimeFilterConfig[K]
  ) => {
    if (!disabled) onChange({ ...config, [key]: value })
  }

  const updateEntryGate = (
    key: keyof EntryGateConfig,
    value: number | boolean | string
  ) => {
    if (disabled) return
    update('entry_structure', {
      ...(config.entry_structure || {}),
      entry_gate: {
        ...(config.entry_structure?.entry_gate || {}),
        [key]: value,
      },
    } as EntryStructureConfig)
  }

  const toggleRegime = (regime: string) => {
    const current = config.allowed_regimes || []
    const exists = current.includes(regime)
    update(
      'allowed_regimes',
      exists ? current.filter((r) => r !== regime) : [...current, regime]
    )
  }

  return (
    <div className="space-y-4">
      {/* Header + how the gate works */}
      <div
        className="p-3 rounded-lg"
        style={{ background: '#0B0E11', border: '1px solid #38BDF833' }}
      >
        <div className="flex items-center gap-2 mb-3">
          <Filter className="w-5 h-5" style={{ color: '#38BDF8' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {ts(preEntryGate.title, language)}
          </h3>
        </div>
        <div className="flex items-center justify-between mb-3">
          <label className="text-sm" style={{ color: '#EAECEF' }}>
            {ts(preEntryGate.enableRegimeFilter, language)}
          </label>
          <input
            type="checkbox"
            checked={config.enabled}
            onChange={(e) => update('enabled', e.target.checked)}
            disabled={disabled}
            className="h-4 w-4 accent-sky-500"
          />
        </div>
        <div
          className="text-[11px] leading-relaxed mb-3"
          style={{ color: '#848E9C' }}
        >
          {ts(preEntryGate.howGateWorks, language)}
        </div>
        <div
          className="p-3 rounded-lg font-mono text-xs"
          style={{
            background: '#11161C',
            border: '1px solid #2B3139',
            color: '#38BDF8',
          }}
        >
          {ts(preEntryGate.gateFlow, language)}
        </div>
        <div className="mt-2 text-xs" style={{ color: '#848E9C' }}>
          {ts(preEntryGate.mutualExclusion, language)}
        </div>
      </div>

      {/* Stage ① Market Access */}
      <Stage
        icon={<Filter className="w-4 h-4" />}
        title={ts(preEntryGate.stageMarketAccess, language)}
        accent="#38BDF8"
      >
        {/* Market regime + funding + volatility */}
        <SubCard
          title={ts(preEntryGate.grpMarketState, language)}
          description={ts(preEntryGate.grpMarketStateDesc, language)}
          tag={tagHard}
        >
          <div>
            <label className="block text-xs mb-2" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.allowedRegimes, language)}
            </label>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-2">
              {regimeOptions.map((option) => {
                const active = (config.allowed_regimes || []).includes(
                  option.value
                )
                return (
                  <button
                    key={option.value}
                    type="button"
                    onClick={() => toggleRegime(option.value)}
                    disabled={disabled}
                    className="px-3 py-2 rounded text-sm border"
                    style={{
                      background: active ? '#1E3A5F' : '#11161C',
                      borderColor: active ? '#38BDF8' : '#2B3139',
                      color: active ? '#EAECEF' : '#848E9C',
                    }}
                  >
                    {isZh ? option.zh : option.en}
                  </button>
                )
              })}
            </div>
          </div>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div>
              <div className="flex items-center gap-2 mb-1">
                <input
                  type="checkbox"
                  checked={config.block_high_funding}
                  onChange={(e) =>
                    update('block_high_funding', e.target.checked)
                  }
                  disabled={disabled}
                  className="h-4 w-4 accent-sky-500"
                />
                <label className="text-sm" style={{ color: '#EAECEF' }}>
                  {ts(preEntryGate.blockHighFunding, language)}
                </label>
              </div>
              <input
                type="number"
                value={config.max_funding_rate_abs}
                min={0}
                step={0.001}
                onChange={(e) =>
                  update(
                    'max_funding_rate_abs',
                    parseFloat(e.target.value) || 0
                  )
                }
                disabled={disabled || !config.block_high_funding}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
              <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
                {ts(preEntryGate.fundingRateUnit, language)}
              </div>
            </div>
            <div>
              <div className="flex items-center gap-2 mb-1">
                <input
                  type="checkbox"
                  checked={config.block_high_volatility}
                  onChange={(e) =>
                    update('block_high_volatility', e.target.checked)
                  }
                  disabled={disabled}
                  className="h-4 w-4 accent-sky-500"
                />
                <label className="text-sm" style={{ color: '#EAECEF' }}>
                  {ts(preEntryGate.blockHighVolatility, language)}
                </label>
              </div>
              <input
                type="number"
                value={config.max_atr14_pct}
                min={0}
                step={0.1}
                onChange={(e) =>
                  update('max_atr14_pct', parseFloat(e.target.value) || 0)
                }
                disabled={disabled || !config.block_high_volatility}
                className="w-full px-3 py-2 rounded"
                style={inputStyle}
              />
              <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
                {ts(preEntryGate.atrUnit, language)}
              </div>
            </div>
          </div>
        </SubCard>

        {/* Trend alignment */}
        <SubCard
          title={ts(preEntryGate.grpTrendAlign, language)}
          description={ts(preEntryGate.grpTrendAlignDesc, language)}
          tag={tagHard}
        >
          <label
            className="flex items-center gap-2 text-sm"
            style={{ color: '#EAECEF' }}
          >
            <input
              type="checkbox"
              checked={config.require_trend_alignment}
              onChange={(e) =>
                update('require_trend_alignment', e.target.checked)
              }
              disabled={disabled}
              className="h-4 w-4 accent-sky-500"
            />
            {ts(preEntryGate.requireTrendAlignment, language)}
          </label>
          <div
            className="text-[11px] leading-relaxed"
            style={{ color: '#848E9C' }}
          >
            {isZh
              ? '注意：即使允许了“下跌趋势”，开启趋势同向后仍会拒绝下跌趋势里的做多；允许“上涨趋势”也仍会拒绝上涨趋势里的做空。'
              : 'Note: allowing a regime does not allow both directions; with trend alignment on, longs are blocked in downtrends and shorts are blocked in uptrends.'}
          </div>
          {config.require_trend_alignment && (
            <label
              className="flex items-center gap-2 text-sm"
              style={{ color: '#EAECEF' }}
            >
              <input
                type="checkbox"
                checked={
                  (config.trend_alignment_mode || 'strict') ===
                  'allow_range_edge_reversal'
                }
                onChange={(e) =>
                  update(
                    'trend_alignment_mode',
                    (e.target.checked
                      ? 'allow_range_edge_reversal'
                      : 'strict') as RegimeFilterConfig['trend_alignment_mode']
                  )
                }
                disabled={disabled}
                className="h-4 w-4 accent-amber-500"
              />
              {isZh
                ? '允许 range_edge 支撑/阻力逆势例外'
                : 'Allow range_edge support/resistance reversal exception'}
            </label>
          )}
          {config.require_trend_alignment && (
            <label
              className="flex items-center gap-2 text-sm"
              style={{ color: '#EAECEF' }}
            >
              <input
                type="checkbox"
                checked={config.block_long_in_htf_downtrend !== false}
                onChange={(e) =>
                  update('block_long_in_htf_downtrend', e.target.checked)
                }
                disabled={disabled}
                className="h-4 w-4 accent-sky-500"
              />
              {ts(preEntryGate.blockLongInHtfDowntrend, language)}
            </label>
          )}
          {config.require_trend_alignment &&
            config.block_long_in_htf_downtrend !== false && (
              <div
                className="text-[11px] leading-relaxed"
                style={{ color: '#848E9C' }}
              >
                {ts(preEntryGate.blockLongInHtfDowntrendDesc, language)}
              </div>
            )}
          {config.require_trend_alignment && (
            <label
              className="flex items-center gap-2 text-sm"
              style={{ color: '#EAECEF' }}
            >
              <input
                type="checkbox"
                checked={config.block_short_in_htf_uptrend !== false}
                onChange={(e) =>
                  update('block_short_in_htf_uptrend', e.target.checked)
                }
                disabled={disabled}
                className="h-4 w-4 accent-sky-500"
              />
              {ts(preEntryGate.blockShortInHtfUptrend, language)}
            </label>
          )}
          {config.require_trend_alignment &&
            config.block_short_in_htf_uptrend !== false && (
              <div
                className="text-[11px] leading-relaxed"
                style={{ color: '#848E9C' }}
              >
                {ts(preEntryGate.blockShortInHtfUptrendDesc, language)}
              </div>
            )}
          {config.require_trend_alignment &&
            (config.trend_alignment_mode || 'strict') ===
              'allow_range_edge_reversal' && (
              <div
                className="text-[11px] leading-relaxed"
                style={{ color: '#F0B90B' }}
              >
                {isZh
                  ? '仅对 setup_type=range_edge 生效；仍要求价格接近布林/结构边缘、短线动量不过度极端，并继续经过 RR、结构、保护门禁。'
                  : 'Only applies to setup_type=range_edge; price must be near a band/structure edge, momentum must not be extreme, and RR/structure/protection gates still apply.'}
              </div>
            )}
        </SubCard>

        {/* Coin momentum gate */}
        <SubCard
          title={ts(preEntryGate.momentumGate, language)}
          description={ts(preEntryGate.momentumGateDesc, language)}
          tag={tagHard}
        >
          <label
            className="flex items-center gap-2 text-sm"
            style={{ color: '#EAECEF' }}
          >
            <input
              type="checkbox"
              checked={config.momentum_gate_enabled ?? false}
              onChange={(e) =>
                update('momentum_gate_enabled', e.target.checked)
              }
              disabled={disabled}
              className="h-4 w-4 accent-amber-500"
            />
            {ts(preEntryGate.enableMomentumGate, language)}
          </label>
          {config.momentum_gate_enabled && (
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {ts(preEntryGate.momentumStaleChg1h, language)}
                </label>
                <input
                  type="number"
                  value={config.momentum_stale_chg1h ?? 0.15}
                  min={0}
                  step={0.01}
                  onChange={(e) =>
                    update(
                      'momentum_stale_chg1h',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
                <div className="text-[10px] mt-1" style={{ color: '#848E9C' }}>
                  {ts(preEntryGate.momentumStaleDesc, language)}
                </div>
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {ts(preEntryGate.momentumStaleChg4h, language)}
                </label>
                <input
                  type="number"
                  value={config.momentum_stale_chg4h ?? 0.4}
                  min={0}
                  step={0.1}
                  onChange={(e) =>
                    update(
                      'momentum_stale_chg4h',
                      parseFloat(e.target.value) || 0
                    )
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
                  {ts(preEntryGate.momentumExhaustedChg4h, language)}
                </label>
                <input
                  type="number"
                  value={config.momentum_exhausted_chg4h ?? 4.5}
                  min={0}
                  step={0.5}
                  onChange={(e) =>
                    update(
                      'momentum_exhausted_chg4h',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
                <div className="text-[10px] mt-1" style={{ color: '#848E9C' }}>
                  {ts(preEntryGate.momentumExhaustedDesc, language)}
                </div>
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {ts(preEntryGate.momentumCounterChg1h, language)}
                </label>
                <input
                  type="number"
                  value={config.momentum_counter_chg1h ?? 0.3}
                  min={0}
                  step={0.05}
                  onChange={(e) =>
                    update(
                      'momentum_counter_chg1h',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
                <div className="text-[10px] mt-1" style={{ color: '#848E9C' }}>
                  {ts(preEntryGate.momentumCounterDesc, language)}
                </div>
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {ts(preEntryGate.momentumFadingChg4h, language)}
                </label>
                <input
                  type="number"
                  value={config.momentum_fading_chg4h ?? 2.5}
                  min={0}
                  step={0.5}
                  onChange={(e) =>
                    update(
                      'momentum_fading_chg4h',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
                <div className="text-[10px] mt-1" style={{ color: '#848E9C' }}>
                  {ts(preEntryGate.momentumFadingDesc, language)}
                </div>
              </div>
              <div>
                <label
                  className="block text-xs mb-1"
                  style={{ color: '#848E9C' }}
                >
                  {ts(preEntryGate.maxSlDistancePct, language)}
                </label>
                <input
                  type="number"
                  value={config.max_sl_distance_pct ?? 2.0}
                  min={0.5}
                  step={0.5}
                  onChange={(e) =>
                    update(
                      'max_sl_distance_pct',
                      parseFloat(e.target.value) || 0
                    )
                  }
                  disabled={disabled}
                  className="w-full px-3 py-2 rounded"
                  style={inputStyle}
                />
                <div className="text-[10px] mt-1" style={{ color: '#848E9C' }}>
                  {ts(preEntryGate.maxSlDistanceDesc, language)}
                </div>
              </div>
            </div>
          )}
        </SubCard>
      </Stage>

      {/* Stage ② Entry Quality */}
      <Stage
        icon={<Layers className="w-4 h-4" />}
        title={ts(preEntryGate.stageEntryQuality, language)}
        accent="#60A5FA"
      >
        {/* Master toggle */}
        <div
          className="p-3 rounded-lg"
          style={{ background: '#11161C', border: '1px solid #2B3139' }}
        >
          <label
            className="flex items-center gap-2 text-sm"
            style={{ color: '#EAECEF' }}
          >
            <input
              type="checkbox"
              checked={config.entry_structure?.entry_gate?.enabled ?? false}
              onChange={(e) =>
                update('entry_structure', {
                  ...(config.entry_structure || {}),
                  entry_gate: {
                    ...(config.entry_structure?.entry_gate || {}),
                    enabled: e.target.checked,
                  },
                } as EntryStructureConfig)
              }
              disabled={disabled}
              className="h-4 w-4 accent-amber-500"
            />
            {ts(preEntryGate.enableEntryGate, language)}
          </label>
          <div
            className="text-[11px] mt-2 leading-relaxed"
            style={{ color: '#F0B90B' }}
          >
            {isZh
              ? '门禁顺序：市场状态/窄波动 → 结构字段 → ATR/入场贴近 → 信心/RR → 保护计划。目标位只用于 RR 与保护分层，不再要求强贴近最近阻力/支撑。'
              : 'Gate order: market state/narrow volatility → structure fields → ATR/entry proximity → confidence/RR → protection plan. Target is used for RR/protection tiers, not strict nearest S/R alignment.'}
          </div>
        </div>

        {/* Group A: Volatility Gate */}
        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.volatility_gate_enabled !==
            false
          }
          onToggle={(v) => updateEntryGate('volatility_gate_enabled', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.volatilityGate, language)}
          description={ts(preEntryGate.volatilityGateDesc, language)}
          example={ts(preEntryGate.volatilityGateExample, language)}
          color="#38BDF8"
        >
          <EntryGateInput
            label={ts(preEntryGate.minAtr14Pct, language)}
            value={config.entry_structure?.entry_gate?.min_atr14_pct ?? 1.2}
            step={0.1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.volatility_gate_enabled ===
                false
            }
            onChange={(v) => updateEntryGate('min_atr14_pct', v)}
          />
        </EntryGateGroup>

        {/* Group B: Entry Precision */}
        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.entry_precision_enabled !==
            false
          }
          onToggle={(v) => updateEntryGate('entry_precision_enabled', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.entryPrecision, language)}
          description={ts(preEntryGate.entryPrecisionDesc, language)}
          example={ts(preEntryGate.entryPrecisionExample, language)}
          color="#A855F7"
        >
          <EntryGateInput
            label={ts(preEntryGate.entryProximityAtr, language)}
            value={
              config.entry_structure?.entry_gate?.entry_proximity_atr_mul ?? 0.6
            }
            step={0.1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.entry_precision_enabled ===
                false
            }
            onChange={(v) => updateEntryGate('entry_proximity_atr_mul', v)}
          />
          <EntryGateInput
            label={ts(preEntryGate.entryProximityMaxPct, language)}
            value={
              config.entry_structure?.entry_gate?.entry_proximity_max_pct ?? 1.5
            }
            step={0.1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.entry_precision_enabled ===
                false
            }
            onChange={(v) => updateEntryGate('entry_proximity_max_pct', v)}
          />
        </EntryGateGroup>

        {/* Group C: Stop / Reward / Setup quality (single backend toggle) */}
        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.stop_quality_enabled !== false
          }
          onToggle={(v) => updateEntryGate('stop_quality_enabled', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.stopQuality, language)}
          description={ts(preEntryGate.stopQualityDesc, language)}
          example={ts(preEntryGate.stopQualityExample, language)}
          color="#0ECB81"
        >
          {/* — sub-group: stop placement — */}
          <div
            className="col-span-2 text-[11px] font-semibold pt-1"
            style={{ color: '#0ECB81' }}
          >
            {ts(preEntryGate.grpStopPlacement, language)}
            <span className="font-normal" style={{ color: '#848E9C' }}>
              {' · '}
              {ts(preEntryGate.grpStopPlacementDesc, language)}
            </span>
          </div>
          <EntryGateInput
            label={ts(preEntryGate.minRiskDistancePct, language)}
            value={
              config.entry_structure?.entry_gate?.min_risk_distance_pct ?? 0.4
            }
            step={0.05}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.stop_quality_enabled === false
            }
            onChange={(v) => updateEntryGate('min_risk_distance_pct', v)}
          />
          <EntryGateInput
            label={ts(preEntryGate.minSlDistanceAtr, language)}
            value={
              config.entry_structure?.entry_gate?.min_sl_distance_atr_mul ?? 1.2
            }
            step={0.1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.stop_quality_enabled === false
            }
            onChange={(v) => updateEntryGate('min_sl_distance_atr_mul', v)}
          />
          <EntryGateInput
            label={ts(preEntryGate.invalidationAtr, language)}
            value={
              config.entry_structure?.entry_gate
                ?.invalidation_structure_atr_mul ?? 0.5
            }
            step={0.1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.stop_quality_enabled === false
            }
            onChange={(v) =>
              updateEntryGate('invalidation_structure_atr_mul', v)
            }
          />
          <div className="col-span-2">
            <label
              className="block text-[11px] mb-1"
              style={{ color: '#848E9C' }}
            >
              {ts(preEntryGate.volatilityBufferAtr, language)}
            </label>
            <div className="flex items-center gap-3">
              <input
                type="range"
                value={
                  config.entry_structure?.entry_gate
                    ?.volatility_buffer_atr_mul ?? 0.3
                }
                onChange={(e) =>
                  updateEntryGate(
                    'volatility_buffer_atr_mul',
                    parseFloat(e.target.value)
                  )
                }
                disabled={
                  disabled ||
                  !(config.entry_structure?.entry_gate?.enabled ?? false) ||
                  config.entry_structure?.entry_gate?.stop_quality_enabled ===
                    false
                }
                min={0}
                max={1.5}
                step={0.1}
                className="flex-1 accent-green-500"
              />
              <span
                className="w-12 text-center font-mono text-sm"
                style={{ color: '#0ECB81' }}
              >
                {(
                  config.entry_structure?.entry_gate
                    ?.volatility_buffer_atr_mul ?? 0.3
                ).toFixed(1)}
              </span>
            </div>
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.volatilityBufferDesc, language)}
            </div>
          </div>

          {/* — sub-group: reward & target — */}
          <div
            className="col-span-2 text-[11px] font-semibold pt-2"
            style={{ color: '#0ECB81' }}
          >
            {ts(preEntryGate.grpRewardTarget, language)}
            <span className="font-normal" style={{ color: '#848E9C' }}>
              {' · '}
              {ts(preEntryGate.grpRewardTargetDesc, language)}
            </span>
          </div>
          <EntryGateInput
            label={ts(preEntryGate.minRewardAtr, language)}
            value={
              config.entry_structure?.entry_gate?.min_reward_atr_mul ?? 1.8
            }
            step={0.1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.stop_quality_enabled === false
            }
            onChange={(v) => updateEntryGate('min_reward_atr_mul', v)}
          />
          <div>
            <EntryGateInput
              label={ts(preEntryGate.maxTargetAtr, language)}
              value={
                config.entry_structure?.entry_gate?.max_target_atr_mul ?? 5.0
              }
              step={0.5}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false) ||
                config.entry_structure?.entry_gate?.stop_quality_enabled ===
                  false
              }
              onChange={(v) => updateEntryGate('max_target_atr_mul', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.maxTargetAtrDesc, language)}
            </div>
          </div>
          <div className="col-span-2">
            <label
              className="flex items-center gap-2 text-sm"
              style={{ color: '#EAECEF' }}
            >
              <input
                type="checkbox"
                checked={
                  (config.entry_structure?.entry_gate
                    ?.target_reachability_mode || 'cap') === 'reject'
                }
                onChange={(e) =>
                  updateEntryGate(
                    'target_reachability_mode',
                    e.target.checked ? 'reject' : 'cap'
                  )
                }
                disabled={
                  disabled ||
                  !(config.entry_structure?.entry_gate?.enabled ?? false) ||
                  config.entry_structure?.entry_gate?.stop_quality_enabled ===
                    false
                }
                className="h-4 w-4 accent-amber-500"
              />
              {ts(preEntryGate.targetReachabilityMode, language)}
              {' — '}
              {(config.entry_structure?.entry_gate?.target_reachability_mode ||
                'cap') === 'reject'
                ? isZh
                  ? '拒绝(旧)'
                  : 'reject (legacy)'
                : isZh
                  ? '压缩(默认)'
                  : 'cap (default)'}
            </label>
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.targetReachabilityModeDesc, language)}
            </div>
          </div>
          <div>
            <EntryGateInput
              label={ts(preEntryGate.realisticTargetRiskMul, language)}
              value={
                config.entry_structure?.entry_gate?.realistic_target_risk_mul ??
                0.8
              }
              step={0.1}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false) ||
                config.entry_structure?.entry_gate?.stop_quality_enabled ===
                  false
              }
              onChange={(v) => updateEntryGate('realistic_target_risk_mul', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.realisticTargetRiskMulDesc, language)}
            </div>
          </div>
          <div>
            <EntryGateInput
              label={ts(preEntryGate.maxNetRr, language)}
              value={config.entry_structure?.entry_gate?.max_net_rr ?? 2.8}
              step={0.1}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false) ||
                config.entry_structure?.entry_gate?.stop_quality_enabled ===
                  false
              }
              onChange={(v) => updateEntryGate('max_net_rr', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.maxNetRrDesc, language)}
            </div>
          </div>

          {/* — sub-group: setup filter (penalties, not hard reject) — */}
          <div
            className="col-span-2 text-[11px] font-semibold pt-2"
            style={{ color: '#F0B90B' }}
          >
            {ts(preEntryGate.grpSetupFilter, language)}
            <span className="font-normal" style={{ color: '#848E9C' }}>
              {' · '}
              {ts(preEntryGate.grpSetupFilterDesc, language)}
            </span>
          </div>
          <div className="col-span-2 space-y-1">
            <label
              className="flex items-center gap-2 text-sm"
              style={{ color: '#EAECEF' }}
            >
              <input
                type="checkbox"
                checked={
                  config.entry_structure?.entry_gate?.block_breakout_retest ??
                  true
                }
                onChange={(e) =>
                  updateEntryGate('block_breakout_retest', e.target.checked)
                }
                disabled={
                  disabled ||
                  !(config.entry_structure?.entry_gate?.enabled ?? false) ||
                  config.entry_structure?.entry_gate?.stop_quality_enabled ===
                    false
                }
                className="h-4 w-4 accent-amber-500"
              />
              {ts(preEntryGate.blockBreakoutRetest, language)}
              <span
                className="px-1.5 py-0.5 rounded text-[10px] font-medium"
                style={{
                  background: `${tagPenalty.color}22`,
                  color: tagPenalty.color,
                }}
              >
                {tagPenalty.text}
              </span>
            </label>
            <div className="text-[11px]" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.blockBreakoutRetestDesc, language)}
            </div>
          </div>
          <div className="col-span-2 space-y-1">
            <label
              className="flex items-center gap-2 text-sm"
              style={{ color: '#EAECEF' }}
            >
              <input
                type="checkbox"
                checked={
                  config.entry_structure?.entry_gate
                    ?.soft_regime_structure_fit ?? true
                }
                onChange={(e) =>
                  updateEntryGate('soft_regime_structure_fit', e.target.checked)
                }
                disabled={
                  disabled ||
                  !(config.entry_structure?.entry_gate?.enabled ?? false)
                }
                className="h-4 w-4 accent-amber-500"
              />
              {ts(preEntryGate.softRegimeFit, language)}
              <span
                className="px-1.5 py-0.5 rounded text-[10px] font-medium"
                style={{
                  background: `${tagPenalty.color}22`,
                  color: tagPenalty.color,
                }}
              >
                {tagPenalty.text}
              </span>
            </label>
            <div className="text-[11px]" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.softRegimeFitDesc, language)}
            </div>
          </div>
        </EntryGateGroup>

        {/* Group D: Path Clarity */}
        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.path_clarity_enabled !== false
          }
          onToggle={(v) => updateEntryGate('path_clarity_enabled', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.pathClarity, language)}
          description={ts(preEntryGate.pathClarityDesc, language)}
          example={ts(preEntryGate.pathClarityExample, language)}
          color="#F0B90B"
        >
          <EntryGateInput
            label={ts(preEntryGate.maxBlockingLevels, language)}
            value={config.entry_structure?.entry_gate?.max_blocking_levels ?? 4}
            step={1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate?.path_clarity_enabled === false
            }
            onChange={(v) => updateEntryGate('max_blocking_levels', v)}
          />
        </EntryGateGroup>

        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.structural_alignment ?? false
          }
          onToggle={(v) => updateEntryGate('structural_alignment', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.grpStructuralAlignment, language)}
          description={ts(preEntryGate.structuralAlignmentDesc, language)}
          example=""
          color="#0ECB81"
        >
          <div>
            <EntryGateInput
              label={ts(preEntryGate.structuralPivotLookback, language)}
              value={
                config.entry_structure?.entry_gate?.structural_pivot_lookback ??
                3
              }
              step={1}
              min={1}
              max={10}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false)
              }
              onChange={(v) => updateEntryGate('structural_pivot_lookback', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.structuralPivotLookbackDesc, language)}
            </div>
          </div>
          <div>
            <EntryGateInput
              label={ts(preEntryGate.structuralSwingCount, language)}
              value={
                config.entry_structure?.entry_gate?.structural_swing_count ?? 2
              }
              step={1}
              min={2}
              max={6}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false)
              }
              onChange={(v) => updateEntryGate('structural_swing_count', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.structuralSwingCountDesc, language)}
            </div>
          </div>
          <div>
            <EntryGateInput
              label={ts(preEntryGate.structuralMinBlockingPct, language)}
              value={
                config.entry_structure?.entry_gate
                  ?.structural_min_blocking_pct ?? 0
              }
              // 0.05 步长: 精调就是按 0.05 走的, 0.1 步会跳过实测最优值
              step={0.05}
              min={0}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false)
              }
              onChange={(v) =>
                updateEntryGate('structural_min_blocking_pct', v)
              }
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.structuralMinBlockingPctDesc, language)}
            </div>
          </div>
          <div>
            <label className="flex items-center gap-2 text-xs cursor-pointer">
              <input
                type="checkbox"
                checked={
                  config.entry_structure?.entry_gate?.structural_audit_only ??
                  false
                }
                disabled={
                  disabled ||
                  !(config.entry_structure?.entry_gate?.enabled ?? false)
                }
                onChange={(e) =>
                  updateEntryGate('structural_audit_only', e.target.checked)
                }
              />
              <span style={{ color: '#EAECEF' }}>
                {ts(preEntryGate.structuralAuditOnly, language)}
              </span>
            </label>
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.structuralAuditOnlyDesc, language)}
            </div>
          </div>
        </EntryGateGroup>

        <EntryStructureEditor
          config={config.entry_structure}
          onChange={(entryStructure: EntryStructureConfig) =>
            update('entry_structure', entryStructure)
          }
          disabled={disabled}
          language={language}
        />
      </Stage>

      {/* Stage ③ Trade Pacing */}
      <Stage
        icon={<Activity className="w-4 h-4" />}
        title={ts(preEntryGate.stageTradePacing, language)}
        accent="#F59E0B"
      >
        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.correlated_adverse_throttle ??
            false
          }
          onToggle={(v) => updateEntryGate('correlated_adverse_throttle', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.grpCorrelatedThrottle, language)}
          description={ts(preEntryGate.correlatedAdverseThrottleDesc, language)}
          example=""
          color="#F59E0B"
        >
          <div>
            <EntryGateInput
              label={ts(preEntryGate.throttleWindowHours, language)}
              value={
                config.entry_structure?.entry_gate?.throttle_window_hours ?? 12
              }
              step={1}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false)
              }
              onChange={(v) => updateEntryGate('throttle_window_hours', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.throttleWindowHoursDesc, language)}
            </div>
          </div>
          <div>
            <EntryGateInput
              label={ts(preEntryGate.throttleMinCloses, language)}
              value={
                config.entry_structure?.entry_gate?.throttle_min_closes ?? 3
              }
              step={1}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false)
              }
              onChange={(v) => updateEntryGate('throttle_min_closes', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.throttleMinClosesDesc, language)}
            </div>
          </div>
          <div>
            <EntryGateInput
              label={ts(preEntryGate.throttleLossRate, language)}
              value={
                config.entry_structure?.entry_gate?.throttle_loss_rate ?? 0.6
              }
              step={0.05}
              disabled={
                disabled ||
                !(config.entry_structure?.entry_gate?.enabled ?? false)
              }
              onChange={(v) => updateEntryGate('throttle_loss_rate', v)}
            />
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.throttleLossRateDesc, language)}
            </div>
          </div>
        </EntryGateGroup>
      </Stage>

      {/* Stage ④ Confidence & Direction */}
      <Stage
        icon={<Target className="w-4 h-4" />}
        title={ts(preEntryGate.stageConfidence, language)}
        accent="#0ECB81"
      >
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          {/* Min Confidence */}
          <div
            className="p-3 rounded-lg"
            style={{ background: '#11161C', border: '1px solid #2B3139' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(preEntryGate.minConfidence, language)}
            </label>
            <div className="flex items-center gap-2">
              <input
                type="range"
                value={config.min_confidence ?? 75}
                onChange={(e) =>
                  update('min_confidence', parseInt(e.target.value))
                }
                disabled={disabled}
                min={50}
                max={100}
                className="flex-1 accent-green-500"
              />
              <span
                className="w-12 text-center font-mono"
                style={{ color: '#0ECB81' }}
              >
                {config.min_confidence ?? 75}
              </span>
            </div>
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.confidenceUnit, language)}
            </div>
          </div>

          {/* Min Risk-Reward Ratio */}
          <div
            className="p-3 rounded-lg"
            style={{ background: '#11161C', border: '1px solid #2B3139' }}
          >
            <label className="block text-sm mb-1" style={{ color: '#EAECEF' }}>
              {ts(preEntryGate.minRiskReward, language)}
            </label>
            <div className="flex items-center">
              <span style={{ color: '#848E9C' }}>1:</span>
              <input
                type="number"
                value={config.min_risk_reward_ratio ?? 3}
                onChange={(e) =>
                  update(
                    'min_risk_reward_ratio',
                    parseFloat(e.target.value) || 3
                  )
                }
                disabled={disabled}
                min={1}
                max={10}
                step={0.5}
                className="w-20 px-3 py-2 rounded ml-2"
                style={inputStyle}
              />
            </div>
            <div className="text-[11px] mt-1" style={{ color: '#848E9C' }}>
              {ts(preEntryGate.rrUnit, language)}
            </div>
          </div>
        </div>

        {/* Group E: Confidence & Direction (per-setup thresholds) */}
        <EntryGateGroup
          enabled={
            config.entry_structure?.entry_gate?.confidence_direction_enabled !==
            false
          }
          onToggle={(v) => updateEntryGate('confidence_direction_enabled', v)}
          masterDisabled={
            disabled || !(config.entry_structure?.entry_gate?.enabled ?? false)
          }
          title={ts(preEntryGate.confidenceDirection, language)}
          description={ts(preEntryGate.confidenceDirectionDesc, language)}
          example={ts(preEntryGate.confidenceDirectionExample, language)}
          color="#F6465D"
        >
          <EntryGateInput
            label={ts(preEntryGate.shortNonDowntrendConf, language)}
            value={
              config.entry_structure?.entry_gate
                ?.short_non_downtrend_min_confidence ?? 85
            }
            step={1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate
                ?.confidence_direction_enabled === false
            }
            onChange={(v) =>
              updateEntryGate('short_non_downtrend_min_confidence', v)
            }
          />
          <EntryGateInput
            label={ts(preEntryGate.squeezeMinConf, language)}
            value={
              config.entry_structure?.entry_gate?.squeeze_min_confidence ?? 80
            }
            step={1}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate
                ?.confidence_direction_enabled === false
            }
            onChange={(v) => updateEntryGate('squeeze_min_confidence', v)}
          />
          <EntryGateInput
            label={ts(preEntryGate.squeezeMinRR, language)}
            value={config.entry_structure?.entry_gate?.squeeze_min_rr ?? 2.5}
            step={0.5}
            disabled={
              disabled ||
              !(config.entry_structure?.entry_gate?.enabled ?? false) ||
              config.entry_structure?.entry_gate
                ?.confidence_direction_enabled === false
            }
            onChange={(v) => updateEntryGate('squeeze_min_rr', v)}
          />
        </EntryGateGroup>
      </Stage>

      {/* Stage ⑤ Execution Policy */}
      <Stage
        icon={<SlidersHorizontal className="w-4 h-4" />}
        title={ts(preEntryGate.stageExecPolicy, language)}
        accent="#A855F7"
      >
        <div className="space-y-2">
          {policyModes.map((pm) => {
            const selected = (config.policy_mode || 'strict') === pm.value
            return (
              <label
                key={pm.value}
                className="flex items-start gap-3 p-3 rounded-lg cursor-pointer border"
                style={{
                  background: selected ? '#11161C' : 'transparent',
                  borderColor: selected ? pm.color : '#2B3139',
                }}
              >
                <input
                  type="radio"
                  name="policy_mode"
                  value={pm.value}
                  checked={selected}
                  onChange={() => update('policy_mode', pm.value)}
                  disabled={disabled}
                  className="mt-0.5 accent-purple-500"
                />
                <div>
                  <div
                    className="text-sm font-medium"
                    style={{ color: '#EAECEF' }}
                  >
                    {ts(preEntryGate[pm.labelKey], language)}
                  </div>
                  <div className="text-xs mt-0.5" style={{ color: '#848E9C' }}>
                    {ts(preEntryGate[pm.descKey], language)}
                  </div>
                </div>
              </label>
            )
          })}
        </div>
      </Stage>
    </div>
  )
}
