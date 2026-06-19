import type {
  SystemStatus,
  AccountInfo,
  Position,
  DecisionRecord,
  Statistics,
  CompetitionData,
  PositionHistoryResponse,
  OpenOrder,
  EvolutionProfile,
  GateBlockEntry,
  CloseAttributionResponse,
} from '../../types'
import { API_BASE, httpClient } from './helpers'

export const dataApi = {
  async getStatus(traderId?: string): Promise<SystemStatus> {
    const url = traderId
      ? `${API_BASE}/status?trader_id=${traderId}`
      : `${API_BASE}/status`
    const result = await httpClient.get<SystemStatus>(url)
    if (!result.success) throw new Error('Failed to fetch system status')
    return result.data!
  },

  async getAccount(traderId?: string): Promise<AccountInfo> {
    const url = traderId
      ? `${API_BASE}/account?trader_id=${traderId}`
      : `${API_BASE}/account`
    const result = await httpClient.get<AccountInfo>(url)
    if (!result.success) throw new Error('Failed to fetch account info')
    return result.data!
  },

  async getPositions(traderId?: string): Promise<Position[]> {
    const url = traderId
      ? `${API_BASE}/positions?trader_id=${traderId}`
      : `${API_BASE}/positions`
    const result = await httpClient.get<Position[]>(url)
    if (!result.success) throw new Error('Failed to fetch positions')
    return result.data!
  },

  async getDecisions(traderId?: string): Promise<DecisionRecord[]> {
    const url = traderId
      ? `${API_BASE}/decisions?trader_id=${traderId}`
      : `${API_BASE}/decisions`
    const result = await httpClient.get<DecisionRecord[]>(url)
    if (!result.success) throw new Error('Failed to fetch decision logs')
    return result.data!
  },

  async getLatestDecisions(
    traderId?: string,
    limit: number = 5
  ): Promise<DecisionRecord[]> {
    const params = new URLSearchParams()
    if (traderId) {
      params.append('trader_id', traderId)
    }
    params.append('limit', limit.toString())

    const result = await httpClient.get<DecisionRecord[]>(
      `${API_BASE}/decisions/latest?${params}`
    )
    if (!result.success) throw new Error('Failed to fetch latest decisions')
    return result.data!
  },

  async getStatistics(traderId?: string): Promise<Statistics> {
    const url = traderId
      ? `${API_BASE}/statistics?trader_id=${traderId}`
      : `${API_BASE}/statistics`
    const result = await httpClient.get<Statistics>(url)
    if (!result.success) throw new Error('Failed to fetch statistics')
    return result.data!
  },

  async getEquityHistory(traderId?: string, days?: number): Promise<any[]> {
    const params = new URLSearchParams()
    if (traderId) params.append('trader_id', traderId)
    if (days) params.append('days', days.toString())

    const url = `${API_BASE}/equity-history${params.toString() ? '?' + params.toString() : ''}`
    const result = await httpClient.get<any[]>(url)
    if (!result.success) throw new Error('Failed to fetch equity history')
    return result.data!
  },

  async getEquityHistoryBatch(
    traderIds: string[],
    hours?: number
  ): Promise<any> {
    const result = await httpClient.post<any>(
      `${API_BASE}/equity-history-batch`,
      { trader_ids: traderIds, hours: hours || 0 }
    )
    if (!result.success) throw new Error('Failed to fetch batch equity history')
    return result.data!
  },

  async getTopTraders(): Promise<any[]> {
    const result = await httpClient.get<any[]>(`${API_BASE}/top-traders`)
    if (!result.success) throw new Error('Failed to fetch top traders')
    return result.data!
  },

  async getPublicTraderConfig(traderId: string): Promise<any> {
    const result = await httpClient.get<any>(
      `${API_BASE}/traders/${traderId}/public-config`
    )
    if (!result.success) throw new Error('Failed to fetch public trader config')
    return result.data!
  },

  async getCompetition(): Promise<CompetitionData> {
    const result = await httpClient.get<CompetitionData>(
      `${API_BASE}/competition`
    )
    if (!result.success) throw new Error('Failed to fetch competition data')
    return result.data!
  },

  async getPositionHistory(
    traderId: string,
    limit: number = 100
  ): Promise<PositionHistoryResponse> {
    const result = await httpClient.get<PositionHistoryResponse>(
      `${API_BASE}/positions/history?trader_id=${traderId}&limit=${limit}`
    )
    if (!result.success) throw new Error('Failed to fetch position history')
    return result.data!
  },

  async getOpenOrders(traderId: string, symbol: string): Promise<OpenOrder[]> {
    const params = new URLSearchParams({ trader_id: traderId, symbol })
    const result = await httpClient.get<OpenOrder[]>(
      `${API_BASE}/open-orders?${params}`
    )
    if (!result.success) throw new Error('Failed to fetch open orders')
    return result.data!
  },

  async getCloseAttribution(
    traderId: string,
    days: number = 30
  ): Promise<CloseAttributionResponse> {
    const result = await httpClient.get<CloseAttributionResponse>(
      `${API_BASE}/positions/attribution?trader_id=${traderId}&days=${days}`
    )
    if (!result.success) throw new Error('Failed to fetch close attribution')
    return result.data!
  },

  async getEvolutionProfiles(traderId: string): Promise<EvolutionProfile[]> {
    const result = await httpClient.get<EvolutionProfile[]>(
      `${API_BASE}/evolution/profiles?trader_id=${traderId}`
    )
    if (!result.success) return []
    return result.data || []
  },

  async getGateBlocks(traderId: string, limit = 50): Promise<GateBlockEntry[]> {
    const result = await httpClient.get<{ blocks: GateBlockEntry[] }>(
      `${API_BASE}/gate-blocks?trader_id=${traderId}&limit=${limit}`
    )
    if (!result.success) return []
    return result.data?.blocks || []
  },

  async resetEvolutionProfile(
    traderId: string,
    symbol: string,
    side: string
  ): Promise<boolean> {
    const result = await httpClient.post<{ message: string }>(
      `${API_BASE}/evolution/reset`,
      { trader_id: traderId, symbol, side }
    )
    return result.success
  },
}
