/*
 * mskblob.c — reader for mskblob files in plain C. See mskblob.h.
 *
 * Standard C only (C99). Builds with MSVC, MinGW, gcc and clang, 32 or
 * 64 bits.
 */
#ifdef _MSC_VER
#define _CRT_SECURE_NO_WARNINGS
#endif

#include "mskblob.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <windows.h>
#else
#include <fcntl.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>
#endif

#define ALIGN 16

struct mskblob {
    const uint8_t *data;  /* the blob's bytes: a file mapping, or the caller's */
    size_t         len;
#ifdef _WIN32
    HANDLE         file;  /* the mapping, when mskblob_open made it */
    HANDLE         map;
#else
    int            fd;
#endif
    int            mapped;
    uint64_t       rd_offset; /* entry the read position belongs to (its offset) */
    uint64_t       rd_pos;    /* read position within that entry */
    mskblob_header hdr;
    mskblob_item  *items; /* hdr.count entries; strings point into the index copy */
    char          *strings; /* one buffer holding every NUL-terminated string */
};

/* --- little-endian readers ------------------------------------------------ */

static uint32_t rd32(const uint8_t *p)
{
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) | ((uint32_t)p[3] << 24);
}

static uint64_t rd64(const uint8_t *p)
{
    return (uint64_t)rd32(p) | ((uint64_t)rd32(p + 4) << 32);
}

/* --- crc32 (IEEE) --------------------------------------------------------- */

static uint32_t crc_table[256];
static int      crc_ready = 0;

static void crc_init(void)
{
    uint32_t i, j, c;
    for (i = 0; i < 256; i++) {
        c = i;
        for (j = 0; j < 8; j++)
            c = (c & 1) ? (0xEDB88320u ^ (c >> 1)) : (c >> 1);
        crc_table[i] = c;
    }
    crc_ready = 1;
}

uint32_t mskblob_crc32(uint32_t crc, const void *data, size_t len)
{
    const uint8_t *p = (const uint8_t *)data;
    if (!crc_ready)
        crc_init();
    crc = ~crc;
    while (len--)
        crc = crc_table[(crc ^ *p++) & 0xFF] ^ (crc >> 8);
    return ~crc;
}

/* --- parsing -------------------------------------------------------------- */

/* cstr reads a NUL-terminated string from rec[*q..end), advancing *q past the
 * NUL. A string running to the end without NUL is taken whole (as the Go
 * reader does). Returns its length. */
static size_t cstr(const uint8_t *rec, size_t end, size_t *q, const char **s)
{
    size_t start = *q, n;
    const uint8_t *z = start < end ? memchr(rec + start, 0, end - start) : NULL;
    if (z) {
        n = (size_t)(z - (rec + start));
        *q = start + n + 1;
    } else {
        n = end - start;
        *q = end;
    }
    *s = (const char *)rec + start;
    return n;
}

static int parse_index(mskblob *b)
{
    const uint8_t *buf;
    size_t idx_len, p, total = 0, sp = 0;
    uint32_t i;
    uint64_t off;

    if (b->hdr.data_offset < MSKBLOB_META || b->hdr.data_offset > b->len)
        return MSKBLOB_E_FORMAT;
    buf = b->data + MSKBLOB_META;
    idx_len = (size_t)b->hdr.data_offset - MSKBLOB_META;

    /* First pass: validate entry sizes and measure the strings. */
    for (i = 0, p = 0; i < b->hdr.count; i++) {
        size_t es, q;
        const char *s;
        if (p + 4 > idx_len)
            return MSKBLOB_E_TRUNCATED;
        es = rd32(buf + p);
        if (es < ALIGN || p + es > idx_len)
            return MSKBLOB_E_FORMAT;
        q = 4;
        total += cstr(buf + p, es, &q, &s) + 1;
        if (q + 16 > es)
            return MSKBLOB_E_FORMAT;
        q += 16;
        total += cstr(buf + p, es, &q, &s) + 1;
        total += cstr(buf + p, es, &q, &s) + 1;
        p += es;
    }

    b->items = (mskblob_item *)calloc(b->hdr.count ? b->hdr.count : 1, sizeof(mskblob_item));
    b->strings = (char *)malloc(total ? total : 1);
    if (!b->items || !b->strings)
        return MSKBLOB_E_MEMORY;

    /* Second pass: fill. */
    off = b->hdr.data_offset;
    for (i = 0, p = 0; i < b->hdr.count; i++) {
        mskblob_item *it = &b->items[i];
        size_t es = rd32(buf + p), q = 4, n;
        const char *s;
        const uint8_t *rec = buf + p;

        n = cstr(rec, es, &q, &s);
        memcpy(b->strings + sp, s, n); b->strings[sp + n] = 0;
        it->key = b->strings + sp; sp += n + 1;

        it->size    = rd64(rec + q); q += 8;
        it->crc32   = rd32(rec + q); q += 4;
        it->restype = (int32_t)rd32(rec + q); q += 4;

        n = cstr(rec, es, &q, &s);
        memcpy(b->strings + sp, s, n); b->strings[sp + n] = 0;
        it->url = b->strings + sp; sp += n + 1;

        n = cstr(rec, es, &q, &s);
        memcpy(b->strings + sp, s, n); b->strings[sp + n] = 0;
        it->filename = b->strings + sp; sp += n + 1;

        it->offset = off;
        if (it->size > b->len || off > b->len - it->size)
            return MSKBLOB_E_TRUNCATED;
        off += it->size;
        p += es;
    }
    return MSKBLOB_OK;
}

static int parse(mskblob *b)
{
    const uint8_t *m = b->data;
    if (b->len < MSKBLOB_META)
        return MSKBLOB_E_TRUNCATED;
    if (memcmp(m, MSKBLOB_MAGIC, 4) != 0)
        return MSKBLOB_E_MAGIC;
    if (m[4] != MSKBLOB_VERSION)
        return MSKBLOB_E_VERSION;
    b->hdr.version     = m[4];
    b->hdr.nocase      = (m[5] & 0x01) != 0;
    b->hdr.count       = rd32(m + 8);
    b->hdr.data_crc32  = rd32(m + 12);
    b->hdr.data_offset = rd32(m + 16);
    memcpy(b->hdr.id, m + MSKBLOB_HDR_SIZE, MSKBLOB_ID_SIZE);
    b->hdr.id[MSKBLOB_ID_SIZE] = 0;
    if (b->hdr.data_offset > b->len)
        return MSKBLOB_E_TRUNCATED;
    b->hdr.data_size = b->len - b->hdr.data_offset;
    return parse_index(b);
}

int mskblob_parse(const void *data, size_t len, mskblob **out)
{
    mskblob *b;
    int err;
    if (!data || !out)
        return MSKBLOB_E_ARGUMENT;
    *out = NULL;
    b = (mskblob *)calloc(1, sizeof(*b));
    if (!b)
        return MSKBLOB_E_MEMORY;
    b->data = (const uint8_t *)data;
    b->len = len;
    b->rd_offset = ~(uint64_t)0;
    if ((err = parse(b)) != MSKBLOB_OK) {
        mskblob_close(b);
        return err;
    }
    *out = b;
    return MSKBLOB_OK;
}

/* Mapping the file: read-only, shared, the whole file. A blob shorter than
 * its header cannot be one, and an empty file cannot even be mapped, so both
 * come back as truncated. */
#ifdef _WIN32

static int map_file(const char *path, mskblob *b)
{
    LARGE_INTEGER size;
    b->file = CreateFileA(path, GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE,
                          NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
    if (b->file == INVALID_HANDLE_VALUE)
        return MSKBLOB_E_IO;
    if (!GetFileSizeEx(b->file, &size))
        return MSKBLOB_E_IO;
    if (size.QuadPart < MSKBLOB_META)
        return MSKBLOB_E_TRUNCATED;
    if ((uint64_t)size.QuadPart > (uint64_t)(size_t)-1)
        return MSKBLOB_E_MEMORY; /* larger than the address space (32-bit) */
    b->map = CreateFileMappingA(b->file, NULL, PAGE_READONLY, 0, 0, NULL);
    if (!b->map)
        return MSKBLOB_E_IO;
    b->data = (const uint8_t *)MapViewOfFile(b->map, FILE_MAP_READ, 0, 0, 0);
    if (!b->data)
        return MSKBLOB_E_IO;
    b->len = (size_t)size.QuadPart;
    b->mapped = 1;
    return MSKBLOB_OK;
}

static void unmap_file(mskblob *b)
{
    if (b->data && b->mapped)
        UnmapViewOfFile(b->data);
    if (b->map)
        CloseHandle(b->map);
    if (b->file && b->file != INVALID_HANDLE_VALUE)
        CloseHandle(b->file);
}

#else

static int map_file(const char *path, mskblob *b)
{
    struct stat st;
    void *p;
    b->fd = open(path, O_RDONLY);
    if (b->fd < 0)
        return MSKBLOB_E_IO;
    if (fstat(b->fd, &st) != 0)
        return MSKBLOB_E_IO;
    if (st.st_size < MSKBLOB_META)
        return MSKBLOB_E_TRUNCATED;
    if ((uint64_t)st.st_size > (uint64_t)(size_t)-1)
        return MSKBLOB_E_MEMORY;
    p = mmap(NULL, (size_t)st.st_size, PROT_READ, MAP_PRIVATE, b->fd, 0);
    if (p == MAP_FAILED)
        return MSKBLOB_E_IO;
    b->data = (const uint8_t *)p;
    b->len = (size_t)st.st_size;
    b->mapped = 1;
    return MSKBLOB_OK;
}

static void unmap_file(mskblob *b)
{
    if (b->data && b->mapped)
        munmap((void *)b->data, b->len);
    if (b->fd > 0)
        close(b->fd);
}

#endif

int mskblob_open(const char *path, mskblob **out)
{
    mskblob *b;
    int err;
    if (!path || !out)
        return MSKBLOB_E_ARGUMENT;
    *out = NULL;
    b = (mskblob *)calloc(1, sizeof(*b));
    if (!b)
        return MSKBLOB_E_MEMORY;
#ifdef _WIN32
    b->file = INVALID_HANDLE_VALUE;
#else
    b->fd = -1;
#endif
    b->rd_offset = ~(uint64_t)0;
    if ((err = map_file(path, b)) != MSKBLOB_OK || (err = parse(b)) != MSKBLOB_OK) {
        mskblob_close(b);
        return err;
    }
    *out = b;
    return MSKBLOB_OK;
}

int mskblob_open_nested(const mskblob *parent, const mskblob_item *it, mskblob **out)
{
    if (!parent || !it || !out)
        return MSKBLOB_E_ARGUMENT;
    if (!(it->restype & MSKBLOB_MSKBLOB))
        return MSKBLOB_E_NOT_A_BLOB;
    return mskblob_parse(parent->data + it->offset, (size_t)it->size, out);
}

void mskblob_close(mskblob *b)
{
    if (!b)
        return;
    unmap_file(b);
    free(b->items);
    free(b->strings);
    free(b);
}

/* --- access --------------------------------------------------------------- */

const mskblob_header *mskblob_hdr(const mskblob *b) { return b ? &b->hdr : NULL; }
uint32_t mskblob_count(const mskblob *b) { return b ? b->hdr.count : 0; }

const mskblob_item *mskblob_at(const mskblob *b, uint32_t i)
{
    return (b && i < b->hdr.count) ? &b->items[i] : NULL;
}

static int str_eq(const char *a, const char *b, int nocase)
{
    if (!nocase)
        return strcmp(a, b) == 0;
    for (; *a && *b; a++, b++) {
        unsigned char x = (unsigned char)*a, y = (unsigned char)*b;
        if (x >= 'A' && x <= 'Z') x += 'a' - 'A';
        if (y >= 'A' && y <= 'Z') y += 'a' - 'A';
        if (x != y)
            return 0;
    }
    return *a == *b;
}

const mskblob_item *mskblob_find_key(const mskblob *b, const char *key)
{
    uint32_t i;
    if (!b || !key || !*key)
        return NULL;
    for (i = 0; i < b->hdr.count; i++)
        if (b->items[i].key[0] && str_eq(b->items[i].key, key, b->hdr.nocase))
            return &b->items[i];
    return NULL;
}

const mskblob_item *mskblob_find_url(const mskblob *b, const char *url)
{
    uint32_t i;
    if (!b || !url || !*url)
        return NULL;
    for (i = 0; i < b->hdr.count; i++)
        if (b->items[i].url[0] && str_eq(b->items[i].url, url, b->hdr.nocase))
            return &b->items[i];
    return NULL;
}

/* --- findfirst / findnext / findclose ------------------------------------- */

static int fold(int c, int nocase)
{
    return (nocase && c >= 'A' && c <= 'Z') ? c + ('a' - 'A') : c;
}

int mskblob_match(const char *pattern, const char *name, int nocase)
{
    const char *star = NULL, *mark = NULL;
    if (!pattern || !*pattern)
        return 1;
    while (*name) {
        if (*pattern == '*') {
            star = pattern++;
            mark = name;
        } else if (*pattern == '?' || fold(*pattern, nocase) == fold(*name, nocase)) {
            pattern++;
            name++;
        } else if (star) {
            pattern = star + 1;
            name = ++mark;
        } else {
            return 0;
        }
    }
    while (*pattern == '*')
        pattern++;
    return *pattern == 0;
}

static const char *item_name(const mskblob_item *it)
{
    return it->url[0] ? it->url : it->key;
}

int mskblob_findnext(mskblob_find *f)
{
    if (!f || !f->blob)
        return MSKBLOB_E_ARGUMENT;
    f->item = NULL;
    while (f->next < f->blob->hdr.count) {
        const mskblob_item *it = &f->blob->items[f->next++];
        if (mskblob_match(f->pattern, item_name(it), f->blob->hdr.nocase)) {
            f->item = it;
            return MSKBLOB_OK;
        }
    }
    return MSKBLOB_E_NOT_FOUND;
}

int mskblob_findfirst(const mskblob *b, const char *pattern, mskblob_find *f)
{
    if (!b || !f)
        return MSKBLOB_E_ARGUMENT;
    f->blob = b;
    f->pattern = (pattern && *pattern) ? pattern : NULL;
    f->next = 0;
    f->item = NULL;
    return mskblob_findnext(f);
}

void mskblob_findclose(mskblob_find *f)
{
    if (!f)
        return;
    f->blob = NULL;
    f->pattern = NULL;
    f->next = 0;
    f->item = NULL;
}

const void *mskblob_data(const mskblob *b, const mskblob_item *it)
{
    return (b && it) ? b->data + it->offset : NULL;
}

int mskblob_readseek(mskblob *b, const mskblob_item *it, uint64_t pos)
{
    if (!b || !it)
        return MSKBLOB_E_ARGUMENT;
    if (pos > it->size)
        pos = it->size;
    b->rd_offset = it->offset;
    b->rd_pos = pos;
    return MSKBLOB_OK;
}

int mskblob_readbytes(mskblob *b, const mskblob_item *it, void *buf, size_t len, size_t *got)
{
    size_t n;
    if (got)
        *got = 0;
    if (!b || !it || (!buf && len))
        return MSKBLOB_E_ARGUMENT;
    if (b->rd_offset != it->offset) { /* another entry: start it from the top */
        b->rd_offset = it->offset;
        b->rd_pos = 0;
    }
    n = mskblob_read(b, it, b->rd_pos, buf, len);
    b->rd_pos += n;
    if (got)
        *got = n;
    return MSKBLOB_OK;
}

size_t mskblob_read(const mskblob *b, const mskblob_item *it, uint64_t offset, void *buf, size_t len)
{
    uint64_t left;
    if (!b || !it || !buf || offset >= it->size)
        return 0;
    left = it->size - offset;
    if ((uint64_t)len > left)
        len = (size_t)left;
    memcpy(buf, b->data + it->offset + offset, len);
    return len;
}

int mskblob_readall(const mskblob *b, const mskblob_item *it, void **out, size_t *len)
{
    uint8_t *buf;
    int err;
    if (!b || !it || !out)
        return MSKBLOB_E_ARGUMENT;
    *out = NULL;
    if (len)
        *len = 0;
    if ((err = mskblob_verify(b, it)) != MSKBLOB_OK)
        return err;
    buf = (uint8_t *)malloc((size_t)it->size + 1);
    if (!buf)
        return MSKBLOB_E_MEMORY;
    memcpy(buf, b->data + it->offset, (size_t)it->size);
    buf[it->size] = 0;
    *out = buf;
    if (len)
        *len = (size_t)it->size;
    return MSKBLOB_OK;
}

int mskblob_verify(const mskblob *b, const mskblob_item *it)
{
    if (!b || !it)
        return MSKBLOB_E_ARGUMENT;
    return mskblob_crc32(0, b->data + it->offset, (size_t)it->size) == it->crc32
        ? MSKBLOB_OK : MSKBLOB_E_CRC;
}

int mskblob_verify_all(const mskblob *b)
{
    if (!b)
        return MSKBLOB_E_ARGUMENT;
    return mskblob_crc32(0, b->data + b->hdr.data_offset, (size_t)b->hdr.data_size) == b->hdr.data_crc32
        ? MSKBLOB_OK : MSKBLOB_E_CRC;
}

int mskblob_extract(const mskblob *b, const mskblob_item *it, const char *path)
{
    FILE *f;
    int err;
    if (!b || !it || !path)
        return MSKBLOB_E_ARGUMENT;
    if ((err = mskblob_verify(b, it)) != MSKBLOB_OK)
        return err;
    f = fopen(path, "wb");
    if (!f)
        return MSKBLOB_E_IO;
    if (it->size && fwrite(b->data + it->offset, 1, (size_t)it->size, f) != (size_t)it->size) {
        fclose(f);
        return MSKBLOB_E_IO;
    }
    return fclose(f) == 0 ? MSKBLOB_OK : MSKBLOB_E_IO;
}

/* --- names ---------------------------------------------------------------- */

const char *mskblob_restype_names(int32_t restype, char *buf)
{
    static const struct { int32_t bit; const char *name; } names[] = {
        { MSKBLOB_STATIC,  "static"  },
        { MSKBLOB_TPL,     "tpl"     },
        { MSKBLOB_PARSE,   "parse"   },
        { MSKBLOB_RSP,     "rsp"     },
        { MSKBLOB_NOMUX,   "nomux"   },
        { MSKBLOB_MSKBLOB, "mskblob" },
        { MSKBLOB_AUTO,    "auto"    },
    };
    size_t i, n = 0;
    buf[0] = 0;
    for (i = 0; i < sizeof(names) / sizeof(names[0]); i++) {
        if (restype & names[i].bit) {
            if (n)
                buf[n++] = ',';
            strcpy(buf + n, names[i].name);
            n += strlen(names[i].name);
        }
    }
    return buf;
}

const char *mskblob_strerror(int err)
{
    switch (err) {
    case MSKBLOB_OK:           return "ok";
    case MSKBLOB_E_IO:         return "cannot read or write file";
    case MSKBLOB_E_MEMORY:     return "out of memory";
    case MSKBLOB_E_MAGIC:      return "not a mskblob file (bad magic)";
    case MSKBLOB_E_VERSION:    return "unsupported mskblob version";
    case MSKBLOB_E_TRUNCATED:  return "blob is truncated";
    case MSKBLOB_E_FORMAT:     return "blob index is inconsistent";
    case MSKBLOB_E_CRC:        return "crc32 mismatch";
    case MSKBLOB_E_NOT_FOUND:  return "entry not found";
    case MSKBLOB_E_NOT_A_BLOB: return "entry is not a nested blob";
    case MSKBLOB_E_ARGUMENT:   return "invalid argument";
    default:                   return "unknown error";
    }
}
