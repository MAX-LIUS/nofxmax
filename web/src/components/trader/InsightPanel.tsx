import { useState, useMemo } from 'react'
import type { DecisionRecord, DecisionAction } from '../../types/trading'

// traderId was dropped with the evolution tab (2026-08-04): the remaining tabs
// derive everything from the `decisions` prop, so no fetch is needed here.
interface InsightPanelProps {
  decisions: DecisionRecord[]
  language: string
}

// (removed 2026-08-04) the 'evolution' tab went with the coin evolution engine.
type TabKey = 'actions' | 'gates'

export function InsightPanel({ decisions, language }: InsightPanelProps) {
  const [activeTab, setActiveTab] = useState<TabKey>('actions')
  const [expanded, setExpanded] = useState(false)

  const { actionableDecisions, gateBlocks, todayStats } = useMemo(() => {
    const now = new Date()
    const todayStart = new Date(
      now.getFullYear(),
      now.getMonth(),
      now.getDate()
    )

    const actionable: {
      action: DecisionAction
      cycle: number
      timestamp: string
    }[] = []
    const blocks: {
      action: DecisionAction
      cycle: number
      timestamp: string
    }[] = []
    let todayTrades = 0
    let todayWins = 0

    for (const d of decisions || []) {
      for (const a of d.decisions || []) {
        const isOpen = a.action.includes('open')
        const isClose = a.action.includes('close')
        const isRejected = a.review_context?.control?.decision === 'rejected'

        if (isOpen || isClose) {
          actionable.push({
            action: a,
            cycle: d.cycle_number,
            timestamp: d.timestamp,
          })
          if (new Date(d.timestamp) >= todayStart) {
            todayTrades++
            if (a.success) todayWins++
          }
        }
        if (isRejected) {
          blocks.push({
            action: a,
            cycle: d.cycle_number,
            timestamp: d.timestamp,
          })
        }
      }
    }

    return {
      actionableDecisions: actionable.slice(0, 20),
      gateBlocks: blocks.slice(0, 20),
      todayStats: {
        trades: todayTrades,
        wins: todayWins,
        winRate:
          todayTrades > 0 ? Math.round((todayWins / todayTrades) * 100) : 0,
      },
    }
  }, [decisions])

  const summaryText =
    language === 'zh'
      ? `今日 ${todayStats.trades} 笔交易，胜率 ${todayStats.winRate}%，拦截 ${gateBlocks.length} 笔`
      : `Today: ${todayStats.trades} trades, ${todayStats.winRate}% win rate, ${gateBlocks.length} blocked`

  const tabs: { key: TabKey; label: string; count: number }[] = [
    {
      key: 'actions',
      label: language === 'zh' ? '活跃决策' : 'Actions',
      count: actionableDecisions.length,
    },
    {
      key: 'gates',
      label: language === 'zh' ? 'Gate拦截' : 'Blocked',
      count: gateBlocks.length,
    },
  ]

  return (
    <div
      className="nofx-glass p-4 mb-6 animate-slide-in"
      style={{ animationDelay: '0.18s' }}
    >
      {/* Collapsed summary */}
      <div
        className="flex items-center gap-3 cursor-pointer select-none"
        onClick={() => setExpanded(!expanded)}
      >
        <div
          className="w-8 h-8 rounded-lg flex items-center justify-center text-base"
          style={{
            background: 'linear-gradient(135deg, #10B981 0%, #059669 100%)',
          }}
        >
          📊
        </div>
        <div className="flex-1">
          <span className="text-sm font-medium text-nofx-text-main">
            {language === 'zh' ? '智能洞察' : 'Smart Insights'}
          </span>
          <span className="text-xs text-nofx-text-muted ml-3">
            {summaryText}
          </span>
        </div>
        <span className="text-nofx-text-muted text-xs">
          {expanded ? '▲' : '▼'}
        </span>
      </div>

      {/* Expanded content */}
      {expanded && (
        <div className="mt-4 pt-3 border-t border-white/5">
          {/* Tab bar */}
          <div className="flex gap-1 mb-3">
            {tabs.map((tab) => (
              <button
                key={tab.key}
                onClick={() => setActiveTab(tab.key)}
                className="px-3 py-1.5 rounded-md text-xs font-medium transition-all"
                style={{
                  background:
                    activeTab === tab.key
                      ? 'rgba(16, 185, 129, 0.15)'
                      : 'transparent',
                  color: activeTab === tab.key ? '#34D399' : '#848E9C',
                  border:
                    activeTab === tab.key
                      ? '1px solid rgba(16, 185, 129, 0.3)'
                      : '1px solid transparent',
                }}
              >
                {tab.label}
                {tab.count > 0 && (
                  <span className="ml-1 opacity-60">({tab.count})</span>
                )}
              </button>
            ))}
          </div>

          {/* Tab content */}
          <div className="max-h-[300px] overflow-y-auto custom-scrollbar">
            {activeTab === 'actions' && (
              <ActionsTab items={actionableDecisions} language={language} />
            )}
            {activeTab === 'gates' && (
              <GatesTab items={gateBlocks} language={language} />
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function ActionsTab({
  items,
  language,
}: {
  items: { action: DecisionAction; cycle: number; timestamp: string }[]
  language: string
}) {
  if (items.length === 0) {
    return (
      <div className="text-xs text-nofx-text-muted py-4 text-center">
        {language === 'zh' ? '暂无活跃决策' : 'No recent actions'}
      </div>
    )
  }
  return (
    <div className="space-y-1.5">
      {items.map((item, i) => {
        const a = item.action
        const isOpen = a.action.includes('open')
        const isLong = a.action.includes('long')
        const actionColor = isLong ? '#10B981' : '#EF4444'
        const actionLabel = isOpen
          ? isLong
            ? 'OPEN LONG'
            : 'OPEN SHORT'
          : 'CLOSE'
        const time = new Date(item.timestamp).toLocaleTimeString([], {
          hour: '2-digit',
          minute: '2-digit',
        })

        return (
          <div
            key={i}
            className="flex items-center gap-2 py-1.5 px-2 rounded-md hover:bg-white/5"
          >
            <span className="text-[10px] text-nofx-text-muted w-10">
              {time}
            </span>
            <span
              className="text-[10px] font-mono px-1.5 py-0.5 rounded"
              style={{ background: `${actionColor}20`, color: actionColor }}
            >
              {actionLabel}
            </span>
            <span className="text-xs font-medium text-nofx-text-main">
              {a.symbol}
            </span>
            {a.confidence && (
              <span className="text-[10px] text-nofx-text-muted">
                conf:{a.confidence}
              </span>
            )}
            {a.success ? (
              <span className="text-[10px] text-green-400 ml-auto">✓</span>
            ) : a.error ? (
              <span className="text-[10px] text-red-400 ml-auto truncate max-w-[120px]">
                {a.error.slice(0, 30)}
              </span>
            ) : null}
          </div>
        )
      })}
    </div>
  )
}

// (removed 2026-08-04) EvolutionTab rendered the per-coin evolution profiles.

function GatesTab({
  items,
  language,
}: {
  items: { action: DecisionAction; cycle: number; timestamp: string }[]
  language: string
}) {
  if (items.length === 0) {
    return (
      <div className="text-xs text-nofx-text-muted py-4 text-center">
        {language === 'zh' ? '近期无拦截' : 'No recent blocks'}
      </div>
    )
  }
  return (
    <div className="space-y-1.5">
      {items.map((item, i) => {
        const a = item.action
        const control = a.review_context?.control
        const time = new Date(item.timestamp).toLocaleTimeString([], {
          hour: '2-digit',
          minute: '2-digit',
        })
        const reason =
          control?.reasons?.[0] || control?.failed_checks?.[0] || 'unknown'

        return (
          <div key={i} className="py-1.5 px-2 rounded-md hover:bg-white/5">
            <div className="flex items-center gap-2">
              <span className="text-[10px] text-nofx-text-muted w-10">
                {time}
              </span>
              <span className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-red-500/10 text-red-400">
                🚫 {a.symbol}
              </span>
              <span className="text-[10px] text-nofx-text-muted">
                {a.action}
              </span>
            </div>
            <div className="text-[10px] text-nofx-text-muted mt-0.5 pl-12 truncate">
              {reason}
            </div>
          </div>
        )
      })}
    </div>
  )
}

// (removed 2026-08-04) formatTimeAgo was only used by EvolutionTab.
