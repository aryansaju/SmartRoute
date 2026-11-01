"""Python port of the Go guard (internal/cache/guard.go). Keep the two in sync: both are tested against the
same table of cases (see test_sweep.py and internal/cache/guard_test.go)."""
import re

NUMBER = re.compile(r"\d+(?:[.,]\d+)*")
WORD = re.compile(r"[^\W\d_](?:[^\W\d_]|['’])*")
NEGATIONS = {"not", "no", "never", "without", "cannot", "none", "neither", "nor", "nothing", "nobody"}
OPPOSITES = [("enable", "disable"), ("increase", "decrease"), ("add", "remove"), ("before", "after"),
             ("true", "false"), ("start", "stop"), ("open", "close"), ("allow", "block"),
             ("install", "uninstall"), ("encrypt", "decrypt"), ("buy", "sell"), ("win", "lose"),
             ("lock", "unlock"), ("on", "off"), ("include", "exclude"), ("accept", "reject"),
             ("import", "export")]
PRONOUN_I = {"i", "i'm", "i've", "i'll", "i'd"}


def _numbers(s):
    return sorted(n.replace(",", "") for n in NUMBER.findall(s))


def _tokens(s):
    return [w.replace("’", "'").lower() for w in WORD.findall(s)]


def _has_negation(toks):
    return any(t in NEGATIONS or t.endswith("n't") for t in toks)


def _capitalised(s):
    out = []
    for i, w in enumerate(WORD.findall(s)):
        if i == 0:
            continue
        if w[0].isupper() and w.lower() not in PRONOUN_I:
            out.append(w.lower())
    return sorted(out)


def guard_allows(new_prompt: str, cached_prompt: str):
    """Return (allowed, reason) exactly like the Go implementation."""
    if _numbers(new_prompt) != _numbers(cached_prompt):
        return False, "number_mismatch"
    ta, tb = _tokens(new_prompt), _tokens(cached_prompt)
    if _has_negation(ta) != _has_negation(tb):
        return False, "negation_mismatch"
    sa, sb = set(ta), set(tb)
    for x, y in OPPOSITES:
        if (x in sa and y not in sa and y in sb and x not in sb) or (y in sa and x not in sa and x in sb and y not in sb):
            return False, "antonym_mismatch"
    if _capitalised(new_prompt) != _capitalised(cached_prompt):
        return False, "entity_mismatch"
    return True, ""
