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

export interface StructureBreak {
  type: string // "BOS" | "CHOCH"
  direction: string // "bullish" | "bearish"
  breakLevel: number
  barsAgo: number
  retestLow: number
  retestHigh: number
  retested: boolean
  sizeATR: number
}

export interface OrderBlock {
  low: number
  high: number
  mid: number
  direction: string // "demand" | "supply"
  barsAgo: number
  mitigated: boolean
  sizeATR: number
}

export interface VolumeProfile {
  poc: number
  vah: number
  val: number
  hvns: number[]
  lvns: number[]
  range_low: number
  range_high: number
  total_vol: number
  timeframe: string
  bin_count: number
}

export interface AnchoredVWAP {
  anchor: string // "swing_high" | "swing_low" | "window_start"
  anchor_price: number
  anchor_bars: number
  vwap: number
  upper_band: number
  lower_band: number
  timeframe: string
}

interface CompositeResponse {
  lines?: CompositeMarketLine[]
  zones?: StructuralZone[]
  structure_breaks?: StructureBreak[]
  order_blocks?: OrderBlock[]
  volume_profile?: VolumeProfile | null
  anchored_vwaps?: AnchoredVWAP[]
}

export interface StructuralLevelsResult {
  lines: CompositeMarketLine[]
  zones: StructuralZone[]
  structureBreaks: StructureBreak[]
  orderBlocks: OrderBlock[]
  volumeProfile: VolumeProfile | null
  anchoredVWAPs: AnchoredVWAP[]
}

export function useStructuralLevels(
  symbol: string,
  exchange: string,
  enabled: boolean
): StructuralLevelsResult {
  const [lines, setLines] = useState<CompositeMarketLine[]>([])
  const [zones, setZones] = useState<StructuralZone[]>([])
  const [structureBreaks, setStructureBreaks] = useState<StructureBreak[]>([])
  const [orderBlocks, setOrderBlocks] = useState<OrderBlock[]>([])
  const [volumeProfile, setVolumeProfile] = useState<VolumeProfile | null>(null)
  const [anchoredVWAPs, setAnchoredVWAPs] = useState<AnchoredVWAP[]>([])
  const abortRef = useRef<AbortController | null>(null)

  useEffect(() => {
    if (!enabled || !symbol) {
      setLines([])
      setZones([])
      setStructureBreaks([])
      setOrderBlocks([])
      setVolumeProfile(null)
      setAnchoredVWAPs([])
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
          setStructureBreaks(result.data.structure_breaks ?? [])
          setOrderBlocks(result.data.order_blocks ?? [])
          setVolumeProfile(result.data.volume_profile ?? null)
          setAnchoredVWAPs(result.data.anchored_vwaps ?? [])
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

  return {
    lines,
    zones,
    structureBreaks,
    orderBlocks,
    volumeProfile,
    anchoredVWAPs,
  }
}
