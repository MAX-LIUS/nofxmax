import { useState, useCallback } from 'react'

const PREFS_KEY = 'nofx-chart-prefs'

export type RefreshRate = 'realtime' | '1s' | '5s' | 'off'

export interface ChartPrefs {
  indicators: Record<string, boolean>
  interval: string
  refreshRate: RefreshRate
  showStructuralLevels: boolean
  showFibonacci: boolean
  showVWAP: boolean
  showOrderMarkers: boolean
  // Pending-order price lines (SL/TP/limit) drawn on the candlestick series
  showOrderLines: boolean
  // Structure-map overlays (BOS/CHoCH, order blocks, volume profile, anchored VWAP)
  showStructureBreaks: boolean
  showOrderBlocks: boolean
  showVolumeProfile: boolean
  showAnchoredVWAP: boolean
  // Per-timeframe toggles: e.g. { "support-5m": true, "fib-1h": false }
  levelTimeframes: Record<string, boolean>
}

const DEFAULT_PREFS: ChartPrefs = {
  indicators: { volume: true },
  interval: '5m',
  refreshRate: '5s',
  showStructuralLevels: true,
  showFibonacci: true,
  showVWAP: true,
  showOrderMarkers: true,
  showOrderLines: true,
  showStructureBreaks: true,
  showOrderBlocks: true,
  showVolumeProfile: false,
  showAnchoredVWAP: false,
  levelTimeframes: {},
}

function loadPrefs(): ChartPrefs {
  try {
    const raw = localStorage.getItem(PREFS_KEY)
    if (!raw) return DEFAULT_PREFS
    return { ...DEFAULT_PREFS, ...JSON.parse(raw) }
  } catch {
    return DEFAULT_PREFS
  }
}

export function useChartPrefs() {
  const [prefs, setPrefs] = useState<ChartPrefs>(loadPrefs)

  const updatePrefs = useCallback((patch: Partial<ChartPrefs>) => {
    setPrefs((prev) => {
      const next = { ...prev, ...patch }
      try {
        localStorage.setItem(PREFS_KEY, JSON.stringify(next))
      } catch {
        /* quota exceeded — ignore */
      }
      return next
    })
  }, [])

  return { prefs, updatePrefs }
}
