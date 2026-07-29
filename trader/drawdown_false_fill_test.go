package trader

import (
	"path/filepath"
	"testing"
	"time"

	"nofx/store"
)

// ============================================================================
// RC-1 假成交 (最早、危害最大的一环)
//
// detectNativeTrailingFills 原来纯靠"仓位缩水"推断 DD 档已成交。对 close=100% 的
// 全平档,expectedRemaining 等于整个仓位,于是**任何**其它机制(梯度止盈/保本/AI 平仓)
// 拿走一半以上仓位,都会被推断成"DD 档成交了" → 该档标 executed → isDrawdownTierExecuted
// 从此永久拒绝重挂 → 交易所侧再也没有这档 DD 单。
//
// 2026-07-28 线上实证 (GPT / SKHYNIXUSDT):
//   18:40:15 deficit=0.0180 expected=0.0350 position=0.0170 → dd1 标 executed
//   而该仓位的 4 笔平仓事件全部是 ladder_tp,DD 一笔都没成交过。
//   峰值 8.11% 越过两档 DD,交易所零委托。07-28/07-29 共 8 次,跨多个 symbol。
//
// 修复:成交判定必须以 position_close_events 的归属为准 —— DD 只认
// native_trailing / managed_drawdown;非 DD 平仓量先从 deficit 里扣除,且
// 记入某档的量永不超过 DD 机制实际平掉的量。
// ============================================================================

// falseFillFixture 造一个带真库的 AutoTrader,并插入一个 OPEN 仓位。
func falseFillFixture(t *testing.T, symbol, side string, entryQty, nowQty float64) (*AutoTrader, int64) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "false-fill.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{
		id:                 "trader-ff",
		exchangeID:         "exch-ff",
		store:              st,
		exchange:           "okx",
		drawdownTierAllocs: make(map[string][]store.DrawdownTierAllocation),
	}
	pos := &store.TraderPosition{
		TraderID:      at.id,
		Symbol:        symbol,
		Side:          "SHORT",
		EntryQuantity: entryQty,
		Quantity:      nowQty,
		EntryPrice:    1000,
		EntryTime:     time.Now().UnixMilli(),
		Status:        "OPEN",
	}
	if side == "long" {
		pos.Side = "LONG"
	}
	if err := at.store.Position().Create(pos); err != nil {
		t.Fatalf("插入持仓: %v", err)
	}
	return at, int64(pos.ID)
}

func addCloseEvent(t *testing.T, at *AutoTrader, posID int64, symbol, side, reason string, qty float64) {
	t.Helper()
	if err := at.store.PositionClose().Create(&store.PositionCloseEvent{
		PositionID:      posID,
		TraderID:        at.id,
		ExchangeID:      at.exchangeID,
		Symbol:          symbol,
		Side:            side,
		CloseReason:     reason,
		ExecutionSource: reason,
		CloseQuantity:   qty,
		EventTime:       time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("插入平仓事件(%s %.4f): %v", reason, qty, err)
	}
}

// 决定性回归:仓位被 ladder_tp 砍掉一半以上,DD 档绝不能被标成 executed。
// 这是 SKHYNIX 那 8 次假成交的直接复现。
func TestDetectFills_LadderTPShrinkMustNotForgeDrawdownFill(t *testing.T) {
	const (
		symbol   = "SKHYNIXUSDT"
		side     = "short"
		entryQty = 0.035
		nowQty   = 0.017
	)
	at, posID := falseFillFixture(t, symbol, side, entryQty, nowQty)

	// 全部缩水都来自 ladder_tp —— DD 一笔都没有(线上真实形态)。
	addCloseEvent(t, at, posID, symbol, "SHORT", "ladder_tp", 0.009)
	addCloseEvent(t, at, posID, symbol, "SHORT", "ladder_tp", 0.009)

	// 全平档:Quantity = 整个仓位,这是被误判的那一档。
	at.setDrawdownTierAllocs(symbol, side, []store.DrawdownTierAllocation{
		{StageName: "dd1", CloseRatioPct: 100, Quantity: entryQty, Status: "tracking"},
	})

	at.detectNativeTrailingFills(symbol, side, nowQty)

	allocs := at.getDrawdownTierAllocs(symbol, side)
	if len(allocs) != 1 {
		t.Fatalf("分配表应保持 1 档,实得 %d", len(allocs))
	}
	if allocs[0].Status == "executed" {
		t.Fatalf("ladder_tp 造成的缩水不得被判成 DD 成交(这会让该档永久不再重挂),实得 status=%q", allocs[0].Status)
	}
	if allocs[0].Status != "tracking" {
		t.Fatalf("该档应保持 tracking,实得 %q", allocs[0].Status)
	}
}

// 正向:确实有 DD 归属的平仓时,该档必须被正确标记 executed —— 防止修复过头把
// 真成交也一并忽略,那会导致同一档被反复重挂。
func TestDetectFills_RealDrawdownFillIsStillDetected(t *testing.T) {
	const (
		symbol   = "WLDUSDT"
		side     = "short"
		entryQty = 10.0
		nowQty   = 0.0
	)
	at, posID := falseFillFixture(t, symbol, side, entryQty, nowQty)
	addCloseEvent(t, at, posID, symbol, "SHORT", "native_trailing", 10.0)

	at.setDrawdownTierAllocs(symbol, side, []store.DrawdownTierAllocation{
		{StageName: "dd1", CloseRatioPct: 100, Quantity: entryQty, Status: "tracking"},
	})

	at.detectNativeTrailingFills(symbol, side, nowQty)

	allocs := at.getDrawdownTierAllocs(symbol, side)
	if allocs[0].Status != "executed" {
		t.Fatalf("真实 native_trailing 成交必须被识别为 executed,实得 %q", allocs[0].Status)
	}
}

// managed_drawdown 及其带后缀的变体同样是 DD 归属(代码侧执行的回撤止盈)。
func TestDetectFills_ManagedDrawdownCountsAsDrawdown(t *testing.T) {
	const (
		symbol   = "HYPEUSDT"
		side     = "long"
		entryQty = 4.0
		nowQty   = 0.0
	)
	at, posID := falseFillFixture(t, symbol, side, entryQty, nowQty)
	addCloseEvent(t, at, posID, symbol, "LONG", "managed_drawdown_partial", 4.0)

	at.setDrawdownTierAllocs(symbol, side, []store.DrawdownTierAllocation{
		{StageName: "dd1", CloseRatioPct: 100, Quantity: entryQty, Status: "tracking"},
	})

	at.detectNativeTrailingFills(symbol, side, nowQty)

	if got := at.getDrawdownTierAllocs(symbol, side)[0].Status; got != "executed" {
		t.Fatalf("managed_drawdown_* 应算 DD 归属,实得 %q", got)
	}
}

// 混合场景:DD 只成交了一小部分,其余是 ladder_tp。记入的量不得超过 DD 实际平掉的量,
// 因此那个大档不能被"借"非 DD 的量凑够 50% 而误判成交。
func TestDetectFills_MixedClosesCapDeficitAtDrawdownQty(t *testing.T) {
	const (
		symbol   = "SPCXUSDT"
		side     = "short"
		entryQty = 1.651
		nowQty   = 1.02
	)
	at, posID := falseFillFixture(t, symbol, side, entryQty, nowQty)
	// 绝大部分缩水来自 ladder_tp,DD 只碰了极小一点。
	addCloseEvent(t, at, posID, symbol, "SHORT", "ladder_tp", 0.60)
	addCloseEvent(t, at, posID, symbol, "SHORT", "native_trailing", 0.031)

	at.setDrawdownTierAllocs(symbol, side, []store.DrawdownTierAllocation{
		{StageName: "dd_full", CloseRatioPct: 100, Quantity: entryQty, Status: "tracking"},
	})

	at.detectNativeTrailingFills(symbol, side, nowQty)

	if got := at.getDrawdownTierAllocs(symbol, side)[0].Status; got == "executed" {
		t.Fatalf("DD 仅成交 0.031,远不足该档 %.3f 的一半,不得判 executed(实得 %q)", entryQty, got)
	}
}

// 破坏性:拿不到归属视图(无仓位记录/查询失败)时必须保守 —— 保持 armed,绝不猜成交。
// 猜错的代价是永久失去这一档保护。
func TestDetectFills_NoAttributionKeepsTiersArmed(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "no-attr.db"))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	at := &AutoTrader{
		id: "trader-na", exchangeID: "exch-na", store: st, exchange: "okx",
		drawdownTierAllocs: make(map[string][]store.DrawdownTierAllocation),
	}
	// 故意不插入任何仓位记录 → drawdownAttributedCloseQty 报 ok=false。
	at.setDrawdownTierAllocs("NOPOSUSDT", "short", []store.DrawdownTierAllocation{
		{StageName: "dd1", CloseRatioPct: 100, Quantity: 10, Status: "tracking"},
	})

	at.detectNativeTrailingFills("NOPOSUSDT", "short", 0.0)

	if got := at.getDrawdownTierAllocs("NOPOSUSDT", "short")[0].Status; got != "tracking" {
		t.Fatalf("无归属视图时必须保持 tracking(保守),实得 %q", got)
	}
}

// isDrawdownAttribution 的白名单边界:只有 DD 机制算 DD。任何非 DD 的平仓原因被
// 误认成 DD,就会重新打开假成交这个洞。
func TestIsDrawdownAttribution_Boundaries(t *testing.T) {
	dd := []string{"native_trailing", "managed_drawdown", "managed_drawdown_partial", "MANAGED_DRAWDOWN", " native_trailing "}
	for _, s := range dd {
		if !isDrawdownAttribution(s) {
			t.Fatalf("%q 应算 DD 归属", s)
		}
	}
	notDD := []string{
		"ladder_tp", "break_even_stop", "structural_sl", "full_tp", "full_sl",
		"ai_close", "max_hold", "giveback_guard_breadth", "manual_close",
		"ladder_sl", "legacy_unknown", "", "trailing", "drawdown",
	}
	for _, s := range notDD {
		if isDrawdownAttribution(s) {
			t.Fatalf("%q 不得算 DD 归属(会重新打开假成交的洞)", s)
		}
	}
}
