package trader

import (
	"testing"
	"time"
)

// 开仓竞态:成交在本地先确认,交易所的持仓接口要再过几秒才返回这条仓位。窗口内
// liveness gate 查不到持仓,含义是"还不知道"而不是"已经平了" —— 必须只推迟保护
// 动作,绝不做破坏性清理(撤掉刚挂好的保护单 + 清冻结 ATR)。
// 实测两次:2026-07-28 04:44 ETHUSDT、05:24 KAITOUSDT。
func TestFillGraceWindowMarksAndExpires(t *testing.T) {
	at := &AutoTrader{}

	// 没打过标记 => 不在宽限期,gate 该正常清理。
	if _, fresh := at.withinFillGraceWindow("ETHUSDT", "long"); fresh {
		t.Fatal("unmarked position must not be inside the grace window")
	}

	at.markPositionFilled("ETHUSDT", "long")
	since, fresh := at.withinFillGraceWindow("ETHUSDT", "long")
	if !fresh {
		t.Fatal("just-filled position must be inside the grace window")
	}
	if since > time.Second {
		t.Fatalf("elapsed since fill = %v, want ~0", since)
	}

	// 大小写不敏感(交易所返回 LONG/long 不一致),positionKey 统一小写化。
	if _, fresh := at.withinFillGraceWindow("ETHUSDT", "LONG"); !fresh {
		t.Fatal("grace window must be case-insensitive on side")
	}
	// 另一个方向/币种不受影响。
	if _, fresh := at.withinFillGraceWindow("ETHUSDT", "short"); fresh {
		t.Fatal("grace window leaked to the opposite side")
	}
	if _, fresh := at.withinFillGraceWindow("BTCUSDT", "long"); fresh {
		t.Fatal("grace window leaked to another symbol")
	}

	// 过期后必须放行清理 —— 否则真的平仓了也不清理,残留孤儿单。
	at.recentFillMu.Lock()
	at.recentFillAt[positionKey("ETHUSDT", "long")] = time.Now().Add(-protectionFillGraceWindow - time.Second)
	at.recentFillMu.Unlock()
	if _, fresh := at.withinFillGraceWindow("ETHUSDT", "long"); fresh {
		t.Fatal("expired grace window must not defer cleanup")
	}
}

// 仓位确认可见后立刻清标记:否则同一 symbol 之后真的平仓时,清理会被过期前的
// 标记挡住一段时间。
func TestFillGraceWindowClearedOnceVisible(t *testing.T) {
	at := &AutoTrader{}
	at.markPositionFilled("KAITOUSDT", "long")
	if _, fresh := at.withinFillGraceWindow("KAITOUSDT", "long"); !fresh {
		t.Fatal("expected grace window right after fill")
	}
	at.clearFillGraceWindow("KAITOUSDT", "long")
	if _, fresh := at.withinFillGraceWindow("KAITOUSDT", "long"); fresh {
		t.Fatal("grace window must be cleared once the position is visible")
	}
}

// 宽限期长度:必须够长以覆盖交易所持仓传播(实测 4-6s),又必须远短于任何真实
// 持仓周期,否则真平仓后的孤儿单清理会被拖延。
func TestFillGraceWindowDuration(t *testing.T) {
	if protectionFillGraceWindow < 10*time.Second {
		t.Fatalf("grace window %v too short to cover exchange position propagation", protectionFillGraceWindow)
	}
	if protectionFillGraceWindow > 2*time.Minute {
		t.Fatalf("grace window %v too long — delays orphan cleanup after a real close", protectionFillGraceWindow)
	}
}

// 空 receiver / 空参数不得 panic(gate 在多条读路径上被调用)。
func TestFillGraceWindowNilSafe(t *testing.T) {
	var at *AutoTrader
	at.markPositionFilled("ETHUSDT", "long")
	if _, fresh := at.withinFillGraceWindow("ETHUSDT", "long"); fresh {
		t.Fatal("nil trader must not report a grace window")
	}
	at.clearFillGraceWindow("ETHUSDT", "long")

	real := &AutoTrader{}
	real.markPositionFilled("", "long")
	real.markPositionFilled("ETHUSDT", "")
	if len(real.recentFillAt) != 0 {
		t.Fatalf("empty symbol/side must not be recorded: %+v", real.recentFillAt)
	}
}
