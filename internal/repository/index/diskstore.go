package index

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"sync"
	"unsafe"

	"github.com/restic/restic/internal/debug"
)

// A diskStore keeps the buckets and the entries of a large indexMap in
// memory-mapped temporary files instead of the Go heap.
//
// The index of a repository with tens of millions of blobs needs gigabytes of
// memory in the Go heap, which the kernel can neither swap out cheaply nor
// drop. Pages of a mapped file are different: clean pages are simply dropped
// under memory pressure and dirty pages are written back to the file. The
// hash table itself is unchanged (chained buckets, entries addressed by
// index, bloom filter in the bucket words). Only the allocation of its two
// arrays changes, which is why indexEntry must stay free of Go pointers.
//
// The temporary files are unlinked right after they have been created, so
// nothing is left behind if the process dies. The directory must be on a real
// disk: on tmpfs the pages would count as memory again.
//
// The feature is off unless RESTIC_INDEX_MMAP_DIR is set:
//
//	RESTIC_INDEX_MMAP_DIR          directory for the temporary files
//	RESTIC_INDEX_MMAP_MIN_ENTRIES  minimum number of entries of a map before it
//	                               moves to disk (default 1000000)
const (
	// diskIndexWorkers caps the parallel index decoding when the mapped store is on.
	diskIndexWorkers = 4

	envDiskDir        = "RESTIC_INDEX_MMAP_DIR"
	envDiskMinEntries = "RESTIC_INDEX_MMAP_MIN_ENTRIES"

	defaultDiskMinEntries = 1000000

	// entries are mapped in segments of 1<<diskBlockShift entries (14 MiB),
	// which are never moved or copied once mapped.
	diskBlockShift = 18
	diskBlockLen   = 1 << diskBlockShift
	diskBlockMask  = diskBlockLen - 1
)

// Package-level settings, read once from the environment. Tests override them.
var (
	diskDirOnce         sync.Once
	indexDiskDir        string
	indexDiskMinEntries uint = defaultDiskMinEntries
)

func loadDiskConfig() {
	indexDiskDir = os.Getenv(envDiskDir)
	if v := os.Getenv(envDiskMinEntries); v != "" {
		n, err := strconv.ParseUint(v, 10, 63)
		if err != nil || n == 0 {
			debug.Log("ignoring invalid %s=%q", envDiskMinEntries, v)
			return
		}
		indexDiskMinEntries = uint(n)
	}
}

// diskStoreEnabled reports whether large indexes are kept on disk.
func diskStoreEnabled() bool {
	diskDirOnce.Do(loadDiskConfig)
	return indexDiskDir != "" && diskSupported
}

func diskStoreWanted(numEntries uint) bool {
	return diskStoreEnabled() && numEntries >= indexDiskMinEntries
}

// DiskStoreEnabled reports whether large indexes are kept in mapped files.
func DiskStoreEnabled() bool { return diskStoreEnabled() }

// diskRegions tracks the mappings so that they can be released once the map
// that uses them is garbage collected.
type diskRegions struct {
	mu      sync.Mutex
	buckets []byte
	blocks  [][]byte
}

func (r *diskRegions) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buckets != nil {
		_ = unmapRegion(r.buckets)
		r.buckets = nil
	}
	for _, b := range r.blocks {
		_ = unmapRegion(b)
	}
	r.blocks = nil
}

type diskStore struct {
	dir  string
	regs *diskRegions
}

func newDiskStore(dir string) *diskStore {
	ds := &diskStore{dir: dir, regs: &diskRegions{}}
	runtime.AddCleanup(ds, func(r *diskRegions) { r.release() }, ds.regs)
	return ds
}

// allocBuckets returns a zeroed bucket array of n words backed by a mapped
// file and unmaps the previous one. The caller must not use the old slice
// afterwards.
func (d *diskStore) allocBuckets(n int) []uint {
	b, err := mapTempFile(d.dir, n*int(unsafe.Sizeof(uint(0))))
	if err != nil {
		panic("index: cannot map bucket file in " + d.dir + ": " + err.Error())
	}
	d.regs.mu.Lock()
	old := d.regs.buckets
	d.regs.buckets = b
	d.regs.mu.Unlock()
	if old != nil {
		_ = unmapRegion(old)
	}
	return unsafe.Slice((*uint)(unsafe.Pointer(&b[0])), n)
}

// mmapArray is an entryArray whose segments are mapped temporary files.
type mmapArray struct {
	store  *diskStore
	size   uint
	blocks []*[diskBlockLen]indexEntry
}

var _ entryArray = (*mmapArray)(nil)

func newMmapArray(ds *diskStore) *mmapArray {
	return &mmapArray{store: ds}
}

func (a *mmapArray) Alloc() (*indexEntry, uint) {
	if a.size&diskBlockMask == 0 {
		a.addBlock()
	}
	pos := a.size
	a.size++
	return a.Ref(pos), pos
}

func (a *mmapArray) addBlock() {
	b, err := mapTempFile(a.store.dir, diskBlockLen*int(unsafe.Sizeof(indexEntry{})))
	if err != nil {
		panic("index: cannot map entry file in " + a.store.dir + ": " + err.Error())
	}
	a.store.regs.mu.Lock()
	a.store.regs.blocks = append(a.store.regs.blocks, b)
	a.store.regs.mu.Unlock()
	a.blocks = append(a.blocks, (*[diskBlockLen]indexEntry)(unsafe.Pointer(&b[0])))
}

func (a *mmapArray) Ref(pos uint) *indexEntry {
	if pos >= a.size {
		panic("array index out of bounds")
	}
	return &a.blocks[pos>>diskBlockShift][pos&diskBlockMask]
}

func (a *mmapArray) Size() uint { return a.size }

// preallocate does nothing: segments are mapped (as sparse files) on demand.
func (a *mmapArray) preallocate(uint) {}

var errMmapUnsupported = errors.New("memory-mapped index is not supported on this platform")

// moveToDisk moves buckets and entries of the map into mapped files. Entry
// indexes are preserved, so the chains stored in the buckets stay valid.
func (m *indexMap) moveToDisk() {
	ds := newDiskStore(indexDiskDir)
	arr := newMmapArray(ds)

	blockCount := m.blockList.Size()
	for i := uint(0); i < blockCount; i++ {
		e, _ := arr.Alloc()
		*e = *m.blockList.Ref(i)
	}

	buckets := ds.allocBuckets(len(m.buckets))
	copy(buckets, m.buckets)

	m.buckets = buckets
	m.blockList = arr
	m.disk = ds
	debug.Log("index map with %d entries moved to %v", m.numentries, indexDiskDir)
}
