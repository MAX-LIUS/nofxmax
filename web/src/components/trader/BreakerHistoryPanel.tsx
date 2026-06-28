import { useEffect, useState } from 'react'
import { dataApi } from '../../lib/api/data'
import type { BreakerEvent } from '../../types'

interface BreakerHistoryPanelProps {
  traderId?: string
  language: string
}

const L = (lang: string, zh: string, en: string) => (lang === 'zh' ? zh : en)

function fmtTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const mm = String(d.getMonth() + 1).padStart(2, '0')
  const dd = String(d.getDate()).padStart(2, '0')
  const hh = String(d.getHours()).padStart(2, '0')
  const mi = String(d.getMinutes()).padStart(2, '0')
  return `${mm}-${dd} ${hh}:${mi}`
}

// BreakerHistoryPanel lists recent circuit-breaker / breadth-guard close events
// so the operator can audit when the portfolio breaker fired and its cost.
export function BreakerHistoryPanel({
  traderId,
  language,
}: BreakerHistoryPanelProps) {
  const [events, setEvents] = useState<BreakerEvent[]>([])
  const [totalPnl, setTotalPnl] = useState(0)
  const [loading, setLoading] = useState(false)
  const [days, setDays] = useState(7)

  useEffect(() => {
    let cancelled = false
    async function load() {
      if (!traderId) return
      setLoading(true)
      try {
        const resp = await dataApi.getBreakerHistory(traderId, days)
        if (cancelled) return
        setEvents(resp.events || [])
        setTotalPnl(resp.total_pnl || 0)
      } catch {
        if (!cancelled) {
          setEvents([])
          setTotalPnl(0)
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => {
      cancelled = true
    }
  }, [traderId, days])

  return (
    <div className="nofx-glass p-4 rounded-lg border border-white/5">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-bold text-nofx-text-main uppercase tracking-wide flex items-center gap-2">
          <span className="text-nofx-red">◈</span>
          {L(language, '熔断历史', 'Breaker History')}
          {loading && (
            <span className="text-[10px] text-nofx-text-muted ml-1">⟳</span>
          )}
        </h3>
        <div className="flex items-center gap-2">
          <span
            className={`text-xs font-mono font-bold ${totalPnl >= 0 ? 'text-nofx-green' : 'text-nofx-red'}`}
          >
            {totalPnl >= 0 ? '+' : ''}
            {totalPnl.toFixed(2)} USDT
          </span>
          <select
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
            className="bg-black/40 border border-white/10 rounded text-[10px] text-nofx-text-muted px-1 py-0.5"
          >
            <option value={7} className="bg-[#0B0E11]">
              7d
            </option>
            <option value={30} className="bg-[#0B0E11]">
              30d
            </option>
            <option value={90} className="bg-[#0B0E11]">
              90d
            </option>
          </select>
        </div>
      </div>

      {events.length > 0 ? (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="text-nofx-text-muted border-b border-white/10">
                <th className="text-left py-1 pr-2 font-medium">
                  {L(language, '时间', 'Time')}
                </th>
                <th className="text-left py-1 px-2 font-medium">
                  {L(language, '标的', 'Symbol')}
                </th>
                <th className="text-center py-1 px-2 font-medium">
                  {L(language, '方向', 'Side')}
                </th>
                <th className="text-right py-1 px-2 font-medium">
                  {L(language, '平仓比', 'Ratio')}
                </th>
                <th className="text-right py-1 pl-2 font-medium">
                  {L(language, '盈亏', 'PnL')}
                </th>
              </tr>
            </thead>
            <tbody>
              {events.map((ev, i) => (
                <tr
                  key={`${ev.symbol}-${ev.event_time}-${i}`}
                  className="border-b border-white/5 hover:bg-white/5"
                >
                  <td className="py-1 pr-2 font-mono text-nofx-text-muted whitespace-nowrap">
                    {fmtTime(ev.event_time)}
                  </td>
                  <td className="py-1 px-2 font-mono text-nofx-text-main">
                    {ev.symbol}
                  </td>
                  <td className="py-1 px-2 text-center">
                    <span
                      className={`px-1.5 py-0.5 rounded text-[10px] font-bold uppercase ${
                        ev.side.toUpperCase() === 'LONG'
                          ? 'bg-nofx-green/10 text-nofx-green'
                          : 'bg-nofx-red/10 text-nofx-red'
                      }`}
                    >
                      {ev.side}
                    </span>
                  </td>
                  <td className="py-1 px-2 text-right font-mono text-nofx-text-main">
                    {ev.close_ratio_pct > 0
                      ? `${ev.close_ratio_pct.toFixed(0)}%`
                      : '—'}
                  </td>
                  <td
                    className={`py-1 pl-2 text-right font-mono font-semibold ${
                      ev.realized_pnl >= 0 ? 'text-nofx-green' : 'text-nofx-red'
                    }`}
                  >
                    {ev.realized_pnl >= 0 ? '+' : ''}
                    {ev.realized_pnl.toFixed(2)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="text-xs text-nofx-text-muted border border-white/10 rounded px-3 py-2">
          {L(language, '近期无熔断事件', 'No recent breaker events')}
        </div>
      )}
    </div>
  )
}
