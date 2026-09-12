#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""稳妥读取 PowerShell 重定向写出的日志（可能是 UTF-16LE / UTF-8 / GBK），
并按需输出尾部若干行。纯排查用工具，可随时删除。

用法：
    python tools/readlog.py <logfile> [tail_lines]
"""
import sys
import io

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")


def load(path: str) -> str:
    raw = open(path, "rb").read()
    # 先按 BOM 判定；helper 自己写的是 UTF-8，PowerShell 重定向可能带 BOM。
    if raw[:2] in (b"\xff\xfe", b"\xfe\xff"):
        return raw.decode("utf-16")
    if raw[:3] == b"\xef\xbb\xbf":
        return raw.decode("utf-8-sig")
    # 无 BOM：优先严格 UTF-8（helper 的原生输出），失败再退 GBK。
    for enc in ("utf-8", "gbk"):
        try:
            return raw.decode(enc)
        except UnicodeDecodeError:
            continue
    # 都有坏字节时，选替换字符少的那个。
    best_txt, best_bad = None, None
    for enc in ("utf-8", "gbk"):
        t = raw.decode(enc, "replace")
        bad = t.count("\ufffd")
        if best_txt is None or bad < best_bad:
            best_txt, best_bad = t, bad
    return best_txt


def main() -> None:
    if len(sys.argv) < 2:
        print(__doc__)
        return
    path = sys.argv[1]
    tail = int(sys.argv[2]) if len(sys.argv) > 2 else 0
    txt = load(path)
    # 日志里每个「[step]」是逻辑一行，可能粘在一起，先按 [ 前插换行
    lines = txt.splitlines()
    if tail:
        lines = lines[-tail:]
    print("\n".join(lines))


if __name__ == "__main__":
    main()
