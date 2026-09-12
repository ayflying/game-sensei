import json, os, sys
from collections import Counter

OUT = r"D:\git\game-sensei\_inspect_out.txt"
out = open(OUT, "w", encoding="utf-8")

def w(s):
    out.write(str(s) + "\n")

base = r"D:\git\game-sensei\.workbuddy\demos"
run = sys.argv[1] if len(sys.argv) > 1 else "pet_run6"
jp = os.path.join(base, run, "trajectory.jsonl")
raw = open(jp, "rb").read()
w(f"run={run}")
w(f"jsonl bytes: {len(raw)} head: {raw[:8]!r}")
txt = None
for enc in ("utf-8", "utf-16-le", "utf-16", "gbk"):
    try:
        txt = raw.decode(enc)
        w(f"decoded with {enc}")
        break
    except Exception:
        pass
lines = [l for l in txt.splitlines() if l.strip()]
w(f"lines: {len(lines)}")
w("RAW FIRST: " + lines[0][:800])
w("RAW LAST : " + lines[-1][:800])
def jload(s):
    return json.loads(s)
o0 = oN = None
for l in lines:
    try:
        o0 = jload(l); break
    except Exception as e:
        w("parse fail first-attempt: " + repr(e))
        break
for l in reversed(lines):
    try:
        oN = jload(l); break
    except Exception:
        continue
w("keys: " + str(list(o0.keys())) if o0 else "no valid json")
w("FIRST: " + json.dumps(o0, ensure_ascii=False)[:800])
w("LAST : " + (json.dumps(oN, ensure_ascii=False)[:800] if oN else "NONE (last line incomplete)"))

def gv(o, *ks):
    for k in ks:
        if k in o:
            return o[k]
    return None

w(f"ts first={gv(o0,'t','time','ts','timestamp')} last={gv(oN,'t','time','ts','timestamp')}")

c = Counter()
img_mismatch = 0
for l in lines:
    try:
        o = json.loads(l)
        a = o.get("action")
        if isinstance(a, dict):
            c[str(a.get("kind") or a.get("type") or list(a.keys()))] += 1
        else:
            c[str(a) if a is not None else (o.get("kind") or "?")] += 1
    except Exception:
        c["ERR"] += 1
w("ACTIONS: " + json.dumps(dict(c), ensure_ascii=False))

# frame count on disk
fr = os.path.join(base, run, "color")
ncol = len(os.listdir(fr)) if os.path.isdir(fr) else -1
w(f"color frames on disk: {ncol}")
out.close()
print("ok")
