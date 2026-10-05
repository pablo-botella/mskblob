// Package mskblob is an external asset container: a self-describing pack file
// that bundles many resources (their bytes plus a full index) into one sidecar
// artifact kept outside the Go binary. A build tool uses it to write blobs, and
// a consuming application imports it to load and serve them at runtime.
//
// It is the runtime/format companion of miniskin (github.com/pablo-botella/miniskin —
// https://pkg.go.dev/github.com/pablo-botella/miniskin), the build-time assembler that
// produces these .blob files; miniskin supports blobs using this component from
// v0.3.12. mskblob is standalone with no dependencies.
//
// # Manifest
//
// An [Item] is the single unit of both a manifest and a blob's index. A
// [Manifest] (an optional id plus items) is the interchange format: build a blob
// from one with [Write], or produce one from a blob with [Blob.Manifest]. The
// declarative fields (URL, Key, Filename, RestType, Src) say what to pack;
// Size/CRC32/Offset are computed by the writer — informational in a manifest and
// ignored when building.
//
// # Format
//
//	BLOCK A — header (64 bytes, little-endian):
//	  0   magic       "MSPK" (4)
//	  4   version     uint8
//	  5   flags       uint8   (reserved)
//	  6   reserved    [2]byte
//	  8   count       uint32  number of index entries
//	  12  dataCRC     uint32  crc32(IEEE) of block D
//	  16  dataOffset  uint32  absolute offset where block D begins
//	  20  reserved    pad to 64
//	BLOCK B — id (64 bytes): guid ASCII, zero-padded (the sync token)
//	BLOCK C — index (count entries, each 16-byte aligned):
//	  entrysize uint32   total bytes of this entry (incl. itself + pad)
//	  key       string\0 asset key
//	  size      uint64   data byte length
//	  crc32     uint32   crc32 of the data (also the ETag)
//	  restype   int32    resource-type flags (Static, Parse, …)
//	  url       string\0 http route, RELATIVE to the blob's base
//	  filename  string\0 source filename
//	  pad       \0…      to the next 16-byte boundary
//	BLOCK D — data: concatenated bytes at dataOffset; each entry's offset is the
//	  running sum of sizes.
package mskblob

// The documentation is not automatic — this makes it: `go generate ./...`
// rebuilds every artifact from _mkskill/ (README, AGENTS.md, the skill and
// the cmd README; the tool version is pinned by go.mod), and the golden
// test fails when anything went stale. The second line propagates the
// version-spec's destinations (__publish.bat) without touching the version.
//go:generate go run github.com/pablo-botella/mkskill/cmd/mkskill -q build
//go:generate go run github.com/pablo-botella/mkskill/cmd/mkskill -q -vbuild

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	magic    = "MSPK"
	version  = 1
	hdrSize  = 64                 // block A
	guidSize = 64                 // block B
	metaSize = hdrSize + guidSize // 128: block C starts here
	align    = 16                 // index entries are padded to this boundary
)

// flagNoCase is the block-A flags bit (byte 5) marking a blob whose URL/Key
// lookups fold case — important on case-sensitive filesystems (Linux), where
// "Logo.JPG" and "logo.jpg" are distinct files.
const flagNoCase byte = 0x01

// RestType is a bitmask of resource-type flags (mirroring miniskin item type=
// flags), JSON-encoded as a fixed-width hex string ("0x00000005" = Static|Parse,
// "" when none).
//
// The low byte (0x00FF) belongs to miniskin: those flags mirror its item types.
// The second byte (0xFF00) is for flags of mskblob's own, so the two sets can grow
// without ever colliding.
type RestType int32

const (
	Static       RestType = 0x0001
	HTMLTemplate RestType = 0x0002
	Parse        RestType = 0x0004
	Response     RestType = 0x0008
	Nomux        RestType = 0x0010
	// Mskblob is mskblob's own flag, not one of miniskin's — hence the second byte,
	// which is reserved for them. The entry's bytes are themselves a blob, mounted
	// with [Blob.OpenBlob] instead of served. Such an
	// entry is key-only (see [Write]), so it never reaches the routing index — an
	// older reader that knows nothing of nesting simply cannot hand it out.
	Mskblob RestType = 0x0100
	// MskBlobAuto is mskblob's own flag too: the entry is the blob's self-contained
	// server configuration, the one a blob carries to be served on its own.
	MskBlobAuto RestType = 0x0200
)

// ownFlags is the second byte of a [RestType]: the flags that are mskblob's own.
// Whatever carries one is never served over HTTP, so it must carry [Nomux] too —
// [Write] refuses to pack it otherwise, and a blob holding such an entry does not
// open.
const ownFlags RestType = 0xFF00

// String formats the flags as a fixed-width hex string, e.g. "0x00000005", or
// "" when no flags are set.
func (r RestType) String() string {
	if r == 0 {
		return ""
	}
	return fmt.Sprintf("0x%08X", uint32(r))
}

// restypeNames lists the canonical short flag names, in bit order.
var restypeNames = []struct {
	bit  RestType
	name string
}{
	{Static, "static"},
	{HTMLTemplate, "tpl"},
	{Parse, "parse"},
	{Response, "rsp"},
	{Nomux, "nomux"},
	{Mskblob, "mskblob"},
	{MskBlobAuto, "auto"},
}

// Names returns the comma-separated human flag names, e.g. "static,parse", or ""
// when no flags are set. This is the JSON representation of restype.
func (r RestType) Names() string {
	var names []string
	for _, f := range restypeNames {
		if r&f.bit != 0 {
			names = append(names, f.name)
		}
	}
	return strings.Join(names, ",")
}

// hx formats a 32-bit value as a fixed-width hex string.
func hx(v uint32) string { return fmt.Sprintf("0x%08X", v) }

// parseRestType reads a restype given as comma-separated flag names
// ("static,parse"), or — for convenience — as a hex string ("0x05"), a decimal
// string ("5"), or a bare JSON number. Missing/empty yields 0.
func parseRestType(raw []byte) (RestType, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" {
		return 0, nil
	}
	if v, err := strconv.ParseInt(s, 0, 32); err == nil {
		return RestType(v), nil
	}
	var r RestType
	for _, name := range strings.Split(s, ",") {
		switch strings.TrimSpace(name) {
		case "":
			// ignore
		case "static":
			r |= Static
		case "tpl", "html-template":
			r |= HTMLTemplate
		case "parse":
			r |= Parse
		case "rsp", "response":
			r |= Response
		case "nomux":
			r |= Nomux
		case "mskblob":
			r |= Mskblob
		case "auto":
			r |= MskBlobAuto
		default:
			return 0, fmt.Errorf("mskblob: unknown restype flag %q", name)
		}
	}
	return r, nil
}

// Item is one resource: the unit of both a manifest and a blob's index. The
// declarative fields describe what to pack; Src is the build-time source path;
// Size/CRC32/Offset are computed by the writer — informational in a manifest and
// ignored when building.
//
// In JSON, restype is a human flag mask (comma-separated names, "static,parse");
// the computed numbers are fixed-width 32-bit hex strings (0x%08X) — readable and
// free of float64 precision loss. The 64-bit Size and Offset are split into
// low/high halves (sizeLow/sizeHigh, offsetLow/offsetHigh), like the binary
// header. On decode the computed numbers are ignored (recomputed on build), so
// only the declarative fields are read; restype also accepts a hex/decimal value.
type Item struct {
	URL      string
	Key      string
	Filename string
	RestType RestType
	Src      string
	Size     uint64
	CRC32    uint32
	Offset   uint64
}

// MarshalJSON renders the item with hex-string numbers; 64-bit Size/Offset split
// into low/high halves.
func (it Item) MarshalJSON() ([]byte, error) {
	o := struct {
		URL        string `json:"url"`
		Key        string `json:"key,omitempty"`
		Filename   string `json:"filename,omitempty"`
		RestType   string `json:"restype"`
		Src        string `json:"src,omitempty"`
		CRC32      string `json:"crc32,omitempty"`
		SizeLow    string `json:"sizeLow,omitempty"`
		SizeHigh   string `json:"sizeHigh,omitempty"`
		OffsetLow  string `json:"offsetLow,omitempty"`
		OffsetHigh string `json:"offsetHigh,omitempty"`
	}{URL: it.URL, Key: it.Key, Filename: it.Filename, Src: it.Src, RestType: it.RestType.Names()}
	if it.CRC32 != 0 {
		o.CRC32 = hx(it.CRC32)
	}
	if it.Size != 0 {
		o.SizeLow, o.SizeHigh = hx(uint32(it.Size)), hx(uint32(it.Size>>32))
	}
	if it.Offset != 0 {
		o.OffsetLow, o.OffsetHigh = hx(uint32(it.Offset)), hx(uint32(it.Offset>>32))
	}
	return json.Marshal(o)
}

// UnmarshalJSON reads only the declarative fields (url, key, filename, restype,
// src). The computed numbers (crc32, size*, offset*) are ignored — they are
// recomputed when a blob is built, so there is nothing to parse.
func (it *Item) UnmarshalJSON(b []byte) error {
	var in struct {
		URL      string          `json:"url"`
		Key      string          `json:"key"`
		Filename string          `json:"filename"`
		RestType json.RawMessage `json:"restype"`
		Src      string          `json:"src"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	rt, err := parseRestType(in.RestType)
	if err != nil {
		return err
	}
	it.URL, it.Key, it.Filename, it.Src, it.RestType = in.URL, in.Key, in.Filename, in.Src, rt
	return nil
}

// Manifest is the interchange format: an id plus the items. Version, Count and
// DataCRC32 are informational (filled from a blob, emitted for inspection, and
// ignored on decode).
type Manifest struct {
	ID        string
	Version   uint8
	Count     uint32
	DataCRC32 uint32
	NoCase    bool
	Items     []Item
}

// MarshalJSON renders the manifest header (DataCRC32 as a hex string) plus items.
// The header is always present (id, count and nocase are emitted even when empty,
// so the shape is consistent); version/dataCRC32 appear only once a blob is built.
func (m Manifest) MarshalJSON() ([]byte, error) {
	o := struct {
		ID        string `json:"id"`
		Version   uint8  `json:"version,omitempty"`
		Count     uint32 `json:"count"`
		DataCRC32 string `json:"dataCRC32,omitempty"`
		NoCase    bool   `json:"nocase"`
		Items     []Item `json:"items"`
	}{ID: m.ID, Version: m.Version, Count: m.Count, NoCase: m.NoCase, Items: m.Items}
	if m.DataCRC32 != 0 {
		o.DataCRC32 = hx(m.DataCRC32)
	}
	return json.Marshal(o)
}

// UnmarshalJSON reads the declarative header (id, nocase) and items, plus
// DataCRC32 (kept as an expected value a caller can check a rebuild against);
// version and count are ignored.
func (m *Manifest) UnmarshalJSON(b []byte) error {
	var in struct {
		ID        string `json:"id"`
		DataCRC32 string `json:"dataCRC32"`
		NoCase    bool   `json:"nocase"`
		Items     []Item `json:"items"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	m.ID, m.NoCase, m.Items = in.ID, in.NoCase, in.Items
	if s := strings.TrimSpace(in.DataCRC32); s != "" {
		if v, err := strconv.ParseUint(s, 0, 32); err == nil {
			m.DataCRC32 = uint32(v)
		}
	}
	return nil
}

// NewManifest starts an empty manifest. Pass an id to pin the blob's guid; leave
// it empty to get a fresh one when written.
func NewManifest(id string) *Manifest { return &Manifest{ID: id} }

// AddItem appends one declarative item (size/crc/offset are computed at Write
// time, so you only fill the descriptive fields). Returns the manifest for
// chaining.
func (m *Manifest) AddItem(it Item) *Manifest {
	m.Items = append(m.Items, it)
	return m
}

// AddFile appends one static resource: bytes are read from src at Write time and
// served under url. The common case — no knowledge of the index internals needed.
func (m *Manifest) AddFile(url, src string) *Manifest {
	return m.AddItem(Item{URL: url, Filename: url, RestType: Static, Src: src})
}

// FolderOptions filters and shapes an [Manifest.AddFolder] scan.
type FolderOptions struct {
	Recurse bool   // descend into subdirectories (default: top level only)
	Include string // comma-separated globs matched on the file name (default: all)
	Exclude string // comma-separated globs matched on the file name
	Base    string // write Src relative to this dir (default: the scanned dir)
	Prefix  string // optional URL/key prefix prepended to every added entry
	NoCase  bool   // case-insensitive Include/Exclude matching (e.g. *.jpg matches .JPG)
}

// AddFolder scans dir and appends a static item per matching file: URL is the
// path relative to dir (Prefix prepended), Src is relative to opts.Base (default
// dir). It returns how many entries were added. As everywhere, size/crc/offset
// are left for Write to compute. With NoCase, the file name and the globs are
// folded to lower case before matching (the stored URL/Src keep their real case).
func (m *Manifest) AddFolder(dir string, opts FolderOptions) (int, error) {
	base := opts.Base
	if base == "" {
		base = dir
	}
	include := opts.Include
	if include == "" {
		include = "*"
	}
	exclude := opts.Exclude
	if opts.NoCase {
		include = strings.ToLower(include)
		exclude = strings.ToLower(exclude)
	}
	added := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && !opts.Recurse {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if opts.NoCase {
			name = strings.ToLower(name)
		}
		if !matchAny(name, include) || (exclude != "" && matchAny(name, exclude)) {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		url := opts.Prefix + filepath.ToSlash(rel)
		src := filepath.ToSlash(p)
		if r, e := filepath.Rel(base, p); e == nil {
			src = filepath.ToSlash(r)
		}
		m.AddItem(Item{URL: url, Key: "/" + url, Filename: url, RestType: Static, Src: src})
		added++
		return nil
	})
	return added, err
}

// matchAny reports whether name matches any of the comma-separated globs.
func matchAny(name, patterns string) bool {
	for _, p := range strings.Split(patterns, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ok, _ := filepath.Match(p, name); ok {
			return true
		}
	}
	return false
}

// Write packs the manifest into a blob at path in one shot and returns its id
// (see the package-level [Write] for the mechanics). When the manifest had no id,
// the freshly generated one is stored back on m.
func (m *Manifest) Write(path string) (string, error) {
	id, err := Write(path, m.Items, Options{ID: m.ID, NoCase: m.NoCase})
	if err == nil {
		m.ID = id
	}
	return id, err
}

// Options tunes how a blob is written.
type Options struct {
	ID     string // guid; a fresh v4 GUID is generated when empty
	NoCase bool   // record the blob as case-insensitive (URL/Key lookups fold case)
	// SkipUnchanged short-circuits the write when a blob already exists at path
	// with the same pinned ID. A pinned id is the content's identity, so a target
	// already carrying it holds the same bytes by contract — Write returns that id
	// without reading any Src or rewriting the file. Needs a pinned ID: with an
	// empty ID every write mints a fresh guid, so there is nothing to compare and
	// the build proceeds normally.
	SkipUnchanged bool
}

// Header is a blob's metadata (blocks A + B).
type Header struct {
	Version   uint8
	ID        string
	Count     uint32
	DataCRC32 uint32
	DataSize  int64
	NoCase    bool // URL/Key lookups fold case
}

// NewID returns a random RFC-4122 v4 GUID, the default blob id.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mskblob: generating id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func align16(n int) int { return (n + align - 1) &^ (align - 1) }

func entryBytes(key, url, filename string) int {
	n := 4 + (len(key) + 1) + 8 + 4 + 4 + (len(url) + 1) + (len(filename) + 1)
	return align16(n)
}

// Write packs items into a self-describing blob at path and returns its id.
// Items are written in lexical URL order so the data (and dataCRC) is
// reproducible; only the guid varies unless Options.ID is set. Each item's
// Size/CRC32/Offset are recomputed from its Src — any values already set are
// ignored. With Options.SkipUnchanged and a pinned Options.ID, Write is a no-op
// (returning that id, no Src read) when path already holds a blob with that id.
func Write(path string, items []Item, opts Options) (string, error) {
	// SkipUnchanged with a pinned id: if the target already carries that id the
	// bytes are the same by contract, so skip the whole pack — no Src is read. Only
	// a cheap 128-byte header read. An empty id has no stable identity to compare.
	if opts.SkipUnchanged && opts.ID != "" {
		if h, err := ReadHeader(path); err == nil && h.ID == opts.ID {
			return opts.ID, nil
		}
	}
	id := opts.ID
	if id == "" {
		var err error
		if id, err = NewID(); err != nil {
			return "", err
		}
	}
	if len(id) > guidSize {
		return "", fmt.Errorf("mskblob: id too long (%d bytes, max %d): %q", len(id), guidSize, id)
	}

	sorted := append([]Item(nil), items...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].URL != sorted[j].URL {
			return sorted[i].URL < sorted[j].URL
		}
		return sorted[i].Key < sorted[j].Key
	})

	// An entry is identified by its URL and/or Key (static→url, template→key); it
	// needs at least one, and each non-empty url/key must be unique. When NoCase,
	// uniqueness folds case (so "A.JPG" and "a.jpg" — distinct files on Linux —
	// would collide and are rejected up front).
	fold := func(s string) string {
		if opts.NoCase {
			return strings.ToLower(s)
		}
		return s
	}
	caseNote := ""
	if opts.NoCase {
		caseNote = " (case-insensitive: this blob folds case)"
	}
	seenURL := make(map[string]bool, len(sorted))
	seenKey := make(map[string]bool, len(sorted))
	for i, in := range sorted {
		if in.URL == "" && in.Key == "" {
			return "", fmt.Errorf("mskblob: item %d has neither url nor key", i)
		}
		// An entry carrying one of mskblob's own flags is never served: a nested blob
		// is mounted, the self-contained configuration is read. It is reached by key,
		// so it needs one; it has no URL, which keeps it out of the routing index
		// altogether; and it says so itself by carrying nomux — required, not implied,
		// so the entry reads the same in a manifest as it behaves.
		if own := in.RestType & ownFlags; own != 0 {
			if in.Key == "" {
				return "", fmt.Errorf("mskblob: item %d is flagged %q and needs a key", i, own.Names())
			}
			if in.URL != "" {
				return "", fmt.Errorf("mskblob: %q is flagged %q and must have no url (it is reached by key, never served)", in.Key, own.Names())
			}
			if in.RestType&Nomux == 0 {
				return "", fmt.Errorf("mskblob: %q is flagged %q and must also be flagged \"nomux\"", in.Key, own.Names())
			}
		}
		if in.URL != "" {
			if seenURL[fold(in.URL)] {
				return "", fmt.Errorf("mskblob: duplicate url %q%s", in.URL, caseNote)
			}
			seenURL[fold(in.URL)] = true
		}
		if in.Key != "" {
			if seenKey[fold(in.Key)] {
				return "", fmt.Errorf("mskblob: duplicate key %q%s", in.Key, caseNote)
			}
			seenKey[fold(in.Key)] = true
		}
	}

	// A file cannot include itself. Creating path truncates it, and the copy would
	// then read back the very bytes it appends — a loop that only ends when the disk
	// is full. Compared as files, not as path strings, so a relative path, a link or
	// a different spelling of the same file is caught too. Checked before anything
	// is created: the existing file is left untouched.
	if out, err := os.Stat(path); err == nil {
		for _, in := range sorted {
			if si, err := os.Stat(in.Src); err == nil && os.SameFile(out, si) {
				return "", fmt.Errorf("mskblob: source %s is the output file itself: a blob cannot include itself", in.Src)
			}
		}
	}

	indexLen := 0
	for _, in := range sorted {
		indexLen += entryBytes(in.Key, in.URL, in.Filename)
	}
	dataOffset := metaSize + indexLen

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("mskblob: creating %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(make([]byte, dataOffset)); err != nil {
		return "", fmt.Errorf("mskblob: reserving header: %w", err)
	}

	packed, dataCRC, err := pack(f, sorted, uint64(dataOffset))
	if err != nil {
		return "", err
	}

	var a [hdrSize]byte
	copy(a[0:4], magic)
	a[4] = version
	if opts.NoCase {
		a[5] = flagNoCase
	}
	binary.LittleEndian.PutUint32(a[8:12], uint32(len(packed)))
	binary.LittleEndian.PutUint32(a[12:16], dataCRC)
	binary.LittleEndian.PutUint32(a[16:20], uint32(dataOffset))
	if _, err := f.WriteAt(a[:], 0); err != nil {
		return "", fmt.Errorf("mskblob: writing header: %w", err)
	}
	var b [guidSize]byte
	copy(b[:], id)
	if _, err := f.WriteAt(b[:], hdrSize); err != nil {
		return "", fmt.Errorf("mskblob: writing id: %w", err)
	}

	idxBuf := make([]byte, indexLen)
	p := 0
	for _, e := range packed {
		es := entryBytes(e.Key, e.URL, e.Filename)
		binary.LittleEndian.PutUint32(idxBuf[p:], uint32(es))
		q := p + 4
		q += copy(idxBuf[q:], e.Key)
		q++ // NUL
		binary.LittleEndian.PutUint64(idxBuf[q:], e.Size)
		q += 8
		binary.LittleEndian.PutUint32(idxBuf[q:], e.CRC32)
		q += 4
		binary.LittleEndian.PutUint32(idxBuf[q:], uint32(e.RestType))
		q += 4
		q += copy(idxBuf[q:], e.URL)
		q++ // NUL
		copy(idxBuf[q:], e.Filename)
		p += es
	}
	if _, err := f.WriteAt(idxBuf, metaSize); err != nil {
		return "", fmt.Errorf("mskblob: writing index: %w", err)
	}
	return id, nil
}

// pack copies each item's bytes (from Src) to w in order, returning the items
// with Offset/Size/CRC32 computed, plus the crc32 of the whole data region.
func pack(w io.Writer, sorted []Item, base uint64) (out []Item, dataCRC uint32, err error) {
	dataHash := crc32.NewIEEE()
	out = make([]Item, 0, len(sorted))
	offset := base
	for _, in := range sorted {
		src, err := os.Open(in.Src)
		if err != nil {
			return nil, 0, fmt.Errorf("mskblob: opening source %s: %w", in.Src, err)
		}
		perFile := crc32.NewIEEE()
		n, err := io.Copy(io.MultiWriter(w, dataHash), io.TeeReader(src, perFile))
		src.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("mskblob: packing %s: %w", in.Src, err)
		}
		out = append(out, Item{
			URL: in.URL, Key: in.Key, Filename: in.Filename, RestType: in.RestType,
			Offset: offset, Size: uint64(n), CRC32: perFile.Sum32(),
		})
		offset += uint64(n)
	}
	return out, dataHash.Sum32(), nil
}

func readMeta(r io.ReaderAt) (id string, count, dataCRC, dataOffset uint32, nocase bool, err error) {
	var m [metaSize]byte
	if _, err := r.ReadAt(m[:], 0); err != nil {
		return "", 0, 0, 0, false, fmt.Errorf("mskblob: reading header: %w", err)
	}
	if string(m[0:4]) != magic {
		return "", 0, 0, 0, false, fmt.Errorf("mskblob: bad magic %q", m[0:4])
	}
	if m[4] != version {
		return "", 0, 0, 0, false, fmt.Errorf("mskblob: unsupported version %d", m[4])
	}
	nocase = m[5]&flagNoCase != 0
	count = binary.LittleEndian.Uint32(m[8:12])
	dataCRC = binary.LittleEndian.Uint32(m[12:16])
	dataOffset = binary.LittleEndian.Uint32(m[16:20])
	id = strings.TrimRight(string(m[hdrSize:metaSize]), "\x00")
	return id, count, dataCRC, dataOffset, nocase, nil
}

func readIndex(r io.ReaderAt, count, dataOffset uint32) ([]Item, error) {
	idxLen := int(dataOffset) - metaSize
	if idxLen < 0 {
		return nil, fmt.Errorf("mskblob: bad data offset %d", dataOffset)
	}
	buf := make([]byte, idxLen)
	if idxLen > 0 {
		if _, err := r.ReadAt(buf, metaSize); err != nil {
			return nil, fmt.Errorf("mskblob: reading index: %w", err)
		}
	}
	items := make([]Item, 0, count)
	off := uint64(dataOffset)
	p := 0
	for i := uint32(0); i < count; i++ {
		if p+4 > len(buf) {
			return nil, fmt.Errorf("mskblob: truncated entry %d", i)
		}
		es := int(binary.LittleEndian.Uint32(buf[p:]))
		if es < align || p+es > len(buf) {
			return nil, fmt.Errorf("mskblob: bad entry size %d at %d", es, i)
		}
		rec := buf[p : p+es]
		q := 4
		key, n := cstr(rec[q:])
		q += n
		size := binary.LittleEndian.Uint64(rec[q : q+8])
		q += 8
		crc := binary.LittleEndian.Uint32(rec[q : q+4])
		q += 4
		restype := RestType(binary.LittleEndian.Uint32(rec[q : q+4]))
		q += 4
		url, n := cstr(rec[q:])
		q += n
		filename, _ := cstr(rec[q:])
		items = append(items, Item{
			URL: url, Key: key, Filename: filename, RestType: restype,
			Size: size, CRC32: crc, Offset: off,
		})
		off += size
		p += es
	}
	return items, nil
}

func cstr(b []byte) (string, int) {
	if z := bytes.IndexByte(b, 0); z >= 0 {
		return string(b[:z]), z + 1
	}
	return string(b), len(b)
}

// ReadHeader reads only a blob's header (cheap: 128 bytes). Used for cache
// checks (matching the guid) and inspection.
func ReadHeader(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	id, count, dataCRC, dataOffset, nocase, err := readMeta(f)
	if err != nil {
		return Header{}, err
	}
	fi, err := f.Stat()
	if err != nil {
		return Header{}, err
	}
	return Header{Version: version, ID: id, Count: count, DataCRC32: dataCRC, DataSize: fi.Size() - int64(dataOffset), NoCase: nocase}, nil
}

// ErrClosed is returned by a read on a blob whose descriptor is already closed —
// including a nested one, which reads through the descriptor its root owns.
var ErrClosed = errors.New("mskblob: blob is closed")

// source is the byte origin shared by a whole blob tree: the descriptor the root
// opened, plus the flag every read consults. Nested blobs read through it over
// their own section, so closing the root makes the entire subtree fail with
// [ErrClosed] instead of reading from a freed — and possibly reused — descriptor.
type source struct {
	f      *os.File
	closed atomic.Bool
}

func (s *source) ReadAt(p []byte, off int64) (int, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	return s.f.ReadAt(p, off)
}

// Close closes the descriptor once; further calls are no-ops.
func (s *source) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.f.Close()
}

// Blob is an opened, indexed blob ready to serve. It is either a root — opened
// from a file, which it owns — or one nested inside another, read in place over
// its own section of the parent.
type Blob struct {
	src    *source     // the descriptor, shared by the whole tree
	r      io.ReaderAt // this blob's own zero: the source, or a section of the parent
	owns   bool        // true for the root: its Close releases src
	hdr    Header
	byURL  map[string]Item // entries with a non-empty URL (served over HTTP)
	byKey  map[string]Item // entries with a non-empty Key (logical lookup, e.g. templates)
	items  []Item
	nocase bool // fold case on URL/Key lookups
}

// fold lower-cases s when the blob is case-insensitive; otherwise returns it as is.
func (b *Blob) fold(s string) string {
	if b.nocase {
		return strings.ToLower(s)
	}
	return s
}

// Open opens a blob file and reads its index into memory.
func Open(path string) (*Blob, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	src := &source{f: f}
	b, err := newBlob(src, src, fi.Size(), true)
	if err != nil {
		f.Close()
		return nil, err
	}
	return b, nil
}

// newBlob reads a blob's header and index from r — the whole file for a root
// blob, a section of the parent for a nested one — and builds its lookups. Every
// offset read is relative to r's own zero, which is what lets a nested blob be
// parsed in place: it never learns where it sits.
func newBlob(src *source, r io.ReaderAt, size int64, owns bool) (*Blob, error) {
	id, count, dataCRC, dataOffset, nocase, err := readMeta(r)
	if err != nil {
		return nil, err
	}
	items, err := readIndex(r, count, dataOffset)
	if err != nil {
		return nil, err
	}
	fold := func(s string) string {
		if nocase {
			return strings.ToLower(s)
		}
		return s
	}
	byURL := make(map[string]Item, len(items))
	byKey := make(map[string]Item, len(items))
	for _, it := range items {
		// Refused rather than ignored: taking the entry for a plain one would let a
		// handler hand out what was packed never to be served.
		if own := it.RestType & ownFlags; own != 0 && it.RestType&Nomux == 0 {
			return nil, fmt.Errorf("mskblob: entry %q is flagged %q without \"nomux\"", entryID(it), own.Names())
		}
		if it.URL != "" {
			byURL[fold(it.URL)] = it
		}
		if it.Key != "" {
			byKey[fold(it.Key)] = it
		}
	}
	return &Blob{
		src:    src,
		r:      r,
		owns:   owns,
		hdr:    Header{Version: version, ID: id, Count: count, DataCRC32: dataCRC, DataSize: size - int64(dataOffset), NoCase: nocase},
		byURL:  byURL,
		byKey:  byKey,
		items:  items,
		nocase: nocase,
	}, nil
}

// entryID names an entry in a message: its key, or its url when it has no key.
func entryID(it Item) string {
	if it.Key != "" {
		return it.Key
	}
	return it.URL
}

// OpenBlob mounts the blob nested under key: an entry marked [Mskblob], whose
// bytes are themselves a blob. It is read in place, over its own section of this
// one — nothing is extracted, and the child's offsets stay relative to itself —
// so what comes back is an ordinary *Blob on which OpenBlob works again: nesting
// costs one addition per level, resolved when mounting, not when serving.
//
// The child reads through the descriptor the root owns: its own [Blob.Close] is a
// no-op, and closing the root closes the whole subtree with it.
func (b *Blob) OpenBlob(key string) (*Blob, error) {
	it := b.GetByKey(key)
	if it == nil {
		return nil, fmt.Errorf("mskblob: no entry with key %q", key)
	}
	if it.RestType&Mskblob == 0 {
		return nil, fmt.Errorf("mskblob: entry %q is not a nested blob (restype %q)", key, it.RestType.Names())
	}
	child, err := newBlob(b.src, io.NewSectionReader(b.r, int64(it.Offset), int64(it.Size)), int64(it.Size), false)
	if err != nil {
		return nil, fmt.Errorf("mskblob: mounting %q: %w", key, err)
	}
	return child, nil
}

// LoadBlob mounts the blob nested under key and, when expectID is non-empty,
// verifies its guid — the nested counterpart of [Load]. Same purpose one level in:
// a child rebuilt or swapped inside the container is rejected rather than read.
func (b *Blob) LoadBlob(key, expectID string) (*Blob, error) {
	child, err := b.OpenBlob(key)
	if err != nil {
		return nil, err
	}
	if expectID != "" && child.hdr.ID != expectID {
		return nil, fmt.Errorf("mskblob: nested blob %q: id mismatch (found %s, expected %s)", key, child.hdr.ID, expectID)
	}
	return child, nil
}

// Load opens a blob and, when expectID is non-empty, verifies its guid matches
// (so a stale/mismatched data file is rejected).
func Load(path, expectID string) (*Blob, error) {
	b, err := Open(path)
	if err != nil {
		return nil, err
	}
	if expectID != "" && b.hdr.ID != expectID {
		b.Close()
		return nil, fmt.Errorf("mskblob %s: id mismatch (file %s, expected %s)", path, b.hdr.ID, expectID)
	}
	return b, nil
}

// Header returns the blob's metadata.
func (b *Blob) Header() Header { return b.hdr }

// Items returns all index items (URL order, with Size/CRC32/Offset filled).
func (b *Blob) Items() []Item { return b.items }

// Manifest returns the blob as a manifest: its header plus items with the
// computed Size/CRC32/Offset. (Item.Src is empty — the bytes live in the blob.)
func (b *Blob) Manifest() Manifest {
	return Manifest{
		ID:        b.hdr.ID,
		Version:   b.hdr.Version,
		Count:     b.hdr.Count,
		DataCRC32: b.hdr.DataCRC32,
		NoCase:    b.hdr.NoCase,
		Items:     b.items,
	}
}

// GetByURL returns the entry served under a relative URL, or nil if absent. The
// lookup folds case when the blob is case-insensitive. (A pure in-memory lookup —
// it cannot fail.)
func (b *Blob) GetByURL(url string) *Item {
	if it, ok := b.byURL[b.fold(url)]; ok {
		return &it
	}
	return nil
}

// GetByKey returns the entry with a logical key, or nil if absent (folding case
// when the blob is case-insensitive). This is how non-served resources
// (templates, parse fragments) are reached.
func (b *Blob) GetByKey(key string) *Item {
	if it, ok := b.byKey[b.fold(key)]; ok {
		return &it
	}
	return nil
}

// Bytes reads an entry's whole content into memory. For large resources prefer
// [Blob.Reader], which streams without allocating the whole thing.
func (b *Blob) Bytes(it *Item) ([]byte, error) {
	buf := make([]byte, it.Size)
	if _, err := b.r.ReadAt(buf, int64(it.Offset)); err != nil {
		return nil, err
	}
	return buf, nil
}

// Reader returns a reader over an entry's bytes, straight from the blob file
// (nothing resident in RAM). Ideal for streaming big assets with io.Copy.
func (b *Blob) Reader(it *Item) *io.SectionReader {
	return io.NewSectionReader(b.r, int64(it.Offset), int64(it.Size))
}

// Close releases the descriptor the root blob opened, and with it the whole tree:
// a later read on this blob or on any blob nested inside it returns [ErrClosed]
// rather than hitting a freed descriptor. On a nested blob it is a no-op — it
// owns nothing, the lifetime belongs to the root.
func (b *Blob) Close() error {
	if !b.owns {
		return nil
	}
	return b.src.Close()
}

// Dispatch codes a [Middleware] returns to drive [Blob.Handler].
const (
	DispatchAuto = 0 // let the blob serve it (a static entry; anything else → 404)
	DispatchDone = 1 // the middleware already wrote the whole response
	// any other value is taken as an HTTP status code and returned via http.Error.
)

// Middleware is the single hook [Blob.Handler] calls for every routed request,
// with the matched item — which is nil when the URL is absent, so it can also
// answer unknown routes. Its return value decides what happens next:
// [DispatchAuto], [DispatchDone], or any other int as an HTTP status code.
type Middleware func(w http.ResponseWriter, r *http.Request, it *Item) int

// Handler is the package's default sub-mux for a blob mounted at base: register
// only base on your own mux (yourMux.Handle(base, b.Handler(base, mw))) and it
// routes everything below by relative URL. It is a building block, not a web
// server — no listening, TLS, or config lives here.
//
// For each request it looks the base-stripped path up and calls mw (when non-nil)
// with the matched item (nil if the URL is absent). An entry flagged [Nomux] is
// not routed at all: it counts as absent, for mw as much as for the default
// serving — reach it by key instead. mw returns:
//   - [DispatchAuto] (0): the blob serves it — but the default handler serves
//     ONLY static entries (streamed lazily, ETag = crc32, If-None-Match → 304).
//     Anything else (template, response, or an absent URL) is treated as if it
//     weren't there → 404. Serve those from the middleware. A nil mw is always auto.
//   - [DispatchDone] (1): mw already wrote the response; nothing more is done.
//   - any other int: returned as that HTTP status via http.Error.
func (b *Blob) Handler(base string, mw Middleware) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		it := b.GetByURL(strings.TrimPrefix(r.URL.Path, base))
		if it != nil && it.RestType&Nomux != 0 {
			it = nil // nomux: not routed, as if the URL were absent
		}
		code := DispatchAuto
		if mw != nil {
			code = mw(w, r, it)
		}
		switch code {
		case DispatchDone:
			return
		case DispatchAuto:
			if it == nil || it.RestType&Static == 0 {
				http.NotFound(w, r) // not static → as if it weren't there
				return
			}
			etag := `"` + strconv.FormatUint(uint64(it.CRC32), 16) + `"`
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if ct := MimeByExt(it.URL); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			io.Copy(w, b.Reader(it))
		default:
			http.Error(w, http.StatusText(code), code)
		}
	})
}

// MimeByExt maps a path's extension to a Content-Type, matching miniskin's
// static guessMime criterion.
func MimeByExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript"
	case ".json":
		return "application/json"
	case ".ttf":
		return "application/octet-stream"
	case ".ico":
		return "image/x-icon"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".svg":
		return "image/svg+xml"
	default:
		return "application/octet-stream"
	}
}
