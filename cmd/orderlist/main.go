// Command orderlist is a READ-ONLY listing of one trader's live open orders on
// any supported exchange (okx or binance). It exists because cmd/cancelbyid is
// binance-only, and the 2026-07-27 audit needs to compare OKX trailing-order
// quantities against each protection record's close_ratio_pct.
//
// It never writes: no cancel path, no store.New (which would AutoMigrate the
// production DB), sqlite opened read-only. Binance gets the same
// SetExecutionPreferences(true, true) as the live trader so USDC-routed symbols
// are queried in the right market.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"

	"nofx/config"
	"nofx/crypto"
	"nofx/proxyhook"
	"nofx/trader/binance"
	"nofx/trader/okx"
	"nofx/trader/types"
)

type client interface {
	GetPositions() ([]map[string]interface{}, error)
	GetOpenOrders(symbol string) ([]types.OpenOrder, error)
}

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "sqlite path (read-only)")
	traderName := flag.String("trader", "", "trader name (required)")
	symbols := flag.String("symbols", "", "comma-separated symbols (required)")
	envFile := flag.String("env", "/opt/webstack/nofx/.env", "env file")
	flag.Parse()

	if *traderName == "" || *symbols == "" {
		fmt.Println("-trader and -symbols are required")
		os.Exit(2)
	}

	if err := godotenv.Load(*envFile); err != nil {
		fmt.Printf("load env %s: %v\n", *envFile, err)
		os.Exit(1)
	}
	config.Init()
	proxyhook.Register()

	db, err := sql.Open("sqlite3", "file:"+*dbPath+"?mode=ro")
	if err != nil {
		fmt.Println("open db:", err)
		os.Exit(1)
	}
	defer db.Close()

	var exType, apiKeyEnc, secretEnc, userID string
	var passEnc sql.NullString
	err = db.QueryRow(`SELECT e.exchange_type, e.api_key, e.secret_key, e.passphrase, t.user_id
		FROM traders t JOIN exchanges e ON e.id = t.exchange_id WHERE t.name = ?`, *traderName).
		Scan(&exType, &apiKeyEnc, &secretEnc, &passEnc, &userID)
	if err != nil {
		fmt.Println("load trader:", err)
		os.Exit(1)
	}

	cs, err := crypto.NewCryptoService()
	if err != nil {
		fmt.Println("crypto:", err)
		os.Exit(1)
	}
	dec := func(s string) string {
		if s == "" {
			return ""
		}
		v, err := cs.DecryptFromStorage(s)
		if err != nil {
			fmt.Println("decrypt:", err)
			os.Exit(1)
		}
		return v
	}

	var c client
	switch exType {
	case "okx":
		c = okx.NewOKXTrader(dec(apiKeyEnc), dec(secretEnc), dec(passEnc.String))
	case "binance":
		bt := binance.NewFuturesTrader(dec(apiKeyEnc), dec(secretEnc), userID)
		bt.SetExecutionPreferences(true, true)
		c = bt
	default:
		fmt.Printf("unsupported exchange type %q\n", exType)
		os.Exit(1)
	}

	fmt.Printf("trader=%s exchange=%s\n", *traderName, exType)

	for _, sym := range strings.Split(*symbols, ",") {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		orders, err := c.GetOpenOrders(sym)
		if err != nil {
			fmt.Printf("\n=== %s: GetOpenOrders error: %v\n", sym, err)
			continue
		}
		sort.Slice(orders, func(i, j int) bool { return orders[i].Type < orders[j].Type })
		fmt.Printf("\n=== %s open orders: %d ===\n", sym, len(orders))
		for _, o := range orders {
			fmt.Printf("  %-22s type=%-22s side=%-5s posSide=%-6s qty=%-12.6f stop=%-12.6f act=%-10s cb=%.4f status=%-4s cid=%s\n",
				o.OrderID, o.Type, o.Side, o.PositionSide, o.Quantity, o.StopPrice,
				o.ActivationStatus, o.CallbackRate, o.Status, o.ClientOrderID)
		}
	}
}
