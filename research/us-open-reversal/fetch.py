#!/usr/bin/env python3
"""Download monthly 5m klines for tokenised US-equity/commodity perps.

Why data.binance.vision and not the API: fapi.binance.com returns 451
(geo-blocked from this host); the public archive returns 200. Verified.
Why 5m and not 1m: 1m quadruples size for no gain -- the finest timeframe the
strategy needs is 5m, and 10/15/30m are aggregated from it.
No unzip binary on this box, so zipfile is used directly.
"""
import os, sys, urllib.request, zipfile, io

DEST = os.environ.get("USOPEN_DIR", "/tmp/usopen")
BASE = "https://data.binance.vision/data/futures/um/monthly/klines"
SYMS = ["SPYUSDT","QQQUSDT","NVDAUSDT","TSLAUSDT","AAPLUSDT","MSTRUSDT",
        "COINUSDT","HOODUSDT","GOOGLUSDT","METAUSDT","AMZNUSDT","MSFTUSDT",
        "AMDUSDT","PLTRUSDT","SPCXUSDT","SKHYNIXUSDT","SOXLUSDT","DRAMUSDT",
        "CLUSDT","XAUUSDT","XAGUSDT","CRCLUSDT","OPENUSDT","LMTUSDT",
        "BAUSDT","UBERUSDT"]
MONTHS = ["2026-04","2026-05","2026-06","2026-07"]

def main():
    os.makedirs(DEST, exist_ok=True)
    ok = miss = 0
    for s in SYMS:
        for m in MONTHS:
            out = os.path.join(DEST, f"{s}-5m-{m}.csv")
            if os.path.exists(out):
                ok += 1
                continue
            url = f"{BASE}/{s}/5m/{s}-5m-{m}.zip"
            try:
                with urllib.request.urlopen(url, timeout=90) as r:
                    blob = r.read()
            except Exception as e:
                miss += 1
                print(f"MISS {s} {m}: {e}", file=sys.stderr)
                continue
            try:
                with zipfile.ZipFile(io.BytesIO(blob)) as z:
                    name = z.namelist()[0]
                    with z.open(name) as fh, open(out, "wb") as w:
                        w.write(fh.read())
                ok += 1
                print(f"OK   {s} {m} {len(blob)//1024}KB", file=sys.stderr)
            except Exception as e:
                miss += 1
                print(f"BAD  {s} {m}: {e}", file=sys.stderr)
    print(f"done ok={ok} miss={miss}", file=sys.stderr)

if __name__ == "__main__":
    main()
