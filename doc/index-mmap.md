Memory-mapped blob index (fork)
===============================

Upstream restic keeps the whole blob index in the Go heap (~135-150 B per
blob measured, 56 B per index entry). This fork can instead keep the large hash
tables in unlinked temporary files that are memory-mapped, so the kernel pages
them in and out and the Go heap stays small.

Configuration (environment variables, off by default, unix only):

* `RESTIC_INDEX_MMAP_DIR`: directory for the temporary files. Enables the
  feature. Must be on a real disk with room for roughly 80 B per blob (the
  files are sparse and unlinked at creation, nothing is left behind after a
  crash). Do not use tmpfs: it is memory again.
* `RESTIC_INDEX_MMAP_MIN_ENTRIES`: a blob type switches to disk once it has
  this many entries (default 1,000,000).

What it changes when enabled:

* buckets and entries of big index maps live in mapped files;
* index files are merged right after decoding instead of all being kept
  until the end of loading;
* the zstd decoder and the parallel index decoding are limited to 4 workers
  (one decoder state per core retained ~500 MiB on 22 cores).

Measured (22 cores, local repository, 5,000,000 files of 0.5-4 KiB, 20M+ blobs):

| | memory (upstream) | mapped |
|---|---|---|
| 2nd backup, startup peak (RssAnon) | 1108 MiB | 218 MiB |
| 2nd backup, steady heap | 705 MiB | 50 MiB |
| 1st backup, peak | 1201 MiB, growing | 644 MiB, flat |

The remaining heap of the first backup does not grow with the number of files:
it is the blob buffer pool and the zstd encoder, which scale with the core
count (`GOMAXPROCS` lowers them). Mapped pages show up as `RssFile`
(reclaimable); `/usr/bin/time -v` "Maximum resident" counts both.

Upstream rejected a similar approach (issue #1988), so this lives on the
`nc/index-mmap` branch and has to be rebased on new releases.
