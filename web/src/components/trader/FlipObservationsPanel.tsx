import { useEffect, useState } from 'react'
import { dataApi } from '../../lib/api/data'
import type { FlipObservationsResponse, FlipObservation } from '../../types'

interface FlipObservationsPanelProps {
  traderId?: string
  language: string
}

const L = (lang: string, zh: string, en: string) => (lang === 'zh' ? zh : en)

function formatTime(iso: string): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return '-'
  // NOFX timezone UTC+8
  return d.toLocaleString('zh-CN', {
    timeZone: 'Asia/Shanghai',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

export function FlipObservationsPanel({
  traderId,
  language,
}: FlipObservationsPanelProps) {
  const [data, setData] = useState<FlipObservationsResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!traderId) return
    let cancelled = false
    setLoading(true)
    setError(null)
    dataApi
      .getFlipObservations(traderId, 100)
      .then((res) => {
        if (!cancelled) setData(res)
      })
      .catch((e) => {
        if (!cancelled) setError(String(e))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [traderId])

  if (!traderId) return null

  return (
    <div className="rounded-lg border border-white/10 bg-white/5 p-4">
      <div className="flex items-center justify-between mb-3">
        <h3 className="text-sm font-semibold" style={{ color: '#EAECEF' }}>
          🔄 {L(language, '趋势反转翻仓', 'Trend-Reversal Flips')}
        </h3>
        {data && (
          <div className="text-xs" style={{ color: '#848E9C' }}>
            {L(language, '共', 'Total')} {data.total} ·{' '}
            <span style={{ color: '#F0B90B' }}>
              {L(language, '观察', 'dry-run')} {data.dry_run_count}
            </span>{' '}
            ·{' '}
            <span style={{ color: '#0ECB81' }}>
              {L(language, '已执行', 'live')} {data.executed_count}
            </span>
          </div>
        )}
      </div>

      <p className="text-[11px] mb-3 leading-5" style={{ color: '#848E9C' }}>
        {L(
          language,
          '当 AI 给出高确信反向信号（≥75）且持仓已满 6 小时，系统会平掉原仓并反向开仓。默认 dry-run（仅记录不下单），用于先评估 AI 反向信号质量。',
          'When AI signals a high-conviction reversal (≥75) on a position aged ≥6h, the system flips it. Default dry-run (logged, no order) to assess live AI signal quality first.'
        )}
      </p>

      {loading && (
        <div className="text-xs" style={{ color: '#848E9C' }}>
          {L(language, '加载中…', 'Loading…')}
        </div>
      )}
      {error && (
        <div className="text-xs" style={{ color: '#F6465D' }}>
          {error}
        </div>
      )}

      {data && data.flips.length === 0 && !loading && (
        <div className="text-xs py-4 text-center" style={{ color: '#848E9C' }}>
          {L(
            language,
            '暂无翻仓信号 — AI 尚未给出满足条件的高确信反向决策',
            'No flip signals yet — AI has not produced a qualifying high-conviction reversal'
          )}
        </div>
      )}

      {data && data.flips.length > 0 && (
        <FlipTable flips={data.flips} language={language} />
      )}
    </div>
  )
}

function FlipTable({
  flips,
  language,
}: {
  flips: FlipObservation[]
  language: string
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-xs" style={{ borderCollapse: 'collapse' }}>
        <thead>
          <tr style={{ borderBottom: '1px solid #2B3139' }}>
            <th
              className="text-left py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '时间', 'Time')}
            </th>
            <th
              className="text-left py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '交易对', 'Symbol')}
            </th>
            <th
              className="text-left py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '方向', 'Flip')}
            </th>
            <th
              className="text-right py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '确信', 'Conf')}
            </th>
            <th
              className="text-right py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '持仓时长', 'Age')}
            </th>
            <th
              className="text-right py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '反向仓盈亏', 'Flip PnL')}
            </th>
            <th
              className="text-center py-2 px-2"
              style={{ color: '#848E9C', fontWeight: 500 }}
            >
              {L(language, '模式', 'Mode')}
            </th>
          </tr>
        </thead>
        <tbody>
          {flips.map((f, i) => (
            <tr
              key={`${f.symbol}-${f.observed_at}-${i}`}
              style={{ borderBottom: '1px solid #2B313922' }}
            >
              <td className="py-2 px-2 font-mono" style={{ color: '#EAECEF' }}>
                {formatTime(f.observed_at)}
              </td>
              <td
                className="py-2 px-2 font-mono font-semibold"
                style={{ color: '#EAECEF' }}
              >
                {f.symbol.replace('USDT', '')}
              </td>
              <td className="py-2 px-2 font-mono">
                <span
                  style={{
                    color: f.from_side === 'long' ? '#0ECB81' : '#F6465D',
                  }}
                >
                  {f.from_side.toUpperCase()}
                </span>
                <span style={{ color: '#848E9C' }}> → </span>
                <span
                  style={{
                    color: f.to_side === 'long' ? '#0ECB81' : '#F6465D',
                  }}
                >
                  {f.to_side.toUpperCase()}
                </span>
              </td>
              <td
                className="py-2 px-2 text-right font-mono"
                style={{ color: '#EAECEF' }}
              >
                {f.confidence}
              </td>
              <td
                className="py-2 px-2 text-right font-mono"
                style={{ color: '#EAECEF' }}
              >
                {f.age_hours.toFixed(1)}h
              </td>
              <td className="py-2 px-2 text-right font-mono">
                {f.executed && f.reverse_realized_pnl !== 0 ? (
                  <span
                    style={{
                      color:
                        f.reverse_realized_pnl >= 0 ? '#0ECB81' : '#F6465D',
                    }}
                  >
                    {f.reverse_realized_pnl >= 0 ? '+' : ''}
                    {f.reverse_realized_pnl.toFixed(2)}
                  </span>
                ) : (
                  <span style={{ color: '#848E9C' }}>—</span>
                )}
              </td>
              <td className="py-2 px-2 text-center">
                <span
                  className="px-2 py-0.5 rounded text-[10px] font-medium"
                  style={
                    f.executed
                      ? {
                          color: '#0ECB81',
                          background: '#0ECB8118',
                          border: '1px solid #0ECB8133',
                        }
                      : {
                          color: '#F0B90B',
                          background: '#F0B90B18',
                          border: '1px solid #F0B90B33',
                        }
                  }
                >
                  {f.executed
                    ? L(language, '已执行', 'live')
                    : L(language, '观察', 'dry-run')}
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
