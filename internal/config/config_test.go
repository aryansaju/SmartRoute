package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.SemanticThreshold != 0.92 || !c.GuardEnabled || c.CacheTTL != 24*time.Hour {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestOverrides(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{
		"LISTEN_ADDR": ":9", "SEMANTIC_THRESHOLD": "0.8", "CACHE_TTL": "90m", "GUARD_ENABLED": "false",
		"UPSTREAM_BASE_URL": "http://x:1/", "L1_SIZE": "5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":9" || c.SemanticThreshold != 0.8 || c.CacheTTL != 90*time.Minute || c.GuardEnabled || c.L1Size != 5 {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.UpstreamBaseURL != "http://x:1" {
		t.Errorf("trailing slash should be trimmed, got %q", c.UpstreamBaseURL)
	}
}

func TestInvalidValues(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"bad duration":       {"CACHE_TTL": "soon"},
		"bad bool":           {"GUARD_ENABLED": "maybe"},
		"bad int":            {"L1_SIZE": "ten"},
		"threshold too high": {"SEMANTIC_THRESHOLD": "1.5"},
		"threshold zero":     {"SEMANTIC_THRESHOLD": "0"},
		"top k zero":         {"SEMANTIC_TOP_K": "0"},
	} {
		if _, err := LoadFrom(env(m)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
