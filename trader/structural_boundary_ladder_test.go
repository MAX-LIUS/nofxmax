package trader

import (
	"testing"
	"time"

	"nofx/market"
	"nofx/store"
)

// ladderBar 构造一根 1h K 线。OpenTime/CloseTime 是 epoch 毫秒(market.Kline 的实际
// 类型),recency 打分依赖它们单调递增。
func ladderBar(high, low, close_ float64, ts time.Time) market.Kline {
	open := (high + low) / 2
	return market.Kline{
		OpenTime:  ts.UnixMilli(),
		CloseTime: ts.Add(time.Hour).UnixMilli(),
		Open:      open,
		High:      high,
		Low:       low,
		Close:     close_,
		Volume:    1000,
	}
}

// 构造一段横盘 + 一个明确的 swing low,验证 T1 优先。
func TestSelectStructuralBoundaryPrefersSwingPivot(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// 缓慢抬升的低点序列。刻意不用横盘:横盘时每根 bar 的 low 都相等,而枢轴判定
	// 用的是严格小于(window[j].Low < l),于是每根都成了「枢轴」,最近的那根就是
	// 入场价旁边那根 —— 测不出层级优先级,只测出了退化数据。
	bars := make([]market.Kline, 0, 60)
	for i := 0; i < 60; i++ {
		p := 100 + float64(i)*0.1
		bars = append(bars, ladderBar(p+1, p, p+0.5, base.Add(time.Duration(i)*time.Hour)))
	}
	// 唯一的孤立低点:两侧各 2 根都明显更高 → k=2 的 fractal 枢轴。
	bars[30] = ladderBar(103.5, 90, 95, base.Add(30*time.Hour))

	// entry 取 100.5:低于它的 low 只有开头几根(它们各自左邻更低,不构成枢轴)
	// 和 bars[30]。
	pick, ok := selectStructuralBoundary(bars, bars, 100.5, 2, true, 2, "1h", false, nil)
	if !ok {
		t.Fatal("no boundary selected on a window that contains a clear swing low")
	}
	if pick.Tier != tierSwingPivot {
		t.Fatalf("expected T1 swing_pivot, got %s (price=%.4f source=%s)", pick.Tier, pick.Price, pick.Source)
	}
	if pick.Price != 90 {
		t.Fatalf("expected the swing low 90, got %.4f", pick.Price)
	}
}

// T1 空手(没有任何保护侧枢轴)且 order block 可用时,必须落到 T2,而不是直接掉到
// bar 极值兜底 —— 这正是改造前 order block 只作「收紧器」造成的浪费。
func TestSelectStructuralBoundaryFallsToOrderBlock(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	// 单调上行,LONG 入场在最低价之下 → 没有低于 entry 的 swing low。
	bars := make([]market.Kline, 0, 40)
	for i := 0; i < 40; i++ {
		p := 100 + float64(i)
		bars = append(bars, ladderBar(p+1, p, p+0.5, base.Add(time.Duration(i)*time.Hour)))
	}
	entry := 99.0 // 低于所有 bar 的 low → T1/T3/T4 在保护侧都无候选

	obCalled := false
	ob := func(_ []market.Kline, _ float64, _ bool, _ string) (float64, bool) {
		obCalled = true
		return 95.0, true
	}
	pick, ok := selectStructuralBoundary(bars, bars, entry, 1, true, 2, "1h", true, ob)
	if !ok || !obCalled {
		t.Fatalf("order block tier not reached: ok=%v called=%v", ok, obCalled)
	}
	if pick.Tier != tierOrderBlock || pick.Price != 95.0 {
		t.Fatalf("expected T2 order_block @95, got %s @%.4f", pick.Tier, pick.Price)
	}
}

// preferProven=false 时 T2 必须跳过(与改造前行为一致),不能偷偷启用一个用户关掉的层。
func TestSelectStructuralBoundarySkipsOrderBlockWhenDisabled(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]market.Kline, 0, 40)
	for i := 0; i < 40; i++ {
		p := 100 + float64(i)
		bars = append(bars, ladderBar(p+1, p, p+0.5, base.Add(time.Duration(i)*time.Hour)))
	}
	called := false
	ob := func(_ []market.Kline, _ float64, _ bool, _ string) (float64, bool) {
		called = true
		return 95.0, true
	}
	_, _ = selectStructuralBoundary(bars, bars, 99, 1, true, 2, "1h", false, ob)
	if called {
		t.Fatal("order block finder invoked while PreferProvenLevels is off")
	}
}

// 全空(保护侧一个候选都没有)必须返回 false,让调用方打 miss 日志而不是拿 0 当价位。
func TestSelectStructuralBoundaryEmptyWhenNoProtectiveSideLevel(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	bars := make([]market.Kline, 0, 30)
	for i := 0; i < 30; i++ {
		bars = append(bars, ladderBar(101, 100, 100.5, base.Add(time.Duration(i)*time.Hour)))
	}
	// LONG 入场在所有 low 之下 → 无保护侧候选。
	if pick, ok := selectStructuralBoundary(bars, bars, 99, 1, true, 2, "1h", false, nil); ok {
		t.Fatalf("expected no boundary, got %s @%.4f", pick.Tier, pick.Price)
	}
}

// 容差:止损必须坐在结构位**之外**一段缓冲,而不是正好压在位上 —— 压在位上会把
// 「价格触碰」和「结构失效」当成同一件事。
func TestApplyBoundaryTolerance(t *testing.T) {
	// LONG:边界下移。
	if got := applyBoundaryTolerance(100, 2, 0.25, true); got != 99.5 {
		t.Fatalf("long pad wrong: got %.4f want 99.5", got)
	}
	// SHORT:边界上移。
	if got := applyBoundaryTolerance(100, 2, 0.25, false); got != 100.5 {
		t.Fatalf("short pad wrong: got %.4f want 100.5", got)
	}
	// ATR 或容差缺失时原样返回,不得凭空移动止损。
	if got := applyBoundaryTolerance(100, 0, 0.25, true); got != 100 {
		t.Fatalf("zero atr must not pad: got %.4f", got)
	}
	if got := applyBoundaryTolerance(100, 2, 0, true); got != 100 {
		t.Fatalf("zero tol must not pad: got %.4f", got)
	}
	if got := applyBoundaryTolerance(0, 2, 0.25, true); got != 0 {
		t.Fatalf("invalid boundary must pass through: got %.4f", got)
	}
}

// EntryTolATR 的默认值必须落到 0.25,并且负值被纠正而不是照用 —— 负容差会把止损拉进
// 结构内侧,在结构位还没被触及时就先打掉。
func TestEntryTolATRDefaults(t *testing.T) {
	got := store.StructuralSLConfig{}.WithDefaults()
	if got.EntryTolATR != 0.25 {
		t.Fatalf("unset EntryTolATR should default to 0.25, got %.4f", got.EntryTolATR)
	}
	neg := store.StructuralSLConfig{EntryTolATR: -1}.WithDefaults()
	if neg.EntryTolATR != 0.25 {
		t.Fatalf("negative EntryTolATR should be corrected to 0.25, got %.4f", neg.EntryTolATR)
	}
	explicit := store.StructuralSLConfig{EntryTolATR: 0.6}.WithDefaults()
	if explicit.EntryTolATR != 0.6 {
		t.Fatalf("explicit EntryTolATR overwritten: got %.4f", explicit.EntryTolATR)
	}
}

// 组合来源("swing_point+volume_cluster")必须可用;fibonacci 单独出现时不可用。
func TestStructuralSourceUsable(t *testing.T) {
	cases := map[string]bool{
		"swing_point":                true,
		"volume_cluster":             true,
		"swing_point+volume_cluster": true,
		"fibonacci":                  false,
		"fibonacci+volume_cluster":   true,
		"":                           false,
	}
	for src, want := range cases {
		if got := structuralSourceUsable(src); got != want {
			t.Fatalf("source %q: got %v want %v", src, got, want)
		}
	}
}

// T3 是用户要的那一层:前高前低 + 成交密集区。构造方式利用 window 与 bars 的分工 ——
// T1(枢轴)和 T4(bar 极值)只看回看窗 window,T3 走整段 bars(触碰次数与 recency
// 需要更长历史)。于是把结构位放在窗口**之外**、整段之内,就只有 T3 够得着。
//
// 这正是 ZECUSDT 那一单的形态:入场前的关键前低在 LookbackBars 窗口之外,改造前的
// 内联枢轴扫描扫不到,边界为空 → 结构单位掉到 fallback_atr_mul 3.2,1.5×ATR 那层
// close-confirm 从未存在。
func TestSelectStructuralBoundaryReachesClusteredTierOutsideWindow(t *testing.T) {
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	entry := 100.0

	bars := make([]market.Kline, 0, 140)
	// 段一(0..59):在 92 一带反复触碰的密集区 —— 前低 + 放量。
	for i := 0; i < 60; i++ {
		low := 92.0
		if i%5 != 0 {
			low = 94.0 + float64(i%3)
		}
		k := ladderBar(low+6, low, low+3, base.Add(time.Duration(i)*time.Hour))
		if low == 92.0 {
			k.Volume = 8000 // 放量,喂 volume_cluster 的成交量打分
		}
		bars = append(bars, k)
	}
	// 段二(60..139):抬升到 entry 之上并停在那里 —— 回看窗内没有任何低于 entry 的
	// low,所以 T1/T4 在保护侧无候选。
	for i := 60; i < 140; i++ {
		bars = append(bars, ladderBar(103, 100.8, 101.5, base.Add(time.Duration(i)*time.Hour)))
	}
	window := bars[len(bars)-24:]

	// 先证明前提成立:T1 与 T4 在这份数据上确实空手,否则本测试测不到 T3。
	if _, ok := nearestFractalPivot(window, entry, true, 2); ok {
		t.Fatal("fixture invalid: T1 found a pivot inside the window")
	}
	if _, ok := nearestBarExtreme(window, entry, true); ok {
		t.Fatal("fixture invalid: T4 found a bar extreme inside the window")
	}

	pick, ok := selectStructuralBoundary(window, bars, entry, 1.5, true, 2, "1h", false, nil)
	if !ok {
		t.Fatal("no boundary selected although a clustered level exists below entry")
	}
	if pick.Tier != tierClustered {
		t.Fatalf("expected T3 clustered, got %s @%.4f source=%s", pick.Tier, pick.Price, pick.Source)
	}
	if pick.Price >= entry {
		t.Fatalf("clustered pick is on the wrong side of entry: %.4f >= %.4f", pick.Price, entry)
	}
	if !structuralSourceUsable(pick.Source) {
		t.Fatalf("clustered pick came from an excluded source: %s", pick.Source)
	}
	if pick.Strength < clusteredMinStrength {
		t.Fatalf("clustered pick below strength gate: strength=%d", pick.Strength)
	}
	if pick.Confidence < clusteredMinConfidence {
		t.Fatalf("clustered pick below confidence gate: conf=%.1f", pick.Confidence)
	}
}
