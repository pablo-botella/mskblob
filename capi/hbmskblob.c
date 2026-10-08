/*
 * hbmskblob.c — Harbour wrapper over mskblob.c: the mskblob reader as
 * Harbour functions. Plain C against the Harbour API (hbapi.h); build it
 * together with mskblob.c, see hbmskblob.hbp.
 *
 *   hBlob := MskBlobOpen( cPath )                  --> blob handle, or NIL
 *   hBlob := MskBlobOpenNested( hBlob, aItem )     --> handle over a nested entry, or NIL
 *                                                  (an independent copy: the parent and the
 *                                                   child can be closed in any order)
 *   MskBlobClose( hBlob )                          (also closed by the garbage collector)
 *   MskBlobError( hBlob )                          --> last error code of that handle (0 = ok)
 *   MskBlobErrorStr( nError )                      --> text
 *
 *   aHdr  := MskBlobHeader( hBlob )                --> { id, version, count, nocase, dataCRC, dataSize }
 *
 *   aItem := MskBlobFindFirst( hBlob, [cPattern] ) --> item array, or NIL (nothing matches)
 *   aItem := MskBlobFindNext( hBlob )              --> next item, or NIL (no more)
 *   MskBlobFindClose( hBlob )
 *   aItem := MskBlobFind( hBlob, cKeyOrUrl )       --> exact lookup by key, else by url, or NIL
 *
 *   cData := MskBlobReadBytes( hBlob, aItem, nLen ) --> up to nLen bytes, "" at the end (like FRead)
 *   MskBlobReadSeek( hBlob, aItem, nPos )
 *   cData := MskBlobReadAll( hBlob, aItem )        --> the whole entry, verified, or NIL
 *   MskBlobVerify( hBlob, aItem )                  --> .T. when the crc32 matches
 *   MskBlobVerifyAll( hBlob )                      --> .T. when the data region crc32 matches
 *   MskBlobExtract( hBlob, aItem, cPath )          --> .T. when written
 *   MskBlobRestypeNames( nRestype )                --> "static,parse"
 *
 * An item array has the MSKBLOB_ITEM_* positions of mskblob.ch:
 *   { key, url, filename, size, offset, crc32, restype }
 * Arrays are the caller's: keep as many as needed, pass any of them back.
 */
#include "hbapi.h"
#include "hbapiitm.h"
#include "hbapierr.h"

#include "mskblob.h"

#include <stdlib.h>
#include <string.h>

/* The handle: the reader, one find walk and the last error. A nested
 * handle also owns a copy of its bytes (owned), so it never depends on
 * the parent mapping staying open. */
typedef struct {
    mskblob     *blob;
    void        *owned;
    mskblob_find find;
    int          finding;
    int          error;
} hb_mskblob;

/* close_handle releases the reader and the owned copy; safe to call twice. */
static void close_handle(hb_mskblob *h)
{
    if (h->blob) {
        mskblob_close(h->blob);
        h->blob = NULL;
    }
    if (h->owned) {
        free(h->owned);
        h->owned = NULL;
    }
}

static HB_GARBAGE_FUNC(hb_mskblob_destructor)
{
    close_handle((hb_mskblob *)Cargo);
}

static const HB_GC_FUNCS s_gcMskblob = { hb_mskblob_destructor, hb_gcDummyMark };

static hb_mskblob *get_handle(int iParam)
{
    hb_mskblob *h = (hb_mskblob *)hb_parptrGC(&s_gcMskblob, iParam);
    if (!h || !h->blob) {
        hb_errRT_BASE(EG_ARG, 3012, "not an open mskblob handle", HB_ERR_FUNCNAME, HB_ERR_ARGS_BASEPARAMS);
        return NULL;
    }
    return h;
}

static void ret_handle(mskblob *b, void *owned)
{
    hb_mskblob *h = (hb_mskblob *)hb_gcAllocate(sizeof(hb_mskblob), &s_gcMskblob);
    memset(h, 0, sizeof(*h));
    h->blob = b;
    h->owned = owned;
    hb_retptrGC(h);
}

/* item_to_array builds the Harbour array for an entry. */
static PHB_ITEM item_to_array(const mskblob_item *it)
{
    PHB_ITEM a = hb_itemArrayNew(7);
    hb_arraySetC(a, 1, it->key);
    hb_arraySetC(a, 2, it->url);
    hb_arraySetC(a, 3, it->filename);
    hb_arraySetNInt(a, 4, (HB_MAXINT)it->size);
    hb_arraySetNInt(a, 5, (HB_MAXINT)it->offset);
    hb_arraySetNInt(a, 6, (HB_MAXINT)it->crc32);
    hb_arraySetNI(a, 7, (int)it->restype);
    return a;
}

/* array_to_item rebuilds an entry from the array at iParam. The strings
 * point into the array, so the item is valid only during the call. Returns
 * 0 when the parameter is not an item array. */
static int array_to_item(int iParam, mskblob_item *it)
{
    PHB_ITEM a = hb_param(iParam, HB_IT_ARRAY);
    if (!a || hb_arrayLen(a) < 7)
        return 0;
    it->key      = hb_arrayGetCPtr(a, 1);
    it->url      = hb_arrayGetCPtr(a, 2);
    it->filename = hb_arrayGetCPtr(a, 3);
    it->size     = (uint64_t)hb_arrayGetNInt(a, 4);
    it->offset   = (uint64_t)hb_arrayGetNInt(a, 5);
    it->crc32    = (uint32_t)hb_arrayGetNInt(a, 6);
    it->restype  = (int32_t)hb_arrayGetNI(a, 7);
    return 1;
}

/* item_param rebuilds the entry at iParam and checks that it lies inside
 * the blob's data region: the array is the caller's and may have been
 * edited, and the C reader trusts offset and size. */
static mskblob_item *item_param(hb_mskblob *h, int iParam, mskblob_item *it)
{
    const mskblob_header *hdr;
    if (!array_to_item(iParam, it)) {
        h->error = MSKBLOB_E_ARGUMENT;
        return NULL;
    }
    hdr = mskblob_hdr(h->blob);
    if (it->offset < hdr->data_offset || it->offset - hdr->data_offset > hdr->data_size
        || it->size > hdr->data_size - (it->offset - hdr->data_offset)) {
        h->error = MSKBLOB_E_ARGUMENT;
        return NULL;
    }
    return it;
}

/* --- open / close --------------------------------------------------------- */

HB_FUNC(MSKBLOBOPEN)
{
    mskblob *b;
    const char *path = hb_parc(1);
    if (!path) {
        hb_errRT_BASE(EG_ARG, 3012, NULL, HB_ERR_FUNCNAME, HB_ERR_ARGS_BASEPARAMS);
        return;
    }
    if (mskblob_open(path, &b) == MSKBLOB_OK)
        ret_handle(b, NULL);
    else
        hb_ret();
}

/* The nested blob is parsed over a verified copy of its bytes, not over
 * the parent mapping: the child handle stays valid whatever happens to
 * the parent, so the garbage collector can release either one first. */
HB_FUNC(MSKBLOBOPENNESTED)
{
    hb_mskblob *h = get_handle(1);
    mskblob_item it;
    mskblob *child;
    void *copy;
    size_t len;
    if (!h)
        return;
    if (!item_param(h, 2, &it)) {
        hb_ret();
        return;
    }
    if (!(it.restype & MSKBLOB_MSKBLOB)) {
        h->error = MSKBLOB_E_NOT_A_BLOB;
        hb_ret();
        return;
    }
    if ((h->error = mskblob_readall(h->blob, &it, &copy, &len)) != MSKBLOB_OK) {
        hb_ret();
        return;
    }
    if ((h->error = mskblob_parse(copy, len, &child)) != MSKBLOB_OK) {
        free(copy);
        hb_ret();
        return;
    }
    ret_handle(child, copy);
}

HB_FUNC(MSKBLOBCLOSE)
{
    hb_mskblob *h = (hb_mskblob *)hb_parptrGC(&s_gcMskblob, 1);
    if (h)
        close_handle(h);
    hb_retl(h != NULL);
}

HB_FUNC(MSKBLOBERROR)
{
    hb_mskblob *h = (hb_mskblob *)hb_parptrGC(&s_gcMskblob, 1);
    hb_retni(h ? h->error : MSKBLOB_E_ARGUMENT);
}

HB_FUNC(MSKBLOBERRORSTR)
{
    hb_retc(mskblob_strerror(hb_parni(1)));
}

/* --- header --------------------------------------------------------------- */

HB_FUNC(MSKBLOBHEADER)
{
    hb_mskblob *h = get_handle(1);
    const mskblob_header *hdr;
    PHB_ITEM a;
    if (!h)
        return;
    hdr = mskblob_hdr(h->blob);
    a = hb_itemArrayNew(6);
    hb_arraySetC(a, 1, hdr->id);
    hb_arraySetNI(a, 2, hdr->version);
    hb_arraySetNInt(a, 3, (HB_MAXINT)hdr->count);
    hb_arraySetL(a, 4, hdr->nocase != 0);
    hb_arraySetNInt(a, 5, (HB_MAXINT)hdr->data_crc32);
    hb_arraySetNInt(a, 6, (HB_MAXINT)hdr->data_size);
    hb_itemReturnRelease(a);
}

/* --- findfirst / findnext / findclose ------------------------------------- */

HB_FUNC(MSKBLOBFINDFIRST)
{
    hb_mskblob *h = get_handle(1);
    if (!h)
        return;
    h->finding = 1;
    h->error = mskblob_findfirst(h->blob, hb_parc(2), &h->find);
    if (h->error == MSKBLOB_OK)
        hb_itemReturnRelease(item_to_array(h->find.item));
    else
        hb_ret();
}

HB_FUNC(MSKBLOBFINDNEXT)
{
    hb_mskblob *h = get_handle(1);
    if (!h)
        return;
    if (!h->finding) {
        h->error = MSKBLOB_E_NOT_FOUND;
        hb_ret();
        return;
    }
    h->error = mskblob_findnext(&h->find);
    if (h->error == MSKBLOB_OK)
        hb_itemReturnRelease(item_to_array(h->find.item));
    else
        hb_ret();
}

HB_FUNC(MSKBLOBFINDCLOSE)
{
    hb_mskblob *h = get_handle(1);
    if (!h)
        return;
    mskblob_findclose(&h->find);
    h->finding = 0;
    hb_retl(HB_TRUE);
}

HB_FUNC(MSKBLOBFIND)
{
    hb_mskblob *h = get_handle(1);
    const char *name = hb_parc(2);
    const mskblob_item *it;
    if (!h)
        return;
    it = name ? mskblob_find_key(h->blob, name) : NULL;
    if (!it && name)
        it = mskblob_find_url(h->blob, name);
    if (it) {
        h->error = MSKBLOB_OK;
        hb_itemReturnRelease(item_to_array(it));
    } else {
        h->error = name ? MSKBLOB_E_NOT_FOUND : MSKBLOB_E_ARGUMENT;
        hb_ret();
    }
}

/* --- reading -------------------------------------------------------------- */

HB_FUNC(MSKBLOBREADBYTES)
{
    hb_mskblob *h = get_handle(1);
    mskblob_item it;
    HB_MAXINT len = hb_parnint(3);
    char *buf;
    size_t got = 0;
    if (!h)
        return;
    if (!item_param(h, 2, &it) || len < 0) {
        hb_retc_null();
        return;
    }
    buf = (char *)hb_xgrab((HB_SIZE)len + 1);
    h->error = mskblob_readbytes(h->blob, &it, buf, (size_t)len, &got);
    if (h->error == MSKBLOB_OK)
        hb_retclen_buffer(buf, (HB_SIZE)got);
    else {
        hb_xfree(buf);
        hb_retc_null();
    }
}

HB_FUNC(MSKBLOBREADSEEK)
{
    hb_mskblob *h = get_handle(1);
    mskblob_item it;
    if (!h)
        return;
    if (!item_param(h, 2, &it)) {
        hb_retl(HB_FALSE);
        return;
    }
    h->error = mskblob_readseek(h->blob, &it, (uint64_t)hb_parnint(3));
    hb_retl(h->error == MSKBLOB_OK);
}

HB_FUNC(MSKBLOBREADALL)
{
    hb_mskblob *h = get_handle(1);
    mskblob_item it;
    void *p;
    size_t n;
    if (!h)
        return;
    if (!item_param(h, 2, &it) || (h->error = mskblob_readall(h->blob, &it, &p, &n)) != MSKBLOB_OK) {
        hb_ret();
        return;
    }
    hb_retclen(p, (HB_SIZE)n);
    free(p);
}

HB_FUNC(MSKBLOBVERIFY)
{
    hb_mskblob *h = get_handle(1);
    mskblob_item it;
    if (!h)
        return;
    if (!item_param(h, 2, &it)) {
        hb_retl(HB_FALSE);
        return;
    }
    h->error = mskblob_verify(h->blob, &it);
    hb_retl(h->error == MSKBLOB_OK);
}

HB_FUNC(MSKBLOBVERIFYALL)
{
    hb_mskblob *h = get_handle(1);
    if (!h)
        return;
    h->error = mskblob_verify_all(h->blob);
    hb_retl(h->error == MSKBLOB_OK);
}

HB_FUNC(MSKBLOBEXTRACT)
{
    hb_mskblob *h = get_handle(1);
    mskblob_item it;
    const char *path = hb_parc(3);
    if (!h)
        return;
    if (!item_param(h, 2, &it) || !path) {
        h->error = MSKBLOB_E_ARGUMENT;
        hb_retl(HB_FALSE);
        return;
    }
    h->error = mskblob_extract(h->blob, &it, path);
    hb_retl(h->error == MSKBLOB_OK);
}

HB_FUNC(MSKBLOBRESTYPENAMES)
{
    char buf[MSKBLOB_RESTYPE_NAMES_MAX];
    hb_retc(mskblob_restype_names((int32_t)hb_parni(1), buf));
}
