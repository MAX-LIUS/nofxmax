import { ShieldCheck, AlertTriangle } from 'lucide-react'
import type { ProtectionConfig } from '../../types/strategy'
import { resolveProtectionOwnership, ownerLabel } from './protectionArbitration'

interface Props {
  config?: ProtectionConfig
  language: string
}

// ProtectionArbitrationBanner makes the backend precedence visible: when multiple
// TP/SL mechanisms are enabled, only one OWNS stop-loss and one OWNS take-profit.
// Previously this resolution lived only in backend code (protection_owner_policy.go),
// leaving users unsure whether enabled mechanisms were fighting. This shows the truth.
export function ProtectionArbitrationBanner({ config, language }: Props) {
  const isZh = language === 'zh'
  const o = resolveProtectionOwnership(config)

  const hasAny = o.stopOwner !== 'none' || o.profitOwner !== 'none'
  if (!hasAny) return null

  const hasShadow = o.shadowedStops.length > 0 || o.shadowedProfits.length > 0

  return (
    <div
      className="p-3 rounded-lg"
      style={{
        background: '#0B0E11',
        border: `1px solid ${hasShadow ? '#F0B90B55' : '#2B3139'}`,
      }}
    >
      <div className="flex items-center gap-2 mb-2">
        <ShieldCheck className="w-4 h-4" style={{ color: '#0ECB81' }} />
        <span className="text-sm font-medium" style={{ color: '#EAECEF' }}>
          {isZh ? '当前实际生效' : 'Currently in effect'}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <div className="text-[10px] uppercase" style={{ color: '#848E9C' }}>
            {isZh ? '止损归属' : 'Stop-loss owner'}
          </div>
          <div className="text-sm font-semibold" style={{ color: '#EAECEF' }}>
            {ownerLabel(o.stopOwner, isZh)}
          </div>
        </div>
        <div>
          <div className="text-[10px] uppercase" style={{ color: '#848E9C' }}>
            {isZh ? '止盈归属' : 'Take-profit owner'}
          </div>
          <div className="text-sm font-semibold" style={{ color: '#EAECEF' }}>
            {ownerLabel(o.profitOwner, isZh)}
          </div>
        </div>
      </div>

      {hasShadow && (
        <div
          className="mt-2 flex items-start gap-2 pt-2"
          style={{ borderTop: '1px solid #2B3139' }}
        >
          <AlertTriangle
            className="w-3.5 h-3.5 mt-0.5 shrink-0"
            style={{ color: '#F0B90B' }}
          />
          <div className="text-xs" style={{ color: '#AAB2BD' }}>
            {isZh
              ? '以下已启用但被覆盖（不会执行）：'
              : 'Enabled but overridden (won’t run): '}
            {[
              ...o.shadowedStops.map(
                (s) => `${labelOf(s, isZh)}${isZh ? '止损' : ' stop'}`
              ),
              ...o.shadowedProfits.map(
                (s) => `${labelOf(s, isZh)}${isZh ? '止盈' : ' TP'}`
              ),
            ].join(isZh ? '、' : ', ')}
            {o.suppressStaticTP && (
              <span style={{ color: '#848E9C' }}>
                {isZh
                  ? '（峰值回撤止盈已接管，静态止盈被压制）'
                  : ' (drawdown TP has taken over; static TP suppressed)'}
              </span>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function labelOf(key: string, isZh: boolean): string {
  const m: Record<string, [string, string]> = {
    full: ['固定', 'Fixed'],
    ladder: ['阶梯', 'Ladder'],
    break_even: ['保本', 'Break-even'],
  }
  const e = m[key] || [key, key]
  return isZh ? e[0] : e[1]
}
