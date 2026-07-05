package binance

// Exported thin wrappers for out-of-package read-only probes/tools. These expose
// the symbol mapping without widening the production API surface used by callers.

// ExecSymbolForTest maps an internal USDT symbol to the exec symbol per the
// trader's current preferUSDC setting.
func (t *FuturesTrader) ExecSymbolForTest(internal string) string { return t.toExecSymbol(internal) }

// InternalSymbolForTest maps an exec symbol back to the internal USDT symbol.
func InternalSymbolForTest(exec string) string { return toInternalSymbol(exec) }
