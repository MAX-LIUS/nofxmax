import { useEffect, useState } from 'react'
import { dataApi } from '../../lib/api/data'
import type {
  CloseAttributionResponse,
  AttributionMechanismRow,
} from '../../types'

interface CloseAttributionPanelProps {
  traderId?: string
  language: string
}

const L = (lang: string, zh: string, en: string) => (lang === 'zh' ? zh : en)

// Category display metadata: label + color. Mirrors the backend taxonomy in
// store/attribution.go (ai | protection | manual | exchange | system).
const CATEGORY_META: Record<string, { zh: string; en: string; cls: string }> = {
  ai: {
    zh: 'AI 主动',
    en: 'AI',
    cls: 'text-blue-400 border-blue-500/30 bg-blue-500/10',
  },
  protection: {
    zh: '保护单',
    en: 'Protection',
    cls: 'text-green-400 border-green-500/30 bg-green-500/10',
  },
  manual: {
    zh: '手工',
    en: 'Manual',
    cls: 'text-purple-400 border-purple-500/30 bg-purple-500/10',
  },
  exchange: {
    zh: '交易所',
    en: 'Exchange',
    cls: 'text-amber-400 border-amber-500/30 bg-amber-500/10',
  },
  system: {
    zh: '系统/未分类',
    en: 'System',
    cls: 'text-gray-400 border-gray-500/30 bg-gray-500/10',
  },
}

const MECH_LABEL: Record<string, { zh: string; en: string }> = {
  ladder_tp: { zh: '阶梯止盈', en: 'Ladder TP' },
  ladder_sl: { zh: '阶梯止损', en: 'Ladder SL' },
  full_tp: { zh: '全量止盈', en: 'Full TP' },
  full_sl: { zh: '全量止损', en: 'Full SL' },
  structural_sl: { zh: '结构位止损', en: 'Structural SL' },
  fallback_maxloss_sl: { zh: '兜底止损', en: 'Fallback SL' },
  native_trailing: { zh: '移动止损', en: 'Trailing' },
  managed_drawdown: { zh: '回撤止盈', en: 'Drawdown' },
  break_even_stop: { zh: '保本止损', en: 'Break-even' },
  time_stop: { zh: '时间止损', en: 'Time stop' },
  max_hold: { zh: '超时平仓', en: 'Max hold' },
  trailing_take_profit: { zh: '移动止盈', en: 'Trailing TP' },
  breadth_breaker: { zh: '广度熔断', en: 'Breadth breaker' },
  trend_reversal_flip: { zh: '趋势反转', en: 'Reversal flip' },
  legacy_unknown: { zh: '历史未记录', en: 'Legacy unknown' },
  ai_close: { zh: 'AI 平仓', en: 'AI close' },
  manual_close: { zh: '手工平仓', en: 'Manual close' },
  liquidation: { zh: '强平/ADL', en: 'Liquidation' },
  emergency_protection_close: { zh: '紧急平仓', en: 'Emergency' },
  sync_external: { zh: '交易所平仓(未归因)', en: 'Exchange (unattributed)' },
  unknown_close: { zh: '未知', en: 'Unknown' },
}

function fmtUsd(v: number): string {
  const sign = v > 0 ? '+' : v < 0 ? '-' : ''
  return `${sign}$${Math.abs(v).toFixed(2)}`
}

function mechLabel(lang: string, mech: string): string {
  const m = MECH_LABEL[mech]
  return m ? L(lang, m.zh, m.en) : mech
}

// CloseAttributionPanel renders the canonical per-mechanism exit breakdown so
// every close is traceable: AI vs protection vs manual vs exchange, each with
// its count and realized PnL contribution. Data from /positions/attribution.
export function CloseAttributionPanel({
  traderId,
  language,
}: CloseAttributionPanelProps) {
  const [data, setData] = useState<CloseAttributionResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [days, setDays] = useState(30)

  useEffect(() => {
    if (!traderId) return
    let cancelled = false
    setLoading(true)
    dataApi
      .getCloseAttribution(traderId, days)
      .then((d) => {
        if (!cancelled) setData(d)
      })
      .catch(() => {
        if (!cancelled) setData(null)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [traderId, days])

  if (!traderId) return null

  const totalEvents = data?.total_events ?? 0

  return (
    <div className="bg-gray-900/60 border border-gray-700/50 rounded-lg p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-medium text-gray-200">
          {L(language, '平仓归因', 'Close Attribution')}
        </h3>
        <select
          value={days}
          onChange={(e) => setDays(Number(e.target.value))}
          className="bg-gray-800 border border-gray-600 rounded text-xs text-gray-300 px-2 py-1"
        >
          <option value={7}>{L(language, '近7天', '7d')}</option>
          <option value={30}>{L(language, '近30天', '30d')}</option>
          <option value={90}>{L(language, '近90天', '90d')}</option>
        </select>
      </div>

      {loading && (
        <div className="text-xs text-gray-500 py-4 text-center">
          {L(language, '加载中…', 'Loading…')}
        </div>
      )}

      {!loading && totalEvents === 0 && (
        <div className="text-xs text-gray-500 py-4 text-center">
          {L(language, '该时间窗内无平仓记录', 'No closes in this window')}
        </div>
      )}

      {!loading && totalEvents > 0 && data && (
        <>
          <CategorySummary data={data} language={language} />
          <MechanismTable rows={data.by_mechanism} language={language} />
        </>
      )}
    </div>
  )
}

function CategorySummary({
  data,
  language,
}: {
  data: CloseAttributionResponse
  language: string
}) {
  return (
    <div className="flex flex-wrap gap-2 mb-3">
      {data.by_category
        .slice()
        .sort((a, b) => b.count - a.count)
        .map((c) => {
          const meta = CATEGORY_META[c.category] || CATEGORY_META.system
          const pct =
            data.total_events > 0
              ? Math.round((c.count / data.total_events) * 100)
              : 0
          return (
            <div
              key={c.category}
              className={`border rounded px-2.5 py-1.5 ${meta.cls}`}
            >
              <div className="text-xs font-medium">
                {L(language, meta.zh, meta.en)} · {pct}%
              </div>
              <div className="text-[11px] opacity-80">
                {c.count}
                {L(language, ' 笔 · ', ' · ')}
                {fmtUsd(c.realized_pnl)}
              </div>
            </div>
          )
        })}
    </div>
  )
}

function MechanismTable({
  rows,
  language,
}: {
  rows: AttributionMechanismRow[]
  language: string
}) {
  const sorted = rows.slice().sort((a, b) => b.count - a.count)
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs">
        <thead>
          <tr className="text-gray-500 border-b border-gray-700/50">
            <th className="text-left py-1.5 font-normal">
              {L(language, '机制', 'Mechanism')}
            </th>
            <th className="text-right py-1.5 font-normal">
              {L(language, '笔数', 'Count')}
            </th>
            <th className="text-right py-1.5 font-normal">
              {L(language, '已实现盈亏', 'PnL')}
            </th>
            <th className="text-right py-1.5 font-normal">
              {L(language, '手续费', 'Fees')}
            </th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((r) => {
            const meta = CATEGORY_META[r.category] || CATEGORY_META.system
            return (
              <tr
                key={`${r.category}-${r.mechanism}`}
                className="border-b border-gray-800/40"
              >
                <td className="py-1.5">
                  <span
                    className={`inline-block w-1.5 h-1.5 rounded-full mr-1.5 ${meta.cls.split(' ')[0].replace('text', 'bg')}`}
                  />
                  {mechLabel(language, r.mechanism)}
                </td>
                <td className="text-right text-gray-300">{r.count}</td>
                <td
                  className={`text-right ${r.realized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}`}
                >
                  {fmtUsd(r.realized_pnl)}
                </td>
                <td className="text-right text-gray-400">
                  ${Math.abs(r.fees).toFixed(2)}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
