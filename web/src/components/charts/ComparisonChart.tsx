import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceLine,
  Legend,
  Area,
  ComposedChart,
} from 'recharts'
import useSWR from 'swr'
import { api } from '../../lib/api'
import type { CompetitionTraderData } from '../../types'
import { getTraderColor } from '../../utils/traderColors'
import { useLanguage } from '../../contexts/LanguageContext'
import { t } from '../../i18n/translations'
import {
  BarChart3,
  TrendingUp,
  TrendingDown,
  Zap,
  RotateCcw,
  Layers,
} from 'lucide-react'
import {
  buildTimeTicks,
  formatAxisTick,
  clampDomain,
  panDomain,
  zoomDomain,
  PLOT_LEFT_PX,
  PLOT_RIGHT_PX,
} from './timeAxis'
import { mergeTraderHistories } from './comparisonMerge'

// Time period options: 1D, 3D, 7D, 30D, All
const TIME_PERIODS = [
  { key: '1d', hours: 24 },
  { key: '3d', hours: 72 },
  { key: '7d', hours: 168 },
  { key: '30d', hours: 720 },
  { key: 'all', hours: 0 },
] as const

// Per-trader sample budget requested from the server. The server downsamples with
// peak/trough preservation, so the drawn shape keeps its extremes.
const MAX_POINTS_PER_TRADER = 1500

interface ComparisonChartProps {
  traders: CompetitionTraderData[]
}

export function ComparisonChart({ traders }: ComparisonChartProps) {
  const { language } = useLanguage()
  const [selectedPeriod, setSelectedPeriod] = useState('7d') // Default to 7 days

  // Get hours for selected period
  const selectedHours =
    TIME_PERIODS.find((p) => p.key === selectedPeriod)?.hours || 0

  // Generate unique key for SWR (include period and hours)
  const tradersKey = traders
    .map((t) => t.trader_id)
    .sort()
    .join(',')

  const { data: batch, isLoading } = useSWR(
    traders.length > 0
      ? `equity-histories-${tradersKey}-${selectedHours}`
      : null,
    async () => {
      const traderIds = traders.map((trader) => trader.trader_id)
      const batchData = await api.getEquityHistoryBatch(
        traderIds,
        selectedHours,
        MAX_POINTS_PER_TRADER
      )
      const histories = traders.map((trader) => {
        const history = batchData.histories?.[trader.trader_id] || []

        // If backend doesn't return total_pnl_pct, calculate it from equity
        if (history.length > 0 && history[0].total_pnl_pct === undefined) {
          const initialEquity = history[0].total_equity
          history.forEach((point: any) => {
            point.total_pnl_pct =
              initialEquity > 0
                ? ((point.total_equity - initialEquity) / initialEquity) * 100
                : 0
          })
        }

        return history
      })
      return { histories, sampling: batchData.sampling || {} }
    },
    {
      refreshInterval: 30000,
      revalidateOnFocus: false,
      dedupingInterval: 0, // No deduping for immediate response
      keepPreviousData: false,
    }
  )

  const allTraderHistories = batch?.histories

  const traderHistories = useMemo(() => {
    if (!allTraderHistories) {
      return traders.map(() => ({ data: undefined }))
    }
    return allTraderHistories.map((data) => ({ data }))
  }, [allTraderHistories, traders.length])

  const combinedData = useMemo(() => {
    const allLoaded = traderHistories.every((h) => h.data)
    if (!allLoaded) return []
    return mergeTraderHistories(
      traders,
      traderHistories.map((h) => h.data as any[])
    )
  }, [allTraderHistories, traders])

  // Full time extent of the loaded data. The zoom/pan domain is clamped to this.
  const dataBounds = useMemo<[number, number]>(() => {
    if (combinedData.length === 0) return [0, 1]
    return [combinedData[0].ts, combinedData[combinedData.length - 1].ts]
  }, [combinedData])

  // null = follow the full extent. Set to a pair once the user zooms or pans, so a
  // 30s refresh does not yank the view back while they are inspecting something.
  const [viewDomain, setViewDomain] = useState<[number, number] | null>(null)
  const plotRef = useRef<HTMLDivElement>(null)
  const dragRef = useRef<{ x: number; domain: [number, number] } | null>(null)
  const [isDragging, setIsDragging] = useState(false)

  // Changing the range button re-fetches a different window, so any zoom into the
  // old window is meaningless: reset to the full extent of the new data.
  useEffect(() => {
    setViewDomain(null)
  }, [selectedPeriod])

  const effectiveDomain = useMemo<[number, number]>(
    () => (viewDomain ? clampDomain(viewDomain, dataBounds) : dataBounds),
    [viewDomain, dataBounds]
  )
  const isZoomed = viewDomain !== null

  // Fraction of the plot width under the pointer, used to anchor wheel zoom on the
  // cursor. The axis and margins are not part of the plot area, so they are
  // subtracted or zooming drifts toward the left edge.
  const pointerFrac = useCallback((clientX: number): number => {
    const el = plotRef.current
    if (!el) return 0.5
    const rect = el.getBoundingClientRect()
    const plotWidth = rect.width - PLOT_LEFT_PX - PLOT_RIGHT_PX
    if (plotWidth <= 0) return 0.5
    return (clientX - rect.left - PLOT_LEFT_PX) / plotWidth
  }, [])

  // Registered manually rather than via onWheel because React attaches wheel
  // listeners passively; preventDefault in a passive listener is ignored, so the
  // page would scroll while zooming.
  useEffect(() => {
    const el = plotRef.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (combinedData.length === 0) return
      e.preventDefault()
      const factor = e.deltaY > 0 ? 1.25 : 0.8
      setViewDomain((cur) =>
        zoomDomain(
          cur ?? dataBounds,
          dataBounds,
          factor,
          pointerFrac(e.clientX)
        )
      )
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [dataBounds, combinedData.length, pointerFrac])

  const onMouseDown = useCallback(
    (e: React.MouseEvent) => {
      if (combinedData.length === 0) return
      dragRef.current = { x: e.clientX, domain: effectiveDomain }
      setIsDragging(true)
    },
    [effectiveDomain, combinedData.length]
  )

  useEffect(() => {
    if (!isDragging) return
    const onMove = (e: MouseEvent) => {
      const start = dragRef.current
      const el = plotRef.current
      if (!start || !el) return
      const plotWidth =
        el.getBoundingClientRect().width - PLOT_LEFT_PX - PLOT_RIGHT_PX
      if (plotWidth <= 0) return
      // Dragging right should move the view EARLIER in time, the direction the
      // content moves under the hand.
      const frac = -(e.clientX - start.x) / plotWidth
      setViewDomain(panDomain(start.domain, dataBounds, frac))
    }
    const onUp = () => {
      dragRef.current = null
      setIsDragging(false)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
    return () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
  }, [isDragging, dataBounds])

  // Get trader color
  const traderColor = (traderId: string) => getTraderColor(traders, traderId)

  if (isLoading) {
    return (
      <div className="flex flex-col items-center justify-center py-20">
        <div className="relative">
          <div
            className="w-16 h-16 border-4 border-t-transparent rounded-full animate-spin"
            style={{ borderColor: '#F0B90B', borderTopColor: 'transparent' }}
          />
          <TrendingUp
            className="w-6 h-6 absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2"
            style={{ color: '#F0B90B' }}
          />
        </div>
        <div className="text-sm mt-4 font-medium" style={{ color: '#848E9C' }}>
          {t('loadingChartData', language) || 'Loading chart data...'}
        </div>
      </div>
    )
  }

  if (combinedData.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center py-20">
        <div
          className="w-20 h-20 rounded-2xl flex items-center justify-center mb-4"
          style={{ background: 'rgba(240, 185, 11, 0.1)' }}
        >
          <BarChart3
            className="w-10 h-10"
            style={{ color: '#F0B90B', opacity: 0.6 }}
          />
        </div>
        <div className="text-lg font-bold mb-2" style={{ color: '#EAECEF' }}>
          {t('noHistoricalData', language)}
        </div>
        <div
          className="text-sm text-center max-w-xs"
          style={{ color: '#848E9C' }}
        >
          {t('dataWillAppear', language)}
        </div>
      </div>
    )
  }

  // No client-side tail slice. The old code kept only the last 500 minute-buckets
  // AFTER merging four traders, which discarded most of every requested window and
  // made 1D/3D/7D/30D/All all render roughly the same recent span. Sample count is
  // now bounded server-side by max_points, with peaks and troughs preserved.
  const displayData = combinedData
  const spanMs = effectiveDomain[1] - effectiveDomain[0]

  // Y range is computed from the VISIBLE window only, so zooming into a flat
  // stretch actually magnifies it instead of leaving it pinned to the full-history
  // scale.
  const calculateYDomain = () => {
    const allValues: number[] = []
    displayData.forEach((point) => {
      if (point.ts < effectiveDomain[0] || point.ts > effectiveDomain[1]) return
      traders.forEach((trader) => {
        const value = point[`${trader.trader_id}_pnl_pct`]
        if (value !== undefined && !isNaN(value)) {
          allValues.push(value)
        }
      })
    })

    if (allValues.length === 0) return [-2, 2]

    const minVal = Math.min(...allValues)
    const maxVal = Math.max(...allValues)
    const range = maxVal - minVal

    // Use actual data range with 20% padding on each side
    // This ensures both lines are clearly visible
    const padding = Math.max(range * 0.2, 2) // At least 2% padding

    return [
      Math.floor((minVal - padding) * 10) / 10,
      Math.ceil((maxVal + padding) * 10) / 10,
    ]
  }

  // Custom Tooltip
  const CustomTooltip = ({ active, payload }: any) => {
    if (active && payload && payload.length) {
      const data = payload[0].payload
      const date = new Date(data.ts)
      const stamp = `${date.getMonth() + 1}/${date.getDate()} ${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`

      return (
        <div
          className="rounded-xl p-4 shadow-2xl backdrop-blur-sm"
          style={{
            background: 'rgba(30, 35, 41, 0.95)',
            border: '1px solid rgba(240, 185, 11, 0.2)',
            minWidth: '280px',
          }}
        >
          <div
            className="flex items-center gap-2 mb-3 pb-2"
            style={{ borderBottom: '1px solid #2B3139' }}
          >
            <Zap className="w-3.5 h-3.5" style={{ color: '#F0B90B' }} />
            <span className="text-xs font-medium" style={{ color: '#F0B90B' }}>
              {stamp}
            </span>
          </div>
          <div className="space-y-2.5">
            {traders.map((trader) => {
              const id = trader.trader_id
              const pnlPct = data[`${id}_pnl_pct`]
              const equity = data[`${id}_equity`]
              if (pnlPct === undefined) return null
              const isPositive = pnlPct >= 0

              const posCount = data[`${id}_pos_count`]
              const posCountRecon = data[`${id}_pos_count_recon`]
              const notional = data[`${id}_notional`]
              const longNotional = data[`${id}_long_notional`]
              const shortNotional = data[`${id}_short_notional`]
              const marginPct = data[`${id}_margin_pct`]
              const stale = data[`${id}_stale`]
              const ageMs = data[`${id}_age_ms`] || 0
              // Anything under a minute is the same snapshot cycle and not worth
              // flagging. Beyond that the age is shown, because the position figures
              // are the carried reading rather than one taken at this instant.
              const ageLabel =
                ageMs < 60_000
                  ? ''
                  : ageMs < 3_600_000
                    ? `${Math.round(ageMs / 60_000)}m`
                    : `${(ageMs / 3_600_000).toFixed(1)}h`

              // Both counts are shown when they disagree. The snapshot count came
              // from the exchange account at that moment; the reconstructed one is
              // replayed from local position rows. Collapsing them to one number
              // would silently hide bookkeeping drift.
              const countLabel =
                posCount === undefined
                  ? posCountRecon !== undefined
                    ? String(posCountRecon)
                    : '—'
                  : posCountRecon !== undefined && posCountRecon !== posCount
                    ? `${posCount} (${t('comparisonChart.reconShort', language)}${posCountRecon})`
                    : String(posCount)

              return (
                <div
                  key={id}
                  className="pb-2 last:pb-0"
                  style={{ borderBottom: '1px solid rgba(43, 49, 57, 0.6)' }}
                >
                  <div className="flex items-center justify-between gap-4">
                    <div className="flex items-center gap-2">
                      <div
                        className="w-2.5 h-2.5 rounded-full"
                        style={{ background: traderColor(id) }}
                      />
                      <span
                        className="text-xs font-medium truncate max-w-[110px]"
                        style={{ color: '#EAECEF' }}
                      >
                        {trader.trader_name}
                      </span>
                      {stale && ageLabel && (
                        <span
                          className="text-[9px] px-1 rounded mono"
                          style={{
                            color: '#848E9C',
                            background: 'rgba(132, 142, 156, 0.15)',
                          }}
                        >
                          -{ageLabel}
                        </span>
                      )}
                    </div>
                    <div className="text-right">
                      <div
                        className="text-sm font-bold mono flex items-center gap-1 justify-end"
                        style={{ color: isPositive ? '#0ECB81' : '#F6465D' }}
                      >
                        {isPositive ? (
                          <TrendingUp className="w-3 h-3" />
                        ) : (
                          <TrendingDown className="w-3 h-3" />
                        )}
                        {isPositive ? '+' : ''}
                        {pnlPct.toFixed(2)}%
                      </div>
                      <div
                        className="text-[10px] mono"
                        style={{ color: '#5E6673' }}
                      >
                        ${equity?.toFixed(2)}
                      </div>
                    </div>
                  </div>

                  <div className="flex items-center gap-3 mt-1 pl-4 flex-wrap">
                    <span
                      className="text-[10px] mono"
                      style={{ color: '#848E9C' }}
                    >
                      {t('comparisonChart.positions', language)}:{' '}
                      <span style={{ color: '#EAECEF' }}>{countLabel}</span>
                    </span>
                    <span
                      className="text-[10px] mono"
                      style={{ color: '#848E9C' }}
                    >
                      {t('comparisonChart.notional', language)}:{' '}
                      <span style={{ color: '#EAECEF' }}>
                        {notional === undefined
                          ? '—'
                          : `$${notional.toFixed(0)}`}
                      </span>
                    </span>
                    {marginPct !== undefined && (
                      <span
                        className="text-[10px] mono"
                        style={{ color: '#848E9C' }}
                      >
                        {t('comparisonChart.margin', language)}:{' '}
                        <span style={{ color: '#EAECEF' }}>
                          {marginPct.toFixed(0)}%
                        </span>
                      </span>
                    )}
                  </div>
                  {(longNotional !== undefined ||
                    shortNotional !== undefined) &&
                  (longNotional || shortNotional) ? (
                    <div className="flex items-center gap-3 mt-0.5 pl-4">
                      <span
                        className="text-[10px] mono"
                        style={{ color: '#0ECB81' }}
                      >
                        L ${(longNotional || 0).toFixed(0)}
                      </span>
                      <span
                        className="text-[10px] mono"
                        style={{ color: '#F6465D' }}
                      >
                        S ${(shortNotional || 0).toFixed(0)}
                      </span>
                    </div>
                  ) : null}
                </div>
              )
            })}
          </div>
          <div
            className="text-[9px] mt-2 pt-2"
            style={{ color: '#5E6673', borderTop: '1px solid #2B3139' }}
          >
            {t('comparisonChart.notionalNote', language)}
          </div>
        </div>
      )
    }
    return null
  }

  // Calculate stats - find each trader's last available data point
  const traderStats = traders
    .map((trader) => {
      // Find the last data point that has data for this trader
      let currentPnl = 0
      let currentEquity = 0
      for (let i = displayData.length - 1; i >= 0; i--) {
        const pnl = displayData[i]?.[`${trader.trader_id}_pnl_pct`]
        if (pnl !== undefined) {
          currentPnl = pnl
          currentEquity = displayData[i]?.[`${trader.trader_id}_equity`] || 0
          break
        }
      }
      return { ...trader, currentPnl, currentEquity }
    })
    .sort((a, b) => b.currentPnl - a.currentPnl)

  const leader = traderStats[0]
  const gap =
    traderStats.length > 1
      ? Math.abs(traderStats[0].currentPnl - traderStats[1].currentPnl).toFixed(
          2
        )
      : '0.00'

  const visibleCount = displayData.filter(
    (p) => p.ts >= effectiveDomain[0] && p.ts <= effectiveDomain[1]
  ).length

  const fmtStamp = (ms: number) => {
    const d = new Date(ms)
    return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }
  const rangeLabel = `${fmtStamp(effectiveDomain[0])} → ${fmtStamp(effectiveDomain[1])}`

  const isDownsampled = Object.values(batch?.sampling || {}).some(
    (s: any) => s?.downsampled
  )

  return (
    <div className="space-y-4">
      {/* Time Period Selector + Mini Stats Bar */}
      <div className="flex items-center justify-between flex-wrap gap-3">
        {/* Time Period Buttons */}
        <div className="flex items-center gap-1">
          {TIME_PERIODS.map((period) => (
            <button
              key={period.key}
              onClick={() => setSelectedPeriod(period.key)}
              className="px-3 py-1.5 text-xs font-medium rounded-lg transition-all"
              style={{
                background:
                  selectedPeriod === period.key
                    ? 'rgba(240, 185, 11, 0.2)'
                    : 'rgba(43, 49, 57, 0.5)',
                color: selectedPeriod === period.key ? '#F0B90B' : '#848E9C',
                border: `1px solid ${selectedPeriod === period.key ? 'rgba(240, 185, 11, 0.4)' : '#2B3139'}`,
              }}
            >
              {t(`comparisonChart.${period.key}`, language)}
            </button>
          ))}
        </div>

        {/* Mini Stats Bar */}
        <div className="flex items-center gap-2 flex-wrap">
          {traderStats.slice(0, 3).map((trader, idx) => (
            <div
              key={trader.trader_id}
              className="flex items-center gap-2 px-3 py-1.5 rounded-full transition-all hover:scale-105"
              style={{
                background:
                  idx === 0
                    ? 'rgba(240, 185, 11, 0.15)'
                    : 'rgba(43, 49, 57, 0.5)',
                border: `1px solid ${idx === 0 ? 'rgba(240, 185, 11, 0.3)' : '#2B3139'}`,
              }}
            >
              <div
                className="w-2 h-2 rounded-full"
                style={{ background: traderColor(trader.trader_id) }}
              />
              <span
                className="text-xs font-medium truncate max-w-[80px]"
                style={{ color: '#EAECEF' }}
              >
                {trader.trader_name}
              </span>
              <span
                className="text-xs font-bold mono"
                style={{
                  color: trader.currentPnl >= 0 ? '#0ECB81' : '#F6465D',
                }}
              >
                {trader.currentPnl >= 0 ? '+' : ''}
                {trader.currentPnl.toFixed(2)}%
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* Zoom / pan controls */}
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div
          className="flex items-center gap-2 text-[10px]"
          style={{ color: '#5E6673' }}
        >
          <Layers className="w-3 h-3" />
          <span>{t('comparisonChart.zoomHint', language)}</span>
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() =>
              setViewDomain(zoomDomain(effectiveDomain, dataBounds, 0.6, 0.5))
            }
            className="px-2 py-1 text-xs font-medium rounded-md"
            style={{
              background: 'rgba(43, 49, 57, 0.5)',
              color: '#848E9C',
              border: '1px solid #2B3139',
            }}
            aria-label={t('comparisonChart.zoomIn', language)}
          >
            +
          </button>
          <button
            onClick={() =>
              setViewDomain(zoomDomain(effectiveDomain, dataBounds, 1.6, 0.5))
            }
            className="px-2 py-1 text-xs font-medium rounded-md"
            style={{
              background: 'rgba(43, 49, 57, 0.5)',
              color: '#848E9C',
              border: '1px solid #2B3139',
            }}
            aria-label={t('comparisonChart.zoomOut', language)}
          >
            −
          </button>
          <button
            onClick={() =>
              setViewDomain(panDomain(effectiveDomain, dataBounds, -0.25))
            }
            className="px-2 py-1 text-xs font-medium rounded-md"
            style={{
              background: 'rgba(43, 49, 57, 0.5)',
              color: '#848E9C',
              border: '1px solid #2B3139',
            }}
            aria-label={t('comparisonChart.panLeft', language)}
          >
            ‹
          </button>
          <button
            onClick={() =>
              setViewDomain(panDomain(effectiveDomain, dataBounds, 0.25))
            }
            className="px-2 py-1 text-xs font-medium rounded-md"
            style={{
              background: 'rgba(43, 49, 57, 0.5)',
              color: '#848E9C',
              border: '1px solid #2B3139',
            }}
            aria-label={t('comparisonChart.panRight', language)}
          >
            ›
          </button>
          <button
            onClick={() => setViewDomain(null)}
            disabled={!isZoomed}
            className="flex items-center gap-1 px-2 py-1 text-xs font-medium rounded-md transition-all"
            style={{
              background: isZoomed
                ? 'rgba(240, 185, 11, 0.15)'
                : 'rgba(43, 49, 57, 0.3)',
              color: isZoomed ? '#F0B90B' : '#474D57',
              border: `1px solid ${isZoomed ? 'rgba(240, 185, 11, 0.3)' : '#2B3139'}`,
              cursor: isZoomed ? 'pointer' : 'default',
            }}
          >
            <RotateCcw className="w-3 h-3" />
            {t('comparisonChart.reset', language)}
          </button>
        </div>
      </div>

      {/* Chart */}
      <div
        ref={plotRef}
        onMouseDown={onMouseDown}
        onDoubleClick={() => setViewDomain(null)}
        className="relative rounded-xl overflow-hidden select-none"
        style={{
          background:
            'linear-gradient(180deg, rgba(11, 14, 17, 0.8) 0%, rgba(11, 14, 17, 1) 100%)',
          cursor: isDragging ? 'grabbing' : 'grab',
          touchAction: 'none',
        }}
      >
        {/* Watermark */}
        <div
          style={{
            position: 'absolute',
            top: '50%',
            left: '50%',
            transform: 'translate(-50%, -50%)',
            fontSize: '80px',
            fontWeight: 'bold',
            color: 'rgba(240, 185, 11, 0.03)',
            zIndex: 1,
            pointerEvents: 'none',
            fontFamily: 'monospace',
            letterSpacing: '0.1em',
          }}
        >
          NOFX
        </div>

        <ResponsiveContainer width="100%" height={420}>
          <ComposedChart
            data={displayData}
            margin={{ top: 20, right: 20, left: 10, bottom: 20 }}
          >
            <defs>
              {traders.map((trader) => (
                <linearGradient
                  key={`area-gradient-${trader.trader_id}`}
                  id={`area-gradient-${trader.trader_id}`}
                  x1="0"
                  y1="0"
                  x2="0"
                  y2="1"
                >
                  <stop
                    offset="0%"
                    stopColor={traderColor(trader.trader_id)}
                    stopOpacity={0.3}
                  />
                  <stop
                    offset="100%"
                    stopColor={traderColor(trader.trader_id)}
                    stopOpacity={0}
                  />
                </linearGradient>
              ))}
              {/* Glow filter */}
              <filter id="glow" x="-50%" y="-50%" width="200%" height="200%">
                <feGaussianBlur stdDeviation="2" result="coloredBlur" />
                <feMerge>
                  <feMergeNode in="coloredBlur" />
                  <feMergeNode in="SourceGraphic" />
                </feMerge>
              </filter>
            </defs>

            <CartesianGrid
              strokeDasharray="3 3"
              stroke="#1E2329"
              vertical={false}
            />

            {/*
              A numeric time axis, not a category axis of label strings. Real
              timestamps mean sample spacing reflects elapsed time (so data gaps
              show as gaps), the domain can be zoomed and panned, and tick labels
              are derived from the VISIBLE span rather than from the range button.
              allowDataOverflow is required for the domain to actually clip.
            */}
            <XAxis
              dataKey="ts"
              type="number"
              scale="time"
              domain={effectiveDomain}
              allowDataOverflow
              ticks={buildTimeTicks(effectiveDomain[0], effectiveDomain[1])}
              tickFormatter={(value) => formatAxisTick(value, spanMs)}
              stroke="#2B3139"
              tick={{ fill: '#5E6673', fontSize: 10 }}
              tickLine={false}
              axisLine={{ stroke: '#2B3139' }}
              minTickGap={12}
            />

            <YAxis
              stroke="#2B3139"
              tick={{ fill: '#5E6673', fontSize: 10 }}
              tickLine={false}
              axisLine={false}
              domain={calculateYDomain()}
              allowDataOverflow
              tickFormatter={(value) => `${value.toFixed(1)}%`}
              width={50}
            />

            <Tooltip content={<CustomTooltip />} />

            {/* Zero reference line */}
            <ReferenceLine
              y={0}
              stroke="#474D57"
              strokeDasharray="8 4"
              strokeWidth={1}
            />

            {/* Area fills for top 2 traders */}
            {traders.slice(0, 2).map((trader) => (
              <Area
                key={`area-${trader.trader_id}`}
                type="monotone"
                dataKey={`${trader.trader_id}_pnl_pct`}
                fill={`url(#area-gradient-${trader.trader_id})`}
                stroke="none"
                connectNulls
              />
            ))}

            {/* Lines for all traders */}
            {traders.map((trader, idx) => (
              <Line
                key={trader.trader_id}
                type="monotone"
                dataKey={`${trader.trader_id}_pnl_pct`}
                stroke={traderColor(trader.trader_id)}
                strokeWidth={idx === 0 ? 3 : 2}
                dot={false}
                activeDot={{
                  r: 6,
                  fill: traderColor(trader.trader_id),
                  stroke: '#0B0E11',
                  strokeWidth: 2,
                  filter: 'url(#glow)',
                }}
                name={trader.trader_name}
                connectNulls
                style={{ filter: idx === 0 ? 'url(#glow)' : undefined }}
              />
            ))}

            <Legend
              wrapperStyle={{ paddingTop: '16px' }}
              content={({ payload }) => {
                // Filter out Area entries (they use raw dataKey containing _pnl_pct)
                const filteredPayload =
                  payload?.filter(
                    (entry: any) =>
                      entry.value && !entry.value.includes('_pnl_pct')
                  ) || []

                return (
                  <div
                    style={{
                      display: 'flex',
                      justifyContent: 'center',
                      gap: '20px',
                      flexWrap: 'wrap',
                    }}
                  >
                    {filteredPayload.map((entry: any, index: number) => {
                      const trader = traders.find(
                        (t) => t.trader_name === entry.value
                      )
                      // Find this trader's last available PnL from traderStats
                      const traderStat = traderStats.find(
                        (t) => t.trader_id === trader?.trader_id
                      )
                      const pnl = traderStat?.currentPnl || 0
                      return (
                        <div
                          key={`legend-${index}`}
                          style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '6px',
                          }}
                        >
                          <div
                            style={{
                              width: '8px',
                              height: '8px',
                              borderRadius: '50%',
                              backgroundColor: entry.color,
                            }}
                          />
                          <span
                            style={{
                              color: '#EAECEF',
                              fontSize: '12px',
                              fontWeight: 500,
                            }}
                          >
                            {entry.value}
                            <span
                              style={{
                                color: pnl >= 0 ? '#0ECB81' : '#F6465D',
                                marginLeft: '6px',
                                fontFamily: 'monospace',
                              }}
                            >
                              ({pnl >= 0 ? '+' : ''}
                              {pnl.toFixed(2)}%)
                            </span>
                          </span>
                        </div>
                      )
                    })}
                  </div>
                )
              }}
            />
          </ComposedChart>
        </ResponsiveContainer>
      </div>

      {/* Bottom Stats */}
      <div className="grid grid-cols-4 gap-2">
        <div
          className="p-3 rounded-lg text-center"
          style={{
            background: 'rgba(240, 185, 11, 0.05)',
            border: '1px solid rgba(240, 185, 11, 0.1)',
          }}
        >
          <div
            className="text-[10px] uppercase tracking-wider mb-1"
            style={{ color: '#848E9C' }}
          >
            {t('leader', language)}
          </div>
          <div
            className="text-sm font-bold truncate"
            style={{ color: '#F0B90B' }}
          >
            {leader?.trader_name || '-'}
          </div>
        </div>
        <div
          className="p-3 rounded-lg text-center"
          style={{ background: 'rgba(14, 203, 129, 0.05)' }}
        >
          <div
            className="text-[10px] uppercase tracking-wider mb-1"
            style={{ color: '#848E9C' }}
          >
            {t('leadPnL', language) || 'Lead PnL'}
          </div>
          <div
            className="text-sm font-bold mono"
            style={{
              color: (leader?.currentPnl || 0) >= 0 ? '#0ECB81' : '#F6465D',
            }}
          >
            {(leader?.currentPnl || 0) >= 0 ? '+' : ''}
            {(leader?.currentPnl || 0).toFixed(2)}%
          </div>
        </div>
        <div
          className="p-3 rounded-lg text-center"
          style={{ background: 'rgba(96, 165, 250, 0.05)' }}
        >
          <div
            className="text-[10px] uppercase tracking-wider mb-1"
            style={{ color: '#848E9C' }}
          >
            {t('currentGap', language)}
          </div>
          <div className="text-sm font-bold mono" style={{ color: '#60a5fa' }}>
            {gap}%
          </div>
        </div>
        <div
          className="p-3 rounded-lg text-center"
          style={{ background: 'rgba(139, 92, 246, 0.05)' }}
        >
          <div
            className="text-[10px] uppercase tracking-wider mb-1"
            style={{ color: '#848E9C' }}
          >
            {t('dataPoints', language)}
          </div>
          <div className="text-sm font-bold mono" style={{ color: '#8b5cf6' }}>
            {visibleCount}
            <span className="text-[10px]" style={{ color: '#5E6673' }}>
              /{displayData.length}
            </span>
          </div>
          {/* Stating the drawn span removes the ambiguity that started this: with a
              label-only axis there was no way to tell what window was on screen. */}
          <div className="text-[9px] mono mt-0.5" style={{ color: '#5E6673' }}>
            {rangeLabel}
          </div>
          {isDownsampled && (
            <div className="text-[9px] mono" style={{ color: '#5E6673' }}>
              {t('comparisonChart.downsampled', language)}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
