# -*- coding: utf-8 -*-
"""trainer/train.py 里「纯函数」的单元测试。

为什么这些值得单独测：训练器最危险的失败模式不是崩溃，而是**静默错标**——
斜向示范被塞成正向、某个类 0 样本、验证集被随机切分切空。这些都不会报错，
只会产出一个「数字看着还行、行为完全不对」的模型。这里把每条防线钉死。

运行（不需要 torch，train.py 只在函数内部 import torch）：
    PY=C:/Users/ay/.workbuddy/binaries/python/envs/default/Scripts/python.exe
    $PY -m unittest trainer.test_train -v
"""

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from trainer import train  # noqa: E402


class TestParseDir(unittest.TestCase):
    def test_八个规范方向(self):
        for d in train.DIRS:
            self.assertEqual(train.parse_dir(d), d)
            self.assertEqual(train.parse_dir(d.upper()), d)
            self.assertEqual(train.parse_dir("  " + d + " "), d)

    def test_分隔符变体(self):
        self.assertEqual(train.parse_dir("up-right"), "up_right")
        self.assertEqual(train.parse_dir("up right"), "up_right")
        self.assertEqual(train.parse_dir("up/right"), "up_right")

    def test_拒绝非方向(self):
        for bad in ("", "upx", "north", "走", "ACTION", "dur=1200"):
            self.assertIsNone(train.parse_dir(bad), f"{bad!r} 不该被当成方向")

    def test_不做子串匹配(self):
        """核心回归：up_right 绝不能被 'up' 前缀吃掉。"""
        self.assertEqual(train.parse_dir("up_right"), "up_right")
        self.assertEqual(train.parse_dir("up_left"), "up_left")
        self.assertEqual(train.parse_dir("down_right"), "down_right")
        self.assertNotEqual(train.parse_dir("up_right"), "up")


class TestDirOf(unittest.TestCase):
    def test_正向(self):
        self.assertEqual(train.dir_of(0.0, -0.08), "up")
        self.assertEqual(train.dir_of(0.0, 0.08), "down")
        self.assertEqual(train.dir_of(-0.09, 0.0), "left")
        self.assertEqual(train.dir_of(0.09, 0.0), "right")

    def test_斜向(self):
        self.assertEqual(train.dir_of(0.06, -0.06), "up_right")
        self.assertEqual(train.dir_of(-0.06, -0.06), "up_left")
        self.assertEqual(train.dir_of(0.06, 0.06), "down_right")
        self.assertEqual(train.dir_of(-0.06, 0.06), "down_left")

    def test_抖动不制造斜向(self):
        """推杆抖动（小分量 < 大分量 40%）不该把『前』判成『右前』。"""
        self.assertEqual(train.dir_of(0.02, -0.09), "up")
        self.assertEqual(train.dir_of(0.01, 0.09), "down")

    def test_零位移不崩(self):
        self.assertIn(train.dir_of(0.0, 0.0), train.DIRS)


class TestMapAction(unittest.TestCase):
    def test_move从action字段取斜向(self):
        s = {"kind": "move", "action": "move:up_right/1200ms", "raw": ""}
        self.assertEqual(train.map_action(s)[0], "up_right")

    def test_move从raw字段取斜向(self):
        s = {"kind": "move", "raw": "ACTION MOVE dir=down_right dur=1500"}
        self.assertEqual(train.map_action(s)[0], "down_right")

    def test_move斜向不被塌成正向(self):
        """回归：旧实现在 raw 上做子串匹配，up_right 会被 'up' 先命中。"""
        for raw, want in [
            ("ACTION MOVE dir=up_right dur=1200", "up_right"),
            ("ACTION MOVE dir=up_left dur=1200", "up_left"),
            ("ACTION MOVE dir=down_left dur=1200", "down_left"),
            ("ACTION MOVE dir=down_right dur=1200", "down_right"),
        ]:
            got = train.map_action({"kind": "move", "raw": raw})[0]
            self.assertEqual(got, want, f"{raw} → {got}，期望 {want}")

    def test_move正向仍可识别(self):
        for d in ("up", "down", "left", "right"):
            s = {"kind": "move", "action": f"move:{d}/1000ms", "raw": ""}
            self.assertEqual(train.map_action(s)[0], d)

    def test_move无方向则丢弃(self):
        self.assertIsNone(train.map_action({"kind": "move", "raw": "ACTION MOVE dur=1200"})[0])

    def test_press与key都落press(self):
        self.assertEqual(train.map_action({"kind": "press"})[0], "press")
        self.assertEqual(train.map_action({"kind": "key"})[0], "press")

    def test_tap带坐标(self):
        k, x, y = train.map_action({"kind": "tap", "nx": 0.3, "ny": 0.7})
        self.assertEqual(k, "tap")
        self.assertAlmostEqual(x, 0.3)
        self.assertAlmostEqual(y, 0.7)

    def test_joy用位移方向(self):
        s = {"kind": "joy", "nx": 0.21, "ny": 0.79, "nx2": 0.21, "ny2": 0.71}
        self.assertEqual(train.map_action(s)[0], "up")
        s2 = {"kind": "joy", "nx": 0.21, "ny": 0.79, "nx2": 0.146, "ny2": 0.733}
        self.assertEqual(train.map_action(s2)[0], "up_left")

    def test_未知kind丢弃(self):
        self.assertIsNone(train.map_action({"kind": "teleport"})[0])

    def test_swipe与zoom降级为wait(self):
        self.assertEqual(train.map_action({"kind": "swipe"})[0], "wait")
        self.assertEqual(train.map_action({"kind": "zoom"})[0], "wait")


class TestClassSpace(unittest.TestCase):
    def test_类别数与权重长度一致(self):
        self.assertEqual(len(train.CLS), len(train.CLASS_WEIGHTS))

    def test_方向类就是八个(self):
        self.assertEqual(list(train.DIRS), [
            "up", "down", "left", "right",
            "up_left", "up_right", "down_left", "down_right",
        ])

    def test_无重复类别(self):
        self.assertEqual(len(set(train.CLS)), len(train.CLS))

    def test_前四类顺序不可改(self):
        """顺序即标签；改了会让权重里已训好的下标错位。"""
        self.assertEqual(train.CLS[0], "up")
        self.assertEqual(train.CLS[4], "up_left")
        self.assertEqual(train.CLS[-1], "none")


class TestStratifiedSplit(unittest.TestCase):
    def test_每类都留在训练集(self):
        # 3 类，各 10/2/1 条
        labels = [0] * 10 + [1] * 2 + [2] * 1
        tr, va, warn = train.stratified_split(labels, 0.2, 42)
        self.assertTrue(va, "验证集不该为空")
        for c in (0, 1, 2):
            ids = [i for i in tr if labels[i] == c]
            self.assertTrue(ids, f"类 {train.CLS[c]} 在训练集里没有样本")

    def test_单样本类只进训练集并告警(self):
        labels = [0] * 10 + [2] * 1
        tr, va, warn = train.stratified_split(labels, 0.2, 42)
        self.assertIn(9, tr)
        self.assertNotIn(9, va)
        self.assertTrue(any("只有 1 个样本" in w for w in warn), warn)

    def test_验证集按类出现(self):
        labels = [0] * 10 + [1] * 10 + [8] * 10
        tr, va, _ = train.stratified_split(labels, 0.2, 42)
        val_classes = {labels[i] for i in va}
        self.assertEqual(val_classes, {0, 1, 8}, "验证集应覆盖每个类")
        self.assertEqual(len(tr) + len(va), len(labels))
        self.assertEqual(len(set(tr) & set(va)), 0, "训练/验证不能重叠")

    def test_可复现(self):
        labels = [0] * 10 + [1] * 10
        a = train.stratified_split(labels, 0.2, 42)
        b = train.stratified_split(labels, 0.2, 42)
        self.assertEqual(a, b)


class TestCheckMissingClasses(unittest.TestCase):
    def test_有数据时不报(self):
        counts = [1] * len(train.CLS)
        self.assertEqual(train.check_missing_classes(counts, allow=False), [])

    def test_空类默认拒绝训练(self):
        counts = [1] * len(train.CLS)
        counts[train.CLS.index("left")] = 0
        with self.assertRaises(SystemExit):
            train.check_missing_classes(counts, allow=False)

    def test_允许时返回空类清单(self):
        counts = [1] * len(train.CLS)
        counts[train.CLS.index("left")] = 0
        counts[train.CLS.index("none")] = 0
        got = train.check_missing_classes(counts, allow=True)
        self.assertEqual(sorted(got), ["left", "none"])


class TestPerClassReport(unittest.TestCase):
    def test_召回与预测分布(self):
        # 真值: up,up,tap,tap  预测: up,tap,tap,tap
        truth = [0, 0, 8, 8]
        pred = [0, 8, 8, 8]
        recall, dist = train.per_class_report(truth, pred)
        self.assertAlmostEqual(recall["up"]["recall"], 0.5)
        self.assertAlmostEqual(recall["tap"]["recall"], 1.0)
        self.assertEqual(dist, {"up": 1, "tap": 3})

    def test_无样本的类不进报告(self):
        recall, _ = train.per_class_report([0, 0], [0, 0])
        self.assertEqual(list(recall.keys()), ["up"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
