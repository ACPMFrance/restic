package index

import (
	"math/rand"
	"os"
	"testing"

	"github.com/restic/restic/internal/repository/pack"
	"github.com/restic/restic/internal/restic"
	rtest "github.com/restic/restic/internal/test"
)

func useDiskStore(t testing.TB, minEntries uint) string {
	if !diskSupported {
		t.Skip("mapped index not supported")
	}
	diskDirOnce.Do(func() {})
	dir := t.TempDir()
	oldDir, oldMin := indexDiskDir, indexDiskMinEntries
	indexDiskDir, indexDiskMinEntries = dir, minEntries
	t.Cleanup(func() { indexDiskDir, indexDiskMinEntries = oldDir, oldMin })
	return dir
}

func TestIndexMapDiskBasic(t *testing.T) {
	dir := useDiskStore(t, 100)

	var (
		m   indexMap
		id  restic.ID
		r   = rand.New(rand.NewSource(1234))
		ids []restic.ID
	)
	const n = 3*diskBlockLen + 1234 // several segments and many bucket doublings
	for i := 1; i <= n; i++ {
		r.Read(id[:])
		ids = append(ids, id)
		m.add(id, uint32(i), uint32(i)*2, uint32(i)*3, uint32(i)*4)
	}
	rtest.Assert(t, m.disk != nil, "map did not move to disk")
	rtest.Equals(t, uint(n), m.len())

	for i, id := range ids {
		e := m.get(id)
		rtest.Assert(t, e != nil, "entry %d not found", i)
		rtest.Equals(t, uint32(i+1), e.packIndex)
		rtest.Equals(t, uint32(i+1)*2, e.offset)
		rtest.Equals(t, uint32(i+1)*3, e.length)
		rtest.Equals(t, uint32(i+1)*4, e.uncompressedLength)
	}
	var unknown restic.ID
	for range 1000 {
		r.Read(unknown[:])
		rtest.Assert(t, m.get(unknown) == nil, "unknown id found")
	}
	count := 0
	for range m.values() {
		count++
	}
	rtest.Equals(t, n, count)

	// the temporary files are unlinked
	entries, err := os.ReadDir(dir)
	rtest.OK(t, err)
	rtest.Equals(t, 0, len(entries))
}

func TestIndexMapDiskMovesExisting(t *testing.T) {
	useDiskStore(t, 1000)

	var m indexMap
	r := rand.New(rand.NewSource(5))
	var ids []restic.ID
	for i := 0; i < 900; i++ { // stays in memory
		var id restic.ID
		r.Read(id[:])
		ids = append(ids, id)
		m.add(id, uint32(i), 1, 2, 3)
	}
	rtest.Assert(t, m.disk == nil, "moved too early")
	for i := 900; i < 5000; i++ { // crosses the threshold
		var id restic.ID
		r.Read(id[:])
		ids = append(ids, id)
		m.add(id, uint32(i), 1, 2, 3)
	}
	rtest.Assert(t, m.disk != nil, "did not move to disk")
	for i, id := range ids {
		e := m.get(id)
		rtest.Assert(t, e != nil && e.packIndex == uint32(i), "entry %d lost or wrong", i)
	}
}

func TestIndexMapDiskFirstIndexAndDuplicates(t *testing.T) {
	useDiskStore(t, 10)

	var m indexMap
	var id restic.ID
	id[0] = 7
	for i := 0; i < 100; i++ {
		var other restic.ID
		other[0], other[1] = byte(i), 1
		m.add(other, 0, 0, 0, 0)
	}
	m.add(id, 1, 0, 0, 0)
	m.add(id, 2, 0, 0, 0)
	n := 0
	for range m.valuesWithID(id) {
		n++
	}
	rtest.Equals(t, 2, n)
	rtest.Assert(t, m.firstIndex(id) > 0, "firstIndex")
}

// The whole Index and MasterIndex machinery works on top of mapped maps.
func TestMasterIndexDiskMerge(t *testing.T) {
	useDiskStore(t, 50)

	mi := NewMasterIndex()
	r := rand.New(rand.NewSource(99))
	var handles []restic.BlobHandle
	for i := 0; i < 20; i++ {
		idx := NewIndex()
		var blobs pack.Blobs
		for j := 0; j < 100; j++ {
			var id restic.ID
			r.Read(id[:])
			bh := restic.BlobHandle{Type: restic.DataBlob, ID: id}
			handles = append(handles, bh)
			blobs = append(blobs, pack.Blob{BlobHandle: bh, Offset: uint(j * 10), Length: 10})
		}
		idx.StorePack(restic.NewRandomID(), blobs)
		idx.Finalize()
		rtest.OK(t, idx.SetID(restic.NewRandomID()))
		rtest.OK(t, mi.mergeLoaded(idx))
	}
	rtest.OK(t, mi.MergeFinalIndexes())
	for _, bh := range handles {
		_, found := mi.LookupSize(bh)
		rtest.Assert(t, found, "blob %v missing", bh)
		pbs := mi.Lookup(bh)
		rtest.Equals(t, 1, len(pbs))
	}
	rtest.Assert(t, mi.idx[0].byType[restic.DataBlob].disk != nil, "master index not on disk")
}
