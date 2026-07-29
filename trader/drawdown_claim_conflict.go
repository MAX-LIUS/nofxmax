package trader

import (
	"strings"

	"nofx/logger"
	"nofx/store"
)

// ── 账本核心不变量:一张活着的保护单,只能有一条 armed 记录认领它 ─────────────────
//
// 2026-07-29 线上 (BN / WLDUSDT short, entry 0.3036) 的实况:
//
//	order 2000001319445192  ← native_trailing      close=100 act=0.292060 cb=0.022805  (对)
//	                        ← native_partial_trailing close=30 act=0.291456 cb=0.0120  (错,且更新)
//	order 2000001319445212  ← native_partial_trailing close=30 act=0.285137 cb=0.018244 (对)
//	                        ← native_trailing      close=100 act=0.296010 cb=0.0150     (错,且更新)
//
// 成因:reclaim 路径用**未 ATR 解析**的规则算 activation/callback/fingerprint(已在
// drawdown_order_reclaim.go 修掉),写出的记录 min=2.5/4.0 与 arm 侧的 3.6791/5.8865
// 在 drawdownRuleIdentity 看来"不是同一档",supersedeOlderArmedRecords 的窄匹配
// (同档 + 不同 orderID + 更旧)永远碰不到它们。于是 4 条记录抢 2 张单,每 ~20s 重写一次。
//
// 危害:档位→单的解析(storedTrailingOrderIDForRule)按 UpdatedAt 取最新 —— 而错的那条
// 恰恰更新。于是 30% 那一档会把全平档的单当成自己的;它一成交,全平档可能把自己标成
// "已执行",**全平回撤保护静默消失**。
//
// 为什么收敛必须放在 reconcile 而不是启动加载:
//   - 启动时手里没有交易所数据,唯一可用的判据是 UpdatedAt,而线上恰恰是**错的更新** ——
//     按"取最新"清扫会留下错的那条,比不清扫更糟;
//   - reconcile 每轮都拿着 openOrders,交易所是唯一的事实来源。判据用"这条记录描述的
//     形状是否匹配它认领的那张真实活单",与猜测无关。
//
// 判据顺序(全部只用手里已有的数据,不发一次额外请求):
//  1. 形状匹配:记录的 ActivationPrice / CallbackRatio 是否对得上那张活单。对得上的胜。
//  2. 档位在册:记录的档位身份是否还在该仓位当前生效的规则集里。在册的胜。
//  3. 都分不出来:保留 UpdatedAt 最新的,并打 WARN —— 明确记下"这次是按时序猜的"。

// resolveConflictingTrailingClaims 收敛"一张单多条 armed 记录"的冲突。
//
// openOrders 是本轮从交易所读到的活单(reconcile 已经在手),ordered 是该仓位当前生效的
// 规范档位集(canonicalDrawdownTierRules)。返回被退役的记录条数。
func (at *AutoTrader) resolveConflictingTrailingClaims(symbol, side string, entryPrice float64, openOrders []OpenOrder, ordered []store.DrawdownTakeProfitRule) int {
	if at == nil || at.store == nil {
		return 0
	}
	records := at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0)
	if len(records) < 2 {
		return 0
	}

	byOrder := make(map[string][]store.DynamicProtectionRecord)
	for _, r := range records {
		if r.ExchangeOrderID == "" {
			continue
		}
		byOrder[r.ExchangeOrderID] = append(byOrder[r.ExchangeOrderID], r)
	}

	liveByID := make(map[string]OpenOrder, len(openOrders))
	for _, o := range openOrders {
		if o.OrderID != "" {
			liveByID[o.OrderID] = o
		}
	}

	inTierSet := make(map[string]struct{}, len(ordered))
	for _, rule := range ordered {
		inTierSet[drawdownRuleIdentity(stableDrawdownRuleFingerprint(entryPrice, rule))] = struct{}{}
	}

	retired := 0
	for orderID, claims := range byOrder {
		if len(claims) < 2 {
			continue
		}
		// 只处理**跨档**冲突。同档的陈旧分叉是 supersedeOlderArmedRecords 的时序职责,
		// 在这里插手会改变既有的自愈语义。
		identities := make(map[string]struct{}, len(claims))
		for _, c := range claims {
			identities[c.ProtectionType+"|"+drawdownRuleIdentity(c.RuleFingerprint)] = struct{}{}
		}
		if len(identities) < 2 {
			continue
		}

		keep, basis := pickAuthoritativeClaim(claims, liveByID[orderID], inTierSet)
		for _, c := range claims {
			if c.Key == keep.Key {
				continue
			}
			superseded := c
			superseded.Status = "superseded"
			if err := at.store.SaveDynamicProtectionRecord(superseded); err != nil {
				logger.Warnf("⚠️ Claim conflict: failed to supersede %s claim on order %s (%s %s): %v",
					c.ProtectionType, orderID, symbol, side, err)
				continue
			}
			retired++
			logger.Warnf("🧾 Claim conflict resolved on order %s (%s %s): retired %s close=%.1f%% act=%.6f cb=%.6f — kept %s close=%.1f%% act=%.6f cb=%.6f (basis: %s). One live order must have exactly one owner, or the partial tier's fill can mark the full-close tier executed and silently drop whole-position drawdown protection.",
				orderID, symbol, side,
				c.ProtectionType, c.CloseRatioPct, c.ActivationPrice, c.CallbackRatio,
				keep.ProtectionType, keep.CloseRatioPct, keep.ActivationPrice, keep.CallbackRatio, basis)
		}
	}
	return retired
}

// pickAuthoritativeClaim 在争抢同一张单的多条记录里挑出该被保留的那条,并返回判据名。
//
// live 为零值(那张单已经不在活单列表里)时退回后两级判据 —— 单子都没了,形状无从对比。
func pickAuthoritativeClaim(claims []store.DynamicProtectionRecord, live OpenOrder, inTierSet map[string]struct{}) (store.DynamicProtectionRecord, string) {
	// 1) 形状匹配交易所上那张真单。
	if live.OrderID != "" {
		shapeHits := make([]store.DynamicProtectionRecord, 0, len(claims))
		for _, c := range claims {
			if claimMatchesLiveOrderShape(c, live) {
				shapeHits = append(shapeHits, c)
			}
		}
		if len(shapeHits) == 1 {
			return shapeHits[0], "exchange order shape"
		}
		if len(shapeHits) > 1 {
			return newestClaim(shapeHits), "exchange order shape (tie broken by recency)"
		}
	}
	// 2) 档位是否还在当前生效的规则集里。
	if len(inTierSet) > 0 {
		tierHits := make([]store.DynamicProtectionRecord, 0, len(claims))
		for _, c := range claims {
			if _, ok := inTierSet[drawdownRuleIdentity(c.RuleFingerprint)]; ok {
				tierHits = append(tierHits, c)
			}
		}
		if len(tierHits) == 1 {
			return tierHits[0], "membership in the position's current tier set"
		}
		if len(tierHits) > 1 {
			return newestClaim(tierHits), "tier-set membership (tie broken by recency)"
		}
	}
	// 3) 分不出来:按时序,并如实说明这是猜的。
	return newestClaim(claims), "recency only — NO shape or tier-set evidence available"
}

// claimMatchesLiveOrderShape 判断一条记录描述的形状是否就是这张活单。
//
// 容差与 trailingOrderMatchesDrawdownRule 同源(activation 1%,callback 0.00025):
// 同一个"是不是这一张"的问题不能在两处得出不同答案。
//
// 已激活的单不参与形状判定:激活后 StopPrice 是移动中的跟踪价,Binance 干脆不报
// callback —— 拿它去比对武装时的计划值必然失配,那会把正确的记录判成错的。返回 false
// 让调用方退到下一级判据,而不是给出一个错误的"匹配"。
func claimMatchesLiveOrderShape(c store.DynamicProtectionRecord, live OpenOrder) bool {
	if strings.EqualFold(live.ActivationStatus, "activated") {
		return false
	}
	if c.ActivationPrice <= 0 || live.StopPrice <= 0 {
		return false
	}
	denom := absFloat(live.StopPrice)
	if absFloat(c.ActivationPrice) > denom {
		denom = absFloat(c.ActivationPrice)
	}
	if denom <= 0 {
		return false
	}
	if absFloat(live.StopPrice-c.ActivationPrice)/denom > 0.01 {
		return false
	}
	// 交易所不报 callback 时只凭 activation 判定,而不是把"没报"当成漂移。
	liveCB := live.CallbackRate
	if liveCB <= 0 && live.CallbackRatePct > 0 {
		liveCB = live.CallbackRatePct / 100.0
	}
	if liveCB > 1 {
		liveCB = liveCB / 100.0
	}
	if liveCB > 0 && c.CallbackRatio > 0 && absFloat(liveCB-c.CallbackRatio) > 0.00025 {
		return false
	}
	return true
}

// ── 账本第二条不变量:活着的 trailing 单认领的平仓比例总和不能超过 100% ─────────────
//
// 上面那条不变量管的是"一张单被多档认领"。它的对偶同样会出事,而且更隐蔽:
// **多张单被同一档(或已经不存在的档)分别认领**,加起来要平掉超过一个仓位。
//
// 2026-07-29 线上 (OKX / ETHUSDT short, entry 1890.56) 的实况 —— 4 张活的 trailing 单:
//
//	order 3784785480328314880  native_trailing         close=100  act=1842.030903  (min=2.5669 max=1.0268)
//	order 3784966212015259648  native_trailing         close=100  act=1850.119086  (min=2.1391 max=0.7701)
//	order 3784969143967977472  native_trailing         close=100  act=1850.119086  (min=2.1391 max=1.2835)  ← 当前档
//	order 3784966244730830848  native_partial_trailing close=30   act=1825.854538
//
// 合计认领 330%。成因:档位身份里含**ATR 解析后**的 min/max 百分比(见
// drawdown_rule_identity.go 的字段说明),而 ATR 会被重新冻结、maxDrawdown 倍数也会被
// 改(这里 0.9→1.0→1.5 ATR)。于是同一档在解析值变化后分叉成新身份,
// supersedeOlderArmedRecords 的"同档 + 更旧"窄匹配判它们不是同一档,旧记录永不退役。
//
// 危害不在账本,在**交易所**:旧记录还挂着 armed,就把它认领的那张旧单从
// classifyProtectionOrder 的 stale_bot_duplicate 清理路径里**屏蔽**掉了
// (classifyTrailing 认领即"预期"),于是 reconciler 每轮都报 staleTrail=0 一切正常。
// 三张全平单同时在场,谁先触发谁平掉整个仓位 —— 而它们的 callback 各不相同,最紧的
// 那张(来自被改掉的旧参数)会比当前策略意图**更早**把仓位平掉。用户看到的是
// "交易所把我的仓位平了,但价格没到我设的档位"。
//
// 不变量的正确表述不是"比例总和 ≤ 100%" —— 100%(全平档)+ 30%(部分锁盈档)= 130%
// 正是**正常**配置,先到的部分档只平 30%,全平档随后接管剩下的。把总和当上限会把
// 每一个正常的多档仓位都判成越界。真正的不变量是**基数**:
//
//	当前生效的每一档,在交易所上最多只能有一张活的 trailing 单;
//	并且不存在"活着、被账本认领、却不属于当前任何一档"的 trailing 单。
//
// 判据刻意不依赖存储的档位身份 —— 身份漂移正是根因,拿漂移的东西当判据会重复同一个错。
// 只用两个事实来源:交易所上真实在场的单,和**当前**重新解析出来的规范档位集。
// 一张活单能被当前某一档认下(优先看挂单时编进 clientOrderID 的档位标识,退回形状匹配),
// 就保留;认不下的,退役其账本认领,让它回到 reconciler 既有的 stale trailing 清理路径。
// 这里**不撤单** —— 撤单权仍然只在 reconciler 手里(它会先备好替代单再撤),本函数只
// 负责摘掉那层不该有的屏蔽。
//
// 只在基数真的越界(活的被认领单数 > 当前档数)时动手。没越界说明账本即使有冗余记录
// 也还没在交易所上多挂出单,交给既有的时序/冲突路径自愈,避免在正常的 ATR 重解析窗口里
// 误摘在场保护单的屏蔽。

// resolveOverClaimedTrailingExposure 收敛"活的被认领 trailing 单数 > 当前档数"的越界。
// 返回被退役的记录条数。
func (at *AutoTrader) resolveOverClaimedTrailingExposure(symbol, side string, entryPrice float64, openOrders []OpenOrder, ordered []store.DrawdownTakeProfitRule) int {
	if at == nil || at.store == nil || len(openOrders) == 0 {
		return 0
	}
	// 拿不到当前档位集就不做判断:没有事实来源时保留现状,永远比猜着摘掉保护单的屏蔽安全。
	if len(ordered) == 0 {
		return 0
	}
	records := at.getArmedDrawdownRecordsForPosition(symbol, side, entryPrice, 0, 0)
	if len(records) < 2 {
		return 0
	}

	// 只统计**确实还在交易所上**的 trailing 单。已经消失的单不占基数,把它算进来会
	// 误判越界,进而误退役在场的记录。
	liveTrailing := make(map[string]OpenOrder, len(openOrders))
	for _, o := range openOrders {
		if o.OrderID == "" || !strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			continue
		}
		if o.PositionSide != "" && !strings.EqualFold(o.PositionSide, strings.ToUpper(side)) {
			continue
		}
		liveTrailing[o.OrderID] = o
	}
	if len(liveTrailing) <= len(ordered) {
		return 0
	}

	// 每张活单只算一次(同一张单的多档冲突是上面那条不变量的职责,已在本轮先跑过)。
	claimByOrder := make(map[string]store.DynamicProtectionRecord)
	for _, r := range records {
		if r.ExchangeOrderID == "" {
			continue
		}
		if _, live := liveTrailing[r.ExchangeOrderID]; !live {
			continue
		}
		if prev, seen := claimByOrder[r.ExchangeOrderID]; seen && r.UpdatedAt <= prev.UpdatedAt {
			continue
		}
		claimByOrder[r.ExchangeOrderID] = r
	}
	if len(claimByOrder) <= len(ordered) {
		return 0
	}

	retired := 0
	kept := 0
	for orderID, claim := range claimByOrder {
		if liveTrailingOrderMatchesAnyTier(liveTrailing[orderID], side, entryPrice, ordered) {
			kept++
			continue
		}
		superseded := claim
		superseded.Status = "superseded"
		if err := at.store.SaveDynamicProtectionRecord(superseded); err != nil {
			logger.Warnf("⚠️ Over-claimed trailing exposure: failed to supersede %s claim on order %s (%s %s): %v",
				claim.ProtectionType, orderID, symbol, side, err)
			continue
		}
		retired++
		logger.Warnf("🧾 Over-claimed trailing exposure on %s %s: %d live claimed trailing orders for %d configured tiers; retired %s close=%.1f%% act=%.6f cb=%.6f on order %s — no current tier claims this order, so its ledger claim was shielding it from stale-order cleanup. Multiple full-close trails mean whichever triggers first closes the whole position, and the tightest stale callback fires EARLIER than the configured tier intends.",
			symbol, side, len(claimByOrder), len(ordered),
			claim.ProtectionType, claim.CloseRatioPct, claim.ActivationPrice, claim.CallbackRatio, orderID)
	}
	if retired == 0 {
		// 每一张活单都对得上当前某一档,数量却仍然超过档数 —— 说明同一档被两张单认下
		// (形状在容差内难分),那是"同档陈旧分叉"的职责范围。这里绝不擅自挑一张退役:
		// 挑错会摘掉正在生效的那张单的屏蔽。只把事实喊出来。
		logger.Errorf("🚨 Over-claimed trailing exposure on %s %s: %d live claimed trailing orders for only %d configured tiers, but EVERY one of them matches a current tier — two orders are answering to the same tier within matching tolerance. Leaving all claims intact on purpose (picking the wrong one would unshield the working order); this is the same-tier fork path's job.",
			symbol, side, len(claimByOrder), len(ordered))
	}
	return retired
}

// liveTrailingOrderMatchesAnyTier 判断一张活的 trailing 单能否被**当前**任一档认下。
//
// 优先用挂单时编进 clientOrderID 的档位标识(见 drawdown_tier_tag.go):它对已激活的单
// 依然成立,而形状匹配在激活后必然失配。没有标识时(旧单/未带标识的路径)才退回形状。
func liveTrailingOrderMatchesAnyTier(order OpenOrder, side string, entryPrice float64, ordered []store.DrawdownTakeProfitRule) bool {
	if len(ordered) == 0 {
		// 拿不到当前档位集就不做判断:没有事实来源时保留现状,永远比猜着退役保护单安全。
		return true
	}
	if order.OrderID == "" {
		// 空活单(调用方没找到对应的在场单)不能当成"属于某一档"。
		return false
	}
	if order.ProtectionTier > 0 {
		return order.ProtectionTier <= len(ordered)
	}
	for _, rule := range ordered {
		if trailingOrderMatchesDrawdownRule(order, side, entryPrice, rule) {
			return true
		}
	}
	return false
}

func newestClaim(claims []store.DynamicProtectionRecord) store.DynamicProtectionRecord {
	best := claims[0]
	for _, c := range claims[1:] {
		if c.UpdatedAt > best.UpdatedAt {
			best = c
		}
	}
	return best
}
