package trader

import (
	"strings"
)

// ── 为什么需要"规则身份"这一层 ────────────────────────────────────────────────
//
// drawdownRuleFingerprint 的第 0 个字段是开仓均价。这让 fingerprint 同时承担了两件
// 互相冲突的职责:
//
//  1. 它是"这一档梯度"的身份证 —— 撮合器靠它找回自己挂的那张单
//     (storedTrailingOrderIDForRule / armedFingerprints / nativeTrailingArmTime)。
//  2. 它记录了挂单当时用的开仓价 —— 而开仓价又是 ATR→百分比换算的除数。
//
// 问题出在开仓均价会被修正:place-at-open 用的是下单响应里的成交价(甚至是下单前
// 快照价),运行时监控用的是交易所同步回来的持仓均价;加仓、部分平仓后的重新计算、
// 交易所侧的均价刷新,都会让这个值漂移零点几个百分点。
//
// 一旦漂移,同一档梯度的 fingerprint 就分叉成两个字符串:
//   - storedTrailingOrderIDForRule 用新 fingerprint 查不到自己挂的单 → 判定"缺单"
//   - armedFingerprints 里只有旧 fingerprint → 判定"没武装过"
//   - nativeTrailingArmTime 用新 fingerprint 查不到 → 300s 冷却也失效
//   → 于是同一档梯度在交易所上挂出第二张单。
//     (2026-07-27 线上:SOXLUSDT 两档策略挂出 4 张单 / 4 条 armed 记录,
//     place-at-open 用计划价 149.09 武装,运行时用真实成交均价 149.07246479 重新武装。)
//
// 修法:把"身份"和"当时的开仓价"拆开。**比较与做 map key 时一律用身份**
// (drawdownRuleIdentity,把第 0 个字段抹平),**写库时仍然写带开仓价的原串**
// (RuleFingerprint 字段语义不变)。这样:
//   - 线上已有记录不需要迁移就能被新代码认领(身份从旧串里现算出来);
//   - 回滚到旧版本时旧代码读到的仍是它熟悉的带价格的串;
//   - auto_trader_risk.go:940 / :1281 那两处按下标解析 parts[2](MinProfitPct)的
//     代码不受影响 —— 身份串保持相同的字段数和分隔符,只是第 0 段换成定值。
//
// 这不是"哪漏补哪":分叉的根因只有一个(身份里混进了会变的量),所以只在这一个地方
// 定义身份,再把所有比较/做 key 的地方统一换过来。
//
// 位置身份(哪一个仓位)不受影响 —— 那一层早就改用交易所 cTime 了
// (refreshDrawdownExecutionFingerprint),本文件只处理"哪一档规则"。

// drawdownRuleIdentityEntryPlaceholder 占据身份串里原本放开仓均价的位置。
// 保留字段本身(而不是删掉)是为了让按下标解析 fingerprint 的现存代码继续成立。
const drawdownRuleIdentityEntryPlaceholder = "*"

// drawdownRuleIdentity 把一条 rule fingerprint 归一成"与开仓价无关"的身份串。
//
// 抹平前两个字段:
//   - 第 0 段 = 开仓均价:会被修正,见文件头。
//   - 第 1 段 = 数量:部分平仓会变。stableDrawdownRuleFingerprint 本来就写 0,
//     但历史记录里可能带着真实数量,一起抹平才能让新旧记录归一到同一个身份。
//
// 其余字段(MinProfitPct / MaxDrawdownPct / CloseRatioPct / StageName / runner 语义)
// 才是真正定义"这是哪一档"的东西,原样保留。
//
// 空串返回空串:调用方对"没有 fingerprint"的处理逻辑不变。
func drawdownRuleIdentity(fingerprint string) string {
	if fingerprint == "" {
		return ""
	}
	parts := strings.Split(fingerprint, "|")
	if len(parts) < 2 {
		// 不是预期格式(例如别处写入的伪 fingerprint)。原样返回,让它只和自己相等,
		// 绝不猜测语义 —— 猜错会把两档不同的梯度并成一档,后果比不归一严重得多。
		return fingerprint
	}
	parts[0] = drawdownRuleIdentityEntryPlaceholder
	parts[1] = drawdownRuleIdentityEntryPlaceholder
	return strings.Join(parts, "|")
}

// nativeTrailingArmKey 是 300s 重复武装冷却的 key。
//
// 历史上这个 map 直接用 fingerprint 做 key,而 fingerprint 里含开仓均价 —— 于是
// "不同币种/不同方向"是靠开仓价碰巧不同来区分的。这有两个后果:
//   - 潜在串味:同一策略下两个开仓价恰好相同的仓位(同币种 long/short 尤其容易)
//     会共用一个冷却窗口,一边武装完另一边 300s 内挂不上单。
//   - 一旦身份不再含开仓价,这个偶然的区分就彻底消失了。
//
// 所以冷却 key 必须显式带上 symbol|side,再拼规则身份。
func nativeTrailingArmKey(symbol, side, ruleFingerprint string) string {
	return strings.ToLower(symbol) + "|" + strings.ToLower(side) + "|" + drawdownRuleIdentity(ruleFingerprint)
}
