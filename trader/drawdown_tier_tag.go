package trader

import (
	"sort"

	"nofx/store"
)

// ── 为什么挂单本身要带档位标识(第二重保险) ────────────────────────────────────
//
// 多档 DD 的**第一重**身份是我们持久化在 DynamicProtectionRecord.ExchangeOrderID
// 里的 orderID:每条挂单成功都按规则身份写一条记录,之后 storedTrailingOrderIDForRule
// 就能精确找回"这一档挂的是哪张单"。三个 matcher 和面板都已改成 ID-first。
//
// 但这重保险有一个共同的单点:**记录必须写成功且还在**。它会缺失于
//   - 挂单成功但持久化失败(写库报错/进程在 persist 前退出);
//   - 交易所不回 orderID(见 auto_trader_risk.go:2953 的 Bitget 分支,记录里
//     ExchangeOrderID 是空串);
//   - 人工/迁移导致记录被清。
//
// 记录一缺,这张单在交易所侧就成了"无主的 trailing 单",只能靠
// qty/activation/callback 模糊匹配去猜属于哪一档 —— 而 place-at-open 之后多档
// 并存,这个猜法必然碰撞(v1.16.8 系列事故的根因),猜错的代价是撤掉一张正在生效的
// 保护单。
//
// 所以把档位序号编进 clOrdId/algoClOrdId(交易所会在挂单列表和成交里回显),让
// **交易所侧自己就能说出这张单属于哪一档**。记录在时以记录为准(更精确,带参数);
// 记录不在时用它兜底,把"猜"降级成"读"。
//
// 序号取"按 MinProfitPct 升序在配置规则里的位次",不取三元组本身:32 字符的
// clientID 装不下三个浮点数,而位次 1..9 一个字符就够。位次会随配置改动而变,
// 因此它只用于**兜底识别**,永远不用于判定档位语义 —— 语义仍由 fingerprint 三元组
// (MinProfitPct/MaxDrawdownPct/CloseRatioPct)定义。

// drawdownTierTagIndex 返回 rule 在本策略配置的 DD 规则列表里按 MinProfitPct 升序的
// 位次(1-based),找不到或超出 1..9 时返回 0(调用方退化为不带档位标识)。
//
// 排序键与 getDrawdownArmRules* / drawdown_order_reclaim.go 一致(MinProfitPct 升序),
// 所以同一档在挂单侧和认领侧算出同一个位次。MinProfitPct 相同的两档用
// MaxDrawdownPct、再用 CloseRatioPct 做次级键定序,保证顺序稳定(map 无序会让位次在
// 两次调用间漂移,那样标识就成了噪音)。
func (at *AutoTrader) drawdownTierTagIndex(rule store.DrawdownTakeProfitRule) int {
	if at.config.StrategyConfig == nil {
		return 0
	}
	rules := at.config.StrategyConfig.Protection.DrawdownTakeProfit.Rules
	if len(rules) == 0 {
		return 0
	}
	sorted := make([]store.DrawdownTakeProfitRule, len(rules))
	copy(sorted, rules)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].MinProfitPct != sorted[j].MinProfitPct {
			return sorted[i].MinProfitPct < sorted[j].MinProfitPct
		}
		if sorted[i].MaxDrawdownPct != sorted[j].MaxDrawdownPct {
			return sorted[i].MaxDrawdownPct < sorted[j].MaxDrawdownPct
		}
		return sorted[i].CloseRatioPct < sorted[j].CloseRatioPct
	})
	for i := range sorted {
		if sameDrawdownTierIdentity(sorted[i], rule) {
			if i+1 > 9 {
				return 0
			}
			return i + 1
		}
	}
	return 0
}

// sameDrawdownTierIdentity 判断两条规则是否是同一档。判据就是 fingerprint 里定义
// 档位的那三个字段 —— 与 stableDrawdownRuleFingerprint / drawdownRuleIdentity 保持
// 同一套语义,不引入第二套"什么算同一档"的定义。
func sameDrawdownTierIdentity(a, b store.DrawdownTakeProfitRule) bool {
	return a.MinProfitPct == b.MinProfitPct &&
		a.MaxDrawdownPct == b.MaxDrawdownPct &&
		a.CloseRatioPct == b.CloseRatioPct
}

// drawdownTrailingReasonTag 生成挂 DD trailing 单时传给适配器的 reason:能定出档位
// 序号时是 "native_trailing#N",否则退回裸的 "native_trailing"。
//
// 注意 store.normalizeReason 会剥掉 "#N",所以所有既有的机制比较
// (cancelAlgoOrdersByReason 的 wantReason、归因解码、CodeForReason)行为不变 ——
// 只有 EncodeReasonClientID 会多编一段 "T<N>"。
func (at *AutoTrader) drawdownTrailingReasonTag(rule store.DrawdownTakeProfitRule) string {
	return store.ReasonWithTier("native_trailing", at.drawdownTierTagIndex(rule))
}
