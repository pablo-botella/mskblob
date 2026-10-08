/*
 * mskblob.h — reader for mskblob files in plain C.
 *
 * mskblob is a simple read-only pack format (github.com/pablo-botella/mskblob):
 * a 64-byte header, a 64-byte GUID, an index of entries (key, size, crc32,
 * resource-type flags, url, filename) and the entries' bytes concatenated,
 * uncompressed. This reader parses a blob held in memory, so it needs nothing
 * beyond the C standard library and works the same on a file read from disk,
 * on bytes just downloaded, or on an entry of another blob (nested blobs).
 *
 * mskblob_open maps the file into memory read-only (MapViewOfFile on
 * Windows, mmap elsewhere): nothing is copied, the index is parsed in place
 * and entry bytes are read straight from the mapping. mskblob_parse does the
 * same over bytes the caller already holds, which is how a nested blob is
 * opened in place too.
 *
 * On-disk format (little-endian):
 *
 *   BLOCK A — header (64 bytes):
 *     0   magic       "MSPK" (4)
 *     4   version     uint8   (1)
 *     5   flags       uint8   bit 0 = nocase (key/url lookups fold case)
 *     8   count       uint32  number of index entries
 *     12  dataCRC     uint32  crc32 (IEEE) of block D
 *     16  dataOffset  uint32  absolute offset where block D begins
 *   BLOCK B — id (64 bytes): GUID as ASCII, zero-padded
 *   BLOCK C — index (count entries, each padded to a 16-byte boundary):
 *     entrysize uint32   total bytes of this entry (incl. itself + pad)
 *     key       string\0
 *     size      uint64   data byte length
 *     crc32     uint32   crc32 of the data
 *     restype   int32    resource-type flags
 *     url       string\0 http route, relative to the blob's base
 *     filename  string\0 source filename
 *   BLOCK D — data: concatenated bytes at dataOffset; each entry's offset is
 *     the running sum of sizes.
 *
 * All functions return 0 (MSKBLOB_OK) on success or a negative mskblob_error.
 */
#ifndef MSKBLOB_H
#define MSKBLOB_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define MSKBLOB_MAGIC    "MSPK"
#define MSKBLOB_VERSION  1
#define MSKBLOB_HDR_SIZE 64
#define MSKBLOB_ID_SIZE  64
#define MSKBLOB_META     (MSKBLOB_HDR_SIZE + MSKBLOB_ID_SIZE)

/* Resource-type flags (mirroring the Go package). */
#define MSKBLOB_STATIC   0x0001
#define MSKBLOB_TPL      0x0002
#define MSKBLOB_PARSE    0x0004
#define MSKBLOB_RSP      0x0008
#define MSKBLOB_NOMUX    0x0010
#define MSKBLOB_MSKBLOB  0x0100 /* the entry's bytes are themselves a blob */
#define MSKBLOB_AUTO     0x0200 /* the blob's self-contained server configuration */

typedef enum {
    MSKBLOB_OK            =  0,
    MSKBLOB_E_IO          = -1, /* cannot read or write a file */
    MSKBLOB_E_MEMORY      = -2, /* malloc failed */
    MSKBLOB_E_MAGIC       = -3, /* not a blob */
    MSKBLOB_E_VERSION     = -4, /* unsupported version */
    MSKBLOB_E_TRUNCATED   = -5, /* header, index or data cut short */
    MSKBLOB_E_FORMAT      = -6, /* inconsistent index */
    MSKBLOB_E_CRC         = -7, /* checksum mismatch */
    MSKBLOB_E_NOT_FOUND   = -8, /* no such entry */
    MSKBLOB_E_NOT_A_BLOB  = -9, /* entry is not flagged as a nested blob */
    MSKBLOB_E_ARGUMENT    = -10 /* NULL or otherwise invalid argument */
} mskblob_error;

typedef struct {
    const char *key;      /* asset key ("" when none) */
    const char *url;      /* http route, relative to the blob's base ("" when none) */
    const char *filename; /* source filename ("" when none) */
    uint64_t    size;     /* data byte length */
    uint64_t    offset;   /* data offset from the start of the blob's bytes */
    uint32_t    crc32;    /* crc32 (IEEE) of the data */
    int32_t     restype;  /* MSKBLOB_* flags */
} mskblob_item;

typedef struct {
    uint8_t  version;
    int      nocase;      /* 1 when key/url lookups fold case */
    char     id[MSKBLOB_ID_SIZE + 1]; /* GUID, NUL-terminated */
    uint32_t count;
    uint32_t data_crc32;
    uint64_t data_offset;
    uint64_t data_size;   /* bytes in block D */
} mskblob_header;

typedef struct mskblob mskblob;

/* mskblob_parse builds a reader over len bytes at data. The bytes are NOT
 * copied: they must outlive the reader. Use it on a download, a resource or
 * a nested entry. */
int mskblob_parse(const void *data, size_t len, mskblob **out);

/* mskblob_open maps the file read-only and parses it. The mapping is
 * released in mskblob_close. */
int mskblob_open(const char *path, mskblob **out);

/* mskblob_open_nested parses the entry (which must carry MSKBLOB_MSKBLOB) of
 * parent as a blob of its own. The child borrows the parent's bytes: close
 * the child first. */
int mskblob_open_nested(const mskblob *parent, const mskblob_item *it, mskblob **out);

void mskblob_close(mskblob *b);

const mskblob_header *mskblob_hdr(const mskblob *b);
uint32_t              mskblob_count(const mskblob *b);
const mskblob_item   *mskblob_at(const mskblob *b, uint32_t i);

/* Lookups honour the blob's nocase flag. NULL when not found. */
const mskblob_item *mskblob_find_key(const mskblob *b, const char *key);
const mskblob_item *mskblob_find_url(const mskblob *b, const char *url);

/* Enumeration in the findfirst / findnext / findclose style.
 *
 *   mskblob_find f;
 *   int rc = mskblob_findfirst(b, "*.msi", &f);
 *   while (rc == MSKBLOB_OK) {
 *       ... f.item->url, f.item->size, mskblob_data(b, f.item) ...
 *       rc = mskblob_findnext(&f);
 *   }
 *   mskblob_findclose(&f);
 *
 * pattern matches the entry's name (its url, or its key when it has no url)
 * with * and ? wildcards, folding case when the blob is nocase; NULL or ""
 * matches every entry. Both calls return MSKBLOB_OK with f.item set, or
 * MSKBLOB_E_NOT_FOUND when there is nothing (more) to return. */
typedef struct {
    const mskblob      *blob;
    const char         *pattern;
    uint32_t            next;  /* index of the next entry to examine */
    const mskblob_item *item;  /* the entry found, or NULL */
} mskblob_find;

int  mskblob_findfirst(const mskblob *b, const char *pattern, mskblob_find *f);
int  mskblob_findnext(mskblob_find *f);
void mskblob_findclose(mskblob_find *f);

/* mskblob_match reports whether name matches pattern (* and ? wildcards),
 * folding case when nocase is set. */
int mskblob_match(const char *pattern, const char *name, int nocase);

/* mskblob_data returns a pointer to the entry's bytes inside the blob
 * (it->size bytes). No copy, no check. */
const void *mskblob_data(const mskblob *b, const mskblob_item *it);

/* mskblob_readbytes reads an entry the way ReadFile reads a file, bounded
 * to that entry: the first call for an entry starts at its first byte, each
 * call copies up to len bytes into buf, sets *got to the number copied and
 * advances; at the end *got is 0. Reading a different entry starts that one
 * from its first byte. mskblob_readseek moves the position within the entry
 * (pos = 0 restarts it). The blob keeps one read position. */
int mskblob_readbytes(mskblob *b, const mskblob_item *it, void *buf, size_t len, size_t *got);
int mskblob_readseek(mskblob *b, const mskblob_item *it, uint64_t pos);

/* mskblob_read copies up to len bytes of the entry starting at offset bytes
 * into it, without touching the read position, and returns the number
 * copied: fewer than len at the end, 0 once past it. */
size_t mskblob_read(const mskblob *b, const mskblob_item *it, uint64_t offset, void *buf, size_t len);

/* mskblob_readall returns the whole entry as a NUL-terminated copy the
 * caller frees with free(): *out gets the bytes (it->size of them, plus a
 * NUL that is not counted), *len the size when len is not NULL. The crc32
 * is verified first. */
int mskblob_readall(const mskblob *b, const mskblob_item *it, void **out, size_t *len);

/* mskblob_verify checks one entry's crc32; mskblob_verify_all checks the
 * whole data region against the header's dataCRC. */
int mskblob_verify(const mskblob *b, const mskblob_item *it);
int mskblob_verify_all(const mskblob *b);

/* mskblob_extract writes the entry's bytes to path (created or truncated),
 * verifying the crc32 first. */
int mskblob_extract(const mskblob *b, const mskblob_item *it, const char *path);

/* mskblob_restype_names fills buf with the comma-separated flag names of
 * restype ("static,parse"; "" when none) and returns buf. buf needs at least
 * MSKBLOB_RESTYPE_NAMES_MAX bytes. */
#define MSKBLOB_RESTYPE_NAMES_MAX 48
const char *mskblob_restype_names(int32_t restype, char *buf);

const char *mskblob_strerror(int err);

/* crc32 (IEEE 802.3, the one Go's hash/crc32 uses), exposed for callers that
 * stream data. Start with crc = 0 and feed chunks in order. */
uint32_t mskblob_crc32(uint32_t crc, const void *data, size_t len);

#ifdef __cplusplus
}
#endif

#endif /* MSKBLOB_H */
