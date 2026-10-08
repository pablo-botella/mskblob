/*
 * mskblob.ch — constants for the Harbour wrapper (hbmskblob.c).
 */
#ifndef MSKBLOB_CH
#define MSKBLOB_CH

/* Positions in an item array, as returned by MskBlobFindFirst/Next/Find. */
#define MSKBLOB_ITEM_KEY       1
#define MSKBLOB_ITEM_URL       2
#define MSKBLOB_ITEM_FILENAME  3
#define MSKBLOB_ITEM_SIZE      4
#define MSKBLOB_ITEM_OFFSET    5
#define MSKBLOB_ITEM_CRC32     6
#define MSKBLOB_ITEM_RESTYPE   7

/* Positions in the header array, as returned by MskBlobHeader. */
#define MSKBLOB_HDR_ID         1
#define MSKBLOB_HDR_VERSION    2
#define MSKBLOB_HDR_COUNT      3
#define MSKBLOB_HDR_NOCASE     4
#define MSKBLOB_HDR_DATACRC    5
#define MSKBLOB_HDR_DATASIZE   6

/* Resource-type flags. */
#define MSKBLOB_STATIC         0x0001
#define MSKBLOB_TPL            0x0002
#define MSKBLOB_PARSE          0x0004
#define MSKBLOB_RSP            0x0008
#define MSKBLOB_NOMUX          0x0010
#define MSKBLOB_MSKBLOB        0x0100
#define MSKBLOB_AUTO           0x0200

/* Error codes (MskBlobError). */
#define MSKBLOB_OK             0
#define MSKBLOB_E_IO          -1
#define MSKBLOB_E_MEMORY      -2
#define MSKBLOB_E_MAGIC       -3
#define MSKBLOB_E_VERSION     -4
#define MSKBLOB_E_TRUNCATED   -5
#define MSKBLOB_E_FORMAT      -6
#define MSKBLOB_E_CRC         -7
#define MSKBLOB_E_NOT_FOUND   -8
#define MSKBLOB_E_NOT_A_BLOB  -9
#define MSKBLOB_E_ARGUMENT   -10

#endif /* MSKBLOB_CH */
