package cache

import "testing"

func TestGuard(t *testing.T) {
	cases := []struct {
		name, a, b string
		allow      bool
	}{
		{"paraphrase", "How do I reset my password?", "How can I reset my password?", true},
		{"same entities", "Explain the steps to deploy to AWS", "Explain steps to deploy to AWS", true},
		{"different country", "What is the capital of France?", "What is the capital of Germany?", false},
		{"different number", "Convert 100 dollars to euros", "Convert 200 dollars to euros", false},
		{"thousands separator is ignored", "Is 1,000 enough?", "Is 1000 enough?", true},
		{"negation added", "How do I enable the feature?", "How do I not enable the feature?", false},
		{"contraction negation", "Can I do this?", "Can't I do this?", false},
		{"antonym", "How do I enable dark mode?", "How do I disable dark mode?", false},
		{"on off", "Turn on dark mode please", "Turn off dark mode please", false},
		{"extra name", "Who founded Apple?", "Who founded Apple and Pixar?", false},
	}
	for _, c := range cases {
		got, reason := GuardAllows(c.a, c.b)
		if got != c.allow {
			t.Errorf("%s: allow = %v (%s), want %v", c.name, got, reason, c.allow)
		}
	}
}

func TestGuardReasons(t *testing.T) {
	if _, r := GuardAllows("add 5 items", "add 6 items"); r != "number_mismatch" {
		t.Errorf("got %q", r)
	}
	if _, r := GuardAllows("do it", "do not it"); r != "negation_mismatch" {
		t.Errorf("got %q", r)
	}
	if _, r := GuardAllows("please enable it", "please disable it"); r != "antonym_mismatch" {
		t.Errorf("got %q", r)
	}
	if _, r := GuardAllows("visit Paris", "visit Rome"); r != "entity_mismatch" {
		t.Errorf("got %q", r)
	}
}

func TestCosine(t *testing.T) {
	if c := Cosine([]float32{1, 0}, []float32{1, 0}); c < 0.9999 {
		t.Errorf("identical vectors: %v", c)
	}
	if c := Cosine([]float32{1, 0}, []float32{0, 1}); c > 1e-9 || c < -1e-9 {
		t.Errorf("orthogonal vectors: %v", c)
	}
	if Cosine([]float32{1}, []float32{1, 2}) != 0 || Cosine(nil, nil) != 0 || Cosine([]float32{0, 0}, []float32{1, 1}) != 0 {
		t.Error("incomparable vectors must give 0")
	}
}
