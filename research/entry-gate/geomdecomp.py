"""Is the wider-stop gain real, or forced by cost_R = cost_bps / (stop_atr * atr)?

Widening the stop mechanically shrinks friction measured in R units. If the whole
delta is that, "wider stop" is just a restatement of the cost-multiple K finding
(already enforced by MinATR14Pct) and carries no new information.

Decomposition: rerun each config with COST_BPS=0 (gross) and with the real cost
(net). If gross delta ~ 0, the effect is pure friction arithmetic -> discard.
"""
import numpy as np, pandas as pd
import bars, events as EV, evalgate as E
import geometry as G

CFGS = [
    (1.5, 0.50, "stop 1.5 (baseline)"),
    (2.0, 0.50, "stop 2.0"),
    (2.5, 0.50, "stop 2.5"),
    (3.0, 0.50, "stop 3.0"),
]


def run():
    syms = EV.all_symbols()
    acc = {(lab, kind): [] for _, _, lab in CFGS for kind in ("gross", "net")}
    for i, s in enumerate(syms):
        f = bars.load_symbol(s)
        if f is None or len(f) < 400:
            continue
        feats = EV.build_features(f)
        atr = feats.pop("_atr")
        n = len(f)
        idx = np.arange(200, n - G.MAX_HOLD - 2, EV.STRIDE * 4)
        if len(idx) == 0:
            continue
        t = f.index.to_numpy()[idx]
        atrp = feats["atr_pct"][idx]
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        for stop_atr, armR, lab in CFGS:
            for kind in ("gross", "net"):
                G.COST_BPS = 0.0 if kind == "gross" else 14.0
                for side in ("LONG", "SHORT"):
                    v = G.sim(f, idx, atr, side, stop_atr, armR, 0.80, 0.30, 1.20)
                    acc[(lab, kind)].append(
                        pd.DataFrame({"t": t, "R": v})[keep])
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    per_q = {}
    rows = []
    for k, lst in acc.items():
        d = pd.concat(lst, ignore_index=True).dropna(subset=["R"])
        d["q"] = pd.PeriodIndex(pd.to_datetime(d.t, utc=True), freq="Q").astype(str)
        q = d.groupby("q").R.mean()
        per_q[k] = q
        rows.append(dict(config=k[0], kind=k[1], qmeanR=q.mean(), n=len(d)))
    tab = pd.DataFrame(rows).pivot(index="config", columns="kind", values="qmeanR")
    print("\n=== quarter-equal-weighted R: gross (no cost) vs net ===")
    print(tab.round(4).to_string())

    print("\n=== delta vs stop-1.5 baseline, split by gross/net ===")
    out = []
    for _, _, lab in CFGS:
        if lab.startswith("stop 1.5"):
            continue
        r = dict(config=lab)
        for kind in ("gross", "net"):
            d = (per_q[(lab, kind)] - per_q[("stop 1.5 (baseline)", kind)]).dropna()
            fr, p, _, _ = E.sign_test(d)
            r[f"{kind}_delta"] = d.mean()
            r[f"{kind}_fracpos"] = fr
            r[f"{kind}_p"] = p
        r["friction_share"] = 1.0 - (r["gross_delta"] / r["net_delta"]) \
            if r["net_delta"] != 0 else np.nan
        out.append(r)
    print(pd.DataFrame(out).set_index("config").round(4).to_string())
    print("\nfriction_share = fraction of the net gain explained purely by paying")
    print("less round-trip cost per R unit. ~1.0 => no new information.")


if __name__ == "__main__":
    run()
