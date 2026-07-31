package trader

// claimedBETier 记录本轮已经认领了某张实物止损单的保本档。
type claimedBETier struct {
	stage string
	price float64
	qty   float64
}

// beTierClaimDecision 是"本档撞上了先到档"的三种结论。
type beTierClaimDecision int

const (
	// beTierClaimIndependent:没撞上任何先到档,本档走自己的路径(挂自己的单)。
	beTierClaimIndependent beTierClaimDecision = iota
	// beTierClaimSuppress:撞上了先到档,且先到档的数量覆盖得住本档 → 抑制本档。
	beTierClaimSuppress
	// beTierClaimUndercovered:撞上了先到档,但先到档覆盖不住本档 → **不抑制**。
	// 这是真实的覆盖不足,必须留在原路径上并打出来,不能被"看起来已有单"吞掉。
	beTierClaimUndercovered
)

// findConflictingBETierClaim 判定本档(price/qty)是否应被本轮已认领的某个档位抑制。
//
// 为什么需要它(2026-07-31 线上 ETHUSDT short):ATR 解析后 BE1 与 BE2 的挂单价相对
// 间距只有 0.1503%,小于 protectionPriceTolerancePct(0.2%)。而 matchingBreakEvenOrderID
// 只按价格匹配,于是 BE2 匹配到了 BE1 那张单 —— 两档都把同一个 orderID 写成 armed,
// "一张活单只有一个主人"这条不变量被破,归属账本每 ~20s 翻转一次(实测 53 次)。
//
// 收敛规则:**先到者赢**。rules 已按 TriggerValue 升序遍历,先到的就是低档位,也正是
// 交易所上那张单被挂出时所属的档位 —— 账本因此与实物一致,而不是反过来让账本记一个
// 交易所上不存在的价格。被抑制的那一档不写记录、不挂单、不撤单,所以这个判定
// **不改变任何实物挂单行为**,只把账本从自相矛盾变成自洽。
//
// 为什么必须放在 replaceStaleBreakEvenTierOrders 上游:替换容差
// (breakEvenTierReplaceTolerancePct=0.05%)比匹配容差(0.2%)更紧,而 0.1503% 正好落在
// 两者之间 —— 若先走替换路径,BE2 会认为那张单"价格不对"从而撤单重挂,BE1 下一轮再撤回来,
// 形成挂撤循环。审计窗口内 24h 零次替换事件,说明它一直是潜伏状态,不是已发生的故障。
//
// 数量比较留 1e-12 容差:数量是量化后的事实,浮点尾差不该被读成"覆盖不住"。
// 覆盖不足**不终止扫描**:它只说明"这一条不算",后面仍可能有一档撞同价且覆盖得住。
// 提前返回会把那种情况误判成"独立成档",于是两档各挂一张同价单 —— 正是本次要修的形态。
// 全都覆盖不住时才回 undercovered,并带出**最先撞上**的那档,让日志指向先到者。
// (与抽出前的唯一差别:原来每个撞上却覆盖不住的档各打一条 WARN,现在只为最先撞上的那档
// 打一条。结论完全一致,只是不再重复喊同一件事。)
func findConflictingBETierClaim(claimed []claimedBETier, price, qty float64) (claimedBETier, beTierClaimDecision) {
	var firstUndercovered claimedBETier
	foundUndercovered := false
	for _, c := range claimed {
		if !approximatelyEqualPrice(c.price, price) {
			continue
		}
		if c.qty+1e-12 < qty {
			if !foundUndercovered {
				firstUndercovered = c
				foundUndercovered = true
			}
			continue
		}
		return c, beTierClaimSuppress
	}
	if foundUndercovered {
		return firstUndercovered, beTierClaimUndercovered
	}
	return claimedBETier{}, beTierClaimIndependent
}
