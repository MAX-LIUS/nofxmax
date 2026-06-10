package store

import (
	"sort"
	"testing"
)

func TestGetSymbolsWithLiveProtectionOrders(t *testing.T) {
	s := newTestOrderStore(t)
	orders := []*TraderOrder{
		// live protection orders → should be returned
		{TraderID: "t", ExchangeID: "ex", ExchangeOrderID: "sl1", Symbol: "WLDUSDT", Side: "BUY", Type: "ALGO", Quantity: 1, Status: "NEW"},
		{TraderID: "t", ExchangeID: "ex", ExchangeOrderID: "tr1", Symbol: "WLDUSDT", Side: "BUY", Type: "TRAILING_STOP_MARKET", Quantity: 1, Status: "NEW"},
		{TraderID: "t", ExchangeID: "ex", ExchangeOrderID: "sl2", Symbol: "BTCUSDT", Side: "SELL", Type: "ALGO", Quantity: 1, Status: "NEW"},
		// non-protection (MARKET) → excluded
		{TraderID: "t", ExchangeID: "ex", ExchangeOrderID: "mk1", Symbol: "SOLUSDT", Side: "BUY", Type: "MARKET", Quantity: 1, Status: "NEW"},
		// terminal status → excluded
		{TraderID: "t", ExchangeID: "ex", ExchangeOrderID: "sl3", Symbol: "ETHUSDT", Side: "SELL", Type: "ALGO", Quantity: 1, Status: "CANCELED"},
		// other exchange → excluded
		{TraderID: "t", ExchangeID: "other", ExchangeOrderID: "sl4", Symbol: "XRPUSDT", Side: "SELL", Type: "ALGO", Quantity: 1, Status: "NEW"},
	}
	for _, o := range orders {
		if err := s.CreateOrder(o); err != nil {
			t.Fatalf("create order: %v", err)
		}
	}

	got, err := s.GetSymbolsWithLiveProtectionOrders("ex")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	sort.Strings(got)
	want := []string{"BTCUSDT", "WLDUSDT"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}

	if empty, _ := s.GetSymbolsWithLiveProtectionOrders(""); empty != nil {
		t.Fatalf("expected nil for empty exchangeID, got %v", empty)
	}
}
