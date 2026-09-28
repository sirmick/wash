package team

// LRU is a least-recently-used cache of at most Cap entries.
//
// Get returns the value and true, and makes the key the most recently
// used; a missing key returns "" and false. Put adds or replaces a value and
// makes the key the most recently used; when that makes more than Cap
// entries, the least recently used one is removed. Keys lists the keys from
// most to least recently used. A cache with Cap <= 0 holds nothing.
type LRU struct {
	Cap int
}

// NewLRU returns an empty cache holding at most capacity entries.
func NewLRU(capacity int) *LRU { return &LRU{Cap: capacity} }

func (c *LRU) Get(key string) (string, bool) { return "", false }
func (c *LRU) Put(key, value string)         {}
func (c *LRU) Len() int                      { return 0 }
func (c *LRU) Keys() []string                { return nil }
