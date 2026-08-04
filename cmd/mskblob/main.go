// Command mskblob is the CLI for mskblob .blob pack files: read, create, inspect,
// dump and serve them.
//
// A .blob bundles many assets (their bytes plus a self-describing index) into one
// sidecar kept outside the Go binary. This command is the build-time and inspection
// side of the format. See https://pkg.go.dev/github.com/pablo-botella/mskblob for the
// library API used at runtime.
//
// # Usage
//
//	mskblob <command> [flags]
//
// # Commands
//
//   - info                   Print the header (id, entries, dataCRC, size, nocase).
//   - list                   List the header + entries as a markdown table or JSON.
//   - manifest               Scan a directory into a reviewable JSON manifest.
//   - create                 Build a blob from a manifest.
//   - dump                   Round-trip a blob back to files + manifest.json.
//   - serve                  Serve one or more blobs over HTTP from a JSON config.
//   - generate-claude-skill  Generate the Claude Code SKILL.md from the ai/ sources.
//   - generate-agent-docs    Generate the agent-agnostic AGENTS.md (Cursor, Aider, etc.).
//
// # Flags
//
// Every option is a named flag, so argument order never matters; a command run with
// no flags prints its own help.
//
//	info      -blob <file> [-short]
//	list      -blob <file> [-md | -json] [-o f]
//	manifest  -dir <dir> [-o f] [-base d] [-include g] [-exclude g] [-recurse] [-nocase]
//	create    -manifest <f> -out <blob> [-dir srcdir] [-id guid] [-base d] [-nocase] [-strict] [-skip-unchanged]
//	dump      -blob <file> -baseout <dir> [-manifest f]   (full extraction)
//	dump      -blob <file> -file <key> (-baseout d | -out f | -stdout)   (one entry)
//	serve     -config <file>
//	generate-claude-skill [-dst f] [-global] [-force]
//	generate-agent-docs   [-dst f] [-force]
//
// # Notes
//
// One manifest shape is used throughout (an id plus items). On create the computed
// fields (size, crc32, offset) are ignored and recomputed; a manifest read from a
// blob carries the real values. list prints a markdown document (header section +
// items table) by default, or JSON with -json; output goes to stdout unless -o
// names a file.
//
// A blob is written whole; it is not updatable in place. To change one: dump it,
// edit the files / manifest.json, and create a new blob. The guid is automatic
// unless you pass -id; with a pinned id, create -skip-unchanged makes the build a
// no-op when the output already carries that id.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/pablo-botella/mskblob"
)

// started is captured at program start, so create/dump can report the total
// wall-clock: from launch to the moment the file is written and closed.
var started = time.Now()

// buildVersion answers the module version the binary was compiled at — a
// `go install ...@vX.Y.Z` or a tag-stamped build reports that tag and can
// never drift from it. A local `go build` has no tag and says "dev".
func buildVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Version == "" || bi.Main.Version == "(devel)" {
		return "dev"
	}
	return bi.Main.Version
}

func main() {
	// mkskill owns the generate commands: CheckParams runs one if mskblob was
	// invoked as such, else falls through to mskblob's own commands. The spec
	// comes composed in the generated embed — no mkskill import needed here.
	if err, done := MkskillSpec.CheckParams(); done {
		return
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "info":
		err = cmdInfo(os.Args[2:])
	case "list":
		err = cmdListing(os.Args[2:])
	case "manifest":
		err = cmdManifest(os.Args[2:])
	case "create":
		err = cmdCreate(os.Args[2:])
	case "dump":
		err = cmdDump(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "version":
		// two truths, both printed when both exist: the embed says which
		// release this claims to be (from the config's version-spec); the
		// build info says what was actually compiled.
		if MkskillSpec.Version != "" {
			fmt.Println("mskblob " + MkskillSpec.Version + " (" + buildVersion() + ")")
		} else {
			fmt.Println("mskblob " + buildVersion())
		}
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\nrun `mskblob` with no arguments to see the list of commands.\n", os.Args[1])
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func printJSON(v any) error {
	mj, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(mj))
	return nil
}

func writeJSONFile(path string, v any) error {
	mj, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(mj, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	return nil
}

func writeTextFile(path, s string) error {
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	return nil
}

func sortedItems(b *mskblob.Blob) []mskblob.Item {
	items := append([]mskblob.Item(nil), b.Items()...)
	sort.Slice(items, func(i, j int) bool { return items[i].URL < items[j].URL })
	return items
}

// renderTable prints a padded GitHub-style markdown table. right[i] right-aligns
// column i.
func renderTable(headers []string, rows [][]string, right []bool) string {
	w := make([]int, len(headers))
	for i, h := range headers {
		w[i] = len(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if len(c) > w[i] {
				w[i] = len(c)
			}
		}
	}
	pad := func(s string, i int) string {
		gap := strings.Repeat(" ", w[i]-len(s))
		if i < len(right) && right[i] {
			return gap + s
		}
		return s + gap
	}
	var sb strings.Builder
	row := func(cells []string) {
		sb.WriteString("|")
		for i := range headers {
			c := ""
			if i < len(cells) {
				c = cells[i]
			}
			sb.WriteString(" " + pad(c, i) + " |")
		}
		sb.WriteString("\n")
	}
	row(headers)
	sb.WriteString("|")
	for i := range headers {
		if i < len(right) && right[i] {
			sb.WriteString(strings.Repeat("-", w[i]+1) + ":|")
		} else {
			sb.WriteString(strings.Repeat("-", w[i]+2) + "|")
		}
	}
	sb.WriteString("\n")
	for _, r := range rows {
		row(r)
	}
	return sb.String()
}

func itemRows(items []mskblob.Item) ([]string, [][]string, []bool) {
	headers := []string{"URL", "SIZE", "CRC32", "TYPE", "FILENAME"}
	right := []bool{false, true, false, false, false}
	rows := make([][]string, len(items))
	for i, e := range items {
		rows[i] = []string{
			e.URL,
			fmt.Sprintf("%d", e.Size),
			fmt.Sprintf("0x%08X", e.CRC32),
			e.RestType.Names(),
			e.Filename,
		}
	}
	return headers, rows, right
}

// renderMarkdown is the default view: a header section plus the items table.
func renderMarkdown(h mskblob.Header, items []mskblob.Item) string {
	var sb strings.Builder
	sb.WriteString("## blob\n\n")
	fmt.Fprintf(&sb, "- id: `%s`\n", h.ID)
	fmt.Fprintf(&sb, "- version: %d\n", h.Version)
	fmt.Fprintf(&sb, "- entries: %d\n", h.Count)
	fmt.Fprintf(&sb, "- nocase: %v\n", h.NoCase)
	fmt.Fprintf(&sb, "- dataCRC: %#08x\n", h.DataCRC32)
	fmt.Fprintf(&sb, "- data: %d bytes\n\n", h.DataSize)
	sb.WriteString("## items\n\n")
	sb.WriteString(renderTable(itemRows(items)))
	return sb.String()
}

func cmdInfo(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: mskblob info -blob <file.blob> [-short]\n\n" +
			"Prints the blob header: id, version, entries, nocase, data CRC and data size.\n" +
			"-short prints a one-line summary.")
		return nil
	}
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	blob := fs.String("blob", "", "the .blob file to read")
	short := fs.Bool("short", false, "one-line summary: id, entries, data size")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *blob == "" {
		return missingFlag("info", "-blob <file.blob>")
	}
	h, err := mskblob.ReadHeader(*blob)
	if err != nil {
		return fmt.Errorf("info: cannot read blob %q: %w", *blob, err)
	}
	if *short {
		fmt.Printf("%s  %d entries  %d bytes  nocase=%v\n", h.ID, h.Count, h.DataSize, h.NoCase)
		return nil
	}
	fmt.Printf("blob:    %s\n", *blob)
	fmt.Printf("version: %d\n", h.Version)
	fmt.Printf("id:      %s\n", h.ID)
	fmt.Printf("entries: %d\n", h.Count)
	fmt.Printf("nocase:  %v\n", h.NoCase)
	fmt.Printf("dataCRC: %#08x\n", h.DataCRC32)
	fmt.Printf("data:    %d bytes\n", h.DataSize)
	return nil
}

// cmdListing implements `list`: the header section plus the items, as a markdown
// document (default), JSON, or written to a file.
func cmdListing(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: mskblob list -blob <file.blob> [-md | -json] [-o file]\n\n" +
			"Prints the blob header plus its entries. Format is markdown (default) or JSON\n" +
			"with -json; -o writes to that file instead of stdout (as `manifest -o` does).")
		return nil
	}
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	blob := fs.String("blob", "", "the .blob file to read")
	out := fs.String("o", "", "write output to this file instead of stdout")
	asJSON := fs.Bool("json", false, "emit JSON instead of the markdown table")
	asMD := fs.Bool("md", false, "emit the markdown table (the default; explicit for clarity)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *blob == "" {
		return missingFlag("list", "-blob <file.blob>")
	}
	if *asJSON && *asMD {
		return fmt.Errorf("list: -md and -json are mutually exclusive")
	}
	b, err := mskblob.Open(*blob)
	if err != nil {
		return fmt.Errorf("list: cannot open blob %q: %w", *blob, err)
	}
	defer b.Close()
	m := b.Manifest()

	if *asJSON {
		if *out != "" {
			return writeJSONFile(*out, m)
		}
		return printJSON(m)
	}
	md := renderMarkdown(b.Header(), m.Items)
	if *out != "" {
		return writeTextFile(*out, md)
	}
	fmt.Print(md)
	return nil
}

// cmdManifest scans a directory and emits a JSON manifest with the declarative
// flags spelled out (url, src, key, filename, restype = static), so it can be
// reviewed and tweaked by hand before `create`. src is written relative to
// -base (default: the scanned dir). The scan itself is Manifest.AddFolder.
func cmdManifest(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: mskblob manifest -dir <dir> [-o file.json] [-base dir] [-include globs] [-exclude globs] [-recurse] [-nocase]\n\n" +
			"Scans -dir into a JSON manifest (header + items) you can edit before `create`.\n" +
			"Not recursive unless -recurse; -include/-exclude are comma-separated globs on the\n" +
			"file name; -base writes src paths relative to that dir (default: the scanned dir).")
		return nil
	}
	fset := flag.NewFlagSet("manifest", flag.ContinueOnError)
	dir := fset.String("dir", "", "directory to scan")
	outFile := fset.String("o", "", "write the manifest JSON to this file (default: stdout)")
	base := fset.String("base", "", "write src paths relative to this directory (default: the scanned dir)")
	include := fset.String("include", "*", "comma-separated globs to include (matched on the file name)")
	exclude := fset.String("exclude", "", "comma-separated globs to exclude (matched on the file name)")
	recurse := fset.Bool("recurse", false, "descend into subdirectories")
	nocase := fset.Bool("nocase", false, "case-insensitive include/exclude matching; also marks the blob case-insensitive")
	if err := parseFlags(fset, args); err != nil {
		return err
	}
	if *dir == "" {
		return missingFlag("manifest", "-dir <dir>")
	}
	// The header is always present: id is left empty (create assigns the guid, or
	// you pin it by hand), count and nocase are filled. version/dataCRC are unknown
	// until the blob is built.
	m := mskblob.NewManifest("")
	m.NoCase = *nocase
	n, err := m.AddFolder(*dir, mskblob.FolderOptions{
		Recurse: *recurse, Include: *include, Exclude: *exclude, Base: *base, NoCase: *nocase,
	})
	if err != nil {
		return fmt.Errorf("manifest: scanning %q: %w", *dir, err)
	}
	if n == 0 {
		return fmt.Errorf("manifest: no files under %q matched (include=%q, exclude=%q, recurse=%v, nocase=%v)", *dir, *include, *exclude, *recurse, *nocase)
	}
	m.Count = uint32(n)
	if *nocase {
		if dups := foldDuplicates(m.Items); len(dups) > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d case-insensitive URL collision(s) — a -nocase blob cannot hold both; fix before `create`:\n", len(dups))
			for _, g := range dups {
				fmt.Fprintf(os.Stderr, "  %s\n", strings.Join(g, "  ==  "))
			}
		}
	}
	// To stderr, so it never corrupts JSON written to stdout.
	fmt.Fprintf(os.Stderr, "scanned %d files in %s\n", n, took(started))
	if *outFile != "" {
		return writeJSONFile(*outFile, m)
	}
	return printJSON(m)
}

func cmdCreate(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: mskblob create -manifest <file> -out <out.blob> [-dir srcdir] [-id guid] [-base dir] [-nocase] [-skip-unchanged]\n\n" +
			"Builds a blob from a manifest: a JSON {id?, nocase?, items:[{url, src, [key], [filename], [restype]}]},\n" +
			"a bare [..] items array, or a text list of \"<url>\\t<src>\" lines. Relative src paths resolve\n" +
			"against -base (default: the manifest's dir). -dir supplies the source files for items that have\n" +
			"no src (e.g. a manifest from `list -json`): each src becomes <dir>/<url>. The guid is the\n" +
			"manifest's id, else -id, else fresh. With -skip-unchanged the build is a no-op (no source file\n" +
			"is read) when the output already exists with the pinned id.")
		return nil
	}
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	manifest := fs.String("manifest", "", "manifest to build from (JSON or text list)")
	outBlob := fs.String("out", "", "output .blob file to write")
	dir := fs.String("dir", "", "source dir: fill each item's src as <dir>/<url> when the manifest has none")
	id := fs.String("id", "", "blob guid to stamp (default: the manifest's id, else a fresh GUID)")
	base := fs.String("base", "", "resolve relative src paths against this dir (default: the manifest's dir)")
	nocase := fs.Bool("nocase", false, "force the blob case-insensitive (overrides the manifest's nocase)")
	strict := fs.Bool("strict", false, "fail (instead of warn) if the rebuilt data CRC differs from the manifest's dataCRC32")
	skipUnchanged := fs.Bool("skip-unchanged", false, "skip the whole build (don't read or pack any source file) when the output blob already exists with the pinned id; requires a pinned id (-id or the manifest's id)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *manifest == "" {
		return missingFlag("create", "-manifest <file>")
	}
	if *outBlob == "" {
		return missingFlag("create", "-out <out.blob>")
	}
	m, err := readManifest(*manifest, *base, *dir)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if len(m.Items) == 0 {
		return fmt.Errorf("create: manifest %q has no items to pack", *manifest)
	}
	wantID := *id
	if wantID == "" {
		wantID = m.ID
	}
	// -skip-unchanged delegates to mskblob.Write (Options.SkipUnchanged): with a pinned
	// id, an output blob already carrying that id holds the same bytes by contract, so the
	// pack is skipped and no source file is read. Without a pinned id there is no stable id
	// to compare, so warn and let the normal build run. The pre-read here only decides the
	// "skipped" vs "created" message; Write performs the actual skip.
	willSkip := false
	if *skipUnchanged {
		if wantID == "" {
			fmt.Fprintln(os.Stderr, "warning: -skip-unchanged needs a pinned id (-id or the manifest's id); building normally")
		} else if h, herr := mskblob.ReadHeader(*outBlob); herr == nil && h.ID == wantID {
			willSkip = true
		}
	}
	expectedCRC := m.DataCRC32 // from the manifest, if it carried one (e.g. list -json)
	gotID, err := mskblob.Write(*outBlob, m.Items, mskblob.Options{ID: wantID, NoCase: m.NoCase || *nocase, SkipUnchanged: *skipUnchanged})
	if err != nil {
		return fmt.Errorf("create: writing blob %q: %w", *outBlob, err)
	}
	if willSkip {
		fmt.Printf("skipped %s: already at id %s — %s\n", *outBlob, gotID, took(started))
		return nil
	}
	// When an id is pinned the blob claims a stable identity, so the content should
	// be verifiable. A recorded data CRC that the rebuild doesn't match — or no CRC
	// at all to check against — is a warning (or an error with -strict). With an
	// auto-generated id every build is a new identity, so there is nothing to check.
	if wantID != "" {
		var msg string
		if expectedCRC == 0 {
			msg = fmt.Sprintf("id %q is pinned but the manifest has no dataCRC32 — the content can't be verified to match this id", gotID)
		} else if h, herr := mskblob.ReadHeader(*outBlob); herr == nil && h.DataCRC32 != expectedCRC {
			msg = fmt.Sprintf("data CRC changed: manifest recorded 0x%08X, rebuilt blob is 0x%08X — same id %q but the content differs", expectedCRC, h.DataCRC32, gotID)
		}
		if msg != "" {
			if *strict {
				return fmt.Errorf("create: %s (blob written to %s)", msg, *outBlob)
			}
			fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
		}
	}
	fmt.Printf("created %s with %d items (id %s, nocase %v) in %s\n", *outBlob, len(m.Items), gotID, m.NoCase || *nocase, took(started))
	return nil
}

// took formats elapsed time since t, rounded for readability.
func took(t time.Time) time.Duration {
	d := time.Since(t)
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(10 * time.Millisecond)
}

// readManifest loads a manifest: JSON ({id, items} or a bare array) or a text
// list. Relative src paths are resolved against baseOverride, or the manifest's
// own directory. When an item has no src and dirOverride is set, its src is filled
// in as <dirOverride>/<url> — letting a blob-derived manifest (e.g. from `list
// -json`, which carries no src) be rebuilt by pointing at the source files.
func readManifest(path, baseOverride, dirOverride string) (mskblob.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return mskblob.Manifest{}, fmt.Errorf("cannot read manifest %q: %w", path, err)
	}
	var m mskblob.Manifest
	if strings.EqualFold(filepath.Ext(path), ".json") {
		if t := bytes.TrimLeft(data, " \t\r\n"); len(t) > 0 && t[0] == '[' {
			if err := json.Unmarshal(data, &m.Items); err != nil {
				return mskblob.Manifest{}, fmt.Errorf("parsing JSON manifest: %w", err)
			}
		} else if err := json.Unmarshal(data, &m); err != nil {
			return mskblob.Manifest{}, fmt.Errorf("parsing JSON manifest: %w", err)
		}
	} else {
		m.Items = parseListManifest(data)
	}

	base := baseOverride
	if base == "" {
		base = filepath.Dir(path)
	}
	for i := range m.Items {
		if m.Items[i].URL == "" && m.Items[i].Key == "" {
			return mskblob.Manifest{}, fmt.Errorf("manifest item %d needs a \"url\" and/or \"key\"", i)
		}
		// Fill src from -dir when the manifest doesn't carry one (e.g. list -json).
		if m.Items[i].Src == "" && dirOverride != "" {
			name := m.Items[i].URL
			if name == "" {
				name = m.Items[i].Filename
			}
			m.Items[i].Src = filepath.Join(dirOverride, filepath.FromSlash(name))
		}
		if m.Items[i].Src == "" {
			return mskblob.Manifest{}, fmt.Errorf("manifest item %d (url %q) has no \"src\" — the file to read.\n"+
				"This looks like a manifest from `mskblob list -json` of an existing blob, where the\n"+
				"bytes live inside the blob, so there is no src to build from. Either:\n"+
				"  - point at the source files: `mskblob create -manifest <this> -dir <srcdir> -out <blob>`\n"+
				"    (each src becomes <srcdir>/<url>), or\n"+
				"  - rebuild from a scan manifest: `mskblob manifest -dir <dir>`, or\n"+
				"  - round-trip the blob: `mskblob dump -blob <blob> -baseout <dir>` then create from there.", i, m.Items[i].URL)
		}
		if m.Items[i].Filename == "" {
			m.Items[i].Filename = m.Items[i].URL
		}
		if m.Items[i].RestType == 0 {
			m.Items[i].RestType = mskblob.Static
		}
		if !filepath.IsAbs(m.Items[i].Src) {
			m.Items[i].Src = filepath.Join(base, filepath.FromSlash(m.Items[i].Src))
		}
	}
	return m, nil
}

// parseListManifest reads a plain list: one "<url>\t<src>" per line, or just
// "<src>" (then url = src). Blank lines and #-comments are ignored.
func parseListManifest(data []byte) []mskblob.Item {
	var items []mskblob.Item
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		url, src := line, line
		if u, s, ok := strings.Cut(line, "\t"); ok {
			url, src = strings.TrimSpace(u), strings.TrimSpace(s)
		}
		url = filepath.ToSlash(url)
		items = append(items, mskblob.Item{
			Key: "/" + url, URL: url, Filename: url, RestType: mskblob.Static, Src: src,
		})
	}
	return items
}

// dumpRel is the on-disk subpath for an entry: its url (or key when url-less), as a
// clean relative path. Mirrors how full extraction lays files out under -baseout.
func dumpRel(it *mskblob.Item) string {
	s := it.URL
	if s == "" {
		s = it.Key
	}
	return filepath.FromSlash(strings.TrimPrefix(s, "/"))
}

// isDirLike reports whether p should be treated as a directory destination: it ends
// in a path separator, or it already exists as a directory.
func isDirLike(p string) bool {
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, string(os.PathSeparator)) {
		return true
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func cmdDump(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: mskblob dump -blob <file.blob> -baseout <dir> [-manifest file]\n" +
			"       mskblob dump -blob <file.blob> -file <key> (-baseout <dir> | -out <file> | -stdout)\n\n" +
			"Full mode extracts every entry under -baseout (and, with -manifest <file>, the\n" +
			"round-trippable manifest). Single mode (-file <key>) extracts just the entry with\n" +
			"that key, to exactly one destination: -baseout <dir> (kept at its subpath),\n" +
			"-out <file> (a path, or a dir if it ends in a separator), or -stdout.")
		return nil
	}
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	blob := fs.String("blob", "", "the .blob file to extract")
	dst := fs.String("baseout", "", "full mode: base directory the entries are extracted into")
	manifestFile := fs.String("manifest", "", "full mode: also write the round-trippable manifest JSON to this file")
	file := fs.String("file", "", "single-entry mode: extract only the entry with this key")
	outFile := fs.String("out", "", "single-entry mode: write that entry's bytes to this file")
	toStdout := fs.Bool("stdout", false, "single-entry mode: write that entry's bytes to stdout")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *blob == "" {
		return missingFlag("dump", "-blob <file.blob>")
	}
	b, err := mskblob.Open(*blob)
	if err != nil {
		return fmt.Errorf("dump: cannot open blob %q: %w", *blob, err)
	}
	defer b.Close()

	// Single-entry mode: -file <key> extracts just that one entry. Its destination is
	// exactly one of -baseout (under the dir, keeping the entry's subpath), -out (a
	// file path, or a dir if it ends in a separator / is one), or -stdout.
	if *file != "" {
		if *manifestFile != "" {
			return fmt.Errorf("dump: -manifest is for full extraction, not with -file")
		}
		n := 0
		for _, set := range []bool{*dst != "", *outFile != "", *toStdout} {
			if set {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("dump: with -file choose one destination: -baseout <dir>, -out <file>, or -stdout")
		}
		it := b.GetByKey(*file)
		if it == nil {
			return fmt.Errorf("dump: no entry with key %q in %q", *file, *blob)
		}
		data, err := b.Bytes(it)
		if err != nil {
			return fmt.Errorf("dump: reading entry %q: %w", *file, err)
		}
		if *toStdout {
			_, err := os.Stdout.Write(data)
			return err
		}
		rel := dumpRel(it)
		var p string
		switch {
		case *dst != "": // under the base dir, preserving the entry's subpath
			if rel == "" || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return fmt.Errorf("dump: refusing unsafe entry path for %q (would escape %q)", *file, *dst)
			}
			p = filepath.Join(*dst, rel)
		default: // -out: a file path, or a directory if it ends in a separator / already is one
			p = *outFile
			if isDirLike(*outFile) {
				p = filepath.Join(*outFile, filepath.Base(rel))
			}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "extracted %q -> %s in %s\n", *file, p, took(started))
		return nil
	}

	// Full mode: extract every entry under -baseout.
	if *outFile != "" || *toStdout {
		return fmt.Errorf("dump: -out/-stdout apply only with -file (single-entry mode)")
	}
	if *dst == "" {
		return missingFlag("dump", "-baseout <dir>")
	}
	if err := os.MkdirAll(*dst, 0o755); err != nil {
		return fmt.Errorf("dump: cannot create output dir %q: %w", *dst, err)
	}

	items := sortedItems(b)
	manifestItems := make([]mskblob.Item, 0, len(items))
	for _, it := range items {
		data, err := b.Bytes(&it)
		if err != nil {
			return fmt.Errorf("dump: reading entry %q: %w", it.URL, err)
		}
		rel := filepath.FromSlash(it.URL)
		if strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("dump: refusing unsafe entry path %q (would escape %q)", it.URL, *dst)
		}
		p := filepath.Join(*dst, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		// the manifest's src points at the file just written (relative to dst)
		mi := it
		mi.Src = it.URL
		manifestItems = append(manifestItems, mi)
	}

	// The manifest is written only on request (-manifest file); by default dump
	// extracts just the asset files. Its src points at the just-extracted files, so
	// `create -manifest <file>` round-trips the blob.
	if *manifestFile != "" {
		manifest := b.Manifest()
		manifest.Items = manifestItems
		if err := writeJSONFile(*manifestFile, manifest); err != nil {
			return err
		}
	}
	fmt.Printf("dumped %d entries -> %s in %s\n", len(items), *dst, took(started))
	return nil
}

// serveConfig is the JSON that drives `mskblob serve`: a web server mounting one
// or more blobs, each under its own base. Variables live at two levels — global
// and per-blob — and merge (the blob's win) for template rendering.
type serveConfig struct {
	Addr    string         `json:"addr"`
	TLS     *tlsConfig     `json:"tls"`
	Vars    map[string]any `json:"vars"`    // global template variables
	Headers []headerKV     `json:"headers"` // extra headers added to every response
	Blobs   []blobMount    `json:"blobs"`
}

type tlsConfig struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

type headerKV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type blobMount struct {
	File    string         `json:"file"`
	Base    string         `json:"base"`
	ID      string         `json:"id"`      // optional: verified on load
	Vars    map[string]any `json:"vars"`    // per-blob variables (override the global ones)
	Headers []headerKV     `json:"headers"` // per-blob extra headers (override the global ones)
}

// cmdServe runs the web server described by a JSON config: it mounts every blob
// as a sub-mux under its base, streams static entries lazily, renders template
// entries with the merged (global + per-blob) variables, and adds the configured
// extra headers to every response. Serves over TLS when tls.cert/tls.key are set.
func cmdServe(args []string) error {
	if len(args) == 0 {
		fmt.Println("usage: mskblob serve -config <config.json>\n\n" +
			"Serves one or more blobs over HTTP from a JSON config:\n" +
			`  {"addr":":8080","tls":{"cert":"","key":""},"vars":{},"headers":[{"name":"","value":""}],` + "\n" +
			`   "blobs":[{"file":"x.blob","base":"/","id":"","vars":{},"headers":[]}]}` + "\n" +
			"Static entries stream lazily; templates render with the merged (global+blob) vars.")
		return nil
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	config := fs.String("config", "", "JSON config file describing the server and blobs")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *config == "" {
		return missingFlag("serve", "-config <config.json>")
	}
	raw, err := os.ReadFile(*config)
	if err != nil {
		return fmt.Errorf("serve: cannot read config %q: %w", *config, err)
	}
	var cfg serveConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("serve: invalid JSON in config %q: %w", *config, err)
	}
	if len(cfg.Blobs) == 0 {
		return fmt.Errorf("serve: config %q lists no blobs (need at least one in \"blobs\")", *config)
	}

	mux := http.NewServeMux()
	for _, bm := range cfg.Blobs {
		base := bm.Base
		if base == "" {
			base = "/"
		}
		b, err := mskblob.Load(bm.File, bm.ID)
		if err != nil {
			return fmt.Errorf("serve: loading blob %q (mounted at %s): %w", bm.File, base, err)
		}
		// Parse the blob's template entries once, render them with merged vars.
		tmpls, err := parseBlobTemplates(b)
		if err != nil {
			return fmt.Errorf("serve: blob %q: %w", bm.File, err)
		}
		vars := mergeVars(cfg.Vars, bm.Vars)
		mw := func(w http.ResponseWriter, r *http.Request, it *mskblob.Item) int {
			if it == nil {
				return mskblob.DispatchAuto // unknown → 404
			}
			if t := tmpls[it.URL]; t != nil {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if err := t.Execute(w, vars); err != nil {
					return http.StatusInternalServerError
				}
				return mskblob.DispatchDone
			}
			return mskblob.DispatchAuto // static → streamed by the blob
		}
		mux.Handle(base, withHeaders(b.Handler(base, mw), bm.Headers))
		fmt.Printf("mounted %s (%d entries, %d templates) at %s\n", bm.File, b.Header().Count, len(tmpls), base)
	}

	addr := cfg.Addr
	if addr == "" {
		addr = ":8080"
	}
	handler := withHeaders(mux, cfg.Headers)
	if cfg.TLS != nil && cfg.TLS.Cert != "" {
		fmt.Printf("serving HTTPS on %s\n", addr)
		return http.ListenAndServeTLS(addr, cfg.TLS.Cert, cfg.TLS.Key, handler)
	}
	fmt.Printf("serving HTTP on %s\n", addr)
	return http.ListenAndServe(addr, handler)
}

// parseBlobTemplates parses every template/parse entry of the blob (keyed by its
// URL) so they can be rendered per request.
func parseBlobTemplates(b *mskblob.Blob) (map[string]*template.Template, error) {
	out := map[string]*template.Template{}
	for _, it := range b.Items() {
		if it.URL == "" || it.RestType&(mskblob.HTMLTemplate|mskblob.Parse) == 0 {
			continue
		}
		data, err := b.Bytes(&it)
		if err != nil {
			return nil, err
		}
		t, err := template.New(it.URL).Parse(string(data))
		if err != nil {
			return nil, fmt.Errorf("parsing template %q: %w", it.URL, err)
		}
		out[it.URL] = t
	}
	return out, nil
}

// mergeVars returns global overlaid with local (local wins).
func mergeVars(global, local map[string]any) map[string]any {
	out := make(map[string]any, len(global)+len(local))
	for k, v := range global {
		out[k] = v
	}
	for k, v := range local {
		out[k] = v
	}
	return out
}

// withHeaders wraps h to add the configured extra headers to every response.
func withHeaders(h http.Handler, headers []headerKV) http.Handler {
	if len(headers) == 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, kv := range headers {
			w.Header().Set(kv.Name, kv.Value)
		}
		h.ServeHTTP(w, r)
	})
}

// missingFlag explains a required named flag that wasn't given. Every option is a
// named flag, so order never matters — you can add a forgotten one at the end.
func missingFlag(cmd, want string) error {
	return fmt.Errorf("%s: required flag %s is missing (flags may be given in any order).\n"+
		"(on Windows, a trailing backslash inside quotes — -dir \"C:\\path\\\" — escapes the "+
		"closing quote and merges it with the next argument; drop the trailing backslash.)",
		cmd, want)
}

// parseFlags parses named flags in any order and rejects any leftover positional
// argument (there are none — every option is a flag). The flag package's own
// usage dump is suppressed so only our explanatory errors show.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected argument(s): %q — every option is a named flag (e.g. -blob <file>), so there are no positional arguments", fs.Name(), rest)
	}
	return nil
}

// foldDuplicates returns groups of item URLs that collide when folded to lower
// case — distinct files on a case-sensitive filesystem, but the same entry under
// nocase. Used to warn at manifest time, before a -nocase create would reject them.
func foldDuplicates(items []mskblob.Item) [][]string {
	byFold := map[string][]string{}
	for _, it := range items {
		if it.URL == "" {
			continue
		}
		k := strings.ToLower(it.URL)
		byFold[k] = append(byFold[k], it.URL)
	}
	var groups [][]string
	for _, urls := range byFold {
		if len(urls) > 1 {
			sort.Strings(urls)
			groups = append(groups, urls)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: mskblob <command> [flags]

All options are named flags — order never matters; run a command with no flags
to see its own help.

Commands:
  info     -blob <file>                  [-short]
  list     -blob <file>                  [-md | -json] [-o f]
  manifest -dir <dir>                    [-o f] [-base d] [-include g] [-exclude g] [-recurse] [-nocase]
  create   -manifest <f> -out <blob>     [-id guid] [-base d] [-nocase] [-skip-unchanged]
  dump     -blob <file> -baseout <dir>   [-manifest f]   (or: -file <key> -out f|-stdout)
  serve    -config <file>
  version                                Print the version (embed + build info)
  %s

One manifest shape ({id?, nocase?, items:[...]}) is used everywhere; on create the
computed fields (size, crc32, offset) are ignored. list prints a markdown
doc by default (-json for JSON); without -o it goes to stdout, -o f writes a file.

A blob is not updatable in place: to change one, dump it, edit, and create anew.
The guid is automatic unless you pass -id to create.
`, strings.ReplaceAll(MkskillSpec.Usage(false), "\n", "\n  "))
	os.Exit(1)
}
