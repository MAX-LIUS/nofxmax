import type { ATRProtectionConfig, ATRDimMode } from '../../types/strategy'

interface ATRProtectionEditorProps {
  config?: ATRProtectionConfig
  onChange: (config: ATRProtectionConfig) => void
  disabled?: boolean
  language?: 'zh' | 'en'
}

const DEFAULTS: ATRProtectionConfig = {
  enabled: false,
  timeframe: '1h',
  atr_period: 14,
  multiple_mode: 'fixed',
  sl_mode: 'fixed',
  tp1_mode: 'fixed',
  tp2_mode: 'fixed',
  be1_mode: 'fixed',
  be2_mode: 'fixed',
  dd_mode: 'percent',
  stop_loss_atr: 4.5,
  take_profit_1_atr: 3.0,
  take_profit_2_atr: 5.0,
  break_even_1_atr: 2.0,
  break_even_2_atr: 2.5,
  drawdown_min_profit_atr: 3.0,
  min_eff_pct: 0.3,
  max_eff_pct: 25,
  ai_min_mult: 0.5,
  ai_max_mult: 10,
}

const T = {
  zh: {
    title: 'ATR 自适应保护',
    desc: '逐维度选择保护距离的计量方式:百分比(用下方策略固定%)、ATR手填(回测最优倍数)、或 AI 按币种分析(开仓时 AI 给个性化倍数)。',
    enable: '启用 ATR 自适应保护',
    timeframe: 'ATR 周期框架',
    atrPeriod: 'ATR 长度',
    dim: '维度',
    mode: '计量方式',
    mult: 'ATR 倍数',
    modePercent: '百分比',
    modeFixed: 'ATR手填',
    modeAI: 'AI按币种',
    rows: {
      sl: '止损 SL',
      tp1: '止盈一档 TP1',
      tp2: '止盈二档 TP2',
      be1: '保本一档 BE1',
      be2: '保本二档 BE2',
      dd: '回撤保护 DD(触发利润)',
    },
    minPct: '换算下限 (%)',
    maxPct: '换算上限 (%)',
    aiNote: 'AI 模式:开仓时 AI 分析该币波动给出个性化倍数(每币不同),手填值作为 AI 失败回退,按币种缓存 4 小时。',
    note: '注:手填默认(SL4.5/TP1 3.0/TP2 5.0/BE1 2.0/BE2 2.5)来自 8 币种 6 月长周期回测最优(全内部解,PF 1.11)。DD 的最大回撤比例仍是峰值百分比(比率,不适用 ATR),这里只 ATR 化「触发利润」距离。',
  },
  en: {
    title: 'ATR-Adaptive Protection',
    desc: 'Per-dimension distance unit: percent (use strategy %), ATR-manual (backtest-best multiple), or AI per-coin (AI returns a custom multiple at open).',
    enable: 'Enable ATR-adaptive protection',
    timeframe: 'ATR timeframe',
    atrPeriod: 'ATR period',
    dim: 'Dimension',
    mode: 'Unit',
    mult: 'ATR multiple',
    modePercent: 'Percent',
    modeFixed: 'ATR-manual',
    modeAI: 'AI per-coin',
    rows: {
      sl: 'Stop-loss SL',
      tp1: 'Take-profit 1',
      tp2: 'Take-profit 2',
      be1: 'Break-even 1',
      be2: 'Break-even 2',
      dd: 'Drawdown (arm profit)',
    },
    minPct: 'Derived floor (%)',
    maxPct: 'Derived cap (%)',
    aiNote: 'AI mode: AI analyzes this coin’s volatility at open and returns a custom multiple (per coin); the manual value is the fallback. Cached 4h per coin.',
    note: 'Manual defaults (SL4.5/TP1 3.0/TP2 5.0/BE1 2.0/BE2 2.5) come from an 8-symbol 6-month backtest optimum (interior, PF 1.11). DD max-drawdown stays a % of peak (a ratio, not ATR); only the arm-profit distance is ATR-ized.',
  },
}

type DimKey = 'sl' | 'tp1' | 'tp2' | 'be1' | 'be2' | 'dd'

const DIM_MODE_FIELD: Record<DimKey, keyof ATRProtectionConfig> = {
  sl: 'sl_mode',
  tp1: 'tp1_mode',
  tp2: 'tp2_mode',
  be1: 'be1_mode',
  be2: 'be2_mode',
  dd: 'dd_mode',
}

const DIM_MULT_FIELD: Record<DimKey, keyof ATRProtectionConfig> = {
  sl: 'stop_loss_atr',
  tp1: 'take_profit_1_atr',
  tp2: 'take_profit_2_atr',
  be1: 'break_even_1_atr',
  be2: 'break_even_2_atr',
  dd: 'drawdown_min_profit_atr',
}

export function ATRProtectionEditor({
  config,
  onChange,
  disabled,
  language = 'zh',
}: ATRProtectionEditorProps) {
  const t = T[language]
  const c = { ...DEFAULTS, ...(config || {}) }

  const set = (patch: Partial<ATRProtectionConfig>) => onChange({ ...c, ...patch })

  const inputStyle = (on: boolean) => ({
    padding: '6px 8px',
    borderRadius: 6,
    border: '1px solid #374151',
    background: disabled || !on ? '#1f2937' : '#111827',
    color: '#e5e7eb',
    width: '100%',
  })

  const dimRow = (key: DimKey) => {
    const mode = (c[DIM_MODE_FIELD[key]] as ATRDimMode) || 'fixed'
    const multField = DIM_MULT_FIELD[key]
    const multOn = c.enabled && mode !== 'percent'
    return (
      <div
        key={key}
        style={{
          display: 'grid',
          gridTemplateColumns: '1.4fr 1.1fr 1fr',
          gap: 10,
          alignItems: 'center',
          marginBottom: 8,
        }}
      >
        <span style={{ color: '#cbd5e1', fontSize: 13 }}>{t.rows[key]}</span>
        <select
          disabled={disabled || !c.enabled}
          value={mode}
          onChange={(e) => set({ [DIM_MODE_FIELD[key]]: e.target.value as ATRDimMode })}
          style={inputStyle(c.enabled)}
        >
          <option value="percent">{t.modePercent}</option>
          <option value="fixed">{t.modeFixed}</option>
          <option value="ai">{t.modeAI}</option>
        </select>
        <input
          type="number"
          step={0.5}
          disabled={disabled || !multOn}
          value={(c[multField] as number) ?? 0}
          onChange={(e) => set({ [multField]: parseFloat(e.target.value) || 0 })}
          style={inputStyle(multOn)}
          placeholder={mode === 'ai' ? 'fallback' : ''}
        />
      </div>
    )
  }

  const anyAI = (['sl', 'tp1', 'tp2', 'be1', 'be2', 'dd'] as DimKey[]).some(
    (k) => ((c[DIM_MODE_FIELD[k]] as ATRDimMode) || 'fixed') === 'ai'
  )

  return (
    <div style={{ border: '1px solid #374151', borderRadius: 8, padding: 16, marginTop: 16, background: '#0f1623' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <h4 style={{ margin: 0, color: '#e5e7eb', fontSize: 15 }}>🎯 {t.title}</h4>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 14 }}>
          <input type="checkbox" disabled={disabled} checked={c.enabled} onChange={(e) => set({ enabled: e.target.checked })} />
          <span style={{ color: c.enabled ? '#34d399' : '#9ca3af' }}>{t.enable}</span>
        </label>
      </div>
      <p style={{ color: '#6b7280', fontSize: 12, margin: '8px 0 14px' }}>{t.desc}</p>

      <div style={{ display: 'flex', gap: 12, marginBottom: 14, flexWrap: 'wrap' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, minWidth: 120 }}>
          <span style={{ color: '#9ca3af' }}>{t.timeframe}</span>
          <select disabled={disabled || !c.enabled} value={c.timeframe} onChange={(e) => set({ timeframe: e.target.value })} style={inputStyle(c.enabled)}>
            {['15m', '30m', '1h', '2h', '4h'].map((tf) => (
              <option key={tf} value={tf}>{tf}</option>
            ))}
          </select>
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, minWidth: 100 }}>
          <span style={{ color: '#9ca3af' }}>{t.atrPeriod}</span>
          <input type="number" step={1} disabled={disabled || !c.enabled} value={c.atr_period} onChange={(e) => set({ atr_period: parseInt(e.target.value) || 14 })} style={inputStyle(c.enabled)} />
        </label>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1.4fr 1.1fr 1fr', gap: 10, marginBottom: 6 }}>
        <span style={{ color: '#6b7280', fontSize: 11 }}>{t.dim}</span>
        <span style={{ color: '#6b7280', fontSize: 11 }}>{t.mode}</span>
        <span style={{ color: '#6b7280', fontSize: 11 }}>{t.mult}</span>
      </div>
      {(['sl', 'tp1', 'tp2', 'be1', 'be2', 'dd'] as DimKey[]).map(dimRow)}

      {c.enabled && anyAI && (
        <p style={{ color: '#fbbf24', fontSize: 12, margin: '10px 0 0', lineHeight: 1.5 }}>🤖 {t.aiNote}</p>
      )}

      <div style={{ display: 'flex', gap: 12, marginTop: 14, flexWrap: 'wrap' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, minWidth: 120 }}>
          <span style={{ color: '#9ca3af' }}>{t.minPct}</span>
          <input type="number" step={0.1} disabled={disabled || !c.enabled} value={c.min_eff_pct} onChange={(e) => set({ min_eff_pct: parseFloat(e.target.value) || 0 })} style={inputStyle(c.enabled)} />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13, minWidth: 120 }}>
          <span style={{ color: '#9ca3af' }}>{t.maxPct}</span>
          <input type="number" step={1} disabled={disabled || !c.enabled} value={c.max_eff_pct} onChange={(e) => set({ max_eff_pct: parseFloat(e.target.value) || 0 })} style={inputStyle(c.enabled)} />
        </label>
      </div>

      <p style={{ color: '#6b7280', fontSize: 11, marginTop: 12, lineHeight: 1.5 }}>{t.note}</p>
    </div>
  )
}

