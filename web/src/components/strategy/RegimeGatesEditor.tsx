import { useMemo } from 'react'
import { ShieldAlert, Plus, Trash2, Eye, Ban } from 'lucide-react'
import type {
  RegimeGateConfig,
  RegimeGateCategory,
  RegimeGateMode,
} from '../../types'

interface RegimeGatesEditorProps {
  gates: RegimeGateConfig[]
  onChange: (gates: RegimeGateConfig[]) => void
  disabled?: boolean
  language: string
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

type ParamKey =
  | 'slope_window'
  | 'block_side'
  | 'min_conf'
  | 'threshold'
  | 'adx_period'
  | 'lookback'
  | 'align_min'
  | 'r2_min'

interface ParamSpec {
  key: ParamKey
  labelZh: string
  labelEn: string
  kind: 'number' | 'side'
  default: number | string
  step?: number
  hintZh?: string
  hintEn?: string
}

interface CategorySpec {
  category: RegimeGateCategory
  titleZh: string
  titleEn: string
  descZh: string
  descEn: string
  params: ParamSpec[]
  color: string
}
// Catalog mirrors the backend switch in trader/regime_gate.go. Defaults match the
// shadow primitives' fallbacks (0 => default).
const CATALOG: CategorySpec[] = [
  {
    category: 'counter_trend',
    titleZh: '逆势拦截 (Counter-Trend)',
    titleEn: 'Counter-Trend Block',
    descZh:
      '在斜率确认的趋势中拦截逆势开仓（上涨趋势里做空、下跌趋势里做多）。',
    descEn:
      'Block opens that fight a slope-confirmed trend (short in uptrend / long in downtrend).',
    color: '#38BDF8',
    params: [
      {
        key: 'slope_window',
        labelZh: '斜率窗口',
        labelEn: 'Slope window',
        kind: 'number',
        default: 30,
        step: 1,
        hintZh: '计算趋势斜率的K线根数，默认30。',
        hintEn: 'Bars used for the trend slope. Default 30.',
      },
    ],
  },
  {
    category: 'trend_direction_only',
    titleZh: '单向趋势过滤 (Direction-Only)',
    titleEn: 'Trend Direction-Only',
    descZh: '只拦截一个方向：仅在下跌趋势里拦做多，或仅在上涨趋势里拦做空。',
    descEn:
      'Block only one side: block longs in downtrends, or shorts in uptrends.',
    color: '#A855F7',
    params: [
      {
        key: 'slope_window',
        labelZh: '斜率窗口',
        labelEn: 'Slope window',
        kind: 'number',
        default: 50,
        step: 1,
      },
      {
        key: 'block_side',
        labelZh: '拦截方向',
        labelEn: 'Block side',
        kind: 'side',
        default: 'LONG',
        hintZh: 'LONG=下跌趋势拦做多；SHORT=上涨趋势拦做空。',
        hintEn:
          'LONG = block longs in downtrend; SHORT = block shorts in uptrend.',
      },
    ],
  },
  {
    category: 'chop_reject',
    titleZh: '震荡拒绝 (Chop Reject)',
    titleEn: 'Chop Reject',
    descZh: '多指标共识判定为震荡区间时，拒绝所有开仓。无参数。',
    descEn:
      'Reject all opens when the multi-indicator consensus flags chop. No params.',
    color: '#F0B90B',
    params: [],
  },
  {
    category: 'chop_lowconf',
    titleZh: '震荡+低信心 (Chop Low-Conf)',
    titleEn: 'Chop + Low Confidence',
    descZh: '仅在震荡区间且AI信心低于阈值时拦截（信心足够高仍放行）。',
    descEn:
      'Block only in chop AND below the confidence threshold (high-conf passes).',
    color: '#F59E0B',
    params: [
      {
        key: 'min_conf',
        labelZh: '最低信心',
        labelEn: 'Min confidence',
        kind: 'number',
        default: 70,
        step: 1,
        hintZh: '低于该信心且处于震荡才拦截，默认70。',
        hintEn: 'Block in chop only below this confidence. Default 70.',
      },
    ],
  },
  {
    category: 'adx_weak',
    titleZh: 'ADX 弱趋势 (ADX Weak)',
    titleEn: 'ADX Weak Trend',
    descZh: 'ADX 低于阈值（趋势强度不足）时拦截开仓。',
    descEn: 'Block opens when ADX is below the threshold (trend too weak).',
    color: '#0ECB81',
    params: [
      {
        key: 'threshold',
        labelZh: 'ADX 阈值',
        labelEn: 'ADX threshold',
        kind: 'number',
        default: 20,
        step: 1,
        hintZh: 'ADX 低于此值判定为弱趋势，默认20。',
        hintEn: 'ADX below this counts as weak. Default 20.',
      },
      {
        key: 'adx_period',
        labelZh: 'ADX 周期',
        labelEn: 'ADX period',
        kind: 'number',
        default: 14,
        step: 1,
        hintZh:
          '计算 ADX 的周期，默认14（留空等同14，不改变原有行为）。合适的周期随主周期而变：15m 主周期实测 ADX(14) 各档全部无效或有害（阈值20→-0.072、25→-0.064、30→-0.120，基线-0.071），只有 ADX(10) 配阈值30 才有增量（+0.139，三折全正）。',
        hintEn:
          'ADX lookback period. Default 14 (leaving it unset behaves exactly as before). The useful period depends on the primary timeframe: on a 15m primary every ADX(14) threshold measured flat-to-harmful (20→-0.072, 25→-0.064, 30→-0.120 against a -0.071 baseline), while ADX(10) at threshold 30 is the only variant that helps (+0.139, all 3 folds positive).',
      },
    ],
  },
  {
    category: 'donchian_counter',
    titleZh: '唐奇安逆突破 (Donchian Counter)',
    titleEn: 'Donchian Counter-Break',
    descZh: '价格向上突破唐奇安通道却做空、或向下突破却做多时拦截。',
    descEn:
      'Block shorts on an up-break, or longs on a down-break, of the Donchian channel.',
    color: '#F6465D',
    params: [
      {
        key: 'lookback',
        labelZh: '通道回看',
        labelEn: 'Lookback',
        kind: 'number',
        default: 48,
        step: 1,
        hintZh: '唐奇安通道回看根数，默认48。',
        hintEn: 'Donchian channel lookback bars. Default 48.',
      },
    ],
  },
  {
    category: 'chart_trend',
    titleZh: '图形化趋势 (Chart Trend)',
    titleEn: 'Chart Trend',
    descZh: '综合摆动对齐、回归R²、方向斜率判定图形化趋势，拦截逆趋势开仓。',
    descEn:
      'Full graphical trend using swing alignment, regression R², and directional slope. Blocks counter-trend opens.',
    color: '#22D3EE',
    params: [
      {
        key: 'slope_window',
        labelZh: '斜率窗口',
        labelEn: 'Slope window',
        kind: 'number',
        default: 30,
        step: 1,
        hintZh: '计算趋势的K线窗口，默认30。',
        hintEn: 'Bars for trend calculation. Default 30.',
      },
      {
        key: 'align_min',
        labelZh: '对齐阈值',
        labelEn: 'Align threshold',
        kind: 'number',
        default: 0.55,
        step: 0.01,
        hintZh: '摆动对齐最小阈值（0-1），默认0.55。',
        hintEn: 'Minimum swing alignment ratio (0-1). Default 0.55.',
      },
      {
        key: 'r2_min',
        labelZh: 'R² 阈值',
        labelEn: 'R² threshold',
        kind: 'number',
        default: 0.6,
        step: 0.01,
        hintZh: '线性回归R²最小值（0-1），默认0.60。',
        hintEn: 'Minimum regression R² (0-1). Default 0.60.',
      },
      {
        key: 'lookback',
        labelZh: '摆动点半宽',
        labelEn: 'Pivot half-width',
        kind: 'number',
        default: 2,
        step: 1,
        hintZh:
          '判定摆动点所需的左右各N根K线，默认2（留空等同2，即本闸门标定时的取值）。注意本闸门的默认2与「结构方向对齐」闸门的默认3不同：后者的实测结论是 lb=2 属噪声、lb=3 才决定性，若两处要保持一致需显式设为3。',
        hintEn:
          'Bars either side required for a swing pivot. Default 2 (leaving it unset behaves as 2, the value this gate was calibrated with). Note this default of 2 differs from the Structural Alignment gate default of 3, where the measured result is that lb=2 is noise-level and lb=3 is decisive; set this explicitly to 3 if you want the two to agree.',
      },
    ],
  },
]

// In-sample bench findings (Virtual Trader Bench). Flagged when the bench measured
// a variant net-negative in-sample so the user can decide before enforcing.
function inSampleFlag(
  g: RegimeGateConfig,
  isZh: boolean
): { text: string; positive: boolean } | null {
  const w = g.params?.slope_window
  if (g.category === 'counter_trend' && (w ?? 30) === 30) {
    return {
      positive: true,
      text: isZh
        ? '样本内唯一正期望门控 (+120R)，唯一建议默认强制的选项'
        : 'Only gate with a real in-sample edge (+120R); the sole recommended enforce default',
    }
  }
  if (g.category === 'counter_trend' && (w ?? 30) === 50) {
    return {
      positive: false,
      text: isZh
        ? '样本内为负 (-36.6R)，强制前请谨慎'
        : 'In-sample negative (-36.6R); enforce with caution',
    }
  }
  if (g.category === 'trend_direction_only' && (w ?? 50) === 50) {
    return {
      positive: false,
      text: isZh
        ? '样本内为负 (-34.7R)，强制前请谨慎'
        : 'In-sample negative (-34.7R); enforce with caution',
    }
  }
  return null
}
function defaultGateFor(spec: CategorySpec): RegimeGateConfig {
  const params: RegimeGateConfig['params'] = {}
  for (const p of spec.params) {
    if (p.kind === 'side') params.block_side = p.default as 'LONG' | 'SHORT'
    else (params as Record<string, number>)[p.key] = p.default as number
  }
  return {
    category: spec.category,
    mode: 'shadow',
    enabled: true,
    params,
  }
}

export function RegimeGatesEditor({
  gates,
  onChange,
  disabled,
  language,
}: RegimeGatesEditorProps) {
  const isZh = language === 'zh'
  const list = useMemo(() => gates ?? [], [gates])

  const update = (idx: number, next: RegimeGateConfig) => {
    if (disabled) return
    onChange(list.map((g, i) => (i === idx ? next : g)))
  }
  const remove = (idx: number) => {
    if (disabled) return
    onChange(list.filter((_, i) => i !== idx))
  }
  const add = (spec: CategorySpec) => {
    if (disabled) return
    onChange([...list, defaultGateFor(spec)])
  }

  const setParam = (idx: number, key: ParamKey, value: number | string) => {
    const g = list[idx]
    update(idx, {
      ...g,
      params: { ...(g.params || {}), [key]: value },
    })
  }

  return (
    <div className="space-y-4">
      {/* Header */}
      <div
        className="p-3 rounded-lg"
        style={{ background: '#0B0E11', border: '1px solid #F59E0B33' }}
      >
        <div className="flex items-center gap-2 mb-2">
          <ShieldAlert className="w-5 h-5" style={{ color: '#F59E0B' }} />
          <h3 className="font-medium" style={{ color: '#EAECEF' }}>
            {isZh ? '状态门控 / Regime Gates' : 'Regime Gates'}
          </h3>
        </div>
        <div
          className="text-[11px] leading-relaxed"
          style={{ color: '#848E9C' }}
        >
          {isZh
            ? '从虚拟交易员对战台晋升的可配置门控。可多选，按大类添加；同一大类用参数区分变体。观察 (shadow) 只记录反事实成绩、不真正拦截；强制 (enforce) 会真正拦下开仓，被拦订单会按开仓设定回放并单独存档评测。'
            : 'Configurable gates promoted from the Virtual Trader Bench. Multi-select by category; params differentiate variants within a category. shadow only records the counterfactual verdict; enforce actually blocks the open, and the blocked intent is replayed and archived for scoring.'}
        </div>
      </div>

      {/* Catalog: add buttons by category */}
      <div className="p-3 rounded-lg space-y-2" style={sectionStyle}>
        <div className="text-xs mb-1" style={{ color: '#848E9C' }}>
          {isZh ? '添加门控（按大类）' : 'Add gate (by category)'}
        </div>
        <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
          {CATALOG.map((spec) => (
            <button
              key={spec.category}
              type="button"
              onClick={() => add(spec)}
              disabled={disabled}
              className="flex items-center gap-2 px-3 py-2 rounded text-sm border text-left"
              style={{
                background: '#11161C',
                borderColor: `${spec.color}55`,
                color: '#EAECEF',
                opacity: disabled ? 0.5 : 1,
              }}
            >
              <Plus className="w-3.5 h-3.5" style={{ color: spec.color }} />
              <span>{isZh ? spec.titleZh : spec.titleEn}</span>
            </button>
          ))}
        </div>
      </div>

      {/* Configured gates */}
      {list.length === 0 ? (
        <div
          className="p-4 rounded-lg text-center text-sm"
          style={{ ...sectionStyle, color: '#5E6673' }}
        >
          {isZh
            ? '尚未配置任何状态门控。从上方按大类添加。'
            : 'No regime gates configured. Add one by category above.'}
        </div>
      ) : (
        list.map((g, idx) => {
          const spec = CATALOG.find((c) => c.category === g.category)
          if (!spec) return null
          const flag = inSampleFlag(g, isZh)
          const isEnforce = g.mode === 'enforce'
          return (
            <div
              key={`${g.category}-${idx}`}
              className="p-3 rounded-lg space-y-3"
              style={{
                background: '#11161C',
                border: `1px solid ${g.enabled ? spec.color : '#2B3139'}55`,
                opacity: g.enabled ? 1 : 0.6,
              }}
            >
              {/* Title row */}
              <div className="flex items-start justify-between gap-3">
                <label
                  className="flex items-center gap-2 text-sm cursor-pointer"
                  style={{ color: '#EAECEF' }}
                >
                  <input
                    type="checkbox"
                    checked={g.enabled}
                    onChange={(e) =>
                      update(idx, { ...g, enabled: e.target.checked })
                    }
                    disabled={disabled}
                    className="h-4 w-4 accent-amber-500"
                  />
                  <span className="font-medium">
                    {isZh ? spec.titleZh : spec.titleEn}
                  </span>
                </label>
                <button
                  type="button"
                  onClick={() => remove(idx)}
                  disabled={disabled}
                  className="p-1 rounded"
                  style={{ color: '#F6465D' }}
                  title={isZh ? '删除' : 'Remove'}
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>

              <div
                className="text-[11px] leading-relaxed"
                style={{ color: '#848E9C' }}
              >
                {isZh ? spec.descZh : spec.descEn}
              </div>

              {flag && (
                <div
                  className="text-[11px] px-2 py-1 rounded"
                  style={{
                    background: flag.positive ? '#0ECB8115' : '#F6465D15',
                    color: flag.positive ? '#0ECB81' : '#F6465D',
                    border: `1px solid ${flag.positive ? '#0ECB81' : '#F6465D'}44`,
                  }}
                >
                  {flag.text}
                </div>
              )}

              {/* Mode toggle: shadow vs enforce */}
              <div className="flex gap-2">
                {(['shadow', 'enforce'] as RegimeGateMode[]).map((m) => {
                  const active = g.mode === m
                  const isEnf = m === 'enforce'
                  return (
                    <button
                      key={m}
                      type="button"
                      onClick={() => update(idx, { ...g, mode: m })}
                      disabled={disabled}
                      className="flex items-center gap-1.5 px-3 py-1.5 rounded text-xs border"
                      style={{
                        background: active
                          ? isEnf
                            ? '#F6465D22'
                            : '#38BDF822'
                          : '#0B0E11',
                        borderColor: active
                          ? isEnf
                            ? '#F6465D'
                            : '#38BDF8'
                          : '#2B3139',
                        color: active ? '#EAECEF' : '#848E9C',
                      }}
                    >
                      {isEnf ? (
                        <Ban className="w-3 h-3" />
                      ) : (
                        <Eye className="w-3 h-3" />
                      )}
                      {isEnf
                        ? isZh
                          ? '强制拦截'
                          : 'Enforce'
                        : isZh
                          ? '仅观察'
                          : 'Shadow'}
                    </button>
                  )
                })}
              </div>

              {isEnforce && (
                <div
                  className="text-[11px] px-2 py-1 rounded"
                  style={{
                    background: '#F6465D10',
                    color: '#F6465D',
                    border: '1px solid #F6465D33',
                  }}
                >
                  {isZh
                    ? '⚠ 强制模式会真正拦下符合条件的开仓（实盘生效）。'
                    : '⚠ Enforce actively blocks matching opens (live).'}
                </div>
              )}

              {/* Params */}
              {spec.params.length > 0 && (
                <div className="grid grid-cols-2 gap-3 pt-1">
                  {spec.params.map((p) => {
                    if (p.kind === 'side') {
                      const cur =
                        (g.params?.block_side as string) ??
                        (p.default as string)
                      return (
                        <div key={p.key} className="col-span-2">
                          <label
                            className="block text-[11px] mb-1"
                            style={{ color: '#848E9C' }}
                          >
                            {isZh ? p.labelZh : p.labelEn}
                          </label>
                          <div className="flex gap-2">
                            {(['LONG', 'SHORT'] as const).map((s) => (
                              <button
                                key={s}
                                type="button"
                                onClick={() => setParam(idx, 'block_side', s)}
                                disabled={disabled}
                                className="px-3 py-1.5 rounded text-xs border"
                                style={{
                                  background: cur === s ? '#1E3A5F' : '#0B0E11',
                                  borderColor:
                                    cur === s ? '#38BDF8' : '#2B3139',
                                  color: cur === s ? '#EAECEF' : '#848E9C',
                                }}
                              >
                                {s}
                              </button>
                            ))}
                          </div>
                          {(isZh ? p.hintZh : p.hintEn) && (
                            <div
                              className="text-[10px] mt-1"
                              style={{ color: '#5E6673' }}
                            >
                              {isZh ? p.hintZh : p.hintEn}
                            </div>
                          )}
                        </div>
                      )
                    }
                    const num =
                      (g.params as Record<string, number> | undefined)?.[
                        p.key
                      ] ?? (p.default as number)
                    return (
                      <div key={p.key}>
                        <label
                          className="block text-[11px] mb-1"
                          style={{ color: '#848E9C' }}
                        >
                          {isZh ? p.labelZh : p.labelEn}
                        </label>
                        <input
                          type="number"
                          value={num}
                          step={p.step ?? 1}
                          min={0}
                          onChange={(e) =>
                            setParam(
                              idx,
                              p.key,
                              parseFloat(e.target.value) || 0
                            )
                          }
                          disabled={disabled}
                          className="w-full px-3 py-2 rounded text-sm"
                          style={inputStyle}
                        />
                        {(isZh ? p.hintZh : p.hintEn) && (
                          <div
                            className="text-[10px] mt-1"
                            style={{ color: '#5E6673' }}
                          >
                            {isZh ? p.hintZh : p.hintEn}
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          )
        })
      )}
    </div>
  )
}
