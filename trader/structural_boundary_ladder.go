package trader

import (
	"fmt"
	"strings"

	"nofx/logger"
	"nofx/market"
	"nofx/store"
)

// 结构位边界的梯队解析。
//
// 为什么需要这个文件:原先 computeStructuralBoundary 在函数内联重写了一套裸 fractal
// 扫描,而 market 包里 DetectStructuralLevels 早就在算三类结构位(swing_point /
// volume_cluster / fibonacci),各自带 Strength/RecencyScore/VolumeScore/Confidence。
// 内联那套一个都没用,后果是:
//
//  1. 对 LONG 只扫「入场下方的 swing 低点」。被突破后转为支撑的**前高**在结构上
//     根本不在搜索范围内 —— 而那是最常见的一类结构位。
//  2. 成交密集区(volume_cluster)完全没参与。
//  3. 找不到 fractal 时直接退到「窗口内最近的 bar 极值」,一个未被市场验证过的
//     随机低点,和真正的结构位不是一个量级。
//
// 线上后果(ZECUSDT LONG 2026-08-03,仓位 1913):边界解析返回空 → ladder 规则的
// structural 单位退到 fallback_atr_mul=3.2 → 止损站在 -3.2%,而 1.5×ATR 那道
// 收盘确认层(约 -1.55%)从未生成。一段缓慢下跌本该在 -1.55% 被拦住,实际跑到
// -3.2% 才平,单笔 -5.68。
//
// 梯队原则:**按「市场是否真的在这个价位上做过事」排序**,上层有货就不看下层。
// 层数刻意控制在 4 层 —— 每多一层就多一个把止损推到奇怪位置的机会。
//
//	T1 swing_pivot    最强。fractal 枢轴:市场在这里真的转过向,且左右各 k 根确认。
//	T2 order_block    次强。冲击性突破的起点,市场用力防守过的位置。
//	T3 clustered      中。多次触碰的 swing 聚类 + 成交密集区。**前高前低落在这层**
//	                  —— DetectStructuralLevels 会把「已被突破、现在位于价格下方」
//	                  的前高自动重分类为 support(market/structure.go:60-62)。
//	T4 bar_extreme    最弱,兜底。保护侧最近的 bar 极值,永远有值。
//
// 刻意排除 fibonacci:它是从波段推导出来的价位,不是市场实际防守过的价位。作为
// AI 上下文和面板展示有价值,作为止损锚点则会凭空多出一堆候选。
type structuralTier int

const (
	tierSwingPivot structuralTier = iota + 1
	tierOrderBlock
	tierClustered
	tierBarExtreme
)

func (t structuralTier) String() string {
	switch t {
	case tierSwingPivot:
		return "T1_swing_pivot"
	case tierOrderBlock:
		return "T2_order_block"
	case tierClustered:
		return "T3_clustered"
	case tierBarExtreme:
		return "T4_bar_extreme"
	}
	return "unknown"
}

// structuralBoundaryPick 是梯队选出的结果 + 它来自哪一层,便于日志与落库自解释。
type structuralBoundaryPick struct {
	Price      float64
	Tier       structuralTier
	Source     string  // 具体来源,如 "swing_point" / "volume_cluster"
	Confidence float64 // 0-100,仅 T3 有;其余为 0
	Strength   int     // 触碰次数,仅 T3 有
}

// clusteredMinStrength 是 T3 采纳一个聚类位所需的最小触碰次数。
//
// 为什么是 2:Strength 是触碰次数(上限 5)。1 次触碰意味着「价格来过一次」,那和
// T4 的 bar 极值没有区别 —— 把它放进 T3 只会让本该退到 T4 的情况伪装成「找到结构
// 位了」。要求 >=2 才算「市场在这里反应过不止一次」,这是 T3 相对 T4 的全部意义。
const clusteredMinStrength = 2

// clusteredMinConfidence 是 T3 的复合置信度门槛。
//
// computeCompositeConfidence = touch*25 + volume*30 + recency*25 + multiTF*20。
// 30 这个值刻意偏低:它的作用是滤掉「两次触碰、零成交量特征、很久以前」这类
// 名义结构位,而不是只留高分位。门槛调高的代价是更多仓位退到 T4 的随机 bar 极值,
// 那比一个中等质量的真结构位更糟。
const clusteredMinConfidence = 30.0

// selectStructuralBoundary 跑完整梯队,返回第一个命中的层。
//
// window 是**已收盘**的回看窗(调用方已丢掉正在形成的那根),bars 是含最后一根的
// 完整序列(order block 检测需要更长的历史)。atr 用于 T2 的内部尺度。
//
// entryPrice 是锚点:LONG 只接受下方的位,SHORT 只接受上方的位。返回 ok=false 仅在
// 连 T4 都找不到任何保护侧价位时发生(窗口全在入场价的错误一侧)。
func selectStructuralBoundary(
	window []market.Kline,
	bars []market.Kline,
	entryPrice, atr float64,
	isLong bool,
	pivotStrength int,
	timeframe string,
	preferProven bool,
	obFinder func([]market.Kline, float64, bool, string) (float64, bool),
) (structuralBoundaryPick, bool) {
	// T1: fractal 枢轴。保持原有语义 —— 取**最近**的那个,不是窗口极值,
	// 否则一个远处的大级别尖峰会把止损推到 backstop。
	if p, ok := nearestFractalPivot(window, entryPrice, isLong, pivotStrength); ok {
		return structuralBoundaryPick{Price: p, Tier: tierSwingPivot, Source: "swing_point"}, true
	}

	// T2: order block。已有实现(nearestProvenBoundary),原先被 PreferProvenLevels
	// 开关挡着、且只作为「收紧器」用。这里把它提为独立层级:T1 空手时它就是最强候选。
	// 注意仍然尊重 preferProven —— 关掉时这层跳过,行为与改造前一致。
	if preferProven && obFinder != nil {
		if p, ok := obFinder(bars, entryPrice, isLong, timeframe); ok && p > 0 {
			return structuralBoundaryPick{Price: p, Tier: tierOrderBlock, Source: "order_block"}, true
		}
	}

	// T3: 聚类结构位 —— 前高前低 + 成交密集区都在这层。
	//
	// DetectStructuralLevels 用**整段** bars(不只回看窗),因为触碰次数和 recency
	// 需要更长历史才有意义;它内部按 currentPrice 判定 support/resistance,这里传
	// entryPrice 作为 currentPrice,于是「已被突破、位于入场价下方的前高」会被标成
	// support —— 正是我们要的那一类。
	if p, ok := nearestClusteredLevel(bars, entryPrice, isLong, timeframe); ok {
		return p, true
	}

	// T4: 兜底。保护侧最近的 bar 极值。
	if p, ok := nearestBarExtreme(window, entryPrice, isLong); ok {
		return structuralBoundaryPick{Price: p, Tier: tierBarExtreme, Source: "bar_extreme"}, true
	}

	return structuralBoundaryPick{}, false
}

// nearestFractalPivot 是原 computeStructuralBoundary 内联 nearestSwing 的原样搬出。
// 行为刻意不变:LONG 找下方最高的 swing low,SHORT 找上方最低的 swing high。
func nearestFractalPivot(window []market.Kline, entryPrice float64, isLong bool, k int) (float64, bool) {
	if k < 1 {
		k = 1
	}
	best := 0.0
	found := false
	for i := k; i < len(window)-k; i++ {
		if !isLong {
			h := window[i].High
			if h <= entryPrice {
				continue
			}
			isPivot := true
			for j := i - k; j <= i+k; j++ {
				if j != i && window[j].High > h {
					isPivot = false
					break
				}
			}
			if isPivot && (!found || h < best) {
				best, found = h, true
			}
		} else {
			l := window[i].Low
			if l >= entryPrice {
				continue
			}
			isPivot := true
			for j := i - k; j <= i+k; j++ {
				if j != i && window[j].Low < l {
					isPivot = false
					break
				}
			}
			if isPivot && (!found || l > best) {
				best, found = l, true
			}
		}
	}
	return best, found
}

// nearestClusteredLevel 是 T3:走 market.DetectStructuralLevels,只收
// swing_point 与 volume_cluster 两类来源,按门槛过滤后取保护侧**最近**的一档。
//
// 为什么只收这两类:DetectStructuralLevels 还会产出 fibonacci 来源的位。斐波那契
// 是从波段推导的价位,不是市场防守过的价位,作为止损锚点会凭空增加候选密度。
// Source 字段可能是组合串(如 "swing_point+volume_cluster"),所以用包含判断。
func nearestClusteredLevel(bars []market.Kline, entryPrice float64, isLong bool, timeframe string) (structuralBoundaryPick, bool) {
	if len(bars) < 10 || entryPrice <= 0 {
		return structuralBoundaryPick{}, false
	}
	levels := market.DetectStructuralLevels(bars, entryPrice, timeframe)
	if len(levels) == 0 {
		return structuralBoundaryPick{}, false
	}

	best := structuralBoundaryPick{}
	found := false
	for _, lv := range levels {
		if lv.Price <= 0 {
			continue
		}
		if !structuralSourceUsable(lv.Source) {
			continue
		}
		if lv.Strength < clusteredMinStrength {
			continue
		}
		if lv.Confidence < clusteredMinConfidence {
			continue
		}
		// 侧别判定用价格与 entry 的关系,不用 lv.Type —— Type 是相对传入的
		// currentPrice 算的,这里传的就是 entryPrice,两者等价;但显式比价更难读错。
		if isLong && lv.Price >= entryPrice {
			continue
		}
		if !isLong && lv.Price <= entryPrice {
			continue
		}
		// 取最近:LONG 要下方**最高**的,SHORT 要上方**最低**的。
		closer := !found ||
			(isLong && lv.Price > best.Price) ||
			(!isLong && lv.Price < best.Price)
		if closer {
			best = structuralBoundaryPick{
				Price:      lv.Price,
				Tier:       tierClustered,
				Source:     lv.Source,
				Confidence: lv.Confidence,
				Strength:   lv.Strength,
			}
			found = true
		}
	}
	return best, found
}

// structuralSourceUsable 判定一个 StructuralLevel.Source 是否可作止损锚点。
// 组合来源(如 "swing_point+volume_cluster")只要含一个可用来源就算可用。
func structuralSourceUsable(source string) bool {
	return strings.Contains(source, "swing_point") || strings.Contains(source, "volume_cluster")
}

// nearestBarExtreme 是 T4:原 computeStructuralBoundary 的兜底分支,原样搬出。
func nearestBarExtreme(window []market.Kline, entryPrice float64, isLong bool) (float64, bool) {
	best := 0.0
	found := false
	for i := 0; i < len(window); i++ {
		if !isLong {
			h := window[i].High
			if h > entryPrice && (!found || h < best) {
				best, found = h, true
			}
		} else {
			l := window[i].Low
			if l < entryPrice && (!found || l > best) {
				best, found = l, true
			}
		}
	}
	if !found || best <= 0 {
		return 0, false
	}
	return best, true
}

// applyBoundaryTolerance 把边界从结构位**推出**一个 ATR 缓冲,使止损坐在结构之外
// 而不是正好压在结构上。
//
// 为什么必须有:止损正好落在结构位上,等于把「价格摸到该位」和「结构失效」当成
// 同一件事。真实行情里价格常常插破结构位一两个 tick 又收回来 —— 那是流动性猎取,
// 不是结构失效。ratchet 路径早就有这个缓冲(TrailTolATR,默认 0.5),入场边界却
// 一直没有,这是不对称的。
//
// 缓冲后仍会经过 clampStructuralBoundary 的 floor/backstop 收敛,所以这里不需要
// 自己做上下限。
func applyBoundaryTolerance(boundary, atr, tolATR float64, isLong bool) float64 {
	if boundary <= 0 || atr <= 0 || tolATR <= 0 {
		return boundary
	}
	pad := tolATR * atr
	if isLong {
		return boundary - pad
	}
	return boundary + pad
}

// logBoundaryPick 让边界解析在日志里自解释。
//
// 为什么加:computeStructuralBoundary 改造前**整个函数零日志**。ZEC 那笔边界为空,
// 事后无法从日志区分是 GetKlines 失败、还是所有层都没命中、还是快照跑在冻结之前 ——
// 三种原因的修法完全不同。这条日志把「返回了什么、来自哪一层」变成可读事实。
func logBoundaryPick(symbol string, isLong bool, entryPrice, atr float64, pick structuralBoundaryPick, padded float64, tolATR float64) {
	side := "SHORT"
	if isLong {
		side = "LONG"
	}
	distATR := 0.0
	if atr > 0 {
		d := entryPrice - padded
		if d < 0 {
			d = -d
		}
		distATR = d / atr
	}
	extra := ""
	if pick.Tier == tierClustered {
		extra = fmt.Sprintf(" strength=%d conf=%.1f", pick.Strength, pick.Confidence)
	}
	logger.Infof("  🧱 Structural boundary %s %s: tier=%s source=%s raw=%.6f +tol(%.2f×ATR)=%.6f dist=%.2f×ATR%s",
		symbol, side, pick.Tier, pick.Source, pick.Price, tolATR, padded, distATR, extra)
}

// logBoundaryMiss 记录整条梯队全空的情况 —— 这是真正需要排查的状态。
func logBoundaryMiss(symbol string, isLong bool, entryPrice float64, reason string, barCount int) {
	side := "SHORT"
	if isLong {
		side = "LONG"
	}
	logger.Warnf("  🧱 Structural boundary %s %s: NO LEVEL FOUND (%s, bars=%d entry=%.6f) — structural SL rules will fall back to fallback_atr_mul",
		symbol, side, reason, barCount, entryPrice)
}

// boundaryTolATR 读取入场边界容差。调用方应已对配置跑过 WithDefaults(),那里会把
// 未设置/非法值填为 0.25;这里再兜一层,避免直接传裸配置时静默退回「贴着结构位」。
func boundaryTolATR(ss store.StructuralSLConfig) float64 {
	if ss.EntryTolATR > 0 {
		return ss.EntryTolATR
	}
	return 0.25
}
