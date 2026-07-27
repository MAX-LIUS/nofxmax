package trader

import "strings"

// 回撤档(trailing / managed drawdown)的**成交价**推算。
//
// 为什么需要它:一个回撤档有两个价位,含义完全不同。
//
//   - 激活价 activationPrice = entry ×(1 ± minProfit%):价格到这里,跟踪止损才
//     开始跟着峰值走。**到激活价不会成交任何东西。**
//   - 成交价 = 峰值 ×(1 ∓ callback):价格从峰值回吐 callback 之后,这一档才真的
//     市价平仓。这是钱真正落袋的价位。
//
// 保护面板按价格从近到远排一列(多头由高到低),排的是"接下来会先碰到哪一道"。
// 用激活价去排回撤档,排出来的位置是错的:一个"涨到 3ATR 才启动、回吐 1.8ATR 才
// 平"的档,实际成交在 +1.2ATR 附近,应该排在 1.1ATR 和 1.7ATR 两个阶梯止盈之间;
// 按激活价排却会被顶到 3ATR 的位置,看起来比所有阶梯都远。用户看到的顺序错乱就是
// 这个 —— 排序键选错了,不是数值算错了。
//
// 峰值取 max(激活价, 已实现峰值)(按方向):
//   - 还没到激活价:峰值最少也要走到激活价这一档才会启动,所以用激活价当下限,
//     算出的是"这一档最早可能的成交价"。
//   - 已经越过激活价:用真实峰值,成交价随峰值一起往盈利方向棘轮上移。
//
// callbackRatio 必须是**比率**(0.018 = 1.8%),不是百分数。tier JSON 里的
// callback_rate 现在全程保持比率(见 auto_trader_decision.go 里 matchCallback 的
// 注释:早先 binance/bitget 会被乘 100,前端只能靠 ">1 就当百分数" 猜,dd% < 1
// 时静默错 100 倍)。
func drawdownTierExecutionPrice(side string, activationPrice, peakPrice, callbackRatio float64) float64 {
	if activationPrice <= 0 || callbackRatio <= 0 {
		return 0
	}
	if callbackRatio >= 1 {
		// 回吐 100% 意味着回到入场价就平,再大没有意义;夹住避免算出负价。
		callbackRatio = 1
	}
	isLong := strings.EqualFold(strings.TrimSpace(side), "long")
	anchor := activationPrice
	if peakPrice > 0 {
		if isLong && peakPrice > anchor {
			anchor = peakPrice
		} else if !isLong && peakPrice < anchor {
			anchor = peakPrice
		}
	}
	if isLong {
		return anchor * (1 - callbackRatio)
	}
	return anchor * (1 + callbackRatio)
}

// callbackRatioToExchangeUnit / callbackExchangeUnitToRatio 是 callback 的单位边界。
//
// 系统内部 callback **恒为比率**(0.018 = 1.8%),只在跟交易所收发的那一刻才换成
// 该交易所的单位:binance / bitget 的 API 用百分数(传 1.8 表示 1.8%),okx 等用比率。
//
// 这个边界必须收在这两个函数里。早先是在算完 callbackRate 之后就地乘 100,于是
// tier JSON 里 "callback_rate" 的单位随交易所而变,前端只能靠 ">1 就当百分数" 猜。
// 那个猜法在 dd% < 1 时静默错 100 倍(dd=0.54% → 传 0.54 → 前端读成 54% 回撤),
// 成交价直接算飞 —— 也就是 BN 交易员面板保护档排序错乱的根因。
func callbackRatioToExchangeUnit(exchange string, ratio float64) float64 {
	if callbackExchangeUsesPercent(exchange) {
		return ratio * 100.0
	}
	return ratio
}

func callbackExchangeUnitToRatio(exchange string, value float64) float64 {
	if callbackExchangeUsesPercent(exchange) {
		return value / 100.0
	}
	return value
}

func callbackExchangeUsesPercent(exchange string) bool {
	switch strings.ToLower(strings.TrimSpace(exchange)) {
	case "binance", "bitget":
		return true
	}
	return false
}

// peakPriceFromPnLPct 把"峰值盈利百分比"换回价格。峰值是按盈利记录的(见
// drawdown_peak_pnl_pct),而排序要的是价格,这里做一次换算。
func peakPriceFromPnLPct(side string, entryPrice, peakPnLPct float64) float64 {
	if entryPrice <= 0 || peakPnLPct <= 0 {
		return 0
	}
	if strings.EqualFold(strings.TrimSpace(side), "long") {
		return entryPrice * (1 + peakPnLPct/100.0)
	}
	return entryPrice * (1 - peakPnLPct/100.0)
}
