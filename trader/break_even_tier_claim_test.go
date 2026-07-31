package trader

import "testing"

// findConflictingBETierClaim 是"本档是否该被先到档抑制"的唯一判定。
// 线上实况(2026-07-31 ETHUSDT short):BE1 1862.1467 / BE2 1859.3489,
// 相对间距 0.1503% < protectionPriceTolerancePct(0.2%) → BE2 该被抑制。
func TestFindConflictingBETierClaim(t *testing.T) {
	be1 := claimedBETier{stage: "BE1", price: 1862.1467, qty: 0.0832}

	cases := []struct {
		name    string
		claimed []claimedBETier
		price   float64
		qty     float64
		want    beTierClaimDecision
		wantWho string
	}{
		{
			name:    "本轮尚无认领 → 独立成档",
			claimed: nil,
			price:   1862.1467,
			qty:     0.0832,
			want:    beTierClaimIndependent,
		},
		{
			// 线上原形:0.1503% 间距,落在 0.2% 容差内。
			name:    "撞进容差带且数量覆盖得住 → 抑制",
			claimed: []claimedBETier{be1},
			price:   1859.3489,
			qty:     0.0832,
			want:    beTierClaimSuppress,
			wantWho: "BE1",
		},
		{
			name:    "价格差超出容差 → 各自独立,不得抑制",
			claimed: []claimedBETier{be1},
			price:   1800.0,
			qty:     0.0832,
			want:    beTierClaimIndependent,
		},
		{
			// 覆盖不足是真实问题,必须留在原路径上并打出来,不能被"看起来已有单"吞掉。
			name:    "撞价但先到档覆盖不住 → 不抑制,报覆盖不足",
			claimed: []claimedBETier{{stage: "BE1", price: 1862.1467, qty: 0.03}},
			price:   1859.3489,
			qty:     0.0832,
			want:    beTierClaimUndercovered,
			wantWho: "BE1",
		},
		{
			// 浮点尾差不该被读成"覆盖不住" —— 数量是量化后的事实。
			name:    "数量仅差浮点尾差 → 仍算覆盖得住",
			claimed: []claimedBETier{{stage: "BE1", price: 1862.1467, qty: 0.0832}},
			price:   1859.3489,
			qty:     0.0832 + 1e-15,
			want:    beTierClaimSuppress,
			wantWho: "BE1",
		},
		{
			// 先扫到的覆盖不住、后面有一档覆盖得住 → 仍应抑制(保留原循环语义:
			// 覆盖不足只是"这一条不算",不终止扫描)。
			name: "多档中后一档覆盖得住 → 抑制",
			claimed: []claimedBETier{
				{stage: "BE1", price: 1862.1467, qty: 0.03},
				{stage: "BE2", price: 1862.1400, qty: 0.0832},
			},
			price:   1859.3489,
			qty:     0.0832,
			want:    beTierClaimSuppress,
			wantWho: "BE2",
		},
		{
			// 全都覆盖不住 → 报第一个撞上的,让日志指向最先到的那档。
			name: "多档全覆盖不住 → 报第一个撞上的",
			claimed: []claimedBETier{
				{stage: "BE1", price: 1862.1467, qty: 0.03},
				{stage: "BE2", price: 1862.1400, qty: 0.04},
			},
			price:   1859.3489,
			qty:     0.0832,
			want:    beTierClaimUndercovered,
			wantWho: "BE1",
		},
		{
			// 价格非正:approximatelyEqualPrice 直接判不等,绝不能误抑制。
			name:    "本档价格为 0 → 不匹配任何档",
			claimed: []claimedBETier{be1},
			price:   0,
			qty:     0.0832,
			want:    beTierClaimIndependent,
		},
		{
			name:    "先到档价格为负(不该出现)→ 不匹配",
			claimed: []claimedBETier{{stage: "BE1", price: -1862.1467, qty: 0.0832}},
			price:   1859.3489,
			qty:     0.0832,
			want:    beTierClaimIndependent,
		},
		{
			// 数量为 0 的本档:任何先到档都覆盖得住它。
			name:    "本档数量为 0 → 撞价即抑制",
			claimed: []claimedBETier{be1},
			price:   1859.3489,
			qty:     0,
			want:    beTierClaimSuppress,
			wantWho: "BE1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			winner, got := findConflictingBETierClaim(c.claimed, c.price, c.qty)
			if got != c.want {
				t.Fatalf("decision = %d, want %d", got, c.want)
			}
			if c.wantWho != "" && winner.stage != c.wantWho {
				t.Fatalf("winner = %q, want %q", winner.stage, c.wantWho)
			}
			if c.want == beTierClaimIndependent && winner.stage != "" {
				t.Fatalf("独立成档时不该带出赢家,got %q", winner.stage)
			}
		})
	}
}

// 抑制判定必须是纯函数:同样输入反复调用结论一致,且不得改动传入的切片。
func TestFindConflictingBETierClaimIsPure(t *testing.T) {
	claimed := []claimedBETier{{stage: "BE1", price: 1862.1467, qty: 0.0832}}
	snapshot := append([]claimedBETier(nil), claimed...)

	for i := 0; i < 50; i++ {
		if _, got := findConflictingBETierClaim(claimed, 1859.3489, 0.0832); got != beTierClaimSuppress {
			t.Fatalf("第 %d 次调用结论变了: %d", i, got)
		}
	}
	if len(claimed) != len(snapshot) || claimed[0] != snapshot[0] {
		t.Fatal("判定不得改动已认领档位列表")
	}
}
