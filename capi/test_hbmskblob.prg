/*
 * test_hbmskblob.prg — exercises the Harbour wrapper against a blob.
 *   hbmk2 test_hbmskblob.hbp
 *   test_hbmskblob <file.blob> [pattern]
 * Lists the entries, streams each one through MskBlobReadBytes and checks
 * the bytes against MskBlobReadAll and the crc32. Exit code 0 when all good.
 */
#include "mskblob.ch"

PROCEDURE Main( cBlob, cPattern )
   LOCAL hBlob, aHdr, aItem, cAll, cChunk, cStream, nBad := 0, nSeen := 0

   IF Empty( cBlob )
      ? "usage: test_hbmskblob <file.blob> [pattern]"
      ErrorLevel( 2 )
      RETURN
   ENDIF

   hBlob := MskBlobOpen( cBlob )
   IF hBlob == NIL
      ? "cannot open", cBlob
      ErrorLevel( 1 )
      RETURN
   ENDIF

   aHdr := MskBlobHeader( hBlob )
   ? aHdr[ MSKBLOB_HDR_ID ], aHdr[ MSKBLOB_HDR_COUNT ], "entries", aHdr[ MSKBLOB_HDR_DATASIZE ], "bytes", ;
     "nocase=" + iif( aHdr[ MSKBLOB_HDR_NOCASE ], "true", "false" )
   ? "data region:", iif( MskBlobVerifyAll( hBlob ), "ok", "CRC MISMATCH" )
   ?

   aItem := MskBlobFindFirst( hBlob, cPattern )
   DO WHILE aItem != NIL
      nSeen++
      // Stream in small chunks, the ReadFile way, and compare with the whole copy.
      cStream := ""
      DO WHILE Len( cChunk := MskBlobReadBytes( hBlob, aItem, 1000 ) ) > 0
         cStream += cChunk
      ENDDO
      cAll := MskBlobReadAll( hBlob, aItem )
      ? PadR( iif( Empty( aItem[ MSKBLOB_ITEM_URL ] ), aItem[ MSKBLOB_ITEM_KEY ], aItem[ MSKBLOB_ITEM_URL ] ), 40 ), ;
        Str( aItem[ MSKBLOB_ITEM_SIZE ], 10 ), ;
        PadR( MskBlobRestypeNames( aItem[ MSKBLOB_ITEM_RESTYPE ] ), 14 ), ;
        iif( MskBlobVerify( hBlob, aItem ), "crc ok ", "CRC BAD" ), ;
        iif( cAll != NIL .AND. cStream == cAll .AND. Len( cAll ) == aItem[ MSKBLOB_ITEM_SIZE ], "stream ok", "STREAM MISMATCH" )
      IF cAll == NIL .OR. !( cStream == cAll ) .OR. !MskBlobVerify( hBlob, aItem )
         nBad++
      ENDIF
      aItem := MskBlobFindNext( hBlob )
   ENDDO
   MskBlobFindClose( hBlob )

   // Exact lookup of the first entry by key, then by url.
   aItem := MskBlobFindFirst( hBlob )
   IF aItem != NIL
      IF MskBlobFind( hBlob, aItem[ MSKBLOB_ITEM_KEY ] ) == NIL .AND. MskBlobFind( hBlob, aItem[ MSKBLOB_ITEM_URL ] ) == NIL
         ? "MskBlobFind failed for", aItem[ MSKBLOB_ITEM_KEY ]
         nBad++
      ENDIF
   ENDIF
   MskBlobFindClose( hBlob )

   ?
   ? nSeen, "entries,", nBad, "bad"
   MskBlobClose( hBlob )
   ErrorLevel( iif( nBad == 0, 0, 1 ) )

   RETURN
