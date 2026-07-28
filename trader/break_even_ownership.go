package trader

import (
	"strings"

	"nofx/store"
)

// breakEvenOwnership 描述"这个仓位的保本止损单,哪些是我的、我该有几张"。
//
// 为什么需要一个类型,而不是继续传 breakEvenArmed bool:
//
// 分类器原来对保本止损的判定是一个**一次性布尔额度**:
//
//	} else if breakEvenArmed { -> expected_dynamic_owner
//	// 上层随后 breakEvenArmed = false,额度用光
//
// 即"整个仓位最多只有一张保本止损是预期的"。这是"BE 只有一档"时代写的判据。
// 配成两档(BE1 0.5×ATR / BE2 1.5×ATR)之后,第二张永远吃不到额度,被
// isLikelyBotProtectionOrder 认成 stale_bot_duplicate → 进撤单路径 → 下一轮 BE
// 监控发现该档缺单又挂回来 → 再撤。两档互相驱逐,形成固定周期的挂撤循环。
//
// 2026-07-24 线上实证(claude/WLDUSDT SHORT,开仓 0.3622):
//   - 21:51~21:58 挂出 4 条 break_even_stop(0.3604/0.3594/0.3568/0.3539),
//     这是冻结 ATR 失效叠加档位额度不足的合成结果。
//   - 07-26 12:29~14:54 更清楚:qty 恒为 95、价格在 0.3594 与 0.3539 之间
//     **严格交替**,每 3 分钟一轮,连续 2.5 小时约 50 张,状态全是 CANCELED。
//     两个价正是同一组 ATR 下的 BE1/BE2 —— 一档挂上、另一档被判多余撤掉,
//     下一轮反过来。这不是"多挂了几张",是一个稳定的挂撤循环。
//
// 目前线上没有再复现,靠的是 reconciler 那条"止损覆盖已满足就容忍多余止损单"
// 的分支(protection_reconciler.go:318)把它盖住了 —— 而那条分支带条件
// (unexpectedTP==0 && staleBot<=5 && !missingSL),条件一破循环就回来。所以它是
// 休眠的,不是修好的。
//
// 正确判据和 trailing 一样只能是集合:**该仓位的 armed BE 记录认领了哪些
// exchange order ID**。在集合里 = 我的;不在 = 无人认领的残留。
// 见 nativeTrailingOwnership —— 这里刻意与它同构。
type breakEvenOwnership struct {
	// Armed 表示该仓位处于 BE armed 状态。保留它是因为"认领集合为空"和"没武装"
	// 必须区分:前者可能是记录还没落盘的窗口期,此时按老语义容忍,绝不能把在场的
	// 保本止损当垃圾撤掉。owner 判定(StopOwner="breakeven")只看这一位。
	Armed bool
	// Claimed 是被 armed BE 记录认领的 exchange order ID 集合。nil/空表示调用方拿不到
	// 认领视图(测试/早期路径/历史空 id 记录),此时退回 Tiers 额度语义。
	Claimed map[string]struct{}
	// Tiers 是"该有几张保本止损"的额度,用于认领集合拿不到时的兜底(历史记录没落
	// order id、交易所不回 id)。<=0 视为 1,即改造前的行为。
	Tiers int
}

// breakEvenArmedOnly 构造只有布尔语义的所有权视图,用于确实拿不到认领集合的调用点
// (以及历史测试)。行为与改造前完全一致:额度恰好一张。
func breakEvenArmedOnly(armed bool) breakEvenOwnership {
	return breakEvenOwnership{Armed: armed, Tiers: 1}
}

// breakEvenOwnershipForPosition 构造带认领集合的所有权视图。
func (at *AutoTrader) breakEvenOwnershipForPosition(symbol, side string, armed bool) breakEvenOwnership {
	claimed, tiers := at.claimedBreakEvenOrderIDsForPosition(symbol, side)
	return breakEvenOwnership{Armed: armed, Claimed: claimed, Tiers: tiers}
}

// currentPositionEntryForBreakEven 取当前在场仓位的开仓均价,用于 cTime 拿不到时的
// 仓位身份兜底。读不到返回 0(= 无从判断,调用方按放行处理)。
func (at *AutoTrader) currentPositionEntryForBreakEven(symbol, side string) float64 {
	if at.trader == nil {
		return 0
	}
	positions, err := at.trader.GetPositions()
	if err != nil {
		return 0
	}
	for _, pos := range positions {
		ps, _ := pos["symbol"].(string)
		pd, _ := pos["side"].(string)
		if !strings.EqualFold(ps, symbol) || !strings.EqualFold(pd, side) {
			continue
		}
		entry, _ := pos["entryPrice"].(float64)
		if entry > 0 {
			return entry
		}
	}
	return 0
}

// claimedBreakEvenOrderIDsForPosition 返回该仓位 armed BE 记录认领的 order id 集合,
// 以及记录里出现过的**不同档位数**(stage 去重)。
//
// 档位数是额度兜底:历史记录的 exchange_order_id 全是空串(部署前线上全库 22 条 BE
// 记录无一例外),认领集合对它们必然为空,但"配了几档"这个信息记录里有 —— fingerprint
// 第 4 段就是 stage。于是即使一张都认领不到,额度也不再固定为 1。
func (at *AutoTrader) claimedBreakEvenOrderIDsForPosition(symbol, side string) (map[string]struct{}, int) {
	claimed := make(map[string]struct{})
	stages := make(map[string]struct{})
	if at.store == nil {
		return claimed, 0
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return claimed, 0
	}
	curCTime := at.currentPositionCreatedTime(symbol, side)
	curEntry := at.currentPositionEntryForBreakEven(symbol, side)
	for _, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if record.ProtectionType != "break_even_stop" {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		// 仓位身份核对:同一 symbol/side 上,上一个已平仓位留下的 armed 记录不能算进
		// 本仓位的认领集合。以前 BE 记录的 order id 全是空串,认领集合恒为空,这个漏洞
		// 到不了;开始写入真实 id 之后就到得了 —— 一旦把已平仓位的 id 认成自己的,
		// 在场的新单反倒落在集合外被判 stale 重复单,挂撤循环换条路回来。
		//
		// 判据与 getArmedDrawdownRecordsForPosition 一致:优先 cTime 精确比对,
		// 两边都拿不到 cTime 时(Binance 不回 cTime、历史记录没有)退回开仓价容差。
		// 两者都无从判断时放行 —— 宁可多认一张,不可错撤在场保护单。
		if !breakEvenRecordBelongsToCurrentPosition(record, curCTime, curEntry) {
			continue
		}
		// 档位数(额度兜底)按**本仓位的全部 BE 记录**去重统计,不限 armed。
		// 理由是这个数回答的是"这个仓位该有几张保本止损",而不是"现在认领到几张"。
		// 只数 armed 会在两个窗口期算少:
		//   - 刚重启/刚部署时,一个仓位往往只剩一条 armed(旧独占逻辑把兄弟档降级成
		//     replaced 了),而交易所上两张都在场 → 额度 1 → 兄弟档被判 stale;
		//   - 同档换价重挂的瞬间,旧记录已 superseded、新记录还没落盘。
		// 数多了的方向是安全的:额度偏大只会**多容忍**一张,不会错撤在场保护单;
		// 而额度偏小会直接撤掉一张真实的保护单。真正的上限由 breakEvenQuota 的
		// "认领集合非空时改按 id 判"接管 —— 那一路是精确的。
		if stage := breakEvenStageFromFingerprint(record.RuleFingerprint); stage != "" {
			stages[stage] = struct{}{}
		}
		// 认领集合只收 armed:非 armed 记录的 id 要么已撤、要么已被替换,
		// 把它们认成自己的会让在场的新单反而落在集合外。
		if record.Status == "armed" && record.ExchangeOrderID != "" {
			claimed[record.ExchangeOrderID] = struct{}{}
		}
	}
	return claimed, len(stages)
}

// breakEvenRecordBelongsToCurrentPosition 判断一条 BE 记录是否属于当前在场的仓位。
//
// 返回 true 的三种情形:cTime 双方都有且相等;cTime 拿不到但开仓价在容差内;
// 以及**两种身份都无从判断**(当前 cTime 与开仓价都取不到,或记录里两者都为空)。
// 最后一种刻意放行:这是改造前的行为,判不了就别判,错撤一张在场保护单的代价更高。
func breakEvenRecordBelongsToCurrentPosition(record store.DynamicProtectionRecord, curCTime int64, curEntry float64) bool {
	if curCTime > 0 && record.PositionCreatedTime > 0 {
		return record.PositionCreatedTime == curCTime
	}
	if curEntry > 0 && record.PositionFingerprint != "" {
		recordEntry := recordEntryFingerprint(record.PositionFingerprint)
		currentEntry := entryPositionFingerprint(curEntry)
		if recordEntry != "" && currentEntry != "" {
			return entryPriceWithinTolerance(recordEntry, currentEntry, 0.005)
		}
	}
	return true
}

// breakEvenStageFromFingerprint 取 BE fingerprint 的档位名。
// 格式(见 applyBreakEvenStop 的持久化):entry|qty|trigger|offset|stage。
// 段数不足就返回 "" —— 绝不猜,猜错会把两档并成一档,后果比不去重严重。
func breakEvenStageFromFingerprint(fingerprint string) string {
	parts := strings.Split(fingerprint, "|")
	if len(parts) < 5 {
		return ""
	}
	return strings.TrimSpace(parts[4])
}

// breakEvenQuota 返回本轮分类时"可以盖章成预期的保本止损张数"。
// 认领集合非空时不需要额度(按 id 判),返回 0。
func (o breakEvenOwnership) breakEvenQuota() int {
	if !o.Armed {
		return 0
	}
	if len(o.Claimed) > 0 {
		return 0
	}
	if o.Tiers > 0 {
		return o.Tiers
	}
	return 1
}

// classifyBreakEvenStop 判定一张(非 trailing 的)止损单是否"我的、预期的保本止损"。
// quota 是剩余额度指针,按 id 命中时不消耗额度;走额度兜底时消耗一张。
//
// 返回 true 表示 expected_dynamic_owner;false 交由调用方按
// isLikelyBotProtectionOrder 继续区分 stale_bot_duplicate 与 manual_or_foreign。
func (o breakEvenOwnership) classifyBreakEvenStop(orderID string, quota *int) bool {
	if !o.Armed {
		return false
	}
	// 有认领视图且非空:按 id 判,精确且不受档数影响。
	if len(o.Claimed) > 0 {
		if orderID == "" {
			// 交易所没回 id,无从比对,容忍 —— 撤错一张在场的保护单后果更重。
			return true
		}
		if _, ok := o.Claimed[orderID]; ok {
			return true
		}
		// 认领集合里没有它:不盖章,交给上层判 stale/foreign。
		return false
	}
	// 没有认领视图或集合为空(记录未落盘/历史空 id):退回额度兜底。
	if quota != nil && *quota > 0 {
		*quota--
		return true
	}
	return false
}
