// Strategy Studio Types
export interface Strategy {
  id: string
  name: string
  description: string
  is_active: boolean
  is_default: boolean
  is_public: boolean // 是否在策略市场公开
  config_visible: boolean // 配置参数是否公开可见
  config: StrategyConfig
  created_at: string
  updated_at: string
}

// 策略使用统计
export interface StrategyStats {
  clone_count: number // 被克隆次数
  active_users: number // 当前使用人数
  top_performers?: StrategyPerformer[] // 收益排行
}

// 策略使用者收益排行
export interface StrategyPerformer {
  user_id: string
  user_name: string // 脱敏后的用户名
  total_pnl_pct: number // 总收益率
  total_pnl: number // 总收益金额
  win_rate: number // 胜率
  trade_count: number // 交易次数
  using_since: string // 使用开始时间
  rank: number // 排名
}

export interface PromptSectionsConfig {
  role_definition?: string
  trading_frequency?: string
  entry_standards?: string
  decision_process?: string
}

export type StrategyControlPolicyMode =
  | 'strict'
  | 'audit_only'
  | 'recommend_only'

export interface StrategyControlPolicyConfig {
  mode?: StrategyControlPolicyMode
}

export interface EntryStructureConfig {
  enabled: boolean
  require_primary_timeframe: boolean
  require_adjacent_timeframes: boolean
  require_support_resistance: boolean
  require_structural_anchors: boolean
  require_fibonacci: boolean
  max_support_levels?: number
  max_resistance_levels?: number
  max_anchor_count?: number
  audit_primary_timeframe?: boolean
  audit_adjacent_timeframes?: boolean
  audit_support_resistance?: boolean
  audit_structural_anchors?: boolean
  audit_fibonacci?: boolean
  require_invalidation_target_linkage?: boolean
  entry_gate?: EntryGateConfig
}

export interface EntryGateConfig {
  enabled?: boolean
  // Group toggles
  volatility_gate_enabled?: boolean
  entry_precision_enabled?: boolean
  stop_quality_enabled?: boolean
  path_clarity_enabled?: boolean
  confidence_direction_enabled?: boolean
  // Volatility gate
  min_atr14_pct?: number
  // Entry precision
  entry_proximity_atr_mul?: number
  entry_proximity_max_pct?: number
  // Stop loss quality
  min_risk_distance_pct?: number
  min_sl_distance_atr_mul?: number
  invalidation_structure_atr_mul?: number
  // Volatility buffer: extra ATR-based SL padding [0=tightest, 1.5=widest]. Default 0.3.
  volatility_buffer_atr_mul?: number
  // Reward distance
  min_reward_atr_mul?: number
  // Max target distance / ATR. Default 5.0, 0/negative disables.
  max_target_atr_mul?: number
  // What to do when first_target > max_target_atr_mul: 'cap' (default, rewrite to
  // reachable ceiling + recompute RR) or 'reject' (legacy hard-block). Cap recovers
  // reverting winners: full-portfolio reject +28.7% vs cap +37.0%.
  target_reachability_mode?: 'cap' | 'reject'
  // Profit-lock ladder/BE tier distance as multiple of stop-risk, fed to AI prompt.
  // Default 0.8 (easier to bank; floored by min_reward_atr_mul to clear noise band).
  realistic_target_risk_mul?: number
  // Path clarity
  max_blocking_levels?: number
  // Confidence & direction
  short_non_downtrend_min_confidence?: number
  squeeze_min_confidence?: number
  squeeze_min_rr?: number
  // Entry-quality hard blocks (backtest: 1190 closed positions, rolling 5-fold
  // walk-forward, bootstrap P(improve)=98.8%).
  // Hard-block setup_type=breakout_retest (net-negative in every time third). Default true.
  block_breakout_retest?: boolean
  // Hard-block entries whose AI-promised net RR exceeds this ceiling (over-promised
  // targets rarely hit, ~52% win rate). Default 2.8, <0 disables.
  max_net_rr?: number
  // Correlated-adverse entry throttle: skip a NEW entry when the trader's own
  // recent finished closes cluster into losses (toxic regime). On the 63-day live
  // set it was the sole entry lever positive out-of-sample, but a multi-year proxy
  // backtest failed to confirm it (full-sample delta -72.7, positive in only 2/5
  // years), so it ships DEFAULT OFF and is enabled per-trader.
  // (Corrected 2026-07-30: this comment previously said "Default on", while
  // CorrelatedAdverseThrottleEnabled() returns false when unset.)
  correlated_adverse_throttle?: boolean
  throttle_window_hours?: number
  throttle_min_closes?: number
  throttle_loss_rate?: number
  // --- Structural alignment gate (HH/HL + blocking-level distance) ---
  // Requires the PRIMARY timeframe to show a clean monotonic swing sequence in the
  // trade direction (HH+HL for long, LH+LL for short) before an entry may open.
  // Unlike every other check in this stage it reads swing structure off the candles
  // instead of trusting the AI's self-reported key levels (the AI omitted blocking
  // levels in 47% of the研究 sample).
  //
  // DEFAULT OFF, enabled per trader. Validated by re-simulating BOTH the 1839
  // opened trades and the 1144 gate-blocked ones that carry an entry price under a
  // single neutral rule (hold 6h, no SL/TP) so the two pass-sets are comparable,
  // excluding protection-plan-defect blocks a no-stop simulation cannot see.
  // At the defaults (lb=3/n=3/0.05): 284 passed, +0.323% mean vs -0.034% no-gate
  // baseline, p=0.0080; 3/3 walk-forward folds, 3/3 months, both directions and all
  // 4 traders positive, and still positive with the top-contributing coin removed.
  // Real-PnL cross-check (July, comparable exit machinery): all 876 opened trades
  // total -78.16 while the 121 this gate would pass total +62.97 at 69.4% win.
  // Unresolved: parameters drift by window, TON alone is 35-66% of the gain,
  // market_state is unevaluable (all controls passed it), and entry frequency drops
  // from ~21/day to ~3.3/day.
  structural_alignment?: boolean
  // Fractal half-width for pivot detection. Default 3, and decisive: lb=2 is noise
  // (best cell p=0.08), lb=4 is weaker, every lb=3 cell 0-0.35 is significant.
  structural_pivot_lookback?: number
  // How many recent swing highs/lows must form a clean monotonic sequence.
  // Default 2, the validated recommendation: 11.5 entries/day, 28 coins, all 4
  // traders improved on real July PnL, out-of-sample increment +0.272 (p=0.0012).
  // 3 is significant on real July PnL (p=0.0192 vs 0.1442) but costs 80% of the
  // frequency and leans 55% on one coin. 4 collapses the sample. Range 2-6.
  structural_swing_count?: number
  // Minimum distance (% of entry) to the nearest COMPUTED blocking pivot ahead.
  // DEFAULT 0 = direction check only, deliberately. A 0.05-step sweep picked 0.05
  // and that pick was rejected: it beats 0 by noise on real PnL while being tuned,
  // and the tuned cells DEGRADE out-of-sample (+0.392 → +0.323) while 0 holds
  // (+0.289 → +0.321). Treat any non-zero value as unvalidated.
  structural_min_blocking_pct?: number
  // Log what the gate WOULD block without blocking it. Deducts no score, so
  // observation cannot quietly shrink position size.
  structural_audit_only?: boolean
  // soft_regime_structure_fit (Market Structure Map): treat the regime↔structure
  // mismatch check as a SOFT confidence penalty (deducts from the gate score →
  // smaller position) instead of a HARD block. A high-conviction counter-structure
  // trade is sized down rather than rejected, so structure acts as confidence-
  // weighting evidence, not an all-or-nothing gate. Genuine invalidation/trap gates
  // (fake_retest_trap, protection_policy_rejected) stay hard regardless. Default off.
  soft_regime_structure_fit?: boolean
  // Legacy compat
  entry_proximity_min_pct?: number
  invalidation_structure_min_pct?: number
  max_target_timeframe_rank_gap?: number
}

export interface StrategyConfig {
  // Strategy type: "ai_trading" (default), "grid_trading", or "breakout_trading"
  strategy_type?: 'ai_trading' | 'grid_trading' | 'breakout_trading'
  // Language setting: "zh" for Chinese, "en" for English
  // Determines the language used for data formatting and prompt generation
  language?: 'zh' | 'en'
  coin_source: CoinSourceConfig
  indicators: IndicatorConfig
  custom_prompt?: string
  risk_control: RiskControlConfig
  protection: ProtectionConfig
  atr_protection?: ATRProtectionConfig
  entry_structure?: EntryStructureConfig
  prompt_sections?: PromptSectionsConfig
  strategy_control_policy?: StrategyControlPolicyConfig
  evolution?: EvolutionConfig
  // Grid trading configuration (only used when strategy_type is 'grid_trading')
  grid_config?: GridStrategyConfig
  // Breakout entry configuration (only used when strategy_type is 'breakout_trading')
  breakout_entry?: BreakoutEntryConfig
  // Regime/trend entry gates promoted from the shadow dry-run bench. Each entry is
  // one gate keyed by CATEGORY (大类); params differentiate variants within a
  // category. mode='shadow' only records a counterfactual verdict; mode='enforce'
  // actually blocks the open (and the blocked intent is replayed for scoring).
  regime_gates?: RegimeGateConfig[]
}

// RegimeGateCategory mirrors the backend switch in trader/regime_gate.go.
export type RegimeGateCategory =
  | 'counter_trend'
  | 'trend_direction_only'
  | 'chop_reject'
  | 'chop_lowconf'
  | 'adx_weak'
  | 'donchian_counter'
  | 'chart_trend'

export type RegimeGateMode = 'shadow' | 'enforce'

// RegimeGateConfig is one configured regime/trend entry gate. Matches
// store.RegimeGateConfig on the backend.
export interface RegimeGateConfig {
  category: RegimeGateCategory
  mode: RegimeGateMode
  enabled: boolean
  params?: {
    slope_window?: number // counter_trend / trend_direction_only / chart_trend
    block_side?: 'LONG' | 'SHORT' // trend_direction_only
    min_conf?: number // chop_lowconf
    threshold?: number // adx_weak
    adx_period?: number // adx_weak; unset = 14
    lookback?: number // donchian_counter; also chart_trend pivot half-width (unset = 2)
    align_min?: number // chart_trend
    r2_min?: number // chart_trend
  }
}

// BreakoutEntryConfig controls the standalone data-validated breakout engine.
export interface BreakoutEntryConfig {
  enabled?: boolean
  // Timeframe series to evaluate, e.g. "1h", "15m". Defaults to primary/"1h".
  timeframe?: string
  // Prior-bar window for the high/low breakout. Default 20.
  lookback?: number
  // Leverage for breakout entries. Defaults to altcoin leverage.
  leverage?: number
  // Position size as a fraction of equity (1.0 = 100% of equity notional).
  size_equity_frac?: number
}

export interface EvolutionConfig {
  enabled: boolean
  half_life_days?: number
  min_sample_size?: number
  adaptation_ttl_days?: number
  score_threshold_low?: number
  score_threshold_high?: number
  inject_to_prompt?: boolean
}

export type ProtectionMode = 'disabled' | 'manual' | 'ai'
export type ProtectionValueMode = 'disabled' | 'manual' | 'ai'
export type BreakEvenTriggerMode = 'profit_pct' | 'r_multiple'

export interface ProtectionValueSource {
  mode: ProtectionValueMode
  value: number
}

export interface FullTPSLConfig {
  enabled: boolean
  mode: ProtectionMode
  take_profit_enabled?: boolean
  stop_loss_enabled?: boolean
  fallback_max_loss_enabled?: boolean
  take_profit: ProtectionValueSource
  stop_loss: ProtectionValueSource
  fallback_max_loss: ProtectionValueSource
}

export interface LadderTPSLRule {
  take_profit_pct?: number
  take_profit_close_ratio_pct?: number
  stop_loss_pct?: number
  stop_loss_close_ratio_pct?: number
  // Per-field unit: 'percent' (value is %) | 'atr' (value is an ATR multiple).
  take_profit_unit?: ProtectionDistanceUnit
  stop_loss_unit?: ProtectionDistanceUnit
}

// Structural (range-anchored) stop-loss config. Active when a ladder SL rule uses
// stop_loss_unit='structural'. The stop is placed just beyond the pre-entry range
// boundary (swing low for long / swing high for short), clamped to [floor, backstop]
// ATR multiples. close_confirm (Phase 2) enforces it on a confirmed bar close via an
// engine poll, parking the resting exchange stop at the backstop as a downtime net.
export interface StructuralSLConfig {
  enabled?: boolean
  floor_atr_mul?: number
  backstop_atr_mul?: number
  lookback_bars?: number
  close_confirm?: boolean
  // pivot_strength: fractal strength for nearest-swing detection (bars on each side).
  // The boundary anchors to the NEAREST swing beyond entry, not the window extreme.
  pivot_strength?: number
  // fallback_atr_mul: tighter cap used instead of the backstop when no near structure
  // exists (nearest swing still beyond the backstop). Must be <= backstop_atr_mul.
  fallback_atr_mul?: number
  // fallback_rr_cap_ratio: on the fallback path, the stop must stay below this ratio ×
  // the TP target move (fallbackSL% <= ratio × TP%), guaranteeing RR >= 1/ratio.
  fallback_rr_cap_ratio?: number

  // --- Ratcheting (trailing) structural stop ---
  // trail_enabled: upgrade the static structural stop into a ratchet that moves the
  // close-confirm boundary tighter toward locking profit each closed bar (never looser).
  trail_enabled?: boolean
  // trail_tol_atr: volatility cushion (ATR mult) added beyond the swing; also the
  // anti-jitter step. Default 0.5.
  trail_tol_atr?: number
  // trail_mode: which structure the trail follows — "current" | "higher" | "both".
  trail_mode?: string
  // trail_higher_mult: higher-timeframe aggregation factor for higher/both modes. Default 4.
  trail_higher_mult?: number
  // trail_min_profit_atr: favorable excursion (ATR mult) required before the trail
  // arms. Unset = 1. An explicit 0 disables the gate.
  trail_min_profit_atr?: number
  // trail_max_ratchets: cap on how many times the boundary may tighten. 0 = unlimited.
  trail_max_ratchets?: number
  // trail_on_profit / trail_on_loss: allow ratcheting while in profit / in loss. Both
  // true = every state; only one = that side; both false = never.
  // Defaults: trail_on_profit true, trail_on_loss FALSE — an in-loss ratchet cannot
  // lock profit by construction, it only pulls the invalidation level into the noise.
  trail_on_profit?: boolean
  trail_on_loss?: boolean
  // asset_adaptive_confirm_tf (Method 4): confirm the close-confirm breach on an
  // asset-aware timeframe. Crypto refines ~2× finer than native (backtest: 1h→30m
  // +10.81 PnL, drawdown 88→77) when a clean 2× step exists; stocks/commodities keep
  // native (fine TFs whipsaw worst on session microstructure). Off = native everywhere.
  asset_adaptive_confirm_tf?: boolean
  // prefer_proven_levels (Market Structure Map): anchor the structural stop to a
  // proven order-block edge (demand-block high for a long / supply-block low for a
  // short) in preference to the raw fractal swing, but only when it does NOT widen
  // the stop past the nearest pivot. An order block is the origin of an impulsive
  // break — a level the market defended — so it is a stronger invalidation point.
  // Backtest (never-widen guarantees non-negative): claude 1h +1.68 PnL / DD flat,
  // Claude-R 15m +0.62 PnL / DD -0.62. Off = fractal-only.
  prefer_proven_levels?: boolean
}

export interface LadderTPSLConfig {
  enabled: boolean
  mode: ProtectionMode
  take_profit_enabled: boolean
  stop_loss_enabled: boolean
  take_profit_price: ProtectionValueSource
  take_profit_size: ProtectionValueSource
  stop_loss_price: ProtectionValueSource
  stop_loss_size: ProtectionValueSource
  fallback_max_loss: ProtectionValueSource
  rules: LadderTPSLRule[]
  structural_sl?: StructuralSLConfig
}

export type DrawdownEngineMode = 'manual' | 'ai'
export type BreakEvenRunnerPolicy =
  | 'primary'
  | 'fallback_only'
  | 'disabled_for_runner'

export interface DrawdownTakeProfitRule {
  min_profit_pct: number
  max_drawdown_pct: number
  close_ratio_pct: number
  poll_interval_seconds: number
  // Per-field AI/manual control
  close_ratio_mode?: ProtectionValueMode
  min_profit_mode?: ProtectionValueMode
  max_drawdown_mode?: ProtectionValueMode
  // Per-field unit: 'percent' (value is %) | 'atr' (value is an ATR multiple).
  min_profit_unit?: ProtectionDistanceUnit
  max_drawdown_unit?: ProtectionDistanceUnit
}

export interface DrawdownTakeProfitConfig {
  enabled: boolean
  /**
   * disabled/manual/ai reflects protection ownership semantics.
   * engine_mode decides whether drawdown behavior stays fixed-rule or structure-driven.
   */
  mode: ProtectionMode
  engine_mode?: DrawdownEngineMode
  runner_enabled?: boolean
  min_runner_keep_pct?: number
  max_first_reduce_pct?: number
  break_even_runner_policy?: BreakEvenRunnerPolicy
  rules: DrawdownTakeProfitRule[]
}

export interface BreakEvenStopRule {
  trigger_mode?: BreakEvenTriggerMode
  trigger_value: number
  offset_pct: number
  close_ratio_pct?: number
  stage_name?: string
  // Per-field unit: 'percent' (value is %) | 'atr' (value is an ATR multiple).
  trigger_unit?: ProtectionDistanceUnit
}

export interface BreakEvenStopConfig {
  enabled: boolean
  mode?: ProtectionMode
  trigger_mode: BreakEvenTriggerMode
  trigger_value: number
  offset_pct: number
  rules?: BreakEvenStopRule[]
}

export interface RegimeFilterConfig {
  enabled: boolean
  allowed_regimes: string[]
  block_high_funding: boolean
  max_funding_rate_abs: number
  block_high_volatility: boolean
  max_atr14_pct: number
  require_trend_alignment: boolean
  trend_alignment_mode?: 'strict' | 'allow_range_edge_reversal'
  // Block open_long into confirmed 1h+4h downtrend (multi-TF gate, symmetric with short-side).
  // Real 2026: 4h-confirmed down×LONG -4.7% vs 1h-only +1.7% (preserves bull-dip longs).
  // Unset defaults to true (enabled).
  block_long_in_htf_downtrend?: boolean
  // Block open_short into confirmed 1h+4h uptrend (symmetric counterpart to long-side gate).
  // Real 2026: 4h-confirmed up×SHORT -4.9% vs 1h-only +6.6% (preserves bear-bounce shorts).
  // Bull sim: up×SHORT in fast bulls -970%, slow bulls -328%. Unset defaults to true.
  block_short_in_htf_uptrend?: boolean

  // Coin momentum gate
  momentum_gate_enabled?: boolean
  momentum_stale_chg1h?: number // abs(chg1h) below this = stale (default 0.15)
  momentum_stale_chg4h?: number // abs(chg4h) below this AND chg1h stale = stale (default 0.4)
  momentum_exhausted_chg4h?: number // abs(chg4h) above this = exhausted (default 4.5 major, 3.5 alt)
  momentum_counter_chg1h?: number // chg1h opposing direction above this = counter (default 0.3)
  momentum_fading_chg4h?: number // chg4h above this triggers fading check (default 2.5)
  max_sl_distance_pct?: number // max SL distance as % of entry price (default 2.0)

  // Entry confidence gate (moved from RiskControl)
  min_confidence?: number // 0-100, minimum AI confidence to open
  min_risk_reward_ratio?: number // minimum TP/SL ratio (e.g., 3 = 1:3)

  // Strategy control policy (moved from StrategyControlPolicy)
  policy_mode?: 'strict' | 'audit_only' | 'recommend_only'

  // Entry structure (embedded, moved from top-level entry_structure)
  entry_structure?: EntryStructureConfig
}

// ATRProtectionConfig: global ATR settings. Individual TP/SL/DD/BE fields opt
// into ATR via their own per-field unit toggle (percent | atr); when a field is
// in ATR mode its value is an ATR MULTIPLE resolved at use time to an effective
// percent (value × ATR(period) / entryPrice × 100), clamped to min/max_eff_pct.
export type ProtectionDistanceUnit = 'percent' | 'atr' | 'structural'

export interface ATRProtectionConfig {
  enabled: boolean
  timeframe?: string
  atr_period?: number
  min_eff_pct?: number
  max_eff_pct?: number
}

export interface ProtectionConfig {
  full_tp_sl: FullTPSLConfig
  ladder_tp_sl: LadderTPSLConfig
  drawdown_take_profit: DrawdownTakeProfitConfig
  break_even_stop: BreakEvenStopConfig
  regime_filter: RegimeFilterConfig
  giveback_guard?: GivebackGuardConfig
  trend_reversal?: TrendReversalConfig
}

// Trend-reversal position flip. Fleet default is ENABLED + LIVE for every trader
// (including newly created ones). These fields are per-trader OVERRIDES on top of
// that fleet default — an empty object means "use fleet defaults" (enabled, live,
// min_confidence 75, min_position_age_hours 6).
export interface TrendReversalConfig {
  // disabled hard-disables the fleet-default feature for THIS trader.
  disabled?: boolean
  // force_dry_run pins THIS trader to observe-only even though the fleet is live —
  // used to quarantine a single trader without fully disabling the feature.
  force_dry_run?: boolean
  // live_execution explicitly opts into live (forward-compat; fleet is already live).
  live_execution?: boolean
  // min_confidence is the AI-decision confidence floor (0-100) the opposite signal
  // must clear to flip an existing position. Default 75.
  min_confidence?: number
  // min_position_age_hours is the minimum hold age before a position may be flipped
  // (prevents noise flips inside the first trend leg). Backtest optimum: 6h.
  min_position_age_hours?: number
}

// Portfolio giveback guard: the breadth circuit breaker (per-symbol monitoring +
// majority-retrace gate). Cuts only losing retracing positions; winners ride
// break-even. Leverage-free. Replaced the old L1/L2/L3 account-equity breakers.
export interface GivebackGuardConfig {
  enabled?: boolean
  dry_run?: boolean

  // Breadth breaker. Per-symbol monitoring + majority-retrace gate: fire when
  // retracingCount/total >= breadth_frac AND total >= breadth_min_pos, then cut
  // only LOSING retracing positions (winners ride break-even).
  breadth_enabled?: boolean
  breadth_min_pos?: number // quorum: min open positions before the gate can fire
  breadth_frac?: number // fraction (0..1) of positions retracing that fires the gate
  breadth_loser_cut_pct?: number // % of each losing+retracing position to cut (default 100)
  breadth_use_atr?: boolean // true => retrace measured in ATR-from-peak units
  breadth_atr_mult?: number // adverse-from-peak in ATR units that counts as retracing
  breadth_giveback_pct?: number // peak-to-current giveback% that counts as retracing (pnl% mode)
  breadth_vel_eps?: number // velocity threshold in ATR-units/bar (ATR-normalized); below -eps counts as retracing
  breadth_vel_window?: number // bars of look-back for pnl-velocity (0 => default 6)
  breadth_cooldown_bars?: number // min bars between fires (0 => disabled)
  breadth_cut_winners?: boolean // true => full deleverage: cut retracing winners too (default false = losers only)

  poll_interval_seconds?: number
}

// Grid trading specific configuration
export interface GridStrategyConfig {
  // Trading pair (e.g., "BTCUSDT")
  symbol: string
  // Number of grid levels (5-50)
  grid_count: number
  // Total investment in USDT
  total_investment: number
  // Leverage (1-20)
  leverage: number
  // Upper price boundary (0 = auto-calculate from ATR)
  upper_price: number
  // Lower price boundary (0 = auto-calculate from ATR)
  lower_price: number
  // Use ATR to auto-calculate bounds
  use_atr_bounds: boolean
  // ATR multiplier for bound calculation (default 2.0)
  atr_multiplier: number
  // Position distribution: "uniform" | "gaussian" | "pyramid"
  distribution: 'uniform' | 'gaussian' | 'pyramid'
  // Maximum drawdown percentage before emergency exit
  max_drawdown_pct: number
  // Stop loss percentage per position
  stop_loss_pct: number
  // Daily loss limit percentage
  daily_loss_limit_pct: number
  // Use maker-only orders for lower fees
  use_maker_only: boolean
  // Enable automatic grid direction adjustment based on box breakouts
  enable_direction_adjust?: boolean
  // Direction bias ratio for long_bias/short_bias modes (default 0.7 = 70%/30%)
  direction_bias_ratio?: number
}

export interface CoinSourceConfig {
  source_type: 'static' | 'ai500' | 'oi_top' | 'oi_low' | 'mixed' | 'market'
  static_coins?: string[]
  excluded_coins?: string[] // 排除的币种列表
  // Exchange for data source (default: 'okx')
  exchange_source?: 'binance' | 'okx'
  use_ai500: boolean
  ai500_limit?: number
  use_oi_top: boolean
  oi_top_limit?: number
  use_oi_low: boolean
  oi_low_limit?: number
  // Market source config (used when source_type = "market")
  market_list?: 'hot' | 'oi_top' | 'oi_low' // deprecated: single list
  market_lists?: ('hot' | 'oi_top' | 'oi_low')[] // multi-select market rankings
  market_limit?: number // top N coins from market
  // Note: API URLs are now built automatically using nofxos_api_key from IndicatorConfig
}

export interface IndicatorConfig {
  klines: KlineConfig
  // Raw OHLCV kline data - required for AI analysis
  enable_raw_klines: boolean
  // Technical indicators (optional)
  enable_ema: boolean
  enable_macd: boolean
  enable_rsi: boolean
  enable_atr: boolean
  enable_boll: boolean
  enable_volume: boolean
  enable_oi: boolean
  enable_funding_rate: boolean
  // Exchange sentiment data toggles
  enable_long_short_ratio?: boolean // long/short account ratio
  enable_top_trader_ratio?: boolean // top trader L/S ratio (Binance only)
  enable_taker_buy_sell_ratio?: boolean // taker buy/sell volume ratio
  enable_order_book_depth?: boolean // order book depth imbalance
  ema_periods?: number[]
  rsi_periods?: number[]
  atr_periods?: number[]
  boll_periods?: number[]
  external_data_sources?: ExternalDataSource[]

  // ========== NofxOS 数据源统一配置 ==========
  // Unified NofxOS API Key - used for all NofxOS data sources
  nofxos_api_key?: string

  // 量化数据源（资金流向、持仓变化、价格变化）
  enable_quant_data?: boolean
  enable_quant_oi?: boolean
  enable_quant_netflow?: boolean

  // OI 排行数据（市场持仓量增减排行）
  enable_oi_ranking?: boolean
  oi_ranking_duration?: string // "1h", "4h", "24h"
  oi_ranking_limit?: number

  // NetFlow 排行数据（机构/散户资金流向排行）
  enable_netflow_ranking?: boolean
  netflow_ranking_duration?: string // "1h", "4h", "24h"
  netflow_ranking_limit?: number

  // Price 排行数据（涨跌幅排行）
  enable_price_ranking?: boolean
  price_ranking_duration?: string // "1h", "4h", "24h" or "1h,4h,24h"
  price_ranking_limit?: number

  // 衍生品增强数据（Derivatives Enhancement）
  enable_cvd?: boolean // Cumulative Volume Delta
  enable_oi_growth_rate?: boolean // OI 增长率
  enable_funding_history?: boolean // 资金费率历史趋势
  enable_vwap?: boolean // 成交量加权均价
  enable_taker_delta?: boolean // Taker 买卖压力
  enable_depth_change_rate?: boolean // 盘口深度变化率
}

export interface KlineConfig {
  primary_timeframe: string
  primary_count: number
  longer_timeframe?: string
  longer_count?: number
  enable_multi_timeframe: boolean
  // 新增：支持选择多个时间周期
  selected_timeframes?: string[]
}

export interface ExternalDataSource {
  name: string
  type: 'api' | 'webhook'
  url: string
  method: string
  headers?: Record<string, string>
  data_path?: string
  refresh_secs?: number
}

export interface RiskControlConfig {
  // Max number of coins held simultaneously (CODE ENFORCED)
  max_positions: number

  // Trading Leverage - exchange leverage for opening positions (AI guided)
  btc_eth_max_leverage: number // BTC/ETH max exchange leverage
  altcoin_max_leverage: number // Altcoin max exchange leverage

  // Position Value Ratio - single position notional value / account equity (CODE ENFORCED)
  // Max position value = equity × this ratio
  btc_eth_max_position_value_ratio?: number // default: 5 (BTC/ETH max position = 5x equity)
  altcoin_max_position_value_ratio?: number // default: 1 (Altcoin max position = 1x equity)

  // Risk Parameters
  max_margin_usage: number // Max margin utilization, e.g. 0.9 = 90% (CODE ENFORCED)
  min_position_size: number // Min position size in USDT (CODE ENFORCED)
  min_risk_reward_ratio: number // Min take_profit / stop_loss ratio (AI guided)
  min_confidence: number // Min AI confidence to open position (AI guided)

  // Risk-based position sizing: reverse-compute size from stop distance so a single
  // trade risks at most risk_per_trade_pct_of_equity % of equity. Off = AI-provided size.
  risk_sizing_enabled?: boolean
  risk_per_trade_pct_of_equity?: number // e.g. 3.0 = risk 3% of equity per trade
  // session_pre_open_block_enabled: forbid OPENING new positions in tokenized stock/
  // commodity symbols during the pre-open window before their underlying cash market
  // opens (max overnight-gap risk). Crypto unaffected; exits never gated.
  session_pre_open_block_enabled?: boolean
  session_pre_open_window_minutes?: number // window length; 0 → 60-min default

  // Execution constraints
  entry_cooldown_minutes?: number // Post-loss cooldown per symbol (default: 90)
  max_entry_deviation_pct?: number // Max entry price deviation % (default: 1.5)

  // Binance-only: bind USDC-pair routing + maker take-profit into one toggle.
  // Tri-state: undefined = auto (ON for Binance), true = force ON, false = OFF.
  // Non-Binance exchanges ignore this. Requires USDC margin / multi-asset mode.
  binance_usdc_maker?: boolean
}
