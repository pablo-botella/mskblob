package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pablo-botella/mskblob"
)

func TestParseListManifest(t *testing.T) {
	in := "# a comment\n\nlogo.png\timg/logo.png\nplain.css\n  # trailing comment\n"
	items := parseListManifest([]byte(in))
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].URL != "logo.png" || items[0].Src != "img/logo.png" {
		t.Errorf("entry 0 = %+v", items[0])
	}
	if items[1].URL != "plain.css" || items[1].Src != "plain.css" {
		t.Errorf("entry 1 = %+v (url should default to src)", items[1])
	}
	for _, it := range items {
		if it.RestType != mskblob.Static {
			t.Errorf("%s: restype = %#x, want static", it.URL, it.RestType)
		}
	}
}

func TestReadManifestJSONObject(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "m.json")
	body := `{"id":"abc","items":[{"url":"a.css","src":"sub/a.css","restype":"static,parse"}]}`
	if err := os.WriteFile(mf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(mf, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "abc" || len(m.Items) != 1 {
		t.Fatalf("manifest = %+v", m)
	}
	it := m.Items[0]
	if it.RestType != mskblob.Static|mskblob.Parse {
		t.Errorf("restype = %#x", it.RestType)
	}
	if it.Filename != "a.css" { // defaults to url
		t.Errorf("filename = %q, want a.css", it.Filename)
	}
	// relative src resolves against the manifest's own dir
	if want := filepath.Join(dir, "sub", "a.css"); it.Src != want {
		t.Errorf("src = %q, want %q", it.Src, want)
	}
}

func TestReadManifestBareArrayAndBase(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "arr.json")
	if err := os.WriteFile(mf, []byte(`[{"url":"a","src":"a"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(dir, "assets")
	m, err := readManifest(mf, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 1 {
		t.Fatalf("items = %d", len(m.Items))
	}
	if m.Items[0].RestType != mskblob.Static { // zero defaults to static
		t.Errorf("restype = %#x", m.Items[0].RestType)
	}
	if want := filepath.Join(base, "a"); m.Items[0].Src != want {
		t.Errorf("src = %q, want %q (resolved against -base)", m.Items[0].Src, want)
	}
}

func TestReadManifestRequiresURLAndSrc(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(mf, []byte(`[{"url":"a"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(mf, "", ""); err == nil {
		t.Error("expected error: src is required")
	}
}

func TestReadManifestDirFillsSrc(t *testing.T) {
	dir := t.TempDir()
	mf := filepath.Join(dir, "m.json")
	// A blob-derived manifest: url present, no src (like `list -json` output).
	body := `{"id":"keep-me","items":[{"url":"img/logo.png"},{"url":"app.css","key":"/app.css"}]}`
	if err := os.WriteFile(mf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srcdir := filepath.Join(dir, "assets")
	m, err := readManifest(mf, "", srcdir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "keep-me" { // editing the id in the JSON carries through
		t.Errorf("id = %q", m.ID)
	}
	if want := filepath.Join(srcdir, "img", "logo.png"); m.Items[0].Src != want {
		t.Errorf("src[0] = %q, want %q", m.Items[0].Src, want)
	}
	if want := filepath.Join(srcdir, "app.css"); m.Items[1].Src != want {
		t.Errorf("src[1] = %q, want %q", m.Items[1].Src, want)
	}
}

func TestCreateDumpRoundTrip(t *testing.T) {
	dir := t.TempDir()
	// sources
	if err := os.WriteFile(filepath.Join(dir, "a.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "img"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "img", "b.png"), []byte("PNGDATA"), 0o644); err != nil {
		t.Fatal(err)
	}

	items := []mskblob.Item{
		{URL: "a.css", Src: filepath.Join(dir, "a.css"), RestType: mskblob.Static},
		{URL: "img/b.png", Src: filepath.Join(dir, "img", "b.png"), RestType: mskblob.Static},
	}
	blobPath := filepath.Join(dir, "t.blob")
	if _, err := mskblob.Write(blobPath, items, mskblob.Options{ID: "rt"}); err != nil {
		t.Fatal(err)
	}

	// dump → directory with the asset files; the manifest is written only on request
	out := filepath.Join(dir, "out")
	mf := filepath.Join(out, "manifest.json")
	if err := cmdDump([]string{"-blob", blobPath, "-baseout", out, "-manifest", mf}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.css", filepath.Join("img", "b.png"), "manifest.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing dumped %s: %v", f, err)
		}
	}

	// without -manifest, no manifest.json is written (only the asset files)
	bare := filepath.Join(dir, "bare")
	if err := cmdDump([]string{"-blob", blobPath, "-baseout", bare}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bare, "manifest.json")); !os.IsNotExist(err) {
		t.Errorf("manifest.json should not be written without -manifest (err=%v)", err)
	}

	// create a new blob from the dumped manifest, it must serve the same bytes
	rebuilt := filepath.Join(dir, "rebuilt.blob")
	if err := cmdCreate([]string{"-manifest", filepath.Join(out, "manifest.json"), "-out", rebuilt}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Header().ID != "rt" { // id preserved from the dumped manifest
		t.Errorf("id = %q, want rt", b.Header().ID)
	}
	it := b.GetByURL("img/b.png")
	if it == nil {
		t.Fatal("missing img/b.png after round-trip")
	}
	got, err := b.Bytes(it)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "PNGDATA" {
		t.Errorf("bytes = %q", got)
	}
}

// A key-only entry — a template, or a nested blob — has no url to lay out on disk,
// so dump lands it at its key. Taking the url alone left it nameless and wrote over
// the base dir itself.
func TestDumpKeyOnlyEntries(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "z.txt")
	if err := os.WriteFile(src, []byte("hi!"), 0o644); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(dir, "inner.blob")
	if _, err := mskblob.Write(inner, []mskblob.Item{
		{URL: "z.txt", Src: src, RestType: mskblob.Static},
	}, mskblob.Options{ID: "inner"}); err != nil {
		t.Fatal(err)
	}
	outer := filepath.Join(dir, "outer.blob")
	if _, err := mskblob.Write(outer, []mskblob.Item{
		{Key: "/page", Filename: "page.html", Src: src, RestType: mskblob.HTMLTemplate},
		{Key: "/sub/inner", Filename: "inner.blob", Src: inner, RestType: mskblob.Mskblob | mskblob.Nomux},
	}, mskblob.Options{ID: "outer"}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out")
	mf := filepath.Join(out, "manifest.json")
	if err := cmdDump([]string{"-blob", outer, "-baseout", out, "-manifest", mf}); err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, f := range []string{"page", filepath.Join("sub", "inner")} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Errorf("missing dumped %s: %v", f, err)
		}
	}

	// The manifest round-trips, nested blob included: it still mounts and serves.
	rebuilt := filepath.Join(dir, "rebuilt.blob")
	if err := cmdCreate([]string{"-manifest", mf, "-out", rebuilt}); err != nil {
		t.Fatalf("create: %v", err)
	}
	b, err := mskblob.Open(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.Header().ID != "outer" {
		t.Errorf("id = %q, want outer", b.Header().ID)
	}
	child, err := b.LoadBlob("/sub/inner", "inner")
	if err != nil {
		t.Fatalf("LoadBlob after the round-trip: %v", err)
	}
	it := child.GetByURL("z.txt")
	if it == nil {
		t.Fatal("missing z.txt inside the nested blob")
	}
	got, err := child.Bytes(it)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi!" {
		t.Errorf("nested bytes = %q", got)
	}

	// Single-entry extraction agrees with full extraction on where things land.
	one := filepath.Join(dir, "one")
	if err := cmdDump([]string{"-blob", outer, "-file", "/sub/inner", "-baseout", one}); err != nil {
		t.Fatalf("dump -file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(one, "sub", "inner")); err != nil {
		t.Errorf("single-entry dump landed elsewhere: %v", err)
	}
}

func TestDumpSingleFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "frita.png")
	if err := os.WriteFile(src, []byte("FRITA"), 0o644); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(dir, "t.blob")
	items := []mskblob.Item{{URL: "patata/frita.png", Key: "/patata/frita.png", Src: src, RestType: mskblob.Static}}
	if _, err := mskblob.Write(blobPath, items, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}

	// -baseout: the one entry lands under the dir at its url subpath
	out := filepath.Join(dir, "out")
	if err := cmdDump([]string{"-blob", blobPath, "-file", "/patata/frita.png", "-baseout", out}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "patata", "frita.png")); string(got) != "FRITA" {
		t.Errorf("baseout single: got %q", got)
	}

	// -out <file>: exact path
	exact := filepath.Join(dir, "exact.png")
	if err := cmdDump([]string{"-blob", blobPath, "-file", "/patata/frita.png", "-out", exact}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exact); string(got) != "FRITA" {
		t.Errorf("out file: got %q", got)
	}

	// -out <dir/>: trailing separator → basename inside the dir
	od := filepath.Join(dir, "od") + string(os.PathSeparator)
	if err := cmdDump([]string{"-blob", blobPath, "-file", "/patata/frita.png", "-out", od}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "od", "frita.png")); string(got) != "FRITA" {
		t.Errorf("out dir: got %q", got)
	}

	// -stdout: bytes go to stdout
	old := os.Stdout
	f, err := os.CreateTemp(dir, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = f
	derr := cmdDump([]string{"-blob", blobPath, "-file", "/patata/frita.png", "-stdout"})
	os.Stdout = old
	f.Close()
	if derr != nil {
		t.Fatal(derr)
	}
	if got, _ := os.ReadFile(f.Name()); string(got) != "FRITA" {
		t.Errorf("stdout: got %q", got)
	}

	// errors: no destination, two destinations, unknown key, -out without -file
	if err := cmdDump([]string{"-blob", blobPath, "-file", "/patata/frita.png"}); err == nil {
		t.Error("no destination should error")
	}
	if err := cmdDump([]string{"-blob", blobPath, "-file", "/patata/frita.png", "-out", "x", "-stdout"}); err == nil {
		t.Error("two destinations should error")
	}
	if err := cmdDump([]string{"-blob", blobPath, "-file", "/nope", "-stdout"}); err == nil {
		t.Error("unknown key should error")
	}
	if err := cmdDump([]string{"-blob", blobPath, "-out", "x"}); err == nil {
		t.Error("-out without -file should error")
	}
}

func TestMergeVars(t *testing.T) {
	global := map[string]any{"title": "G", "env": "prod"}
	local := map[string]any{"title": "L", "extra": 1}
	got := mergeVars(global, local)
	if got["title"] != "L" { // blob wins
		t.Errorf("title = %v, want L", got["title"])
	}
	if got["env"] != "prod" || got["extra"] != 1 {
		t.Errorf("merge = %v", got)
	}
	// inputs untouched
	if global["title"] != "G" {
		t.Error("mergeVars mutated the global map")
	}
}

func TestWithHeaders(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	h := withHeaders(base, []headerKV{{Name: "X-A", Value: "1"}, {Name: "Cache-Control", Value: "no-store"}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-A") != "1" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers not applied: %v", resp.Header)
	}
	// nil headers → same handler back (no wrapping)
	if withHeaders(base, nil) == nil {
		t.Error("withHeaders(nil) returned nil")
	}
}

func TestParseBlobTemplates(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "t")
	if err := os.WriteFile(tpl, []byte("<h1>{{.title}}</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(dir, "p")
	if err := os.WriteFile(png, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []mskblob.Item{
		{URL: "page", RestType: mskblob.HTMLTemplate, Src: tpl},
		{URL: "logo.png", RestType: mskblob.Static, Src: png},
	}
	if _, err := mskblob.Write(filepath.Join(dir, "b.blob"), items, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(filepath.Join(dir, "b.blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	tmpls, err := parseBlobTemplates(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpls) != 1 || tmpls["page"] == nil { // only the template, not the static
		t.Fatalf("templates = %v", tmpls)
	}
	var sb strings.Builder
	if err := tmpls["page"].Execute(&sb, map[string]any{"title": "Hi"}); err != nil {
		t.Fatal(err)
	}
	if sb.String() != "<h1>Hi</h1>" {
		t.Errorf("render = %q", sb.String())
	}
}

func TestCreateCRCCheck(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.png")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Build a pinned-id blob and capture its data CRC.
	blob1 := filepath.Join(dir, "v1.blob")
	if _, err := mskblob.Write(blob1, []mskblob.Item{{URL: "a.png", RestType: mskblob.Static, Src: src}}, mskblob.Options{ID: "pinned"}); err != nil {
		t.Fatal(err)
	}
	h, err := mskblob.ReadHeader(blob1)
	if err != nil {
		t.Fatal(err)
	}
	mf := filepath.Join(dir, "m.json")
	withID := fmt.Sprintf(`{"id":"pinned","dataCRC32":"0x%08X","items":[{"url":"a.png"}]}`, h.DataCRC32)
	if err := os.WriteFile(mf, []byte(withID), 0o644); err != nil {
		t.Fatal(err)
	}
	// Change the content so the rebuilt CRC differs.
	if err := os.WriteFile(src, []byte("v2-different"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Non-strict: warns (stderr) but succeeds.
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", filepath.Join(dir, "o1.blob")}); err != nil {
		t.Errorf("non-strict crc mismatch should warn, not fail: %v", err)
	}
	// Strict: fails.
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", filepath.Join(dir, "o2.blob"), "-strict"}); err == nil {
		t.Error("strict crc mismatch should fail")
	}
	// No pinned id: the check is skipped (fresh id each build), even with -strict.
	noID := fmt.Sprintf(`{"dataCRC32":"0x%08X","items":[{"url":"a.png"}]}`, h.DataCRC32)
	if err := os.WriteFile(mf, []byte(noID), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", filepath.Join(dir, "o3.blob"), "-strict"}); err != nil {
		t.Errorf("no-id manifest should skip the crc check even with -strict: %v", err)
	}

	// Pinned id but no crc to verify against: warns; fails under -strict.
	idNoCRC := `{"id":"pinned","items":[{"url":"a.png"}]}`
	if err := os.WriteFile(mf, []byte(idNoCRC), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", filepath.Join(dir, "o4.blob")}); err != nil {
		t.Errorf("pinned id without crc should warn, not fail: %v", err)
	}
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", filepath.Join(dir, "o5.blob"), "-strict"}); err == nil {
		t.Error("strict + pinned id without crc should fail")
	}
}

func TestCreateSkipUnchanged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.png")
	if err := os.WriteFile(src, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "o.blob")
	mf := filepath.Join(dir, "m.json")
	if err := os.WriteFile(mf, []byte(`{"id":"pinned","items":[{"url":"a.png"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// First build writes the blob.
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", out, "-skip-unchanged"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}

	// Delete the source: a real build would fail to read it. -skip-unchanged must
	// short-circuit on the matching pinned id without touching any source file.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", out, "-skip-unchanged"}); err != nil {
		t.Errorf("matching id should skip the build (no source read): %v", err)
	}
	if fi2, err := os.Stat(out); err != nil {
		t.Fatal(err)
	} else if !fi2.ModTime().Equal(fi.ModTime()) {
		t.Error("skipped build should leave the output file untouched")
	}

	// A different pinned id does not match, so the build runs — and now fails
	// because the source is gone, proving it was actually attempted.
	if err := os.WriteFile(mf, []byte(`{"id":"other","items":[{"url":"a.png"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", out, "-skip-unchanged"}); err == nil {
		t.Error("non-matching id should attempt the build (and fail on the missing source)")
	}

	// No pinned id: nothing stable to compare, so it always builds (and fails here).
	if err := os.WriteFile(mf, []byte(`{"items":[{"url":"a.png"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdCreate([]string{"-manifest", mf, "-dir", dir, "-out", out, "-skip-unchanged"}); err == nil {
		t.Error("no pinned id should fall through to a normal build")
	}
}

func TestRenderTableAlignment(t *testing.T) {
	out := renderTable(
		[]string{"URL", "SIZE"},
		[][]string{{"a", "1"}, {"longer", "100"}},
		[]bool{false, true},
	)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 { // header, separator, 2 rows
		t.Fatalf("lines = %d:\n%s", len(lines), out)
	}
	// right-aligned SIZE column uses a trailing ':' in the separator
	if !strings.Contains(lines[1], ":|") {
		t.Errorf("separator missing right-align marker: %q", lines[1])
	}
	// all rows share the same width
	for _, l := range lines {
		if len(l) != len(lines[0]) {
			t.Errorf("row width mismatch:\n%s", out)
			break
		}
	}
}

// serve mounts a blob nested in a file only when the config names it: internal_path
// lists the keys to descend through, one nested blob per element. One file serves
// three mounts here — itself, its child and its grandchild — and is opened once.
func TestServeInternalPath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	build := func(name, id string, items []mskblob.Item) string {
		p := filepath.Join(dir, name)
		if _, err := mskblob.Write(p, items, mskblob.Options{ID: id}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inner := build("inner.blob", "inner", []mskblob.Item{
		{URL: "z.txt", Src: write("z.txt", "deep"), RestType: mskblob.Static},
	})
	// The grandchild's key has slashes of its own: the reason internal_path is an
	// array of keys and not one slash-separated string.
	mid := build("mid.blob", "mid", []mskblob.Item{
		{URL: "m.txt", Src: write("m.txt", "middle"), RestType: mskblob.Static},
		{Key: "/sub/inner", Src: inner, RestType: mskblob.Mskblob | mskblob.Nomux},
	})
	site := build("site.blob", "site", []mskblob.Item{
		{URL: "r.txt", Src: write("r.txt", "root"), RestType: mskblob.Static},
		{Key: "/mid", Src: mid, RestType: mskblob.Mskblob | mskblob.Nomux},
	})

	// Through the JSON, so the field name is part of what is tested.
	config := func(blobs string) serveConfig {
		t.Helper()
		var cfg serveConfig
		raw := strings.ReplaceAll(blobs, "SITE", filepath.ToSlash(site))
		if err := json.Unmarshal([]byte(`{"blobs":[`+raw+`]}`), &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	mux, closeBlobs, err := mountBlobs(config(`
		{"file": "SITE", "base": "/", "id": "site"},
		{"file": "SITE", "base": "/m/", "internal_path": ["/mid"]},
		{"file": "SITE", "base": "/deep/", "internal_path": ["/mid", "/sub/inner"], "id": "inner"}`), nil, "")
	if err != nil {
		t.Fatalf("mountBlobs: %v", err)
	}
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	for path, want := range map[string]string{"/r.txt": "root", "/m/m.txt": "middle", "/deep/z.txt": "deep"} {
		if code, body := get(path); code != http.StatusOK || body != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, code, body, want)
		}
	}
	// Nothing is mounted by itself: the nested blob is not reachable through its
	// parent, neither by its key nor by a path that was not configured.
	for _, path := range []string{"/mid", "/mid/m.txt", "/sub/inner/z.txt", "/m/sub/inner", "/m/z.txt"} {
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
	// Closing releases the one shared descriptor; on Windows the temp dir could not
	// be removed otherwise.
	closeBlobs()
	if err := os.Remove(site); err != nil {
		t.Errorf("site.blob still open after close: %v", err)
	}
	site = build("site.blob", "site", []mskblob.Item{
		{URL: "r.txt", Src: write("r.txt", "root"), RestType: mskblob.Static},
		{Key: "/mid", Src: mid, RestType: mskblob.Mskblob | mskblob.Nomux},
	})

	// A config that cannot be honoured stops the server from starting, naming the
	// element that fails — and leaves no file open behind.
	bad := map[string]struct{ blobs, want string }{
		"unknown key":      {`{"file": "SITE", "internal_path": ["/nope"]}`, `internal_path[0]`},
		"unknown deep key": {`{"file": "SITE", "internal_path": ["/mid", "/nope"]}`, `internal_path[1]`},
		"not a blob":       {`{"file": "SITE", "base": "/", "internal_path": ["/mid", "/sub/inner", "/k"]}`, `internal_path[2]`},
		"child id":         {`{"file": "SITE", "internal_path": ["/mid"], "id": "site"}`, "id mismatch (found mid, expected site)"},
		"root id":          {`{"file": "SITE", "id": "mid"}`, "id mismatch (found site, expected mid)"},
		"second entry":     {`{"file": "SITE"}, {"file": "SITE", "base": "/x/", "internal_path": ["/r"]}`, `internal_path[0]`},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			_, _, err := mountBlobs(config(c.blobs), nil, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("mountBlobs: got %v, want an error containing %q", err, c.want)
			}
		})
	}
	if err := os.Remove(site); err != nil {
		t.Errorf("site.blob left open by a failed mount: %v", err)
	}
}

// serve -auto: the blob is the entry point and carries the configuration that
// serves it, under a fixed key. There an empty "file" is that same blob, and
// internal_path descends from it. The configuration itself is never served.
func TestServeAuto(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	build := func(name string, items []mskblob.Item) string {
		p := filepath.Join(dir, name)
		if _, err := mskblob.Write(p, items, mskblob.Options{ID: strings.TrimSuffix(name, ".blob")}); err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
		return p
	}
	const autoFlags = mskblob.MskBlobAuto | mskblob.Nomux
	other := build("other.blob", []mskblob.Item{
		{URL: "o.txt", Src: write("o.txt", "other"), RestType: mskblob.Static},
	})
	mid := build("mid.blob", []mskblob.Item{
		{URL: "m.txt", Src: write("m.txt", "middle"), RestType: mskblob.Static},
		// A nested blob's own configuration is ignored when its parent is served.
		{Key: autoConfigKey, Src: write("mid.json", `{"blobs":[{"base":"/wrong/"}]}`), RestType: autoFlags},
	})
	cfgJSON := `{
		"vars": {"who": "auto"},
		"blobs": [
			{"base": "/", "id": "site"},
			{"base": "/m/", "internal_path": ["/mid"], "id": "mid"},
			{"file": "` + filepath.ToSlash(other) + `", "base": "/o/"}
		]}`
	site := build("site.blob", []mskblob.Item{
		{URL: "r.txt", Src: write("r.txt", "root"), RestType: mskblob.Static},
		{URL: "hi.html", Src: write("hi.html", "hello {{.who}}"), RestType: mskblob.HTMLTemplate},
		{Key: "/mid", Src: mid, RestType: mskblob.Mskblob | mskblob.Nomux},
		{Key: autoConfigKey, Src: write("site.json", cfgJSON), RestType: autoFlags},
	})

	b, err := mskblob.Open(site)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAutoConfig(b)
	if err != nil {
		t.Fatalf("loadAutoConfig: %v", err)
	}
	if len(cfg.Blobs) != 3 || cfg.Blobs[0].File != "" || cfg.Vars["who"] != "auto" {
		t.Fatalf("configuration not read as written: %+v", cfg)
	}
	mux, closeBlobs, err := mountBlobs(cfg, b, site)
	if err != nil {
		t.Fatalf("mountBlobs: %v", err)
	}
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	for path, want := range map[string]string{
		"/r.txt":   "root",       // the blob itself: "file" omitted
		"/hi.html": "hello auto", // templates and vars work as with -config
		"/m/m.txt": "middle",     // a nested blob, descended from the blob itself
		"/o/o.txt": "other",      // a "file" with a value is still an external file
	} {
		if code, body := get(path); code != http.StatusOK || body != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, code, body, want)
		}
	}
	// The configuration is never served, by any spelling, from any mount; and the
	// nested blob's own one changed nothing.
	for _, path := range []string{autoConfigKey, "/m" + autoConfigKey, "/site.json", "/mid", "/wrong/m.txt"} {
		if code, body := get(path); code != http.StatusNotFound || strings.Contains(body, "blobs") {
			t.Errorf("GET %s = %d %q, want 404", path, code, body)
		}
	}
	closeBlobs()
	b.Close()

	// Without the attributes that make it a configuration, the server does not start.
	bad := map[string]struct {
		items []mskblob.Item
		want  string
	}{
		"absent": {nil, "carries no configuration of its own"},
		"not flagged auto": {[]mskblob.Item{
			{Key: autoConfigKey, Src: write("c1.json", cfgJSON), RestType: mskblob.Static | mskblob.Nomux},
		}, `must be flagged "nomux,auto"`},
		"plain entry": {[]mskblob.Item{
			{Key: autoConfigKey, URL: "site.json", Src: write("c2.json", cfgJSON), RestType: mskblob.Static},
		}, `must be flagged "nomux,auto"`},
		"not json": {[]mskblob.Item{
			{Key: autoConfigKey, Src: write("c3.json", "addr: 8080"), RestType: autoFlags},
		}, "invalid JSON in entry"},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			items := append([]mskblob.Item{{URL: "r.txt", Src: write("r.txt", "root"), RestType: mskblob.Static}}, c.items...)
			p := build("bad.blob", items)
			// Through the command: it must fail before listening.
			err := cmdServe([]string{"-auto", p})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("serve -auto: got %v, want an error containing %q", err, c.want)
			}
			if err := os.Remove(p); err != nil {
				t.Errorf("the blob was left open: %v", err)
			}
		})
	}

	// A configuration listing no blobs is refused too, naming where it came from.
	empty := build("empty.blob", []mskblob.Item{
		{Key: autoConfigKey, Src: write("c4.json", `{"addr": ":0"}`), RestType: autoFlags},
	})
	if err := cmdServe([]string{"-auto", empty}); err == nil || !strings.Contains(err.Error(), "lists no blobs") || !strings.Contains(err.Error(), "inside") {
		t.Errorf("serve -auto with no blobs: %v", err)
	}
}

// -config and -auto are opposite entry points — the config loads the blob, or the
// blob loads the config — and are never merged.
func TestServeConfigAndAutoExclusive(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "server.json")
	if err := os.WriteFile(cfgFile, []byte(`{"blobs":[{"base":"/"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := cmdServe([]string{"-config", cfgFile, "-auto", filepath.Join(dir, "x.blob")})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("-config with -auto: %v", err)
	}
	// An empty "file" means "this same blob", so a config file cannot use it.
	err = cmdServe([]string{"-config", cfgFile})
	if err == nil || !strings.Contains(err.Error(), `"file" is empty`) {
		t.Errorf(`-config with an empty "file": %v`, err)
	}
	if err := cmdServe([]string{"-auto", ""}); err == nil || !strings.Contains(err.Error(), "-auto") {
		t.Errorf("serve with neither flag: %v", err)
	}
}
