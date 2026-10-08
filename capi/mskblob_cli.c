/*
 * mskblob_cli.c — command-line tool over mskblob.c: inspect, verify and
 * extract mskblob files without Go.
 *
 *   mskblob-c info   <file.blob>
 *   mskblob-c list   <file.blob> [pattern]
 *   mskblob-c verify <file.blob>
 *   mskblob-c dump   <file.blob> <outdir>
 *   mskblob-c get    <file.blob> <key> [outfile]
 *
 * Nested blobs (entries flagged "mskblob") are listed and verified in place;
 * dump writes them as files, like the Go tool does.
 */
#ifdef _MSC_VER
#define _CRT_SECURE_NO_WARNINGS
#endif

#include "mskblob.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <direct.h>
#include <fcntl.h>
#include <io.h>
#define MKDIR(p) _mkdir(p)
#define PATH_SEP '\\'
#else
#include <sys/stat.h>
#define MKDIR(p) mkdir(p, 0755)
#define PATH_SEP '/'
#endif

static int fail(const char *what, const char *arg, int err)
{
    if (arg)
        fprintf(stderr, "mskblob-c: %s %s: %s\n", what, arg, mskblob_strerror(err));
    else
        fprintf(stderr, "mskblob-c: %s: %s\n", what, mskblob_strerror(err));
    return 1;
}

/* entry_name is what to call an entry: its url, or its key when it has none. */
static const char *entry_name(const mskblob_item *it)
{
    return it->url[0] ? it->url : it->key;
}

/* --- info ----------------------------------------------------------------- */

static int cmd_info(const char *path)
{
    mskblob *b;
    const mskblob_header *h;
    int err = mskblob_open(path, &b);
    if (err)
        return fail("cannot open", path, err);
    h = mskblob_hdr(b);
    printf("blob:    %s\n", path);
    printf("version: %u\n", (unsigned)h->version);
    printf("id:      %s\n", h->id);
    printf("entries: %u\n", (unsigned)h->count);
    printf("nocase:  %s\n", h->nocase ? "true" : "false");
    printf("dataCRC: 0x%08x\n", (unsigned)h->data_crc32);
    printf("data:    %llu bytes\n", (unsigned long long)h->data_size);
    mskblob_close(b);
    return 0;
}

/* --- list ----------------------------------------------------------------- */

static void list_blob(const mskblob *b, const char *pattern, int depth)
{
    mskblob_find f;
    int rc;
    char names[MSKBLOB_RESTYPE_NAMES_MAX];
    for (rc = mskblob_findfirst(b, pattern, &f); rc == MSKBLOB_OK; rc = mskblob_findnext(&f)) {
        const mskblob_item *it = f.item;
        printf("%*s%-40s %12llu  0x%08x  %-14s %s\n", depth * 2, "",
               entry_name(it), (unsigned long long)it->size, (unsigned)it->crc32,
               mskblob_restype_names(it->restype, names),
               it->filename[0] ? it->filename : "");
        if (it->restype & MSKBLOB_MSKBLOB) {
            mskblob *child;
            if (mskblob_open_nested(b, it, &child) == MSKBLOB_OK) {
                printf("%*s  [nested blob %s, %u entries]\n", depth * 2, "",
                       mskblob_hdr(child)->id, (unsigned)mskblob_count(child));
                list_blob(child, pattern, depth + 1);
                mskblob_close(child);
            } else {
                printf("%*s  [nested blob: cannot parse]\n", depth * 2, "");
            }
        }
    }
    mskblob_findclose(&f);
}

static int cmd_list(const char *path, const char *pattern)
{
    mskblob *b;
    const mskblob_header *h;
    int err = mskblob_open(path, &b);
    if (err)
        return fail("cannot open", path, err);
    h = mskblob_hdr(b);
    printf("%s  %u entries  %llu bytes  nocase=%s\n\n", h->id, (unsigned)h->count,
           (unsigned long long)h->data_size, h->nocase ? "true" : "false");
    printf("%-40s %12s  %-10s  %-14s %s\n", "url/key", "size", "crc32", "restype", "filename");
    list_blob(b, pattern, 0);
    mskblob_close(b);
    return 0;
}

/* --- verify --------------------------------------------------------------- */

static int verify_blob(const mskblob *b, const char *label, int *bad)
{
    uint32_t i;
    int err = mskblob_verify_all(b);
    if (err) {
        printf("FAIL  %s: data region crc32 mismatch\n", label);
        (*bad)++;
    }
    for (i = 0; i < mskblob_count(b); i++) {
        const mskblob_item *it = mskblob_at(b, i);
        if ((err = mskblob_verify(b, it)) != MSKBLOB_OK) {
            printf("FAIL  %s: %s\n", label, entry_name(it));
            (*bad)++;
        }
        if (it->restype & MSKBLOB_MSKBLOB) {
            mskblob *child;
            if (mskblob_open_nested(b, it, &child) == MSKBLOB_OK) {
                verify_blob(child, entry_name(it), bad);
                mskblob_close(child);
            } else {
                printf("FAIL  %s: nested blob %s cannot be parsed\n", label, entry_name(it));
                (*bad)++;
            }
        }
    }
    return 0;
}

static int cmd_verify(const char *path)
{
    mskblob *b;
    int bad = 0;
    int err = mskblob_open(path, &b);
    if (err)
        return fail("cannot open", path, err);
    verify_blob(b, path, &bad);
    if (bad == 0)
        printf("OK    %s: %u entries, all checksums match\n", path, (unsigned)mskblob_count(b));
    mskblob_close(b);
    return bad ? 1 : 0;
}

/* --- dump ----------------------------------------------------------------- */

/* safe_rel turns an entry name into a relative path under the output dir:
 * leading slashes dropped, slashes turned into the native separator, and
 * anything that climbs out (..) refused. Returns 0 when unusable. */
static int safe_rel(const char *name, char *out, size_t cap)
{
    size_t n;
    const char *seg;
    while (*name == '/' || *name == '\\')
        name++;
    n = strlen(name);
    if (n == 0 || n + 1 > cap)
        return 0;
    strcpy(out, name);
    for (seg = out; *seg; ) {
        const char *end = seg;
        while (*end && *end != '/' && *end != '\\')
            end++;
        if (end - seg == 2 && seg[0] == '.' && seg[1] == '.')
            return 0;
        seg = *end ? end + 1 : end;
    }
    for (n = 0; out[n]; n++)
        if (out[n] == '/' || out[n] == '\\')
            out[n] = PATH_SEP;
    return 1;
}

/* mkdirs creates every directory in path up to the last separator. */
static void mkdirs(char *path)
{
    char *p;
    for (p = path + 1; *p; p++) {
        if (*p == PATH_SEP) {
            *p = 0;
            MKDIR(path);
            *p = PATH_SEP;
        }
    }
}

static int cmd_dump(const char *path, const char *outdir)
{
    mskblob *b;
    uint32_t i;
    int err = mskblob_open(path, &b), bad = 0;
    char rel[1024], full[2048];
    if (err)
        return fail("cannot open", path, err);
    MKDIR(outdir);
    for (i = 0; i < mskblob_count(b); i++) {
        const mskblob_item *it = mskblob_at(b, i);
        const char *name = entry_name(it);
        if (!safe_rel(name, rel, sizeof rel)) {
            fprintf(stderr, "mskblob-c: skipping entry %u with unusable name %s\n", (unsigned)i, name);
            bad++;
            continue;
        }
        if (strlen(outdir) + 1 + strlen(rel) + 1 > sizeof full) {
            fprintf(stderr, "mskblob-c: path too long for %s\n", name);
            bad++;
            continue;
        }
        strcpy(full, outdir);
        full[strlen(outdir)] = PATH_SEP;
        strcpy(full + strlen(outdir) + 1, rel);
        mkdirs(full);
        if ((err = mskblob_extract(b, it, full)) != MSKBLOB_OK) {
            fail("cannot extract", name, err);
            bad++;
            continue;
        }
        printf("%s\n", full);
    }
    mskblob_close(b);
    return bad ? 1 : 0;
}

/* --- get ------------------------------------------------------------------ */

static int cmd_get(const char *path, const char *key, const char *outfile)
{
    mskblob *b;
    const mskblob_item *it;
    int err = mskblob_open(path, &b);
    if (err)
        return fail("cannot open", path, err);
    it = mskblob_find_key(b, key);
    if (!it)
        it = mskblob_find_url(b, key);
    if (!it) {
        mskblob_close(b);
        return fail("no entry", key, MSKBLOB_E_NOT_FOUND);
    }
    if (outfile) {
        if ((err = mskblob_extract(b, it, outfile)) != MSKBLOB_OK) {
            mskblob_close(b);
            return fail("cannot extract", key, err);
        }
    } else {
        if ((err = mskblob_verify(b, it)) != MSKBLOB_OK) {
            mskblob_close(b);
            return fail("bad entry", key, err);
        }
        /* Streamed through mskblob_readbytes in chunks, the way a client would. */
        char chunk[4096];
        size_t n;
#ifdef _WIN32
        _setmode(_fileno(stdout), _O_BINARY);
#endif
        while (mskblob_readbytes(b, it, chunk, sizeof chunk, &n) == MSKBLOB_OK && n > 0) {
            if (fwrite(chunk, 1, n, stdout) != n) {
                mskblob_close(b);
                return fail("writing stdout", NULL, MSKBLOB_E_IO);
            }
        }
    }
    mskblob_close(b);
    return 0;
}

/* --- main ----------------------------------------------------------------- */

static int usage(void)
{
    fprintf(stderr,
        "Usage: mskblob-c <command> <file.blob> [args]\n"
        "\n"
        "  info   <file.blob>                 header: id, version, entries, nocase, data crc and size\n"
        "  list   <file.blob> [pattern]       header plus the entries, all or those matching * ? on url/key\n"
        "  verify <file.blob>                 check every crc32, nested blobs included\n"
        "  dump   <file.blob> <outdir>        extract every entry under outdir, by url (or key)\n"
        "  get    <file.blob> <key> [outfile] extract one entry (by key, else by url) to outfile or stdout\n");
    return 2;
}

int main(int argc, char **argv)
{
    const char *cmd;
    if (argc < 3)
        return usage();
    cmd = argv[1];
    if (strcmp(cmd, "info") == 0 && argc == 3)
        return cmd_info(argv[2]);
    if (strcmp(cmd, "list") == 0 && (argc == 3 || argc == 4))
        return cmd_list(argv[2], argc == 4 ? argv[3] : NULL);
    if (strcmp(cmd, "verify") == 0 && argc == 3)
        return cmd_verify(argv[2]);
    if (strcmp(cmd, "dump") == 0 && argc == 4)
        return cmd_dump(argv[2], argv[3]);
    if (strcmp(cmd, "get") == 0 && (argc == 4 || argc == 5))
        return cmd_get(argv[2], argv[3], argc == 5 ? argv[4] : NULL);
    return usage();
}
