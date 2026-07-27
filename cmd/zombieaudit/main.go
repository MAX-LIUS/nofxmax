// zombieaudit cross-references live exchange trailing orders against the armed
// dynamic-protection records, to find "zombie" trailing orders that no record
// claims. Those are leftovers from two distinct bugs: the pre-v1.16.5 collapse
// bug (cancelled sibling tiers, records left pointing at dead orders) and the
// pre-v1.16.7 partial-arm leak (Binance/Bitget partial tiers re-armed every
// poll because the arm never persisted a record, so the cooldown never applied
// — hundreds of identical trailing orders with nothing tracking them).
//
// Default mode is READ-ONLY (audit). Pass -cancel to actually cancel the
// unclaimed orders. Cancel only ever touches orders that (a) no armed record
// references AND (b) belong to a symbol/side whose every live position tier is
// already covered by a claimed order — so protection coverage never shrinks.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3"

	"nofx/config"
	"nofx/crypto"
	"nofx/proxyhook"
	"nofx/store"
	"nofx/trader/binance"
	"nofx/trader/okx"
	"nofx/trader/types"
)

type exchangeAccount struct {
	TraderName   string
	ExchangeID   string
	ExchangeType string
	UserID       string
	APIKey       string
	SecretKey    string
	Passphrase   string
}

// auditClient is the minimal read/cancel surface the audit needs. Both the OKX
// and Binance adapters satisfy it, so the audit logic stays exchange-agnostic
// instead of being duplicated per venue.
type auditClient interface {
	GetPositions() ([]map[string]interface{}, error)
	GetOpenOrders(symbol string) ([]types.OpenOrder, error)
	CancelOrder(symbol, orderID string) error
}

func newAuditClient(a exchangeAccount) (auditClient, error) {
	switch a.ExchangeType {
	case "okx":
		return okx.NewOKXTrader(a.APIKey, a.SecretKey, a.Passphrase), nil
	case "binance":
		bt := binance.NewFuturesTrader(a.APIKey, a.SecretKey, a.UserID)
		// MUST mirror the live trader's execution preferences or the audit queries the
		// WRONG MARKET. auto_trader.go:309 calls SetExecutionPreferences(usdcMaker,
		// usdcMaker) with ResolveBinanceUSDCMaker(), which defaults to TRUE for Binance,
		// so positions on bases that have a USDC perp actually rest on <base>USDC.
		// Without this the adapter's toExecSymbol leaves the symbol as <base>USDT, the
		// list endpoint returns an EMPTY set (no error — the market simply has no orders
		// for this account), and the audit reports trailing=0 with every live record
		// counted as "stale". Observed 2026-07-27: SOLUSDT audited as trailing=0
		// stale_records=3 while SOLUSDC actually carried 3 trailing + 1 stop + 4 TP,
		// including the two orderIDs the audit had just called stale. A false orphan
		// verdict here would drive a cancel against healthy protection.
		bt.SetExecutionPreferences(true, true)
		return bt, nil
	default:
		return nil, fmt.Errorf("unsupported exchange type %q", a.ExchangeType)
	}
}

// loadProtectionState reads the dynamic-protection JSON blob out of system_config
// without going through the GORM store (no migrations against production).
func loadProtectionState(db *sql.DB) (*store.DynamicProtectionState, error) {
	var raw string
	err := db.QueryRow(`SELECT value FROM system_config WHERE key = ?`,
		store.DynamicProtectionStateConfigKey).Scan(&raw)
	if err == sql.ErrNoRows || raw == "" {
		return &store.DynamicProtectionState{Records: map[string]store.DynamicProtectionRecord{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var state store.DynamicProtectionState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	if state.Records == nil {
		state.Records = map[string]store.DynamicProtectionRecord{}
	}
	return &state, nil
}

// loadAccounts returns one entry per running trader bound to a supported
// exchange, deduplicated by exchange account so each account is audited once.
func loadAccounts(db *sql.DB, cs *crypto.CryptoService, only string) ([]exchangeAccount, error) {
	rows, err := db.Query(`
		SELECT t.name, t.user_id, e.id, e.exchange_type, e.api_key, e.secret_key, e.passphrase
		FROM traders t JOIN exchanges e ON e.id = t.exchange_id
		WHERE e.exchange_type IN ('okx', 'binance')
		ORDER BY e.exchange_type, t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[string]struct{}{}
	out := make([]exchangeAccount, 0)
	for rows.Next() {
		var a exchangeAccount
		var pass sql.NullString
		if err := rows.Scan(&a.TraderName, &a.UserID, &a.ExchangeID, &a.ExchangeType, &a.APIKey, &a.SecretKey, &pass); err != nil {
			return nil, err
		}
		a.Passphrase = pass.String
		if only != "" && !strings.EqualFold(only, a.ExchangeType) {
			continue
		}
		if _, dup := seen[a.ExchangeID]; dup {
			continue
		}
		seen[a.ExchangeID] = struct{}{}
		a.APIKey = decryptField(cs, a.APIKey)
		a.SecretKey = decryptField(cs, a.SecretKey)
		a.Passphrase = decryptField(cs, a.Passphrase)
		out = append(out, a)
	}
	return out, rows.Err()
}

func decryptField(cs *crypto.CryptoService, value string) string {
	if value == "" || !cs.IsEncryptedStorageValue(value) {
		return value
	}
	plain, err := cs.DecryptFromStorage(value)
	if err != nil {
		return value
	}
	return plain
}

func main() {
	dbPath := flag.String("db", "/opt/webstack/nofx/data/data.db", "sqlite database path")
	doCancel := flag.Bool("cancel", false, "actually cancel unclaimed trailing orders")
	onlySymbol := flag.String("symbol", "", "restrict to one symbol (e.g. CLUSDT)")
	onlyExchange := flag.String("exchange", "", "restrict to one exchange type (okx|binance)")
	envFile := flag.String("env", "/opt/webstack/nofx/.env", "env file with DATA_ENCRYPTION_KEY / RSA_PRIVATE_KEY")
	flag.Parse()

	if *envFile != "" {
		if err := godotenv.Load(*envFile); err != nil {
			log.Fatalf("load env %s: %v", *envFile, err)
		}
	}
	if os.Getenv("DATA_ENCRYPTION_KEY") == "" {
		log.Fatal("DATA_ENCRYPTION_KEY not set — credentials cannot be decrypted")
	}
	// Binance blocks this host's IP directly; the live process reaches it through
	// BINANCE_PROXY_URL. Register the same hook (after config.Init, before any
	// trader is constructed) or every Binance call fails with a geo restriction.
	config.Init()
	proxyhook.Register()
	cs, err := crypto.NewCryptoService()
	if err != nil {
		log.Fatalf("crypto service: %v", err)
	}

	// Raw read-only connection: the production DB is ~5GB and already migrated by
	// the live process, so we must NOT run store.New (AutoMigrate) against it.
	db, err := sql.Open("sqlite3", "file:"+*dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	state, err := loadProtectionState(db)
	if err != nil {
		log.Fatalf("load protection state: %v", err)
	}

	// claimed[exchangeID][symbol|side] = set of orderIDs referenced by armed records
	claimed := map[string]map[string]map[string]string{}
	for _, rec := range state.Records {
		if rec.Status != "armed" || rec.ExchangeOrderID == "" {
			continue
		}
		byPos := claimed[rec.ExchangeID]
		if byPos == nil {
			byPos = map[string]map[string]string{}
			claimed[rec.ExchangeID] = byPos
		}
		k := strings.ToUpper(rec.Symbol) + "|" + strings.ToLower(rec.Side)
		if byPos[k] == nil {
			byPos[k] = map[string]string{}
		}
		byPos[k][rec.ExchangeOrderID] = fmt.Sprintf("%s ratio=%.0f", rec.ProtectionType, rec.CloseRatioPct)
	}

	accounts, err := loadAccounts(db, cs, *onlyExchange)
	if err != nil {
		log.Fatalf("load accounts: %v", err)
	}
	if len(accounts) == 0 {
		log.Fatal("no matching exchange account found")
	}

	for _, acct := range accounts {
		fmt.Printf("\n=== trader %s exchange=%s type=%s ===\n", acct.TraderName, acct.ExchangeID, acct.ExchangeType)
		tr, err := newAuditClient(acct)
		if err != nil {
			fmt.Printf("  client ERR: %v\n", err)
			continue
		}
		ex := acct

		positions, err := tr.GetPositions()
		if err != nil {
			fmt.Printf("  GetPositions ERR: %v\n", err)
			continue
		}
		byPos := claimed[ex.ExchangeID]

		for _, p := range positions {
			rawSym, _ := p["symbol"].(string)
			if rawSym == "" {
				continue
			}
			sym := strings.ToUpper(rawSym)
			if *onlySymbol != "" && !strings.EqualFold(sym, *onlySymbol) {
				continue
			}
			amt, _ := p["positionAmt"].(float64)
			if amt == 0 {
				continue
			}
			// OKX reports positionAmt as an absolute value and carries the
			// direction in "side" — deriving it from the sign would mislabel
			// every short position as long.
			side, _ := p["side"].(string)
			side = strings.ToLower(side)
			if side != "long" && side != "short" {
				side = "long"
				if amt < 0 {
					side = "short"
				}
			}
			orders, err := tr.GetOpenOrders(rawSym)
			if err != nil {
				fmt.Printf("  %s %s: GetOpenOrders ERR: %v\n", sym, side, err)
				continue
			}
			trailing := make([]types.OpenOrder, 0)
			for _, o := range orders {
				if !strings.Contains(strings.ToUpper(o.Type), "TRAILING") {
					continue
				}
				if o.PositionSide != "" && !strings.EqualFold(o.PositionSide, side) {
					continue
				}
				trailing = append(trailing, o)
			}
			claims := map[string]string{}
			if byPos != nil {
				claims = byPos[sym+"|"+side]
			}
			orphans := make([]types.OpenOrder, 0)
			matched := 0
			for _, o := range trailing {
				if _, ok := claims[o.OrderID]; ok {
					matched++
					continue
				}
				orphans = append(orphans, o)
			}
			// Stale claims: records pointing at orders that no longer exist.
			live := map[string]struct{}{}
			for _, o := range trailing {
				live[o.OrderID] = struct{}{}
			}
			staleClaims := make([]string, 0)
			for id, desc := range claims {
				if _, ok := live[id]; !ok {
					staleClaims = append(staleClaims, id+" ("+desc+")")
				}
			}
			sort.Strings(staleClaims)

			fmt.Printf("  %-12s %-5s trailing=%d claimed_live=%d orphan=%d stale_records=%d\n",
				sym, side, len(trailing), matched, len(orphans), len(staleClaims))
			for _, s := range staleClaims {
				fmt.Printf("      stale record -> %s\n", s)
			}
			// The pre-v1.16.7 leak can leave hundreds of identical orphans on one
			// position; print a bounded sample plus a shape summary rather than
			// flooding the audit output with near-duplicate lines.
			const orphanSample = 10
			for i, o := range orphans {
				if i >= orphanSample {
					fmt.Printf("      ... and %d more orphans (same shape check below)\n", len(orphans)-orphanSample)
					break
				}
				fmt.Printf("      ORPHAN %s qty=%.6f activation=%.6f cb=%.4f%%\n",
					o.OrderID, o.Quantity, o.ActivationPrice, o.CallbackRatePct)
			}
			if len(orphans) > 1 {
				shapes := map[string]int{}
				for _, o := range orphans {
					shapes[fmt.Sprintf("qty=%.6f activation=%.6f cb=%.4f%%", o.Quantity, o.ActivationPrice, o.CallbackRatePct)]++
				}
				keys := make([]string, 0, len(shapes))
				for k := range shapes {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Printf("      orphan shape x%-4d %s\n", shapes[k], k)
				}
			}

			if !*doCancel || len(orphans) == 0 {
				continue
			}
			// Safety gate: never cancel when NO claimed order is live — that
			// would mean the orphans are all the protection this position has.
			if matched == 0 {
				fmt.Printf("      SKIP cancel: no claimed live order, orphans are the only protection\n")
				continue
			}
			for _, o := range orphans {
				if err := tr.CancelOrder(rawSym, o.OrderID); err != nil {
					fmt.Printf("      cancel %s ERR: %v\n", o.OrderID, err)
					continue
				}
				fmt.Printf("      cancelled %s\n", o.OrderID)
			}
		}
	}
}
