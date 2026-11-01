package cache

import (
	"container/list"
	"sync"
	"time"

	"github.com/<your-user>/smartroute-core/internal/chat"
)

type lruItem struct {
	key     string
	comp    chat.Completion
	expires time.Time
}

// LRU is a small in process cache (the L1 layer) with a per entry expiry.
type LRU struct {
	mu    sync.Mutex
	cap   int
	ll    *list.List
	items map[string]*list.Element
	now   func() time.Time
}

// NewLRU creates a cache holding at most capacity entries. A capacity below 1 disables it.
func NewLRU(capacity int) *LRU {
	return &LRU{cap: capacity, ll: list.New(), items: make(map[string]*list.Element), now: time.Now}
}

// Get returns a live entry and marks it recently used.
func (l *LRU) Get(key string) (chat.Completion, bool) {
	if l.cap < 1 {
		return chat.Completion{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.items[key]
	if !ok {
		return chat.Completion{}, false
	}
	it := el.Value.(*lruItem)
	if !it.expires.After(l.now()) {
		l.ll.Remove(el)
		delete(l.items, key)
		return chat.Completion{}, false
	}
	l.ll.MoveToFront(el)
	return it.comp, true
}

// Add inserts or refreshes an entry that lives for ttl.
func (l *LRU) Add(key string, comp chat.Completion, ttl time.Duration) {
	if l.cap < 1 || ttl <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	exp := l.now().Add(ttl)
	if el, ok := l.items[key]; ok {
		it := el.Value.(*lruItem)
		it.comp, it.expires = comp, exp
		l.ll.MoveToFront(el)
		return
	}
	l.items[key] = l.ll.PushFront(&lruItem{key: key, comp: comp, expires: exp})
	for l.ll.Len() > l.cap {
		oldest := l.ll.Back()
		l.ll.Remove(oldest)
		delete(l.items, oldest.Value.(*lruItem).key)
	}
}

// Len returns the number of entries currently held.
func (l *LRU) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ll.Len()
}
