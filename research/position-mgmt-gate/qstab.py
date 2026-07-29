"""按季等权复核 sl3.0 / sl4.0 / time48 —— 我自己定的规矩:合并显著必须配逐季检验。"""
import os, numpy as np, pandas as pd
from scipy import stats
import pmgate as P, fastsim as F, tournament as T

NAMES=["sl3.0","sl4.0","time48r0.0","tr0.25a1.0"]
syms=sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith('.pkl'))
lib=T.variant_library(); paths=T.load_paths(syms)
edges=pd.date_range("2022-01-01","2026-07-01",freq="QS",tz="UTC")
want={"baseline":{}}; [want.__setitem__(n,lib[n]) for n in NAMES]
res={n:{"dw":[],"de":[],"ds":[]} for n in NAMES}
qs=[]
for k in range(len(edges)-1):
    w=T.run_window(paths,want,edges[k],edges[k+1])
    b=w["baseline"]
    if b is None or len(b["net"])<300: continue
    bn=b["net"]; bw=(bn>0).mean(); be=bn.mean()*1e4; bs=bn.mean()/bn.std(ddof=1)
    qs.append(str(edges[k].date()))
    for n in NAMES:
        c=w[n]["net"]
        res[n]["dw"].append((c>0).mean()-bw)
        res[n]["de"].append(c.mean()*1e4-be)
        res[n]["ds"].append(c.mean()/c.std(ddof=1)-bs)
print(f"逐季 {len(qs)} 季: {qs[0]} ~ {qs[-1]}\n")
print(f"{'变种':<14}{'Δ胜率均值':>11}{'Δ期望均值':>11}{'正季':>7}{'二项p':>9}{'t p':>9}{'Δ夏普均值':>11}{'正季':>7}{'最差季Δ期望':>13}")
for n in NAMES:
    dw=np.array(res[n]["dw"]); de=np.array(res[n]["de"]); ds=np.array(res[n]["ds"])
    pe=stats.ttest_1samp(de,0.0).pvalue
    bp=stats.binomtest(int((de>0).sum()),len(de),0.5).pvalue
    print(f"{n:<14}{dw.mean():>+11.3f}{de.mean():>+11.2f}{int((de>0).sum()):>4}/{len(de):<2}"
          f"{bp:>9.4f}{pe:>9.4f}{ds.mean():>+11.4f}{int((ds>0).sum()):>4}/{len(ds):<2}{de.min():>+13.1f}")
print("\n判据:Δ期望的'正季数'与二项 p 才是可外推的证据;合并 p=0.0000 但正季 9/18 一律按噪声处理。")
