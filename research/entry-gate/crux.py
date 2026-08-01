"""Does the cost-multiple K add anything the existing netRR gate does not?

The live gate enforces netRR >= min. Since netRR = grossRR - 1/K, a netRR floor
already penalises small K. So the recommendation hinges on ONE question:

    within a band of (nearly) constant netRR, does K still separate outcomes?

If yes -> K is a genuinely separate lever and worth adding.
If no  -> the honest answer is "retune the existing netRR floor", and I should say
          so rather than dress up a redundant gate as a finding.
"""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = pd.read_parquet("/root/projects/nofxmax/research/entry-gate/cache/multi.parquet")
print(f"rows={len(ev)} quarters={ev.q.nunique()}")

# Condition on netRR bands, then look at K terciles inside each band.
ev["nrr_band"] = pd.cut(ev.netRR, [-9, 0.5, 1.0, 1.5, 2.0, 3.0, 99],
                        labels=["<0.5","0.5-1","1-1.5","1.5-2","2-3",">3"])
ev["k3"] = pd.qcut(ev.K, 3, labels=["K_lo","K_mid","K_hi"])

print("\n=== coin_R by netRR band x K tercile (quarter-equal-weighted) ===")
piv = (ev.groupby(["q","nrr_band","k3"], observed=True).coin_R.mean()
         .groupby(["nrr_band","k3"]).mean().unstack())
print(piv.round(4).to_string())

print("\n=== within-band K_hi minus K_lo, tested across quarters ===")
rows=[]
for band in ev.nrr_band.cat.categories:
    s = ev[ev.nrr_band == band]
    if len(s) < 5000:
        continue
    p = s.groupby(["q","k3"], observed=True).coin_R.mean().unstack()
    if "K_lo" not in p or "K_hi" not in p:
        continue
    d = (p["K_hi"] - p["K_lo"]).dropna()
    if len(d) < 8:
        continue
    fr, pv, npos, n = E.sign_test(d)
    rows.append(dict(band=band, n=len(s), delta=d.mean(), frac_pos=fr,
                     binom_p=pv, t_p=stats.ttest_1samp(d,0).pvalue, nq=n))
print(pd.DataFrame(rows).set_index("band").round(4).to_string())

# Also the reverse: within K bands, does netRR still matter? (sanity — it should)
print("\n=== reverse check: within K tercile, netRR band effect (should be present) ===")
p2 = (ev.groupby(["q","k3","nrr_band"], observed=True).coin_R.mean()
        .groupby(["k3","nrr_band"]).mean().unstack())
print(p2.round(4).to_string())
