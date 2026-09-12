#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""诊断并读取 PowerShell 重定向日志的真实编码。

用法：
    python tools/logenc.py info <logfile>     # 打印头部字节 + 各编码尝试结果
    python tools/logenc.py tail <logfile> [n] # 按识别出的编码打印尾部 n 行
"""
import sys
import io

sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding="utf-8", errors="replace")

CANDIDATES = ["utf-8", "utf-16-le", "utf-16-be", "gbk"]


def decode_report(raw: bytes):
    """返回 (编码名, 文本, 说明) 列表。"""
    rows = []
    for enc in CANDIDATES:
        try:
            txt = raw.decode(enc)
            note = "严格通过"
        except UnicodeDecodeError as e:
            txt = raw.decode(enc, "replace")
            note = f"替换 {txt.count(chr(0xFFFD))} 处"
        nulls = txt.count("\x00")
        rows.append((enc, txt, note, nulls))
    return rows


def pick(raw: bytes) -> str:
    # 1) BOM 优先
    if raw.startswith(b"\xff\xfe"):
        return raw.decode("utf-16")
    if raw.startswith(b"\xfe\xff"):
        return raw.decode("utf-16")
    if raw.startswith(b"\xef\xbb\xbf"):
        return raw.decode("utf-8-sig")
    # 2) 无 BOM：字节里 NUL 多 → UTF-16LE
    if raw[:600].count(0) > 60:
        return raw.decode("utf-16-le", "replace")
    # 3) 严格 UTF-8
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError:
        pass
    # 4) 兜底 GBK
    return raw.decode("gbk", "replace")


def main() -> None:
    if len(sys.argv) < 3:
        print(__doc__)
        return
    mode, path = sys.argv[1], sys.argv[2]
    raw = open(path, "rb").read()
    if mode == "info":
        print(f"文件 {path} 共 {len(raw)} 字节")
        print("头部 48 字节 hex:", raw[:48].hex(" "))
        print("头部 48 字节 ascii:", "".join(chr(b) if 32 <= b < 127 else "." for b in raw[:48]))
        for enc, txt, note, nulls in decode_report(raw):
            sample = txt[:40].replace("\n", "\\n").replace("\x00", "·")
            print(f"  {enc:10s} {note:12s} NUL={nulls:5d}  样本: {sample}")
        print("选定:", "见下")
        print(pick(raw)[:200].replace("\n", "\\n"))
        return
    if mode == "tail":
        n = int(sys.argv[3]) if len(sys.argv) > 3 else 0
        lines = pick(raw).splitlines()
        print("\n".join(lines[-n:] if n else lines))
        return
    print(__doc__)


if __name__ == "__main__":
    main()
