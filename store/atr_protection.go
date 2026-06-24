package store

// ATRProtectionConfig is an OPT-IN, strategy-level block that makes the existing
// protection distances (TP/SL ladder, break-even, drawdown) ATR-driven instead
// of fixed percentages. When Enabled is false (the zero value), it is a complete
// no-op and the strategy uses its configured percentages unchanged — so adding
// this field changes nothing for existing traders.
//
// At placement time, each enabled multiple M is converted to an effective
// percent: effPct = M * ATR(period) / entryPrice * 100, where ATR is the
// Wilder ATR of the configured timeframe (default 1h). This mirrors the
// existing resolve-at-placement pattern used for break-even r_multiple rules.
type ATRProtectionConfig struct {
	Enabled   bool   `json:"enabled"`              // master switch (off = no-op)
	Timeframe string `json:"timeframe,omitempty"`  // ATR timeframe, default "1h"
	ATRPeriod int    `json:"atr_period,omitempty"` // ATR period, default 14

	// MultipleMode is the panel-level default mode, kept for backward-compat and
	// as the fallback when a per-dimension mode is unset:
	//   "fixed" (default) — use the per-dimension multiples configured below
	//   "ai"             — AI computes per-coin multiples
	MultipleMode string `json:"multiple_mode,omitempty"`

	// Per-dimension mode: each protection dimension independently chooses
	//   "percent" — keep the strategy's configured fixed percentage (no ATR)
	//   "fixed"   — ATR × the configured multiple below
	//   "ai"      — ATR × an AI-computed per-coin multiple (fallback: fixed)
	// Empty falls back to MultipleMode (or "fixed" when a positive multiple is set).
	SLMode  string `json:"sl_mode,omitempty"`
	TP1Mode string `json:"tp1_mode,omitempty"`
	TP2Mode string `json:"tp2_mode,omitempty"`
	BE1Mode string `json:"be1_mode,omitempty"`
	BE2Mode string `json:"be2_mode,omitempty"`
	DDMode  string `json:"dd_mode,omitempty"`

	// Per-dimension ATR multiples. A non-positive value means "leave this
	// dimension on its configured percent" — so you can ATR-ize only some
	// dimensions and keep others fixed. In "ai" mode these act as the fallback.
	StopLossATR    float64 `json:"stop_loss_atr,omitempty"`     // SL distance in ATR
	TakeProfit1ATR float64 `json:"take_profit_1_atr,omitempty"` // ladder TP1 distance in ATR
	TakeProfit2ATR float64 `json:"take_profit_2_atr,omitempty"` // ladder TP2 distance in ATR
	BreakEven1ATR  float64 `json:"break_even_1_atr,omitempty"`  // BE tier-1 trigger in ATR
	BreakEven2ATR  float64 `json:"break_even_2_atr,omitempty"`  // BE tier-2 trigger in ATR
	// Drawdown dimension: ATR multiple for the DD min-profit ARM threshold (the
	// profit distance at which the drawdown-trailing rule becomes active). The
	// max-drawdown give-back stays a % of peak (it's a ratio, not a price
	// distance, so ATR does not apply there).
	DrawdownMinProfitATR float64 `json:"drawdown_min_profit_atr,omitempty"`

	// Bounds to keep ATR-derived percents sane in extreme volatility.
	MinEffPct float64 `json:"min_eff_pct,omitempty"` // floor for any derived % (default 0.3)
	MaxEffPct float64 `json:"max_eff_pct,omitempty"` // cap for any derived % (default 25)

	// AI-mode multiple bounds (clamp AI output to sane ranges).
	AIMinMult float64 `json:"ai_min_mult,omitempty"` // floor for AI multiples (default 0.5)
	AIMaxMult float64 `json:"ai_max_mult,omitempty"` // cap for AI multiples (default 10)
}

// WithDefaults returns a copy with unset fields filled to safe defaults.
func (c ATRProtectionConfig) WithDefaults() ATRProtectionConfig {
	if c.Timeframe == "" {
		c.Timeframe = "1h"
	}
	if c.ATRPeriod <= 0 {
		c.ATRPeriod = 14
	}
	if c.MinEffPct <= 0 {
		c.MinEffPct = 0.3
	}
	if c.MaxEffPct <= 0 {
		c.MaxEffPct = 25
	}
	if c.MultipleMode == "" {
		c.MultipleMode = "fixed"
	}
	if c.AIMinMult <= 0 {
		c.AIMinMult = 0.5
	}
	if c.AIMaxMult <= 0 {
		c.AIMaxMult = 10
	}
	return c
}

// EffectivePercent converts an ATR multiple to an effective percent-of-entry
// distance using the supplied ATR value and entry price, clamped to bounds.
// Returns (pct, ok). ok=false when inputs are invalid or the multiple is <=0
// (meaning "this dimension stays on its configured percent").
func (c ATRProtectionConfig) EffectivePercent(atrMultiple, atrValue, entryPrice float64) (float64, bool) {
	if atrMultiple <= 0 || atrValue <= 0 || entryPrice <= 0 {
		return 0, false
	}
	cfg := c.WithDefaults()
	pct := atrMultiple * atrValue / entryPrice * 100.0
	if pct < cfg.MinEffPct {
		pct = cfg.MinEffPct
	}
	if pct > cfg.MaxEffPct {
		pct = cfg.MaxEffPct
	}
	return pct, true
}

// ATR dimension identifiers.
const (
	ATRDimSL  = "sl"
	ATRDimTP1 = "tp1"
	ATRDimTP2 = "tp2"
	ATRDimBE1 = "be1"
	ATRDimBE2 = "be2"
	ATRDimDD  = "dd"
)

// DimMode returns the effective mode ("percent"|"fixed"|"ai") for a dimension.
// Resolution order: explicit per-dimension mode → panel MultipleMode → "fixed".
// A "percent" result means "do not ATR-ize this dimension".
func (c ATRProtectionConfig) DimMode(dim string) string {
	var m string
	switch dim {
	case ATRDimSL:
		m = c.SLMode
	case ATRDimTP1:
		m = c.TP1Mode
	case ATRDimTP2:
		m = c.TP2Mode
	case ATRDimBE1:
		m = c.BE1Mode
	case ATRDimBE2:
		m = c.BE2Mode
	case ATRDimDD:
		m = c.DDMode
	}
	switch m {
	case "percent", "fixed", "ai":
		return m
	}
	// Unset: fall back to the panel-level mode.
	switch c.MultipleMode {
	case "ai":
		return "ai"
	case "percent":
		return "percent"
	default:
		return "fixed"
	}
}

// AnyATRDimActive reports whether at least one dimension is in fixed/ai mode
// (i.e. ATR sizing should run at all).
func (c ATRProtectionConfig) AnyATRDimActive() bool {
	for _, d := range []string{ATRDimSL, ATRDimTP1, ATRDimTP2, ATRDimBE1, ATRDimBE2, ATRDimDD} {
		if c.DimMode(d) != "percent" {
			return true
		}
	}
	return false
}
