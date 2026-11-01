import csv
import json
import tempfile
import unittest
from pathlib import Path

import numpy as np

import sweep
from guard import guard_allows

HERE = Path(__file__).parent

# Same table as internal/cache/guard_test.go. If you change one, change the other.
GUARD_CASES = [
    ("How do I reset my password?", "How can I reset my password?", True),
    ("Explain the steps to deploy to AWS", "Explain steps to deploy to AWS", True),
    ("What is the capital of France?", "What is the capital of Germany?", False),
    ("Convert 100 dollars to euros", "Convert 200 dollars to euros", False),
    ("Is 1,000 enough?", "Is 1000 enough?", True),
    ("How do I enable the feature?", "How do I not enable the feature?", False),
    ("Can I do this?", "Can't I do this?", False),
    ("How do I enable dark mode?", "How do I disable dark mode?", False),
    ("Turn on dark mode please", "Turn off dark mode please", False),
    ("Who founded Apple?", "Who founded Apple and Pixar?", False),
]


class GuardParity(unittest.TestCase):
    def test_cases(self):
        for a, b, want in GUARD_CASES:
            self.assertEqual(guard_allows(a, b)[0], want, (a, b))

    def test_reasons(self):
        self.assertEqual(guard_allows("add 5 items", "add 6 items")[1], "number_mismatch")
        self.assertEqual(guard_allows("do it", "do not it")[1], "negation_mismatch")
        self.assertEqual(guard_allows("please enable it", "please disable it")[1], "antonym_mismatch")
        self.assertEqual(guard_allows("visit Paris", "visit Rome")[1], "entity_mismatch")


class MetricTests(unittest.TestCase):
    def test_evaluate(self):
        sims = [0.95, 0.90, 0.85, 0.60]
        labels = [1, 1, 0, 0]
        r = sweep.evaluate(sims, labels, [True] * 4, 0.80)
        self.assertEqual((r["tp"], r["fp"], r["hits"]), (2, 1, 3))
        self.assertAlmostEqual(r["hit_rate"], 1.0)
        self.assertAlmostEqual(r["false_positive_rate"], 0.5)
        self.assertAlmostEqual(r["precision"], 2 / 3)
        self.assertAlmostEqual(r["wrong_answer_share"], 1 / 3)

    def test_guard_mask_removes_hits(self):
        r = sweep.evaluate([0.95, 0.95], [1, 0], [True, False], 0.9)
        self.assertEqual((r["tp"], r["fp"]), (1, 0))

    def test_no_hits_gives_none_precision(self):
        self.assertIsNone(sweep.evaluate([0.1], [1], [True], 0.9)["precision"])

    def test_choose_threshold_respects_fpr_budget(self):
        sims = np.array([0.99, 0.95, 0.90, 0.80, 0.97, 0.92, 0.85, 0.70])
        labels = np.array([1, 1, 1, 1, 0, 0, 0, 0])
        rows = sweep.sweep(sims, labels, np.ones(8, dtype=bool), [0.75, 0.86, 0.93, 0.98])
        self.assertEqual(sweep.choose_threshold(rows, 0.0), 0.98)
        self.assertIsNone(sweep.choose_threshold([dict(r, false_positive_rate=0.5) for r in rows], 0.0))

    def test_split_is_deterministic_and_disjoint(self):
        a, b = sweep.split_indices(20, seed=3), sweep.split_indices(20, seed=3)
        self.assertEqual(a, b)
        self.assertFalse(set(a[0]) & set(a[1]))
        self.assertEqual(len(a[0]) + len(a[1]), 20)

    def test_cosine_rows(self):
        c = sweep.cosine_rows([[1, 0], [0, 1]], [[2, 0], [1, 0]])
        self.assertAlmostEqual(c[0], 1.0)
        self.assertAlmostEqual(c[1], 0.0)


class DatasetAndRun(unittest.TestCase):
    def test_handwritten_pairs_are_well_formed(self):
        pairs = sweep.load_pairs(HERE / "data" / "handwritten_pairs.csv")
        self.assertGreater(len(pairs), 50)
        self.assertEqual({p["label"] for p in pairs}, {0, 1})
        self.assertTrue(all(p["q1"] != p["q2"] for p in pairs))
        cats = {p["category"] for p in pairs}
        self.assertIn("guard_blind", cats)   # cases the guard cannot catch must stay in the set, for honesty

    def test_guard_catches_designed_cases_but_not_blind_ones(self):
        pairs = sweep.load_pairs(HERE / "data" / "handwritten_pairs.csv")
        blocked = {}
        for p in pairs:
            blocked.setdefault(p["category"], []).append(not guard_allows(p["q2"], p["q1"])[0])
        for cat in ("entity_swap", "number_swap", "antonym", "negation"):
            self.assertGreaterEqual(sum(blocked[cat]) / len(blocked[cat]), 0.8, cat)
        self.assertEqual(sum(blocked["paraphrase"]), 0, "the guard must not reject plain paraphrases")
        self.assertLess(sum(blocked["guard_blind"]) / len(blocked["guard_blind"]), 0.2)

    def test_end_to_end_with_offline_backend(self):
        embed_fn, name, semantic = sweep.make_embed_fn("hash", "", "")
        self.assertFalse(semantic)
        pairs = sweep.load_pairs(HERE / "data" / "handwritten_pairs.csv")
        result = sweep.run(pairs, embed_fn, max_fpr=0.05, seed=0)
        self.assertEqual(result["n_tune"] + result["n_test"], len(pairs))
        self.assertIn("with_guard", result["variants"])
        with tempfile.TemporaryDirectory() as tmp:
            result["meta"] = {"backend": name}
            sweep.write_outputs(result, tmp)
            self.assertTrue((Path(tmp) / "results.json").exists())
            self.assertTrue((Path(tmp) / "sweep_test.csv").exists())
            with open(Path(tmp) / "sweep_test.csv", encoding="utf-8") as fh:
                rows = list(csv.DictReader(fh))
            self.assertEqual(len(rows), 2 * len(sweep.DEFAULT_THRESHOLDS))
            json.loads((Path(tmp) / "results.json").read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
