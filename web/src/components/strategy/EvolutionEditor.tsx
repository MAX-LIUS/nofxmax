import { useState, useEffect } from 'react'
import { api } from '../../lib/api'
import type { EvolutionConfig } from '../../types/strategy'
import type { EvolutionProfile } from '../../types/trading'

interface EvolutionEditorProps {
  config?: EvolutionConfig
  onChange: (config: EvolutionConfig) => void
  disabled?: boolean
  strategyId?: string
}

export function EvolutionEditor({
  config,
  onChange,
  disabled,
  strategyId,
}: EvolutionEditorProps) {
  const [profiles, setProfiles] = useState<EvolutionProfile[]>([])
  const [loadingProfiles, setLoadingProfiles] = useState(false)

  const current: EvolutionConfig = config || {
    enabled: false,
    half_life_days: 14,
    min_sample_size: 5,
    adaptation_ttl_days: 30,
    score_threshold_low: 35,
    score_threshold_high: 65,
    inject_to_prompt: true,
  }

  // Fetch profiles for the trader using this strategy
  useEffect(() => {
    if (!strategyId || !current.enabled) return
    setLoadingProfiles(true)
    // Find traders using this strategy via the traders list
    api
      .getTraders()
      .then(async (traders) => {
        const matching = traders.find((t: any) => t.strategy_id === strategyId)
        if (matching) {
          const profs = await api.getEvolutionProfiles(matching.trader_id)
          setProfiles(profs || [])
        }
      })
      .catch(() => {})
      .finally(() => setLoadingProfiles(false))
  }, [strategyId, current.enabled])

  const update = (patch: Partial<EvolutionConfig>) => {
    onChange({ ...current, ...patch })
  }

  // Collect all active adaptations across all profiles
  const allAdaptations = profiles.flatMap((p) =>
    (p.adaptations || []).map((a) => ({ ...a, symbol: p.symbol, side: p.side }))
  )

  return (
    <div className="space-y-4">
      {/* Master switch */}
      <div className="flex items-center justify-between">
        <div>
          <div className="text-sm font-medium text-nofx-text-main">
            启用进化引擎
          </div>
          <div className="text-xs text-nofx-text-muted">
            基于历史交易自动学习，为每个币种生成个性化交易方案
          </div>
        </div>
        <button
          onClick={() => update({ enabled: !current.enabled })}
          disabled={disabled}
          className={`relative w-11 h-6 rounded-full transition-colors ${
            current.enabled ? 'bg-indigo-500' : 'bg-gray-600'
          } ${disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}`}
        >
          <span
            className={`absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white transition-transform ${
              current.enabled ? 'translate-x-5' : ''
            }`}
          />
        </button>
      </div>

      {current.enabled && (
        <>
          {/* Inject to prompt */}
          <div className="flex items-center justify-between">
            <div>
              <div className="text-sm font-medium text-nofx-text-main">
                注入AI Prompt
              </div>
              <div className="text-xs text-nofx-text-muted">
                将进化画像摘要注入AI决策上下文
              </div>
            </div>
            <button
              onClick={() =>
                update({ inject_to_prompt: !current.inject_to_prompt })
              }
              disabled={disabled}
              className={`relative w-11 h-6 rounded-full transition-colors ${
                current.inject_to_prompt ? 'bg-indigo-500' : 'bg-gray-600'
              } ${disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'}`}
            >
              <span
                className={`absolute top-0.5 left-0.5 w-5 h-5 rounded-full bg-white transition-transform ${
                  current.inject_to_prompt ? 'translate-x-5' : ''
                }`}
              />
            </button>
          </div>

          {/* Parameters */}
          <div className="grid grid-cols-2 gap-4">
            <div>
              <label className="text-xs text-nofx-text-muted block mb-1">
                半衰期（天）
              </label>
              <input
                type="number"
                value={current.half_life_days || 14}
                onChange={(e) =>
                  update({ half_life_days: Number(e.target.value) })
                }
                disabled={disabled}
                min={3}
                max={60}
                className="w-full px-3 py-1.5 rounded-lg bg-black/40 border border-white/10 text-sm text-nofx-text-main focus:outline-none focus:border-indigo-500"
              />
              <div className="text-[10px] text-nofx-text-muted mt-0.5">
                越短越重视近期数据
              </div>
            </div>
            <div>
              <label className="text-xs text-nofx-text-muted block mb-1">
                最小样本量
              </label>
              <input
                type="number"
                value={current.min_sample_size || 5}
                onChange={(e) =>
                  update({ min_sample_size: Number(e.target.value) })
                }
                disabled={disabled}
                min={3}
                max={20}
                className="w-full px-3 py-1.5 rounded-lg bg-black/40 border border-white/10 text-sm text-nofx-text-main focus:outline-none focus:border-indigo-500"
              />
              <div className="text-[10px] text-nofx-text-muted mt-0.5">
                低于此不生成调整方案
              </div>
            </div>
            <div>
              <label className="text-xs text-nofx-text-muted block mb-1">
                调整过期（天）
              </label>
              <input
                type="number"
                value={current.adaptation_ttl_days || 30}
                onChange={(e) =>
                  update({ adaptation_ttl_days: Number(e.target.value) })
                }
                disabled={disabled}
                min={7}
                max={90}
                className="w-full px-3 py-1.5 rounded-lg bg-black/40 border border-white/10 text-sm text-nofx-text-main focus:outline-none focus:border-indigo-500"
              />
              <div className="text-[10px] text-nofx-text-muted mt-0.5">
                超过此天数自动失效
              </div>
            </div>
            <div>
              <label className="text-xs text-nofx-text-muted block mb-1">
                评分阈值（低/高）
              </label>
              <div className="flex gap-2">
                <input
                  type="number"
                  value={current.score_threshold_low || 35}
                  onChange={(e) =>
                    update({ score_threshold_low: Number(e.target.value) })
                  }
                  disabled={disabled}
                  min={10}
                  max={50}
                  className="w-full px-3 py-1.5 rounded-lg bg-black/40 border border-white/10 text-sm text-nofx-text-main focus:outline-none focus:border-indigo-500"
                />
                <input
                  type="number"
                  value={current.score_threshold_high || 65}
                  onChange={(e) =>
                    update({ score_threshold_high: Number(e.target.value) })
                  }
                  disabled={disabled}
                  min={50}
                  max={90}
                  className="w-full px-3 py-1.5 rounded-lg bg-black/40 border border-white/10 text-sm text-nofx-text-main focus:outline-none focus:border-indigo-500"
                />
              </div>
              <div className="text-[10px] text-nofx-text-muted mt-0.5">
                低于左值触发调整，高于右值为正面信号
              </div>
            </div>
          </div>

          {/* Global adaptations preview */}
          {allAdaptations.length > 0 && (
            <div
              className="p-3 rounded-lg"
              style={{
                background: 'rgba(0,0,0,0.3)',
                border: '1px solid rgba(255,255,255,0.05)',
              }}
            >
              <div className="text-xs font-medium text-nofx-text-main mb-2">
                全局活跃调整 ({allAdaptations.length})
              </div>
              <div className="space-y-1 max-h-[200px] overflow-y-auto custom-scrollbar">
                {allAdaptations.map((a, i) => (
                  <div key={i} className="flex items-center gap-2 text-[11px]">
                    <span className="text-nofx-text-main font-medium w-16">
                      {a.symbol.replace('USDT', '')}{' '}
                      {a.side.toUpperCase().slice(0, 1)}
                    </span>
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
                    <span className="text-nofx-text-main text-[10px]">
                      {a.action}
                    </span>
                    {a.contradictions > 0 && (
                      <span className="text-amber-400 text-[9px] ml-auto">
                        ⚠{a.contradictions}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
          {loadingProfiles && (
            <div className="text-xs text-nofx-text-muted text-center py-2">
              加载调整方案...
            </div>
          )}

          {/* Info box */}
          <div
            className="p-3 rounded-lg text-xs text-nofx-text-muted"
            style={{
              background: 'rgba(99,102,241,0.05)',
              border: '1px solid rgba(99,102,241,0.15)',
            }}
          >
            <div className="font-medium text-indigo-300 mb-1">
              进化引擎工作原理
            </div>
            <ul className="space-y-0.5 list-disc list-inside">
              <li>
                每笔交易关闭后，自动分析入场场景（趋势阶段、EMA20方向、动量等）
              </li>
              <li>为每个币种+方向计算多维度因素评分（0-100）</li>
              <li>
                评分偏离中性区时，自动生成个性化调整方案（非禁止，而是调参）
              </li>
              <li>高评分因素会放宽要求（如降低confidence门槛、增加仓位）</li>
              <li>旧数据按半衰期衰减，调整方案到期自动失效，避免信息臃肿</li>
            </ul>
          </div>
        </>
      )}
    </div>
  )
}
