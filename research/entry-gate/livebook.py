"""Hypothesis generation from the live book; validation happens on 20 quarters.

The event-table studies all assume the candidate set is "every bar". The live
candidate set is whatever the AI proposed, and that is the population a gate
actually filters. So: find what separates winners from losers HERE, then test the
same condition on the long history. Live data alone (n=831, weeks) has no power to
confirm anything — it can only propose.
"""
import json, numpy as np, pandas as pd
from scipy import stats

d = pd.DataFrame(json.load(open("/tmp/rumers/live_all.json")))
d["et_dt"] = pd.to_datetime(d.et, unit="ms", utc=True)
print(f"live trades={len(d)}  {d.et_dt.min().date()} -> {d.et_dt.max().date()}")
print(f"overall: mean={d.bps.mean():+.2f}bps median={d.bps.median():+.2f} win={100*(d.bps>0).mean():.1f}%")

print("\n=== by side ===")
print(d.groupby("side").bps.agg(["count","mean","median",lambda s:100*(s>0).mean()])
       .rename(columns={"<lambda_0>":"win%"}).round(2).to_string())

print("\n=== by regime (as labelled at decision time) ===")
print(d.groupby("regime").bps.agg(["count","mean","median",lambda s:100*(s>0).mean()])
       .rename(columns={"<lambda_0>":"win%"}).round(2).sort_values("mean").to_string())

print("\n=== by setup ===")
print(d.groupby("setup").bps.agg(["count","mean","median",lambda s:100*(s>0).mean()])
       .rename(columns={"<lambda_0>":"win%"}).round(2).sort_values("mean").to_string())

print("\n=== by exit reason ===")
print(d.groupby("reason").bps.agg(["count","mean","median"]).round(2).sort_values("mean").to_string())

print("\n=== side x regime interaction (mean bps / n) ===")
piv = d.pivot_table(index="regime", columns="side", values="bps", aggfunc="mean").round(1)
cnt = d.pivot_table(index="regime", columns="side", values="bps", aggfunc="count")
print(piv.to_string()); print("counts:"); print(cnt.to_string())

print("\n=== confidence deciles ===")
d["cd"] = pd.qcut(d.conf, 4, labels=False, duplicates="drop")
print(d.groupby("cd").agg(n=("bps","size"), conf=("conf","mean"), mean=("bps","mean"),
                          win=("bps", lambda s:100*(s>0).mean())).round(2).to_string())
