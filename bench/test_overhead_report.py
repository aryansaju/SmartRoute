import json
import tempfile
import unittest
from pathlib import Path

import overhead_report as o


class OverheadReport(unittest.TestCase):
    def write(self, tmp, name, metric):
        p = Path(tmp) / name
        p.write_text(json.dumps({"metrics": {"http_req_duration": metric}}))
        return str(p)

    def test_flat_and_nested_formats(self):
        flat = {"med": 500.0, "p(90)": 505.0, "p(95)": 510.0, "p(99)": 520.0, "max": 600.0}
        nested = {"values": {"med": 501.5, "p(90)": 507.0, "p(95)": 512.0, "p(99)": 525.0, "max": 610.0}}
        with tempfile.TemporaryDirectory() as tmp:
            rows = o.report(o.load(self.write(tmp, "d.json", flat)), o.load(self.write(tmp, "g.json", nested)))
        by = {r[0]: r for r in rows}
        self.assertAlmostEqual(by["p50"][3], 1.5)
        self.assertAlmostEqual(by["p99"][3], 5.0)

    def test_missing_stat_is_none(self):
        rows = o.report({"med": 1.0}, {"med": 2.0})
        self.assertIsNone(dict((r[0], r[3]) for r in rows)["p99"])


if __name__ == "__main__":
    unittest.main()
