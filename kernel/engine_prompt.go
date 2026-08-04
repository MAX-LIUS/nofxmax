package kernel

import (
	"fmt"
	"nofx/market"
	"nofx/provider/nofxos"
	"nofx/store"
	"strings"
	"time"
)

// ============================================================================
// Prompt Building - System Prompt
// ============================================================================

// BuildSystemPrompt builds System Prompt according to strategy configuration
func (e *StrategyEngine) BuildSystemPrompt(accountEquity float64, variant string) string {
	var sb strings.Builder
	riskControl := e.config.RiskControl
	promptSections := e.config.PromptSections
	decisionMode := strings.ToLower(strings.TrimSpace(variant))

	// Parse fine-grained close permission flags
	allowAIStopClose := true
	allowAITakeProfit := true
	if strings.Contains(decisionMode, "|no_stop_close") {
		allowAIStopClose = false
		decisionMode = strings.ReplaceAll(decisionMode, "|no_stop_close", "")
	}
	if strings.Contains(decisionMode, "|no_take_profit") {
		allowAITakeProfit = false
		decisionMode = strings.ReplaceAll(decisionMode, "|no_take_profit", "")
	}
	// Legacy flag: treat |no_close as both disabled
	allowAIClose := true
	if strings.Contains(decisionMode, "|no_close") {
		allowAIClose = false
		allowAIStopClose = false
		allowAITakeProfit = false
		decisionMode = strings.ReplaceAll(decisionMode, "|no_close", "")
	}
	// Parse open permission flag
	allowAIOpen := true
	if strings.Contains(decisionMode, "|no_open") {
		allowAIOpen = false
		decisionMode = strings.ReplaceAll(decisionMode, "|no_open", "")
	}

	// Entry-gate soft-flag awareness (align the prompt with the actual backend
	// gate config so guidance matches enforcement instead of contradicting it).
	//   - penalizeBreakoutRetest: breakout_retest carries a HEAVY size penalty
	//     (not a hard block) → tell the AI it may still use it but only at high
	//     conviction and reduced size, and it is de-preferred.
	// (The regime/setup soft-fit rule is rendered in formatMarketContextV2, which
	// computes SoftRegimeStructureFit locally.)
	entryGateFlags := e.config.EntryStructure.EntryGate.WithDefaults()
	penalizeBreakoutRetest := entryGateFlags.BlockBreakoutRetest != nil && *entryGateFlags.BlockBreakoutRetest

	// 0. Data Dictionary & Schema (ensure AI understands all fields)
	lang := e.GetLanguage()
	schemaPrompt := GetSchemaPrompt(lang)
	sb.WriteString(schemaPrompt)
	sb.WriteString("\n\n")
	sb.WriteString("---\n\n")

	// 0.1 Hard language contract — enforce decision/reasoning language
	if lang == LangChinese {
		sb.WriteString("## ⚠️ 输出语言硬性要求\n")
		sb.WriteString("- 你必须用**中文**输出所有分析、reasoning、entry_protection_rationale、protection_plan 说明性字段、reason_anchor、structural_anchor、notes、alignment_notes 等文本内容\n")
		sb.WriteString("- 决策 JSON 的字段名保持英文 schema 不变，但字段值里的自然语言解释必须是中文\n")
		sb.WriteString("- 不要输出英文分析，不要中英混写，除非是币种、周期、字段名或技术术语缩写\n")
		sb.WriteString("- 如果输出语言不是中文，视为不合格输出\n\n")
	} else {
		sb.WriteString("## ⚠️ Output Language Contract\n")
		sb.WriteString("- You MUST use **English** for all reasoning, explanatory text, entry_protection_rationale, protection_plan explanation fields, reason_anchor, structural_anchor, notes, alignment_notes, etc.\n")
		sb.WriteString("- Keep JSON field names in the schema unchanged, but all natural-language values inside the JSON must be English\n")
		sb.WriteString("- Do not output Chinese analysis or mixed Chinese-English prose unless it is a symbol, timeframe, field name, or technical abbreviation\n")
		sb.WriteString("- If the output language is not English, it is invalid\n\n")
	}

	// 1. Role definition (editable)
	if promptSections.RoleDefinition != "" {
		sb.WriteString(promptSections.RoleDefinition)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("# You are a professional cryptocurrency trading AI\n\n")
		sb.WriteString("Your task is to make trading decisions based on provided market data.\n\n")
	}

	// 2. Trading mode variant
	switch decisionMode {
	case "aggressive":
		sb.WriteString("## Mode: Aggressive\n- Prioritize capturing trend breakouts, can build positions in batches when confidence ≥ 70\n- Allow higher positions, but must strictly set stop-loss and explain risk-reward ratio\n\n")
	case "conservative":
		sb.WriteString("## Mode: Conservative\n- Only open positions when multiple signals resonate\n- Prioritize cash preservation, must pause for multiple periods after consecutive losses\n\n")
	case "balanced", "":
		sb.WriteString("## Mode: Balanced\n- Balance opportunity capture and risk control\n- Prefer clear setups with sufficient confirmation, but do not become overly passive\n\n")
	case "scalping":
		sb.WriteString("## Mode: Scalping\n- Focus on short-term momentum, smaller profit targets but require quick action\n- If price doesn't move as expected within two bars, immediately reduce position or stop-loss\n\n")
	}

	// 2b. Trading principles — factual backend-gate awareness only; method/analysis
	// prescriptions removed so the AI reasons autonomously.
	sb.WriteString("## Trading Principles\n\n")
	sb.WriteString("- Backend quantitative gates (ATR distance, RR ratio, regime alignment, confidence floor) reject trades below threshold. Focus on setup quality; the exact numbers are enforced by the backend.\n")
	sb.WriteString("- You decide the analysis method, the setup, the entry/invalidation/target, and when a setup is good enough. Use your own judgment.\n\n")

	// 2c. Trigger type — trigger_type is backend-enforced (must be one of the 6 enum
	// values); the prescriptive confirmation prose has been removed so the AI decides
	// autonomously WHEN a trigger is confirmed enough.
	sb.WriteString("## Entry Trigger (trigger_type required)\n\n")
	sb.WriteString("`trigger_type` is REQUIRED on every open and must be one of these 6 (3 long / 3 short mirror pairs):\n")
	sb.WriteString("- long: `support_rejection_confirmed`, `resistance_breakout_retest_successful`, `higher_low_breakout_confirmed`\n")
	sb.WriteString("- short: `resistance_rejection_confirmed`, `support_breakdown_retest_failed`, `lower_high_breakdown_confirmed`\n")
	sb.WriteString("Choose whichever trigger fits, and decide for yourself when confirmation is sufficient. Only reference structural zones present in the market data.\n\n")

	// 3. Hard constraints (risk control)
	btcEthPosValueRatio := riskControl.BTCETHMaxPositionValueRatio
	if btcEthPosValueRatio <= 0 {
		btcEthPosValueRatio = 5.0
	}
	altcoinPosValueRatio := riskControl.AltcoinMaxPositionValueRatio
	if altcoinPosValueRatio <= 0 {
		altcoinPosValueRatio = 1.0
	}

	sb.WriteString("# Hard Constraints (Risk Control)\n\n")
	sb.WriteString("## CODE ENFORCED (Backend validation, cannot be bypassed):\n")
	sb.WriteString(fmt.Sprintf("- Max Positions: %d coins simultaneously\n", riskControl.MaxPositions))
	sb.WriteString(fmt.Sprintf("- Position Value Limit (Altcoins): max %.0f USDT (= equity %.0f × %.1fx)\n",
		accountEquity*altcoinPosValueRatio, accountEquity, altcoinPosValueRatio))
	sb.WriteString(fmt.Sprintf("- Position Value Limit (BTC/ETH): max %.0f USDT (= equity %.0f × %.1fx)\n",
		accountEquity*btcEthPosValueRatio, accountEquity, btcEthPosValueRatio))
	sb.WriteString(fmt.Sprintf("- Max Margin Usage: ≤%.0f%%\n", riskControl.MaxMarginUsage*100))
	minExecutablePositionSize := riskControl.MinPositionSize
	if minExecutablePositionSize <= 0 {
		minExecutablePositionSize = 12
	}
	btcEthExecutableMin := minExecutablePositionSize
	if accountEquity > 0 {
		if adaptiveMin := accountEquity * 0.9; adaptiveMin > 0 && adaptiveMin < btcEthExecutableMin {
			btcEthExecutableMin = adaptiveMin
		}
	}
	if btcEthExecutableMin < 5 {
		btcEthExecutableMin = 5
	}
	sb.WriteString(fmt.Sprintf("- Min Position Size: ≥%.0f USDT (BTC/ETH on small accounts may use the executable floor around %.0f USDT)\n\n", minExecutablePositionSize, btcEthExecutableMin))

	sb.WriteString("## AI GUIDED (Recommended, you should follow):\n")
	sb.WriteString(fmt.Sprintf("- Trading Leverage: Altcoins max %dx | BTC/ETH max %dx\n",
		riskControl.AltcoinMaxLeverage, riskControl.BTCETHMaxLeverage))
	sb.WriteString(fmt.Sprintf("- Risk-Reward Ratio: ≥1:%.1f (take_profit / stop_loss)\n", riskControl.MinRiskRewardRatio))
	sb.WriteString(fmt.Sprintf("- Min Confidence: ≥%d to open position\n\n", riskControl.MinConfidence))

	// Position sizing guidance
	sb.WriteString("## Position Sizing Guidance\n")
	sb.WriteString("Calculate `position_size_usd` based on your confidence and the Position Value Limits above:\n")
	sb.WriteString("- High confidence (≥85): Use 80-100%% of max position value limit\n")
	sb.WriteString("- Medium confidence (70-84): Use 65-85%% of max position value limit\n")
	sb.WriteString("- Low confidence (60-69): Use 50-70%% of max position value limit\n")
	sb.WriteString(fmt.Sprintf("- Example: With equity %.0f and BTC/ETH ratio %.1fx, max is %.0f USDT\n",
		accountEquity, btcEthPosValueRatio, accountEquity*btcEthPosValueRatio))
	sb.WriteString(fmt.Sprintf("- For any open decision, `position_size_usd` must stay above the executable floor. On this account, BTC/ETH opens should generally not be below about %.0f USDT unless venue constraints explicitly allow it. Avoid tiny probe sizes that are likely to fail validation or venue minimums.\n", btcEthExecutableMin))
	sb.WriteString("- **DO NOT** just use available_balance as position_size_usd. Use the Position Value Limits!\n\n")

	// Risk-based sizing notice: when the strategy reverse-computes size from the stop
	// distance, the AI's position_size_usd is a hint only — the backend overwrites it so
	// a single trade risks at most RiskPerTradePctOfEquity% of equity. What matters then
	// is a REALISTIC stop_loss, because the stop distance directly drives the size.
	if riskControl.RiskSizingEnabled && riskControl.RiskPerTradePctOfEquity > 0 {
		sb.WriteString("## ⚠️ Risk-Based Sizing is ACTIVE (this overrides your position_size_usd)\n")
		sb.WriteString(fmt.Sprintf("- The backend REVERSE-COMPUTES position size from your stop distance so a single trade loses at most **%.1f%% of equity**. Your `position_size_usd` is only a fallback hint; the risk formula wins.\n", riskControl.RiskPerTradePctOfEquity))
		sb.WriteString("- Formula: `size = equity × risk%% ÷ effective_stop_distance%%`, where effective stop = the WIDER of your `stop_loss` and the strategy's structural stop. A TIGHT, well-placed stop → larger size; a FAR/sloppy stop → smaller size.\n")
		sb.WriteString("- Therefore your ONE job on sizing is a **realistic, structurally-justified `stop_loss`**. Do not pad the stop \"for safety\" — a needlessly wide stop shrinks your size and wastes the setup. Do not set an artificially tight stop to inflate size — it will just get swept.\n")
		sb.WriteString("- If your stop is so far that the computed size falls below the executable floor, the trade is SKIPPED. That is correct behavior: the setup's risk/structure did not justify a viable size.\n\n")
	}

	// 4. Trading frequency (editable)
	if promptSections.TradingFrequency != "" {
		sb.WriteString(promptSections.TradingFrequency)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("# ⏱️ Trading Frequency Awareness\n\n")
		sb.WriteString("- Excellent traders: 2-4 trades/day ≈ 0.1-0.2 trades/hour\n")
		sb.WriteString("- >2 trades/hour = Overtrading\n")
		sb.WriteString("- Single position hold time ≥ 30-60 minutes\n")
		sb.WriteString("If you find yourself trading every period → standards too low; if closing positions < 30 minutes → too impatient.\n\n")
	}

	// 5. Entry standards (editable)
	if promptSections.EntryStandards != "" {
		sb.WriteString(promptSections.EntryStandards)
		sb.WriteString("\n\nYou have the following indicator data:\n")
		e.writeAvailableIndicators(&sb)
		sb.WriteString(fmt.Sprintf("\n**Confidence ≥ %d** required to open positions.\n\n", riskControl.MinConfidence))
	} else {
		sb.WriteString("# 🎯 Entry Standards (Strict)\n\n")
		sb.WriteString("Only open positions when multiple signals resonate. You have:\n")
		e.writeAvailableIndicators(&sb)
		sb.WriteString(fmt.Sprintf("\nFeel free to use any effective analysis method, but **confidence ≥ %d** required to open positions; avoid low-quality behaviors such as single indicators, contradictory signals, sideways consolidation, reopening immediately after closing, etc.\n\n", riskControl.MinConfidence))
	}

	entryGate := e.config.EntryStructure.EntryGate
	gateDefaults := entryGate.WithDefaults()
	minSLATR := entryGate.MinSLDistanceATRMul
	if minSLATR <= 0 {
		minSLATR = 1.2
	}
	minRewardATR := entryGate.MinRewardATRMul
	if minRewardATR <= 0 {
		minRewardATR = 1.8
	}
	volBuffer := gateDefaults.VolatilityBufferATRMul
	effectiveMinSL := minSLATR + volBuffer
	maxTargetATR := gateDefaults.MaxTargetATRMul            // reachable target ceiling (default 5.0)
	realisticRiskMul := gateDefaults.RealisticTargetRiskMul // profit-lock tier (default 0.8× risk)

	// Structural analysis requirements — authoritative constants defined ONCE here,
	// referenced by name later. Do not restate the numbers elsewhere.
	sb.WriteString("# 🏗️ Structural Analysis & Protection (authoritative definitions)\n\n")
	sb.WriteString("You receive auto-detected support/resistance levels and Fibonacci retracements per timeframe. Higher TFs (4h,1d) give stronger levels; lower TFs (15m) give precision. Levels are hints: prefer confidence>=60 and multi_tf_count>=1, discard confidence<30. Only reference levels present in the market data — never invent levels.\n\n")

	sb.WriteString("## Key constants (all planning references these)\n")
	sb.WriteString(fmt.Sprintf("- **MIN_SL_DIST = %.1f× ATR14** (base %.1f + volatility buffer %.1f). Every stop must sit this far from entry so normal wicks/stop-hunts don't trip it. Closer stops are auto-widened by the backend.\n", effectiveMinSL, minSLATR, volBuffer))
	sb.WriteString(fmt.Sprintf("- **TARGET_CEILING = %.1f× ATR14**. `first_target` must stay within this. Targets beyond it hit only ~12%% of the time; the backend caps an over-far first_target to this and recomputes RR. Park farther objectives as an OUTER runner tier, not as first_target.\n", maxTargetATR))
	sb.WriteString(fmt.Sprintf("- **MIN_REWARD = %.1f× ATR14**. The main target must come from a HIGHER-TF level and be at least this far; if the nearest higher-TF target is closer, the setup is too crowded — wait.\n", minRewardATR))
	sb.WriteString(fmt.Sprintf("- **FIRST_LOCK = ~%.1f× risk** (floored at MIN_REWARD). Data: 72%% of reverting trades had reached 0.5× risk and 54%% reached 1× risk before giving it back. Lock the first profit tier here, keep 20-35%% as a runner to the higher-TF target.\n", realisticRiskMul))
	sb.WriteString("- **first_target distance must be >= 1.2× the stop distance AND <= TARGET_CEILING.**\n\n")

	sb.WriteString("## Placement rules (backend-checked facts, symmetric long/short)\n")
	sb.WriteString("- **Direction sanity (enforced)**: long → invalidation < entry < first_target; short → invalidation > entry > first_target.\n")
	sb.WriteString("- **Stop loss**: total distance from entry must be >= MIN_SL_DIST (closer stops are auto-widened by the backend). Where exactly to place it is your call.\n")
	sb.WriteString("- **Take profit**: first_target within [MIN_REWARD, TARGET_CEILING] (the backend caps an over-far first_target and recomputes RR). Level selection is your call.\n")
	sb.WriteString("- **Anchoring**: derive prices however you judge best; the backend does not require any particular level source.\n\n")

	sb.WriteString("## Required output arrays for every open\n")
	sb.WriteString("- `selected_levels`: every level you used for SL/TP/entry, each with price, type, timeframe, source, used_for, basis_type (structural/atr_based/percentage/fibonacci), brief reason. If no structural level fits, use atr_based/percentage and say why.\n")
	sb.WriteString("- `structural_key_levels`: the levels that drove entry/TP/SL/drawdown, each with price, type, timeframe, source, used_for. Must be consistent with key_levels.support/resistance (don't leave them empty).\n")
	sb.WriteString("- If `timeframe_context.higher` is present, include `higher_timeframe_anchors` (or `timeframe_structures`) with explicit higher-TF price anchors; outer runner/drawdown stages must cite those, not only primary-TF text.\n\n")

	sb.WriteString("## Protection plan by mode (when mode = ai)\n")
	sb.WriteString("- **ladder**: 2-3 tiers. Each TP tier maps to a structural level; each rule has `structural_anchor` (level + timeframe) + a volatility buffer. All ladder SL tiers use the SAME nearest primary-TF invalidation structure at distinct inside/outside-buffer prices (>=75% close near it, any farther tier <=25%). Ladder[0]/[1] TP within 0.5% of a structural level; Ladder[2] may extend but its close ratio <=20%.\n")
	sb.WriteString("- **drawdown**: >=2 stages. Stage 1 locks partial profit near the first primary-TF structure at FIRST_LOCK with generous `max_drawdown_pct`; Stage 2+ protects the runner against higher-TF structure. `max_drawdown_pct` is percent-of-peak-profit giveback (trailing semantics), sized for ATR so normal retests don't trip it. Each stage needs `reason_anchor` (level + timeframe). Use exact field name `close_ratio_pct` (never `close_ratio`).\n")
	sb.WriteString(fmt.Sprintf("- **break_even**: trigger at FIRST_LOCK (~%.1f× risk / first structure past entry), offset beyond the nearest support/resistance + ATR buffer; cite the level in `break_even_reason_anchor`.\n\n", realisticRiskMul))
	if e.config.Protection.DrawdownTakeProfit.Enabled {
		sb.WriteString("- If Drawdown Take Profit is enabled in strategy config, your reasoning must explicitly mention drawdown, trailing, or profit-protection ownership.\n")
	}
	if e.config.Protection.BreakEvenStop.Enabled {
		sb.WriteString("- If Break-even Stop is enabled in strategy config (`mode=break_even`, fields `break_even_trigger_mode/value/offset`), your reasoning must mention break-even or acknowledge that an additional stop layer exists after profit trigger.\n")
	}
	if e.config.Protection.DrawdownTakeProfit.Enabled || e.config.Protection.BreakEvenStop.Enabled {
		sb.WriteString("\n")
	}

	// Backend entry gates explanation — awareness only, kept short
	sb.WriteString("## Backend gates (awareness — rejected trades appear in the log with a reason)\n")
	sb.WriteString("Regime alignment (direction must match regime) · ATR-relative SL/TP distances · min RR · confidence floor (higher for squeeze/crowded, and for shorts in non-downtrend / longs in non-uptrend) · funding-rate cap · setup_type must match regime's allowed setups.\n\n")

	// Dynamic: inject hard requirement when drawdown is actually in AI mode
	prot := e.config.Protection
	if prot.DrawdownTakeProfit.Enabled && prot.DrawdownTakeProfit.Mode == store.ProtectionModeAI {
		if prot.LadderTPSL.Enabled && prot.LadderTPSL.Mode == store.ProtectionModeAI {
			sb.WriteString("### ⚠️ ACTIVE: Ladder + Drawdown are BOTH in AI mode for this strategy\n")
			sb.WriteString("- You MUST include one `protection_plan` with `mode=\"combined\"` for every open_long/open_short decision\n")
			sb.WriteString("- The combined plan MUST include both non-empty `ladder_rules` and at least 2 `drawdown_rules`; omission or a single drawdown stage rejects the trade\n")
			sb.WriteString("- `ladder_rules` own staged stop-loss / optional staged TP; derive absolute prices, percentages, buffers, and close ratios from structure, not from default round numbers\n")
			sb.WriteString("- Every ladder rule must include a volatility/wick buffer using ATR or recent wick behavior; do not place stops/targets exactly on crowded structure/fib levels\n")
			sb.WriteString("- `drawdown_rules` own profit-protection/trailing stages; derive min_profit_pct/max_drawdown_pct/close_ratio_pct from structural targets and volatility\n")
			sb.WriteString("- Include structural_anchor on every ladder rule and reason_anchor on every drawdown rule\n")
			sb.WriteString(fmt.Sprintf("- Strategy has %d default ladder rule(s) and %d default drawdown rule(s).\n", len(prot.LadderTPSL.Rules), len(prot.DrawdownTakeProfit.Rules)))
			for i, r := range prot.LadderTPSL.Rules {
				sb.WriteString(fmt.Sprintf("  - Ladder reference %d: tp=%.2f%% close=%.0f%%, sl=%.2f%% close=%.0f%%\n", i+1, r.TakeProfitPct, r.TakeProfitCloseRatioPct, r.StopLossPct, r.StopLossCloseRatioPct))
			}
			for i, r := range prot.DrawdownTakeProfit.Rules {
				sb.WriteString(fmt.Sprintf("  - Drawdown tier %d (ceiling): min_profit ≤ %.2f%%, peak_profit_giveback=%.0f%%, close_ratio=%.0f%%\n", i+1, r.MinProfitPct, r.MaxDrawdownPct, r.CloseRatioPct))
			}
			sb.WriteString(buildDrawdownTierConstraintPrompt(prot.DrawdownTakeProfit))
			sb.WriteString("\n")
		} else {
			sb.WriteString("### ⚠️ ACTIVE: Drawdown Take Profit is in AI mode for this strategy\n")
			sb.WriteString("- You MUST include `protection_plan` with `mode=\"drawdown\"` and at least 2 `drawdown_rules` for every open_long/open_short decision\n")
			sb.WriteString("- Rule 1 should partially lock profit near the first primary-timeframe structural target; rule 2+ should protect a runner using primary/higher timeframe structure and ATR tolerance\n")
			sb.WriteString("- Omitting `drawdown_rules` or providing only one drawdown stage will cause the trade to be rejected\n")
			sb.WriteString("- Your reasoning must explicitly claim drawdown, trailing, or profit-protection ownership\n")
			if prot.FullTPSL.Enabled && prot.FullTPSL.Mode == store.ProtectionModeAI {
				sb.WriteString("- Combined ownership mode is active: drawdown AI owns profit-taking / profit-protection, while full AI remains strategy-level stop-loss / fallback stop protection\n")
				sb.WriteString("- In this combined mode, DO NOT output `mode=full` in the AI decision. Output only drawdown ownership fields and let strategy-level full stop protection merge at execution time\n")
			}
			sb.WriteString(fmt.Sprintf("- Strategy has %d default drawdown rule(s).\n", len(prot.DrawdownTakeProfit.Rules)))
			for i, r := range prot.DrawdownTakeProfit.Rules {
				sb.WriteString(fmt.Sprintf("  - Drawdown tier %d (ceiling): min_profit ≤ %.2f%%, peak_profit_giveback=%.0f%%, close_ratio=%.0f%%\n", i+1, r.MinProfitPct, r.MaxDrawdownPct, r.CloseRatioPct))
			}
			sb.WriteString(buildDrawdownTierConstraintPrompt(prot.DrawdownTakeProfit))
			sb.WriteString("\n")
		}
	}

	// Per-leg MANUAL notice: any protection leg configured in manual mode is owned
	// by the strategy template (resolved from ATR/structure at execution time), NOT
	// by the AI. Without this notice the AI — seeing the generic "when mode = ai"
	// guidance above — emits a half-baked protection_plan that is silently discarded
	// at execution (manual legs never defer to it) and only pollutes the record.
	// This block is emitted whenever at least one of ladder/drawdown/full-TPSL is in
	// manual mode, telling the AI exactly which legs to leave alone while STILL
	// requiring its structural SL/TP + R-multiple opinion at the top level.
	ladderManual := prot.LadderTPSL.Enabled && prot.LadderTPSL.Mode != store.ProtectionModeAI
	drawdownManual := prot.DrawdownTakeProfit.Enabled && prot.DrawdownTakeProfit.Mode != store.ProtectionModeAI && prot.DrawdownTakeProfit.Mode != store.ProtectionModeDisabled
	fullManual := prot.FullTPSL.Enabled && prot.FullTPSL.Mode != store.ProtectionModeAI
	if ladderManual || drawdownManual || fullManual {
		sb.WriteString("### ⚠️ ACTIVE: Some protection legs are MANUALLY managed by the strategy\n")
		sb.WriteString("- The legs below are placed by the strategy's manual template (resolved from ATR + structure at open); you do NOT own them.\n")
		if ladderManual {
			sb.WriteString(fmt.Sprintf("  - Ladder TP/SL: MANUAL (%d template rule(s)). Do NOT output `ladder_rules` in protection_plan — they will be ignored.\n", len(prot.LadderTPSL.Rules)))
		}
		if drawdownManual {
			sb.WriteString(fmt.Sprintf("  - Drawdown take-profit: MANUAL (%d template rule(s)). Do NOT output `drawdown_rules` — they will be ignored.\n", len(prot.DrawdownTakeProfit.Rules)))
		}
		if fullManual {
			sb.WriteString("  - Full TP/SL: MANUAL. Do NOT output `mode=full` tp/sl in protection_plan — they will be ignored.\n")
		}
		sb.WriteString("- For a fully-manual protection stack, OMIT `protection_plan` entirely (do not emit an empty or partial plan).\n")
		sb.WriteString("- REGARDLESS of manual legs, you MUST STILL provide, at the decision top level, your structural opinion: `stop_loss` (nearest primary-TF invalidation + ATR buffer) and `take_profit` (higher-TF structural target), plus `selected_levels`/`structural_key_levels` and the R multiple in `risk_reward`. These drive entry-quality gating and the manual-vs-structural deviation panel even though the manual template executes the actual orders.\n\n")
	}

	if prot.BreakEvenStop.Enabled {
		if prot.BreakEvenStop.Mode == store.ProtectionModeAI {
			sb.WriteString("### ⚠️ ACTIVE: Break-even Stop is enabled in AI mode for this strategy\n")
			sb.WriteString("- Use `mode=break_even` (or include break-even fields in your combined plan) and include break_even_trigger_mode/value/offset in protection_plan for every open action\n")
			sb.WriteString(fmt.Sprintf("  - Manual fallback/reference: trigger_mode=%s, trigger_value=%.1f, offset=%.2f%%\n", prot.BreakEvenStop.TriggerMode, prot.BreakEvenStop.TriggerValue, prot.BreakEvenStop.OffsetPct))
			sb.WriteString("- Your reasoning must mention break-even or acknowledge that an additional stop layer exists after the profit trigger\n")
		} else {
			sb.WriteString("### ⚠️ ACTIVE: Break-even Stop is enabled in manual mode for this strategy\n")
			sb.WriteString("- Break-even uses the strategy manual trigger/offset; do NOT invent AI break-even values unless another AI protection route needs rationale text\n")
			sb.WriteString(fmt.Sprintf("  - Manual: trigger_mode=%s, trigger_value=%.1f, offset=%.2f%%\n", prot.BreakEvenStop.TriggerMode, prot.BreakEvenStop.TriggerValue, prot.BreakEvenStop.OffsetPct))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("### For break_even mode:\n")
	sb.WriteString("- trigger_value should be set near the first structural level past entry (e.g. first resistance for long, first support for short)\n")
	sb.WriteString("- offset_pct should keep the stop just beyond the nearest support/resistance, plus ATR buffer to avoid wick sweeps\n")
	sb.WriteString("- Reference the specific structural level and timeframe in break_even_reason_anchor\n\n")

	// 6. Decision process (editable)
	if promptSections.DecisionProcess != "" {
		sb.WriteString(promptSections.DecisionProcess)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("# 📋 Decision Process\n\n")
		sb.WriteString("1. Check positions → Should we take profit/stop-loss\n")
		sb.WriteString("2. Scan candidate coins + multi-timeframe → Are there strong signals\n")
		sb.WriteString("3. Write chain of thought first, then output structured JSON\n\n")
	}

	// 7. AI Close permission gate
	if !allowAIClose || (!allowAIStopClose && !allowAITakeProfit) {
		sb.WriteString("# AI Close Gate\n\n")
		sb.WriteString("- You are NOT allowed to output `close_long` or `close_short`.\n")
		sb.WriteString("- Existing positions may only be closed by code protection and exchange protection orders.\n")
		sb.WriteString("- You must continue analyzing open positions, but if you want a close, output `hold` and explain the risk instead.\n\n")
	} else if allowAIStopClose && !allowAITakeProfit {
		sb.WriteString("# AI Close Permission — Stop-Loss Only\n\n")
		sb.WriteString("- You MAY output `close_long` or `close_short` ONLY for stop-loss reasons.\n")
		sb.WriteString("- You MUST set `\"is_stop_loss\": true` and `\"close_reason\"` to one of:\n")
		sb.WriteString("  - `\"structure_break\"`: 开仓逻辑/结构失效（支撑跌破、趋势反转确认）\n")
		sb.WriteString("  - `\"time_decay\"`: 持仓超时无进展（价格横盘、动量消失）\n")
		sb.WriteString("  - `\"correlation_risk\"`: 关联资产异动预警（BTC暴跌、板块联动）\n")
		sb.WriteString("- You are NOT allowed to close for profit-taking. If you want to take profit, output `hold`.\n")
		sb.WriteString("- The system will reject stop-loss closes if the position's unrealized loss is below the configured threshold.\n\n")
	} else if !allowAIStopClose && allowAITakeProfit {
		sb.WriteString("# AI Close Permission — Take-Profit Only\n\n")
		sb.WriteString("- You MAY output `close_long` or `close_short` ONLY for take-profit reasons.\n")
		sb.WriteString("- You MUST set `\"is_stop_loss\": false`.\n")
		sb.WriteString("- You are NOT allowed to close for stop-loss. Protection orders handle that.\n\n")
	}
	// Both allowed: inject proactive close guidance
	if allowAIClose && allowAIStopClose && allowAITakeProfit {
		if lang == LangChinese {
			sb.WriteString("# AI 主动平仓指南\n\n")
			sb.WriteString("你可以输出 `close_long` 或 `close_short`。以下情况你**应该**主动平仓而非继续 hold：\n\n")
			sb.WriteString("**止损平仓条件**（设置 `is_stop_loss: true`）——仅在结构层面失效时主动止损：\n")
			sb.WriteString("- 开仓结构已失效：价格收盘跌破/突破你的 entry_trigger 锚点且无法收回\n")
			sb.WriteString("- Regime 翻转：开仓时 regime 为 trending_down 但现在已转为 trending_up（或反之）\n")
			sb.WriteString("- 关联资产异动：BTC 突然暴跌/暴涨，你的持仓方向与之相反\n")
			sb.WriteString("- **不要仅因浮亏幅度而主动止损**：单纯浮亏 -3%~-5% 但结构未破时，交给交易所 -5% 止损单执行，不要主动 close（主动市价平仓会吃滑点、且常砍在最差位置）。只有出现上述明确结构破坏信号时才主动止损。\n\n")
			sb.WriteString("**止盈平仓条件**（设置 `is_stop_loss: false`）：\n")
			sb.WriteString("- 价格已到达或接近 first_target 且出现反转 K 线形态\n")
			sb.WriteString("- 动量衰竭：价格接近目标但 RSI 极端/MACD 背离\n\n")
			sb.WriteString("**不要 hold 等死** — 如果结构已破、方向已反，主动平仓比等止损单触发更好。保护系统是最后防线，不是唯一防线。\n\n")
		} else {
			sb.WriteString("# AI Proactive Close Guide\n\n")
			sb.WriteString("You may output `close_long` or `close_short`. You **should** proactively close rather than hold when:\n\n")
			sb.WriteString("**Stop-loss close** (set `is_stop_loss: true`) — only close on structural invalidation:\n")
			sb.WriteString("- Entry structure invalidated: price closed beyond your entry_trigger anchor and failed to reclaim\n")
			sb.WriteString("- Regime flipped: regime was trending_down at entry but now trending_up (or vice versa)\n")
			sb.WriteString("- Correlated asset shock: BTC sudden crash/pump opposing your position direction\n")
			sb.WriteString("- **Do NOT close on unrealized-loss magnitude alone**: when loss is -3%~-5% but structure is intact, let the exchange -5% stop order handle it — do not market-close (a proactive market close incurs slippage and often exits at the worst spot). Only close proactively when one of the structural-break signals above is present.\n\n")
			sb.WriteString("**Take-profit close** (set `is_stop_loss: false`):\n")
			sb.WriteString("- Price reached or nearing first_target with reversal candle pattern\n")
			sb.WriteString("- Momentum exhaustion: price near target but RSI extreme / MACD divergence\n\n")
			sb.WriteString("**Do not hold and wait for stop-loss** — if structure is broken and direction reversed, proactive close is better than letting the protection system hit the hard stop. Protection is the last line of defense, not the only one.\n\n")
		}
	}

	// AI Open permission gate
	if !allowAIOpen {
		sb.WriteString("# AI Open Gate\n\n")
		sb.WriteString("- You are NOT allowed to output `open_long` or `open_short`.\n")
		sb.WriteString("- Focus on analyzing existing positions and market conditions.\n")
		sb.WriteString("- If you see a good setup, output `hold` and describe it in reasoning.\n\n")
	}

	// 7. Output format
	sb.WriteString("# Output Format — HARD OUTPUT CONTRACT\n\n")
	sb.WriteString("Output EXACTLY two blocks, nothing before or after:\n")
	sb.WriteString("- `<reasoning>`: prose analysis only, no JSON. wait/hold ≤3 sentences; open/close ≤5 sentences. One combined summary for coins you skip — do NOT list every coin.\n")
	sb.WriteString("- `<decision>`: ONE JSON array only. No prose, no markdown fences, no comments, no trailing text. If no trade: `<decision>[]</decision>`.\n\n")

	sb.WriteString("## JSON validity rules (violations are rejected by the parser — read carefully)\n")
	sb.WriteString("1. `<decision>` is an ARRAY whose elements are ALL objects `{...}`. Never put a bare string, number, range, or symbol as an element (e.g. `[B]`, `[60.9-61.4]` are INVALID).\n")
	sb.WriteString("2. Standard JSON only: double-quoted keys/strings, comma between elements, no trailing comma, no comment, no `//`. Do not append a second array or stray token after the closing `]`.\n")
	sb.WriteString("3. **STRICT JSON NUMBER RULE**: Numeric fields = plain digits with an optional single decimal point. Never use thousands separators, spaces, or localized punctuation, and NEVER put a formula. Correct: `97687.05`, `2293.23`. Wrong: `97,687.05`, `2 293`, `3000*0.01`. Comma-formatted prices may appear ONLY inside quoted natural-language strings.\n")
	sb.WriteString("4. ANTI-TRUNCATION (top cause of rejected outputs): the whole array must be complete and closed within the token budget. If you are running low on budget, emit FEWER complete decision objects rather than a half-written one. NEVER stop mid-object or mid-array. A missing closing `}`/`]` voids the entire response.\n\n")

	sb.WriteString("## `action` — CLOSED enum (any other value is rejected)\n")
	sb.WriteString("Allowed values, EXACTLY these six: `open_long` | `open_short` | `close_long` | `close_short` | `hold` | `wait`.\n")
	sb.WriteString("Do NOT invent variants. Map your intent to the enum:\n")
	sb.WriteString("- want to keep a position, maybe tighten/trail its stop → `hold` (describe the stop change in reasoning; the protection layer owns the stop)\n")
	sb.WriteString("- want to reduce/partially exit → `close_long`/`close_short` (partial size is expressed via position/close fields, NOT via a new action word)\n")
	sb.WriteString("- INVALID examples seen in logs, never output them: `close`, `hold_tighten`, `hold_tighten_stop`, `hold_raise_stop`, `hold_position`, `hold_with_protection`, `update_stop`, `adjust_stop`, `partial_close`.\n\n")

	examplePositionSize := accountEquity * btcEthPosValueRatio
	sb.WriteString("## Skeleton (structure only — derive every number from data)\n")
	sb.WriteString("<reasoning>\nRegime, key observation, action rationale. ≤3 sentences for wait/hold.\n</reasoning>\n")
	sb.WriteString("<decision>\n[\n")
	if e.anyProtectionLegAI() {
		sb.WriteString(fmt.Sprintf("  {\"symbol\":\"BTCUSDT\",\"action\":\"open_short\",\"leverage\":%d,\"position_size_usd\":%.0f,\"stop_loss\":97000,\"take_profit\":91000,\"confidence\":85,\"risk_usd\":300,\"trigger_type\":\"resistance_rejection_confirmed\",\"entry_protection_rationale\":{\"timeframe_context\":{\"primary\":\"1h\",\"lower\":[\"15m\"],\"higher\":[\"4h\"]},\"risk_reward\":{\"entry\":95000,\"invalidation\":97000,\"first_target\":91000,\"gross_estimated_rr\":2.0,\"net_estimated_rr\":1.8,\"min_required_rr\":%.1f,\"passed\":true},\"key_levels\":{\"support\":[91000],\"resistance\":[97000]},\"anchors\":[{\"type\":\"resistance\",\"timeframe\":\"1h\",\"price\":96000,\"reason\":\"primary rejection\"}]},\"selected_levels\":[{\"price\":97000,\"type\":\"resistance\",\"timeframe\":\"1h\",\"source\":\"swing\",\"used_for\":\"stop_loss\",\"basis_type\":\"structural\",\"reason\":\"1h invalidation\"}],\"protection_plan\":{\"mode\":\"combined\",\"ladder_rules\":[{\"stop_loss_price\":97000,\"stop_loss_close_ratio_pct\":50,\"structural_anchor\":\"1h invalidation resistance + ATR buffer\",\"volatility_buffer_reason\":\"ATR/wick buffer applied\"},{\"stop_loss_price\":98200,\"stop_loss_close_ratio_pct\":50,\"structural_anchor\":\"same 1h structure, outer buffer\",\"volatility_buffer_reason\":\"ATR/wick buffer applied\"}],\"drawdown_rules\":[{\"timeframe\":\"1h\",\"min_profit_pct\":0.75,\"max_drawdown_pct\":60,\"close_ratio_pct\":65,\"runner_keep_pct\":35,\"stage_name\":\"partial_profit_lock\",\"reason_anchor\":\"1h first structural target\"},{\"timeframe\":\"4h\",\"min_profit_pct\":1.4,\"max_drawdown_pct\":55,\"close_ratio_pct\":80,\"runner_keep_pct\":20,\"stage_name\":\"runner_extension\",\"reason_anchor\":\"4h runner structure\"}]}},\n", riskControl.BTCETHMaxLeverage, examplePositionSize, riskControl.MinRiskRewardRatio))
	} else {
		// Manual/disabled protection: any protection_plan is ignored at execution,
		// so the skeleton omits it. The AI still gives top-level stop_loss/take_profit
		// (structural opinion) + selected_levels for entry-quality gating.
		sb.WriteString(fmt.Sprintf("  {\"symbol\":\"BTCUSDT\",\"action\":\"open_short\",\"leverage\":%d,\"position_size_usd\":%.0f,\"stop_loss\":97000,\"take_profit\":91000,\"confidence\":85,\"risk_usd\":300,\"trigger_type\":\"resistance_rejection_confirmed\",\"entry_protection_rationale\":{\"timeframe_context\":{\"primary\":\"1h\",\"lower\":[\"15m\"],\"higher\":[\"4h\"]},\"risk_reward\":{\"entry\":95000,\"invalidation\":97000,\"first_target\":91000,\"gross_estimated_rr\":2.0,\"net_estimated_rr\":1.8,\"min_required_rr\":%.1f,\"passed\":true},\"key_levels\":{\"support\":[91000],\"resistance\":[97000]},\"anchors\":[{\"type\":\"resistance\",\"timeframe\":\"1h\",\"price\":96000,\"reason\":\"primary rejection\"}]},\"selected_levels\":[{\"price\":97000,\"type\":\"resistance\",\"timeframe\":\"1h\",\"source\":\"swing\",\"used_for\":\"stop_loss\",\"basis_type\":\"structural\",\"reason\":\"1h invalidation\"}]},\n", riskControl.BTCETHMaxLeverage, examplePositionSize, riskControl.MinRiskRewardRatio))
	}
	sb.WriteString("  {\"symbol\":\"ETHUSDT\",\"action\":\"wait\"}\n]\n</decision>\n\n")

	sb.WriteString("## Fields by action\n")
	protectionPlanReq := ""
	if e.anyProtectionLegAI() {
		protectionPlanReq = "`protection_plan`, "
	}
	sb.WriteString(fmt.Sprintf("- **open_long / open_short** REQUIRE: `symbol`, `action`, `leverage`, `position_size_usd`, `stop_loss`, `take_profit`, `confidence` (0-100, open ≥ %d), `risk_usd`, `trigger_type`, `entry_protection_rationale`, %s`selected_levels`. Direction sanity: long → invalidation < entry < first_target; short → invalidation > entry > first_target. RR must meet min ≥ %.1f.\n", riskControl.MinConfidence, protectionPlanReq, riskControl.MinRiskRewardRatio))
	sb.WriteString("- **hold / wait / close_long / close_short**: Do NOT output protection_plan for hold/wait/close actions, nor entry/structural fields.\n")
	sb.WriteString("- `trigger_type`: one of the 6 in the Entry Trigger Gate section. No valid trigger → do not open.\n")
	sb.WriteString("- Optional audit fields (include on strong opens): `regime` (trend_up|trend_down|range|squeeze|chop|news_risk|no_trade), `setup_type` (trend_pullback|range_edge|breakout_retest|none), `quality_score`.\n")
	if penalizeBreakoutRetest {
		sb.WriteString("- ⚠️ `setup_type=breakout_retest` is DE-PREFERRED: it is net-negative across all backtest periods and carries a heavy gate-score penalty that sharply reduces position size (it is NOT hard-rejected). Only tag a trade breakout_retest when conviction is very high and you accept the reduced size; otherwise prefer trend_pullback or range_edge.\n")
	}
	sb.WriteString("\n")

	sb.WriteString("## entry_protection_rationale (open only)\n")
	sb.WriteString("- Must contain `timeframe_context`, `risk_reward` (entry/invalidation/first_target/gross_estimated_rr, prefer net_estimated_rr), `key_levels.support` + `key_levels.resistance` (only decision-relevant levels, ≤3 each), and structural `anchors`.\n")
	sb.WriteString("- `gross_estimated_rr` MUST equal abs(first_target-entry)/abs(entry-invalidation). Do not round it up; if below min RR, output wait.\n")
	sb.WriteString("- Include a first-target anchor via `anchors[].type=\"first_target\"` OR `structural_key_levels[].used_for` in {first_target,tp1,take_profit}.\n")
	sb.WriteString("- Leave `timeframe_context.higher` omitted unless you have a concrete higher-TF anchor; if present, also include `higher_timeframe_anchors` (or `timeframe_structures`) with explicit higher-TF prices.\n")
	sb.WriteString("- Keep it compact: only the levels/anchors needed for entry, invalidation, and first target. Do not dump every indicator.\n\n")

	sb.WriteString("## selected_levels / structural_key_levels (open only)\n")
	sb.WriteString("- `selected_levels` (REQUIRED): every level you used for SL/TP/entry. Each item: `price`, `type`, `timeframe`, `source`, `used_for` (stop_loss/tp1/tp2/tp3/invalidation/entry_trigger/break_even), `basis_type` (structural/atr_based/percentage/fibonacci), `reason`. No structural level available → use basis_type atr_based/percentage and say why.\n")
	sb.WriteString("- `structural_key_levels` (optional): levels that shaped protection; must stay consistent with key_levels.support/resistance (do not leave those empty).\n\n")

	// protection_plan schema is only relevant when at least one protection leg is in
	// AI mode. With an all-manual/disabled protection stack the AI's protection_plan is
	// discarded at execution, so the full schema is dead weight — skip it and keep only
	// the top-level structural SL/TP opinion (still used for entry-quality gating and the
	// manual-vs-structural deviation panel).
	if e.anyProtectionLegAI() {
		sb.WriteString("## protection_plan (open only) — reuse the Key constants above; do not restate numbers here\n")
		sb.WriteString("- `mode`: `mode=full` | `mode=ladder` | `mode=drawdown` | `combined` | `mode=break_even`. Use `combined` when ladder AI and drawdown AI are both active.\n")
		sb.WriteString("- Each rule tags `basis_type` (structural/atr_based/percentage/fibonacci). Never use arbitrary round percentages when structure exists.\n")
		sb.WriteString("- `mode=full`: output `take_profit_pct` / `stop_loss_pct` only; no absolute price fields.\n")
		sb.WriteString("- `ladder_rules` (2~3 objects): every active TP leg needs `take_profit_price` + `take_profit_close_ratio_pct`; every active SL leg needs `stop_loss_price` + `stop_loss_close_ratio_pct` (never a price without its close ratio). Absolute structural prices only. Every rule carries `structural_anchor` + a volatility buffer (`volatility_buffer_pct` or `volatility_buffer_reason`). All ladder SL tiers reference the SAME nearest primary-TF invalidation structure with distinct inside/outside buffer prices (≥75% close near it, any farther tier ≤25%); do not repeat identical stop prices. TP1/TP2 within 0.5% of a structural level; TP3 may extend but its close ratio ≤20%.\n")
		sb.WriteString("- `drawdown_rules` (≥2 objects): each needs `timeframe` (SINGLE string like \"1h\", never \"15m/1h\"), `min_profit_pct`, `max_drawdown_pct` (percent-of-peak-profit giveback, not price), `close_ratio_pct` (this exact key, not `close_ratio`), `stage_name`, `reason_anchor`. Stage 1 locks partial profit near first structure (close <60%); outer stage protects the runner off higher-TF structure.\n")
		sb.WriteString("- When drawdown_rules are present they own TP/profit-taking: prefer ladder rules with the SL side only and omit ladder TP fields.\n")
		sb.WriteString("- Do not underspecify protection to lean on fallback. If structure is too unclear to derive valid protection, output wait instead of opening.\n\n")
	} else {
		sb.WriteString("## Protection: MANAGED BY STRATEGY (do NOT output `protection_plan`)\n")
		sb.WriteString("- All TP/SL/drawdown/break-even legs are placed by the strategy template from ATR + structure at execution; any `protection_plan` you emit is ignored.\n")
		sb.WriteString("- Still provide the top-level `stop_loss` and `take_profit` (structural opinion) plus `selected_levels`; these drive entry-quality gating even though the template executes the orders.\n\n")
	}

	// 8. Custom Prompt
	if e.config.CustomPrompt != "" {
		sb.WriteString("# 📌 Personalized Trading Strategy\n\n")
		sb.WriteString(e.config.CustomPrompt)
		sb.WriteString("\n\n")
		sb.WriteString("Note: The above personalized strategy is a supplement to the basic rules and cannot violate the basic risk control principles.\n")
	}

	// 9. Output Discipline (Critical - prevents token waste)
	sb.WriteString("\n---\n\n")
	if lang == LangChinese {
		sb.WriteString("## ⚠️ 输出纪律（关键）\n\n")
		sb.WriteString("**硬性限制**：\n")
		sb.WriteString("- reasoning 部分：最多 300 词\n")
		sb.WriteString("- 每个决策的 reasoning 字段：最多 30 词\n")
		sb.WriteString("- 总响应：目标 2500 tokens，绝对上限 4000 tokens\n")
		sb.WriteString("- 如果超过 4000 tokens，响应将被截断并拒绝\n\n")
		sb.WriteString("**禁止内容**：\n")
		sb.WriteString("- 市场数据摘要（我已有数据）\n")
		sb.WriteString("- 重复解释\n")
		sb.WriteString("- 决策 JSON 之外的冗长分析\n")
		sb.WriteString("- 哲学性评论或泛泛而谈\n\n")
		sb.WriteString("精准、简洁、惜字如金。\n")
	} else {
		sb.WriteString("## ⚠️ OUTPUT DISCIPLINE (CRITICAL)\n\n")
		sb.WriteString("**HARD LIMITS**:\n")
		sb.WriteString("- reasoning section: MAX 300 words\n")
		sb.WriteString("- Each decision's reasoning field: MAX 30 words\n")
		sb.WriteString("- Total response: TARGET 2500 tokens, ABSOLUTE MAX 4000 tokens\n")
		sb.WriteString("- If you exceed 4000 tokens, the response will be truncated and rejected\n\n")
		sb.WriteString("**FORBIDDEN**:\n")
		sb.WriteString("- Market data summaries (I already have the data)\n")
		sb.WriteString("- Duplicate explanations\n")
		sb.WriteString("- Verbose analysis outside the decision JSON\n")
		sb.WriteString("- Philosophical commentary\n\n")
		sb.WriteString("Be precise. Be concise. Value every token.\n")
	}

	return sb.String()
}

// anyProtectionLegAI reports whether at least one protection leg (full / ladder /
// drawdown / break-even) is in AI mode. When NONE are, the AI's protection_plan is
// fully discarded at execution (see trader/protection_execution.go usesManualProtection
// and protectionRouteRequiresDecisionPlan), so emitting the full protection_plan schema
// into the system prompt is wasted tokens with zero effect on behavior. In that case we
// skip the schema entirely and only keep the top-level structural SL/TP opinion (which
// still drives entry-quality gating and the manual-vs-structural deviation panel).
func (e *StrategyEngine) anyProtectionLegAI() bool {
	prot := e.config.Protection
	return (prot.FullTPSL.Enabled && prot.FullTPSL.Mode == store.ProtectionModeAI) ||
		(prot.LadderTPSL.Enabled && prot.LadderTPSL.Mode == store.ProtectionModeAI) ||
		(prot.DrawdownTakeProfit.Enabled && prot.DrawdownTakeProfit.Mode == store.ProtectionModeAI) ||
		(prot.BreakEvenStop.Enabled && prot.BreakEvenStop.Mode == store.ProtectionModeAI)
}

func (e *StrategyEngine) writeAvailableIndicators(sb *strings.Builder) {
	indicators := e.config.Indicators
	kline := indicators.Klines

	sb.WriteString(fmt.Sprintf("- %s price series", kline.PrimaryTimeframe))
	if kline.EnableMultiTimeframe {
		sb.WriteString(fmt.Sprintf(" + %s K-line series\n", kline.LongerTimeframe))
	} else {
		sb.WriteString("\n")
	}

	if indicators.EnableEMA {
		sb.WriteString("- EMA indicators")
		if len(indicators.EMAPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (periods: %v)", indicators.EMAPeriods))
		}
		sb.WriteString("\n")
	}

	if indicators.EnableMACD {
		sb.WriteString("- MACD indicators\n")
	}

	if indicators.EnableRSI {
		sb.WriteString("- RSI indicators")
		if len(indicators.RSIPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (periods: %v)", indicators.RSIPeriods))
		}
		sb.WriteString("\n")
	}

	if indicators.EnableATR {
		sb.WriteString("- ATR indicators")
		if len(indicators.ATRPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (periods: %v)", indicators.ATRPeriods))
		}
		sb.WriteString("\n")
	}

	if indicators.EnableBOLL {
		sb.WriteString("- Bollinger Bands (BOLL) - Upper/Middle/Lower bands")
		if len(indicators.BOLLPeriods) > 0 {
			sb.WriteString(fmt.Sprintf(" (periods: %v)", indicators.BOLLPeriods))
		}
		sb.WriteString("\n")
	}

	if indicators.EnableVolume {
		sb.WriteString("- Volume data\n")
	}

	if indicators.EnableOI {
		sb.WriteString("- Open Interest (OI) data\n")
	}

	if indicators.EnableFundingRate {
		sb.WriteString("- Funding rate\n")
	}

	if len(e.config.CoinSource.StaticCoins) > 0 || e.config.CoinSource.UseAI500 || e.config.CoinSource.UseOITop {
		sb.WriteString("- AI500 / OI_Top filter tags (if available)\n")
	}

	if indicators.EnableQuantData {
		sb.WriteString("- Quantitative data (institutional/retail fund flow, position changes, multi-period price changes)\n")
	}
}

// ============================================================================
// Prompt Building - User Prompt
// ============================================================================

// BuildUserPrompt builds User Prompt based on strategy configuration
func (e *StrategyEngine) BuildUserPrompt(ctx *Context) string {
	var sb strings.Builder
	lang := e.GetLanguage()

	// Hard language reminder in user prompt too (reinforces system prompt)
	if lang == LangChinese {
		sb.WriteString("【硬性要求】本次所有分析、解释、结构理由、保护方案说明必须使用中文；JSON 字段名保持英文，但字段值中的自然语言文本必须是中文。\n\n")
	} else {
		sb.WriteString("[HARD REQUIREMENT] All analysis, explanations, structural rationale, and protection-plan explanatory text must be in English. Keep JSON field names in English, but any natural-language values inside JSON must also be English.\n\n")
	}

	// System status
	if lang == LangChinese {
		sb.WriteString(fmt.Sprintf("时间: %s | 周期: #%d | 运行时长: %d 分钟\n\n",
			ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))
	} else {
		sb.WriteString(fmt.Sprintf("Time: %s | Period: #%d | Runtime: %d minutes\n\n",
			ctx.CurrentTime, ctx.CallCount, ctx.RuntimeMinutes))
	}

	// BTC market
	if btcData, hasBTC := ctx.MarketDataMap["BTCUSDT"]; hasBTC {
		if lang == LangChinese {
			sb.WriteString(fmt.Sprintf("BTC: %s (1h: %s%%, 4h: %s%%) | MACD: %s | RSI: %s\n\n",
				formatAIFloat(btcData.CurrentPrice), formatAISignedFloat(btcData.PriceChange1h), formatAISignedFloat(btcData.PriceChange4h),
				formatAIFloat(btcData.CurrentMACD), formatAIFloat(btcData.CurrentRSI7)))
		} else {
			sb.WriteString(fmt.Sprintf("BTC: %s (1h: %s%%, 4h: %s%%) | MACD: %s | RSI: %s\n\n",
				formatAIFloat(btcData.CurrentPrice), formatAISignedFloat(btcData.PriceChange1h), formatAISignedFloat(btcData.PriceChange4h),
				formatAIFloat(btcData.CurrentMACD), formatAIFloat(btcData.CurrentRSI7)))
		}
	}

	// Account information
	if lang == LangChinese {
		sb.WriteString(fmt.Sprintf("账户: 权益 %s | 可用余额 %s (%s%%) | 盈亏 %s%% | 保证金 %s%% | 持仓 %d\n\n",
			formatAIFloat(ctx.Account.TotalEquity),
			formatAIFloat(ctx.Account.AvailableBalance),
			formatAIFloat((ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100),
			formatAISignedFloat(ctx.Account.TotalPnLPct),
			formatAIFloat(ctx.Account.MarginUsedPct),
			ctx.Account.PositionCount))
	} else {
		sb.WriteString(fmt.Sprintf("Account: Equity %s | Balance %s (%s%%) | PnL %s%% | Margin %s%% | Positions %d\n\n",
			formatAIFloat(ctx.Account.TotalEquity),
			formatAIFloat(ctx.Account.AvailableBalance),
			formatAIFloat((ctx.Account.AvailableBalance/ctx.Account.TotalEquity)*100),
			formatAISignedFloat(ctx.Account.TotalPnLPct),
			formatAIFloat(ctx.Account.MarginUsedPct),
			ctx.Account.PositionCount))
	}

	// Recently completed orders (placed before positions to ensure visibility)
	if len(ctx.RecentOrders) > 0 {
		if lang == LangChinese {
			sb.WriteString("## 最近已完成交易\n")
		} else {
			sb.WriteString("## Recent Completed Trades\n")
		}
		for i, order := range ctx.RecentOrders {
			resultStr := "Profit"
			if lang == LangChinese {
				resultStr = "盈利"
			}
			if order.RealizedPnL < 0 {
				if lang == LangChinese {
					resultStr = "亏损"
				} else {
					resultStr = "Loss"
				}
			}
			if lang == LangChinese {
				sb.WriteString(fmt.Sprintf("%d. %s %s | 开仓 %s 平仓 %s | %s: %s USDT (%s%%) | %s→%s (%s)\n",
					i+1, order.Symbol, order.Side,
					formatAIFloat(order.EntryPrice), formatAIFloat(order.ExitPrice),
					resultStr, formatAISignedFloat(order.RealizedPnL), formatAISignedFloat(order.PnLPct),
					order.EntryTime, order.ExitTime, order.HoldDuration))
			} else {
				sb.WriteString(fmt.Sprintf("%d. %s %s | Entry %s Exit %s | %s: %s USDT (%s%%) | %s→%s (%s)\n",
					i+1, order.Symbol, order.Side,
					formatAIFloat(order.EntryPrice), formatAIFloat(order.ExitPrice),
					resultStr, formatAISignedFloat(order.RealizedPnL), formatAISignedFloat(order.PnLPct),
					order.EntryTime, order.ExitTime, order.HoldDuration))
			}
		}
		sb.WriteString("\n")
	}

	// Historical trading statistics (helps AI understand past performance)
	if ctx.TradingStats != nil && ctx.TradingStats.TotalTrades > 0 {
		// Get language from strategy config
		lang := e.GetLanguage()

		// Win/Loss ratio
		var winLossRatio float64
		if ctx.TradingStats.AvgLoss > 0 {
			winLossRatio = ctx.TradingStats.AvgWin / ctx.TradingStats.AvgLoss
		}

		if lang == LangChinese {
			sb.WriteString("## 历史交易统计\n")
			sb.WriteString(fmt.Sprintf("总交易: %d 笔 | 盈利因子: %.2f | 夏普比率: %.2f | 盈亏比: %.2f\n",
				ctx.TradingStats.TotalTrades,
				ctx.TradingStats.ProfitFactor,
				ctx.TradingStats.SharpeRatio,
				winLossRatio))
			sb.WriteString(fmt.Sprintf("总盈亏: %+.2f USDT | 平均盈利: +%.2f | 平均亏损: -%.2f | 最大回撤: %.1f%%\n",
				ctx.TradingStats.TotalPnL,
				ctx.TradingStats.AvgWin,
				ctx.TradingStats.AvgLoss,
				ctx.TradingStats.MaxDrawdownPct))

		} else {
			sb.WriteString("## Historical Trading Statistics\n")
			sb.WriteString(fmt.Sprintf("Total Trades: %d | Profit Factor: %.2f | Sharpe: %.2f | Win/Loss Ratio: %.2f\n",
				ctx.TradingStats.TotalTrades,
				ctx.TradingStats.ProfitFactor,
				ctx.TradingStats.SharpeRatio,
				winLossRatio))
			sb.WriteString(fmt.Sprintf("Total PnL: %+.2f USDT | Avg Win: +%.2f | Avg Loss: -%.2f | Max Drawdown: %.1f%%\n",
				ctx.TradingStats.TotalPnL,
				ctx.TradingStats.AvgWin,
				ctx.TradingStats.AvgLoss,
				ctx.TradingStats.MaxDrawdownPct))

		}
		sb.WriteString("\n")
	}

	// Position information
	if len(ctx.Positions) > 0 {
		sb.WriteString("## Current Positions\n")
		for i, pos := range ctx.Positions {
			sb.WriteString(e.formatPositionInfo(i+1, pos, ctx))
		}
	} else {
		sb.WriteString("Current Positions: None\n\n")
	}

	// Candidate coins (exclude coins already in positions to avoid duplicate data)
	positionSymbols := make(map[string]bool)
	for _, pos := range ctx.Positions {
		// Normalize symbol to handle both "ETH" and "ETHUSDT" formats
		normalizedSymbol := market.Normalize(pos.Symbol)
		positionSymbols[normalizedSymbol] = true
	}

	seenCandidateSymbols := make(map[string]bool)
	displayableCount := 0
	for _, coin := range ctx.CandidateCoins {
		normalizedCoinSymbol := market.Normalize(coin.Symbol)
		if positionSymbols[normalizedCoinSymbol] || seenCandidateSymbols[normalizedCoinSymbol] {
			continue
		}
		if _, hasData := ctx.MarketDataMap[coin.Symbol]; !hasData {
			continue
		}
		seenCandidateSymbols[normalizedCoinSymbol] = true
		displayableCount++
	}

	sb.WriteString(fmt.Sprintf("## Candidate Coins (%d coins)\n\n", displayableCount))
	displayedCount := 0
	displayedCandidateSymbols := make(map[string]bool)
	for _, coin := range ctx.CandidateCoins {
		// Skip if this coin is already a position (data already shown in positions section)
		normalizedCoinSymbol := market.Normalize(coin.Symbol)
		if positionSymbols[normalizedCoinSymbol] || displayedCandidateSymbols[normalizedCoinSymbol] {
			continue
		}

		marketData, hasData := ctx.MarketDataMap[coin.Symbol]
		if !hasData {
			continue
		}
		displayedCandidateSymbols[normalizedCoinSymbol] = true
		displayedCount++

		sourceTags := e.formatCoinSourceTag(coin.Sources)
		sb.WriteString(fmt.Sprintf("### %d. %s%s\n\n", displayedCount, coin.Symbol, sourceTags))
		sb.WriteString(e.formatMarketData(marketData))
		sb.WriteString(e.formatMarketContextV2(coin.Symbol, marketData))

		if ctx.QuantDataMap != nil {
			if quantData, hasQuant := ctx.QuantDataMap[coin.Symbol]; hasQuant {
				sb.WriteString(e.formatQuantData(quantData))
			}
		}
		// (removed 2026-08-04) "Historical Performance Profile" was injected here.
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// Get language for market data formatting
	nofxosLang := nofxos.LangEnglish
	if e.GetLanguage() == LangChinese {
		nofxosLang = nofxos.LangChinese
	}

	// OI Ranking data (market-wide open interest changes)
	if ctx.OIRankingData != nil {
		sb.WriteString(nofxos.FormatOIRankingForAI(ctx.OIRankingData, nofxosLang))
	}

	// NetFlow Ranking data (market-wide fund flow)
	if ctx.NetFlowRankingData != nil {
		sb.WriteString(nofxos.FormatNetFlowRankingForAI(ctx.NetFlowRankingData, nofxosLang))
	}

	// Price Ranking data (market-wide gainers/losers)
	if ctx.PriceRankingData != nil {
		sb.WriteString(nofxos.FormatPriceRankingForAI(ctx.PriceRankingData, nofxosLang))
	}

	// Optional data availability: never fail closed; continue with available exchange/market data.
	if len(ctx.OptionalDataStates) > 0 {
		sb.WriteString("## Optional Data Availability\n")
		for _, state := range ctx.OptionalDataStates {
			status := "missing"
			if state.Available {
				status = "available"
			}
			sb.WriteString(fmt.Sprintf("- source=%s status=%s", state.Source, status))
			if state.Reason != "" {
				sb.WriteString(fmt.Sprintf(" reason=%s", state.Reason))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("Rule: optional data absence is not a no-trade signal by itself; use remaining price/structure/exchange data and mention uncertainty if relevant.\n\n")
	}

	sb.WriteString("---\n\n")
	sb.WriteString("Now please analyze and output your decision (Chain of Thought + JSON)\n")

	return sb.String()
}

func (e *StrategyEngine) formatPositionInfo(index int, pos PositionInfo, ctx *Context) string {
	var sb strings.Builder

	holdingDuration := ""
	if pos.UpdateTime > 0 {
		durationMs := time.Now().UnixMilli() - pos.UpdateTime
		durationMin := durationMs / (1000 * 60)
		if durationMin < 60 {
			holdingDuration = fmt.Sprintf(" | Holding Duration %d min", durationMin)
		} else {
			durationHour := durationMin / 60
			durationMinRemainder := durationMin % 60
			holdingDuration = fmt.Sprintf(" | Holding Duration %dh %dm", durationHour, durationMinRemainder)
		}
	}

	positionValue := pos.Quantity * pos.MarkPrice
	if positionValue < 0 {
		positionValue = -positionValue
	}

	sb.WriteString(fmt.Sprintf("%d. %s %s | Entry %s Current %s | Qty %s | Position Value %s USDT | PnL%s%% | PnL Amount%s USDT | Peak PnL%s%% | Leverage %dx | Margin %s | Liq Price %s%s\n\n",
		index, pos.Symbol, strings.ToUpper(pos.Side),
		formatAIFloat(pos.EntryPrice), formatAIFloat(pos.MarkPrice), formatAIFloat(pos.Quantity), formatAIFloat(positionValue), formatAISignedFloat(pos.UnrealizedPnLPct), formatAISignedFloat(pos.UnrealizedPnL), formatAIFloat(pos.PeakPnLPct),
		pos.Leverage, formatAIFloat(pos.MarginUsed), formatAIFloat(pos.LiquidationPrice), holdingDuration))

	if marketData, ok := ctx.MarketDataMap[pos.Symbol]; ok {
		sb.WriteString(e.formatMarketData(marketData))
		sb.WriteString(e.formatMarketContextV2(pos.Symbol, marketData))

		if ctx.QuantDataMap != nil {
			if quantData, hasQuant := ctx.QuantDataMap[pos.Symbol]; hasQuant {
				sb.WriteString(e.formatQuantData(quantData))
			}
		}
		// (removed 2026-08-04) "Historical Performance" was injected here.
		sb.WriteString("\n")
	}

	return sb.String()
}

func (e *StrategyEngine) formatCoinSourceTag(sources []string) string {
	if len(sources) > 1 {
		// Multiple signal source combination
		hasAI500 := false
		hasOITop := false
		hasOILow := false
		hasHyperAll := false
		hasHyperMain := false
		for _, s := range sources {
			switch s {
			case "ai500":
				hasAI500 = true
			case "oi_top":
				hasOITop = true
			case "oi_low":
				hasOILow = true
			case "hyper_all":
				hasHyperAll = true
			case "hyper_main":
				hasHyperMain = true
			}
		}
		if hasAI500 && hasOITop {
			return " (AI500+OI_Top dual signal)"
		}
		if hasAI500 && hasOILow {
			return " (AI500+OI_Low dual signal)"
		}
		if hasOITop && hasOILow {
			return " (OI_Top+OI_Low)"
		}
		if hasHyperMain && hasAI500 {
			return " (HyperMain+AI500)"
		}
		if hasHyperAll || hasHyperMain {
			return " (Hyperliquid)"
		}
		return " (Multiple sources)"
	} else if len(sources) == 1 {
		switch sources[0] {
		case "ai500":
			return " (AI500)"
		case "oi_top":
			return " (OI_Top OI increase)"
		case "oi_low":
			return " (OI_Low OI decrease)"
		case "static":
			return " (Manual selection)"
		case "hyper_all":
			return " (Hyperliquid All)"
		case "hyper_main":
			return " (Hyperliquid Top20)"
		}
	}
	return ""
}

// ============================================================================
// Market Data Formatting
// ============================================================================

func (e *StrategyEngine) formatMarketData(data *market.Data) string {
	var sb strings.Builder
	indicators := e.config.Indicators

	// Clearly label the coin symbol
	sb.WriteString(fmt.Sprintf("=== %s Market Data ===\n\n", data.Symbol))
	sb.WriteString(fmt.Sprintf("current_price = %s", formatAIFloat(data.CurrentPrice)))

	if indicators.EnableEMA {
		sb.WriteString(fmt.Sprintf(", current_ema20 = %s", formatAIFloat(data.CurrentEMA20)))
	}

	if indicators.EnableMACD {
		sb.WriteString(fmt.Sprintf(", current_macd = %s", formatAIFloat(data.CurrentMACD)))
	}

	if indicators.EnableRSI {
		sb.WriteString(fmt.Sprintf(", current_rsi7 = %s", formatAIFloat(data.CurrentRSI7)))
	}

	sb.WriteString("\n\n")

	if indicators.EnableOI || indicators.EnableFundingRate {
		sb.WriteString(fmt.Sprintf("Additional data for %s:\n\n", data.Symbol))

		if indicators.EnableOI && data.OpenInterest != nil {
			sb.WriteString(fmt.Sprintf("Open Interest: latest=%s average=%s\n\n",
				formatAIFloat(data.OpenInterest.Latest), formatAIFloat(data.OpenInterest.Average)))
		}

		if indicators.EnableFundingRate {
			sb.WriteString(fmt.Sprintf("Funding Rate: %s\n\n", formatAIFloat(data.FundingRate)))
		}
	}

	if len(data.TimeframeData) > 0 {
		timeframeOrder := []string{"1m", "3m", "5m", "15m", "30m", "1h", "2h", "4h", "6h", "8h", "12h", "1d", "3d", "1w"}
		for _, tf := range timeframeOrder {
			if tfData, ok := data.TimeframeData[tf]; ok {
				sb.WriteString(fmt.Sprintf("=== %s Timeframe (oldest → latest) ===\n\n", strings.ToUpper(tf)))
				e.formatTimeframeSeriesData(&sb, tfData, indicators)
			}
		}
	} else {
		// Compatible with old data format
		if data.IntradaySeries != nil {
			klineConfig := indicators.Klines
			sb.WriteString(fmt.Sprintf("Intraday series (%s intervals, oldest → latest):\n\n", klineConfig.PrimaryTimeframe))

			if len(data.IntradaySeries.MidPrices) > 0 {
				sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.IntradaySeries.MidPrices)))
			}

			if indicators.EnableEMA && len(data.IntradaySeries.EMA20Values) > 0 {
				sb.WriteString(fmt.Sprintf("EMA indicators (20-period): %s\n\n", formatFloatSlice(data.IntradaySeries.EMA20Values)))
			}

			if indicators.EnableMACD && len(data.IntradaySeries.MACDValues) > 0 {
				sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.IntradaySeries.MACDValues)))
			}

			if indicators.EnableRSI {
				if len(data.IntradaySeries.RSI7Values) > 0 {
					sb.WriteString(fmt.Sprintf("RSI indicators (7-Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI7Values)))
				}
				if len(data.IntradaySeries.RSI14Values) > 0 {
					sb.WriteString(fmt.Sprintf("RSI indicators (14-Period): %s\n\n", formatFloatSlice(data.IntradaySeries.RSI14Values)))
				}
			}

			if indicators.EnableVolume && len(data.IntradaySeries.Volume) > 0 {
				sb.WriteString(fmt.Sprintf("Volume: %s\n\n", formatFloatSlice(data.IntradaySeries.Volume)))
			}

			if indicators.EnableATR {
				sb.WriteString(fmt.Sprintf("3m ATR (14-period): %s\n\n", formatAIFloat(data.IntradaySeries.ATR14)))
			}
		}

		if data.LongerTermContext != nil && indicators.Klines.EnableMultiTimeframe {
			sb.WriteString(fmt.Sprintf("Longer-term context (%s timeframe):\n\n", indicators.Klines.LongerTimeframe))

			if indicators.EnableEMA {
				sb.WriteString(fmt.Sprintf("20-Period EMA: %s vs. 50-Period EMA: %s\n\n",
					formatAIFloat(data.LongerTermContext.EMA20), formatAIFloat(data.LongerTermContext.EMA50)))
			}

			if indicators.EnableATR {
				sb.WriteString(fmt.Sprintf("3-Period ATR: %s vs. 14-Period ATR: %s\n\n",
					formatAIFloat(data.LongerTermContext.ATR3), formatAIFloat(data.LongerTermContext.ATR14)))
			}

			if indicators.EnableVolume {
				sb.WriteString(fmt.Sprintf("Current Volume: %s vs. Average Volume: %s\n\n",
					formatAIFloat(data.LongerTermContext.CurrentVolume), formatAIFloat(data.LongerTermContext.AverageVolume)))
			}

			if indicators.EnableMACD && len(data.LongerTermContext.MACDValues) > 0 {
				sb.WriteString(fmt.Sprintf("MACD indicators: %s\n\n", formatFloatSlice(data.LongerTermContext.MACDValues)))
			}

			if indicators.EnableRSI && len(data.LongerTermContext.RSI14Values) > 0 {
				sb.WriteString(fmt.Sprintf("RSI indicators (14-Period): %s\n\n", formatFloatSlice(data.LongerTermContext.RSI14Values)))
			}
		}
	}

	// Sentiment and structural data
	sb.WriteString(formatSentimentDataEN(data, indicators))
	sb.WriteString(formatStructuralLevelsEN(data))
	sb.WriteString(e.formatDerivativesDeepDive(data, indicators))

	return sb.String()
}

// formatDerivativesDeepDive appends a Derivatives Deep Dive section computed
// from kline data already present in market.Data. Only emits the section when
// at least one relevant indicator toggle is enabled.
func (e *StrategyEngine) formatDerivativesDeepDive(data *market.Data, indicators store.IndicatorConfig) string {
	if data == nil {
		return ""
	}
	if !indicators.EnableCVD && !indicators.EnableOIGrowthRate &&
		!indicators.EnableFundingHistory && !indicators.EnableVWAP &&
		!indicators.EnableTakerDelta && !indicators.EnableDepthChangeRate {
		return ""
	}

	enriched := market.BuildDerivativesEnriched(data, indicators.EnableCVD, indicators.EnableOIGrowthRate,
		indicators.EnableFundingHistory, indicators.EnableVWAP, indicators.EnableTakerDelta,
		indicators.EnableDepthChangeRate)
	if enriched == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## Derivatives Deep Dive\n")

	if indicators.EnableCVD {
		cvd1hLabel := "neutral"
		if enriched.CVD1h > 0 {
			cvd1hLabel = "buyers accumulating"
		} else if enriched.CVD1h < 0 {
			cvd1hLabel = "sellers dominant"
		}
		cvd4hLabel := "neutral"
		if enriched.CVD4h > 0 {
			cvd4hLabel = "buyers accumulating"
		} else if enriched.CVD4h < 0 {
			cvd4hLabel = "sellers dominant"
		}
		sb.WriteString(fmt.Sprintf("CVD(1h): %+.0f (%s)  |  CVD(4h): %+.0f (%s)\n",
			enriched.CVD1h, cvd1hLabel, enriched.CVD4h, cvd4hLabel))
	}

	if indicators.EnableOIGrowthRate {
		sb.WriteString(fmt.Sprintf("OI Growth: 1h=%+.2f%%, 4h=%+.2f%%\n",
			enriched.OIGrowthRate1h, enriched.OIGrowthRate4h))
	}

	if indicators.EnableFundingHistory && len(enriched.FundingHistory) > 0 {
		parts := make([]string, len(enriched.FundingHistory))
		for i, v := range enriched.FundingHistory {
			parts[i] = fmt.Sprintf("%.4f", v)
		}
		sb.WriteString(fmt.Sprintf("Funding Trend: %s (last %d: %s)\n",
			enriched.FundingTrend, len(enriched.FundingHistory), strings.Join(parts, ", ")))
	}

	if indicators.EnableVWAP && enriched.VWAP > 0 {
		sign := "+"
		if enriched.PriceVsVWAP < 0 {
			sign = ""
		}
		sb.WriteString(fmt.Sprintf("VWAP: %s (price %s%.2f%% vs VWAP)\n",
			formatAIFloat(enriched.VWAP), sign, enriched.PriceVsVWAP))
	}

	if indicators.EnableTakerDelta {
		takerLabel := "neutral"
		if enriched.TakerDelta > 0.3 {
			takerLabel = "strong buy pressure"
		} else if enriched.TakerDelta > 0.1 {
			takerLabel = "mild buy pressure"
		} else if enriched.TakerDelta < -0.3 {
			takerLabel = "strong sell pressure"
		} else if enriched.TakerDelta < -0.1 {
			takerLabel = "mild sell pressure"
		}
		sb.WriteString(fmt.Sprintf("Taker Delta: %+.2f (%s)\n", enriched.TakerDelta, takerLabel))
	}

	if indicators.EnableDepthChangeRate {
		depthLabel := "stable"
		if enriched.DepthChangeRate > 0 {
			depthLabel = "bids strengthening"
		} else if enriched.DepthChangeRate < 0 {
			depthLabel = "asks strengthening"
		}
		sb.WriteString(fmt.Sprintf("Depth Change: %+.1f%% (%s)\n", enriched.DepthChangeRate, depthLabel))
	}

	return sb.String()
}

func (e *StrategyEngine) formatTimeframeSeriesData(sb *strings.Builder, data *market.TimeframeSeriesData, indicators store.IndicatorConfig) {
	if len(data.Klines) > 0 {
		sb.WriteString("Time(UTC)      Open      High      Low       Close     Volume\n")
		for i, k := range data.Klines {
			t := time.Unix(k.Time/1000, 0).UTC()
			timeStr := t.Format("01-02 15:04")
			marker := ""
			if i == len(data.Klines)-1 {
				marker = "  <- current"
			}
			sb.WriteString(fmt.Sprintf("%-14s %-9.4f %-9.4f %-9.4f %-9.4f %-12.2f%s\n",
				timeStr, k.Open, k.High, k.Low, k.Close, k.Volume, marker))
		}
		sb.WriteString("\n")
	} else if len(data.MidPrices) > 0 {
		sb.WriteString(fmt.Sprintf("Mid prices: %s\n\n", formatFloatSlice(data.MidPrices)))
		if indicators.EnableVolume && len(data.Volume) > 0 {
			sb.WriteString(fmt.Sprintf("Volume: %s\n\n", formatFloatSlice(data.Volume)))
		}
	}

	if indicators.EnableEMA {
		if len(data.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA20: %s\n", formatFloatSlice(data.EMA20Values)))
		}
		if len(data.EMA50Values) > 0 {
			sb.WriteString(fmt.Sprintf("EMA50: %s\n", formatFloatSlice(data.EMA50Values)))
		}
	}

	if indicators.EnableMACD && len(data.MACDValues) > 0 {
		sb.WriteString(fmt.Sprintf("MACD: %s\n", formatFloatSlice(data.MACDValues)))
	}

	if indicators.EnableRSI {
		if len(data.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI7: %s\n", formatFloatSlice(data.RSI7Values)))
		}
		if len(data.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("RSI14: %s\n", formatFloatSlice(data.RSI14Values)))
		}
	}

	if indicators.EnableATR && data.ATR14 > 0 {
		sb.WriteString(fmt.Sprintf("ATR14: %s\n", formatAIFloat(data.ATR14)))
	}

	if indicators.EnableBOLL && len(data.BOLLUpper) > 0 {
		sb.WriteString(fmt.Sprintf("BOLL Upper: %s\n", formatFloatSlice(data.BOLLUpper)))
		sb.WriteString(fmt.Sprintf("BOLL Middle: %s\n", formatFloatSlice(data.BOLLMiddle)))
		sb.WriteString(fmt.Sprintf("BOLL Lower: %s\n", formatFloatSlice(data.BOLLLower)))
	}

	sb.WriteString("\n")
}

func (e *StrategyEngine) formatMarketContextV2(symbol string, data *market.Data) string {
	ctx := market.BuildMarketContextV2(symbol, data, []string{"15m", "1h", "4h", "1d"}, "1h")
	if ctx == nil || ctx.RegimeRules == nil {
		return ""
	}
	// Flag-aware regime rule: when SoftRegimeStructureFit is on (default), a
	// setup/regime mismatch is a soft size penalty — a high-conviction
	// counter-structure trade opens at reduced size rather than waiting. When
	// off, the mismatch stays a hard gate ("otherwise wait").
	softRegimeFit := true
	if e.config != nil {
		if f := e.config.EntryStructure.EntryGate.WithDefaults().SoftRegimeStructureFit; f != nil {
			softRegimeFit = *f
		}
	}
	regimeRule := "  rule: open only when setup_type is compatible with allowed_setups and structural anchors satisfy structure_mode; otherwise wait.\n"
	if softRegimeFit {
		regimeRule = "  rule: prefer setups compatible with allowed_setups that satisfy structure_mode. A setup/regime MISMATCH is not an automatic wait — if conviction is high you may still open a counter-structure trade, but the backend reduces its position size (soft penalty). Reserve a full wait for genuine invalidation (fake-retest trap, protection policy rejection) or when there is no real edge.\n"
	}
	snapshot := market.BuildCompositeMarketSnapshotFromExistingData("okx", []string{"15m", "1h", "4h", "1d"}, "1h", 180*time.Second, data)
	if snapshot != nil && snapshot.AICompact != "" {
		return "Composite Market Context (shared human/AI source):\n" + snapshot.AICompact + regimeRule + "  For any open with ladder protection, stop_loss_price must be an explicit structural invalidation price beyond support/resistance/fibonacci plus ATR/wick buffer; stop_loss_pct is only a derived display value, never the planning input.\n"
	}
	var sb strings.Builder
	sb.WriteString("Execution Regime Guidance:\n")
	sb.WriteString(fmt.Sprintf("  regime=%s structure_mode=%s fibonacci_mode=%s\n", ctx.RegimeRules.Regime, ctx.RegimeRules.StructureMode, ctx.RegimeRules.FibonacciMode))
	if ctx.TrendPhase != nil {
		sb.WriteString(fmt.Sprintf("  trend_phase=%s direction=%s extension=%.1f%% momentum_decay=%.2f\n", ctx.TrendPhase.Phase, ctx.TrendPhase.Direction, ctx.TrendPhase.ExtensionPct, ctx.TrendPhase.MomentumDecay))
		if ctx.TrendPhase.Warning != "" {
			sb.WriteString(fmt.Sprintf("  ⚠️ %s\n", ctx.TrendPhase.Warning))
		}
	}
	if len(ctx.RegimeRules.AllowedSetups) > 0 {
		sb.WriteString(fmt.Sprintf("  allowed_setups=%s\n", strings.Join(ctx.RegimeRules.AllowedSetups, ",")))
	}
	if len(ctx.RegimeRules.RequiredAnchors) > 0 {
		sb.WriteString(fmt.Sprintf("  required_anchors=%s\n", strings.Join(ctx.RegimeRules.RequiredAnchors, ",")))
	}
	if ctx.RegimeRules.ProtectionGuidance != "" {
		sb.WriteString(fmt.Sprintf("  protection_guidance=%s\n", ctx.RegimeRules.ProtectionGuidance))
	}
	if ctx.RegimeRules.RegimeReversalRisk {
		sb.WriteString(fmt.Sprintf("  ⚠️ Regime reversal risk: %s\n", ctx.RegimeRules.ReversalRiskReason))
		sb.WriteString("  → Consider whether the trend is still intact before opening in the trend direction\n")
	}
	if ctx.Derivatives != nil {
		sb.WriteString(fmt.Sprintf("  derivatives: funding_bias=%s squeeze_risk=%s oi_1h=%.2f%% volume_z=%.2f\n", ctx.Derivatives.FundingBias, ctx.Derivatives.SqueezeRisk, ctx.Derivatives.OIChange1hPct, ctx.Derivatives.VolumeZScore))
	}
	if ctx.Quant != nil && ctx.Quant.DataQuality != "" && ctx.Quant.DataQuality != "missing" {
		sb.WriteString(fmt.Sprintf("  quant: flow_bias=%s crowding=%s inst_future_1h=%s retail_future_1h=%s oi_1h=%.2f%%\n", ctx.Quant.FlowBias, ctx.Quant.CrowdingRisk, formatFlowValue(ctx.Quant.InstitutionFuture1h), formatFlowValue(ctx.Quant.RetailFuture1h), ctx.Quant.OIChange1hPct))
	}
	if ctx.ExchangeFlow != nil && ctx.ExchangeFlow.DataQuality != "" && ctx.ExchangeFlow.DataQuality != "missing" {
		sb.WriteString(fmt.Sprintf("  exchange_flow: funding=%s long_short=%s taker=%s depth=%s crowding=%s depth_total=%s\n", ctx.ExchangeFlow.FundingBias, ctx.ExchangeFlow.LongShortSkew, ctx.ExchangeFlow.TakerFlowBias, ctx.ExchangeFlow.DepthBias, ctx.ExchangeFlow.CrowdingRisk, formatFlowValue(ctx.ExchangeFlow.DepthTotalUSDT)))
	}
	sb.WriteString(regimeRule)
	return sb.String()
}

func (e *StrategyEngine) formatQuantData(data *QuantData) string {
	if data == nil {
		return ""
	}

	indicators := e.config.Indicators
	if !indicators.EnableQuantOI && !indicators.EnableQuantNetflow {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 %s Quantitative Data:\n", data.Symbol))

	if len(data.PriceChange) > 0 {
		sb.WriteString("Price Change: ")
		timeframes := []string{"5m", "15m", "1h", "4h", "12h", "24h"}
		parts := []string{}
		for _, tf := range timeframes {
			if v, ok := data.PriceChange[tf]; ok {
				parts = append(parts, fmt.Sprintf("%s: %+.4f%%", tf, v*100))
			}
		}
		sb.WriteString(strings.Join(parts, " | "))
		sb.WriteString("\n")
	}

	if indicators.EnableQuantNetflow && data.Netflow != nil {
		sb.WriteString("Fund Flow (Netflow):\n")
		timeframes := []string{"5m", "15m", "1h", "4h", "12h", "24h"}

		if data.Netflow.Institution != nil {
			if data.Netflow.Institution.Future != nil && len(data.Netflow.Institution.Future) > 0 {
				sb.WriteString("  Institutional Futures:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Institution.Future[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
			if data.Netflow.Institution.Spot != nil && len(data.Netflow.Institution.Spot) > 0 {
				sb.WriteString("  Institutional Spot:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Institution.Spot[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
		}

		if data.Netflow.Personal != nil {
			if data.Netflow.Personal.Future != nil && len(data.Netflow.Personal.Future) > 0 {
				sb.WriteString("  Retail Futures:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Personal.Future[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
			if data.Netflow.Personal.Spot != nil && len(data.Netflow.Personal.Spot) > 0 {
				sb.WriteString("  Retail Spot:\n")
				for _, tf := range timeframes {
					if v, ok := data.Netflow.Personal.Spot[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %s\n", tf, formatFlowValue(v)))
					}
				}
			}
		}
	}

	if indicators.EnableQuantOI && len(data.OI) > 0 {
		for exchange, oiData := range data.OI {
			if len(oiData.Delta) > 0 {
				sb.WriteString(fmt.Sprintf("Open Interest (%s):\n", exchange))
				for _, tf := range []string{"5m", "15m", "1h", "4h", "12h", "24h"} {
					if d, ok := oiData.Delta[tf]; ok {
						sb.WriteString(fmt.Sprintf("    %s: %+.4f%% (%s)\n", tf, d.OIDeltaPercent, formatFlowValue(d.OIDeltaValue)))
					}
				}
			}
		}
	}

	return sb.String()
}

func formatFlowValue(v float64) string {
	sign := ""
	if v >= 0 {
		sign = "+"
	}
	absV := v
	if absV < 0 {
		absV = -absV
	}
	if absV >= 1e9 {
		return fmt.Sprintf("%s%.2fB", sign, v/1e9)
	} else if absV >= 1e6 {
		return fmt.Sprintf("%s%.2fM", sign, v/1e6)
	} else if absV >= 1e3 {
		return fmt.Sprintf("%s%.2fK", sign, v/1e3)
	}
	return fmt.Sprintf("%s%.2f", sign, v)
}

func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = formatAIFloat(v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

func buildDrawdownTierConstraintPrompt(cfg store.DrawdownTakeProfitConfig) string {
	if !cfg.Enabled || len(cfg.Rules) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Drawdown tier guidelines:\n")
	sb.WriteString("- CRITICAL: drawdown_rules min_profit_pct MUST be derived from multi-timeframe structural levels for each coin.\n")
	sb.WriteString("- Calculation logic:\n")
	sb.WriteString("  1. For each tier, pick a structural target from a DIFFERENT timeframe:\n")
	sb.WriteString("     - T1: primary TF (e.g. 1h) nearest resistance/support target\n")
	sb.WriteString("     - T2: higher TF (e.g. 4h) structural level or fib retracement\n")
	sb.WriteString("     - T3+: highest TF or fib extension targets\n")
	sb.WriteString("  2. min_profit_pct = (distance from entry to target) × pessimistic_factor\n")
	sb.WriteString("     - pessimistic_factor = 0.70~0.85 (assume price may reverse BEFORE reaching the level)\n")
	sb.WriteString("     - Example: target is 2.0% away → min_profit = 2.0% × 0.75 = 1.5%\n")
	sb.WriteString("     - This ensures DD protection activates even if price falls short of the structural target\n")
	sb.WriteString("  3. max_drawdown_pct = how much peak profit to give back before triggering:\n")
	sb.WriteString("     - Near targets (T1): 55-65% (tighter, lock profit quickly)\n")
	sb.WriteString("     - Mid targets (T2): 45-55%\n")
	sb.WriteString("     - Far targets (T3+): 35-50% (wider, allow trend room)\n")
	sb.WriteString("- Number of tiers is flexible (3, 4, 5...) — more structural targets = more tiers.\n")
	sb.WriteString("- FORBIDDEN: using fixed values like 0.8/1.5/2.5 or copying strategy config numbers. Each coin is unique.\n")
	sb.WriteString(fmt.Sprintf("- close_ratio_pct is fixed by strategy config (do NOT override): %s.\n",
		formatDrawdownCloseRatios(cfg.Rules)))
	sb.WriteString("- Sanity check: T1 min_profit must be > 0.5× ATR14 (as %% of entry) to avoid noise triggers.\n")
	return sb.String()
}

func formatDrawdownCloseRatios(rules []store.DrawdownTakeProfitRule) string {
	parts := make([]string, len(rules))
	for i, r := range rules {
		parts[i] = fmt.Sprintf("T%d=%.0f%%", i+1, r.CloseRatioPct)
	}
	return strings.Join(parts, ", ")
}
