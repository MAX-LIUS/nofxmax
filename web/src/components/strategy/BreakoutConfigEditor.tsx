import { Zap } from 'lucide-react'
import type { BreakoutEntryConfig } from '../../types/strategy'
import { t, type Language } from '../../i18n/translations'

interface BreakoutConfigEditorProps {
  config: BreakoutEntryConfig
  onChange: (config: BreakoutEntryConfig) => void
  disabled?: boolean
  language: Language
}

// Default breakout entry configuration. Mirrors the Go-side defaults:
// timeframe falls back to primary/"1h", lookback 20, leverage altcoin, frac 1.0.
export const defaultBreakoutEntryConfig: BreakoutEntryConfig = {
  enabled: true,
  timeframe: '1h',
  lookback: 20,
  leverage: 5,
  size_equity_frac: 1.0,
}

const TIMEFRAMES = ['15m', '30m', '1h', '4h', '1d']

export function BreakoutConfigEditor({
  config,
  onChange,
  disabled,
  language,
}: BreakoutConfigEditorProps) {
  const tr = (key: string) => t(`strategyStudio.${key}`, language)

  const updateField = <K extends keyof BreakoutEntryConfig>(
    key: K,
    value: BreakoutEntryConfig[K]
  ) => {
    if (!disabled) {
      onChange({ ...config, [key]: value })
    }
  }

  const inputStyle = {
    background: '#1E2329',
    border: '1px solid #2B3139',
    color: '#EAECEF',
  }
  const sectionStyle = {
    background: '#0B0E11',
    border: '1px solid #2B3139',
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <Zap className="w-5 h-5" style={{ color: '#38BDF8' }} />
        <h3 className="font-medium" style={{ color: '#EAECEF' }}>
          {tr('breakoutConfig')}
        </h3>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        {/* Timeframe */}
        <div className="p-4 rounded-lg" style={sectionStyle}>
          <label className="block text-sm mb-2" style={{ color: '#EAECEF' }}>
            {tr('breakoutTimeframe')}
          </label>
          <select
            value={config.timeframe || '1h'}
            onChange={(e) => updateField('timeframe', e.target.value)}
            disabled={disabled}
            className="w-full px-3 py-2 rounded"
            style={inputStyle}
          >
            {TIMEFRAMES.map((tf) => (
              <option key={tf} value={tf}>
                {tf}
              </option>
            ))}
          </select>
        </div>

        {/* Lookback */}
        <div className="p-4 rounded-lg" style={sectionStyle}>
          <label className="block text-sm mb-2" style={{ color: '#EAECEF' }}>
            {tr('breakoutLookback')}
          </label>
          <input
            type="number"
            min={2}
            max={500}
            value={config.lookback ?? 20}
            onChange={(e) =>
              updateField('lookback', parseInt(e.target.value, 10) || 0)
            }
            disabled={disabled}
            className="w-full px-3 py-2 rounded"
            style={inputStyle}
          />
        </div>

        {/* Leverage */}
        <div className="p-4 rounded-lg" style={sectionStyle}>
          <label className="block text-sm mb-2" style={{ color: '#EAECEF' }}>
            {tr('breakoutLeverage')}
          </label>
          <input
            type="number"
            min={1}
            max={125}
            value={config.leverage ?? 5}
            onChange={(e) =>
              updateField('leverage', parseInt(e.target.value, 10) || 0)
            }
            disabled={disabled}
            className="w-full px-3 py-2 rounded"
            style={inputStyle}
          />
        </div>

        {/* Size fraction */}
        <div className="p-4 rounded-lg" style={sectionStyle}>
          <label className="block text-sm mb-2" style={{ color: '#EAECEF' }}>
            {tr('breakoutSizeFrac')}
          </label>
          <input
            type="number"
            min={0.05}
            max={1}
            step={0.05}
            value={config.size_equity_frac ?? 1.0}
            onChange={(e) =>
              updateField('size_equity_frac', parseFloat(e.target.value) || 0)
            }
            disabled={disabled}
            className="w-full px-3 py-2 rounded"
            style={inputStyle}
          />
        </div>
      </div>
    </div>
  )
}
