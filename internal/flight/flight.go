// Package flight collapses concurrent calls that share a key into one execution (request coalescing).
package flight

import "sync"

type call struct {
	wg  sync.WaitGroup
	val any
	err error
}

// Group deduplicates in flight work by key. The zero value is ready to use.
type Group struct {
	mu sync.Mutex
	m  map[string]*call
}

// Do runs fn once per key at a time. Callers that arrive while it runs wait and receive the same result.
// leader is true for the caller that actually executed fn.
func (g *Group) Do(key string, fn func() (any, error)) (val any, leader bool, err error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, false, c.err
	}
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
		c.wg.Done()
	}()
	c.val, c.err = fn()
	return c.val, true, c.err
}
