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

// ── 为什么档位序号必须在"同一套已解析规则"里算(v1.17.2 修正) ────────────────────
//
// v1.17.1 的 drawdownTierTagIndex 拿传入的 rule 去比 at.config...DrawdownTakeProfit.Rules,
// 用的是**精确浮点相等**。这在纯百分比策略上碰巧能对上,但在 ATR 单位策略上**永远**对
// 不上:挂单路径拿到的 rule 已经过 resolveDrawdownRulesATR 解析(min_profit_pct 从
// "2.5 ATR" 变成 3.6791%),而 config 里存的还是原始倍数 2.5。于是序号恒为 0,每一档都
// 退回裸 "native_trailing" —— 整个第二重保险在 4 个实盘交易员上静默失效(它们全是
// ATR 单位)。
//
// 修法是把"档位序号"的定义从"在原始配置里的位次"改成"在**该仓位当前生效的、已解析的**
// 规则集里的位次",并让挂单侧和认领侧调同一个函数拿这个规则集。这样:
//   - ATR 策略:两侧都拿解析后的百分比,能对上;
//   - 百分比策略:解析器 no-op,行为与 v1.17.1 完全一致;
//   - AI 规则(drawdownAIRules):以前根本不在 config.Rules 里,序号必然 0;现在
//     getActiveDrawdownRulesForPosition 会先返回 AI 规则,于是 AI 档位也能带标识。
//
// 序号仍然只用于**兜底识别**,不用于判定档位语义 —— 语义仍由 fingerprint 三元组定义。

// canonicalDrawdownTierOrder 把规则按"档位序号"的规范顺序排好并返回副本。
//
// 排序键:MinProfitPct 升序,次级 MaxDrawdownPct,再次级 CloseRatioPct。次级键是必需
// 的 —— 两档 MinProfitPct 相同时,只按主键排会让位次在两次调用之间漂移(slice 来源
// 是 map 时尤甚),那样标识就成了噪音。
//
// 同时过滤掉不完整的档(三个字段任一 <=0),与 drawdown_order_reclaim /
// computeDrawdownTierAllocations 的口径一致:不完整的档不会被挂单,也就不该占位次。
func canonicalDrawdownTierOrder(rules []store.DrawdownTakeProfitRule) []store.DrawdownTakeProfitRule {
	out := make([]store.DrawdownTakeProfitRule, 0, len(rules))
	for _, r := range rules {
		r = normalizeDrawdownRule(r)
		if r.MinProfitPct > 0 && r.MaxDrawdownPct > 0 && r.CloseRatioPct > 0 {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MinProfitPct != out[j].MinProfitPct {
			return out[i].MinProfitPct < out[j].MinProfitPct
		}
		if out[i].MaxDrawdownPct != out[j].MaxDrawdownPct {
			return out[i].MaxDrawdownPct < out[j].MaxDrawdownPct
		}
		return out[i].CloseRatioPct < out[j].CloseRatioPct
	})
	return out
}

// canonicalDrawdownTierRules 返回某仓位当前生效的 DD 规则集,已 ATR 解析 + 规范排序。
//
// 这是档位序号的**唯一**基准:挂单侧(drawdownTierTagIndex)和认领侧
// (reclaimLiveDrawdownTrailingOrders / tierTaggedRuleIndex)都必须经由它,否则两侧
// 会在不同基准上算序号,标识就从"读出来的真相"退化成另一种猜。
//
// entryPrice 只作为 ATR 解析失败时的兜底分母:resolveDrawdownRulesATR 内部用的是
// frozenATRAndEntryForPosition 冻结的 anchorEntry,所以开仓均价被修正后,解析结果
// (以及档位序号)保持不变 —— 这正是序号能跨轮次稳定的前提。
func (at *AutoTrader) canonicalDrawdownTierRules(symbol, side string, entryPrice float64) []store.DrawdownTakeProfitRule {
	rules := at.getActiveDrawdownRulesForPosition(symbol, side)
	if len(rules) == 0 {
		return nil
	}
	return canonicalDrawdownTierOrder(at.resolveDrawdownRulesATR(rules, symbol, side, entryPrice))
}

// drawdownTierOrdinalIn 返回 rule 在已规范排序的 ordered 里的位次(1-based),
// 找不到或超出 1..9 时返回 0(调用方退化为不带档位标识)。纯函数,便于两侧共用。
func drawdownTierOrdinalIn(ordered []store.DrawdownTakeProfitRule, rule store.DrawdownTakeProfitRule) int {
	for i := range ordered {
		if sameDrawdownTierIdentity(ordered[i], rule) {
			if i+1 > 9 {
				return 0
			}
			return i + 1
		}
	}
	return 0
}

// drawdownTierTagIndex 返回 rule 在该仓位当前生效规则集里的档位位次(1-based),
// 定不出来时返回 0。
func (at *AutoTrader) drawdownTierTagIndex(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule) int {
	return drawdownTierOrdinalIn(at.canonicalDrawdownTierRules(symbol, side, entryPrice), rule)
}

// drawdownTierIdentityEpsilon 是档位三元组比对的绝对容差(百分点)。
//
// 为什么需要容差:两侧的百分比都由 EffectivePercent(atr/anchorEntry) 现算,虽然输入
// 相同、结果通常逐位相同,但只要中间有一次不同顺序的浮点运算(例如先乘后除 vs 先除
// 后乘),就会差 1 个 ULP —— 精确相等会因此判成"不是同一档",又回到序号恒 0 的老问题。
// 1e-9 个百分点远小于任何真实档距(实盘最近的两档差 2.2 个百分点),不会把两档并成一档。
const drawdownTierIdentityEpsilon = 1e-9

func nearlySameTierValue(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= drawdownTierIdentityEpsilon
}

// sameDrawdownTierIdentity 判断两条规则是否是同一档。判据就是 fingerprint 里定义
// 档位的那三个字段 —— 与 stableDrawdownRuleFingerprint / drawdownRuleIdentity 保持
// 同一套语义,不引入第二套"什么算同一档"的定义。
func sameDrawdownTierIdentity(a, b store.DrawdownTakeProfitRule) bool {
	return nearlySameTierValue(a.MinProfitPct, b.MinProfitPct) &&
		nearlySameTierValue(a.MaxDrawdownPct, b.MaxDrawdownPct) &&
		nearlySameTierValue(a.CloseRatioPct, b.CloseRatioPct)
}

// drawdownTrailingReasonTag 生成挂 DD trailing 单时传给适配器的 reason:能定出档位
// 序号时是 "native_trailing#N",否则退回裸的 "native_trailing"。
//
// 注意 store.normalizeReason 会剥掉 "#N",所以所有既有的机制比较
// (cancelAlgoOrdersByReason 的 wantReason、归因解码、CodeForReason)行为不变 ——
// 只有 EncodeReasonClientID 会多编一段 "T<N>"。
func (at *AutoTrader) drawdownTrailingReasonTag(symbol, side string, entryPrice float64, rule store.DrawdownTakeProfitRule) string {
	return store.ReasonWithTier("native_trailing", at.drawdownTierTagIndex(symbol, side, entryPrice, rule))
}
