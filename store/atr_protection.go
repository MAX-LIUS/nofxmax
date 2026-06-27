package store

// ATRProtectionConfig holds the GLOBAL ATR settings used when any protection
// field opts into ATR-unit mode (see ProtectionDistanceUnit on the TP/SL/BE/DD
// rules). It no longer rewrites distances itself — each field decides its own
// unit, and ATR-unit fields are converted to an effective percent via
// EffectivePercent using these settings. Enabled is the master switch for ATR
// resolution; when false, every field is treated as percent regardless of unit.
type ATRProtectionConfig struct {
	Enabled   bool   `json:"enabled"`              // master switch for ATR-unit resolution
	Timeframe string `json:"timeframe,omitempty"`  // ATR timeframe, default "1h"
	ATRPeriod int    `json:"atr_period,omitempty"` // ATR period, default 14

	// Bounds to keep ATR-derived percents sane in extreme volatility.
	MinEffPct float64 `json:"min_eff_pct,omitempty"` // floor for any derived % (default 0.3)
	MaxEffPct float64 `json:"max_eff_pct,omitempty"` // cap for any derived % (default 25)
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
	return c
}

// EffectivePercent converts an ATR multiple to an effective percent-of-entry
// distance using the supplied ATR value and entry price, clamped to bounds.
// Returns (pct, ok). ok=false when inputs are invalid or the multiple is <=0.
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
