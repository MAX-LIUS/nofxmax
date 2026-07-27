package trader

import (
	"math"
	"testing"
	"time"

	"nofx/store"
)

// 线上实测(2026-07-27 BN/CLUSDT short):分配表 1.1/0.33,交易所躺着 1.4/0.4。
//
// 成因链:分配表只存内存(drawdownTierAllocs),重启后由 auto_trader_risk.go
// 的 lazy-init 兜底重算,而它传的是**当前**持仓量。仓位被某一档部分止盈砍过
// (1.4 → 1.1)再重启,分配表就按 1.1 重算(30% = 0.33),而交易所上的单是
// 开仓时按 1.4 挂的(30% = 0.42)。arm 路径的数量匹配已按 EntryQuantity 锚定,
// 两边口径不一致 → 匹配不上 → 重挂 → 重复单。
func TestTierAllocAnchoredToEntryQuantityAfterPartialFill(t *testing.T) {
	const (
		symbol   = "CLUSDT"
		side     = "short"
		entry    = 83.37
		entryQty = 1.4
		nowQty   = 1.1 // 被部分止盈砍过之后的剩余量
	)
	at := atrTierFixture(t, symbol, side, entry, 1.0)

	if err := at.store.Position().Create(&store.TraderPosition{
		TraderID:      at.id,
		Symbol:        symbol,
		Side:          "SHORT",
		EntryQuantity: entryQty,
		Quantity:      nowQty,
		EntryPrice:    entry,
		EntryTime:     time.Now().UnixMilli(),
		Status:        "OPEN",
	}); err != nil {
		t.Fatalf("插入持仓: %v", err)
	}

	// 重启后的 lazy-init:调用方给的是当前量 1.1。
	at.initDrawdownTiersFromResolvedRules(symbol, side, nowQty, entry, ct30ATRRules())

	allocs := at.getDrawdownTierAllocs(symbol, side)
	if len(allocs) == 0 {
		t.Fatal("应算出分配表")
	}
	var partial *store.DrawdownTierAllocation
	for i := range allocs {
		if allocs[i].CloseRatioPct > 0 && allocs[i].CloseRatioPct < 100 {
			partial = &allocs[i]
		}
	}
	if partial == nil {
		t.Fatalf("应有部分档,got %+v", allocs)
	}

	wantQty := entryQty * partial.CloseRatioPct / 100.0
	if math.Abs(partial.Quantity-wantQty) > 1e-9 {
		t.Fatalf("部分档数量应按开仓量 %.4f 锚定(want %.6f),got %.6f —— 按当前量算会是 %.6f",
			entryQty, wantQty, partial.Quantity, nowQty*partial.CloseRatioPct/100.0)
	}
	// 反向验证:确认这个断言真的能区分两种口径(否则测了个恒真)。
	if math.Abs(wantQty-nowQty*partial.CloseRatioPct/100.0) < 1e-9 {
		t.Fatal("测试构造无效:开仓量与当前量算出的目标数量相同,分辨不出锚定与否")
	}
}

// 开仓瞬间 DB 行还没落地时必须退回调用方传的量(此时它本来就等于开仓量),
// 不能因为取不到 DB 就把分配表算成 0 或跳过。
func TestTierAllocFallsBackToCallerQuantityWithoutDBRow(t *testing.T) {
	const (
		symbol = "WLDUSDT"
		side   = "long"
		entry  = 0.3562
		qty    = 380.0
	)
	at := atrTierFixture(t, symbol, side, entry, 0.0049866)

	if got := at.entryQuantityAnchor(symbol, side, qty); got != qty {
		t.Fatalf("无 DB 行时应退回调用方数量 %.4f,got %.4f", qty, got)
	}

	at.initDrawdownTiersFromResolvedRules(symbol, side, qty, entry, ct30ATRRules())
	allocs := at.getDrawdownTierAllocs(symbol, side)
	if len(allocs) == 0 {
		t.Fatal("无 DB 行时仍应算出分配表")
	}
	for _, a := range allocs {
		want := qty * a.CloseRatioPct / 100.0
		if math.Abs(a.Quantity-want) > 1e-9 {
			t.Fatalf("档 %s 数量应为 %.6f,got %.6f", a.StageName, want, a.Quantity)
		}
	}
}
