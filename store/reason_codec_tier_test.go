package store

import "testing"

// 档位标识是多档 DD 的第二重保险:第一重是我们持久化的 ExchangeOrderID,记录一丢
// (写库失败/交易所不回 orderID/重启前没 persist)就只能按 qty/激活价/回调率去猜
// 这张单属于哪一档 —— 多档并存时那个猜法必然碰撞。把档位编进 clientOrderID 后,
// 交易所侧自己就能说出档位。
//
// 这组测试锁死三件事:编出来能解回来、旧 id 不会被误解出档位、带档位的 reason 在
// 所有既有机制比较里仍等价于裸 reason(否则撤单/归因会全线错位)。

const testPrefix = "x-KzrpZaP9"

func TestEncodeDecodeTier_RoundTripsEveryValidTier(t *testing.T) {
	for tier := 1; tier <= 9; tier++ {
		reason := ReasonWithTier(MechNativeTrailing, tier)
		id := EncodeReasonClientID(testPrefix, reason)
		if id == "" {
			t.Fatalf("tier %d: encode returned empty id for reason %q", tier, reason)
		}
		if len(id) > 32 {
			t.Fatalf("tier %d: encoded id %q is %d chars, exceeds the 32-char venue budget", tier, id, len(id))
		}
		if got := DecodeTierFromClientID(testPrefix, id); got != tier {
			t.Fatalf("tier %d: decoded %d from %q", tier, got, id)
		}
		// 机制必须仍然解得出来:档位段插在 code 之后,不能挡住 code。
		if got := DecodeReasonFromClientID(testPrefix, id); got != MechNativeTrailing {
			t.Fatalf("tier %d: mechanism decoded as %q, want %q (id=%q)", tier, got, MechNativeTrailing, id)
		}
	}
}

func TestEncodeTier_LegacyIDWithoutTierDecodesToZero(t *testing.T) {
	// 不带档位的 reason:id 里没有 T 段,解出的档位必须是 0(未知),而不是把 nonce
	// 的第一个 hex 字符误读成档位。
	id := EncodeReasonClientID(testPrefix, MechNativeTrailing)
	if id == "" {
		t.Fatal("encode returned empty id")
	}
	if got := DecodeTierFromClientID(testPrefix, id); got != 0 {
		t.Fatalf("legacy id %q decoded tier %d, want 0 — a hex nonce char must never be read as a tier", id, got)
	}
	if got := DecodeReasonFromClientID(testPrefix, id); got != MechNativeTrailing {
		t.Fatalf("legacy id %q decoded mechanism %q, want %q", id, got, MechNativeTrailing)
	}
}

func TestDecodeTier_ForeignIDIsNeverGuessed(t *testing.T) {
	cases := []string{
		"",
		"someoneElsesClientId",
		"x-OTHERPFXNT" + "T3" + "aabbcc", // 前缀不是我们的
		testPrefix,                       // 只有前缀,长度不足
		testPrefix + "N",                 // code 不完整
	}
	for _, id := range cases {
		if got := DecodeTierFromClientID(testPrefix, id); got != 0 {
			t.Fatalf("id %q decoded tier %d, want 0 (never guess on foreign/short ids)", id, got)
		}
	}
	if got := DecodeTierFromClientID("", testPrefix+"NTT1aabbcc"); got != 0 {
		t.Fatal("empty broker prefix must decode tier 0, not match anything")
	}
}

func TestReasonWithTier_OutOfRangeDegradesToBareReason(t *testing.T) {
	// 0 和 >9 都装不进一个字符。要求是降级成"不带档位"(仍然可挂单、仍然能归因),
	// 而不是编出一个解不开的 id。
	for _, tier := range []int{-1, 0, 10, 99} {
		if got := ReasonWithTier(MechNativeTrailing, tier); got != MechNativeTrailing {
			t.Fatalf("tier %d: ReasonWithTier returned %q, want bare %q", tier, got, MechNativeTrailing)
		}
	}
}

func TestTieredReason_StaysEquivalentToBareReasonEverywhere(t *testing.T) {
	// 这是最关键的一条反向锁:带档位的 reason 一旦在机制比较里不等于裸 reason,
	// OKX 的 cancelAlgoOrdersByReason 就会认为"没有注册的机制码"而跳过定向清理,
	// 或者更糟 —— 归因把 native_trailing#2 当成未知机制,归因链整条断掉。
	tiered := ReasonWithTier(MechNativeTrailing, 2)
	if tiered == MechNativeTrailing {
		t.Fatal("fixture is not discriminating: tiered reason must differ from the bare one")
	}
	if got, want := NormalizeMechanism(tiered), MechNativeTrailing; got != want {
		t.Fatalf("NormalizeMechanism(%q) = %q, want %q", tiered, got, want)
	}
	if got, want := CodeForReason(tiered), CodeForReason(MechNativeTrailing); got != want {
		t.Fatalf("CodeForReason(%q) = %q, want %q", tiered, got, want)
	}
	if CodeForReason(tiered) == "" {
		t.Fatal("tiered reason lost its mechanism code — targeted cleanup would refuse to run")
	}
}

func TestTieredReason_ManagedDrawdownVariantStillFolds(t *testing.T) {
	// managed_drawdown_<stage> 的折叠规则必须在加了档位后依然成立(两条折叠规则
	// 叠加时的顺序问题)。
	tiered := ReasonWithTier(MechManagedDrawdown+"_dd2", 3)
	if got, want := NormalizeMechanism(tiered), MechManagedDrawdown; got != want {
		t.Fatalf("NormalizeMechanism(%q) = %q, want %q", tiered, got, want)
	}
	id := EncodeReasonClientID(testPrefix, tiered)
	if id == "" {
		t.Fatalf("encode returned empty id for %q", tiered)
	}
	if got := DecodeTierFromClientID(testPrefix, id); got != 3 {
		t.Fatalf("decoded tier %d from %q, want 3", got, id)
	}
	if got := DecodeReasonFromClientID(testPrefix, id); got != MechManagedDrawdown {
		t.Fatalf("decoded mechanism %q from %q, want %q", got, id, MechManagedDrawdown)
	}
}
