package okx

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrTriggerPriceAlreadyPassed 表示交易所拒单的原因是**触发价已被市价越过** ——
// 这一档不是挂不上,而是已经没有意义可挂:市价早就穿过了它。
//
// 为什么要把它单独标出来(2026-07-31 线上 BTCUSDT SHORT):加仓后 qty resize 撤掉一张
// 覆盖不足的 ladder_tp,计划重新物化 4 档 TP 时,其中一档触发价 62861.4135 已被市价
// 62860.10 越过(空头的 TP 必须低于最新价),OKX 回 code=51277。这一档随后在 2 秒内
// 以 62860.10 成交 —— 它本来就在成交,而不是"保护缺失"。
//
// 但它被当成普通挂单失败计入 tier 失败,于是整份保护计划被判失败:2 次重试全败、
// 标记不可重试、reconciler 打出 ❌ reconcile failed。真正的保护(其余 3 档 TP + SL)
// 其实都挂上了,却因为这一档把整份计划连坐。
//
// 用 errors.Is 判别,调用方据此把该档记为"跳过"而不是"失败"。
var ErrTriggerPriceAlreadyPassed = errors.New("trigger price already passed by market")

// okxTriggerPriceAlreadyPassedCodes 是 OKX 侧"触发价方向不对/已被越过"的错误码。
// 只收录语义明确的这一族,不做模糊的字符串匹配 —— 把别的拒因误判成"可跳过"
// 会让真实的挂单失败被静默吞掉。
//
//	51277 TP trigger price cannot be higher than the last price   (空头 TP 高于市价)
//	51278 TP trigger price cannot be lower than the last price    (多头 TP 低于市价)
var okxTriggerPriceAlreadyPassedCodes = map[string]struct{}{
	"51277": {},
	"51278": {},
}

func parseOKXAlgoOrderResponse(resp []byte, action string) (string, error) {
	var orders []struct {
		AlgoId string `json:"algoId"`
		SCode  string `json:"sCode"`
		SMsg   string `json:"sMsg"`
	}
	if err := json.Unmarshal(resp, &orders); err != nil {
		return "", fmt.Errorf("failed to parse OKX %s response: %w", action, err)
	}
	if len(orders) == 0 {
		return "", fmt.Errorf("OKX %s response missing order result", action)
	}
	if orders[0].SCode != "0" {
		if _, passed := okxTriggerPriceAlreadyPassedCodes[orders[0].SCode]; passed {
			return "", fmt.Errorf("OKX %s rejected: code=%s msg=%s: %w", action, orders[0].SCode, orders[0].SMsg, ErrTriggerPriceAlreadyPassed)
		}
		return "", fmt.Errorf("OKX %s rejected: code=%s msg=%s", action, orders[0].SCode, orders[0].SMsg)
	}
	return orders[0].AlgoId, nil
}
