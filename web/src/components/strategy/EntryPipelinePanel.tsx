import { ArrowDown, CheckCircle2, Circle } from 'lucide-react'
import type {
  RiskControlConfig,
  RegimeFilterConfig,
} from '../../types/strategy'
import {
  buildEntryPipeline,
  STAGE_META,
  type PipelineStage,
} from './entryPipeline'

interface Props {
  riskControl?: RiskControlConfig
  regimeFilter?: RegimeFilterConfig
  language: string
}

// EntryPipelinePanel renders the entry-decision gauntlet in the backend's actual
// evaluation order, consolidating controls that are otherwise scattered across the
// risk_control and regime_filter editors. Read-only: it reflects current config so the
// operator can see the full ordered pipeline a candidate trade must pass.
export function EntryPipelinePanel({
  riskControl,
  regimeFilter,
  language,
}: Props) {
  const isZh = language === 'zh'
  const gates = buildEntryPipeline(riskControl, regimeFilter)
  const stages: PipelineStage[] = [
    'market_state',
    'structural_fit',
    'confidence_risk',
  ]

  return (
    <div
      className="p-4 rounded-lg"
      style={{ background: '#0B0E11', border: '1px solid #2B3139' }}
    >
      <div className="mb-3">
        <h3 className="text-sm font-medium" style={{ color: '#EAECEF' }}>
          {isZh ? '进场决策流水线' : 'Entry decision pipeline'}
        </h3>
        <p className="text-xs mt-1" style={{ color: '#848E9C' }}>
          {isZh
            ? '候选交易按以下顺序逐层通过；任一“启用”项不满足即被拦截。仅展示当前配置，不在此编辑。'
            : 'A candidate trade passes these layers in order; any active gate it fails blocks entry. Read-only view of current config.'}
        </p>
      </div>

      {stages.map((stage, si) => {
        const stageGates = gates.filter((g) => g.stage === stage)
        const meta = STAGE_META[stage]
        const activeCount = stageGates.filter((g) => g.active).length
        return (
          <div key={stage}>
            <div
              className="rounded-lg p-3"
              style={{
                background: '#11161C',
                border: '1px solid #2B3139',
                opacity: stage === 'structural_fit' ? 0.6 : 1,
              }}
            >
              <div className="flex items-center justify-between mb-2">
                <span
                  className="text-xs font-semibold"
                  style={{ color: '#F0B90B' }}
                >
                  {isZh ? meta.zh : meta.en}
                </span>
                <span className="text-[10px]" style={{ color: '#848E9C' }}>
                  {stage === 'structural_fit'
                    ? isZh
                      ? '由 Entry Structure 控制'
                      : 'Controlled by Entry Structure'
                    : `${activeCount}/${stageGates.length} ${isZh ? '启用' : 'active'}`}
                </span>
              </div>
              {stageGates.length > 0 && (
                <div className="space-y-1.5">
                  {stageGates.map((g) => (
                    <div
                      key={g.label[1]}
                      className="flex items-center justify-between text-xs"
                    >
                      <span className="flex items-center gap-1.5">
                        {g.active ? (
                          <CheckCircle2
                            className="w-3.5 h-3.5"
                            style={{ color: '#0ECB81' }}
                          />
                        ) : (
                          <Circle
                            className="w-3.5 h-3.5"
                            style={{ color: '#4A5159' }}
                          />
                        )}
                        <span
                          style={{ color: g.active ? '#EAECEF' : '#5E6673' }}
                        >
                          {isZh ? g.label[0] : g.label[1]}
                        </span>
                      </span>
                      <span className="flex items-center gap-2">
                        {g.value && (
                          <span style={{ color: '#AAB2BD' }}>{g.value}</span>
                        )}
                        <span
                          className="text-[10px] px-1.5 py-0.5 rounded"
                          style={{ background: '#1E2329', color: '#5E6673' }}
                        >
                          {g.source}
                        </span>
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
            {si < stages.length - 1 && (
              <div className="flex justify-center py-1">
                <ArrowDown className="w-4 h-4" style={{ color: '#2B3139' }} />
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}
