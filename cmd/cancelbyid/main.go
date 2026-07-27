// Command cancelbyid cancels SPECIFIC exchange order IDs for one trader/symbol.
//
// This exists because cmd/zombieaudit -cancel only ever cancels orders that NO armed
// protection record claims. The 2026-07-27 cleanup is the opposite case: the orders to
// remove ARE claimed — by records whose parameters were computed wrong (raw ATR multiples
// armed as percents, before the v1.16.9 fix). Bulk "cancel all trailing" is not an option
// either, because the correctly-resolved sibling tier rests alongside them and must survive.
//
// Safety design:
//   - dry-run by default; -cancel must be passed explicitly
//   - every requested ID must be verified present in GetOpenOrders BEFORE anything is
//     cancelled; if any is missing the whole batch aborts. A stale ID list means the
//     operator's view is out of date, and cancelling from a stale view is how you kill
//     the wrong order.
//   - it prints the full live order set first, and refuses to cancel every trailing order
//     on the symbol (that would leave the position with no trailing protection at all).
//   - SetExecutionPreferences(true, true) mirrors the live trader's USDC routing; without
//     it the tool queries the wrong market entirely and sees an empty order set.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
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
	traderName := flag.String("trader", "BN", "trader name")
	symbol := flag.String("symbol", "", "symbol (required)")
	idsCSV := flag.String("ids", "", "comma-separated exchange order IDs to cancel (required)")
	doCancel := flag.Bool("cancel", false, "actually cancel (default: dry run)")
	envFile := flag.String("env", "/opt/webstack/nofx/.env", "env file")
	flag.Parse()

	if *symbol == "" || *idsCSV == "" {
		fmt.Println("-symbol and -ids are required")
		os.Exit(2)
	}
	want := map[string]bool{}
	for _, s := range strings.Split(*idsCSV, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
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

	var apiKeyEnc, secretEnc, userID, exType string
	err = db.QueryRow(`SELECT e.exchange_type, e.api_key, e.secret_key, t.user_id
		FROM traders t JOIN exchanges e ON e.id = t.exchange_id WHERE t.name = ?`, *traderName).
		Scan(&exType, &apiKeyEnc, &secretEnc, &userID)
	if err != nil {
		fmt.Println("load trader:", err)
		os.Exit(1)
	}
	if exType != "binance" {
		fmt.Printf("trader %s is %s, this tool only handles binance\n", *traderName, exType)
		os.Exit(1)
	}

	cs, err := crypto.NewCryptoService()
	if err != nil {
		fmt.Println("crypto:", err)
		os.Exit(1)
	}
	apiKey, err := cs.DecryptFromStorage(apiKeyEnc)
	if err != nil {
		fmt.Println("decrypt api key:", err)
		os.Exit(1)
	}
	secret, err := cs.DecryptFromStorage(secretEnc)
	if err != nil {
		fmt.Println("decrypt secret:", err)
		os.Exit(1)
	}

	tr := binance.NewFuturesTrader(apiKey, secret, userID)
	tr.SetExecutionPreferences(true, true)

	orders, err := tr.GetOpenOrders(*symbol)
	if err != nil {
		fmt.Println("GetOpenOrders:", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== %s / %s live open orders: %d ===\n", *traderName, *symbol, len(orders))
	live := map[string]bool{}
	trailingTotal := 0
	trailingTargeted := 0
	for _, o := range orders {
		isTrailing := strings.Contains(strings.ToUpper(o.Type), "TRAILING")
		if isTrailing {
			trailingTotal++
		}
		live[o.OrderID] = true
		mark := "  keep"
		if want[o.OrderID] {
			mark = "  ►CANCEL"
			if isTrailing {
				trailingTargeted++
			}
		}
		fmt.Printf("%s %-20s type=%-22s qty=%.6f stop=%.6f status=%s\n", mark, o.OrderID, o.Type, o.Quantity, o.StopPrice, o.Status)
	}

	// Abort on any stale ID: an out-of-date view must never drive a cancel.
	var missing []string
	for id := range want {
		if !live[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		fmt.Printf("\nABORT: %d requested id(s) are not live right now: %v\n", len(missing), missing)
		fmt.Println("The order list changed since it was captured. Re-read and rebuild the id list.")
		os.Exit(1)
	}
	if trailingTotal > 0 && trailingTargeted >= trailingTotal {
		fmt.Printf("\nABORT: that would cancel ALL %d trailing orders on %s, leaving no trailing protection.\n", trailingTotal, *symbol)
		os.Exit(1)
	}

	fmt.Printf("\nplan: cancel %d order(s); %d of %d trailing orders remain afterwards\n",
		len(want), trailingTotal-trailingTargeted, trailingTotal)
	if !*doCancel {
		fmt.Println("DRY RUN — pass -cancel to execute")
		return
	}

	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	if err := tr.CancelTrailingStopOrdersByIDs(*symbol, ids); err != nil {
		fmt.Println("cancel failed:", err)
		os.Exit(1)
	}
	fmt.Println("cancelled:", strings.Join(ids, ", "))

	after, err := tr.GetOpenOrders(*symbol)
	if err != nil {
		fmt.Println("post-cancel re-read failed:", err)
		return
	}
	fmt.Printf("\n=== %s open orders after cancel: %d ===\n", *symbol, len(after))
	for _, o := range after {
		fmt.Printf("  %-20s type=%-22s qty=%.6f stop=%.6f status=%s\n", o.OrderID, o.Type, o.Quantity, o.StopPrice, o.Status)
	}
}
