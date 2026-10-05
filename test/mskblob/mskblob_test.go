package mskblob_test

import (
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pablo-botella/mskblob"
)

func writeSources(t *testing.T, dir string, files map[string][]byte) []mskblob.Item {
	t.Helper()
	var items []mskblob.Item
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		items = append(items, mskblob.Item{Key: "/k/" + name, URL: name, Filename: name, RestType: mskblob.Static, Src: p})
	}
	return items
}

func TestWriteOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"a.png":     []byte("first bytes"),
		"sub/b.css": []byte("body{color:red}"),
		"c.js":      []byte("console.log(1) \x00\x01\x02"),
	}
	inputs := writeSources(t, dir, files)

	id, err := mskblob.Write(filepath.Join(dir, "t.blob"), inputs, mskblob.Options{ID: "test-id"})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if id != "test-id" {
		t.Fatalf("id = %q", id)
	}

	b, err := mskblob.Open(filepath.Join(dir, "t.blob"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()

	if b.Header().ID != "test-id" {
		t.Errorf("header id = %q", b.Header().ID)
	}
	if int(b.Header().Count) != len(files) {
		t.Errorf("count = %d, want %d", b.Header().Count, len(files))
	}

	for url, want := range files {
		e := b.GetByURL(url)
		if e == nil {
			t.Errorf("missing entry %q", url)
			continue
		}
		if e.Size != uint64(len(want)) {
			t.Errorf("%s: size=%d want=%d", url, e.Size, len(want))
		}
		if e.RestType != mskblob.Static {
			t.Errorf("%s: restype=%#x", url, e.RestType)
		}
		if e.Key != "/k/"+url {
			t.Errorf("%s: key=%q", url, e.Key)
		}
		got, err := b.Bytes(e)
		if err != nil {
			t.Errorf("%s: Bytes: %v", url, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("%s: bytes mismatch", url)
		}
		if crc32.ChecksumIEEE(got) != e.CRC32 {
			t.Errorf("%s: crc mismatch", url)
		}
	}
}

func TestHandlerServes(t *testing.T) {
	dir := t.TempDir()
	inputs := writeSources(t, dir, map[string][]byte{"img/logo.png": []byte("PNGDATA")})
	if _, err := mskblob.Write(filepath.Join(dir, "h.blob"), inputs, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(filepath.Join(dir, "h.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	srv := httptest.NewServer(b.Handler("/assets/", nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/assets/img/logo.png")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, resp.ContentLength)
	resp.Body.Read(body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if string(body) != "PNGDATA" {
		t.Errorf("body = %q", body)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Error("missing ETag")
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q", ct)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/assets/img/logo.png", nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("conditional status = %d, want 304", resp2.StatusCode)
	}

	resp3, err := http.Get(srv.URL + "/assets/img/nope.png")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("miss status = %d, want 404", resp3.StatusCode)
	}
}

func TestHandlerMiddleware(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("RAW"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []mskblob.Item{
		{URL: "logo.png", Filename: "logo.png", RestType: mskblob.Static, Src: src},    // static
		{URL: "page", Filename: "page.html", RestType: mskblob.HTMLTemplate, Src: src}, // non-static
	}
	if _, err := mskblob.Write(filepath.Join(dir, "m.blob"), items, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(filepath.Join(dir, "m.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	get := func(h http.Handler, path string) (*http.Response, string) {
		srv := httptest.NewServer(h)
		defer srv.Close()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(body)
	}

	// nil middleware: static auto-served; everything else (template, absent) 404.
	if resp, body := get(b.Handler("/", nil), "/logo.png"); resp.StatusCode != 200 || body != "RAW" {
		t.Errorf("nil mw static: status=%d body=%q", resp.StatusCode, body)
	}
	if resp, _ := get(b.Handler("/", nil), "/page"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("nil mw template should 404, got %d", resp.StatusCode)
	}
	if resp, _ := get(b.Handler("/", nil), "/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("nil mw absent should 404, got %d", resp.StatusCode)
	}

	// A middleware that: renders the template (DispatchDone), blocks /logo.png
	// with 403, and lets absent URLs through to auto (→404).
	mw := func(w http.ResponseWriter, r *http.Request, it *mskblob.Item) int {
		if it != nil && it.RestType&(mskblob.HTMLTemplate|mskblob.Parse) != 0 {
			w.Write([]byte("RENDERED:" + it.URL))
			return mskblob.DispatchDone
		}
		if it != nil && it.RestType&mskblob.Static != 0 {
			return http.StatusForbidden // any other code → that status
		}
		return mskblob.DispatchAuto
	}
	h := b.Handler("/", mw)
	if resp, body := get(h, "/page"); resp.StatusCode != 200 || body != "RENDERED:page" {
		t.Errorf("mw template render: status=%d body=%q", resp.StatusCode, body)
	}
	if resp, _ := get(h, "/logo.png"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("mw block: status=%d, want 403", resp.StatusCode)
	}
	// Absent URL still reaches the middleware (it == nil) → auto → 404.
	if resp, _ := get(h, "/nope"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("mw absent: status=%d, want 404", resp.StatusCode)
	}
}

func TestLoadIDMismatch(t *testing.T) {
	dir := t.TempDir()
	inputs := writeSources(t, dir, map[string][]byte{"x.txt": []byte("hi")})
	if _, err := mskblob.Write(filepath.Join(dir, "x.blob"), inputs, mskblob.Options{ID: "real-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mskblob.Load(filepath.Join(dir, "x.blob"), "wrong-id"); err == nil {
		t.Error("expected id mismatch error")
	}
	b, err := mskblob.Load(filepath.Join(dir, "x.blob"), "real-id")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Close()
}

func TestNewIDFormat(t *testing.T) {
	id, err := mskblob.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 36 || strings.Count(id, "-") != 4 {
		t.Errorf("not a guid: %q", id)
	}
}

func TestWriteIdentityValidation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Neither url nor key → no identity → error.
	if _, err := mskblob.Write(filepath.Join(dir, "e.blob"), []mskblob.Item{{Src: src}}, mskblob.Options{}); err == nil {
		t.Error("expected error for item with neither url nor key")
	}
	// url-only and key-only are both valid.
	if _, err := mskblob.Write(filepath.Join(dir, "u.blob"), []mskblob.Item{{URL: "a", Src: src}}, mskblob.Options{}); err != nil {
		t.Errorf("url-only should be valid: %v", err)
	}
	if _, err := mskblob.Write(filepath.Join(dir, "k.blob"), []mskblob.Item{{Key: "/tpl", Src: src}}, mskblob.Options{}); err != nil {
		t.Errorf("key-only (template) should be valid: %v", err)
	}
	// Duplicate url and duplicate key are rejected.
	if _, err := mskblob.Write(filepath.Join(dir, "du.blob"), []mskblob.Item{{URL: "a", Src: src}, {URL: "a", Src: src}}, mskblob.Options{}); err == nil {
		t.Error("expected error for duplicate url")
	}
	if _, err := mskblob.Write(filepath.Join(dir, "dk.blob"), []mskblob.Item{{Key: "k", Src: src}, {Key: "k", Src: src}}, mskblob.Options{}); err == nil {
		t.Error("expected error for duplicate key")
	}
}

func TestWriteSkipUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "o.blob")
	items := []mskblob.Item{{URL: "a", RestType: mskblob.Static, Src: src}}

	// First write creates the blob.
	if _, err := mskblob.Write(path, items, mskblob.Options{ID: "pinned"}); err != nil {
		t.Fatal(err)
	}

	// Remove the source: a real pack would fail to read it. SkipUnchanged must
	// short-circuit on the matching pinned id without touching any Src.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	id, err := mskblob.Write(path, items, mskblob.Options{ID: "pinned", SkipUnchanged: true})
	if err != nil {
		t.Fatalf("matching id should skip without reading Src: %v", err)
	}
	if id != "pinned" {
		t.Errorf("skip should return the pinned id, got %q", id)
	}

	// A different pinned id does not match, so the pack runs — and fails because
	// the source is gone, proving the build was actually attempted.
	if _, err := mskblob.Write(path, items, mskblob.Options{ID: "other", SkipUnchanged: true}); err == nil {
		t.Error("non-matching id should attempt the pack (and fail on missing Src)")
	}

	// No pinned id: nothing stable to compare, so it always packs (and fails here).
	if _, err := mskblob.Write(path, items, mskblob.Options{SkipUnchanged: true}); err == nil {
		t.Error("empty id should fall through to a normal pack")
	}

	// Target absent: nothing to compare against, so it packs normally.
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mskblob.Write(filepath.Join(dir, "new.blob"), items, mskblob.Options{ID: "pinned", SkipUnchanged: true}); err != nil {
		t.Errorf("absent target should pack normally: %v", err)
	}
}

func TestGetByKeyAndReader(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "tpl.html")
	if err := os.WriteFile(src, []byte("<h1>{{.T}}</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []mskblob.Item{
		{URL: "logo.png", Filename: "logo.png", RestType: mskblob.Static, Src: src},     // url-served
		{Key: "/page", Filename: "page.html", RestType: mskblob.HTMLTemplate, Src: src}, // key-only template
	}
	if _, err := mskblob.Write(filepath.Join(dir, "g.blob"), items, mskblob.Options{ID: "g"}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(filepath.Join(dir, "g.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// The template is reachable by key but NOT by url.
	if it := b.GetByKey("/page"); it == nil || it.RestType != mskblob.HTMLTemplate {
		t.Errorf("GetByKey(/page) = %v", it)
	}
	if it := b.GetByURL("/page"); it != nil {
		t.Error("template should not be reachable by url")
	}
	// The static is reachable by url.
	it := b.GetByURL("logo.png")
	if it == nil {
		t.Fatal("GetByURL(logo.png) = nil")
	}
	// Reader streams the same bytes as Bytes.
	rdr := b.Reader(it)
	streamed := make([]byte, it.Size)
	if _, err := io.ReadFull(rdr, streamed); err != nil {
		t.Fatal(err)
	}
	want, _ := b.Bytes(it)
	if string(streamed) != string(want) {
		t.Errorf("Reader bytes != Bytes")
	}
	// Missing → nil.
	if b.GetByURL("nope") != nil || b.GetByKey("nope") != nil {
		t.Error("missing lookups should be nil")
	}
}

func TestReproducibleData(t *testing.T) {
	dir := t.TempDir()
	// Same inputs in different declaration order must yield the same data CRC,
	// because Write sorts by URL before packing.
	a := writeSources(t, dir, map[string][]byte{"a": []byte("1"), "b": []byte("22"), "c": []byte("333")})
	reversed := []mskblob.Item{a[2], a[0], a[1]}

	id1, err := mskblob.Write(filepath.Join(dir, "1.blob"), a, mskblob.Options{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := mskblob.Write(filepath.Join(dir, "2.blob"), reversed, mskblob.Options{ID: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %q %q", id1, id2)
	}
	h1, _ := mskblob.ReadHeader(filepath.Join(dir, "1.blob"))
	h2, _ := mskblob.ReadHeader(filepath.Join(dir, "2.blob"))
	if h1.DataCRC32 != h2.DataCRC32 {
		t.Errorf("data crc not reproducible: %#x vs %#x", h1.DataCRC32, h2.DataCRC32)
	}
}

func TestRestTypeNamesAndParse(t *testing.T) {
	cases := []struct {
		r     mskblob.RestType
		names string
	}{
		{0, ""},
		{mskblob.Static, "static"},
		{mskblob.Static | mskblob.Parse, "static,parse"},
		{mskblob.HTMLTemplate | mskblob.Response | mskblob.Nomux, "tpl,rsp,nomux"},
		// mskblob's own flags live in the second byte, after miniskin's.
		{mskblob.Mskblob | mskblob.Nomux, "nomux,mskblob"},
		{mskblob.MskBlobAuto | mskblob.Nomux, "nomux,auto"},
		{mskblob.MskBlobAuto, "auto"},
	}
	for _, c := range cases {
		if got := c.r.Names(); got != c.names {
			t.Errorf("%#x Names() = %q, want %q", uint32(c.r), got, c.names)
		}
		// Names round-trip through JSON (the manifest representation), via
		// Item.UnmarshalJSON — the path a real manifest read takes.
		var it mskblob.Item
		if err := json.Unmarshal([]byte(`{"restype":"`+c.names+`"}`), &it); err != nil {
			t.Errorf("parse %q: %v", c.names, err)
		}
		if it.RestType != c.r {
			t.Errorf("parse %q = %#x, want %#x", c.names, uint32(it.RestType), uint32(c.r))
		}
	}
	// hex and decimal are also accepted on input.
	for _, s := range []string{`"0x05"`, `5`, `"5"`} {
		var it mskblob.Item
		if err := json.Unmarshal([]byte(`{"restype":`+s+`}`), &it); err != nil || it.RestType != mskblob.Static|mskblob.Parse {
			t.Errorf("parse %s = %#x, %v", s, uint32(it.RestType), err)
		}
	}
	var it mskblob.Item
	if err := json.Unmarshal([]byte(`{"restype":"bogus"}`), &it); err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestManifestReadsDataCRC(t *testing.T) {
	var m mskblob.Manifest
	if err := json.Unmarshal([]byte(`{"id":"x","dataCRC32":"0xBAA500AF","nocase":true,"items":[{"url":"a","src":"a"}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.DataCRC32 != 0xBAA500AF {
		t.Errorf("dataCRC32 = %#x, want 0xBAA500AF", m.DataCRC32)
	}
	if m.ID != "x" || !m.NoCase {
		t.Errorf("manifest = %+v", m)
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	inputs := writeSources(t, dir, map[string][]byte{"a.css": []byte("body{}"), "img/b.png": []byte("PNG")})
	if _, err := mskblob.Write(filepath.Join(dir, "m.blob"), inputs, mskblob.Options{ID: "mid"}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(filepath.Join(dir, "m.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	raw, err := json.Marshal(b.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	// Computed numbers are hex strings, not JSON numbers.
	if !strings.Contains(string(raw), `"crc32": "0x`) && !strings.Contains(string(raw), `"crc32":"0x`) {
		t.Errorf("crc32 not a hex string in: %s", raw)
	}

	var back mskblob.Manifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != "mid" {
		t.Errorf("id = %q", back.ID)
	}
	if len(back.Items) != 2 {
		t.Fatalf("items = %d", len(back.Items))
	}
	// Decode keeps the declarative fields and drops the computed ones.
	for _, it := range back.Items {
		if it.URL == "" {
			t.Error("empty url after decode")
		}
		if it.Size != 0 || it.CRC32 != 0 || it.Offset != 0 {
			t.Errorf("%s: computed fields should be ignored on decode (size=%d crc=%#x off=%d)", it.URL, it.Size, it.CRC32, it.Offset)
		}
	}
}

func TestNoCase(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s")
	if err := os.WriteFile(src, []byte("X"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A case-insensitive blob: stored as mixed case, found by any case.
	if _, err := mskblob.Write(filepath.Join(dir, "ci.blob"), []mskblob.Item{{URL: "Img/Logo.PNG", Filename: "Logo.PNG", RestType: mskblob.Static, Src: src}}, mskblob.Options{NoCase: true}); err != nil {
		t.Fatal(err)
	}
	h, err := mskblob.ReadHeader(filepath.Join(dir, "ci.blob"))
	if err != nil {
		t.Fatal(err)
	}
	if !h.NoCase {
		t.Error("header NoCase = false, want true")
	}
	b, err := mskblob.Open(filepath.Join(dir, "ci.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, q := range []string{"Img/Logo.PNG", "img/logo.png", "IMG/LOGO.PNG"} {
		if it := b.GetByURL(q); it == nil {
			t.Errorf("nocase GetByURL(%q) = nil", q)
		} else if it.URL != "Img/Logo.PNG" {
			t.Errorf("stored URL case not preserved: %q", it.URL)
		}
	}

	// A case-sensitive blob (default): only the exact case matches, and the header
	// flag is off.
	if _, err := mskblob.Write(filepath.Join(dir, "cs.blob"), []mskblob.Item{{URL: "Logo.PNG", RestType: mskblob.Static, Src: src}}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	cs, err := mskblob.Open(filepath.Join(dir, "cs.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if cs.Header().NoCase {
		t.Error("case-sensitive blob reports NoCase=true")
	}
	if cs.GetByURL("logo.png") != nil {
		t.Error("case-sensitive blob matched a different case")
	}

	// With NoCase, two URLs that differ only by case collide and are rejected.
	dup := []mskblob.Item{{URL: "A.png", RestType: mskblob.Static, Src: src}, {URL: "a.PNG", RestType: mskblob.Static, Src: src}}
	if _, err := mskblob.Write(filepath.Join(dir, "dup.blob"), dup, mskblob.Options{NoCase: true}); err == nil {
		t.Error("expected case-folded duplicate url to be rejected under NoCase")
	}
}

// A blob's bytes are opaque, so a blob can hold another one. The entry is marked
// mskblob and OpenBlob mounts it in place — over its own section of the parent,
// nothing extracted — which composes: this builds three levels and reads the
// innermost file through the outermost handle.
func TestNestedBlobs(t *testing.T) {
	dir := t.TempDir()
	want := []byte("bytes at the bottom")
	leafSrc := filepath.Join(dir, "deep.txt")
	if err := os.WriteFile(leafSrc, want, 0o644); err != nil {
		t.Fatal(err)
	}

	// The leaf: an ordinary blob, built like any other.
	leaf := filepath.Join(dir, "leaf.blob")
	if _, err := mskblob.Write(leaf, []mskblob.Item{
		{URL: "deep.txt", Filename: "deep.txt", RestType: mskblob.Static, Src: leafSrc},
	}, mskblob.Options{ID: "leaf-id"}); err != nil {
		t.Fatal(err)
	}

	// The middle level: the leaf as a key-only entry, plus a file of its own.
	mid := filepath.Join(dir, "mid.blob")
	if _, err := mskblob.Write(mid, []mskblob.Item{
		{URL: "own.txt", Filename: "own.txt", RestType: mskblob.Static, Src: leafSrc},
		{Key: "/leaf", Filename: "leaf.blob", RestType: mskblob.Mskblob | mskblob.Nomux, Src: leaf},
	}, mskblob.Options{ID: "mid-id"}); err != nil {
		t.Fatal(err)
	}

	// The container: one file to deploy, holding the whole tree.
	outer := filepath.Join(dir, "outer.blob")
	if _, err := mskblob.Write(outer, []mskblob.Item{
		{Key: "/mid", Filename: "mid.blob", RestType: mskblob.Mskblob | mskblob.Nomux, Src: mid},
	}, mskblob.Options{ID: "outer-id"}); err != nil {
		t.Fatal(err)
	}

	b, err := mskblob.Open(outer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer b.Close()

	// Each level keeps its own identity, index and case rule: nesting copies the
	// child in byte for byte, so its guid survives the trip.
	m, err := b.OpenBlob("/mid")
	if err != nil {
		t.Fatalf("OpenBlob(/mid): %v", err)
	}
	if m.Header().ID != "mid-id" {
		t.Errorf("middle id = %q, want mid-id", m.Header().ID)
	}
	l, err := m.OpenBlob("/leaf")
	if err != nil {
		t.Fatalf("OpenBlob(/leaf): %v", err)
	}
	if l.Header().ID != "leaf-id" {
		t.Errorf("leaf id = %q, want leaf-id", l.Header().ID)
	}

	// LoadBlob is the same mount with the guid checked, like Load is for a file: a
	// child rebuilt or swapped inside the container is rejected, not read.
	if _, err := m.LoadBlob("/leaf", "leaf-id"); err != nil {
		t.Errorf("LoadBlob with the right id: %v", err)
	}
	if _, err := m.LoadBlob("/leaf", "another-id"); err == nil {
		t.Error("expected LoadBlob to reject a mismatched id")
	}

	it := l.GetByURL("deep.txt")
	if it == nil {
		t.Fatal("leaf entry not found through two levels of nesting")
	}
	if it.CRC32 != crc32.ChecksumIEEE(want) {
		t.Errorf("leaf crc = %#x, want %#x", it.CRC32, crc32.ChecksumIEEE(want))
	}
	got, err := l.Bytes(it)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Bytes = %q, want %q", got, want)
	}
	streamed, err := io.ReadAll(l.Reader(it))
	if err != nil {
		t.Fatalf("Reader: %v", err)
	}
	if string(streamed) != string(want) {
		t.Errorf("Reader = %q, want %q", streamed, want)
	}

	// A nested blob owns no descriptor, so closing it releases nothing and the tree
	// keeps working.
	if err := l.Close(); err != nil {
		t.Errorf("closing a nested blob: %v", err)
	}
	if _, err := l.Bytes(it); err != nil {
		t.Errorf("read after closing a nested blob: %v", err)
	}

	// Closing the root closes the subtree with it: reads fail loudly rather than
	// reaching a freed (and possibly reused) descriptor.
	if err := b.Close(); err != nil {
		t.Fatalf("closing the root: %v", err)
	}
	if _, err := l.Bytes(it); !errors.Is(err, mskblob.ErrClosed) {
		t.Errorf("Bytes after closing the root = %v, want ErrClosed", err)
	}
	if _, err := io.ReadAll(l.Reader(it)); !errors.Is(err, mskblob.ErrClosed) {
		t.Errorf("Reader after closing the root = %v, want ErrClosed", err)
	}
}

// A nested blob is mounted by key and never served, so Write refuses to record one
// with a URL: without a URL it is not in the routing index at all, and no handler
// can hand out the container's bytes as if they were an asset.
func TestNestedBlobIsKeyOnly(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(dir, "inner.blob")
	if _, err := mskblob.Write(inner, []mskblob.Item{
		{URL: "x.txt", Filename: "x.txt", RestType: mskblob.Static, Src: src},
	}, mskblob.Options{ID: "inner-id"}); err != nil {
		t.Fatal(err)
	}

	if _, err := mskblob.Write(filepath.Join(dir, "bad.blob"), []mskblob.Item{
		{URL: "inner.blob", Key: "/inner", RestType: mskblob.Mskblob | mskblob.Nomux, Src: inner},
	}, mskblob.Options{}); err == nil {
		t.Error("expected a nested blob carrying a url to be rejected")
	}

	outer := filepath.Join(dir, "outer.blob")
	if _, err := mskblob.Write(outer, []mskblob.Item{
		{Key: "/inner", Filename: "inner.blob", RestType: mskblob.Mskblob | mskblob.Nomux, Src: inner},
	}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(outer)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, q := range []string{"inner", "/inner", "inner.blob"} {
		if b.GetByURL(q) != nil {
			t.Errorf("nested blob reachable by url %q", q)
		}
	}

	// The flag round-trips through the manifest's human names.
	if n := mskblob.Mskblob.Names(); n != "mskblob" {
		t.Errorf("Mskblob.Names() = %q, want mskblob", n)
	}

	// Mounting something that isn't marked as a nested blob is an error, not a
	// misreading of whatever bytes happen to be there.
	if _, err := b.OpenBlob("/nope"); err == nil {
		t.Error("expected OpenBlob on a missing key to fail")
	}
	in, err := mskblob.Open(inner)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if _, err := in.OpenBlob("/x.txt"); err == nil {
		t.Error("expected OpenBlob on a plain entry to fail")
	}
}

func TestReadHeaderBadMagic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.blob")
	buf := make([]byte, 128) // blocks A+B (64+64): the fixed header area
	copy(buf, "XXXX")
	if err := os.WriteFile(p, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mskblob.ReadHeader(p); err == nil {
		t.Error("expected error for bad magic")
	}
}

// A blob cannot include itself: an item whose Src is the output file is rejected
// before the output is created, so the existing file survives untouched. Without
// the check the copy reads back what it appends and never ends.
func TestWriteRejectsOutputAsSource(t *testing.T) {
	dir := t.TempDir()
	items := writeSources(t, dir, map[string][]byte{"a.txt": []byte("hello")})
	out := filepath.Join(dir, "x.blob")
	if _, err := mskblob.Write(out, items, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The directory must exist: on POSIX "sub/.." is resolved through the filesystem.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := map[string]mskblob.Item{
		"nested blob": {Key: "/self", RestType: mskblob.Mskblob | mskblob.Nomux, Src: out},
		"plain item":  {URL: "self.bin", RestType: mskblob.Static, Src: out},
		// Same file under another spelling: compared as files, not as strings.
		"other spelling": {URL: "self.bin", RestType: mskblob.Static, Src: filepath.Join(dir, "sub", "..", "x.blob")},
	}
	for name, self := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := mskblob.Write(out, append([]mskblob.Item{self}, items...), mskblob.Options{})
			if err == nil || !strings.Contains(err.Error(), "cannot include itself") {
				t.Fatalf("Write with src == output: got %v, want a cannot-include-itself error", err)
			}
			after, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("the existing output file was modified by a rejected Write")
			}
		})
	}
}

// The flag values are part of the binary format: miniskin's in the low byte,
// mskblob's own in the second one.
func TestOwnFlagsLiveInSecondByte(t *testing.T) {
	if mskblob.Mskblob != 0x0100 || mskblob.MskBlobAuto != 0x0200 {
		t.Fatalf("Mskblob = %#x, MskBlobAuto = %#x; want 0x100, 0x200", uint32(mskblob.Mskblob), uint32(mskblob.MskBlobAuto))
	}
	const miniskin = mskblob.Static | mskblob.HTMLTemplate | mskblob.Parse | mskblob.Response | mskblob.Nomux
	if miniskin&^0x00FF != 0 {
		t.Errorf("a miniskin flag left the low byte: %#x", uint32(miniskin))
	}
	if own := mskblob.Mskblob | mskblob.MskBlobAuto; own&^0xFF00 != 0 {
		t.Errorf("an own flag left the second byte: %#x", uint32(own))
	}
}

// An entry carrying one of mskblob's own flags is never served, and has to say so:
// Write packs it only with a key, without a url and flagged nomux.
func TestOwnFlagsRequireNomux(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, own := range []mskblob.RestType{mskblob.Mskblob, mskblob.MskBlobAuto, mskblob.Mskblob | mskblob.MskBlobAuto, 0x8000} {
		bad := map[string]struct {
			it   mskblob.Item
			want string
		}{
			"no nomux": {mskblob.Item{Key: "/k", RestType: own, Src: src}, `must also be flagged "nomux"`},
			"no key":   {mskblob.Item{URL: "k", RestType: own | mskblob.Nomux, Src: src}, "needs a key"},
			"url":      {mskblob.Item{Key: "/k", URL: "k", RestType: own | mskblob.Nomux, Src: src}, "must have no url"},
		}
		for name, c := range bad {
			out := filepath.Join(dir, "bad.blob")
			_, err := mskblob.Write(out, []mskblob.Item{c.it}, mskblob.Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("%#x %s: got %v, want an error containing %q", uint32(own), name, err, c.want)
			}
			if _, statErr := os.Stat(out); statErr == nil {
				t.Errorf("%#x %s: the rejected blob was created anyway", uint32(own), name)
			}
		}
	}
	// A miniskin flag alone asks for nothing: nomux stays optional in the low byte.
	if _, err := mskblob.Write(filepath.Join(dir, "ok.blob"), []mskblob.Item{
		{URL: "x.txt", RestType: mskblob.Static, Src: src},
	}, mskblob.Options{}); err != nil {
		t.Errorf("a plain static entry was rejected: %v", err)
	}
}

// A blob packed by something else may hold an own-flagged entry without nomux.
// Opening it fails: reading the entry as a plain one would let it be served.
func TestOpenRejectsOwnFlagWithoutNomux(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	const key = "/cfg"
	path := filepath.Join(dir, "b.blob")
	if _, err := mskblob.Write(path, []mskblob.Item{
		{Key: key, RestType: mskblob.MskBlobAuto | mskblob.Nomux, Src: src},
	}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	if b, err := mskblob.Open(path); err != nil {
		t.Fatalf("the well-formed blob does not open: %v", err)
	} else {
		b.Close()
	}

	// Clear nomux in the only index record: 128-byte header, u32 entry size, the
	// key and its NUL, u64 size, u32 crc — then the u32 restype, little-endian.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	at := 128 + 4 + len(key) + 1 + 8 + 4
	if raw[at] != byte(mskblob.Nomux) || raw[at+1] != byte(mskblob.MskBlobAuto>>8) {
		t.Fatalf("restype not where expected: % x", raw[at:at+4])
	}
	raw[at] = 0
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(path)
	if err == nil {
		b.Close()
		t.Fatal("a blob with an own-flagged entry lacking nomux opened")
	}
	if !strings.Contains(err.Error(), `without "nomux"`) || !strings.Contains(err.Error(), key) {
		t.Errorf("error does not name the entry and the missing flag: %v", err)
	}
}

// nomux means not routed: the handler treats the entry as absent — for the
// middleware as much as for the default serving — while it stays reachable by key.
func TestHandlerDoesNotRouteNomux(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	path := filepath.Join(dir, "b.blob")
	if _, err := mskblob.Write(path, []mskblob.Item{
		{URL: "open.txt", RestType: mskblob.Static, Src: write("open.txt", "public")},
		{URL: "hidden.txt", Key: "/hidden", RestType: mskblob.Static | mskblob.Nomux, Src: write("hidden.txt", "private")},
	}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	get := func(h http.Handler, path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get(b.Handler("/", nil), "/open.txt"); code != http.StatusOK || body != "public" {
		t.Errorf("GET /open.txt = %d %q", code, body)
	}
	if code, body := get(b.Handler("/", nil), "/hidden.txt"); code != http.StatusNotFound || strings.Contains(body, "private") {
		t.Errorf("GET /hidden.txt = %d %q, want 404", code, body)
	}

	// A middleware that would serve anything it is handed never gets the entry.
	seen := map[string]bool{}
	eager := func(w http.ResponseWriter, r *http.Request, it *mskblob.Item) int {
		seen[r.URL.Path] = it != nil
		if it == nil {
			return mskblob.DispatchAuto
		}
		data, _ := b.Bytes(it)
		w.Write(data)
		return mskblob.DispatchDone
	}
	if code, body := get(b.Handler("/", eager), "/hidden.txt"); code != http.StatusNotFound || strings.Contains(body, "private") {
		t.Errorf("GET /hidden.txt through an eager middleware = %d %q, want 404", code, body)
	}
	if seen["/hidden.txt"] {
		t.Error("the middleware was handed a nomux entry")
	}

	// By key nothing changes.
	it := b.GetByKey("/hidden")
	if it == nil {
		t.Fatal("nomux entry not found by key")
	}
	if data, err := b.Bytes(it); err != nil || string(data) != "private" {
		t.Errorf("nomux entry by key = %q, %v", data, err)
	}
}
