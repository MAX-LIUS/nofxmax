import { useState, useEffect, useRef } from 'react'
import { httpClient } from '../lib/httpClient'

export interface CompositeMarketLine {
  id: string
  price: number
  kind: string
  label: string
  timeframe?: string
  strength?: number
  source?: string
  distance_pct?: number
  confidence?: number
  multi_tf_count?: number
}

export interface StructuralZone {
  low: number
  high: number
  mid_price: number
  type: 'support' | 'resistance'
  timeframes: string[]
  sources: string[]
  touch_count: number
  confidence: number
  quality_grade: 'A' | 'B' | 'C'
  last_touch_bars: number
  volume_score: number
  atr_width: number
  flipped: boolean
  flip_count: number
  multi_tf_count: number
}

interface CompositeResponse {
  lines?: CompositeMarketLine[]
  zones?: StructuralZone[]
}

export function useStructuralLevels(
  symbol: string,
  exchange: string,
  enabled: boolean
): { lines: CompositeMarketLine[]; zones: StructuralZone[] } {
  const [lines, setLines] = useState<CompositeMarketLine[]>([])
  const [zones, setZones] = useState<StructuralZone[]>([])
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!enabled || !symbol) {
      setLines([])
      setZones([])
      return
    }

    const fetchLines = async () => {
      try {
        abortRef.current?.abort()
        abortRef.current = new AbortController()

        const result = await httpClient.get<CompositeResponse>(
          `/api/market/composite?symbol=${encodeURIComponent(symbol)}&exchange=${encodeURIComponent(exchange)}&view=chart`
        )

        if (result.success && result.data) {
          setLines(result.data.lines ?? [])
          setZones(result.data.zones ?? [])
        }
      } catch {
        // silently ignore fetch errors for structural levels
      }
    }

    fetchLines()
    const id = setInterval(fetchLines, 60_000)
    return () => {
      clearInterval(id)
      abortRef.current?.abort()
    }
  }, [symbol, exchange, enabled])

  return { lines, zones }
}
