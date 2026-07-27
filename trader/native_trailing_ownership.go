package trader

// nativeTrailingOwnership 描述"这个仓位的原生 trailing 单,哪些是我的"。
//
// 为什么需要一个类型,而不是继续传 nativeTrailingArmed bool:
//
// 分类器原来对 trailing 的判定是
//
//	} else if nativeTrailingArmed { -> expected_dynamic_owner
//
// 即"只要这个 symbol 有任意一档 armed,交易所上所有 trailing 单都算预期的"。
// 这是"单侧只可能有一张 trailing"时代写的判据。改成开仓即挂的多档模型后,
// 一个仓位会同时躺着全平档 + 若干部分档,于是这个布尔量把**任何**多出来的
// trailing 单也一并盖章成"我的、预期的" —— 它们既不会被计为 staleBotDuplicate,
// 也就永远进不了 reconciler 的撤单路径,会一直挂到仓位平掉为止。
//
// 2026-07-27 线上实证:
//   - BN/ETHUSDT long:DB 只认领 2 张(0.040 / 0.134),交易所躺着 3 张,
//     多出来的 0.067(半仓)止损价 1922.09 紧贴止损 1922.39 —— 一旦触发会在
//     止损位提前砍掉半仓。reconciler 每轮都打 dynamicOwner=3 unexpectedTP=0,
//     即"三张都是我的",从不报警。
//   - claude/SOXLUSDT long:全平档 1.42 有两张、30% 档 0.43 有两张。两张
//     部分档同时触发会平掉 60% 而不是 30%(全平档那对靠 reduce-only 兜住,
//     部分档没有这层保护),这是真实的多平。
//
// 正确判据只能是集合而不是布尔:**该仓位的 armed 记录认领了哪些 exchange order
// ID**。在集合里 = 我的;不在 = 我挂的但已经没人认领的残留(stale duplicate)。
// 认领集合来自 claimedTrailingOrderIDsForPosition,它已经同时覆盖梯度档和开仓
// 即时单,所以这里不需要再单独拼接。
type nativeTrailingOwnership struct {
	// Armed 表示该仓位处于 native trailing armed/arming 状态。保留它是因为
	// "认领集合为空"和"没武装"必须区分开:前者可能是记录还没落盘的窗口期,
	// 此时按老语义容忍,绝不能把在场的 trailing 单当垃圾撤掉。
	Armed bool
	// Claimed 是被 armed 记录认领的 exchange order ID 集合。nil 表示调用方
	// 无法给出认领视图(测试或早期路径),此时退回 Armed 的布尔语义。
	Claimed map[string]struct{}
}

// nativeTrailingArmedOnly 构造只有布尔语义的所有权视图,用于确实拿不到认领集合
// 的调用点(以及历史测试)。它的行为与改造前完全一致。
func nativeTrailingArmedOnly(armed bool) nativeTrailingOwnership {
	return nativeTrailingOwnership{Armed: armed}
}

// nativeTrailingOwnershipForPosition 构造带认领集合的所有权视图。
// excludeRuleFP 传空:这里要的是"全部属于我的单",不排除任何一档。
func (at *AutoTrader) nativeTrailingOwnershipForPosition(symbol, side string, entryPrice float64, armed bool) nativeTrailingOwnership {
	return nativeTrailingOwnership{
		Armed:   armed,
		Claimed: at.claimedTrailingOrderIDsForPosition(symbol, side, entryPrice, ""),
	}
}

// classifyTrailing 判定一张 trailing 单的归属类别。
//
// 返回 true 表示"是我的、预期的";false 表示"不是我认领的",由调用方按
// isLikelyBotProtectionOrder 继续区分 stale_bot_duplicate 与 manual_or_foreign。
func (o nativeTrailingOwnership) classifyTrailing(orderID string) bool {
	if !o.Armed {
		return false
	}
	// 没有认领视图时退回老语义:容忍。
	if o.Claimed == nil {
		return true
	}
	// 认领集合为空,说明武装了但记录还没落盘(或记录被清过)。此时无从判断
	// 哪张是多余的,容忍在场的单 —— 撤错一张在场的保护单,后果远重于多留一张。
	if len(o.Claimed) == 0 {
		return true
	}
	// 交易所没回 order ID 时无法比对,只能容忍。
	if orderID == "" {
		return true
	}
	_, ok := o.Claimed[orderID]
	return ok
}
