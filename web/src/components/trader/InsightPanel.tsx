import { useState, useMemo, useEffect } from 'react'
import { api } from '../../lib/api'
import type {
  DecisionRecord,
  DecisionAction,
  EvolutionProfile,
} from '../../types/trading'

interface InsightPanelProps {
  traderId: string
  decisions: DecisionRecord[]
  language: string
}

type TabKey = 'actions' | 'evolution' | 'gates'

export function InsightPanel({
  traderId,
  decisions,
  language,
}: InsightPanelProps) {
  const [activeTab, setActiveTab] = useState<TabKey>('actions')
  const [expanded, setExpanded] = useState(false)
  const [evolutionProfiles, setEvolutionProfiles] = useState<
    EvolutionProfile[]
  >([])

  useEffect(() => {
    if (!traderId) return
    api
      .getEvolutionProfiles(traderId)
      .then(setEvolutionProfiles)
      .catch(() => {})
  }, [traderId])

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
      key: 'evolution',
      label: language === 'zh' ? '进化洞察' : 'Evolution',
      count: evolutionProfiles.length,
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
            {activeTab === 'evolution' && (
              <EvolutionTab profiles={evolutionProfiles} language={language} />
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

function EvolutionTab({
  profiles,
  language,
}: {
  profiles: EvolutionProfile[]
  language: string
}) {
  if (!profiles || profiles.length === 0) {
    return (
      <div className="text-xs text-nofx-text-muted py-4 text-center">
        {language === 'zh'
          ? '进化引擎尚未生成画像'
          : 'No evolution profiles yet'}
      </div>
    )
  }

  const sortedProfiles = [...profiles].sort(
    (a, b) => b.updated_at - a.updated_at
  )

  return (
    <div className="space-y-2">
      {sortedProfiles.slice(0, 8).map((profile, i) => {
        const updatedAgo = formatTimeAgo(profile.updated_at, language)
        const topInsights = (profile.factors || [])
          .filter(
            (f) =>
              f.insight && f.sample_size >= 5 && (f.score < 35 || f.score > 65)
          )
          .slice(0, 2)

        return (
          <div
            key={i}
            className="py-2 px-2 rounded-md bg-white/[0.02] border border-white/5"
          >
            <div className="flex items-center gap-2">
              <span className="text-xs font-medium text-nofx-text-main">
                {profile.symbol} {profile.side.toUpperCase()}
              </span>
              <span className="text-[10px] text-nofx-text-muted">
                v{profile.version}
              </span>
              <span className="text-[10px] text-nofx-text-muted ml-auto">
                {updatedAgo}
              </span>
            </div>
            {topInsights.map((f, j) => (
              <div
                key={j}
                className="text-[10px] text-nofx-text-muted mt-1 pl-2 border-l border-white/10"
              >
                {f.insight}
              </div>
            ))}
            {(profile.adaptations || []).length > 0 && (
              <div className="text-[10px] mt-1 pl-2 border-l border-amber-500/30 text-amber-400/80">
                {(profile.adaptations || [])
                  .map((a) => `${a.condition}→${a.action}`)
                  .join('; ')}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

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

function formatTimeAgo(unixMs: number, language: string): string {
  const diff = Date.now() - unixMs
  const hours = Math.floor(diff / 3600000)
  const days = Math.floor(hours / 24)

  if (days > 0) return language === 'zh' ? `${days}天前` : `${days}d ago`
  if (hours > 0) return language === 'zh' ? `${hours}小时前` : `${hours}h ago`
  return language === 'zh' ? '刚刚' : 'just now'
}
