package mskblob_test

import (
	"encoding/json"
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
