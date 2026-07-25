import { lazy, Suspense, useEffect, useState, useRef, useMemo } from 'react'
import { mutate } from 'swr'
import { api } from '../lib/api'
const ChartTabs = lazy(() =>
  import('../components/charts/ChartTabs').then((m) => ({
    default: m.ChartTabs,
  }))
)
import { DecisionCard } from '../components/trader/DecisionCard'
import { PositionProtectionPanel } from '../components/trader/PositionProtectionPanel'
import { ExpectancyPanel } from '../components/trader/ExpectancyPanel'
import { CloseAttributionPanel } from '../components/trader/CloseAttributionPanel'
import { FlipObservationsPanel } from '../components/trader/FlipObservationsPanel'
import { BreakerHistoryPanel } from '../components/trader/BreakerHistoryPanel'
import { EvolutionProfilePanel } from '../components/trader/EvolutionProfilePanel'
import { InsightPanel } from '../components/trader/InsightPanel'
const PositionHistory = lazy(() =>
  import('../components/trader/PositionHistory').then((m) => ({
    default: m.PositionHistory,
  }))
)
import { PunkAvatar, getTraderAvatar } from '../components/common/PunkAvatar'
import { confirmToast, notify } from '../lib/notify'
import { formatPrice, formatQuantity } from '../utils/format'
import { t, type Language } from '../i18n/translations'
import { LogOut, Loader2, Eye, EyeOff, Copy, Check } from 'lucide-react'
import { DeepVoidBackground } from '../components/common/DeepVoidBackground'
const GridRiskPanel = lazy(() =>
  import('../components/strategy/GridRiskPanel').then((m) => ({
    default: m.GridRiskPanel,
  }))
)
import type {
  SystemStatus,
  AccountInfo,
  Position,
  DecisionRecord,
  Statistics,
  TraderInfo,
  Exchange,
} from '../types'

// --- Helper Functions ---

// Get friendly AI model display name
function getModelDisplayName(modelId: string): string {
  switch (modelId.toLowerCase()) {
    case 'deepseek':
      return 'DeepSeek'
    case 'qwen':
      return 'Qwen'
    case 'claude':
      return 'Claude'
    default:
      return modelId.toUpperCase()
  }
}

// Helper function to get exchange display name from exchange ID (UUID)
function getExchangeDisplayNameFromList(
  exchangeId: string | undefined,
  exchanges: Exchange[] | undefined
): string {
  if (!exchangeId) return 'Unknown'
  const exchange = exchanges?.find((e) => e.id === exchangeId)
  if (!exchange) return exchangeId.substring(0, 8).toUpperCase() + '...'
  const typeName = exchange.exchange_type?.toUpperCase() || exchange.name
  return exchange.account_name
    ? `${typeName} - ${exchange.account_name}`
    : typeName
}

// Helper function to get exchange type from exchange ID (UUID) - for kline charts
function getExchangeTypeFromList(
  exchangeId: string | undefined,
  exchanges: Exchange[] | undefined
): string {
  if (!exchangeId) return 'binance'
  const exchange = exchanges?.find((e) => e.id === exchangeId)
  if (!exchange) return 'binance' // Default to binance for charts
  return exchange.exchange_type?.toLowerCase() || 'binance'
}

// Helper function to check if exchange is a perp-dex type (wallet-based)
function isPerpDexExchange(exchangeType: string | undefined): boolean {
  if (!exchangeType) return false
  const perpDexTypes = ['hyperliquid', 'lighter', 'aster']
  return perpDexTypes.includes(exchangeType.toLowerCase())
}

// Helper function to get wallet address for perp-dex exchanges
function getWalletAddress(exchange: Exchange | undefined): string | undefined {
  if (!exchange) return undefined
  const type = exchange.exchange_type?.toLowerCase()
  switch (type) {
    case 'hyperliquid':
      return exchange.hyperliquidWalletAddr
    case 'lighter':
      return exchange.lighterWalletAddr
    case 'aster':
      return exchange.asterSigner
    default:
      return undefined
  }
}

// Helper function to truncate wallet address for display
function truncateAddress(address: string, startLen = 6, endLen = 4): string {
  if (address.length <= startLen + endLen + 3) return address
  return `${address.slice(0, startLen)}...${address.slice(-endLen)}`
}

// --- Components ---

interface TraderDashboardPageProps {
  selectedTrader?: TraderInfo
  traders?: TraderInfo[]
  tradersError?: Error
  selectedTraderId?: string
  onTraderSelect: (traderId: string) => void
  onNavigateToTraders: () => void
  status?: SystemStatus
  account?: AccountInfo
  positions?: Position[]
  decisions?: DecisionRecord[]
  decisionsLimit: number
  onDecisionsLimitChange: (limit: number) => void
  stats?: Statistics
  lastUpdate: string
  language: Language
  exchanges?: Exchange[]
}

function SectionLoader({
  heightClass = 'min-h-[240px]',
}: {
  heightClass?: string
}) {
  return (
    <div
      className={`w-full ${heightClass} rounded-lg border border-white/5 bg-black/20 animate-pulse`}
    />
  )
}

export function TraderDashboardPage({
  selectedTrader,
  status,
  account,
  positions,
  decisions,
  decisionsLimit,
  onDecisionsLimitChange,
  stats,
  lastUpdate,
  language,
  traders,
  tradersError,
  selectedTraderId,
  onTraderSelect,
  onNavigateToTraders,
  exchanges,
}: TraderDashboardPageProps) {
  const [closingPosition, setClosingPosition] = useState<string | null>(null)
  // Decision list filter: all decisions / all opens / successful opens only /
  // rejected opens only. Paired with client-side pagination below.
  const [decisionFilter, setDecisionFilter] = useState<
    'all' | 'opens' | 'opens_success' | 'opens_rejected'
  >('all')
  const [decisionsPage, setDecisionsPage] = useState<number>(1)
  const decisionsPageSize = 20
  const [selectedChartSymbol, setSelectedChartSymbol] = useState<
    string | undefined
  >(undefined)
  const [chartUpdateKey, setChartUpdateKey] = useState<number>(0)
  const chartSectionRef = useRef<HTMLDivElement>(null)
  const [showWalletAddress, setShowWalletAddress] = useState<boolean>(false)
  const [copiedAddress, setCopiedAddress] = useState<boolean>(false)
  const [allowAIOpen, setAllowAIOpen] = useState<boolean>(true)
  const [allowAIStopClose, setAllowAIStopClose] = useState<boolean>(false)
  const [allowAITakeProfit, setAllowAITakeProfit] = useState<boolean>(false)
  const [aiStopMinLossPct, setAIStopMinLossPct] = useState<number>(0.5)
  const [aiDecisionMode, setAIDecisionMode] = useState<
    'conservative' | 'balanced' | 'aggressive'
  >('balanced')
  const [savingAIControls, setSavingAIControls] = useState<boolean>(false)

  // Current positions pagination
  const [positionsPageSize, setPositionsPageSize] = useState<number>(20)
  const [positionsCurrentPage, setPositionsCurrentPage] = useState<number>(1)

  // Calculate paginated positions
  const totalPositions = positions?.length || 0
  const totalPositionPages = Math.ceil(totalPositions / positionsPageSize)
  const paginatedPositions =
    positions?.slice(
      (positionsCurrentPage - 1) * positionsPageSize,
      positionsCurrentPage * positionsPageSize
    ) || []

  // Long/short notional exposure (USDT) + counts, derived from live positions.
  // Net value uses USDT notional (qty * mark price), not coin quantity.
  const sideBreakdown = useMemo(() => {
    let longNotion = 0
    let shortNotion = 0
    let longCount = 0
    let shortCount = 0
    for (const p of positions || []) {
      const px = Number(p.mark_price || p.entry_price || 0)
      const notion = Number(p.quantity || 0) * px
      if (String(p.side).toUpperCase() === 'LONG') {
        longNotion += notion
        longCount++
      } else {
        shortNotion += notion
        shortCount++
      }
    }
    return { longNotion, shortNotion, longCount, shortCount }
  }, [positions])

  // Multi-select state for batch close.
  const [selectedPositions, setSelectedPositions] = useState<Set<string>>(
    new Set()
  )
  const [batchClosing, setBatchClosing] = useState(false)
  const positionKey = (symbol: string, side: string) =>
    `${symbol}__${String(side).toUpperCase()}`

  // Reset page when positions change
  useEffect(() => {
    setPositionsCurrentPage(1)
  }, [selectedTraderId, positionsPageSize])

  // Auto-set chart symbol for grid trading
  useEffect(() => {
    if (status?.strategy_type === 'grid_trading' && status?.grid_symbol) {
      setSelectedChartSymbol(status.grid_symbol)
    }
  }, [status?.strategy_type, status?.grid_symbol])

  useEffect(() => {
    let cancelled = false
    async function loadAIControls() {
      if (!selectedTraderId) return
      try {
        const cfg = await api.getTraderConfig(selectedTraderId)
        if (cancelled) return
        setAllowAIOpen(cfg.allow_ai_open !== false)
        setAllowAIStopClose(cfg.allow_ai_stop_close === true)
        setAllowAITakeProfit(cfg.allow_ai_take_profit === true)
        setAIStopMinLossPct(
          (cfg.ai_stop_min_loss_pct ?? 0) > 0 ? cfg.ai_stop_min_loss_pct! : 0.5
        )
        setAIDecisionMode(cfg.ai_decision_mode || 'balanced')
      } catch (err) {
        console.error('Failed to load trader AI controls', err)
      }
    }
    loadAIControls()
    return () => {
      cancelled = true
    }
  }, [selectedTraderId])

  const saveAIControls = async (patch: Record<string, unknown>) => {
    if (!selectedTraderId) return
    setSavingAIControls(true)
    try {
      const result = await api.updateTraderAIControls(selectedTraderId, patch)
      if (typeof result.allow_ai_open === 'boolean')
        setAllowAIOpen(result.allow_ai_open as boolean)
      if (typeof result.allow_ai_stop_close === 'boolean')
        setAllowAIStopClose(result.allow_ai_stop_close as boolean)
      if (typeof result.allow_ai_take_profit === 'boolean')
        setAllowAITakeProfit(result.allow_ai_take_profit as boolean)
      if (typeof result.ai_stop_min_loss_pct === 'number')
        setAIStopMinLossPct(result.ai_stop_min_loss_pct as number)
      if (result.ai_decision_mode)
        setAIDecisionMode(
          result.ai_decision_mode as 'conservative' | 'balanced' | 'aggressive'
        )
      await Promise.all([
        mutate(`trader-config-${selectedTraderId}`),
        mutate(`${selectedTraderId}-status`),
        mutate('public-traders'),
      ])
    } catch (err) {
      notify.error(
        err instanceof Error ? err.message : 'Failed to update AI controls'
      )
      throw err
    } finally {
      setSavingAIControls(false)
    }
  }
  const clearSafeMode = async () => {
    if (!selectedTraderId) return
    setSavingAIControls(true)
    try {
      await api.updateTraderAIControls(selectedTraderId, {
        clear_safe_mode: true,
      })
      notify.success(
        'Safe mode cleared. Trader will retry AI on the next cycle.'
      )
      await Promise.all([
        mutate(`${selectedTraderId}-status`),
        mutate('public-traders'),
      ])
    } catch (err) {
      notify.error(
        err instanceof Error ? err.message : 'Failed to clear safe mode'
      )
      throw err
    } finally {
      setSavingAIControls(false)
    }
  }

  // Get current exchange info for perp-dex wallet display
  const currentExchange = exchanges?.find(
    (e) => e.id === selectedTrader?.exchange_id
  )
  const walletAddress = getWalletAddress(currentExchange)
  const isPerpDex = isPerpDexExchange(currentExchange?.exchange_type)

  // Copy wallet address to clipboard
  const handleCopyAddress = async () => {
    if (!walletAddress) return
    try {
      await navigator.clipboard.writeText(walletAddress)
      setCopiedAddress(true)
      setTimeout(() => setCopiedAddress(false), 2000)
    } catch (err) {
      console.error('Failed to copy address:', err)
    }
  }

  // Handle symbol click from Decision Card
  const handleSymbolClick = (symbol: string) => {
    // Set the selected symbol and force chart tabs to react even if the symbol is the same
    setSelectedChartSymbol(symbol)
    setChartUpdateKey(Date.now())
    // Scroll to chart section
    setTimeout(() => {
      chartSectionRef.current?.scrollIntoView({
        behavior: 'smooth',
        block: 'start',
      })
    }, 100)
  }

  // Close position handler
  const handleClosePosition = async (symbol: string, side: string) => {
    if (!selectedTraderId) return

    const sideLabel = side === 'LONG' ? 'LONG' : 'SHORT'
    const confirmMsg = t('traderDashboard.confirmClosePosition', language, {
      symbol,
      side: sideLabel,
    })

    const confirmed = await confirmToast(confirmMsg, {
      title: t('traderDashboard.confirmClose', language),
      okText: t('traderDashboard.confirm', language),
      cancelText: t('traderDashboard.cancel', language),
    })

    if (!confirmed) return

    setClosingPosition(symbol)
    try {
      await api.closePosition(selectedTraderId, symbol, side)
      notify.success(t('traderDashboard.positionClosed', language))
      // Use SWR mutate to refresh data instead of reloading page
      await Promise.all([
        mutate(`positions-${selectedTraderId}`),
        mutate(`account-${selectedTraderId}`),
      ])
    } catch (err: unknown) {
      const errorMsg =
        err instanceof Error
          ? err.message
          : t('traderDashboard.closeFailed', language)
      notify.error(errorMsg)
    } finally {
      setClosingPosition(null)
    }
  }

  // Batch close selected positions at market (sequential to keep error handling
  // per-position; refreshes once at the end).
  const handleBatchClose = async () => {
    if (!selectedTraderId || selectedPositions.size === 0) return
    const targets = (positions || []).filter((p) =>
      selectedPositions.has(positionKey(p.symbol, p.side))
    )
    if (targets.length === 0) return

    const confirmed = await confirmToast(
      t('traderDashboard.confirmBatchClose', language, {
        count: targets.length,
      }),
      {
        title: t('traderDashboard.confirmClose', language),
        okText: t('traderDashboard.confirm', language),
        cancelText: t('traderDashboard.cancel', language),
      }
    )
    if (!confirmed) return

    setBatchClosing(true)
    let ok = 0
    let failed = 0
    for (const p of targets) {
      try {
        await api.closePosition(
          selectedTraderId,
          p.symbol,
          String(p.side).toUpperCase()
        )
        ok++
      } catch {
        failed++
      }
    }
    await Promise.all([
      mutate(`positions-${selectedTraderId}`),
      mutate(`account-${selectedTraderId}`),
    ])
    setSelectedPositions(new Set())
    setBatchClosing(false)
    if (failed === 0) {
      notify.success(
        t('traderDashboard.batchCloseDone', language, { count: ok })
      )
    } else {
      notify.error(
        t('traderDashboard.batchClosePartial', language, { ok, failed })
      )
    }
  }

  const toggleSelectPosition = (symbol: string, side: string) => {
    const key = positionKey(symbol, side)
    setSelectedPositions((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const toggleSelectAll = () => {
    setSelectedPositions((prev) => {
      if (prev.size === (positions?.length || 0)) return new Set()
      return new Set(
        (positions || []).map((p) => positionKey(p.symbol, p.side))
      )
    })
  }

  // If API failed with error, show empty state (likely backend not running)
  if (tradersError) {
    return (
      <div className="flex items-center justify-center min-h-[60vh] relative z-10">
        <div className="text-center max-w-md mx-auto px-6">
          <div
            className="w-24 h-24 mx-auto mb-6 rounded-full flex items-center justify-center nofx-glass"
            style={{
              background: 'rgba(240, 185, 11, 0.1)',
              borderColor: 'rgba(240, 185, 11, 0.3)',
            }}
          >
            <svg
              className="w-12 h-12 text-nofx-gold"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
              />
            </svg>
          </div>
          <h2 className="text-2xl font-bold mb-3 text-nofx-text-main">
            {t('traderDashboard.connectionFailed', language)}
          </h2>
          <p className="text-base mb-6 text-nofx-text-muted">
            {t('traderDashboard.connectionFailedDesc', language)}
          </p>
          <button
            onClick={() => window.location.reload()}
            className="px-6 py-3 rounded-lg font-semibold transition-all hover:scale-105 active:scale-95 nofx-glass border border-nofx-gold/30 text-nofx-gold hover:bg-nofx-gold/10"
          >
            {t('traderDashboard.retry', language)}
          </button>
        </div>
      </div>
    )
  }

  // If traders is loaded and empty, show empty state
  if (traders && traders.length === 0) {
    return (
      <div className="flex items-center justify-center min-h-[60vh] relative z-10">
        <div className="text-center max-w-md mx-auto px-6">
          <div
            className="w-24 h-24 mx-auto mb-6 rounded-full flex items-center justify-center nofx-glass"
            style={{
              background: 'rgba(240, 185, 11, 0.1)',
              borderColor: 'rgba(240, 185, 11, 0.3)',
            }}
          >
            <svg
              className="w-12 h-12 text-nofx-gold"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"
              />
            </svg>
          </div>
          <h2 className="text-2xl font-bold mb-3 text-nofx-text-main">
            {t('dashboardEmptyTitle', language)}
          </h2>
          <p className="text-base mb-6 text-nofx-text-muted">
            {t('dashboardEmptyDescription', language)}
          </p>
          <button
            onClick={onNavigateToTraders}
            className="px-6 py-3 rounded-lg font-semibold transition-all hover:scale-105 active:scale-95 nofx-glass border border-nofx-gold/30 text-nofx-gold hover:bg-nofx-gold/10"
          >
            {t('goToTradersPage', language)}
          </button>
        </div>
      </div>
    )
  }

  // If traders is still loading or selectedTrader is not ready, show skeleton
  if (!selectedTrader) {
    return (
      <div className="space-y-6 relative z-10">
        <div className="nofx-glass p-6 animate-pulse">
          <div className="h-8 w-48 mb-3 bg-nofx-bg/50 rounded"></div>
          <div className="flex gap-4">
            <div className="h-4 w-32 bg-nofx-bg/50 rounded"></div>
            <div className="h-4 w-24 bg-nofx-bg/50 rounded"></div>
            <div className="h-4 w-28 bg-nofx-bg/50 rounded"></div>
          </div>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
          {[1, 2, 3, 4].map((i) => (
            <div key={i} className="nofx-glass p-5 animate-pulse">
              <div className="h-4 w-24 mb-3 bg-nofx-bg/50 rounded"></div>
              <div className="h-8 w-32 bg-nofx-bg/50 rounded"></div>
            </div>
          ))}
        </div>
        <div className="nofx-glass p-6 animate-pulse">
          <div className="h-6 w-40 mb-4 bg-nofx-bg/50 rounded"></div>
          <div className="h-64 w-full bg-nofx-bg/50 rounded"></div>
        </div>
      </div>
    )
  }

  return (
    <DeepVoidBackground className="min-h-screen pb-6" disableAnimation>
      <div className="w-full px-3 md:px-5 lg:px-6 relative z-10 pt-3">
        {/* Trader Header */}
        <div
          className="mb-3 rounded-lg p-4 animate-scale-in nofx-glass group"
          style={{
            background:
              'linear-gradient(135deg, rgba(15, 23, 42, 0.6) 0%, rgba(15, 23, 42, 0.4) 100%)',
          }}
        >
          <div className="flex items-start justify-between mb-2">
            <h2 className="text-2xl font-bold flex items-center gap-3 text-nofx-text-main">
              <div className="relative">
                <PunkAvatar
                  seed={getTraderAvatar(
                    selectedTrader.trader_id,
                    selectedTrader.trader_name
                  )}
                  size={40}
                  className="rounded-xl border-2 border-nofx-gold/30 shadow-[0_0_15px_rgba(240,185,11,0.2)]"
                />
                <div className="absolute -bottom-1 -right-1 w-4 h-4 bg-nofx-green rounded-full border-2 border-[#0B0E11] shadow-[0_0_8px_rgba(14,203,129,0.8)] animate-pulse" />
              </div>
              <div className="flex flex-col">
                <span className="text-xl tracking-tight text-nofx-text font-semibold">
                  {selectedTrader.trader_name}
                </span>
                <span className="text-[10px] font-mono text-nofx-text-muted opacity-60 flex items-center gap-2">
                  <div className="w-1.5 h-1.5 bg-nofx-gold rounded-full" />
                  ID: {selectedTrader.trader_id.slice(0, 8)}...
                </span>
              </div>
            </h2>

            <div className="flex items-center gap-4">
              {/* Trader Selector */}
              {traders && traders.length > 0 && (
                <div className="flex items-center gap-2 nofx-glass px-1 py-1 rounded-lg border border-white/5">
                  <select
                    value={selectedTraderId}
                    onChange={(e) => onTraderSelect(e.target.value)}
                    className="bg-transparent text-sm font-medium cursor-pointer transition-colors text-nofx-text-main focus:outline-none px-2 py-1"
                  >
                    {traders.map((trader) => (
                      <option
                        key={trader.trader_id}
                        value={trader.trader_id}
                        className="bg-[#0B0E11]"
                      >
                        {trader.trader_name}
                      </option>
                    ))}
                  </select>
                </div>
              )}

              {/* Wallet Address Display for Perp-DEX */}
              {exchanges && isPerpDex && (
                <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg nofx-glass border border-nofx-gold/20">
                  {walletAddress ? (
                    <>
                      <span className="text-xs font-mono text-nofx-gold">
                        {showWalletAddress
                          ? walletAddress
                          : truncateAddress(walletAddress)}
                      </span>
                      <button
                        type="button"
                        onClick={() => setShowWalletAddress(!showWalletAddress)}
                        className="p-1 rounded hover:bg-white/10 transition-colors"
                        title={
                          showWalletAddress
                            ? t('traderDashboard.hideAddress', language)
                            : t('traderDashboard.showFullAddress', language)
                        }
                      >
                        {showWalletAddress ? (
                          <EyeOff className="w-3.5 h-3.5 text-nofx-text-muted" />
                        ) : (
                          <Eye className="w-3.5 h-3.5 text-nofx-text-muted" />
                        )}
                      </button>
                      <button
                        type="button"
                        onClick={handleCopyAddress}
                        className="p-1 rounded hover:bg-white/10 transition-colors"
                        title={t('traderDashboard.copyAddress', language)}
                      >
                        {copiedAddress ? (
                          <Check className="w-3.5 h-3.5 text-nofx-green" />
                        ) : (
                          <Copy className="w-3.5 h-3.5 text-nofx-text-muted" />
                        )}
                      </button>
                    </>
                  ) : (
                    <span className="text-xs text-nofx-text-muted">
                      {t('traderDashboard.noAddressConfigured', language)}
                    </span>
                  )}
                </div>
              )}
            </div>
          </div>
          <div className="flex items-center gap-4 text-xs flex-wrap text-nofx-text-muted font-mono pl-1">
            <span className="flex items-center gap-2">
              <span className="opacity-60">AI Model:</span>
              <span
                className="font-bold px-2 py-0.5 rounded text-xs tracking-wide"
                style={{
                  background: selectedTrader.ai_model.includes('qwen')
                    ? 'rgba(192, 132, 252, 0.15)'
                    : 'rgba(96, 165, 250, 0.15)',
                  color: selectedTrader.ai_model.includes('qwen')
                    ? '#c084fc'
                    : '#60a5fa',
                  border: `1px solid ${selectedTrader.ai_model.includes('qwen') ? '#c084fc' : '#60a5fa'}40`,
                }}
              >
                {getModelDisplayName(
                  selectedTrader.ai_model.split('_').pop() ||
                    selectedTrader.ai_model
                )}
              </span>
              <div className="flex items-center gap-3 ml-2">
                {/* AI Open toggle */}
                <label className="flex items-center gap-1 text-xs cursor-pointer">
                  <span className="opacity-60">Open</span>
                  <button
                    type="button"
                    disabled={savingAIControls}
                    onClick={async () => {
                      const next = !allowAIOpen
                      setAllowAIOpen(next)
                      try {
                        await saveAIControls({ allow_ai_open: next })
                      } catch {
                        setAllowAIOpen(!next)
                      }
                    }}
                    className={`relative w-8 h-4 rounded-full transition-colors ${allowAIOpen ? 'bg-green-500/80' : 'bg-white/10'}`}
                  >
                    <div
                      className={`absolute top-0.5 w-3 h-3 rounded-full bg-white shadow transition-transform ${allowAIOpen ? 'translate-x-[18px]' : 'translate-x-[2px]'}`}
                    />
                  </button>
                </label>
                {/* AI Stop-Loss Close toggle */}
                <label className="flex items-center gap-1 text-xs cursor-pointer">
                  <span className="opacity-60">SL</span>
                  <button
                    type="button"
                    disabled={savingAIControls}
                    onClick={async () => {
                      const next = !allowAIStopClose
                      setAllowAIStopClose(next)
                      try {
                        await saveAIControls({ allow_ai_stop_close: next })
                      } catch {
                        setAllowAIStopClose(!next)
                      }
                    }}
                    className={`relative w-8 h-4 rounded-full transition-colors ${allowAIStopClose ? 'bg-green-500/80' : 'bg-white/10'}`}
                  >
                    <div
                      className={`absolute top-0.5 w-3 h-3 rounded-full bg-white shadow transition-transform ${allowAIStopClose ? 'translate-x-[18px]' : 'translate-x-[2px]'}`}
                    />
                  </button>
                </label>
                {/* AI Take-Profit Close toggle */}
                <label className="flex items-center gap-1 text-xs cursor-pointer">
                  <span className="opacity-60">TP</span>
                  <button
                    type="button"
                    disabled={savingAIControls}
                    onClick={async () => {
                      const next = !allowAITakeProfit
                      setAllowAITakeProfit(next)
                      try {
                        await saveAIControls({ allow_ai_take_profit: next })
                      } catch {
                        setAllowAITakeProfit(!next)
                      }
                    }}
                    className={`relative w-8 h-4 rounded-full transition-colors ${allowAITakeProfit ? 'bg-green-500/80' : 'bg-white/10'}`}
                  >
                    <div
                      className={`absolute top-0.5 w-3 h-3 rounded-full bg-white shadow transition-transform ${allowAITakeProfit ? 'translate-x-[18px]' : 'translate-x-[2px]'}`}
                    />
                  </button>
                </label>
                {/* Min Loss % adjuster (only visible when SL is enabled) */}
                {allowAIStopClose && (
                  <div className="flex items-center gap-0.5 text-xs">
                    <span className="opacity-60">Min</span>
                    <button
                      type="button"
                      disabled={savingAIControls || aiStopMinLossPct <= 0.1}
                      onClick={async () => {
                        const next = Math.max(
                          0.1,
                          +(aiStopMinLossPct - 0.1).toFixed(1)
                        )
                        const prev = aiStopMinLossPct
                        setAIStopMinLossPct(next)
                        try {
                          await saveAIControls({ ai_stop_min_loss_pct: next })
                        } catch {
                          setAIStopMinLossPct(prev)
                        }
                      }}
                      className="w-4 h-4 flex items-center justify-center rounded bg-white/5 hover:bg-white/10 text-nofx-text-muted"
                    >
                      −
                    </button>
                    <span className="w-9 text-center text-nofx-text-main">
                      {aiStopMinLossPct.toFixed(1)}%
                    </span>
                    <button
                      type="button"
                      disabled={savingAIControls || aiStopMinLossPct >= 5.0}
                      onClick={async () => {
                        const next = Math.min(
                          5.0,
                          +(aiStopMinLossPct + 0.1).toFixed(1)
                        )
                        const prev = aiStopMinLossPct
                        setAIStopMinLossPct(next)
                        try {
                          await saveAIControls({ ai_stop_min_loss_pct: next })
                        } catch {
                          setAIStopMinLossPct(prev)
                        }
                      }}
                      className="w-4 h-4 flex items-center justify-center rounded bg-white/5 hover:bg-white/10 text-nofx-text-muted"
                    >
                      +
                    </button>
                  </div>
                )}
                {/* Decision mode */}
                <select
                  value={aiDecisionMode}
                  disabled={savingAIControls}
                  onChange={async (e) => {
                    const next = e.target.value as
                      | 'conservative'
                      | 'balanced'
                      | 'aggressive'
                    const prev = aiDecisionMode
                    setAIDecisionMode(next)
                    try {
                      await saveAIControls({ ai_decision_mode: next })
                    } catch {
                      setAIDecisionMode(prev)
                    }
                  }}
                  className="bg-transparent border border-white/10 rounded px-2 py-0.5 text-xs text-nofx-text-main"
                >
                  <option value="conservative" className="bg-[#0B0E11]">
                    保守
                  </option>
                  <option value="balanced" className="bg-[#0B0E11]">
                    平衡
                  </option>
                  <option value="aggressive" className="bg-[#0B0E11]">
                    激进
                  </option>
                </select>
              </div>
            </span>
            <span className="w-px h-3 bg-white/10 hidden md:block" />
            <span className="flex items-center gap-2">
              <span className="opacity-60">Exchange:</span>
              <span className="text-nofx-text-main font-semibold">
                {getExchangeDisplayNameFromList(
                  selectedTrader.exchange_id,
                  exchanges
                )}
              </span>
            </span>
            <span className="w-px h-3 bg-white/10 hidden md:block" />
            <span className="flex items-center gap-2">
              <span className="opacity-60">Strategy:</span>
              <span className="text-nofx-gold font-semibold tracking-wide">
                {selectedTrader.strategy_name || 'No Strategy'}
              </span>
            </span>
            {status &&
              (status.protect_only ||
                status.safe_mode ||
                status.allow_ai_open === false ||
                (!allowAIStopClose && !allowAITakeProfit) ||
                allowAIStopClose !== allowAITakeProfit) && (
                <span className="w-px h-3 bg-white/10 hidden md:block" />
              )}
            {status?.protect_only && (
              <span className="px-2 py-0.5 rounded border border-amber-400/40 bg-amber-400/10 text-amber-300 font-semibold">
                PROTECT-ONLY
              </span>
            )}
            {status?.safe_mode && !status?.protect_only && (
              <span
                className="px-2 py-0.5 rounded border border-orange-400/40 bg-orange-400/10 text-orange-300 font-semibold"
                title={status.safe_mode_reason || undefined}
              >
                SAFE MODE
              </span>
            )}
            {status?.allow_ai_open === false && (
              <span className="px-2 py-0.5 rounded border border-purple-400/30 bg-purple-400/10 text-purple-300 font-semibold">
                AI OPEN OFF
              </span>
            )}
            {status?.allow_ai_close === false &&
              !allowAIStopClose &&
              !allowAITakeProfit && (
                <span className="px-2 py-0.5 rounded border border-blue-400/30 bg-blue-400/10 text-blue-300 font-semibold">
                  AI CLOSE OFF
                </span>
              )}
            {(allowAIStopClose || allowAITakeProfit) &&
              !(allowAIStopClose && allowAITakeProfit) && (
                <span className="px-2 py-0.5 rounded border border-cyan-400/30 bg-cyan-400/10 text-cyan-300 font-semibold">
                  {allowAIStopClose && !allowAITakeProfit && 'SL ONLY'}
                  {!allowAIStopClose && allowAITakeProfit && 'TP ONLY'}
                </span>
              )}
            {status?.safe_mode_reason && (
              <span
                className="max-w-[720px] whitespace-normal break-words rounded border border-white/10 bg-black/20 px-2 py-1"
                title={status.safe_mode_reason}
              >
                Reason:{' '}
                <span className="text-nofx-text-main">
                  {status.safe_mode_reason}
                </span>
              </span>
            )}
            {status?.safe_mode && (
              <button
                type="button"
                disabled={savingAIControls}
                onClick={() => clearSafeMode().catch(() => undefined)}
                className="px-2 py-0.5 rounded border border-emerald-400/40 bg-emerald-400/10 text-emerald-300 font-semibold hover:bg-emerald-400/20 disabled:opacity-50"
                title="Clear safe mode and let the trader retry AI on the next cycle"
              >
                CLEAR SAFE MODE
              </button>
            )}
            {status && (
              <div className="hidden md:contents">
                <span className="w-px h-3 bg-white/10" />
                <span>
                  Cycles:{' '}
                  <span className="text-nofx-text-main">
                    {status.call_count}
                  </span>
                </span>
                <span className="w-px h-3 bg-white/10" />
                <span>
                  Runtime:{' '}
                  <span className="text-nofx-text-main">
                    {status.runtime_minutes} min
                  </span>
                </span>
              </div>
            )}
          </div>
        </div>

        {/* Debug Info */}
        {account && (
          <div className="mb-2 px-3 py-1 rounded bg-black/40 border border-white/5 text-[10px] font-mono text-nofx-text-muted flex justify-between items-center opacity-60 hover:opacity-100 transition-opacity">
            <span>SYSTEM_STATUS::ONLINE</span>
            <div className="flex gap-4">
              <span>LAST_UPDATE::{lastUpdate}</span>
              <span>EQ::{account?.total_equity?.toFixed(2)}</span>
              <span>PNL::{account?.total_pnl?.toFixed(2)}</span>
            </div>
          </div>
        )}

        {/* Account Overview */}
        <div className="grid grid-cols-2 md:grid-cols-6 gap-3 mb-4">
          <StatCard
            title={t('totalEquity', language)}
            value={`${account?.total_equity?.toFixed(2) || '0.00'}`}
            unit="USDT"
            change={account?.total_pnl_pct || 0}
            positive={(account?.total_pnl ?? 0) > 0}
            icon="💰"
          />
          <StatCard
            title={t('availableBalance', language)}
            value={`${account?.available_balance?.toFixed(2) || '0.00'}`}
            unit="USDT"
            subtitle={`${account?.available_balance && account?.total_equity ? ((account.available_balance / account.total_equity) * 100).toFixed(1) : '0.0'}% ${t('free', language)}`}
            icon="💳"
          />
          <StatCard
            title={t('totalPnL', language)}
            value={`${account?.total_pnl !== undefined && account.total_pnl >= 0 ? '+' : ''}${account?.total_pnl?.toFixed(2) || '0.00'}`}
            unit="USDT"
            change={account?.total_pnl_pct || 0}
            positive={(account?.total_pnl ?? 0) >= 0}
            icon="📈"
          />
          <SideSplitCard
            longNotion={sideBreakdown.longNotion}
            shortNotion={sideBreakdown.shortNotion}
            longCount={sideBreakdown.longCount}
            shortCount={sideBreakdown.shortCount}
            velIndex={status?.breadth_vel_index}
            peakIndex={status?.breadth_peak_index}
            traderId={selectedTraderId}
            language={language}
          />
          <StatCard
            title={t('positions', language)}
            value={`${account?.position_count || 0}`}
            unit="ACTIVE"
            subtitle={`${t('margin', language)}: ${account?.margin_used_pct?.toFixed(1) || '0.0'}%`}
            icon="📊"
          />
          <SystemHealthCard decisions={decisions} language={language} />
        </div>

        {/* Grid Risk Panel - Only show for grid trading strategy */}
        {status?.strategy_type === 'grid_trading' && selectedTraderId && (
          <div
            className="mb-4 animate-slide-in"
            style={{ animationDelay: '0.05s' }}
          >
            <GridRiskPanel
              traderId={selectedTraderId}
              language={language}
              refreshInterval={5000}
            />
          </div>
        )}

        {/* Main Content Area */}
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4 mb-4">
          {/* Left Column: Charts + Positions */}
          <div className="space-y-4">
            {/* Chart Tabs (Equity / K-line) */}
            <div
              ref={chartSectionRef}
              className="chart-container animate-slide-in scroll-mt-32 backdrop-blur-sm"
              style={{ animationDelay: '0.1s' }}
            >
              <Suspense
                fallback={
                  <SectionLoader heightClass="h-[500px] md:h-[600px]" />
                }
              >
                <ChartTabs
                  traderId={selectedTrader.trader_id}
                  selectedSymbol={selectedChartSymbol}
                  updateKey={chartUpdateKey}
                  exchangeId={getExchangeTypeFromList(
                    selectedTrader.exchange_id,
                    exchanges
                  )}
                  disableAutoRefresh={false}
                />
              </Suspense>
              {/* Coin evolution summary below chart */}
              {selectedChartSymbol && selectedTraderId && (
                <CoinProfileSummary
                  traderId={selectedTraderId}
                  symbol={selectedChartSymbol}
                />
              )}
            </div>

            {/* Current Positions */}
            <div
              className="nofx-glass p-4 animate-slide-in relative overflow-hidden group"
              style={{ animationDelay: '0.15s' }}
            >
              <div className="absolute top-0 right-0 p-3 opacity-10 group-hover:opacity-20 transition-opacity">
                <div className="w-24 h-24 rounded-full bg-blue-500 blur-3xl" />
              </div>
              <div className="flex items-center justify-between mb-5 relative z-10">
                <h2 className="text-lg font-bold flex items-center gap-2 text-nofx-text-main uppercase tracking-wide">
                  <span className="text-blue-500">◈</span>{' '}
                  {t('currentPositions', language)}
                </h2>
                {positions && positions.length > 0 && (
                  <div className="text-xs px-2 py-1 rounded bg-nofx-gold/10 text-nofx-gold border border-nofx-gold/20 font-mono shadow-[0_0_10px_rgba(240,185,11,0.1)]">
                    {positions.length} {t('active', language)}
                  </div>
                )}
              </div>
              {positions && positions.length > 0 ? (
                <div>
                  <div className="flex items-center gap-3 mb-3">
                    <label className="flex items-center gap-1.5 text-xs text-nofx-text-muted cursor-pointer select-none">
                      <input
                        type="checkbox"
                        checked={
                          selectedPositions.size === positions.length &&
                          positions.length > 0
                        }
                        onChange={toggleSelectAll}
                        className="accent-nofx-gold w-3.5 h-3.5"
                      />
                      {t('traderDashboard.selectAll', language)}
                    </label>
                    {selectedPositions.size > 0 && (
                      <button
                        type="button"
                        onClick={handleBatchClose}
                        disabled={batchClosing}
                        className="inline-flex items-center gap-1 px-2.5 py-1 rounded text-[11px] font-semibold transition-all hover:scale-105 disabled:opacity-50 disabled:cursor-not-allowed bg-nofx-red/15 text-nofx-red border border-nofx-red/40 hover:bg-nofx-red/25"
                      >
                        {batchClosing ? (
                          <Loader2 className="w-3 h-3 animate-spin" />
                        ) : (
                          <LogOut className="w-3 h-3" />
                        )}
                        {t('traderDashboard.closeSelected', language)} (
                        {selectedPositions.size})
                      </button>
                    )}
                  </div>
                  <div className="overflow-x-auto">
                    <table className="w-full text-xs">
                      <thead className="text-left border-b border-white/5">
                        <tr>
                          <th className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-center w-6">
                            <span className="sr-only">select</span>
                          </th>
                          <th className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-left">
                            {t('symbol', language)}
                          </th>
                          <th className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-center">
                            {t('side', language)}
                          </th>
                          <th className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-center">
                            {t('traderDashboard.action', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-right hidden md:table-cell"
                            title={t('entryPrice', language)}
                          >
                            {t('traderDashboard.entry', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-right hidden md:table-cell"
                            title={t('markPrice', language)}
                          >
                            {t('traderDashboard.mark', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-right"
                            title={t('quantity', language)}
                          >
                            {t('traderDashboard.qty', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-right hidden md:table-cell"
                            title={t('positionValue', language)}
                          >
                            {t('traderDashboard.value', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-center hidden md:table-cell"
                            title={t('leverage', language)}
                          >
                            {t('traderDashboard.lev', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-right"
                            title={t('unrealizedPnL', language)}
                          >
                            {t('traderDashboard.uPnL', language)}
                          </th>
                          <th
                            className="px-1 pb-3 font-semibold text-nofx-text-muted whitespace-nowrap text-right hidden md:table-cell"
                            title={t('liqPrice', language)}
                          >
                            {t('traderDashboard.liq', language)}
                          </th>
                        </tr>
                      </thead>
                      <tbody>
                        {paginatedPositions.map((pos, i) => (
                          <tr
                            key={i}
                            className="border-b border-white/5 last:border-0 transition-all hover:bg-white/5 cursor-pointer group/row"
                            onClick={() => {
                              setSelectedChartSymbol(pos.symbol)
                              setChartUpdateKey(Date.now())
                              if (chartSectionRef.current) {
                                chartSectionRef.current.scrollIntoView({
                                  behavior: 'smooth',
                                  block: 'start',
                                })
                              }
                            }}
                          >
                            <td
                              className="px-1 py-3 text-center"
                              onClick={(e) => e.stopPropagation()}
                            >
                              <input
                                type="checkbox"
                                checked={selectedPositions.has(
                                  positionKey(pos.symbol, pos.side)
                                )}
                                onChange={() =>
                                  toggleSelectPosition(pos.symbol, pos.side)
                                }
                                className="accent-nofx-gold w-3.5 h-3.5"
                              />
                            </td>
                            <td className="px-1 py-3 font-mono font-semibold whitespace-nowrap text-left text-nofx-text-main group-hover/row:text-white transition-colors">
                              {pos.symbol}
                            </td>
                            <td className="px-1 py-3 whitespace-nowrap text-center">
                              <span
                                className={`px-1.5 py-0.5 rounded text-[10px] font-bold uppercase tracking-wider ${pos.side === 'long' ? 'bg-nofx-green/10 text-nofx-green shadow-[0_0_8px_rgba(14,203,129,0.2)]' : 'bg-nofx-red/10 text-nofx-red shadow-[0_0_8px_rgba(246,70,93,0.2)]'}`}
                              >
                                {t(
                                  pos.side === 'long' ? 'long' : 'short',
                                  language
                                )}
                              </span>
                            </td>
                            <td className="px-1 py-3 whitespace-nowrap text-center">
                              <button
                                type="button"
                                onClick={(e) => {
                                  e.stopPropagation()
                                  handleClosePosition(
                                    pos.symbol,
                                    pos.side.toUpperCase()
                                  )
                                }}
                                disabled={closingPosition === pos.symbol}
                                className="inline-flex items-center gap-1 px-2 py-1 rounded text-[10px] font-semibold transition-all hover:scale-105 disabled:opacity-50 disabled:cursor-not-allowed mx-auto bg-nofx-red/10 text-nofx-red border border-nofx-red/30 hover:bg-nofx-red/20"
                                title={t(
                                  'traderDashboard.closePosition',
                                  language
                                )}
                              >
                                {closingPosition === pos.symbol ? (
                                  <Loader2 className="w-3 h-3 animate-spin" />
                                ) : (
                                  <LogOut className="w-3 h-3" />
                                )}
                                {t('traderDashboard.close', language)}
                              </button>
                            </td>
                            <td className="px-1 py-3 font-mono whitespace-nowrap text-right text-nofx-text-main hidden md:table-cell">
                              {formatPrice(pos.entry_price)}
                            </td>
                            <td className="px-1 py-3 font-mono whitespace-nowrap text-right text-nofx-text-main hidden md:table-cell">
                              {formatPrice(pos.mark_price)}
                            </td>
                            <td className="px-1 py-3 font-mono whitespace-nowrap text-right text-nofx-text-main">
                              {formatQuantity(pos.quantity)}
                            </td>
                            <td className="px-1 py-3 font-mono font-bold whitespace-nowrap text-right text-nofx-text-main hidden md:table-cell">
                              {(pos.quantity * pos.mark_price).toFixed(2)}
                            </td>
                            <td className="px-1 py-3 font-mono whitespace-nowrap text-center text-nofx-gold hidden md:table-cell">
                              {pos.leverage}x
                            </td>
                            <td className="px-1 py-3 font-mono whitespace-nowrap text-right">
                              <span
                                className={`font-bold ${pos.unrealized_pnl >= 0 ? 'text-nofx-green shadow-nofx-green' : 'text-nofx-red shadow-nofx-red'}`}
                                style={{
                                  textShadow:
                                    pos.unrealized_pnl >= 0
                                      ? '0 0 10px rgba(14,203,129,0.3)'
                                      : '0 0 10px rgba(246,70,93,0.3)',
                                }}
                              >
                                {pos.unrealized_pnl >= 0 ? '+' : ''}
                                {pos.unrealized_pnl.toFixed(2)}
                              </span>
                            </td>
                            <td className="px-1 py-3 font-mono whitespace-nowrap text-right text-nofx-text-muted hidden md:table-cell">
                              {formatPrice(pos.liquidation_price)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {/* Pagination footer */}
                  {totalPositions > 10 && (
                    <div className="flex flex-wrap items-center justify-between gap-3 pt-4 mt-4 text-xs border-t border-white/5 text-nofx-text-muted">
                      <span>
                        {t('traderDashboard.showingPositions', language, {
                          shown: paginatedPositions.length,
                          total: totalPositions,
                        })}
                      </span>
                      <div className="flex items-center gap-3">
                        <div className="flex items-center gap-2">
                          <span>{t('traderDashboard.perPage', language)}:</span>
                          <select
                            value={positionsPageSize}
                            onChange={(e) =>
                              setPositionsPageSize(Number(e.target.value))
                            }
                            className="bg-black/40 border border-white/10 rounded px-2 py-1 text-xs text-nofx-text-main focus:outline-none focus:border-nofx-gold/50 transition-colors"
                          >
                            <option value={20}>20</option>
                            <option value={50}>50</option>
                            <option value={100}>100</option>
                          </select>
                        </div>
                        {totalPositionPages > 1 && (
                          <div className="flex items-center gap-1">
                            {[
                              '«',
                              '‹',
                              `${positionsCurrentPage} / ${totalPositionPages}`,
                              '›',
                              '»',
                            ].map((label, idx) => {
                              const isText = idx === 2
                              const isFirst = idx === 0
                              const isPrev = idx === 1
                              const isNext = idx === 3
                              const isLast = idx === 4
                              if (isText)
                                return (
                                  <span
                                    key={idx}
                                    className="px-3 text-nofx-text-main"
                                  >
                                    {label}
                                  </span>
                                )
                              return (
                                <button
                                  key={idx}
                                  onClick={() => {
                                    if (isFirst) setPositionsCurrentPage(1)
                                    if (isPrev)
                                      setPositionsCurrentPage(
                                        Math.max(1, positionsCurrentPage - 1)
                                      )
                                    if (isNext)
                                      setPositionsCurrentPage(
                                        Math.min(
                                          totalPositionPages,
                                          positionsCurrentPage + 1
                                        )
                                      )
                                    if (isLast)
                                      setPositionsCurrentPage(
                                        totalPositionPages
                                      )
                                  }}
                                  disabled={
                                    isFirst || isPrev
                                      ? positionsCurrentPage === 1
                                      : positionsCurrentPage ===
                                        totalPositionPages
                                  }
                                  className="px-2 py-1 rounded border border-white/10 hover:bg-white/5 disabled:opacity-40 disabled:cursor-not-allowed"
                                >
                                  {label}
                                </button>
                              )
                            })}
                          </div>
                        )}
                      </div>
                    </div>
                  )}
                </div>
              ) : (
                <div className="text-sm text-nofx-text-muted">
                  {t('noData', language)}
                </div>
              )}
            </div>
          </div>

          {/* Right Column: Position Protection */}
          <div
            className="animate-slide-in h-fit lg:sticky lg:top-16 lg:max-h-[calc(100vh-80px)] overflow-y-auto"
            style={{ animationDelay: '0.2s' }}
          >
            <PositionProtectionPanel
              traderId={selectedTraderId}
              positions={positions}
              language={language}
              exchange={getExchangeTypeFromList(
                selectedTrader?.exchange_id,
                exchanges
              )}
              onSymbolClick={handleSymbolClick}
            />
          </div>
        </div>

        {/* Decisions + Position History — portrait: stacked (decisions first);
            landscape: side-by-side. Wrapped together so the two can sit in one
            row on wide/landscape screens while the intervening analytics + insight
            panels fall below as full-width sections. */}
        <div className="flex flex-col landscape:flex-row lg:flex-row gap-4 mb-4 items-stretch">
          {/* Recent Decisions */}
          <div
            className="nofx-glass p-4 animate-slide-in landscape:flex-1 lg:flex-1 min-w-0"
            style={{ animationDelay: '0.2s' }}
          >
            {/* Header */}
            <div className="flex items-center gap-3 mb-5 pb-4 border-b border-white/5 shrink-0">
              <div
                className="w-10 h-10 rounded-xl flex items-center justify-center text-xl shadow-[0_4px_14px_rgba(99,102,241,0.4)]"
                style={{
                  background:
                    'linear-gradient(135deg, #6366F1 0%, #8B5CF6 100%)',
                }}
              >
                🧠
              </div>
              <div className="flex-1">
                <h2 className="text-xl font-bold text-nofx-text-main">
                  {t('recentDecisions', language)}
                </h2>
                {decisions && decisions.length > 0 && (
                  <div className="text-xs text-nofx-text-muted">
                    {t('lastCycles', language, { count: decisions.length })}
                  </div>
                )}
              </div>
              {/* Decision type filter */}
              <select
                value={decisionFilter}
                onChange={(e) => {
                  setDecisionFilter(e.target.value as typeof decisionFilter)
                  setDecisionsPage(1)
                }}
                className="px-3 py-1.5 rounded-lg text-sm font-medium cursor-pointer transition-all bg-black/40 text-nofx-text-main border border-white/10 hover:border-nofx-accent focus:outline-none"
              >
                <option value="all">
                  {language === 'zh' ? '全部决策' : 'All decisions'}
                </option>
                <option value="opens">
                  {language === 'zh' ? '全部开仓决策' : 'All opens'}
                </option>
                <option value="opens_success">
                  {language === 'zh' ? '仅成功开仓' : 'Successful opens'}
                </option>
                <option value="opens_rejected">
                  {language === 'zh' ? '仅拒绝开仓' : 'Rejected opens'}
                </option>
              </select>
              {/* Limit Selector */}
              <select
                value={decisionsLimit}
                onChange={(e) => {
                  onDecisionsLimitChange(Number(e.target.value))
                  setDecisionsPage(1)
                }}
                className="px-3 py-1.5 rounded-lg text-sm font-medium cursor-pointer transition-all bg-black/40 text-nofx-text-main border border-white/10 hover:border-nofx-accent focus:outline-none"
              >
                <option value={5}>5</option>
                <option value={10}>10</option>
                <option value={20}>20</option>
                <option value={50}>50</option>
                <option value={100}>100</option>
                <option value={300}>300</option>
                <option value={500}>500</option>
              </select>
            </div>

            {/* Decisions List */}
            <div
              className="space-y-4 overflow-y-auto pr-2 custom-scrollbar"
              style={{ maxHeight: '600px' }}
            >
              {(() => {
                const isOpenRejected = (d: DecisionRecord) =>
                  d.decisions?.some(
                    (a) =>
                      a.action.includes('open') &&
                      (a.review_context?.control?.decision === 'rejected' ||
                        a.review_context?.control?.decision ===
                          'downgraded_to_wait' ||
                        a.review_context?.quality_gate?.decision ===
                          'rejected' ||
                        a.review_context?.quality_gate?.decision ===
                          'blocked' ||
                        (!a.success && !!a.error))
                  )
                const isOpenSuccess = (d: DecisionRecord) =>
                  d.decisions?.some(
                    (a) => a.action.includes('open') && a.success
                  )
                const hasOpen = (d: DecisionRecord) =>
                  d.decisions?.some((a) => a.action.includes('open'))

                const filteredDecisions = (decisions || []).filter((d) => {
                  switch (decisionFilter) {
                    case 'opens':
                      return hasOpen(d)
                    case 'opens_success':
                      return isOpenSuccess(d)
                    case 'opens_rejected':
                      return isOpenRejected(d) && !isOpenSuccess(d)
                    default:
                      return true
                  }
                })

                const totalPages = Math.max(
                  1,
                  Math.ceil(filteredDecisions.length / decisionsPageSize)
                )
                const page = Math.min(decisionsPage, totalPages)
                const pageStart = (page - 1) * decisionsPageSize
                const pageDecisions = filteredDecisions.slice(
                  pageStart,
                  pageStart + decisionsPageSize
                )

                if (filteredDecisions.length === 0) {
                  return (
                    <div className="py-16 text-center text-nofx-text-muted opacity-60">
                      <div className="text-6xl mb-4 opacity-30 grayscale">
                        🧠
                      </div>
                      <div className="text-lg font-semibold mb-2 text-nofx-text-main">
                        {t('noDecisionsYet', language)}
                      </div>
                      <div className="text-sm">
                        {t('aiDecisionsWillAppear', language)}
                      </div>
                    </div>
                  )
                }

                return (
                  <>
                    {pageDecisions.map((decision, i) => (
                      <DecisionCard
                        key={`${decision.cycle_number}-${i}`}
                        decision={decision}
                        language={language}
                        traderId={selectedTraderId}
                        onSymbolClick={handleSymbolClick}
                      />
                    ))}
                    {totalPages > 1 && (
                      <div className="flex items-center justify-center gap-2 pt-2 flex-wrap">
                        <button
                          onClick={() =>
                            setDecisionsPage((p) => Math.max(1, p - 1))
                          }
                          disabled={page <= 1}
                          className="px-3 py-1 rounded-lg text-xs font-medium bg-black/40 text-nofx-text-main border border-white/10 disabled:opacity-40 hover:border-nofx-accent"
                        >
                          {language === 'zh' ? '上一页' : 'Prev'}
                        </button>
                        <span className="text-xs text-nofx-text-muted">
                          {page} / {totalPages}
                        </span>
                        <button
                          onClick={() =>
                            setDecisionsPage((p) => Math.min(totalPages, p + 1))
                          }
                          disabled={page >= totalPages}
                          className="px-3 py-1 rounded-lg text-xs font-medium bg-black/40 text-nofx-text-main border border-white/10 disabled:opacity-40 hover:border-nofx-accent"
                        >
                          {language === 'zh' ? '下一页' : 'Next'}
                        </button>
                      </div>
                    )}
                  </>
                )
              })()}
            </div>
          </div>

          {/* Position History TABLE — sibling column (landscape) / below decisions (portrait).
              The aggregate stats block is rendered full-width below this row. */}
          {selectedTraderId && (
            <div
              className="nofx-glass p-4 animate-slide-in landscape:flex-1 lg:flex-1 min-w-0"
              style={{ animationDelay: '0.25s' }}
            >
              <div className="flex items-center justify-between mb-5">
                <h2 className="text-xl font-bold flex items-center gap-2 text-nofx-text-main">
                  <span className="text-2xl">📜</span>
                  {t('positionHistory.title', language)}
                </h2>
              </div>
              <Suspense
                fallback={<SectionLoader heightClass="min-h-[420px]" />}
              >
                <PositionHistory
                  traderId={selectedTraderId}
                  onSymbolClick={handleSymbolClick}
                  section="table"
                />
              </Suspense>
            </div>
          )}
        </div>

        {/* Position History STATS — full width below the decisions + table row */}
        {selectedTraderId && (
          <div
            className="nofx-glass p-4 animate-slide-in mb-4"
            style={{ animationDelay: '0.28s' }}
          >
            <div className="flex items-center justify-between mb-5">
              <h2 className="text-xl font-bold flex items-center gap-2 text-nofx-text-main">
                <span className="text-2xl">📊</span>
                {t('positionHistory.title', language)}
              </h2>
            </div>
            <Suspense fallback={<SectionLoader heightClass="min-h-[240px]" />}>
              <PositionHistory
                traderId={selectedTraderId}
                onSymbolClick={handleSymbolClick}
                section="stats"
              />
            </Suspense>
            <EvolutionProfilePanel traderId={selectedTraderId} />
          </div>
        )}

        {/* Advanced Analytics - Collapsible */}
        <details
          className="nofx-glass mb-4 animate-slide-in"
          style={{ animationDelay: '0.25s' }}
        >
          <summary className="cursor-pointer p-4 flex items-center gap-3 hover:bg-white/5 transition-colors rounded-lg">
            <div
              className="w-8 h-8 rounded-lg flex items-center justify-center text-base shadow-sm"
              style={{
                background: 'linear-gradient(135deg, #10B981 0%, #059669 100%)',
              }}
            >
              📊
            </div>
            <div className="flex-1">
              <h3 className="text-lg font-bold text-nofx-text-main">
                {language === 'zh' ? '高级分析' : 'Advanced Analytics'}
              </h3>
              <p className="text-xs text-nofx-text-muted">
                {language === 'zh'
                  ? '策略期望值与平仓归因详情'
                  : 'Strategy expectancy & close attribution details'}
              </p>
            </div>
            <span className="text-nofx-text-muted text-sm">▼</span>
          </summary>
          <div className="p-4 pt-0 space-y-4">
            {/* Strategy expectancy (net-of-fees edge) */}
            <ExpectancyPanel stats={stats} language={language} />

            {/* Close attribution: every exit traced to AI/protection/manual/exchange */}
            <CloseAttributionPanel
              traderId={selectedTraderId}
              language={language}
            />

            {/* Trend-reversal flip observations (dry-run + live) */}
            <FlipObservationsPanel
              traderId={selectedTraderId}
              language={language}
            />

            {/* Circuit-breaker / breadth-guard close history */}
            <BreakerHistoryPanel
              traderId={selectedTraderId}
              language={language}
            />
          </div>
        </details>

        {/* Smart Insights Panel */}
        {selectedTraderId && decisions && (
          <InsightPanel
            traderId={selectedTraderId}
            decisions={decisions}
            language={language}
          />
        )}
      </div>
    </DeepVoidBackground>
  )
}

// SideSplitCard shows live long/short notional exposure (USDT) plus a 12h curve
// of each side's share of total exposure (long% and short%, summing to 100%,
// bounded 0..100% with explicit axis labels).
// BreadthGauge renders a single 0–100 breadth circuit-breaker pressure index as
// a compact vertical bar. Color escalates with urgency: <50 green, 50–70 amber,
// 70–85 orange, 85–<100 red, 100 solid red (= breaker fire threshold reached).
// 0 = no risk / no position retracing on that path.
function BreadthGauge({
  value,
  label,
  title,
}: {
  value: number
  label: string
  title: string
}) {
  const v = Math.max(0, Math.min(100, value))
  const color =
    v >= 100
      ? '#F6465D'
      : v >= 85
        ? '#FF5C39'
        : v >= 70
          ? '#FF8A00'
          : v >= 50
            ? '#F0B90B'
            : '#0ECB81'
  const trackH = 28
  return (
    <div className="flex flex-col items-center gap-0.5" title={title}>
      <div
        className="relative w-2 rounded-sm overflow-hidden bg-white/10"
        style={{ height: trackH }}
      >
        {/* threshold ticks at 50 / 70 / 85 */}
        {[50, 70, 85].map((m) => (
          <div
            key={m}
            className="absolute left-0 right-0"
            style={{
              bottom: `${m}%`,
              height: 1,
              background: 'rgba(255,255,255,0.25)',
            }}
          />
        ))}
        <div
          className="absolute bottom-0 left-0 right-0 transition-[height] duration-500"
          style={{ height: `${v}%`, background: color }}
        />
      </div>
      <span className="text-[8px] font-mono leading-none" style={{ color }}>
        {Math.round(v)}
      </span>
      <span className="text-[7px] font-mono uppercase text-nofx-text-muted/60 leading-none">
        {label}
      </span>
    </div>
  )
}

function SideSplitCard({
  longNotion,
  shortNotion,
  longCount,
  shortCount,
  velIndex,
  peakIndex,
  traderId,
  language,
}: {
  longNotion: number
  shortNotion: number
  longCount: number
  shortCount: number
  velIndex?: number
  peakIndex?: number
  traderId: string | null | undefined
  language: string
}) {
  const [series, setSeries] = useState<
    { longShare: number; shortShare: number }[]
  >([])

  useEffect(() => {
    let cancelled = false
    async function load() {
      if (!traderId) return
      try {
        const resp = await api.getSidePnLSeries(traderId, 12)
        if (cancelled) return
        const pts = (resp.series || []).map((b) => {
          const total = b.long_notion + b.short_notion
          if (total <= 0) return { longShare: 50, shortShare: 50 }
          return {
            longShare: (b.long_notion / total) * 100,
            shortShare: (b.short_notion / total) * 100,
          }
        })
        setSeries(pts)
      } catch {
        // silent
      }
    }
    load()
    const timer = window.setInterval(load, 60000)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [traderId])

  // Two share lines on a fixed 0..100% scale (bounds always visible).
  const spark = useMemo(() => {
    if (series.length < 2) return null
    const w = 96
    const h = 28
    const x = (i: number) => (i / (series.length - 1)) * w
    const y = (pct: number) => h - (pct / 100) * h
    const line = (key: 'longShare' | 'shortShare') =>
      series
        .map((p, i) => `${x(i).toFixed(1)},${y(p[key]).toFixed(1)}`)
        .join(' ')
    return {
      w,
      h,
      long: line('longShare'),
      short: line('shortShare'),
      midY: y(50),
    }
  }, [series])

  const totalNotion = longNotion + shortNotion
  const longSharePct = totalNotion > 0 ? (longNotion / totalNotion) * 100 : 0
  const shortSharePct = totalNotion > 0 ? (shortNotion / totalNotion) * 100 : 0
  const fmtNotion = (v: number) =>
    v >= 1000 ? `$${(v / 1000).toFixed(1)}k` : `$${v.toFixed(0)}`

  return (
    <div className="group nofx-glass p-3 rounded-lg transition-all duration-300 hover:bg-white/5 hover:translate-y-[-2px] border border-white/5 hover:border-nofx-gold/20 relative overflow-hidden">
      <div className="text-[10px] mb-1 font-mono uppercase tracking-wider text-nofx-text-muted">
        {language === 'zh' ? '多空净值 (USDT)' : 'Long/Short Value'}
      </div>
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-baseline gap-2">
          <span
            className="text-sm font-bold font-mono text-nofx-green"
            title={language === 'zh' ? '做多名义价值' : 'Long notional'}
          >
            L {fmtNotion(longNotion)}
          </span>
          <span
            className="text-sm font-bold font-mono text-nofx-red"
            title={language === 'zh' ? '做空名义价值' : 'Short notional'}
          >
            S {fmtNotion(shortNotion)}
          </span>
        </div>
        {/* Two breadth circuit-breaker pressure gauges: velocity + from-peak.
            0 = no risk; 100 = that close condition reached the fire threshold. */}
        <div className="flex items-end gap-1.5">
          <BreadthGauge
            value={velIndex ?? 0}
            label="VEL"
            title={
              language === 'zh'
                ? `回撤熔断·速度路指数 ${Math.round(velIndex ?? 0)}/100（0=无风险，100=达平仓标准）`
                : `Breadth velocity-path index ${Math.round(velIndex ?? 0)}/100 (0=safe, 100=cut)`
            }
          />
          <BreadthGauge
            value={peakIndex ?? 0}
            label="PEAK"
            title={
              language === 'zh'
                ? `回撤熔断·回撤幅度路指数 ${Math.round(peakIndex ?? 0)}/100（0=无风险，100=达平仓标准）`
                : `Breadth from-peak-path index ${Math.round(peakIndex ?? 0)}/100 (0=safe, 100=cut)`
            }
          />
        </div>
      </div>
      <div className="flex items-center justify-between mt-1 gap-2">
        <div className="flex flex-col text-[10px] font-mono text-nofx-text-muted leading-tight">
          <span>
            {longCount}L / {shortCount}S
          </span>
          <span>
            <span className="text-nofx-green">{longSharePct.toFixed(0)}%</span>
            {' / '}
            <span className="text-nofx-red">{shortSharePct.toFixed(0)}%</span>
          </span>
        </div>
        {spark && (
          <div className="flex items-stretch gap-1">
            <div className="flex flex-col justify-between text-[8px] font-mono text-nofx-text-muted/60 leading-none py-0.5">
              <span>100</span>
              <span>0</span>
            </div>
            <svg width={spark.w} height={spark.h} aria-hidden>
              <line
                x1={0}
                y1={0}
                x2={spark.w}
                y2={0}
                stroke="rgba(255,255,255,0.12)"
              />
              <line
                x1={0}
                y1={spark.h}
                x2={spark.w}
                y2={spark.h}
                stroke="rgba(255,255,255,0.12)"
              />
              <line
                x1={0}
                y1={spark.midY}
                x2={spark.w}
                y2={spark.midY}
                stroke="rgba(255,255,255,0.1)"
                strokeDasharray="2 2"
              />
              <polyline
                points={spark.long}
                fill="none"
                stroke="#0ECB81"
                strokeWidth={1.2}
              />
              <polyline
                points={spark.short}
                fill="none"
                stroke="#F6465D"
                strokeWidth={1.2}
              />
            </svg>
          </div>
        )}
      </div>
    </div>
  )
}

// Stat Card Component - Deep Void Style
function StatCard({
  title,
  value,
  unit,
  change,
  positive,
  subtitle,
  icon,
}: {
  title: string
  value: string
  unit?: string
  change?: number
  positive?: boolean
  subtitle?: string
  icon?: string
}) {
  return (
    <div className="group nofx-glass p-3 rounded-lg transition-all duration-300 hover:bg-white/5 hover:translate-y-[-2px] border border-white/5 hover:border-nofx-gold/20 relative overflow-hidden">
      <div className="absolute top-0 right-0 p-3 opacity-5 group-hover:opacity-10 transition-opacity text-3xl grayscale group-hover:grayscale-0">
        {icon}
      </div>
      <div className="text-[10px] mb-1 font-mono uppercase tracking-wider text-nofx-text-muted flex items-center gap-2">
        {title}
      </div>
      <div className="flex items-baseline gap-1">
        <div className="text-xl font-bold font-mono text-nofx-text-main tracking-tight group-hover:text-white transition-colors">
          {value}
        </div>
        {unit && (
          <span className="text-[10px] font-mono text-nofx-text-muted opacity-60">
            {unit}
          </span>
        )}
      </div>

      {change !== undefined && (
        <div className="flex items-center gap-1">
          <div
            className={`text-xs mono font-bold flex items-center gap-1 ${positive ? 'text-nofx-green' : 'text-nofx-red'}`}
          >
            <span>{positive ? '▲' : '▼'}</span>
            <span>
              {positive ? '+' : ''}
              {change.toFixed(2)}%
            </span>
          </div>
        </div>
      )}
      {subtitle && (
        <div className="text-[10px] mt-1 mono text-nofx-text-muted opacity-80">
          {subtitle}
        </div>
      )}
    </div>
  )
}

function SystemHealthCard({
  decisions,
  language,
}: {
  decisions: DecisionRecord[] | undefined
  language: string
}) {
  const { score, label, color } = useMemo(() => {
    if (!decisions || decisions.length === 0) {
      return {
        score: 50,
        label: language === 'zh' ? '无数据' : 'No data',
        color: '#848E9C',
      }
    }

    let wins = 0
    let total = 0
    let consecutiveLosses = 0
    let maxConsecutiveLosses = 0
    let apiErrors = 0

    for (const d of decisions) {
      for (const a of d.decisions || []) {
        if (a.action.includes('open')) {
          if (a.success) {
            wins++
            total++
            consecutiveLosses = 0
          } else if (a.review_context?.control?.decision === 'rejected') {
            // Gate rejections are normal system behavior, not failures
          } else if (a.error) {
            // Only count actual execution failures (API errors, order rejected by exchange)
            if (
              a.error.includes('API') ||
              a.error.includes('429') ||
              a.error.includes('timeout')
            ) {
              apiErrors++
            } else {
              total++
              consecutiveLosses++
              maxConsecutiveLosses = Math.max(
                maxConsecutiveLosses,
                consecutiveLosses
              )
            }
          }
        }
      }
    }

    // Score: base from execution success rate, penalty for consecutive failures
    let s: number
    if (total === 0 && apiErrors === 0) {
      // No executed trades — system is observing, not unhealthy
      s = 80
    } else if (total === 0 && apiErrors > 0) {
      // Only API errors — connectivity issue
      s = 60 - apiErrors * 5
    } else {
      const winRate = wins / total
      s = Math.round(winRate * 80)
      s -= maxConsecutiveLosses * 5
      s += 20
    }
    s = Math.max(0, Math.min(100, s))

    let lbl: string
    let clr: string
    if (s >= 70) {
      lbl = language === 'zh' ? '健康' : 'Healthy'
      clr = '#0ECB81'
    } else if (s >= 50) {
      lbl = language === 'zh' ? '一般' : 'Fair'
      clr = '#F0B90B'
    } else {
      lbl = language === 'zh' ? '注意' : 'Caution'
      clr = '#F6465D'
    }

    return { score: s, label: lbl, color: clr }
  }, [decisions, language])

  return (
    <div className="group nofx-glass p-3 rounded-lg transition-all duration-300 hover:bg-white/5 hover:translate-y-[-2px] border border-white/5 hover:border-nofx-gold/20 relative overflow-hidden">
      <div className="absolute top-0 right-0 p-3 opacity-5 group-hover:opacity-10 transition-opacity text-3xl grayscale group-hover:grayscale-0">
        🧬
      </div>
      <div className="text-[10px] mb-1 font-mono uppercase tracking-wider text-nofx-text-muted flex items-center gap-2">
        {language === 'zh' ? '系统健康' : 'HEALTH'}
      </div>
      <div className="flex items-baseline gap-1">
        <div
          className="text-xl font-bold font-mono tracking-tight transition-colors"
          style={{ color }}
        >
          {score}
        </div>
        <span className="text-xs font-mono text-nofx-text-muted opacity-60">
          /100
        </span>
      </div>
      <div className="text-xs mt-2 mono opacity-80" style={{ color }}>
        {label}
      </div>
    </div>
  )
}

function CoinProfileSummary({
  traderId,
  symbol,
}: {
  traderId: string
  symbol: string
}) {
  const [summary, setSummary] = useState<string | null>(null)

  useEffect(() => {
    if (!traderId || !symbol) {
      setSummary(null)
      return
    }
    api
      .getEvolutionProfiles(traderId)
      .then((profiles) => {
        const matching = profiles.filter(
          (p) =>
            p.symbol === symbol ||
            p.symbol === symbol.replace('USDT', '') + 'USDT'
        )
        if (matching.length === 0) {
          setSummary(null)
          return
        }

        const parts: string[] = []
        for (const p of matching) {
          const avgScore =
            p.factors?.length > 0
              ? Math.round(
                  p.factors.reduce((s, f) => s + f.score, 0) / p.factors.length
                )
              : 0
          const topInsight = p.factors?.find(
            (f) =>
              f.insight && f.sample_size >= 5 && (f.score < 35 || f.score > 65)
          )
          let line = `${p.side.toUpperCase()} ${avgScore}/100`
          if (topInsight?.insight) line += ` · ${topInsight.insight}`
          if (p.adaptations?.length > 0)
            line += ` · ${p.adaptations.length}个调整`
          parts.push(line)
        }
        setSummary(parts.join(' | '))
      })
      .catch(() => setSummary(null))
  }, [traderId, symbol])

  if (!summary) return null

  return (
    <div
      className="mt-2 px-3 py-2 rounded-lg text-[11px] text-nofx-text-muted"
      style={{
        background: 'rgba(99,102,241,0.05)',
        border: '1px solid rgba(99,102,241,0.15)',
      }}
    >
      <span className="text-indigo-300 font-medium mr-2">
        🧬 {symbol.replace('USDT', '')}
      </span>
      {summary}
    </div>
  )
}
