import math
import unittest

from embedder import DIM, HashBackend, make_backend


def cos(a, b):
    return sum(x * y for x, y in zip(a, b))


class HashBackendTests(unittest.TestCase):
    def setUp(self):
        self.b = HashBackend()

    def test_dimension_and_normalisation(self):
        v = self.b.encode("How do I reset my password?")
        self.assertEqual(len(v), DIM)
        self.assertAlmostEqual(math.sqrt(sum(x * x for x in v)), 1.0, places=6)

    def test_deterministic(self):
        self.assertEqual(self.b.encode("same text"), self.b.encode("same text"))

    def test_overlap_is_closer_than_unrelated(self):
        base = self.b.encode("how do i reset my password")
        near = self.b.encode("how can i reset my password")
        far = self.b.encode("best pizza toppings in naples")
        self.assertGreater(cos(base, near), cos(base, far))

    def test_empty_text_gives_zero_vector(self):
        self.assertTrue(all(x == 0 for x in self.b.encode("   ")))

    def test_batch_matches_single(self):
        texts = ["a b c", "d e f"]
        self.assertEqual(self.b.encode_batch(texts), [self.b.encode(t) for t in texts])

    def test_declares_itself_non_semantic(self):
        self.assertFalse(self.b.is_semantic)


class FactoryTests(unittest.TestCase):
    def test_hash(self):
        self.assertEqual(make_backend("hash").name, "hash")

    def test_unknown(self):
        with self.assertRaises(ValueError):
            make_backend("nope")


if __name__ == "__main__":
    unittest.main()
