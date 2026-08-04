#!/usr/bin/env python3
"""Invariant tests. The -7.23R mean came from a wrong-signed risk denominator;
these pin the invariants that make such a number impossible."""
import sys
sys.path.insert(0,'.')
from engine import backtest

fail=0
for sym,mult,pat in (("SPYUSDT",3,"wick"),("QQQUSDT",2,"reclaim"),
                     ("NVDAUSDT",1,"wick"),("MSTRUSDT",1,"reclaim")):
    t=backtest(sym,mult,pat,3,6,0.5,2.0,1.0,1.0)
    for d,side,R,reason,held in t:
        # A stop exit can never be better than -1R (plus cost).
        if reason=="stop" and R>-0.9:
            print(f"  FAIL {sym} {d} {side}: stop exit gave R={R:+.3f} (应 <= -1R)"); fail+=1
        # No exit may exceed the configured target by a wide margin.
        if R>2.5:
            print(f"  FAIL {sym} {d} {side}: R={R:+.3f} 超过 2R 目标过多 ({reason})"); fail+=1
        # Nothing may be worse than -1R minus a small slippage allowance,
        # except a gap-through, which must be a trail/close exit not a stop.
        if R<-1.6 and reason=="stop":
            print(f"  FAIL {sym} {d} {side}: stop 却亏 R={R:+.3f} (风险分母可疑)"); fail+=1
print("ENGINE TEST","FAIL" if fail else "PASS",f"({fail} 项)")
sys.exit(1 if fail else 0)
