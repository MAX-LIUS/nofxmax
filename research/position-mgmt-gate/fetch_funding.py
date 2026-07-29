"""拉取资金费率(月档)。8h 一条,是明确不在单币价格路径里的量。"""
import io,os,sys,zipfile,urllib.request,urllib.error,time
OUT="/root/.claude/jobs/5cbb3cf4/tmp/cdeval/funding"
KL="/root/.claude/jobs/5cbb3cf4/tmp/cdeval/klines"
U="https://data.binance.vision/data/futures/um/monthly/fundingRate/{s}/{s}-fundingRate-{m}.zip"
syms=sorted({f.split('-',1)[0] for f in os.listdir(KL) if f.endswith('.csv')})
months=[f"{y}-{m:02d}" for y in range(2022,2027) for m in range(1,13)]
months=[m for m in months if m<="2026-07"]
os.makedirs(OUT,exist_ok=True)
ok=miss=skip=0
for s in syms:
    p=f"{OUT}/{s}.csv"
    if os.path.exists(p): skip+=1; continue
    rows=[]
    for m in months:
        try:
            b=urllib.request.urlopen(U.format(s=s,m=m),timeout=60).read()
            z=zipfile.ZipFile(io.BytesIO(b))
            for ln in z.read(z.namelist()[0]).decode().splitlines()[1:]:
                if ln.strip(): rows.append(ln)
            ok+=1
        except urllib.error.HTTPError as e:
            if e.code==404: miss+=1
            else: print(f"  {s} {m} HTTP {e.code}",flush=True)
        except Exception as e:
            print(f"  {s} {m} {type(e).__name__}",flush=True)
        time.sleep(0.05)
    if rows:
        open(p,"w").write("calc_time,funding_interval_hours,last_funding_rate\n"+"\n".join(rows)+"\n")
    print(f"{s}: {len(rows)} rows",flush=True)
print(f"DONE ok={ok} miss404={miss} skip={skip}")
