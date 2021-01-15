package cache

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
)

type Item struct {
	Object     interface{} `json:"object"`
	Expiration int64       `json:"expiration"`
}

// Returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}

	return time.Now().UnixNano() > item.Expiration
}

const (
	// For use with functions that take an expiration time.
	NoExpiration time.Duration = -1
	// For use with functions that take an expiration time. Equivalent to
	// passing in the same expiration duration as was given to New() or
	// NewFrom() when the cache was created (e.g. 5 minutes.)
	DefaultExpiration time.Duration = 0
)

type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	// global default expiration
	expiration time.Duration
	items      map[string]Item
	mutex      sync.RWMutex
	onEvicted  func(string, interface{})
	janitor    *janitor
}

// Add an item to the cache, replacing any existing item. If the duration is 0
// (DefaultExpiration), the cache's default expiration time is used. If it is -1
// (NoExpiration), the item never expires.
func (c *cache) Set(key string, value interface{}, duration time.Duration) {
	// "Inlining" of set
	var expiration int64
	if duration == DefaultExpiration {
		duration = c.expiration
	}
	if duration > 0 {
		expiration = time.Now().Add(duration).UnixNano()
	}

	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items[key] = Item{
		Object:     value,
		Expiration: expiration,
	}
}

func (c *cache) set(key string, value interface{}, duration time.Duration) {
	var expiration int64
	if duration == DefaultExpiration {
		duration = c.expiration
	}
	if duration > 0 {
		expiration = time.Now().Add(duration).UnixNano()
	}

	c.items[key] = Item{
		Object:     value,
		Expiration: expiration,
	}
}

// Add an item to the cache, replacing any existing item, using the default
// expiration.
func (c *cache) SetDefault(key string, value interface{}) {
	c.Set(key, value, DefaultExpiration)
}

// Add an item to the cache only if an item doesn't already exist for the given
// key, or if the existing item has expired. Returns an error otherwise.
func (c *cache) Add(key string, value interface{}, duration time.Duration) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, found := c.get(key)
	if found {
		return fmt.Errorf("item %s already exists", key)
	}

	c.set(key, value, duration)

	return nil
}

// Set a new value for the cache key only if it already exists, and the existing
// item hasn't expired. Returns an error otherwise.
func (c *cache) Replace(key string, value interface{}, duration time.Duration) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	_, found := c.get(key)
	if !found {
		return fmt.Errorf("item %s doesn't exist", key)
	}

	c.set(key, value, duration)

	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(key string) (interface{}, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	// "Inlining" of get and Expired
	item, found := c.items[key]
	if !found {
		return nil, false
	}
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}

	return item.Object, true
}

// GetWithExpiration returns an item and its expiration time from the cache.
// It returns the item or nil, the expiration time if one is set (if the item
// never expires a zero value for time.Time is returned), and a bool indicating
// whether the key was found.
func (c *cache) GetWithExpiration(key string) (interface{}, time.Time, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	// "Inlining" of get and Expired
	item, found := c.items[key]
	if !found {
		return nil, time.Time{}, false
	}
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, time.Time{}, false
		}
		// Return the item and the expiration time
		return item.Object, time.Unix(0, item.Expiration), true
	}

	// If expiration <= 0 (i.e. no expiration time set) then return the item
	// and a zeroed time.Time
	return item.Object, time.Time{}, true
}

func (c *cache) get(key string) (interface{}, bool) {
	item, found := c.items[key]
	if !found {
		return nil, false
	}
	// "Inlining" of Expired
	if item.Expiration > 0 {
		if time.Now().UnixNano() > item.Expiration {
			return nil, false
		}
	}
	return item.Object, true
}

// Increment an item of type int, int8, int16, int32, int64, uintptr, uint,
// uint8, uint32, or uint64, float32 or float64 by n. Returns an error if the
// item's value is not an integer, if it was not found, or if it is not
// possible to increment it by n. To retrieve the incremented value, use one
// of the specialized methods, e.g. IncrementInt64.
func (c *cache) Increment(key string, n int64) error {
	c.mutex.Lock()
	c.mutex.RUnlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		c.mutex.Unlock()
		return fmt.Errorf("item %s not found", key)
	}

	switch value.Object.(type) {
	case int:
		value.Object = value.Object.(int) + int(n)
	case int8:
		value.Object = value.Object.(int8) + int8(n)
	case int16:
		value.Object = value.Object.(int16) + int16(n)
	case int32:
		value.Object = value.Object.(int32) + int32(n)
	case int64:
		value.Object = value.Object.(int64) + n
	case uint:
		value.Object = value.Object.(uint) + uint(n)
	case uintptr:
		value.Object = value.Object.(uintptr) + uintptr(n)
	case uint8:
		value.Object = value.Object.(uint8) + uint8(n)
	case uint16:
		value.Object = value.Object.(uint16) + uint16(n)
	case uint32:
		value.Object = value.Object.(uint32) + uint32(n)
	case uint64:
		value.Object = value.Object.(uint64) + uint64(n)
	case float32:
		value.Object = value.Object.(float32) + float32(n)
	case float64:
		value.Object = value.Object.(float64) + float64(n)
	default:
		return fmt.Errorf("the value for %s is not an integer", key)
	}
	c.items[key] = value

	return nil
}

// Increment an item of type float32 or float64 by n. Returns an error if the
// item's value is not floating point, if it was not found, or if it is not
// possible to increment it by n. Pass a negative number to decrement the
// value. To retrieve the incremented value, use one of the specialized methods,
// e.g. IncrementFloat64.
func (c *cache) IncrementFloat(key string, n float64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return fmt.Errorf("item %s not found", key)
	}
	switch value.Object.(type) {
	case float32:
		value.Object = value.Object.(float32) + float32(n)
	case float64:
		value.Object = value.Object.(float64) + n
	default:
		return fmt.Errorf("the value for %s does not have type float32 or float64", key)
	}
	c.items[key] = value

	return nil
}

// Increment an item of type int by n. Returns an error if the item's value is
// not an int, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt(key string, n int) (int, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type int8 by n. Returns an error if the item's value is
// not an int8, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt8(key string, n int8) (int8, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int8)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int8", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type int16 by n. Returns an error if the item's value is
// not an int16, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt16(key string, n int16) (int16, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int16)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int16", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type int32 by n. Returns an error if the item's value is
// not an int32, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt32(key string, n int32) (int32, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int32)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int32", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type int64 by n. Returns an error if the item's value is
// not an int64, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt64(key string, n int64) (int64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int64)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int64", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type uint by n. Returns an error if the item's value is
// not an uint, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementUint(key string, n uint) (uint, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type uintptr by n. Returns an error if the item's value
// is not an uintptr, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUintptr(key string, n uintptr) (uintptr, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uintptr)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uintptr", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type uint8 by n. Returns an error if the item's value
// is not an uint8, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint8(key string, n uint8) (uint8, error) {
	c.mutex.Lock()
	c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint8)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint8", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type uint16 by n. Returns an error if the item's value
// is not an uint16, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint16(key string, n uint16) (uint16, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint16)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint16", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type uint32 by n. Returns an error if the item's value
// is not an uint32, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint32(key string, n uint32) (uint32, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint32)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint32", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type uint64 by n. Returns an error if the item's value
// is not an uint64, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint64(key string, n uint64) (uint64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint64)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint64", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type float32 by n. Returns an error if the item's value
// is not an float32, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementFloat32(key string, n float32) (float32, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(float32)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an float32", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Increment an item of type float64 by n. Returns an error if the item's value
// is not an float64, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementFloat64(key string, n float64) (float64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(float64)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an float64", key)
	}
	nv := rv + n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type int, int8, int16, int32, int64, uintptr, uint,
// uint8, uint32, or uint64, float32 or float64 by n. Returns an error if the
// item's value is not an integer, if it was not found, or if it is not
// possible to decrement it by n. To retrieve the decremented value, use one
// of the specialized methods, e.g. DecrementInt64.
func (c *cache) Decrement(key string, n int64) error {
	// TODO: Implement Increment and Decrement more cleanly.
	// (Cannot do Increment(key, n*-1) for uints.)
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return fmt.Errorf("item %s not found", key)
	}
	switch value.Object.(type) {
	case int:
		value.Object = value.Object.(int) - int(n)
	case int8:
		value.Object = value.Object.(int8) - int8(n)
	case int16:
		value.Object = value.Object.(int16) - int16(n)
	case int32:
		value.Object = value.Object.(int32) - int32(n)
	case int64:
		value.Object = value.Object.(int64) - n
	case uint:
		value.Object = value.Object.(uint) - uint(n)
	case uintptr:
		value.Object = value.Object.(uintptr) - uintptr(n)
	case uint8:
		value.Object = value.Object.(uint8) - uint8(n)
	case uint16:
		value.Object = value.Object.(uint16) - uint16(n)
	case uint32:
		value.Object = value.Object.(uint32) - uint32(n)
	case uint64:
		value.Object = value.Object.(uint64) - uint64(n)
	case float32:
		value.Object = value.Object.(float32) - float32(n)
	case float64:
		value.Object = value.Object.(float64) - float64(n)
	default:
		return fmt.Errorf("the value for %s is not an integer", key)
	}
	c.items[key] = value

	return nil
}

// Decrement an item of type float32 or float64 by n. Returns an error if the
// item's value is not floating point, if it was not found, or if it is not
// possible to decrement it by n. Pass a negative number to decrement the
// value. To retrieve the decremented value, use one of the specialized methods,
// e.g. DecrementFloat64.
func (c *cache) DecrementFloat(key string, n float64) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return fmt.Errorf("item %s not found", key)
	}
	switch value.Object.(type) {
	case float32:
		value.Object = value.Object.(float32) - float32(n)
	case float64:
		value.Object = value.Object.(float64) - n
	default:
		return fmt.Errorf("the value for %s does not have type float32 or float64", key)
	}
	c.items[key] = value

	return nil
}

// Decrement an item of type int by n. Returns an error if the item's value is
// not an int, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt(key string, n int) (int, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type int8 by n. Returns an error if the item's value is
// not an int8, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt8(key string, n int8) (int8, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int8)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int8", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type int16 by n. Returns an error if the item's value is
// not an int16, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt16(key string, n int16) (int16, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int16)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int16", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type int32 by n. Returns an error if the item's value is
// not an int32, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt32(key string, n int32) (int32, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int32)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int32", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type int64 by n. Returns an error if the item's value is
// not an int64, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt64(key string, n int64) (int64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(int64)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an int64", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type uint by n. Returns an error if the item's value is
// not an uint, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementUint(key string, n uint) (uint, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type uintptr by n. Returns an error if the item's value
// is not an uintptr, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUintptr(key string, n uintptr) (uintptr, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uintptr)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uintptr", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type uint8 by n. Returns an error if the item's value is
// not an uint8, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementUint8(key string, n uint8) (uint8, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint8)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint8", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type uint16 by n. Returns an error if the item's value
// is not an uint16, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint16(key string, n uint16) (uint16, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint16)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint16", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type uint32 by n. Returns an error if the item's value
// is not an uint32, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint32(key string, n uint32) (uint32, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint32)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint32", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type uint64 by n. Returns an error if the item's value
// is not an uint64, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint64(key string, n uint64) (uint64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(uint64)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an uint64", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type float32 by n. Returns an error if the item's value
// is not an float32, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementFloat32(key string, n float32) (float32, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(float32)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an float32", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Decrement an item of type float64 by n. Returns an error if the item's value
// is not an float64, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementFloat64(key string, n float64) (float64, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	value, found := c.items[key]
	if !found || value.Expired() {
		return 0, fmt.Errorf("item %s not found", key)
	}
	rv, ok := value.Object.(float64)
	if !ok {
		return 0, fmt.Errorf("the value for %s is not an float64", key)
	}
	nv := rv - n
	value.Object = nv
	c.items[key] = value

	return nv, nil
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(key string) {
	c.mutex.Lock()
	value, evicted := c.delete(key)
	c.mutex.Unlock()

	if evicted {
		c.onEvicted(key, value)
	}
}

func (c *cache) delete(key string) (interface{}, bool) {
	if c.onEvicted != nil {
		if value, found := c.items[key]; found {
			delete(c.items, key)
			return value.Object, true
		}
	}

	delete(c.items, key)

	return nil, false
}

type keyAndValue struct {
	key   string
	value interface{}
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()

	c.mutex.Lock()
	for key, value := range c.items {
		// "Inlining" of expired
		if value.Expiration > 0 && now > value.Expiration {
			ov, evicted := c.delete(key)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{key, ov})
			}
		}
	}
	c.mutex.Unlock()

	for _, value := range evictedItems {
		c.onEvicted(value.key, value.value)
	}
}

// Sets an (optional) function that is called with the key and value when an
// item is evicted from the cache. (Including when it is deleted manually, but
// not when it is overwritten.) Set to nil to disable.
func (c *cache) OnEvicted(f func(string, interface{})) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.onEvicted = f
}

// Write the cache's items (using Gob) to an io.Writer.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) Save(w io.Writer) (err error) {
	enc := gob.NewEncoder(w)
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("error registering item types with Gob library")
		}
	}()

	c.mutex.RLock()
	defer c.mutex.RUnlock()

	for _, value := range c.items {
		gob.Register(value.Object)
	}
	err = enc.Encode(&c.items)

	return
}

// Save the cache's items to the given filename, creating the file if it
// doesn't exist, and overwriting it if it does.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) SaveFile(fname string) error {
	fp, err := os.Create(fname)
	if err != nil {
		return err
	}

	err = c.Save(fp)
	defer fp.Close()
	if err != nil {
		return err
	}

	return fp.Close()
}

// Add (Gob-serialized) cache items from an io.Reader, excluding any items with
// keys that already exist (and haven't expired) in the current cache.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) Load(r io.Reader) error {
	dec := gob.NewDecoder(r)
	items := map[string]Item{}

	err := dec.Decode(&items)
	if err == nil {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		for key, value := range items {
			ov, found := c.items[key]
			if !found || ov.Expired() {
				c.items[key] = value
			}
		}
	}

	return err
}

// Load and add cache items from the given filename, excluding any items with
// keys that already exist in the current cache.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) LoadFile(fname string) error {
	fp, err := os.Open(fname)
	if err != nil {
		return err
	}

	err = c.Load(fp)
	defer fp.Close()
	if err != nil {
		return err
	}

	return fp.Close()
}

// Copies all unexpired items in the cache into a new map and returns it.
func (c *cache) Items() map[string]Item {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	m := make(map[string]Item, len(c.items))
	now := time.Now().UnixNano()
	for key, value := range c.items {
		// "Inlining" of Expired
		if value.Expiration > 0 {
			if now > value.Expiration {
				continue
			}
		}
		m[key] = value
	}

	return m
}

// Returns the number of items in the cache. This may include items that have
// expired, but have not yet been cleaned up.
func (c *cache) ItemCount() int {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	return len(c.items)
}

// Delete all items from the cache.
func (c *cache) Flush() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.items = map[string]Item{}
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
		stop:     make(chan bool),
	}
	c.janitor = j

	go j.Run(c)
}

func newCache(duration time.Duration, items map[string]Item) *cache {
	if duration == 0 {
		duration = -1
	}

	c := &cache{
		expiration: duration,
		items:      items,
	}

	return c
}

func newCacheWithJanitor(de time.Duration, ci time.Duration, m map[string]Item) *Cache {
	c := newCache(de, m)
	// This trick ensures that the janitor goroutine (which--granted it
	// was enabled--is running DeleteExpired on c forever) does not keep
	// the returned C object from being garbage collected. When it is
	// garbage collected, the finalizer stops the janitor goroutine, after
	// which c can be collected.
	C := &Cache{c}

	if ci > 0 {
		runJanitor(c, ci)
		runtime.SetFinalizer(C, stopJanitor)
	}

	return C
}

// Return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
func New(defaultExpiration, cleanupInterval time.Duration) *Cache {
	items := make(map[string]Item)
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}

// Return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
//
// NewFrom() also accepts an items map which will serve as the underlying map
// for the cache. This is useful for starting from a deserialized cache
// (serialized using e.g. gob.Encode() on c.Items()), or passing in e.g.
// make(map[string]Item, 500) to improve startup performance when the cache
// is expected to reach a certain minimum size.
//
// Only the cache's methods synchronize access to this map, so it is not
// recommended to keep any references to the map around after creating a cache.
// If need be, the map can be accessed at a later point using c.Items() (subject
// to the same caveat.)
//
// Note regarding serialization: When using e.g. gob, make sure to
// gob.Register() the individual types stored in the cache before encoding a
// map retrieved with c.Items(), and to register those same types before
// decoding a blob containing an items map.
func NewFrom(defaultExpiration, cleanupInterval time.Duration, items map[string]Item) *Cache {
	return newCacheWithJanitor(defaultExpiration, cleanupInterval, items)
}
