package market

import (
	"testing"
	"time"
)

func TestMarketForSymbol(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{
		"AAPL": AssetStock, "SKHYNIX": AssetStock, "SONY": AssetStock,
		"XAU": AssetCommodity, "BTC": AssetCrypto,
	})
	cases := []struct {
		sym  string
		want string
		ok   bool
	}{
		{"AAPLUSDT", "US_EQUITY", true},
		{"SKHYNIX-USDT-SWAP", "KRX", true},
		{"SONYUSDT", "TSE", true},
		{"XAU-USDT-SWAP", "COMMODITY", true},
		{"BTCUSDT", "", false},
	}
	for _, c := range cases {
		m, ok := MarketForSymbol(c.sym)
		if ok != c.ok || (ok && m.Name != c.want) {
			t.Errorf("MarketForSymbol(%q)=%q,%v want %q,%v", c.sym, m.Name, ok, c.want, c.ok)
		}
	}
}

func TestPreOpenWindowUSEquity(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	// Wed 2026-07-15, 60-min window before 09:30 → block 08:30..09:29.
	at := func(h, m int) time.Time { return time.Date(2026, 7, 15, h, m, 0, 0, loc) }
	if !marketUSEquity.InPreOpenWindow(at(8, 45), 60) {
		t.Error("08:45 ET should be in pre-open window")
	}
	if !marketUSEquity.InPreOpenWindow(at(9, 29), 60) {
		t.Error("09:29 ET should be in pre-open window")
	}
	if marketUSEquity.InPreOpenWindow(at(9, 30), 60) {
		t.Error("09:30 ET (open) should NOT be blocked")
	}
	if marketUSEquity.InPreOpenWindow(at(8, 29), 60) {
		t.Error("08:29 ET (before window) should NOT be blocked")
	}
	if marketUSEquity.InPreOpenWindow(at(14, 0), 60) {
		t.Error("14:00 ET (midday) should NOT be blocked")
	}
	// Saturday → no session, never blocked.
	sat := time.Date(2026, 7, 18, 8, 45, 0, 0, loc)
	if marketUSEquity.InPreOpenWindow(sat, 60) {
		t.Error("Saturday should never block")
	}
}

func TestPreOpenWindowDSTvsWinter(t *testing.T) {
	// Same wall-clock local time must gate identically in summer (EDT) and winter (EST).
	loc, _ := time.LoadLocation("America/New_York")
	summer := time.Date(2026, 7, 15, 9, 0, 0, 0, loc) // Wed EDT
	winter := time.Date(2026, 1, 14, 9, 0, 0, 0, loc) // Wed EST
	if !marketUSEquity.InPreOpenWindow(summer, 60) || !marketUSEquity.InPreOpenWindow(winter, 60) {
		t.Error("09:00 local must be in pre-open window in BOTH EDT and EST")
	}
}

func TestInPreOpenBlockCryptoNeverBlocks(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{"ETH": AssetCrypto})
	if blocked, _ := InPreOpenBlock("ETHUSDT", time.Now(), 60); blocked {
		t.Error("crypto must never be pre-open blocked")
	}
	// unknown → crypto default → never blocked
	if blocked, _ := InPreOpenBlock("NEWCOINUSDT", time.Now(), 60); blocked {
		t.Error("unknown symbol must never be blocked")
	}
}

func TestUSEquityHolidayGate(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{"AAPL": AssetStock})
	loc, _ := time.LoadLocation("America/New_York")
	// Thanksgiving 2026 = Nov 26 (Thu). 08:45 ET is inside the naive pre-open window
	// but the market is CLOSED → must NOT block.
	thanks := time.Date(2026, 11, 26, 8, 45, 0, 0, loc)
	if blocked, _ := InPreOpenBlock("AAPLUSDT", thanks, 60); blocked {
		t.Error("Thanksgiving must not gate (market closed)")
	}
	// The day before (Wed Nov 25) is a normal trading day → 08:45 blocks.
	wed := time.Date(2026, 11, 25, 8, 45, 0, 0, loc)
	if blocked, _ := InPreOpenBlock("AAPLUSDT", wed, 60); !blocked {
		t.Error("Nov 25 (trading day) 08:45 should gate")
	}
	// July 4 2026 observed Fri Jul 3 → closed → no gate.
	jul3 := time.Date(2026, 7, 3, 8, 45, 0, 0, loc)
	if blocked, _ := InPreOpenBlock("AAPLUSDT", jul3, 60); blocked {
		t.Error("Jul 3 2026 (observed July 4) must not gate")
	}
}

func TestKRXHolidayGate(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{"SKHYNIX": AssetStock})
	loc, _ := time.LoadLocation("Asia/Seoul")
	// Seollal 2026-02-17 → closed → no gate at 08:30 KST.
	seollal := time.Date(2026, 2, 17, 8, 30, 0, 0, loc)
	if blocked, _ := InPreOpenBlock("SKHYNIX-USDT-SWAP", seollal, 60); blocked {
		t.Error("KRX Seollal must not gate")
	}
	// Unknown year (2030) degrades to weekday-only: a Tuesday 08:30 still gates.
	tue2030 := time.Date(2030, 3, 5, 8, 30, 0, 0, loc) // Tue
	if blocked, _ := InPreOpenBlock("SKHYNIX-USDT-SWAP", tue2030, 60); !blocked {
		t.Error("KRX 2030 weekday should still gate (degrade to weekday-only)")
	}
}

func TestCommodityFridayNoReopen(t *testing.T) {
	setAssetClasses("okx", map[string]AssetClass{"XAU": AssetCommodity})
	loc, _ := time.LoadLocation("America/New_York")
	// Friday 17:15 ET: no 18:00 reopen (closed to Sunday) → must NOT gate.
	fri := time.Date(2026, 7, 17, 17, 15, 0, 0, loc) // Fri
	if blocked, _ := InPreOpenBlock("XAU-USDT-SWAP", fri, 60); blocked {
		t.Error("Commodity Friday evening has no reopen → must not gate")
	}
	// Sunday 17:15 ET: reopens 18:00 into Monday session → should gate.
	sun := time.Date(2026, 7, 19, 17, 15, 0, 0, loc) // Sun
	if blocked, _ := InPreOpenBlock("XAU-USDT-SWAP", sun, 60); !blocked {
		t.Error("Commodity Sunday 17:15 (before 18:00 reopen) should gate")
	}
}
