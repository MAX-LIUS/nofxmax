import { useState } from 'react'
import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  ReferenceLine,
} from 'recharts'
import useSWR, { mutate } from 'swr'
import { api } from '../../lib/api'
import { useLanguage } from '../../contexts/LanguageContext'
import { useAuth } from '../../contexts/AuthContext'
import { t } from '../../i18n/translations'
import { confirmToast, notify } from '../../lib/notify'
import {
  AlertTriangle,
  BarChart3,
  DollarSign,
  Percent,
  TrendingUp as ArrowUp,
  TrendingDown as ArrowDown,
  RotateCcw,
  Trash2,
  Loader2,
} from 'lucide-react'

interface EquityPoint {
  timestamp: string
  total_equity: number
  pnl: number
  pnl_pct: number
  cycle_number: number
}

interface EquityChartProps {
  traderId?: string
  embedded?: boolean // 嵌入模式（不显示外层卡片）
}

type TimeRange = 1 | 7 | 30 | 90 | 180 | 0 // 0 means all data

export function EquityChart({ traderId, embedded = false }: EquityChartProps) {
  const { language } = useLanguage()
  const { user, token } = useAuth()
  const [displayMode, setDisplayMode] = useState<'dollar' | 'percent'>('dollar')
  const [timeRange, setTimeRange] = useState<TimeRange>(7) // Default to 7 days
  const [resettingCurve, setResettingCurve] = useState(false)
  const [resettingPnL, setResettingPnL] = useState(false)

  const {
    data: history,
    error,
    isLoading,
  } = useSWR<EquityPoint[]>(
    user && token && traderId
      ? `equity-history-${traderId}-${timeRange}`
      : null,
    () => api.getEquityHistory(traderId, timeRange || undefined),
    {
      refreshInterval: 30000, // 30秒刷新（历史数据更新频率较低）
      revalidateOnFocus: false,
      dedupingInterval: 20000,
    }
  )

  const { data: account } = useSWR(
    user && token && traderId ? `account-${traderId}` : null,
    () => api.getAccount(traderId),
    {
      refreshInterval: 15000, // 15秒刷新（配合后端缓存）
      revalidateOnFocus: false,
      dedupingInterval: 10000,
    }
  )

  // Handle reset equity curve
  const handleResetEquityCurve = async () => {
    if (!traderId) return

    const confirmed = await confirmToast(
      language === 'zh'
        ? '确定要重置净值曲线吗？这将删除所有历史净值数据，无法恢复！'
        : 'Are you sure you want to reset the equity curve? This will delete all historical equity data and cannot be undone!',
      {
        title: language === 'zh' ? '重置净值曲线' : 'Reset Equity Curve',
        okText: language === 'zh' ? '确认重置' : 'Confirm Reset',
        cancelText: language === 'zh' ? '取消' : 'Cancel',
      }
    )

    if (!confirmed) return

    setResettingCurve(true)
    try {
      await api.resetEquityCurve(traderId)
      notify.success(
        language === 'zh'
          ? '净值曲线已重置，新的数据将在下个周期开始记录'
          : 'Equity curve reset successfully. New data will be recorded from next cycle.'
      )
      // Refresh equity history
      await mutate(`equity-history-${traderId}-${timeRange}`)
    } catch (err) {
      notify.error(
        err instanceof Error
          ? err.message
          : language === 'zh'
            ? '重置失败'
            : 'Reset failed'
      )
    } finally {
      setResettingCurve(false)
    }
  }

  // Handle reset total PnL
  const handleResetTotalPnL = async () => {
    if (!traderId) return

    const confirmed = await confirmToast(
      language === 'zh'
        ? '确定要重置总盈亏吗？这将把当前净值设为新的初始余额，总盈亏归零。'
        : 'Are you sure you want to reset total PnL? This will set current equity as new initial balance, resetting total PnL to zero.',
      {
        title: language === 'zh' ? '重置总盈亏' : 'Reset Total PnL',
        okText: language === 'zh' ? '确认重置' : 'Confirm Reset',
        cancelText: language === 'zh' ? '取消' : 'Cancel',
      }
    )

    if (!confirmed) return

    setResettingPnL(true)
    try {
      await api.resetTotalPnL(traderId)
      notify.success(
        language === 'zh'
          ? '总盈亏已重置，初始余额已更新为当前净值'
          : 'Total PnL reset successfully. Initial balance updated to current equity.'
      )
      // Refresh both account and equity history
      await Promise.all([
        mutate(`account-${traderId}`),
        mutate(`equity-history-${traderId}-${timeRange}`),
      ])
    } catch (err) {
      notify.error(
        err instanceof Error
          ? err.message
          : language === 'zh'
            ? '重置失败'
            : 'Reset failed'
      )
    } finally {
      setResettingPnL(false)
    }
  }

  // Loading state - show skeleton
  if (isLoading) {
    return (
      <div className={embedded ? 'p-6' : 'binance-card p-6'}>
        {!embedded && (
          <h3
            className="text-lg font-semibold mb-6"
            style={{ color: '#EAECEF' }}
          >
            {t('accountEquityCurve', language)}
          </h3>
        )}
        <div className="animate-pulse">
          <div className="skeleton h-64 w-full rounded"></div>
        </div>
      </div>
    )
  }

  if (error) {
    return (
      <div className={embedded ? 'p-6' : 'binance-card p-6'}>
        <div
          className="flex items-center gap-3 p-4 rounded"
          style={{
            background: 'rgba(246, 70, 93, 0.1)',
            border: '1px solid rgba(246, 70, 93, 0.2)',
          }}
        >
          <AlertTriangle className="w-6 h-6" style={{ color: '#F6465D' }} />
          <div>
            <div className="font-semibold" style={{ color: '#F6465D' }}>
              {t('loadingError', language)}
            </div>
            <div className="text-sm" style={{ color: '#848E9C' }}>
              {error.message}
            </div>
          </div>
        </div>
      </div>
    )
  }

  // 过滤掉无效数据：total_equity为0或小于1的数据点（API失败导致）
  const validHistory = history?.filter((point) => point.total_equity > 1) || []

  if (!validHistory || validHistory.length === 0) {
    return (
      <div className={embedded ? 'p-6' : 'binance-card p-6'}>
        {!embedded && (
          <h3
            className="text-lg font-semibold mb-6"
            style={{ color: '#EAECEF' }}
          >
            {t('accountEquityCurve', language)}
          </h3>
        )}
        <div className="text-center py-16" style={{ color: '#848E9C' }}>
          <div className="mb-4 flex justify-center opacity-50">
            <BarChart3 className="w-16 h-16" />
          </div>
          <div className="text-lg font-semibold mb-2">
            {t('noHistoricalData', language)}
          </div>
          <div className="text-sm">{t('dataWillAppear', language)}</div>
        </div>
      </div>
    )
  }

  // 限制显示最近的数据点（性能优化）
  // 如果数据超过2000个点，只显示最近2000个
  const MAX_DISPLAY_POINTS = 2000
  const displayHistory =
    validHistory.length > MAX_DISPLAY_POINTS
      ? validHistory.slice(-MAX_DISPLAY_POINTS)
      : validHistory

  // 计算初始余额（优先从 account 获取配置的初始余额，备选从历史数据反推）
  const initialBalance =
    account?.initial_balance || // 从交易员配置读取真实初始余额
    (validHistory[0]
      ? validHistory[0].total_equity - validHistory[0].pnl
      : undefined) || // 备选：淨值 - 盈亏
    1000 // 默认值（与创建交易员时的默认配置一致）

  // 转换数据格式
  const chartData = displayHistory.map((point, index) => {
    const pnl = point.total_equity - initialBalance
    const pnlPct = ((pnl / initialBalance) * 100).toFixed(2)
    return {
      time: new Date(point.timestamp).toLocaleTimeString('zh-CN', {
        hour: '2-digit',
        minute: '2-digit',
      }),
      value: displayMode === 'dollar' ? point.total_equity : parseFloat(pnlPct),
      cycle: point.cycle_number ?? index + 1,
      raw_equity: point.total_equity,
      raw_pnl: pnl,
      raw_pnl_pct: parseFloat(pnlPct),
    }
  })

  const currentValue = chartData[chartData.length - 1]
  const isProfit = currentValue.raw_pnl >= 0

  // 计算Y轴范围
  const calculateYDomain = () => {
    if (displayMode === 'percent') {
      // 百分比模式：找到最大最小值，留20%余量
      const values = chartData.map((d) => d.value)
      const minVal = Math.min(...values)
      const maxVal = Math.max(...values)
      const range = Math.max(Math.abs(maxVal), Math.abs(minVal))
      const padding = Math.max(range * 0.2, 1) // 至少留1%余量
      return [Math.floor(minVal - padding), Math.ceil(maxVal + padding)]
    } else {
      // 美元模式：以初始余额为基准，上下留10%余量
      const values = chartData.map((d) => d.value)
      const minVal = Math.min(...values, initialBalance)
      const maxVal = Math.max(...values, initialBalance)
      const range = maxVal - minVal
      const padding = Math.max(range * 0.15, initialBalance * 0.01) // 至少留1%余量
      return [Math.floor(minVal - padding), Math.ceil(maxVal + padding)]
    }
  }

  // 自定义Tooltip - Binance Style
  const CustomTooltip = ({ active, payload }: any) => {
    if (active && payload && payload.length) {
      const data = payload[0].payload
      return (
        <div
          className="rounded p-3 shadow-xl"
          style={{ background: '#1E2329', border: '1px solid #2B3139' }}
        >
          <div className="text-xs mb-1" style={{ color: '#848E9C' }}>
            Cycle #{data.cycle != null ? data.cycle : '—'}
          </div>
          <div className="font-bold mono" style={{ color: '#EAECEF' }}>
            {data.raw_equity.toFixed(2)} USDT
          </div>
          <div
            className="text-sm mono font-bold"
            style={{ color: data.raw_pnl >= 0 ? '#0ECB81' : '#F6465D' }}
          >
            {data.raw_pnl >= 0 ? '+' : ''}
            {data.raw_pnl.toFixed(2)} USDT ({data.raw_pnl_pct >= 0 ? '+' : ''}
            {data.raw_pnl_pct}%)
          </div>
        </div>
      )
    }
    return null
  }

  return (
    <div
      className={
        embedded ? 'p-3 sm:p-5' : 'binance-card p-3 sm:p-5 animate-fade-in'
      }
    >
      {/* Header */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between mb-4">
        <div className="flex-1">
          {!embedded && (
            <h3
              className="text-base sm:text-lg font-bold mb-2"
              style={{ color: '#EAECEF' }}
            >
              {t('accountEquityCurve', language)}
            </h3>
          )}
          <div className="flex flex-col sm:flex-row sm:items-baseline gap-2 sm:gap-4">
            <span
              className="text-2xl sm:text-3xl font-bold mono"
              style={{ color: '#EAECEF' }}
            >
              {account?.total_equity.toFixed(2) || '0.00'}
              <span
                className="text-base sm:text-lg ml-1"
                style={{ color: '#848E9C' }}
              >
                USDT
              </span>
            </span>
            <div className="flex items-center gap-2 flex-wrap">
              <span
                className="text-sm sm:text-lg font-bold mono px-2 sm:px-3 py-1 rounded flex items-center gap-1"
                style={{
                  color: isProfit ? '#0ECB81' : '#F6465D',
                  background: isProfit
                    ? 'rgba(14, 203, 129, 0.1)'
                    : 'rgba(246, 70, 93, 0.1)',
                  border: `1px solid ${
                    isProfit
                      ? 'rgba(14, 203, 129, 0.2)'
                      : 'rgba(246, 70, 93, 0.2)'
                  }`,
                }}
              >
                {isProfit ? (
                  <ArrowUp className="w-4 h-4" />
                ) : (
                  <ArrowDown className="w-4 h-4" />
                )}
                {isProfit ? '+' : ''}
                {currentValue.raw_pnl_pct}%
              </span>
              <span
                className="text-xs sm:text-sm mono"
                style={{ color: '#848E9C' }}
              >
                ({isProfit ? '+' : ''}
                {currentValue.raw_pnl.toFixed(2)} USDT)
              </span>
            </div>
          </div>
        </div>

        {/* Controls: Time Range + Display Mode + Reset Buttons */}
        <div className="flex flex-col gap-2 sm:gap-3">
          {/* Row 1: Time Range Selector + Display Mode */}
          <div className="flex flex-wrap gap-2 items-center">
            {/* Time Range */}
            <div
              className="flex gap-0.5 rounded p-0.5"
              style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
            >
              {([1, 7, 30, 90, 180, 0] as TimeRange[]).map((days) => (
                <button
                  key={days}
                  onClick={() => setTimeRange(days)}
                  className="px-2 sm:px-3 py-1 rounded text-xs font-bold transition-all whitespace-nowrap"
                  style={
                    timeRange === days
                      ? {
                          background: '#F0B90B',
                          color: '#000',
                          boxShadow: '0 2px 8px rgba(240, 185, 11, 0.4)',
                        }
                      : { background: 'transparent', color: '#848E9C' }
                  }
                  title={
                    days === 0
                      ? language === 'zh'
                        ? '全部数据'
                        : 'All Data'
                      : `${language === 'zh' ? '最近' : 'Last'} ${days} ${language === 'zh' ? '天' : 'days'}`
                  }
                >
                  {days === 0
                    ? language === 'zh'
                      ? '全部'
                      : 'All'
                    : `${days}${language === 'zh' ? '天' : 'd'}`}
                </button>
              ))}
            </div>

            {/* Display Mode */}
            <div
              className="flex gap-0.5 rounded p-0.5"
              style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
            >
              <button
                onClick={() => setDisplayMode('dollar')}
                className="px-2 sm:px-3 py-1 rounded text-xs font-bold transition-all flex items-center gap-1 whitespace-nowrap"
                style={
                  displayMode === 'dollar'
                    ? {
                        background: '#F0B90B',
                        color: '#000',
                        boxShadow: '0 2px 8px rgba(240, 185, 11, 0.4)',
                      }
                    : { background: 'transparent', color: '#848E9C' }
                }
              >
                <DollarSign className="w-3 h-3 sm:w-4 sm:h-4" />
                <span className="hidden xs:inline">USDT</span>
              </button>
              <button
                onClick={() => setDisplayMode('percent')}
                className="px-2 sm:px-3 py-1 rounded text-xs font-bold transition-all flex items-center gap-1 whitespace-nowrap"
                style={
                  displayMode === 'percent'
                    ? {
                        background: '#F0B90B',
                        color: '#000',
                        boxShadow: '0 2px 8px rgba(240, 185, 11, 0.4)',
                      }
                    : { background: 'transparent', color: '#848E9C' }
                }
              >
                <Percent className="w-3 h-3 sm:w-4 sm:h-4" />
              </button>
            </div>
          </div>

          {/* Row 2: Reset Buttons */}
          <div className="flex gap-2 flex-wrap">
            <button
              onClick={handleResetTotalPnL}
              disabled={resettingPnL}
              className="px-2 sm:px-3 py-1.5 rounded text-xs font-bold transition-all flex items-center gap-1 hover:opacity-80 disabled:opacity-50 whitespace-nowrap"
              style={{
                background: 'rgba(14, 203, 129, 0.1)',
                color: '#0ECB81',
                border: '1px solid rgba(14, 203, 129, 0.3)',
              }}
              title={
                language === 'zh'
                  ? '将当前净值设为新的初始余额，总盈亏归零'
                  : 'Set current equity as new initial balance, reset total PnL to zero'
              }
            >
              {resettingPnL ? (
                <Loader2 className="w-3 h-3 sm:w-4 sm:h-4 animate-spin" />
              ) : (
                <RotateCcw className="w-3 h-3 sm:w-4 sm:h-4" />
              )}
              <span className="hidden sm:inline">
                {language === 'zh' ? '重置总盈亏' : 'Reset Total PnL'}
              </span>
              <span className="sm:hidden">
                {language === 'zh' ? '重置盈亏' : 'Reset PnL'}
              </span>
            </button>

            <button
              onClick={handleResetEquityCurve}
              disabled={resettingCurve}
              className="px-2 sm:px-3 py-1.5 rounded text-xs font-bold transition-all flex items-center gap-1 hover:opacity-80 disabled:opacity-50 whitespace-nowrap"
              style={{
                background: 'rgba(246, 70, 93, 0.1)',
                color: '#F6465D',
                border: '1px solid rgba(246, 70, 93, 0.3)',
              }}
              title={
                language === 'zh'
                  ? '删除所有历史净值数据，无法恢复！'
                  : 'Delete all historical equity data, cannot be undone!'
              }
            >
              {resettingCurve ? (
                <Loader2 className="w-3 h-3 sm:w-4 sm:h-4 animate-spin" />
              ) : (
                <Trash2 className="w-3 h-3 sm:w-4 sm:h-4" />
              )}
              <span className="hidden sm:inline">
                {language === 'zh' ? '清空历史曲线' : 'Clear History Curve'}
              </span>
              <span className="sm:hidden">
                {language === 'zh' ? '清空曲线' : 'Clear Curve'}
              </span>
            </button>
          </div>
        </div>
      </div>

      {/* Chart */}
      <div
        className="my-2"
        style={{
          borderRadius: '8px',
          overflow: 'hidden',
          position: 'relative',
        }}
      >
        {/* NOFX Watermark */}
        <div
          style={{
            position: 'absolute',
            top: '15px',
            right: '15px',
            fontSize: '20px',
            fontWeight: 'bold',
            color: 'rgba(240, 185, 11, 0.15)',
            zIndex: 10,
            pointerEvents: 'none',
            fontFamily: 'monospace',
          }}
        >
          NOFX
        </div>
        <ResponsiveContainer width="100%" height={280}>
          <LineChart
            data={chartData}
            margin={{ top: 10, right: 20, left: 5, bottom: 30 }}
          >
            <defs>
              <linearGradient id="colorGradient" x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor="#F0B90B" stopOpacity={0.8} />
                <stop offset="95%" stopColor="#FCD535" stopOpacity={0.2} />
              </linearGradient>
            </defs>
            <CartesianGrid strokeDasharray="3 3" stroke="#2B3139" />
            <XAxis
              dataKey="time"
              stroke="#5E6673"
              tick={{ fill: '#848E9C', fontSize: 11 }}
              tickLine={{ stroke: '#2B3139' }}
              interval={Math.floor(chartData.length / 10)}
              angle={-15}
              textAnchor="end"
              height={60}
            />
            <YAxis
              stroke="#5E6673"
              tick={{ fill: '#848E9C', fontSize: 12 }}
              tickLine={{ stroke: '#2B3139' }}
              domain={calculateYDomain()}
              tickFormatter={(value) =>
                displayMode === 'dollar' ? `$${value.toFixed(0)}` : `${value}%`
              }
            />
            <Tooltip content={<CustomTooltip />} />
            <ReferenceLine
              y={displayMode === 'dollar' ? initialBalance : 0}
              stroke="#474D57"
              strokeDasharray="3 3"
              label={{
                value:
                  displayMode === 'dollar'
                    ? t('initialBalance', language).split(' ')[0]
                    : '0%',
                fill: '#848E9C',
                fontSize: 12,
              }}
            />
            <Line
              type="natural"
              dataKey="value"
              stroke="url(#colorGradient)"
              strokeWidth={3}
              dot={chartData.length > 50 ? false : { fill: '#F0B90B', r: 3 }}
              activeDot={{
                r: 6,
                fill: '#FCD535',
                stroke: '#F0B90B',
                strokeWidth: 2,
              }}
              connectNulls={true}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>

      {/* Footer Stats */}
      <div
        className="mt-3 grid grid-cols-2 sm:grid-cols-4 gap-2 sm:gap-3 pt-3"
        style={{ borderTop: '1px solid #2B3139' }}
      >
        <div
          className="p-2 rounded transition-all hover:bg-opacity-50"
          style={{ background: 'rgba(240, 185, 11, 0.05)' }}
        >
          <div
            className="text-xs mb-1 uppercase tracking-wider"
            style={{ color: '#848E9C' }}
          >
            {t('initialBalance', language)}
          </div>
          <div
            className="text-xs sm:text-sm font-bold mono"
            style={{ color: '#EAECEF' }}
          >
            {initialBalance.toFixed(2)} USDT
          </div>
        </div>
        <div
          className="p-2 rounded transition-all hover:bg-opacity-50"
          style={{ background: 'rgba(240, 185, 11, 0.05)' }}
        >
          <div
            className="text-xs mb-1 uppercase tracking-wider"
            style={{ color: '#848E9C' }}
          >
            {t('currentEquity', language)}
          </div>
          <div
            className="text-xs sm:text-sm font-bold mono"
            style={{ color: '#EAECEF' }}
          >
            {currentValue.raw_equity.toFixed(2)} USDT
          </div>
        </div>
        <div
          className="p-2 rounded transition-all hover:bg-opacity-50"
          style={{ background: 'rgba(240, 185, 11, 0.05)' }}
        >
          <div
            className="text-xs mb-1 uppercase tracking-wider"
            style={{ color: '#848E9C' }}
          >
            {t('historicalCycles', language)}
          </div>
          <div
            className="text-xs sm:text-sm font-bold mono"
            style={{ color: '#EAECEF' }}
          >
            {validHistory.length} {t('cycles', language)}
          </div>
        </div>
        <div
          className="p-2 rounded transition-all hover:bg-opacity-50"
          style={{ background: 'rgba(240, 185, 11, 0.05)' }}
        >
          <div
            className="text-xs mb-1 uppercase tracking-wider"
            style={{ color: '#848E9C' }}
          >
            {t('displayRange', language)}
          </div>
          <div
            className="text-xs sm:text-sm font-bold mono"
            style={{ color: '#EAECEF' }}
          >
            {validHistory.length > MAX_DISPLAY_POINTS
              ? `${t('recent', language)} ${MAX_DISPLAY_POINTS}`
              : t('allData', language)}
          </div>
        </div>
      </div>
    </div>
  )
}
