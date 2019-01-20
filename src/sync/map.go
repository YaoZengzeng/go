// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)

// Map is like a Go map[interface{}]interface{} but is safe for concurrent use
// by multiple goroutines without additional locking or coordination.
// Loads, stores, and deletes run in amortized constant time.
// Loads, stores以及deletes都在平均在常数时间完成
//
// The Map type is specialized. Most code should use a plain Go map instead,
// with separate locking or coordination, for better type safety and to make it
// easier to maintain other invariants along with the map content.
//
// The Map type is optimized for two common use cases: (1) when the entry for a given
// key is only ever written once but read many times, as in caches that only grow,
// or (2) when multiple goroutines read, write, and overwrite entries for disjoint
// sets of keys. In these two cases, use of a Map may significantly reduce lock
// contention compared to a Go map paired with a separate Mutex or RWMutex.
// Map类型主要对如下两个通用的场景进行了优化：（1）对于一个给定的key的entry只写一次，但是会被读多次
// （2）当多个goroutines读，写并且overwrite不同集合的keys对应的entry，这两种情况下，Map和map
// paired with Mutex或者RWMutex相比都能极大地降低锁竞争
//
// The zero Map is empty and ready for use. A Map must not be copied after first use.
type Map struct {
	mu Mutex

	// read contains the portion of the map's contents that are safe for
	// concurrent access (with or without mu held).
	// read包含了map中可以并发访问的部分（有或者没有锁）
	//
	// The read field itself is always safe to load, but must only be stored with
	// mu held.
	// read在load时总是安全的，但是store的时候必须要持有锁
	//
	// Entries stored in read may be updated concurrently without mu, but updating
	// a previously-expunged entry requires that the entry be copied to the dirty
	// map and unexpunged with mu held.
	// 在read中存储的Entries可以在没有持有mu的情况下并发更新，当在更新一个previous-expunged
	// entry的时候需要entry被拷贝到dirty map并且在持有mu的情况下unexpunged
	read atomic.Value // readOnly

	// dirty contains the portion of the map's contents that require mu to be
	// held. To ensure that the dirty map can be promoted to the read map quickly,
	// it also includes all of the non-expunged entries in the read map.
	// dirty中包含了那些需要持有mu的map中的数据，为了保证dirty map能快速提升为read map
	// 它同时也包含了read map中所有未被擦除的entries
	//
	// Expunged entries are not stored in the dirty map. An expunged entry in the
	// clean map must be unexpunged and added to the dirty map before a new value
	// can be stored to it.
	// 被擦除的entry不能被存放在dirty map中，一个clean map中被擦除的entry必须先被unexpunged
	// 并且添加到dirty map中，之后新的value才能保存其中
	//
	// If the dirty map is nil, the next write to the map will initialize it by
	// making a shallow copy of the clean map, omitting stale entries.
	// 如果dirty map为nil，下一次写回通过浅拷贝一份clean map来初始化，并且忽略stale entries
	dirty map[interface{}]*entry

	// misses counts the number of loads since the read map was last updated that
	// needed to lock mu to determine whether the key was present.
	// misses统计自上一次read map更新以来，需要通过锁住mu，来确定key是否存在的次数
	//
	// Once enough misses have occurred to cover the cost of copying the dirty
	// map, the dirty map will be promoted to the read map (in the unamended
	// state) and the next store to the map will make a new dirty copy.
	// 一旦misses的次数达到了拷贝dirty map的消耗，dirty map就会被提升为read map
	// 下一次store就会创建一个新的dirty copy
	misses int
}

// readOnly is an immutable struct stored atomically in the Map.read field.
// readOnly是原子存储在Map.read中的不可变的部分
type readOnly struct {
	m       map[interface{}]*entry
	// 如果dirty map包含了一些不在m中的key，amended就为true
	amended bool // true if the dirty map contains some key not in m.
}

// expunged is an arbitrary pointer that marks entries which have been deleted
// from the dirty map.
// expunged是一个任意的指针，用于标记已经从dirty map中删除的entry
var expunged = unsafe.Pointer(new(interface{}))

// An entry is a slot in the map corresponding to a particular key.
// 一个entry是map中的一个slot，对应于某个特定的key
type entry struct {
	// p points to the interface{} value stored for the entry.
	//
	// If p == nil, the entry has been deleted and m.dirty == nil.
	// 如果p == nil，则entry已经被删除了并且m.dirty == nil
	//
	// If p == expunged, the entry has been deleted, m.dirty != nil, and the entry
	// is missing from m.dirty.
	// 如果p == expunged，则entry已经被删除了，m.dirty != nil，并且entry不存在于m.dirty
	//
	// Otherwise, the entry is valid and recorded in m.read.m[key] and, if m.dirty
	// != nil, in m.dirty[key].
	// 否则，entry是合法的，并且记录在m.read.m[key]中，如果m.dirty != nil，则也在m.dirty[key]中
	//
	// An entry can be deleted by atomic replacement with nil: when m.dirty is
	// next created, it will atomically replace nil with expunged and leave
	// m.dirty[key] unset.
	// 一个entry可以被atomic replacement with nil的方式进行删除，当m.dirty下一次被创建
	// 它会被原子地用expunged移除nil，并且unset m.dirty[key]
	//
	// An entry's associated value can be updated by atomic replacement, provided
	// p != expunged. If p == expunged, an entry's associated value can be updated
	// only after first setting m.dirty[key] = e so that lookups using the dirty
	// map find the entry.
	// 如果p != expunged，一个entry相关的value可以通过atomic replacement进行更新
	// 当p为expunged时，一个entry相关的value只有在第一次设置m.dirty[key] = e时才能更新
	// 这样的话，lookup就会使用dirty map找到entry
	p unsafe.Pointer // *interface{}
}

func newEntry(i interface{}) *entry {
	return &entry{p: unsafe.Pointer(&i)}
}

// Load returns the value stored in the map for a key, or nil if no
// value is present.
// Load返回存储在map中的key对应的value，或者是nil，如果value不存在的话
// The ok result indicates whether value was found in the map.
func (m *Map) Load(key interface{}) (value interface{}, ok bool) {
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	if !ok && read.amended {
		// 如果amended为true，则说明dirty中可能有数据
		m.mu.Lock()
		// Avoid reporting a spurious miss if m.dirty got promoted while we were
		// blocked on m.mu. (If further loads of the same key will not miss, it's
		// not worth copying the dirty map for this key.)
		// 防止发生spurious miss，如果在锁m.mu的时候，m.dirty被promoted了
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]
			// Regardless of whether the entry was present, record a miss: this key
			// will take the slow path until the dirty map is promoted to the read
			// map.
			// 不过entry是否存在，记录一次miss，这个key会一直通过这样的slow path获取，直到
			// dirty map被提升为read map
			m.missLocked()
		}
		m.mu.Unlock()
	}
	// 如果最终找不到key
	if !ok {
		return nil, false
	}
	return e.load()
}

func (e *entry) load() (value interface{}, ok bool) {
	// 原子加载e.p
	p := atomic.LoadPointer(&e.p)
	if p == nil || p == expunged {
		// 如果entry中存储的是nil或者expunged
		return nil, false
	}
	return *(*interface{})(p), true
}

// Store sets the value for a key.
func (m *Map) Store(key, value interface{}) {
	read, _ := m.read.Load().(readOnly)
	// 如果key在m.read中存在，则直接打算存储到read中
	if e, ok := read.m[key]; ok && e.tryStore(&value) {
		return
	}

	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {
			// The entry was previously expunged, which implies that there is a
			// non-nil dirty map and this entry is not in it.
			// 如果entry之前被expunged，这意味着dirty map不为nil，并该entry不在其中
			m.dirty[key] = e
		}
		// 直接修改entry包含的指针的值
		e.storeLocked(&value)
	} else if e, ok := m.dirty[key]; ok {
		e.storeLocked(&value)
	} else {
		if !read.amended {
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			// 我们将新的key加入dirty map中
			// 确保dirty map已经被分配，并且将read-only map设置为incomplete
			m.dirtyLocked()
			// 将amended设置为true
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		// 将key加入dirty map中
		m.dirty[key] = newEntry(value)
	}
	m.mu.Unlock()
}

// tryStore stores a value if the entry has not been expunged.
// tryStore存储一个value，如果entry没有被擦除的话
//
// If the entry is expunged, tryStore returns false and leaves the entry
// unchanged.
// 如果entry被擦除了，tryStore返回false并且不改变entry
func (e *entry) tryStore(i *interface{}) bool {
	// 当对应的entry已经被expunged时，则store失败
	p := atomic.LoadPointer(&e.p)
	if p == expunged {
		return false
	}
	// 如果没被expunged，则直接交换
	for {
		if atomic.CompareAndSwapPointer(&e.p, p, unsafe.Pointer(i)) {
			return true
		}
		p = atomic.LoadPointer(&e.p)
		// 当p变为expunged时，返回
		if p == expunged {
			return false
		}
	}
}

// unexpungeLocked ensures that the entry is not marked as expunged.
//
// If the entry was previously expunged, it must be added to the dirty map
// before m.mu is unlocked.
// unexpungeLocked确保entry不被标记为expunged，如果entry之前被expunged，它必须在
// m.mu unlocked之前，被加入dirty map
func (e *entry) unexpungeLocked() (wasExpunged bool) {
	return atomic.CompareAndSwapPointer(&e.p, expunged, nil)
}

// storeLocked unconditionally stores a value to the entry.
//
// The entry must be known not to be expunged.
// storeLocked无条件地将value存储到entry中，entry必须已知不被expunged
func (e *entry) storeLocked(i *interface{}) {
	atomic.StorePointer(&e.p, unsafe.Pointer(i))
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *Map) LoadOrStore(key, value interface{}) (actual interface{}, loaded bool) {
	// Avoid locking if it's a clean hit.
	read, _ := m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		actual, loaded, ok := e.tryLoadOrStore(value)
		if ok {
			return actual, loaded
		}
	}

	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {
			m.dirty[key] = e
		}
		actual, loaded, _ = e.tryLoadOrStore(value)
	} else if e, ok := m.dirty[key]; ok {
		actual, loaded, _ = e.tryLoadOrStore(value)
		m.missLocked()
	} else {
		if !read.amended {
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			m.dirtyLocked()
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
		actual, loaded = value, false
	}
	m.mu.Unlock()

	return actual, loaded
}

// tryLoadOrStore atomically loads or stores a value if the entry is not
// expunged.
//
// If the entry is expunged, tryLoadOrStore leaves the entry unchanged and
// returns with ok==false.
func (e *entry) tryLoadOrStore(i interface{}) (actual interface{}, loaded, ok bool) {
	p := atomic.LoadPointer(&e.p)
	if p == expunged {
		return nil, false, false
	}
	if p != nil {
		return *(*interface{})(p), true, true
	}

	// Copy the interface after the first load to make this method more amenable
	// to escape analysis: if we hit the "load" path or the entry is expunged, we
	// shouldn't bother heap-allocating.
	ic := i
	for {
		if atomic.CompareAndSwapPointer(&e.p, nil, unsafe.Pointer(&ic)) {
			return i, false, true
		}
		p = atomic.LoadPointer(&e.p)
		if p == expunged {
			return nil, false, false
		}
		if p != nil {
			return *(*interface{})(p), true, true
		}
	}
}

// Delete deletes the value for a key.
func (m *Map) Delete(key interface{}) {
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	// 从read map中删除key
	if !ok && read.amended {
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			// 从dirty map中删除key
			delete(m.dirty, key)
		}
		m.mu.Unlock()
	}
	if ok {
		// 如果这个key存在，则将entry设置为nil
		e.delete()
	}
}

func (e *entry) delete() (hadValue bool) {
	for {
		p := atomic.LoadPointer(&e.p)
		// 如果已经被删除了
		if p == nil || p == expunged {
			return false
		}
		// 将entry的值设置为nil
		if atomic.CompareAndSwapPointer(&e.p, p, nil) {
			return true
		}
	}
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot of the Map's
// contents: no key will be visited more than once, but if the value for any key
// is stored or deleted concurrently, Range may reflect any mapping for that key
// from any point during the Range call.
//
// Range may be O(N) with the number of elements in the map even if f returns
// false after a constant number of calls.
func (m *Map) Range(f func(key, value interface{}) bool) {
	// We need to be able to iterate over all of the keys that were already
	// present at the start of the call to Range.
	// If read.amended is false, then read.m satisfies that property without
	// requiring us to hold m.mu for a long time.
	read, _ := m.read.Load().(readOnly)
	if read.amended {
		// m.dirty contains keys not in read.m. Fortunately, Range is already O(N)
		// (assuming the caller does not break out early), so a call to Range
		// amortizes an entire copy of the map: we can promote the dirty copy
		// immediately!
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		if read.amended {
			read = readOnly{m: m.dirty}
			m.read.Store(read)
			m.dirty = nil
			m.misses = 0
		}
		m.mu.Unlock()
	}

	for k, e := range read.m {
		v, ok := e.load()
		if !ok {
			continue
		}
		if !f(k, v) {
			break
		}
	}
}

func (m *Map) missLocked() {
	m.misses++
	// 如果misses的次数，小于m.dirty的长度，就直接返回
	if m.misses < len(m.dirty) {
		return
	}
	// 否则将m.dirty提升为m.read
	m.read.Store(readOnly{m: m.dirty})
	// 将m.dirty设置为nil
	m.dirty = nil
	m.misses = 0
}

func (m *Map) dirtyLocked() {
	if m.dirty != nil {
		// 如果dirty map已经分配了，就返回
		return
	}

	read, _ := m.read.Load().(readOnly)
	// 加载read map
	m.dirty = make(map[interface{}]*entry, len(read.m))
	for k, e := range read.m {
		// 当entry不为expunged时，加入dirty map中
		if !e.tryExpungeLocked() {
			m.dirty[k] = e
		}
	}
}

func (e *entry) tryExpungeLocked() (isExpunged bool) {
	p := atomic.LoadPointer(&e.p)
	// 当p为nil时，不断尝试将其设置为expunged
	for p == nil {
		if atomic.CompareAndSwapPointer(&e.p, nil, expunged) {
			return true
		}
		p = atomic.LoadPointer(&e.p)
	}
	return p == expunged
}
