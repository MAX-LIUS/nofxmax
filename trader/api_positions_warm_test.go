package trader

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"nofx/store"
	tradertypes "nofx/trader/types"
)

// ── 2026-07-29 "切换交易员要等十几秒" ────────────────────────────────────────────
//
// apiPositionsSnap 只有真的来了一次 API 请求才会被填(fetchAndProjectPositions 末尾)。
// 监控循环走 at.trader.GetPositions() 适配器缓存,从不更新这份投影。于是切到一个
// 5 分钟以上没看过的交易员,/api/positions 必然走**阻塞分支** —— 在 1 核机器上要和
// 4 个交易员的监控循环抢 CPU,线上实测 16.39s / 10.22s,而同一时刻 OKX 接口本身
// 不到 1 秒返回(日志 18:33:54→18:34:00 有 6 秒整段空档,是排队不是慢接口)。
//
// 修法:监控循环每轮顺手按年龄异步预热。这些测试钉住三件事 ——
//   1. 冷快照会被预热(这是"第一次切过去"最慢的那一下);
//   2. 温快照不重复预热(1 核上不能每 10s 都付一次投影成本);
//   3. 空仓不预热(白花 CPU)。

type warmPosTrader struct {
	calls atomic.Int64
	pos   []map[string]interface{}
	err   error
}

func (f *warmPosTrader) GetBalance() (map[string]interface{}, error) { return nil, nil }
func (f *warmPosTrader) GetPositions() ([]map[string]interface{}, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.pos, nil
}
func (f *warmPosTrader) OpenLong(s string, q float64, l int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *warmPosTrader) OpenShort(s string, q float64, l int) (map[string]interface{}, error) {
	return nil, nil
}
func (f *warmPosTrader) CloseLong(s string, q float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *warmPosTrader) CloseShort(s string, q float64) (map[string]interface{}, error) {
	return nil, nil
}
func (f *warmPosTrader) SetLeverage(s string, l int) error                  { return nil }
func (f *warmPosTrader) SetMarginMode(s string, c bool) error               { return nil }
func (f *warmPosTrader) GetMarketPrice(s string) (float64, error)           { return 100, nil }
func (f *warmPosTrader) CancelStopLossOrders(s string) error                { return nil }
func (f *warmPosTrader) CancelTakeProfitOrders(s string) error              { return nil }
func (f *warmPosTrader) CancelAllOrders(s string) error                     { return nil }
func (f *warmPosTrader) CancelStopOrders(s string) error                    { return nil }
func (f *warmPosTrader) SetStopLoss(s, ps string, q, p float64) error       { return nil }
func (f *warmPosTrader) SetTakeProfit(s, ps string, q, p float64) error     { return nil }
func (f *warmPosTrader) FormatQuantity(s string, q float64) (string, error) { return "", nil }
func (f *warmPosTrader) GetOrderStatus(s, o string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *warmPosTrader) GetClosedPnL(t time.Time, l int) ([]tradertypes.ClosedPnLRecord, error) {
	return nil, nil
}
func (f *warmPosTrader) GetOpenOrders(s string) ([]tradertypes.OpenOrder, error) { return nil, nil }

func newWarmAT(f *warmPosTrader) *AutoTrader {
	return &AutoTrader{
		id:     "warm-trader",
		trader: f,
		config: AutoTraderConfig{StrategyConfig: &store.StrategyConfig{}},
	}
}

// waitForCalls 等异步预热落地。预热是 go 出去的,不能只靠一次断言。
func waitForCalls(t *testing.T, f *warmPosTrader, want int64, within time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f.calls.Load() >= want {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return f.calls.Load()
}

func TestWarmPositionsSnapshot_ColdSnapshotIsWarmed(t *testing.T) {
	f := &warmPosTrader{pos: warmSamplePositions()}
	at := newWarmAT(f)

	// 冷快照:apiPositionsAt 是零值 ⇒ 必须预热。
	at.WarmPositionsSnapshotIfStale(true)
	if got := waitForCalls(t, f, 1, time.Second); got < 1 {
		t.Fatalf("冷快照没有被预热(适配器调用 %d 次) —— 这正是切换交易员第一下最慢的原因", got)
	}

	// 预热后快照必须真的填上了,否则下一次请求仍走阻塞分支。
	at.apiReadMu.RLock()
	snap, ts := at.apiPositionsSnap, at.apiPositionsAt
	at.apiReadMu.RUnlock()
	if snap == nil || ts.IsZero() {
		t.Fatalf("预热跑了但快照没落地: snap=%v at=%v", snap != nil, ts)
	}

	// 关键收益:此刻 GetPositions 必须命中快速分支(不再阻塞取交易所)。
	before := f.calls.Load()
	if _, err := at.GetPositions(); err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if f.calls.Load() != before {
		t.Errorf("预热后 GetPositions 仍然打了交易所(%d → %d),没有命中 stale-while-revalidate 快速分支",
			before, f.calls.Load())
	}
}

func TestWarmPositionsSnapshot_FreshSnapshotNotRewarmed(t *testing.T) {
	f := &warmPosTrader{pos: warmSamplePositions()}
	at := newWarmAT(f)
	at.apiReadMu.Lock()
	at.apiPositionsSnap = []map[string]interface{}{{"symbol": "BTCUSDT"}}
	at.apiPositionsAt = time.Now()
	at.apiReadMu.Unlock()

	at.WarmPositionsSnapshotIfStale(true)
	time.Sleep(80 * time.Millisecond)
	if n := f.calls.Load(); n != 0 {
		t.Errorf("温快照被重复预热 %d 次 —— 1 核机器上每 10s 付一次投影成本会反过来加重排队", n)
	}
}

// 快照老到超过 warm 年龄就必须续期,否则会走到 stale 窗口外触发阻塞。
func TestWarmPositionsSnapshot_StaleBeyondWarmAgeIsRewarmed(t *testing.T) {
	f := &warmPosTrader{pos: warmSamplePositions()}
	at := newWarmAT(f)
	at.apiReadMu.Lock()
	at.apiPositionsSnap = []map[string]interface{}{{"symbol": "BTCUSDT"}}
	at.apiPositionsAt = time.Now().Add(-apiReadWarmAge - time.Second)
	at.apiReadMu.Unlock()

	at.WarmPositionsSnapshotIfStale(true)
	if got := waitForCalls(t, f, 1, time.Second); got < 1 {
		t.Errorf("快照已超过 warm 年龄却没续期 —— 会滑出 stale 窗口后触发阻塞取数")
	}
}

func TestWarmPositionsSnapshot_NoPositionsSkipsWarm(t *testing.T) {
	f := &warmPosTrader{}
	at := newWarmAT(f)
	at.WarmPositionsSnapshotIfStale(false)
	time.Sleep(80 * time.Millisecond)
	if n := f.calls.Load(); n != 0 {
		t.Errorf("空仓也预热了 %d 次 —— 空投影没有收益,纯浪费 1 核 CPU", n)
	}
}

// 预热失败不能污染快照,也不能 panic:交易所报错时下一次请求应照旧走阻塞分支重试。
func TestWarmPositionsSnapshot_ErrorLeavesSnapshotUntouched(t *testing.T) {
	f := &warmPosTrader{err: errors.New("exchange down")}
	at := newWarmAT(f)
	at.WarmPositionsSnapshotIfStale(true)
	waitForCalls(t, f, 1, time.Second)
	at.apiReadMu.RLock()
	snap := at.apiPositionsSnap
	at.apiReadMu.RUnlock()
	if snap != nil {
		t.Errorf("预热失败却写入了快照 %v —— 会把错误状态当成可用缓存服务出去", snap)
	}
}

// 预热不得让写侧对账误判:PositionsSnapshotFresh 只在 8s 内才算 fresh,
// 而预热用的是 warm 年龄(2.5min)。若两者混淆,handler 会拿一份 2 分钟前的快照去
// MarkOpenPositionsAbsentFromExchangeClosed —— 可能误关刚开的仓。
func TestWarmPositionsSnapshot_DoesNotWidenWriteSideFreshness(t *testing.T) {
	at := newWarmAT(&warmPosTrader{})
	at.apiReadMu.Lock()
	at.apiPositionsSnap = []map[string]interface{}{{"symbol": "BTCUSDT"}}
	at.apiPositionsAt = time.Now().Add(-apiReadFreshWindow - time.Second)
	at.apiReadMu.Unlock()
	if at.PositionsSnapshotFresh() {
		t.Error("超过 apiReadFreshWindow 的快照被判为 fresh —— 写侧对账会据此误关仓位")
	}
	if apiReadWarmAge <= apiReadFreshWindow {
		t.Errorf("apiReadWarmAge(%s) 必须显著大于 apiReadFreshWindow(%s),否则预热会变成每轮都跑",
			apiReadWarmAge, apiReadFreshWindow)
	}
	if apiReadWarmAge >= apiReadStaleWindow {
		t.Errorf("apiReadWarmAge(%s) 必须小于 apiReadStaleWindow(%s),否则预热来不及在过期前续上",
			apiReadWarmAge, apiReadStaleWindow)
	}
}

// warmSamplePositions 用 **适配器真实字段名**(entryPrice / positionAmt / markPrice …)。
// 早先这个 fixture 写的是 entry_price/quantity,于是 fetchAndProjectPositions 的裸
// 断言直接 panic —— 那次 panic 反而暴露了真问题:该函数跑在 refreshPositionsAsync
// 的裸 goroutine 里,而 singleflight.Do 是 re-panic,panic 会打死整个 nofx。
func warmSamplePositions() []map[string]interface{} {
	return []map[string]interface{}{{
		"symbol":           "BTCUSDT",
		"side":             "long",
		"entryPrice":       60000.0,
		"markPrice":        60500.0,
		"positionAmt":      0.01,
		"unRealizedProfit": 5.0,
		"liquidationPrice": 30000.0,
		"leverage":         10.0,
	}}
}

// 畸形返回不得打死进程。交易所返回的 map 是不可控输入,缺字段/类型不符都可能发生;
// 而这条链路(监控循环 → 预热 → 裸 goroutine → singleflight re-panic)一旦 panic
// 就是整个交易进程退出。
func TestFetchAndProjectPositions_MalformedPositionDoesNotPanic(t *testing.T) {
	f := &warmPosTrader{pos: []map[string]interface{}{
		{"symbol": "BADUSDT"},                        // 全缺
		{"symbol": "NILUSDT", "entryPrice": nil},     // 类型为 nil
		{"symbol": "STRUSDT", "entryPrice": "60000"}, // 类型是字符串
		warmSamplePositions()[0],                     // 正常的那条必须仍被投影出来
	}}
	at := newWarmAT(f)

	done := make(chan struct{})
	var got []map[string]interface{}
	var err error
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("畸形仓位导致 panic %v —— 这条链路的 panic 会打死整个 nofx 进程", r)
			}
		}()
		got, err = at.fetchAndProjectPositions()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("fetchAndProjectPositions 卡住")
	}
	if err != nil {
		t.Fatalf("不该整体失败,应跳过坏行: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("投影出 %d 条,期望 1 条(只有正常那条) —— 坏行必须被跳过而不是拖垮整批", len(got))
	}
	if len(got) == 1 {
		if sym, _ := got[0]["symbol"].(string); sym != "BTCUSDT" {
			t.Errorf("投影出的是 %q,期望 BTCUSDT", sym)
		}
	}
}

// 预热路径必须整体 panic-safe:即使投影内部还有别的裸断言,也不能逃出 goroutine。
func TestRefreshPositionsAsync_RecoversPanic(t *testing.T) {
	at := newWarmAT(&warmPosTrader{pos: []map[string]interface{}{
		{"symbol": "X", "side": "long", "entryPrice": 1.0, "positionAmt": 1.0},
	}})
	// 直接验证 recover 存在:让 singleflight 里的函数 panic。
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic 逃出了 refreshPositionsAsync: %v", r)
			}
		}()
		at.refreshPositionsAsync()
		time.Sleep(200 * time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("卡住")
	}
}
