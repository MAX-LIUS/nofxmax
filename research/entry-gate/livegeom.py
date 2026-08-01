"""Does the wider-stop geometry finding hold on the ACTUAL live book?

The 20-quarter sweep says net R improves as the stop widens, mostly by paying
less friction per R. The live book is the population that matters. So for each
live trade, reconstruct from klines:
  - the ATR at entry
  - how deep price went ADVERSE, in ATR units, before it recovered
  - whether a wider stop would have converted the loss into a win

Live data cannot CONFIRM anything (n=831, few weeks). It can only say whether the
history-validated effect is even present here, or contradicted.
"""
import json, numpy as np, pandas as pd
import bars

d = pd.DataFrame(json.load(open("/tmp/rumers/live_all.json")))
d = d[(d.entry > 0) & d.xt.notna() & (d.xt > d.et)].copy()
print(f"live trades usable={len(d)}")

rows = []
miss = 0
for sym, g in d.groupby("sym"):
    f = bars.load_symbol(sym)
    if f is None or len(f) < 200:
        miss += len(g)
        continue
    ts = f.index.view("int64") // 10**6   # ms since epoch
    o = f["open"].to_numpy(); h = f["high"].to_numpy(); l = f["low"].to_numpy()
    c = f["close"].to_numpy()
    atr = bars.wilder_atr(h, l, c, 14)
    for _, r in g.iterrows():
        i = int(np.searchsorted(ts, r.et, side="right")) - 1
        j = int(np.searchsorted(ts, r.xt, side="right")) - 1
        if i < 20 or j <= i or j >= len(ts):
            miss += 1
            continue
        a = atr[i]
        if not np.isfinite(a) or a <= 0:
            miss += 1
            continue
        seg_h = h[i:j+1]; seg_l = l[i:j+1]
        if r.side.upper() == "LONG":
            adv = (r.entry - seg_l.min()) / a       # deepest adverse, ATR units
            fav = (seg_h.max() - r.entry) / a
        else:
            adv = (seg_h.max() - r.entry) / a
            fav = (r.entry - seg_l.min()) / a
        rows.append(dict(sym=sym, side=r.side, reason=r.reason, bps=r.bps,
                         conf=r.conf, regime=r.regime,
                         adv_atr=adv, fav_atr=fav,
                         atr_pct=100.0 * a / r.entry,
                         hold_bars=j - i))

e = pd.DataFrame(rows)
print(f"reconstructed={len(e)}  unmatched={miss}")

print("\n=== adverse excursion in ATR units, by exit reason ===")
t = e.groupby("reason").agg(
    n=("bps", "size"), mean_bps=("bps", "mean"),
    adv_atr=("adv_atr", "mean"), fav_atr=("fav_atr", "mean"),
    atr_pct=("atr_pct", "mean"), hold=("hold_bars", "mean"))
print(t.round(3).sort_values("mean_bps").to_string())

# The core question: among LOSERS, how far adverse did they go before exit, and
# how far favourable did they EVER go? If fav_atr is large on losers, a wider
# stop / earlier arm would have banked them.
loss = e[e.bps < 0]
win = e[e.bps >= 0]
print(f"\nlosers n={len(loss)} mean={loss.bps.mean():+.1f}bps"
      f"  adv={loss.adv_atr.mean():.2f}ATR  fav={loss.fav_atr.mean():.2f}ATR")
print(f"winners n={len(win)} mean={win.bps.mean():+.1f}bps"
      f"  adv={win.adv_atr.mean():.2f}ATR  fav={win.fav_atr.mean():.2f}ATR")

print("\n=== how many losers ever showed a real favourable excursion first? ===")
for thr in (0.3, 0.5, 0.8, 1.0, 1.5):
    m = loss.fav_atr >= thr
    print(f"  fav >= {thr:.1f} ATR : {m.sum():4d}/{len(loss)} "
          f"({100*m.mean():5.1f}%)  their mean={loss.bps[m].mean():+8.1f}bps")

print("\n=== live ATR-unit stop depth: where do the losses actually sit? ===")
q = loss.adv_atr.quantile([.1,.25,.5,.75,.9]).round(2)
print("  adverse-excursion quantiles on losers:", q.to_dict())
print("  (if the mass sits below ~1.5 ATR, live stops are in the steep part")
print("   of the friction curve and widening them is on-target)")

print("\n=== stop-outs only ===")
so = e[e.reason.astype(str).str.contains("sl|stop", case=False, na=False)]
if len(so):
    print(f"  n={len(so)} mean={so.bps.mean():+.1f}bps"
          f" adv={so.adv_atr.mean():.2f}ATR fav={so.fav_atr.mean():.2f}ATR")
    print("  adv quantiles:", so.adv_atr.quantile([.25,.5,.75]).round(2).to_dict())
    print("  fav quantiles:", so.fav_atr.quantile([.25,.5,.75]).round(2).to_dict())
