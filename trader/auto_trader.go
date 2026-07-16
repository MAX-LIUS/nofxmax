package trader

import (
	"fmt"
	"github.com/ethereum/go-ethereum/crypto"
	"nofx/kernel"
	"nofx/logger"
	"nofx/market"
	"nofx/mcp"
	_ "nofx/mcp/payment"
	_ "nofx/mcp/provider"
	"nofx/store"
	"nofx/trader/aster"
	"nofx/trader/binance"
	"nofx/trader/bitget"
	"nofx/trader/bybit"
	"nofx/trader/gate"
	"nofx/trader/hyperliquid"
	"nofx/trader/indodax"
	"nofx/trader/kucoin"
	"nofx/trader/lighter"
	"nofx/trader/okx"
	"nofx/wallet"
	"strings"
	"sync"
	"time"
)

// AutoTraderConfig auto trading configuration (simplified version - AI makes all decisions)
type AutoTraderConfig struct {
	// Trader identification
	ID      string // Trader unique identifier (for log directory, etc.)
	Name    string // Trader display name
	AIModel string // AI model: "qwen" or "deepseek"

	// Trading platform selection
	Exchange   string // Exchange type: "binance", "bybit", "okx", "bitget", "gate", "hyperliquid", "aster" or "lighter"
	ExchangeID string // Exchange account UUID (for multi-account support)

	// Binance API configuration
	BinanceAPIKey    string
	BinanceSecretKey string

	// Bybit API configuration
	BybitAPIKey    string
	BybitSecretKey string

	// OKX API configuration
	OKXAPIKey     string
	OKXSecretKey  string
	OKXPassphrase string

	// Bitget API configuration
	BitgetAPIKey     string
	BitgetSecretKey  string
	BitgetPassphrase string

	// Gate API configuration
	GateAPIKey    string
	GateSecretKey string

	// KuCoin API configuration
	KuCoinAPIKey     string
	KuCoinSecretKey  string
	KuCoinPassphrase string

	// Indodax API configuration
	IndodaxAPIKey    string
	IndodaxSecretKey string

	// Hyperliquid configuration
	HyperliquidPrivateKey  string
	HyperliquidWalletAddr  string
	HyperliquidTestnet     bool
	HyperliquidUnifiedAcct bool // Unified Account mode: Spot USDC as Perp collateral

	// Aster configuration
	AsterUser       string // Aster main wallet address
	AsterSigner     string // Aster API wallet address
	AsterPrivateKey string // Aster API wallet private key

	// LIGHTER configuration
	LighterWalletAddr       string // LIGHTER wallet address (L1 wallet)
	LighterPrivateKey       string // LIGHTER L1 private key (for account identification)
	LighterAPIKeyPrivateKey string // LIGHTER API Key private key (40 bytes, for transaction signing)
	LighterAPIKeyIndex      int    // LIGHTER API Key index (0-255)
	LighterTestnet          bool   // Whether to use testnet

	// AI configuration
	UseQwen     bool
	DeepSeekKey string
	QwenKey     string

	// Custom AI API configuration
	CustomAPIURL    string
	CustomAPIKey    string
	CustomModelName string

	// Scan configuration
	ScanInterval time.Duration // Scan interval (recommended 3 minutes)

	// Account configuration
	InitialBalance float64 // Initial balance (for P&L calculation, must be set manually)

	// Risk control (only as hints, AI can make autonomous decisions)
	MaxDailyLoss    float64       // Maximum daily loss percentage (hint)
	MaxDrawdown     float64       // Maximum drawdown percentage (hint)
	StopTradingTime time.Duration // Pause duration after risk control triggers

	// Position mode
	IsCrossMargin bool // true=cross margin mode, false=isolated margin mode

	// Competition visibility
	ShowInCompetition bool    // Whether to show in competition page
	AllowAIOpen       bool    // Whether AI is allowed to issue open_long / open_short
	AllowAIClose      bool    // Whether AI is allowed to issue close_long / close_short
	AllowAIStopClose  bool    // Whether AI can close for stop-loss reasons
	AllowAITakeProfit bool    // Whether AI can close for take-profit reasons
	AIStopMinLossPct  float64 // Minimum unrealized loss % before AI can stop-loss
	AIDecisionMode    string  // conservative | balanced | aggressive

	// Strategy configuration (use complete strategy config)
	StrategyConfig *store.StrategyConfig // Strategy configuration (includes coin sources, indicators, risk control, prompts, etc.)
}

// AutoTrader automatic trader
type AutoTrader struct {
	id                     string  // Trader unique identifier
	name                   string  // Trader display name
	aiModel                string  // AI model name
	exchange               string  // Trading platform type (binance/bybit/etc)
	exchangeID             string  // Exchange account UUID
	ownsAccountProtection  bool    // Whether this instance owns its account's protection lifecycle (account exclusivity)
	showInCompetition      bool    // Whether to show in competition page
	allowAIOpen            bool    // Whether AI can actively open positions
	allowAIClose           bool    // Whether AI can actively close positions
	allowAIStopClose       bool    // Whether AI can close for stop-loss reasons
	allowAITakeProfit      bool    // Whether AI can close for take-profit reasons
	aiStopMinLossPct       float64 // Minimum unrealized loss % before AI can stop-loss
	aiDecisionMode         string  // conservative | balanced | aggressive
	config                 AutoTraderConfig
	trader                 Trader // Use Trader interface (supports multiple platforms)
	mcpClient              mcp.AIClient
	store                  *store.Store           // Data storage (decision records, etc.)
	strategyEngine         *kernel.StrategyEngine // Strategy engine (uses strategy configuration)
	cycleNumber            int                    // Current cycle number
	initialBalance         float64
	dailyPnL               float64
	lastResetTime          time.Time
	stopUntil              time.Time
	isRunning              bool
	isRunningMutex         sync.RWMutex                              // Mutex to protect isRunning flag
	startTime              time.Time                                 // System start time
	callCount              int                                       // AI call count
	positionFirstSeenTime  map[string]int64                          // Position first seen time (symbol_side -> timestamp in milliseconds)
	stopMonitorCh          chan struct{}                             // Used to stop monitoring goroutine
	monitorWg              sync.WaitGroup                            // Used to wait for monitoring goroutine to finish
	peakPnLCache           map[string]float64                        // Peak profit cache (symbol -> peak P&L percentage)
	troughPnLCache         map[string]float64                        // Adverse trough cache (symbol_side -> low-water profit%); mirrors peak
	peakAtrMultCache       map[string]float64                        // Peak excursion in open-time ATR multiples (symbol_side -> |peak dist|/ATR%)
	troughAtrMultCache     map[string]float64                        // Trough excursion in open-time ATR multiples (symbol_side -> |trough dist|/ATR%)
	peakPnLCacheMutex      sync.RWMutex                              // Cache read-write lock (guards peak + trough + ATR-mult caches)
	gbGuardMutex           sync.Mutex                                // Protects giveback-guard breadth state below
	gbPnlHist              map[string][]float64                      // Giveback guard breadth: symbol_side -> recent profit% samples (velocity)
	gbBreadthBarsSinceFire int                                       // Giveback guard breadth: ticks since last breadth fire (cooldown)
	gbLastBreadthBarMs     int64                                     // Giveback guard breadth: ms timestamp of last advanced velocity "bar"
	gbBreadthVelIndex      float64                                   // Breadth pressure index (0-100) for the velocity path, last eval cycle
	gbBreadthPeakIndex     float64                                   // Breadth pressure index (0-100) for the from-peak path, last eval cycle
	gbBreadthIndexAt       int64                                     // ms timestamp the two breadth indices were last computed
	structSLFiredBar       map[string]int64                          // structural SL close-confirm: symbol_side -> last closed-bar openTime that already fired (dedup)
	structSLMutex          sync.Mutex                                // protects structSLFiredBar
	protectionStateMutex   sync.RWMutex                              // Protects last protection reconcile state
	protectionState        map[string]string                         // symbol_side -> last known protection status
	breakEvenStateMutex    sync.RWMutex                              // Protects break-even armed state per position
	breakEvenState         map[string]string                         // symbol_side -> idle/armed
	breakEvenFingerprints  map[string]string                         // symbol_side -> entry/qty fingerprint for lifecycle reset
	breakEvenSource        map[string]string                         // symbol_side -> strategy|ai_decision
	drawdownState          map[string]string                         // symbol_side -> last executed drawdown rule fingerprint
	drawdownSource         map[string]string                         // symbol_side -> strategy|ai_decision
	drawdownAIRules        map[string][]store.DrawdownTakeProfitRule // symbol_side -> per-position AI drawdown rules restored from entry decision
	drawdownRunnerState    map[string]DrawdownRunnerState            // symbol_side -> active runner semantics after partial drawdown
	drawdownTierAllocs     map[string][]store.DrawdownTierAllocation // symbol_side -> fixed tier allocations computed at open
	drawdownTierAllocMu    sync.RWMutex                              // Protects drawdownTierAllocs
	nativeTrailingArmTime  map[string]time.Time                      // fingerprint -> last successful arm time (prevents re-arm loop)
	immediateTrailingIDs   map[string]string                         // symbol_side -> immediate trailing order ID (canceled when tier trailing arms)
	cooldownManager        *entryCooldownManager                     // Post-loss entry cooldown per symbol
	lastBalanceSyncTime    time.Time                                 // Last balance sync time
	userID                 string                                    // User ID
	gridState              *GridState                                // Grid trading state (only used when StrategyType == "grid_trading")
	claw402WalletAddr      string                                    // Claw402 wallet address (derived from private key at start)
	consecutiveAIFailures  int                                       // Consecutive AI call failures
	safeMode               bool                                      // Safe mode: no new positions, protect existing ones
	safeModeReason         string                                    // Why safe mode was activated
	lastMarketDataMap      map[string]*market.Data                   // Market data from current cycle (for scene tag recording)
	lastTriggerTypes       map[string]string                         // Trigger types from current cycle decisions (symbol → trigger_type)
}

// NewAutoTrader creates an automatic trader
// st parameter is used to store decision records to database
func NewAutoTrader(config AutoTraderConfig, st *store.Store, userID string) (*AutoTrader, error) {
	// Set default values
	if config.ID == "" {
		config.ID = "default_trader"
	}
	if config.Name == "" {
		config.Name = "Default Trader"
	}
	if config.AIModel == "" {
		if config.UseQwen {
			config.AIModel = "qwen"
		} else {
			config.AIModel = "deepseek"
		}
	}
	if config.AIDecisionMode == "" {
		config.AIDecisionMode = "balanced"
	}

	// Initialize AI client based on provider
	var mcpClient mcp.AIClient
	aiModel := config.AIModel
	if config.UseQwen && aiModel == "" {
		aiModel = "qwen"
	}

	// Resolve API key (provider-specific overrides)
	apiKey := config.CustomAPIKey
	customURL := config.CustomAPIURL
	switch aiModel {
	case "qwen":
		if config.QwenKey != "" {
			apiKey = config.QwenKey
		}
	case "deepseek", "":
		if config.DeepSeekKey != "" {
			apiKey = config.DeepSeekKey
		}
	}

	// Create client via registry (covers all registered providers)
	if aiModel == "custom" {
		mcpClient = mcp.New()
	} else if aiModel == "" {
		aiModel = "deepseek"
		mcpClient = mcp.NewAIClientByProvider(aiModel)
	} else {
		mcpClient = mcp.NewAIClientByProvider(aiModel)
	}
	if mcpClient == nil {
		mcpClient = mcp.New()
	}

	// Payment providers (blockrun-*, claw402) ignore customURL
	switch aiModel {
	case "blockrun-base", "blockrun-sol", "claw402":
		mcpClient.SetAPIKey(apiKey, "", config.CustomModelName)
	default:
		mcpClient.SetAPIKey(apiKey, customURL, config.CustomModelName)
	}
	logger.Infof("🤖 [%s] Using %s AI", config.Name, aiModel)

	if config.CustomAPIURL != "" || config.CustomModelName != "" {
		logger.Infof("🔧 [%s] Custom config - URL: %s, Model: %s", config.Name, config.CustomAPIURL, config.CustomModelName)
	}

	// Set default trading platform
	if config.Exchange == "" {
		config.Exchange = "binance"
	}

	// Create corresponding trader based on configuration
	var trader Trader
	var err error

	// Record position mode (general)
	marginModeStr := "Cross Margin"
	if !config.IsCrossMargin {
		marginModeStr = "Isolated Margin"
	}
	logger.Infof("📊 [%s] Position mode: %s", config.Name, marginModeStr)

	switch config.Exchange {
	case "binance":
		logger.Infof("🏦 [%s] Using Binance Futures trading", config.Name)
		bt := binance.NewFuturesTrader(config.BinanceAPIKey, config.BinanceSecretKey, userID)
		if config.StrategyConfig != nil {
			// One toggle drives both USDC routing and maker take-profit; auto-ON
			// for Binance unless explicitly disabled in the strategy.
			usdcMaker := config.StrategyConfig.RiskControl.ResolveBinanceUSDCMaker()
			bt.SetExecutionPreferences(usdcMaker, usdcMaker)
		}
		trader = bt
	case "bybit":
		logger.Infof("🏦 [%s] Using Bybit Futures trading", config.Name)
		trader = bybit.NewBybitTrader(config.BybitAPIKey, config.BybitSecretKey)
	case "okx":
		logger.Infof("🏦 [%s] Using OKX Futures trading", config.Name)
		trader = okx.NewOKXTrader(config.OKXAPIKey, config.OKXSecretKey, config.OKXPassphrase)
	case "bitget":
		logger.Infof("🏦 [%s] Using Bitget Futures trading", config.Name)
		trader = bitget.NewBitgetTrader(config.BitgetAPIKey, config.BitgetSecretKey, config.BitgetPassphrase)
	case "gate":
		logger.Infof("🏦 [%s] Using Gate.io Futures trading", config.Name)
		trader = gate.NewGateTrader(config.GateAPIKey, config.GateSecretKey)
	case "kucoin":
		logger.Infof("🏦 [%s] Using KuCoin Futures trading", config.Name)
		trader = kucoin.NewKuCoinTrader(config.KuCoinAPIKey, config.KuCoinSecretKey, config.KuCoinPassphrase)
	case "hyperliquid":
		logger.Infof("🏦 [%s] Using Hyperliquid trading", config.Name)
		trader, err = hyperliquid.NewHyperliquidTrader(config.HyperliquidPrivateKey, config.HyperliquidWalletAddr, config.HyperliquidTestnet, config.HyperliquidUnifiedAcct)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Hyperliquid trader: %w", err)
		}
	case "aster":
		logger.Infof("🏦 [%s] Using Aster trading", config.Name)
		trader, err = aster.NewAsterTrader(config.AsterUser, config.AsterSigner, config.AsterPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Aster trader: %w", err)
		}
	case "lighter":
		logger.Infof("🏦 [%s] Using LIGHTER trading", config.Name)

		if config.LighterWalletAddr == "" || config.LighterAPIKeyPrivateKey == "" {
			return nil, fmt.Errorf("Lighter requires wallet address and API Key private key")
		}

		// Lighter only supports mainnet (testnet disabled)
		trader, err = lighter.NewLighterTraderV2(
			config.LighterWalletAddr,
			config.LighterAPIKeyPrivateKey,
			config.LighterAPIKeyIndex,
			false, // Always use mainnet for Lighter
		)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize LIGHTER trader: %w", err)
		}
		logger.Infof("✓ LIGHTER trader initialized successfully")
	case "indodax":
		logger.Infof("🏦 [%s] Using Indodax Spot trading", config.Name)
		trader = indodax.NewIndodaxTrader(config.IndodaxAPIKey, config.IndodaxSecretKey)
	default:
		return nil, fmt.Errorf("unsupported trading platform: %s", config.Exchange)
	}

	// Validate initial balance configuration.
	// Keep startup resilient: if InitialBalance is not set, do NOT block trader creation on
	// a live exchange balance fetch here. Startup/health should come up even when the
	// exchange is slow or temporarily unreachable. Users can sync balance later, and runtime
	// account info will still come from the exchange when available.
	if config.InitialBalance <= 0 {
		logger.Infof("⚠️ [%s] Initial balance not set; using 0 as temporary baseline and skipping startup balance fetch", config.Name)
	}

	// Get last cycle number (for recovery)
	var cycleNumber int
	if st != nil {
		cycleNumber, _ = st.Decision().GetLastCycleNumber(config.ID)
		logger.Infof("📊 [%s] Decision records will be stored to database", config.Name)
	}

	// Create strategy engine (must have strategy config)
	if config.StrategyConfig == nil {
		return nil, fmt.Errorf("[%s] strategy not configured", config.Name)
	}
	strategyEngine := kernel.NewStrategyEngine(config.StrategyConfig)
	logger.Infof("✓ [%s] Using strategy engine (strategy configuration loaded)", config.Name)

	return &AutoTrader{
		id:                    config.ID,
		name:                  config.Name,
		aiModel:               config.AIModel,
		exchange:              config.Exchange,
		exchangeID:            config.ExchangeID,
		showInCompetition:     config.ShowInCompetition,
		allowAIOpen:           config.AllowAIOpen,
		allowAIClose:          config.AllowAIClose,
		allowAIStopClose:      config.AllowAIStopClose,
		allowAITakeProfit:     config.AllowAITakeProfit,
		aiStopMinLossPct:      config.AIStopMinLossPct,
		aiDecisionMode:        config.AIDecisionMode,
		config:                config,
		trader:                trader,
		mcpClient:             mcpClient,
		store:                 st,
		strategyEngine:        strategyEngine,
		cycleNumber:           cycleNumber,
		initialBalance:        config.InitialBalance,
		lastResetTime:         time.Now(),
		startTime:             time.Now(),
		callCount:             0,
		isRunning:             false,
		positionFirstSeenTime: make(map[string]int64),
		stopMonitorCh:         make(chan struct{}),
		monitorWg:             sync.WaitGroup{},
		peakPnLCache:          make(map[string]float64),
		troughPnLCache:        make(map[string]float64),
		peakAtrMultCache:      make(map[string]float64),
		troughAtrMultCache:    make(map[string]float64),
		peakPnLCacheMutex:     sync.RWMutex{},
		gbPnlHist:             make(map[string][]float64),
		protectionStateMutex:  sync.RWMutex{},
		protectionState:       make(map[string]string),
		breakEvenStateMutex:   sync.RWMutex{},
		breakEvenState:        make(map[string]string),
		breakEvenFingerprints: make(map[string]string),
		drawdownState:         make(map[string]string),
		breakEvenSource:       make(map[string]string),
		drawdownSource:        make(map[string]string),
		drawdownAIRules:       make(map[string][]store.DrawdownTakeProfitRule),
		drawdownRunnerState:   make(map[string]DrawdownRunnerState),
		drawdownTierAllocs:    make(map[string][]store.DrawdownTierAllocation),
		nativeTrailingArmTime: make(map[string]time.Time),
		immediateTrailingIDs:  make(map[string]string),
		cooldownManager:       newEntryCooldownManagerFromConfig(config),
		lastBalanceSyncTime:   time.Now(),
		userID:                userID,
	}, nil
}

// loadPeakPnLFromStore restores the peak-PnL high-water cache from the DB so
// that, after a restart, peak/drawdown tiers and the UI keep the highest profit
// the position ever reached instead of resetting to the current PnL.
func (at *AutoTrader) loadPeakPnLFromStore() {
	if at == nil || at.store == nil {
		return
	}
	excursions, err := at.store.LoadExcursionForTrader(at.id)
	if err != nil {
		logger.Warnf("⚠️ Excursion: failed to load persisted state: %v", err)
		return
	}
	if len(excursions) == 0 {
		return
	}

	// Only restore peaks for positions that are still OPEN. Positions closed while
	// the process was down never fired ClearPeakPnLCache, so their persisted rows
	// would otherwise resurrect a stale high-water mark for a future re-open.
	openKeys := make(map[string]bool)
	if openPositions, posErr := at.store.Position().GetOpenPositions(at.id); posErr == nil {
		for _, p := range openPositions {
			// Cache keys use the exchange-reported side casing (e.g. OKX "long"),
			// while DB stores uppercase. Compare case-insensitively.
			openKeys[strings.ToLower(p.Symbol+"_"+p.Side)] = true
		}
	} else {
		logger.Warnf("⚠️ Peak PnL: failed to list open positions for restore filter: %v", posErr)
	}

	at.peakPnLCacheMutex.Lock()
	restored := 0
	stale := make([]string, 0)
	for posKey, ex := range excursions {
		if openKeys[strings.ToLower(posKey)] {
			at.peakPnLCache[posKey] = ex.PeakPnlPct
			at.troughPnLCache[posKey] = ex.TroughPnlPct
			at.peakAtrMultCache[posKey] = ex.PeakAtrMult
			at.troughAtrMultCache[posKey] = ex.TroughAtrMult
			restored++
		} else {
			stale = append(stale, posKey)
		}
	}
	at.peakPnLCacheMutex.Unlock()

	// Clean up stale persisted rows for positions no longer open.
	for _, posKey := range stale {
		if delErr := at.store.DeletePeakPnL(at.id, posKey); delErr != nil {
			logger.Warnf("⚠️ Excursion: failed to delete stale row %s: %v", posKey, delErr)
		}
	}
	logger.Infof("🔁 Excursion: restored %d position excursion(s), pruned %d stale", restored, len(stale))
}

// loadBreadthVelocityStateFromStore restores the breadth breaker's velocity
// history + bar clock so the velocity path does not need a 2–6h warm-up after a
// restart. Staleness guard: if the persisted snapshot is older than 2 bars (2h),
// it is discarded and the guard starts from an empty history — this prevents
// computing a velocity across a downtime gap, which would otherwise produce a
// bogus slope on the first post-restart bar and could misfire. Only history for
// still-open positions is restored.
func (at *AutoTrader) loadBreadthVelocityStateFromStore() {
	if at == nil || at.store == nil {
		return
	}
	st, err := at.store.LoadBreadthVelocityState(at.id)
	if err != nil {
		logger.Warnf("⚠️ GivebackGuard Breadth: failed to load velocity state: %v", err)
		return
	}
	if len(st.PnlHist) == 0 {
		return // nothing persisted yet — normal cold start
	}

	const breadthBarMs int64 = 3600_000
	ageMs := time.Now().UnixMilli() - st.LastBarMs
	// stale = the persisted snapshot is older than 2 bars (2h), e.g. across a
	// downtime gap or a config hot-reload. We DON'T discard history (that left the
	// velocity path with too few samples and made the breaker hypersensitive for
	// hours — it fired on 2 thin samples). Instead we KEEP the most recent samples
	// (capped at staleKeepBars) but reset the bar clock to now, so the retained
	// samples stay internally contiguous and the next live sample appends cleanly
	// without computing a slope across the downtime gap. The window-floor in
	// gbApplyBreadth still prevents firing until enough samples re-accumulate.
	stale := st.LastBarMs <= 0 || ageMs > 2*breadthBarMs
	const staleKeepBars = 10

	// Prune to still-open positions (same rationale as peak-PnL restore).
	openKeys := make(map[string]bool)
	if openPositions, posErr := at.store.Position().GetOpenPositions(at.id); posErr == nil {
		for _, p := range openPositions {
			openKeys[strings.ToLower(p.Symbol+"_"+p.Side)] = true
		}
	}
	at.gbGuardMutex.Lock()
	restored := 0
	for posKey, hist := range st.PnlHist {
		if len(openKeys) == 0 || openKeys[strings.ToLower(posKey)] {
			if stale && len(hist) > staleKeepBars {
				hist = hist[len(hist)-staleKeepBars:] // keep most-recent samples only
			}
			at.gbPnlHist[posKey] = hist
			restored++
		}
	}
	if stale {
		// Reset the bar clock to now so the next sampled bar does not span the gap;
		// retained samples remain valid for velocity (they are internally contiguous).
		at.gbLastBreadthBarMs = time.Now().UnixMilli()
		at.gbBreadthBarsSinceFire = 0
	} else {
		at.gbLastBreadthBarMs = st.LastBarMs
		at.gbBreadthBarsSinceFire = st.BarsSinceFire
	}
	at.gbGuardMutex.Unlock()
	logger.Infof("🔁 GivebackGuard Breadth: restored velocity history for %d position(s) (age=%.1fh, stale=%v, barsSinceFire=%d)", restored, float64(ageMs)/3600000.0, stale, at.gbBreadthBarsSinceFire)
}

// restoreEntryCooldownsFromStore rebuilds post-loss entry cooldowns after a
// restart. The cooldown map is memory-only and is normally set when a live
// position transitions to closed; on restart that transition is never observed,
// so a symbol just stopped out at a loss would be immediately re-tradable,
// bypassing the consecutive-loss cooldown (a real risk control). We recompute
// each symbol's cooldown deadline from its last closed trade: deadline =
// exitTime + duration*consecutiveLosses, and re-arm any still in the future.
func (at *AutoTrader) restoreEntryCooldownsFromStore() {
	if at == nil || at.store == nil || at.cooldownManager == nil {
		return
	}
	// Look back over recent closed trades; cooldown maxes at 4x duration, so any
	// loss older than 4*duration cannot still be cooling. Scan a generous window.
	trades, err := at.store.Position().GetRecentTrades(at.id, 50)
	if err != nil {
		logger.Warnf("⚠️ Entry cooldown: failed to load recent trades for restore: %v", err)
		return
	}
	// Only the latest trade per symbol matters. Cooldown blocks entry, so a win
	// on a symbol can only happen after any prior loss-cooldown already expired —
	// meaning if the most recent trade is a win, there is no active cooldown. We
	// therefore consider just the first (latest) trade seen per symbol and arm a
	// cooldown only when that latest trade is a loss. GetRecentTrades is exit_time
	// DESC, so the first trade seen per symbol is the latest.
	seen := make(map[string]bool)
	restored := 0
	for _, t := range trades {
		if seen[t.Symbol] || t.ExitTime <= 0 {
			continue
		}
		seen[t.Symbol] = true // latest trade for this symbol decided; ignore older ones
		if t.RealizedPnL >= 0 {
			continue // latest trade was a win → no active cooldown
		}
		consecutiveLosses := at.store.Position().GetConsecutiveLossCount(at.id, t.Symbol, t.Side)
		if consecutiveLosses < 1 {
			consecutiveLosses = 1
		}
		if consecutiveLosses > 4 {
			consecutiveLosses = 4
		}
		// t.ExitTime is in seconds (GetRecentTrades divides ms by 1000).
		until := time.Unix(t.ExitTime, 0).Add(at.cooldownManager.duration * time.Duration(consecutiveLosses))
		if at.cooldownManager.RestoreCooldown(t.Symbol, until) {
			restored++
			logger.Infof("⏳ [%s] Entry cooldown restored for %s (%dx, until %s)",
				at.name, t.Symbol, consecutiveLosses, until.Format("15:04:05"))
		}
	}
	if restored > 0 {
		logger.Infof("🔁 Entry cooldown: restored %d active post-loss cooldown(s)", restored)
	}
}

func (at *AutoTrader) loadDynamicProtectionStateFromStore() {
	if at == nil || at.store == nil {
		return
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil {
		logger.Warnf("⚠️ Dynamic protection state: failed to load persisted state: %v", err)
		return
	}
	if len(state.Records) == 0 {
		return
	}
	if at.protectionState == nil {
		at.protectionState = make(map[string]string)
	}
	if at.drawdownState == nil {
		at.drawdownState = make(map[string]string)
	}
	if at.breakEvenState == nil {
		at.breakEvenState = make(map[string]string)
	}
	if at.breakEvenFingerprints == nil {
		at.breakEvenFingerprints = make(map[string]string)
	}
	latestBreakEvenRecord := make(map[string]store.DynamicProtectionRecord)
	managedRecords := make([]store.DynamicProtectionRecord, 0, len(state.Records))
	for _, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if record.Status != "armed" && record.Status != "executed" {
			continue
		}
		if record.Status == "executed" && record.ProtectionType != "managed_drawdown" {
			continue
		}
		key := positionKey(record.Symbol, record.Side)
		if isDynamicNativeProtectionType(record.ProtectionType) {
			switch record.ProtectionType {
			case "native_trailing":
				at.protectionState[key] = "native_trailing_armed"
			case "native_partial_trailing":
				if at.protectionState[key] != "native_trailing_armed" {
					at.protectionState[key] = "native_partial_trailing_armed"
				}
			}
			if record.RuleFingerprint != "" {
				at.drawdownState[key] = record.RuleFingerprint
			}
		}
		if record.ProtectionType == "break_even_stop" {
			existingRecord, hasExisting := latestBreakEvenRecord[key]
			if !hasExisting || record.UpdatedAt >= existingRecord.UpdatedAt {
				latestBreakEvenRecord[key] = record
			}
		}
		managedRecords = append(managedRecords, record)
	}
	for _, record := range latestBreakEvenRecord {
		key := positionKey(record.Symbol, record.Side)
		at.breakEvenState[key] = "armed"
		if record.PositionFingerprint != "" {
			at.breakEvenFingerprints[key] = record.PositionFingerprint
		}
	}
	for _, record := range managedRecords {
		if record.ProtectionType != "managed_drawdown" || record.RuleFingerprint == "" {
			continue
		}
		at.drawdownState[positionKey(record.Symbol, record.Side)] = record.RuleFingerprint
	}
	logger.Infof("🧷 Dynamic protection state: loaded %d persisted records", len(state.Records))
}

func isDynamicNativeProtectionType(protectionType string) bool {
	return protectionType == "native_trailing" || protectionType == "native_partial_trailing"
}

// Run runs the automatic trading main loop
func (at *AutoTrader) Run() error {
	at.isRunningMutex.Lock()
	at.isRunning = true
	at.isRunningMutex.Unlock()

	at.stopMonitorCh = make(chan struct{})
	at.startTime = time.Now()

	logger.Info("🚀 AI-driven automatic trading system started")
	at.loadDynamicProtectionStateFromStore()
	at.loadPeakPnLFromStore()
	at.loadBreadthVelocityStateFromStore()
	at.restoreEntryCooldownsFromStore()
	logger.Infof("💰 Initial balance: %.2f USDT", at.initialBalance)
	logger.Infof("⚙️  Scan interval: %v", at.config.ScanInterval)
	logger.Info("🤖 AI will make full decisions on leverage, position size, stop loss/take profit, etc.")

	// Pre-launch checks for claw402 users
	at.runPreLaunchChecks()
	at.monitorWg.Add(1)
	defer at.monitorWg.Done()

	// Start drawdown monitoring
	// Account exclusivity: only the sole protection owner of this exchange account
	// may run the reconciler/drawdown monitor. A second instance sharing the same
	// account would fight over the shared net position's protection orders.
	at.ownsAccountProtection = claimAccountProtectionOwnership(at.exchangeID, at.id)
	if at.ownsAccountProtection {
		at.startDrawdownMonitor()
		at.startProtectionReconciler()
	} else {
		logger.Warnf("⚠️ [%s] Protection reconciler/drawdown monitor DISABLED: exchange account %s is already owned by another active trader instance (shared-account conflict prevented)", at.name, at.exchangeID)
	}

	// Start Lighter order sync if using Lighter exchange
	if at.exchange == "lighter" {
		if lighterTrader, ok := at.trader.(*lighter.LighterTraderV2); ok && at.store != nil {
			lighterTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Lighter order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start Hyperliquid order sync if using Hyperliquid exchange
	if at.exchange == "hyperliquid" {
		if hyperliquidTrader, ok := at.trader.(*hyperliquid.HyperliquidTrader); ok && at.store != nil {
			hyperliquidTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Hyperliquid order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start Bybit order sync if using Bybit exchange
	if at.exchange == "bybit" {
		if bybitTrader, ok := at.trader.(*bybit.BybitTrader); ok && at.store != nil {
			bybitTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Bybit order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start OKX order sync if using OKX exchange
	if at.exchange == "okx" {
		if okxTrader, ok := at.trader.(*okx.OKXTrader); ok && at.store != nil {
			okxTrader.StartOrderSyncWithFullCloseHandler(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second, at.handleSyncedFullClose)
			logger.Infof("🔄 [%s] OKX order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start Bitget order sync if using Bitget exchange
	if at.exchange == "bitget" {
		if bitgetTrader, ok := at.trader.(*bitget.BitgetTrader); ok && at.store != nil {
			bitgetTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Bitget order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start Aster order sync if using Aster exchange
	if at.exchange == "aster" {
		if asterTrader, ok := at.trader.(*aster.AsterTrader); ok && at.store != nil {
			asterTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Aster order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start Binance order sync if using Binance exchange
	if at.exchange == "binance" {
		if binanceTrader, ok := at.trader.(*binance.FuturesTrader); ok && at.store != nil {
			binanceTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Binance order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start Gate order sync if using Gate exchange
	if at.exchange == "gate" {
		if gateTrader, ok := at.trader.(*gate.GateTrader); ok && at.store != nil {
			gateTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] Gate order+position sync enabled (every 30s)", at.name)
		}
	}

	// Start KuCoin order sync if using KuCoin exchange
	if at.exchange == "kucoin" {
		if kucoinTrader, ok := at.trader.(*kucoin.KuCoinTrader); ok && at.store != nil {
			kucoinTrader.StartOrderSync(at.id, at.exchangeID, at.exchange, at.store, 30*time.Second)
			logger.Infof("🔄 [%s] KuCoin order+position sync enabled (every 30s)", at.name)
		}
	}

	ticker := time.NewTicker(at.config.ScanInterval)
	defer ticker.Stop()

	// Check if this is a grid trading strategy
	isGridStrategy := at.IsGridStrategy()
	if isGridStrategy {
		logger.Infof("🔲 [%s] Grid trading strategy detected, initializing grid...", at.name)
		if err := at.InitializeGrid(); err != nil {
			logger.Errorf("❌ [%s] Failed to initialize grid: %v", at.name, err)
			return fmt.Errorf("grid initialization failed: %w", err)
		}
	} else if at.IsBreakoutStrategy() {
		logger.Infof("🟦 [%s] Breakout trading strategy detected (data-validated edge + AI soft-veto sizing)", at.name)
	}

	// Execute immediately on first run
	at.executeCycleByType(isGridStrategy)

	for {
		at.isRunningMutex.RLock()
		running := at.isRunning
		at.isRunningMutex.RUnlock()

		if !running {
			break
		}

		select {
		case <-ticker.C:
			at.executeCycleByType(isGridStrategy)
		case <-at.stopMonitorCh:
			logger.Infof("[%s] ⏹ Stop signal received, exiting automatic trading main loop", at.name)
			return nil
		}
	}

	return nil
}

// Stop stops the automatic trading
func (at *AutoTrader) Stop() {
	at.isRunningMutex.Lock()
	if !at.isRunning {
		at.isRunningMutex.Unlock()
		return
	}
	at.isRunning = false
	at.isRunningMutex.Unlock()

	close(at.stopMonitorCh) // Notify monitoring goroutine to stop
	at.monitorWg.Wait()     // Wait for monitoring goroutine to finish
	// Release account protection ownership so a future instance on the same
	// exchange account can take over after this one stops.
	if at.ownsAccountProtection {
		releaseAccountProtectionOwnership(at.exchangeID, at.id)
		at.ownsAccountProtection = false
	}
	logger.Info("⏹ Automatic trading system stopped")
}

// GetID gets trader ID
func (at *AutoTrader) GetID() string {
	return at.id
}

// GetUnderlyingTrader returns the underlying Trader interface implementation
// This is used by grid trading and other components that need direct exchange access
func (at *AutoTrader) GetUnderlyingTrader() Trader {
	return at.trader
}

// GetName gets trader name
func (at *AutoTrader) GetName() string {
	return at.name
}

// GetAIModel gets AI model
func (at *AutoTrader) GetAIModel() string {
	return at.aiModel
}

// GetExchange gets exchange
func (at *AutoTrader) GetExchange() string {
	return at.exchange
}

// GetShowInCompetition returns whether trader should be shown in competition
func (at *AutoTrader) GetShowInCompetition() bool {
	return at.showInCompetition
}

// GetAllowAIOpen returns whether AI can actively open positions
func (at *AutoTrader) GetAllowAIOpen() bool {
	return at.allowAIOpen
}

// SetAllowAIOpen updates whether AI can actively open positions
func (at *AutoTrader) SetAllowAIOpen(allow bool) {
	at.allowAIOpen = allow
}

// GetAllowAIClose returns whether AI can actively close positions
func (at *AutoTrader) GetAllowAIClose() bool {
	return at.allowAIClose
}

// SetProtectOnlyMode enables/disables protect-only safe mode. In this mode the
// trader loop, order sync, drawdown monitor, and protection reconciler can run,
// but AI open decisions are blocked by the existing safe-mode execution gate.
func (at *AutoTrader) SetProtectOnlyMode(enabled bool, reason string) {
	if enabled {
		if reason == "" {
			reason = "protect-only mode requested"
		}
		at.safeMode = true
		at.safeModeReason = reason
		return
	}
	at.safeMode = false
	at.safeModeReason = ""
}

// ClearSafeMode manually exits transient safe mode. Protect-only mode is also
// cleared because this is an explicit operator recovery action.
func (at *AutoTrader) ClearSafeMode(reason string) {
	at.safeMode = false
	at.safeModeReason = ""
	at.consecutiveAIFailures = 0
	if reason == "" {
		reason = "manual safe-mode clear"
	}
	logger.Warnf("🛡️ [%s] SAFE MODE CLEARED — %s", at.name, reason)
}

// SetAllowAIClose updates whether AI can actively close positions
func (at *AutoTrader) SetAllowAIClose(allow bool) {
	at.allowAIClose = allow
}

// GetAllowAIStopClose returns whether AI can close for stop-loss reasons
func (at *AutoTrader) GetAllowAIStopClose() bool {
	return at.allowAIStopClose
}

// SetAllowAIStopClose updates whether AI can close for stop-loss reasons
func (at *AutoTrader) SetAllowAIStopClose(allow bool) {
	at.allowAIStopClose = allow
}

// GetAllowAITakeProfit returns whether AI can close for take-profit reasons
func (at *AutoTrader) GetAllowAITakeProfit() bool {
	return at.allowAITakeProfit
}

// SetAllowAITakeProfit updates whether AI can close for take-profit reasons
func (at *AutoTrader) SetAllowAITakeProfit(allow bool) {
	at.allowAITakeProfit = allow
}

// GetAIStopMinLossPct returns the minimum loss % threshold for AI stop-loss
func (at *AutoTrader) GetAIStopMinLossPct() float64 {
	if at.aiStopMinLossPct <= 0 {
		return 0.5
	}
	return at.aiStopMinLossPct
}

// SetAIStopMinLossPct updates the minimum loss % threshold for AI stop-loss
func (at *AutoTrader) SetAIStopMinLossPct(pct float64) {
	at.aiStopMinLossPct = pct
}

// GetAIDecisionMode returns the configured AI decision mode
func (at *AutoTrader) GetAIDecisionMode() string {
	if at.aiDecisionMode == "" {
		return "balanced"
	}
	return at.aiDecisionMode
}

// SetAIDecisionMode updates the AI decision mode
func (at *AutoTrader) SetAIDecisionMode(mode string) {
	if mode == "" {
		mode = "balanced"
	}
	at.aiDecisionMode = mode
}

// SetShowInCompetition sets whether trader should be shown in competition
func (at *AutoTrader) SetShowInCompetition(show bool) {
	at.showInCompetition = show
}

// SetFallbackEndpoints configures fallback endpoints for the AI client
func (at *AutoTrader) SetFallbackEndpoints(endpoints []store.FallbackEndpoint) {
	if at.mcpClient == nil {
		return
	}
	// Convert store.FallbackEndpoint to mcp.FallbackEndpoint
	mcpEndpoints := make([]mcp.FallbackEndpoint, len(endpoints))
	for i, ep := range endpoints {
		mcpEndpoints[i] = mcp.FallbackEndpoint{
			Name:     ep.Name,
			BaseURL:  ep.BaseURL,
			APIKey:   ep.APIKey,
			Model:    ep.Model,
			Priority: ep.Priority,
		}
	}
	at.mcpClient.SetFallbackEndpoints(mcpEndpoints)
}

// GetSystemPromptTemplate gets current system prompt template name (from strategy config)
func (at *AutoTrader) GetSystemPromptTemplate() string {
	if at.strategyEngine != nil {
		config := at.strategyEngine.GetConfig()
		if config.CustomPrompt != "" {
			return "custom"
		}
	}
	return "strategy"
}

// GetStore gets data store (for external access to decision records, etc.)
func (at *AutoTrader) GetStore() *store.Store {
	return at.store
}

// calculatePnLPercentage calculates P&L percentage (based on margin, automatically considers leverage)
// Return rate = Unrealized P&L / Margin x 100%
func calculatePnLPercentage(unrealizedPnl, marginUsed float64) float64 {
	if marginUsed > 0 {
		return (unrealizedPnl / marginUsed) * 100
	}
	return 0.0
}

// runPreLaunchChecks performs pre-launch checks for claw402 users (wallet balance, runway estimate)
func (at *AutoTrader) runPreLaunchChecks() {
	if !store.IsClaw402Config(at.config.AIModel) {
		return
	}

	logger.Info("🔍 Running pre-launch checks (claw402)...")

	// Derive wallet address from CustomAPIKey (which is the private key for claw402)
	if at.config.CustomAPIKey != "" {
		// Try to derive address using go-ethereum
		addr := deriveWalletAddress(at.config.CustomAPIKey)
		if addr != "" {
			at.claw402WalletAddr = addr
			logger.Infof("💳 [%s] Claw402 wallet: %s", at.name, addr)

			// Query USDC balance
			balance, err := wallet.QueryUSDCBalance(addr)
			if err != nil {
				logger.Warnf("⚠️ [%s] Could not query USDC balance: %v", at.name, err)
			} else {
				// Estimate runway
				scanMinutes := int(at.config.ScanInterval.Minutes())
				modelName := at.config.CustomModelName
				if modelName == "" {
					modelName = "deepseek"
				}
				dailyCost, runway := store.EstimateRunway(balance, modelName, scanMinutes)
				logger.Infof("💰 [%s] USDC Balance: $%.2f | Daily AI cost: ~$%.2f | Runway: ~%.1f days",
					at.name, balance, dailyCost, runway)

				if balance < 1.0 {
					logger.Warnf("⚠️ [%s] Low USDC balance! Consider topping up.", at.name)
				}
				if balance <= 0 {
					logger.Errorf("🚨 [%s] USDC balance is ZERO — AI calls will fail!", at.name)
				}
			}
		}
	}

	logger.Info("✅ Pre-launch checks complete")
}

// deriveWalletAddress derives an Ethereum address from a hex private key
func deriveWalletAddress(privateKeyHex string) string {
	// Remove 0x prefix if present
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}

	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return ""
	}

	address := crypto.PubkeyToAddress(privateKey.PublicKey)
	return address.Hex()
}
