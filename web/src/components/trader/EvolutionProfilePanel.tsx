import { useState, useEffect } from 'react'
import { api } from '../../lib/api'
import { FactorRadarChart } from './FactorRadarChart'
import type { EvolutionProfile, EvolutionFactor } from '../../types'

interface EvolutionProfilePanelProps {
  traderId: string
}

const factorLabels: Record<string, string> = {
  trend_phase_fit: '趋势阶段适配',
  ema20_alignment: 'EMA20方向一致性',
  trigger_quality: 'Trigger质量',
  momentum_sweet_spot: '动量甜蜜区',
  hold_duration_fit: '持仓时长适配',
  protection_effectiveness: '保护有效性',
  time_of_day: '时段偏好',
  volatility_regime: '波动率适配',
}

function scoreColor(score: number): string {
  if (score >= 65) return '#0ECB81'
  if (score >= 50) return '#F0B90B'
  if (score >= 35) return '#F6465D'
  return '#FF4444'
}

function confidenceBadge(confidence: number): string {
  if (confidence >= 0.8) return '高'
  if (confidence >= 0.5) return '中'
  return '低'
}

function formatTimeAgo(unixMs: number): string {
  if (!unixMs) return '-'
  const diff = Date.now() - unixMs
  const hours = Math.floor(diff / 3600000)
  if (hours < 1) return '刚刚'
  if (hours < 24) return `${hours}h前`
  const days = Math.floor(hours / 24)
  return `${days}d前`
}

export function EvolutionProfilePanel({
  traderId,
}: EvolutionProfilePanelProps) {
  const [profiles, setProfiles] = useState<EvolutionProfile[]>([])
  const [loading, setLoading] = useState(true)
  const [expanded, setExpanded] = useState(false)
  const [selectedSymbol, setSelectedSymbol] = useState<string | null>(null)

  useEffect(() => {
    if (!traderId) return
    setLoading(true)
    api
      .getEvolutionProfiles(traderId)
      .then((data) => {
        setProfiles(data || [])
      })
      .catch(() => setProfiles([]))
      .finally(() => setLoading(false))
  }, [traderId])

  if (loading) return null
  if (profiles.length === 0) return null

  // Group by symbol
  const symbolMap = new Map<string, EvolutionProfile[]>()
  for (const p of profiles) {
    const existing = symbolMap.get(p.symbol) || []
    existing.push(p)
    symbolMap.set(p.symbol, existing)
  }

  const symbols = Array.from(symbolMap.keys()).sort()
  const activeAdaptations = profiles.reduce(
    (sum, p) => sum + (p.adaptations?.length || 0),
    0
  )

  return (
    <div className="mt-4">
      {/* Collapsed summary */}
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full flex items-center gap-3 px-4 py-3 rounded-xl transition-all hover:bg-white/5"
        style={{
          background: expanded ? 'rgba(99,102,241,0.08)' : 'rgba(0,0,0,0.2)',
          border: `1px solid ${expanded ? 'rgba(99,102,241,0.3)' : 'rgba(255,255,255,0.05)'}`,
        }}
      >
        <span className="text-lg">🧬</span>
        <span className="text-sm font-medium text-nofx-text-main">
          进化画像
        </span>
        <span className="text-xs text-nofx-text-muted">
          {symbols.length} 币种 · {activeAdaptations} 活跃调整
        </span>
        <span className="ml-auto text-xs text-nofx-text-muted">
          {expanded ? '▼' : '▶'}
        </span>
      </button>

      {/* Expanded panel */}
      {expanded && (
        <div
          className="mt-2 rounded-xl p-4"
          style={{
            background: 'rgba(0,0,0,0.3)',
            border: '1px solid rgba(255,255,255,0.05)',
          }}
        >
          {/* Symbol selector */}
          <div className="flex flex-wrap gap-2 mb-4">
            {symbols.map((sym) => (
              <button
                key={sym}
                onClick={() =>
                  setSelectedSymbol(selectedSymbol === sym ? null : sym)
                }
                className="px-3 py-1 rounded-lg text-xs font-medium transition-all"
                style={{
                  background:
                    selectedSymbol === sym
                      ? 'rgba(99,102,241,0.2)'
                      : 'rgba(255,255,255,0.05)',
                  color: selectedSymbol === sym ? '#818CF8' : '#848E9C',
                  border: `1px solid ${selectedSymbol === sym ? 'rgba(99,102,241,0.4)' : 'rgba(255,255,255,0.1)'}`,
                }}
              >
                {sym.replace('USDT', '')}
              </button>
            ))}
          </div>

          {/* Profile details */}
          {selectedSymbol &&
            symbolMap.get(selectedSymbol)?.map((profile) => (
              <div
                key={`${profile.symbol}-${profile.side}`}
                className="mb-4 p-3 rounded-lg"
                style={{
                  background: 'rgba(255,255,255,0.02)',
                  border: '1px solid rgba(255,255,255,0.05)',
                }}
              >
                <div className="flex items-center gap-2 mb-3">
                  <span
                    className="px-2 py-0.5 rounded text-xs font-semibold uppercase"
                    style={{
                      color: profile.side === 'long' ? '#0ECB81' : '#F6465D',
                      background:
                        profile.side === 'long'
                          ? 'rgba(14,203,129,0.15)'
                          : 'rgba(246,70,93,0.15)',
                    }}
                  >
                    {profile.side}
                  </span>
                  <span className="text-xs text-nofx-text-muted">
                    样本 {profile.sample_size} 笔 · v{profile.version} · 更新{' '}
                    {formatTimeAgo(profile.updated_at)}
                  </span>
                  <button
                    onClick={async (e) => {
                      e.stopPropagation()
                      if (
                        !confirm(
                          `确认重置 ${profile.symbol} ${profile.side} 的进化画像？将从历史数据重新学习。`
                        )
                      )
                        return
                      const ok = await api.resetEvolutionProfile(
                        traderId,
                        profile.symbol,
                        profile.side
                      )
                      if (ok) {
                        setProfiles(
                          profiles.filter(
                            (p) =>
                              !(
                                p.symbol === profile.symbol &&
                                p.side === profile.side
                              )
                          )
                        )
                      }
                    }}
                    className="ml-auto text-[10px] px-2 py-0.5 rounded text-nofx-text-muted hover:text-red-400 hover:bg-red-500/10 transition-all"
                    title="重置画像，从历史数据重新学习"
                  >
                    重置
                  </button>
                </div>

                {/* Radar chart */}
                {profile.factors && profile.factors.length >= 3 && (
                  <FactorRadarChart
                    factors={profile.factors.map((f: EvolutionFactor) => ({
                      name: f.name,
                      score: f.score,
                      label: (factorLabels[f.name] || f.name).slice(0, 4),
                    }))}
                    size={180}
                  />
                )}

                {/* Factor scores */}
                <div className="grid grid-cols-2 gap-2 mb-3">
                  {(profile.factors || []).map((factor: EvolutionFactor) => (
                    <div key={factor.name} className="flex items-center gap-2">
                      <div className="flex-1">
                        <div className="flex items-center justify-between mb-0.5">
                          <span className="text-[10px] text-nofx-text-muted">
                            {factorLabels[factor.name] || factor.name}
                          </span>
                          <span
                            className="text-[10px] font-mono font-medium"
                            style={{ color: scoreColor(factor.score) }}
                          >
                            {factor.score.toFixed(0)}
                          </span>
                        </div>
                        <div
                          className="h-1.5 rounded-full overflow-hidden"
                          style={{ background: 'rgba(255,255,255,0.05)' }}
                        >
                          <div
                            className="h-full rounded-full transition-all"
                            style={{
                              width: `${Math.min(factor.score, 100)}%`,
                              background: scoreColor(factor.score),
                              opacity: factor.sample_size >= 5 ? 1 : 0.4,
                            }}
                          />
                        </div>
                      </div>
                      <span
                        className="text-[9px] px-1 rounded"
                        style={{
                          color: '#848E9C',
                          background: 'rgba(255,255,255,0.05)',
                        }}
                      >
                        {confidenceBadge(factor.confidence)}
                      </span>
                    </div>
                  ))}
                </div>

                {/* Insights */}
                {(profile.factors || [])
                  .filter(
                    (f: EvolutionFactor) =>
                      f.insight &&
                      f.sample_size >= 5 &&
                      (f.score < 35 || f.score > 65)
                  )
                  .map((f: EvolutionFactor) => (
                    <div
                      key={f.name}
                      className="text-[11px] text-nofx-text-muted mb-1 pl-2"
                      style={{
                        borderLeft: `2px solid ${scoreColor(f.score)}33`,
                      }}
                    >
                      {f.insight}
                    </div>
                  ))}

                {/* Active adaptations */}
                {profile.adaptations && profile.adaptations.length > 0 && (
                  <div className="mt-3 pt-2 border-t border-white/5">
                    <div className="text-[10px] text-nofx-text-muted mb-1 font-medium">
                      活跃调整:
                    </div>
                    {profile.adaptations.map((a, i) => (
                      <div
                        key={i}
                        className="text-[11px] flex items-center gap-2 mb-1"
                      >
                        <span
                          className="px-1.5 py-0.5 rounded text-[9px] font-mono"
                          style={{
                            background: 'rgba(246,70,93,0.1)',
                            color: '#F6465D',
                            border: '1px solid rgba(246,70,93,0.2)',
                          }}
                        >
                          {a.condition}
                        </span>
                        <span className="text-nofx-text-muted">→</span>
                        <span className="text-nofx-text-main">{a.action}</span>
                        {a.expires_at > 0 && (
                          <span className="text-[9px] text-nofx-text-muted ml-auto">
                            过期: {formatTimeAgo(a.expires_at)}
                          </span>
                        )}
                      </div>
                    ))}
                  </div>
                )}

                {/* Timeline — adaptation history */}
                {profile.adaptations && profile.adaptations.length > 0 && (
                  <div className="mt-2 pt-2 border-t border-white/5">
                    <div className="text-[10px] text-nofx-text-muted mb-1 font-medium">
                      变更时间线:
                    </div>
                    <div className="space-y-0.5">
                      {[...profile.adaptations]
                        .sort(
                          (a, b) => (b.created_at || 0) - (a.created_at || 0)
                        )
                        .slice(0, 5)
                        .map((a, i) => (
                          <div
                            key={i}
                            className="flex items-center gap-2 text-[10px]"
                          >
                            <span className="w-1.5 h-1.5 rounded-full bg-indigo-400 flex-shrink-0" />
                            <span className="text-nofx-text-muted">
                              {a.created_at ? formatTimeAgo(a.created_at) : '—'}
                            </span>
                            <span className="text-nofx-text-main">
                              {a.condition}→{a.action}
                            </span>
                            {a.contradictions > 0 && (
                              <span className="text-amber-400 text-[9px]">
                                ⚠{a.contradictions}次矛盾
                              </span>
                            )}
                          </div>
                        ))}
                    </div>
                  </div>
                )}
              </div>
            ))}

          {/* No selection prompt */}
          {!selectedSymbol && (
            <div className="text-center text-sm text-nofx-text-muted py-4">
              选择币种查看详细进化画像
            </div>
          )}
        </div>
      )}
    </div>
  )
}
