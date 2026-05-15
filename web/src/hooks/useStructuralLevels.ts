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

export function useStructuralLevels(
  symbol: string,
  exchange: string,
  enabled: boolean,
): CompositeMarketLine[] {
  const [lines, setLines] = useState<CompositeMarketLine[]>([])
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!enabled || !symbol) {
      setLines([])
      return
    }

    const fetchLines = async () => {
      try {
        abortRef.current?.abort()
        abortRef.current = new AbortController()

        const result = await httpClient.get<{
          lines?: CompositeMarketLine[]
        }>(`/api/market/composite?symbol=${encodeURIComponent(symbol)}&exchange=${encodeURIComponent(exchange)}&view=chart`)

        if (result.success && result.data?.lines) {
          setLines(result.data.lines)
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

  return lines
}
