// Package hub implements the v3 hub-side multiplexing layer: singleflight
// object reads over the v2 origin engine, a byte-budgeted LRU of encoded
// chunks/metadata, and per-device served metrics. The v2 FaultServer behavior
// (ETag/304, ranges, injected faults) is preserved; only the object-body read
// path is wrapped.
package hub

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

// Loader reads an object body by its origin-relative path
// (e.g. "chunks/ab/0123….gz", "releases/desired.json").
type Loader func(rel string) ([]byte, error)

// entry is one cached object.
type entry struct {
	rel  string
	data []byte
}

// LRU is a byte-budgeted LRU of object bodies. Not safe for concurrent use;
// Hub serializes access.
type LRU struct {
	maxBytes int
	curBytes int
	items    map[string]*list.Element
	order    *list.List // front = most recent
}

func newLRU(maxBytes int) *LRU {
	return &LRU{maxBytes: maxBytes, items: map[string]*list.Element{}, order: list.New()}
}

func (l *LRU) get(rel string) ([]byte, bool) {
	el, ok := l.items[rel]
	if !ok {
		return nil, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*entry).data, true
}

func (l *LRU) put(rel string, data []byte) (evicted int) {
	if el, ok := l.items[rel]; ok {
		l.order.MoveToFront(el)
		e := el.Value.(*entry)
		l.curBytes += len(data) - len(e.data)
		e.data = data
	} else {
		l.items[rel] = l.order.PushFront(&entry{rel: rel, data: data})
		l.curBytes += len(data)
	}
	for l.curBytes > l.maxBytes && l.order.Len() > 1 {
		oldest := l.order.Back()
		if oldest == nil {
			break
		}
		e := oldest.Value.(*entry)
		l.order.Remove(oldest)
		delete(l.items, e.rel)
		l.curBytes -= len(e.data)
		evicted++
	}
	return evicted
}

// DeviceMetrics is the per-client (X-Edgelab-Device) accounting record.
type DeviceMetrics struct {
	Device        string    `json:"device"`
	Requests      int64     `json:"requests"`
	ChunkRequests int64     `json:"chunk_requests"`
	MetaRequests  int64     `json:"meta_requests"`
	BytesServed   int64     `json:"bytes_served"`
	LastSeen      time.Time `json:"last_seen"`
}

// Stats is the hub-wide snapshot exposed via the admin socket and /hubstats.
type Stats struct {
	CacheMaxBytes int            `json:"cache_max_bytes"`
	CacheBytes    int            `json:"cache_bytes"`
	CacheObjects  int            `json:"cache_objects"`
	DiskReads     int64          `json:"disk_reads"`
	CacheHits     int64          `json:"cache_hits"`
	Coalesced     int64          `json:"coalesced_requests"`
	Evictions     int64          `json:"evictions"`
	InflightReads int            `json:"inflight_reads"`
	Devices       []DeviceMetrics `json:"devices"`
}

// Hub multiplexes object reads: identical concurrent requests share one
// underlying read (singleflight), results are cached in a byte-budgeted LRU,
// and per-device metrics are recorded.
type Hub struct {
	underlying Loader
	lru        *lruLocked
	anonName   string

	mu        sync.Mutex // guards counters + inflight map
	inflight  map[string]*call
	diskReads int64
	cacheHits int64
	coalesced int64
	evictions int64

	dmu    sync.Mutex
	devices map[string]*DeviceMetrics
}

type call struct {
	wg   sync.WaitGroup
	data []byte
	err  error
}

func newlruLocked(maxBytes int) *lruLocked { return &lruLocked{LRU: *newLRU(maxBytes)} }

// lruLocked wraps LRU with its own mutex so the hub's singleflight lock is not
// held during cache hits.
type lruLocked struct {
	mu sync.Mutex
	LRU
}

// New builds a hub over the underlying loader with the given cache byte
// budget. A maxBytes <= 0 disables caching (singleflight only).
func New(underlying Loader, maxBytes int) *Hub {
	return &Hub{
		underlying: underlying,
		lru:        newlruLocked(maxBytes),
		anonName:   "anonymous",
		inflight:   map[string]*call{},
		devices:    map[string]*DeviceMetrics{},
	}
}

// Loader returns the http-server-facing read function to install as
// FaultServer.ObjectLoader.
func (h *Hub) Loader() Loader { return h.load }

func (h *Hub) load(rel string) ([]byte, error) {
	// Fast path: cache hit without taking the singleflight lock.
	h.lru.mu.Lock()
	if data, ok := h.lru.get(rel); ok {
		h.lru.mu.Unlock()
		h.mu.Lock()
		h.cacheHits++
		h.mu.Unlock()
		// Return a copy: fault injection mutates the body buffer in place.
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	}
	h.lru.mu.Unlock()

	h.mu.Lock()
	if c, ok := h.inflight[rel]; ok {
		h.coalesced++
		h.mu.Unlock()
		c.wg.Wait()
		if c.err != nil {
			return nil, c.err
		}
		h.lru.mu.Lock()
		data, ok := h.lru.get(rel)
		h.lru.mu.Unlock()
		if ok {
			out := make([]byte, len(data))
			copy(out, data)
			return out, nil
		}
		return append([]byte(nil), c.data...), nil
	}
	c := &call{}
	c.wg.Add(1)
	h.inflight[rel] = c
	h.mu.Unlock()

	data, err := h.underlying(rel)

	h.mu.Lock()
	delete(h.inflight, rel)
	if err == nil {
		h.diskReads++
	}
	h.mu.Unlock()

	h.lru.mu.Lock()
	if err == nil {
		if ev := h.lru.put(rel, data); ev > 0 {
			h.mu.Lock()
			h.evictions += int64(ev)
			h.mu.Unlock()
		}
	}
	h.lru.mu.Unlock()

	c.data, c.err = data, err
	c.wg.Done()
	return data, err
}

// InvalidatePrefix drops every cached object whose origin-relative path has
// the given prefix (e.g. "releases/"). The hub calls this when the announce
// trigger fires so a fresh channel write is never shadowed by stale cached
// metadata; chunk objects are content-addressed and never need it. Returns
// the number of entries dropped.
func (h *Hub) InvalidatePrefix(prefix string) int {
	h.lru.mu.Lock()
	defer h.lru.mu.Unlock()
	dropped := 0
	for {
		removed := false
		for el := h.lru.order.Front(); el != nil; el = el.Next() {
			e := el.Value.(*entry)
			if strings.HasPrefix(e.rel, prefix) {
				h.lru.order.Remove(el)
				delete(h.lru.items, e.rel)
				h.lru.curBytes -= len(e.data)
				dropped++
				removed = true
				break
			}
		}
		if !removed {
			break
		}
	}
	return dropped
}

// Record notes one served request for the named device.
func (h *Hub) Record(device string, isChunk bool, bytes int64) {
	if device == "" {
		device = h.anonName
	}
	h.dmu.Lock()
	defer h.dmu.Unlock()
	d, ok := h.devices[device]
	if !ok {
		d = &DeviceMetrics{Device: device}
		h.devices[device] = d
	}
	d.Requests++
	if isChunk {
		d.ChunkRequests++
	} else {
		d.MetaRequests++
	}
	d.BytesServed += bytes
	d.LastSeen = time.Now()
}

// Snapshot returns the hub stats with per-device metrics sorted by name.
func (h *Hub) Snapshot() Stats {
	h.mu.Lock()
	st := Stats{
		DiskReads:     h.diskReads,
		CacheHits:     h.cacheHits,
		Coalesced:     h.coalesced,
		Evictions:     h.evictions,
		InflightReads: len(h.inflight),
	}
	h.mu.Unlock()
	h.lru.mu.Lock()
	st.CacheMaxBytes = h.lru.maxBytes
	st.CacheBytes = h.lru.curBytes
	st.CacheObjects = h.lru.order.Len()
	h.lru.mu.Unlock()
	h.dmu.Lock()
	for _, d := range h.devices {
		dd := *d
		st.Devices = append(st.Devices, dd)
	}
	h.dmu.Unlock()
	sortDevices(st.Devices)
	return st
}

func sortDevices(ds []DeviceMetrics) {
	for i := 1; i < len(ds); i++ {
		for j := i; j > 0 && ds[j].Device < ds[j-1].Device; j-- {
			ds[j], ds[j-1] = ds[j-1], ds[j]
		}
	}
}
