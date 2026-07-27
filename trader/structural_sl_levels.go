package trader

import "strings"

// structuralSLLevels 是结构位止损的两级价位 + 元信息。
//
// 之所以抽出来:这两个价位原先只在 buildPositionProtectionRuntime 里就地算(面板用),
// 开仓快照(protection_plan_snapshots)完全没有它们 —— 于是"结构位"这道真实会成交的
// 保护在落库侧根本不存在,也就无法参与按价格的排序。两处各算一遍必然漂移,所以收成
// 一个函数,面板和快照都从这里取。
type structuralSLLevels struct {
	Enabled       bool
	CloseConfirm  bool
	BoundaryPrice float64 // Phase 2:收盘确认边界(guard 实际执行的、已 clamp 的价)
	BackstopPrice float64 // Phase 1:挂在交易所的安全网(entry ∓ backstopATRMul×ATR)
	FloorATRMul   float64
	BackstopMul   float64
}

// resolveStructuralSLLevels 解析某仓位的结构位两级价位。
// atrAtEntry 传 0 时会自行按冻结 ATR 取值(快照路径没有现成的 atrAtEntry)。
func (at *AutoTrader) resolveStructuralSLLevels(symbol, side string, entryPrice, atrAtEntry float64) structuralSLLevels {
	out := structuralSLLevels{}
	if at == nil || at.config.StrategyConfig == nil || entryPrice <= 0 {
		return out
	}
	ladderCfg := at.config.StrategyConfig.Protection.LadderTPSL
	sscfg := ladderCfg.StructuralSL
	if !sscfg.Enabled {
		return out
	}
	isLong := strings.EqualFold(strings.TrimSpace(side), "long")
	ss := sscfg.WithDefaults()
	acfg := at.config.StrategyConfig.ATRProtection
	out.Enabled = true
	out.CloseConfirm = ss.CloseConfirm
	out.FloorATRMul = ss.FloorATRMul
	out.BackstopMul = ss.BackstopATRMul

	if atrAtEntry <= 0 {
		if fa, ok := at.frozenATRForPosition(symbol, side, entryPrice, acfg); ok && fa > 0 {
			atrAtEntry = fa
		}
	}
	if b, ok := at.frozenStructBoundaryForPosition(symbol, entryPrice, isLong, false, sscfg, acfg); ok && b > 0 {
		// 用 clamp 后的边界(guard 真正执行的),不是原始 swing —— 否则会显示一个
		// 比安全网还远、永远不可能触发的 Struct 档。
		out.BoundaryPrice = b
		if atrAtEntry > 0 {
			tpTargetPct := ladderMaxTPTargetPct(ladderCfg.Rules, atrAtEntry, entryPrice, acfg)
			if clamped, cok := clampStructuralBoundary(entryPrice, b, atrAtEntry, tpTargetPct, isLong, sscfg); cok {
				out.BoundaryPrice = clamped
			}
		}
	}
	if atrAtEntry > 0 && ss.BackstopATRMul > 0 {
		dist := ss.BackstopATRMul * atrAtEntry
		if isLong {
			out.BackstopPrice = entryPrice - dist
		} else {
			out.BackstopPrice = entryPrice + dist
		}
	}
	return out
}
