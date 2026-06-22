package store

// UnifiedProtectionConfig 统一保护配置（支持 R 值和百分比双模式）
// 这是对现有 ProtectionConfig 的扩展，保持向后兼容
type UnifiedProtectionConfig struct {
	// 全局设置
	Enabled       bool           `json:"enabled"`        // 保护系统总开关
	Unit          ProtectionUnit `json:"unit"`           // 计量单位: "R" 或 "percentage"
	RiskPerTrade  float64        `json:"risk_per_trade"` // 1R = 账户的 X% (仅 R 模式使用)

	// 基础保护（兼容现有 FullTPSL）
	StopLoss  UnifiedProtectionValue `json:"stop_loss"`
	TakeProfit UnifiedProtectionValue `json:"take_profit"`

	// 高级保护（新增）
	TrailingTP    TrailingTPConfig    `json:"trailing_tp"`     // 追踪止盈
	BreakEvenStop BreakEvenStopConfig `json:"break_even_stop"` // 保本止损（与现有兼容）
	LadderTP      LadderTPConfig      `json:"ladder_tp"`       // 分批止盈

	// 市场过滤
	MarketFilters MarketFiltersConfig `json:"market_filters"`

	// 趋势转换分级响应（默认关闭/仅记录，反手为高风险需显式开启）
	TrendResponse TrendResponseConfig `json:"trend_response"`

	// 向后兼容：保留原有配置
	Legacy *ProtectionConfig `json:"legacy,omitempty"` // 如果存在，优先使用新配置
}

// TrendResponseConfig 趋势转换分级响应配置。
// MaxActionLevel 控制允许的最高动作级别（默认 0 = 仅记录，不交易）：
//
//	0 = 仅记录转换日志，不做任何仓位动作（最安全，可观察）
//	1 = 允许 Level 1（收紧/启动追踪止盈）
//	2 = 允许至 Level 2（减仓 50%）
//	3 = 允许至 Level 3（清仓，不反手）
//	4 = 允许至 Level 4（清仓 + 反手，高风险，需显式开启）
type TrendResponseConfig struct {
	Enabled            bool `json:"enabled"`             // 是否启用趋势监控
	MaxActionLevel     int  `json:"max_action_level"`    // 允许的最高动作级别 0-4
	ConfirmationCycles int  `json:"confirmation_cycles"` // 同一转换需连续确认的 cycle 数
}

// ProtectionUnit 保护计量单位
type ProtectionUnit string

const (
	ProtectionUnitR          ProtectionUnit = "R"          // R 值模式
	ProtectionUnitPercentage ProtectionUnit = "percentage" // 百分比模式
)

// UnifiedProtectionValue 统一保护值（支持 R 和百分比）
type UnifiedProtectionValue struct {
	Enabled      bool                `json:"enabled"`       // 是否启用
	DecisionMode ProtectionValueMode `json:"decision_mode"` // "manual" 或 "ai"
	ValueR       float64             `json:"value_r"`       // R 值
	ValuePercent float64             `json:"value_percent"` // 百分比值
}

// TrailingTPConfig 追踪止盈配置
type TrailingTPConfig struct {
	Enabled      bool    `json:"enabled"`       // 是否启用
	ActivationR  float64 `json:"activation_r"`  // 启动条件（达到多少 R）
	PullbackR    float64 `json:"pullback_r"`    // 回撤触发（回撤多少 R）
	MaxTargetR   float64 `json:"max_target_r"`  // 最大目标 R
	TrailingMode string  `json:"trailing_mode"` // "fixed" 或 "dynamic"
}

// LadderTPConfig 分批止盈配置
type LadderTPConfig struct {
	Enabled bool               `json:"enabled"` // 是否启用
	Levels  []LadderTPLevel    `json:"levels"`  // 分批级别
}

// LadderTPLevel 分批止盈级别
type LadderTPLevel struct {
	TargetR     float64 `json:"target_r"`      // 目标 R 值
	SizePercent float64 `json:"size_percent"`  // 平仓百分比
}

// MarketFiltersConfig 市场过滤配置
type MarketFiltersConfig struct {
	VolatilityFilter   VolatilityFilterConfig   `json:"volatility_filter"`
	FundingRateFilter  FundingRateFilterConfig  `json:"funding_rate_filter"`
	DrawdownProtection DrawdownProtectionConfig `json:"drawdown_protection"`
}

// VolatilityFilterConfig 波动性过滤
type VolatilityFilterConfig struct {
	Enabled bool    `json:"enabled"` // 是否启用
	MaxATR  float64 `json:"max_atr"` // 最大 ATR (%)
}

// FundingRateFilterConfig 资金费率过滤
type FundingRateFilterConfig struct {
	Enabled bool    `json:"enabled"`  // 是否启用
	MaxRate float64 `json:"max_rate"` // 最大费率 (%)
}

// DrawdownProtectionConfig 回撤保护
type DrawdownProtectionConfig struct {
	Enabled     bool    `json:"enabled"`      // 是否启用
	MaxDrawdown float64 `json:"max_drawdown"` // 最大回撤 (%)
}

// ToRValue 将当前单位的值转换为 R 值
func (upv *UnifiedProtectionValue) ToRValue(unit ProtectionUnit, riskPerTrade float64) float64 {
	if unit == ProtectionUnitR {
		return upv.ValueR
	}
	// 百分比转 R：百分比 / riskPerTrade
	if riskPerTrade == 0 {
		return 0
	}
	return upv.ValuePercent / riskPerTrade
}

// ToPercentage 将当前单位的值转换为百分比
func (upv *UnifiedProtectionValue) ToPercentage(unit ProtectionUnit, riskPerTrade float64) float64 {
	if unit == ProtectionUnitPercentage {
		return upv.ValuePercent
	}
	// R 转百分比：R * riskPerTrade
	return upv.ValueR * riskPerTrade
}

// GetEffectiveValue 获取当前单位下的有效值
func (upv *UnifiedProtectionValue) GetEffectiveValue(unit ProtectionUnit) float64 {
	if unit == ProtectionUnitR {
		return upv.ValueR
	}
	return upv.ValuePercent
}

// MigrateFromLegacy 从旧配置迁移到新配置
func (upc *UnifiedProtectionConfig) MigrateFromLegacy(legacy *ProtectionConfig) {
	if legacy == nil {
		return
	}

	// 保存原配置
	upc.Legacy = legacy

	// 迁移 FullTPSL
	if legacy.FullTPSL.Enabled {
		upc.Enabled = true
		upc.Unit = ProtectionUnitPercentage // 默认使用百分比

		// 止损
		if legacy.FullTPSL.StopLossEnabled {
			upc.StopLoss.Enabled = true
			upc.StopLoss.DecisionMode = ProtectionValueMode(legacy.FullTPSL.Mode)
			upc.StopLoss.ValuePercent = legacy.FullTPSL.StopLoss.Value
		}

		// 止盈
		if legacy.FullTPSL.TakeProfitEnabled {
			upc.TakeProfit.Enabled = true
			upc.TakeProfit.DecisionMode = ProtectionValueMode(legacy.FullTPSL.Mode)
			upc.TakeProfit.ValuePercent = legacy.FullTPSL.TakeProfit.Value
		}
	}

	// 迁移 BreakEvenStop
	if legacy.BreakEvenStop.Enabled {
		upc.BreakEvenStop.Enabled = true
		// BreakEvenStop 的字段已经兼容
	}

	// 迁移 DrawdownTakeProfit（可以映射到 TrailingTP）
	if legacy.DrawdownTakeProfit.Enabled {
		upc.TrailingTP.Enabled = true
		upc.TrailingTP.TrailingMode = "dynamic"
		// 设置默认值
		upc.TrailingTP.ActivationR = 1.0
		upc.TrailingTP.PullbackR = 0.5
		upc.TrailingTP.MaxTargetR = 5.0
	}
}

// ToLegacyFullTPSL 转换为旧的 FullTPSL 配置（用于向后兼容）
func (upc *UnifiedProtectionConfig) ToLegacyFullTPSL() FullTPSLConfig {
	config := FullTPSLConfig{
		Enabled:           upc.Enabled,
		Mode:              ProtectionMode(upc.StopLoss.DecisionMode), // 使用止损的决策模式
		TakeProfitEnabled: upc.TakeProfit.Enabled,
		StopLossEnabled:   upc.StopLoss.Enabled,
	}

	// 转换止盈值
	if upc.TakeProfit.Enabled {
		config.TakeProfit = ProtectionValueSource{
			Mode:  upc.TakeProfit.DecisionMode,
			Value: upc.TakeProfit.GetEffectiveValue(upc.Unit),
		}
	}

	// 转换止损值
	if upc.StopLoss.Enabled {
		config.StopLoss = ProtectionValueSource{
			Mode:  upc.StopLoss.DecisionMode,
			Value: upc.StopLoss.GetEffectiveValue(upc.Unit),
		}
	}

	return config
}

// DefaultUnifiedProtectionConfig 返回默认配置
func DefaultUnifiedProtectionConfig() UnifiedProtectionConfig {
	return UnifiedProtectionConfig{
		Enabled:      true,
		Unit:         ProtectionUnitR,
		RiskPerTrade: 1.0, // 1R = 1% 账户

		StopLoss: UnifiedProtectionValue{
			Enabled:      true,
			DecisionMode: ProtectionValueModeManual,
			ValueR:       1.0,
			ValuePercent: 2.5,
		},

		TakeProfit: UnifiedProtectionValue{
			Enabled:      true,
			DecisionMode: ProtectionValueModeManual,
			ValueR:       2.0,
			ValuePercent: 5.0,
		},

		TrailingTP: TrailingTPConfig{
			Enabled:      false,
			ActivationR:  1.0,
			PullbackR:    0.5,
			MaxTargetR:   5.0,
			TrailingMode: "dynamic",
		},

		BreakEvenStop: BreakEvenStopConfig{
			Enabled: false,
			// 使用现有的 BreakEvenStopConfig 字段
		},

		LadderTP: LadderTPConfig{
			Enabled: false,
			Levels: []LadderTPLevel{
				{TargetR: 1.0, SizePercent: 50},
				{TargetR: 2.0, SizePercent: 30},
				{TargetR: 3.0, SizePercent: 20},
			},
		},

		MarketFilters: MarketFiltersConfig{
			VolatilityFilter: VolatilityFilterConfig{
				Enabled: false,
				MaxATR:  3.0,
			},
			FundingRateFilter: FundingRateFilterConfig{
				Enabled: false,
				MaxRate: 0.1,
			},
			DrawdownProtection: DrawdownProtectionConfig{
				Enabled:     false,
				MaxDrawdown: 40,
			},
		},
	}
}

// PresetConservative 保守型预设
func PresetConservative() UnifiedProtectionConfig {
	config := DefaultUnifiedProtectionConfig()
	config.RiskPerTrade = 0.5
	config.StopLoss.ValueR = 0.8
	config.StopLoss.ValuePercent = 2.0
	config.TakeProfit.ValueR = 1.5
	config.TakeProfit.ValuePercent = 3.0
	config.TrailingTP.Enabled = true
	config.BreakEvenStop.Enabled = true
	return config
}

// PresetBalanced 平衡型预设
func PresetBalanced() UnifiedProtectionConfig {
	return DefaultUnifiedProtectionConfig()
}

// PresetAggressive 激进型预设
func PresetAggressive() UnifiedProtectionConfig {
	config := DefaultUnifiedProtectionConfig()
	config.RiskPerTrade = 2.0
	config.StopLoss.ValueR = 1.5
	config.StopLoss.ValuePercent = 3.5
	config.TakeProfit.ValueR = 3.0
	config.TakeProfit.ValuePercent = 7.0
	config.TrailingTP.Enabled = true
	config.TrailingTP.MaxTargetR = 8.0
	return config
}
