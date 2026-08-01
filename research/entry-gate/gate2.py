"""Is the cost gate real, or arithmetic?

Decompose the gate's effect:
    delta_net = delta_gross - delta_drag
`delta_drag` is guaranteed by construction (we selected on it). `delta_gross` is
the market's response. If gross degrades by as much as drag improves, the gate is
worthless. If gross holds, the gate converts a known friction into real edge.

This is the honest version of the test, and it is the one that decides.
"""
import numpy as np, pandas as pd
from scipy import stats
import runits, evalgate as E

ev = runits.load_r()
ev["gross_R"] = ev.coin_R + ev.cost_R
base = ev.groupby("q").agg(net=("coin_R","mean"), gross=("gross_R","mean"), drag=("cost_R","mean"))

print("=== decomposition of the cost gate (per quarter, then averaged) ===")
rows = []
for K in (4, 6, 8, 10, 12, 15, 20):
    keep = ev.r_unit_bps >= K * runits.COST_BPS
    kept = ev[keep].groupby("q").agg(net=("coin_R","mean"), gross=("gross_R","mean"), drag=("cost_R","mean"))
    d = (kept - base).dropna()
    fr_net, p_net, _, _ = E.sign_test(d.net)
    fr_gr, p_gr, _, _ = E.sign_test(d.gross)
    rows.append(dict(K=K, pass_rate=keep.mean(),
                     d_net=d.net.mean(), d_gross=d.gross.mean(), d_drag=d.drag.mean(),
                     net_frac=fr_net, net_p=p_net, gross_frac=fr_gr, gross_p=p_gr))
t = pd.DataFrame(rows).set_index("K")
print(t.round(4).to_string())
print("\nd_net should equal d_gross - d_drag (identity check):")
print((t.d_gross - t.d_drag - t.d_net).abs().max())

print("\n=== interpretation guide ===")
print("d_drag < 0  : gate reduced friction (guaranteed by selection)")
print("d_gross < 0 : market outcomes got WORSE on the kept subset (gate costs us edge)")
print("d_net > 0   : friction saved exceeds edge lost -> gate is worth running")
