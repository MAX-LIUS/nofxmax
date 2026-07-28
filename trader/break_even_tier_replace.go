package trader

import (
	"fmt"
	"math"
	"strings"

	"nofx/logger"
	"nofx/store"
	tradertypes "nofx/trader/types"
)

// 本文件实现"保本止损逐档替换"(Phase 2)。
//
// 为什么需要它:
//
// 保本价的基准是交易所实时持仓均价(applyBreakEvenStops 收到的 entryPrice 来自
// trader.GetPositions())。加减仓会让均价漂移 —— 线上 WLDUSDT SHORT
// 0.3622→0.3691→0.3697 漂了 1.9% —— 于是同一档 BE 在漂移后算出一个新价格,
// 而 matchingBreakEvenOrderID 只按价格近似匹配,新价格匹配不上旧单 → 走挂新单路径 →
// 旧单还在 → 同一档在交易所上堆成两张。四张 BE 单(0.3604/0.3594/0.3568/0.3539)
// 就是"两档 × 漂移前后两组价格"的乘积。
//
// Phase 1 把冻结 ATR 的仓位身份判据换成 cTime,止住了"ATR 被重新冻结"这一路,
// 但保本价本身仍然跟随均价 —— 这是**对的**:保本止损的语义就是"退到成本线",
// 成本线变了它就该跟着变。真正错的是"跟随时只挂新的、不撤旧的"。
//
// 所以正确修法不是把基准也冻住(那会让加仓后的保本单停在旧成本上,失去保本意义,
// 且对多空/加减仓方向并不一致地保守),而是**先撤该档的旧单,再挂该档的新单**。
//
// 撤单必须按 order id 逐档撤,绝不能按 tag 撤:BE 所有档位在 reason_codec 里的
// 机制码都是 "BE",按 tag 撤会连兄弟档一起撤掉 —— 这正是 v1.16.5「collapse 误撤
// 兄弟档」那一类事故。id 从 Phase 1 开始写入 armed 记录的 ExchangeOrderID。

// breakEvenTierReplaceTolerancePct 是"价格没有实质变化"的容差。低于这个相对差就
// 认为该档不需要替换 —— 避免均价的最后一位小数抖动引发无意义的撤挂对。
// 取 0.05%,与 entrySamePosition 的仓位身份容差同量级。
const breakEvenTierReplaceTolerancePct = 0.05

// staleBreakEvenTierOrder 描述一张"同档但价格已过期"的在场保本止损单。
type staleBreakEvenTierOrder struct {
	OrderID   string
	StopPrice float64
	Stage     string
}

// findStaleBreakEvenTierOrders 找出该仓位该档位下"记录认领的、但价格已不是目标价"的在场单。
//
// 判据三重与门,任一不满足都不撤 —— 撤错一张在场保护单的后果远重于多留一张:
//  1. 记录必须是本 trader、本仓位、break_even_stop、armed,且 fingerprint 的 stage 段等于目标档位;
//  2. 记录必须带非空 ExchangeOrderID(历史空 id 记录一律跳过,交给 reconciler 的常规路径);
//  3. 该 id 必须仍出现在 openOrders 里,且那张单确实是一张保本止损单(按 tag/类型判),
//     价格与目标价的相对差超过容差。
//
// 第 3 条是关键的安全阀:只撤"我确认此刻在场、确认是 BE、确认价格过期"的单。
func findStaleBreakEvenTierOrders(records []store.DynamicProtectionRecord, traderID, symbol, side, stage string, targetPrice float64, openOrders []tradertypes.OpenOrder) []staleBreakEvenTierOrder {
	if stage == "" || targetPrice <= 0 {
		return nil
	}
	positionSide := strings.ToUpper(side)
	live := make(map[string]tradertypes.OpenOrder, len(openOrders))
	for _, order := range openOrders {
		if order.OrderID == "" {
			continue
		}
		live[order.OrderID] = order
	}

	var out []staleBreakEvenTierOrder
	seen := make(map[string]struct{})
	for _, record := range records {
		if record.TraderID != "" && traderID != "" && record.TraderID != traderID {
			continue
		}
		if record.ProtectionType != "break_even_stop" || record.Status != "armed" {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if breakEvenStageFromFingerprint(record.RuleFingerprint) != stage {
			continue
		}
		if record.ExchangeOrderID == "" {
			continue
		}
		if _, ok := seen[record.ExchangeOrderID]; ok {
			continue
		}
		order, ok := live[record.ExchangeOrderID]
		if !ok {
			// 已经不在场(成交/已撤/交易所侧清掉了),没什么可撤的。
			continue
		}
		if !isBreakEvenTaggedOrder(order, positionSide) {
			// id 对上了但那张单不是保本止损 —— 记录与实盘不一致,宁可不动。
			continue
		}
		if order.StopPrice <= 0 {
			continue
		}
		if math.Abs(order.StopPrice-targetPrice)/targetPrice*100 <= breakEvenTierReplaceTolerancePct {
			// 价格实质未变,该档无需替换。
			continue
		}
		seen[record.ExchangeOrderID] = struct{}{}
		out = append(out, staleBreakEvenTierOrder{OrderID: record.ExchangeOrderID, StopPrice: order.StopPrice, Stage: stage})
	}
	return out
}

// replaceStaleBreakEvenTierOrders 撤掉该档过期的在场单,并把对应记录标记为 replaced。
// 返回实际撤掉的张数。撤单失败不返回错误:调用方随后仍会挂新单,多留一张旧单由
// reconciler 的常规路径兜底,比"因为撤单失败就不挂保护单"安全。
func (at *AutoTrader) replaceStaleBreakEvenTierOrders(symbol, side, stage string, targetPrice float64, openOrders []tradertypes.OpenOrder) int {
	if at == nil || at.store == nil {
		return 0
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return 0
	}
	records := make([]store.DynamicProtectionRecord, 0, len(state.Records))
	for _, record := range state.Records {
		records = append(records, record)
	}
	stale := findStaleBreakEvenTierOrders(records, at.id, symbol, side, stage, targetPrice, openOrders)
	if len(stale) == 0 {
		return 0
	}

	cancelled := 0
	for _, victim := range stale {
		if err := at.cancelBreakEvenOrderByID(symbol, victim.OrderID); err != nil {
			logger.Warnf("⚠️ BE tier %s replace: cancel old order failed (%s %s id=%s stop=%.6f): %v", stage, symbol, side, victim.OrderID, victim.StopPrice, err)
			continue
		}
		cancelled++
		logger.Infof("🔁 BE tier %s replaced: %s %s | cancelled old stop=%.6f id=%s → new stop=%.6f", stage, symbol, side, victim.StopPrice, victim.OrderID, targetPrice)
		at.markBreakEvenRecordReplaced(symbol, side, stage, victim.OrderID)
	}
	return cancelled
}

// orderIDCanceller 是"能按 order id 撤单"的可选能力(GridTrader 带这个方法,
// 基础 Trader 接口没有)。按 id 撤是逐档替换的唯一安全撤法,所以拿不到这个能力时
// 宁可不撤 —— 见 cancelBreakEvenOrderByID。
type orderIDCanceller interface {
	CancelOrder(symbol, orderID string) error
}

// cancelBreakEvenOrderByID 按 order id 撤一张保本止损单。优先走 OKX 的 algo 撤单口,
// 否则退回通用 CancelOrder。
//
// 两个口都拿不到时返回错误而**不做任何降级**:唯一的降级手段是按 tag 撤,而 BE 所有
// 档位的机制码都是 "BE",按 tag 撤必然连兄弟档一起撤掉。宁可让该档多留一张旧单
// (reduce-only,无资金风险,由 reconciler 常规路径兜底),也不撤掉一张在场的保护单。
func (at *AutoTrader) cancelBreakEvenOrderByID(symbol, orderID string) error {
	if orderID == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(strings.TrimSuffix(orderID, "_sl"), "_tp")
	if canceller, ok := at.trader.(okxProtectionOrderIDCanceller); ok {
		return canceller.CancelAlgoOrderByID(symbol, trimmed)
	}
	if canceller, ok := at.trader.(orderIDCanceller); ok {
		return canceller.CancelOrder(symbol, trimmed)
	}
	return fmt.Errorf("exchange %s does not support cancel-by-order-id; refusing to cancel by tag", at.exchange)
}

// markBreakEvenRecordReplaced 把被撤掉那张单对应的 armed 记录状态改为 replaced,
// 使它立刻退出认领集合(claimedBreakEvenOrderIDsForPosition 只收 armed)。
// 不这么做的话,被撤的 id 会继续留在认领集合里,分类器会把一个已经不存在的 id 当自己的。
func (at *AutoTrader) markBreakEvenRecordReplaced(symbol, side, stage, orderID string) {
	if at == nil || at.store == nil || orderID == "" {
		return
	}
	state, err := at.store.LoadDynamicProtectionState()
	if err != nil || state == nil {
		return
	}
	for key, record := range state.Records {
		if record.TraderID != "" && record.TraderID != at.id {
			continue
		}
		if record.ProtectionType != "break_even_stop" || record.Status != "armed" {
			continue
		}
		if !strings.EqualFold(record.Symbol, symbol) || !strings.EqualFold(record.Side, side) {
			continue
		}
		if record.ExchangeOrderID != orderID {
			continue
		}
		if stage != "" && breakEvenStageFromFingerprint(record.RuleFingerprint) != stage {
			continue
		}
		record.Status = "replaced"
		if err := at.store.SaveDynamicProtectionRecordByKey(key, record); err != nil {
			logger.Warnf("⚠️ BE tier %s replace: mark record replaced failed (%s %s id=%s): %v", stage, symbol, side, orderID, err)
		}
		return
	}
}
