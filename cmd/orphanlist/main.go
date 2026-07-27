// orphanlist lists open orders for a symbol on the BN Binance account WITHOUT
// requiring an open position. The zombieaudit tool iterates live positions, so once
// a position closes its leftover algo orders become invisible to it — exactly the
// state HYPEUSDT reached when break_even_stop closed the position at 00:19:32 while
// ~174 leaked trailing orders were still resting. Read-only: lists, never cancels.
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
)

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "sqlite path (read-only)")
	symbol := flag.String("symbol", "HYPEUSDT", "symbol")
	envFile := flag.String("env", "/opt/webstack/nofx/.env", "env file with DATA_ENCRYPTION_KEY / RSA_PRIVATE_KEY / BINANCE_PROXY_URL")
	flag.Parse()

	if *envFile != "" {
		if err := godotenv.Load(*envFile); err != nil {
			fmt.Printf("load env %s: %v\n", *envFile, err)
			os.Exit(1)
		}
	}
	config.Init()
	proxyhook.Register()

	db, err := sql.Open("sqlite3", "file:"+*dbPath+"?mode=ro")
	if err != nil {
		fmt.Println("db:", err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT e.id, e.api_key, e.secret_key, t.name, e.user_id
		FROM exchanges e JOIN traders t ON t.exchange_id = e.id
		WHERE e.exchange_type = 'binance' LIMIT 1`)
	if err != nil {
		fmt.Println("query:", err)
		os.Exit(1)
	}
	defer rows.Close()
	if !rows.Next() {
		fmt.Println("no binance exchange row")
		os.Exit(1)
	}
	var exID, encKey, encSec, name, userID string
	if err := rows.Scan(&exID, &encKey, &encSec, &name, &userID); err != nil {
		fmt.Println("scan:", err)
		os.Exit(1)
	}
	cs, err := crypto.NewCryptoService()
	if err != nil {
		fmt.Println("crypto:", err)
		os.Exit(1)
	}
	key, err := cs.DecryptFromStorage(encKey)
	if err != nil {
		fmt.Println("decrypt key:", err)
		os.Exit(1)
	}
	sec, err := cs.DecryptFromStorage(encSec)
	if err != nil {
		fmt.Println("decrypt secret:", err)
		os.Exit(1)
	}

	tr := binance.NewFuturesTrader(key, sec, userID)
	// Mirror the live trader: auto_trader.go:309 enables USDC routing + maker TP for
	// Binance by default (ResolveBinanceUSDCMaker returns true), so a symbol like
	// SOLUSDT actually executes as SOLUSDC. Without this the list endpoint returns an
	// empty set for the USDT symbol and the tool falsely reports "no protection".
	tr.SetExecutionPreferences(true, true)
	orders, err := tr.GetOpenOrders(*symbol)
	if err != nil {
		fmt.Println("GetOpenOrders:", err)
		os.Exit(1)
	}
	fmt.Printf("\n=== %s (trader %s) open orders: %d ===\n", *symbol, name, len(orders))
	shapes := map[string]int{}
	for _, o := range orders {
		k := fmt.Sprintf("type=%-22s side=%-4s posSide=%-5s qty=%.6f stop=%.6f cb=%.4f status=%s role=%s",
			o.Type, o.Side, o.PositionSide, o.Quantity, o.StopPrice, o.CallbackRate, o.Status, o.ProtectionRole)
		shapes[k]++
	}
	keys := make([]string, 0, len(shapes))
	for k := range shapes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return shapes[keys[i]] > shapes[keys[j]] })
	for _, k := range keys {
		fmt.Printf("  x%-4d %s\n", shapes[k], k)
	}
	trailing := 0
	for _, o := range orders {
		if strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			trailing++
		}
	}
	fmt.Printf("\n  trailing total: %d\n", trailing)
	// Print EVERY trailing order with its id. The previous 6-id sample truncated the
	// list, which is useless when the whole point is deciding which specific order to
	// cancel: a cleanup driven by a truncated list would either miss a duplicate or
	// target the wrong id. On Binance the only distinguishing field is StopPrice (the
	// live moving stop), since the venue reports no callbackRate — see
	// trader/venue_trailing_parity_test.go.
	fmt.Println("  trailing orders (id | qty | stop):")
	for _, o := range orders {
		if !strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
			continue
		}
		fmt.Printf("    %-20s qty=%.6f stop=%.6f cb=%.6f status=%s\n", o.OrderID, o.Quantity, o.StopPrice, o.CallbackRate, o.Status)
	}
}
