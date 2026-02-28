package quota

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Basic get / set / invalidate
// ---------------------------------------------------------------------------

// TestQuotaCacheGetMiss verifies that getting from an empty cache returns nil.
func TestQuotaCacheGetMiss(t *testing.T) {
	c := NewQuotaCache(time.Second)
	if e := c.get("nonexistent"); e != nil {
		t.Errorf("get() on empty cache = %v, want nil", e)
	}
}

// TestQuotaCacheSetGet verifies that set values can be retrieved with correct fields.
func TestQuotaCacheSetGet(t *testing.T) {
	c := NewQuotaCache(time.Minute)
	c.set("user1", 100, 3000)

	e := c.get("user1")
	if e == nil {
		t.Fatal("get() after set should not return nil")
	}
	if e.dailyUsed != 100 {
		t.Errorf("dailyUsed = %d, want 100", e.dailyUsed)
	}
	if e.monthlyUsed != 3000 {
		t.Errorf("monthlyUsed = %d, want 3000", e.monthlyUsed)
	}
	if e.expiresAt.IsZero() {
		t.Error("expiresAt should not be zero after set")
	}
}

// TestQuotaCacheOverwrite verifies that calling set twice overwrites the first entry.
func TestQuotaCacheOverwrite(t *testing.T) {
	c := NewQuotaCache(time.Minute)
	c.set("user2", 100, 1000)
	c.set("user2", 200, 2000)

	e := c.get("user2")
	if e == nil {
		t.Fatal("entry should exist after overwrite")
	}
	if e.dailyUsed != 200 {
		t.Errorf("dailyUsed = %d, want 200 after overwrite", e.dailyUsed)
	}
	if e.monthlyUsed != 2000 {
		t.Errorf("monthlyUsed = %d, want 2000 after overwrite", e.monthlyUsed)
	}
}

// TestQuotaCacheInvalidate verifies that invalidate removes the entry.
func TestQuotaCacheInvalidate(t *testing.T) {
	c := NewQuotaCache(time.Minute)
	c.set("user3", 200, 800)

	if c.get("user3") == nil {
		t.Fatal("entry should be present before invalidate")
	}

	c.invalidate("user3")

	if c.get("user3") != nil {
		t.Error("entry should be nil after invalidate")
	}
}

// TestQuotaCacheInvalidateNonExistent verifies that invalidating a missing key is safe.
func TestQuotaCacheInvalidateNonExistent(t *testing.T) {
	c := NewQuotaCache(time.Minute)
	c.invalidate("doesnotexist") // should not panic
}

// ---------------------------------------------------------------------------
// TTL expiry
// ---------------------------------------------------------------------------

// TestQuotaCacheExpiry verifies that an entry is not returned after its TTL has elapsed.
// The cache uses lazy deletion: the entry is removed on the next get() call.
func TestQuotaCacheExpiry(t *testing.T) {
	c := NewQuotaCache(20 * time.Millisecond)
	c.set("user4", 50, 500)

	if c.get("user4") == nil {
		t.Fatal("entry should be present before TTL expires")
	}

	time.Sleep(50 * time.Millisecond)

	if c.get("user4") != nil {
		t.Error("entry should be nil after TTL has elapsed (lazy deletion)")
	}
}

// TestQuotaCacheExpiryLazyDelete verifies that the expired entry is removed from
// the internal sync.Map during lazy deletion (so it doesn't linger in memory).
func TestQuotaCacheExpiryLazyDelete(t *testing.T) {
	c := NewQuotaCache(10 * time.Millisecond)
	c.set("user5", 10, 100)

	time.Sleep(30 * time.Millisecond)

	c.get("user5") // triggers lazy deletion

	// Verify the entry is no longer stored in the map.
	if _, ok := c.m.Load("user5"); ok {
		t.Error("expired entry should have been deleted from sync.Map by lazy deletion")
	}
}

// ---------------------------------------------------------------------------
// Default TTL
// ---------------------------------------------------------------------------

// TestQuotaCacheZeroTTLDefault verifies that NewQuotaCache(0) defaults to 60 seconds.
func TestQuotaCacheZeroTTLDefault(t *testing.T) {
	c := NewQuotaCache(0)
	if c.ttl != 60*time.Second {
		t.Errorf("default ttl = %v, want 60s", c.ttl)
	}
}

// TestQuotaCacheExpiryWithDefaultTTL verifies that an entry set on a
// zero-TTL cache actually uses the 60s default and is not immediately expired.
func TestQuotaCacheExpiryWithDefaultTTL(t *testing.T) {
	c := NewQuotaCache(0) // should default to 60s
	c.set("user6", 1, 1)

	if c.get("user6") == nil {
		t.Error("entry should be present immediately after set (default 60s TTL)")
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

// TestQuotaCacheConcurrent verifies that concurrent set/get/invalidate operations
// are race-free (run with -race to detect data races).
func TestQuotaCacheConcurrent(t *testing.T) {
	c := NewQuotaCache(time.Minute)

	const goroutines = 30
	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			c.set("shared-user", int64(i), int64(i*10))
		}(i)
		go func() {
			defer wg.Done()
			c.get("shared-user")
		}()
		go func() {
			defer wg.Done()
			c.invalidate("shared-user")
		}()
	}
	wg.Wait()
}

// TestQuotaCacheMultipleUsers verifies that independent users do not interfere.
func TestQuotaCacheMultipleUsers(t *testing.T) {
	c := NewQuotaCache(time.Minute)
	c.set("alice", 100, 1000)
	c.set("bob", 200, 2000)
	c.set("carol", 300, 3000)

	ea := c.get("alice")
	eb := c.get("bob")
	ec := c.get("carol")

	if ea == nil || ea.dailyUsed != 100 {
		t.Errorf("alice dailyUsed = %v, want 100", ea)
	}
	if eb == nil || eb.dailyUsed != 200 {
		t.Errorf("bob dailyUsed = %v, want 200", eb)
	}
	if ec == nil || ec.dailyUsed != 300 {
		t.Errorf("carol dailyUsed = %v, want 300", ec)
	}
}
