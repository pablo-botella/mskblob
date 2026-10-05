package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"errors"
	"hash"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pablo-botella/mskblob"
)

// A mount serves exact matches only, unless it declares what else to try:
// remove_extensions and default_document. Nothing undeclared exists.
func TestServeFallbacks(t *testing.T) {
	dir := t.TempDir()
	n := 0
	src := func(content string) string {
		n++
		p := filepath.Join(dir, "src"+string(rune('a'+n))+".txt")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	static := func(url string) mskblob.Item {
		return mskblob.Item{URL: url, Src: src("<" + url + ">"), RestType: mskblob.Static}
	}
	inner := filepath.Join(dir, "inner.blob")
	if _, err := mskblob.Write(inner, []mskblob.Item{static("index.html"), static("deep.html")}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(dir, "site.blob")
	if _, err := mskblob.Write(site, []mskblob.Item{
		static("index.html"),
		static("about.html"),
		static("page.htm"),
		static("logo.png"),
		static("docs.html"), // next to the folder docs/: the extension rule comes first
		static("docs/index.html"),
		static("guide/index.html"),
		static("both/index.html"),
		static("both/default.htm"),
		static("alt/default.htm"),
		static("v1.2/index.html"), // a folder with a dot in its name
		{URL: "hi/index.html", Src: src("hello {{.who}}"), RestType: mskblob.HTMLTemplate},
		{URL: "secret/index.html", Key: "/secret", Src: src("private"), RestType: mskblob.Static | mskblob.Nomux},
		{URL: "hidden.html", Key: "/hidden", Src: src("private"), RestType: mskblob.Static | mskblob.Nomux},
		{Key: "/inner", Src: inner, RestType: mskblob.Mskblob | mskblob.Nomux},
	}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}

	var cfg serveConfig
	raw := strings.ReplaceAll(`{"vars": {"who": "you"}, "blobs": [
		{"file": "SITE", "base": "/",
		 "remove_extensions": [".html", "htm"], "default_document": ["index.html", "default.htm"]},
		{"file": "SITE", "base": "/plain/"},
		{"file": "SITE", "base": "/onlydoc/", "default_document": ["index.html"]},
		{"file": "SITE", "base": "/onlyext/", "remove_extensions": [".html"]},
		{"file": "SITE", "base": "/n/", "internal_path": ["/inner"],
		 "remove_extensions": [".html"], "default_document": ["index.html"]}
	]}`, "SITE", filepath.ToSlash(site))
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	mux, closeBlobs, err := mountBlobs(cfg, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer closeBlobs()

	const notFound = "404"
	cases := []struct{ path, want string }{
		// exact matches always win, and are never redirected
		{"/about.html", "<about.html>"},
		{"/logo.png", "<logo.png>"},
		{"/docs/index.html", "<docs/index.html>"},

		// remove_extensions: no extension, or a trailing slash
		{"/about", "<about.html>"},
		{"/about/", "<about.html>"},
		{"/page", "<page.htm>"}, // second of the list, declared without its dot
		{"/logo", notFound},     // .png is not in the list
		{"/nope", notFound},
		{"/missing.png", notFound}, // it has an extension: nothing is appended
		{"/hidden", notFound},      // a nomux entry never counts

		// default_document: the root and folders, with their slash
		{"/", "<index.html>"},
		{"/guide/", "<guide/index.html>"},
		{"/both/", "<both/index.html>"}, // the first of the list wins
		{"/alt/", "<alt/default.htm>"},  // the second, when the first is not there
		{"/v1.2/", "<v1.2/index.html>"}, // the slash makes it a folder, dot or not
		{"/hi/", "hello you"},           // a template found this way is rendered
		{"/secret/", notFound},          // nomux
		{"/guide", notFound},            // no slash: not the folder. Nothing is guessed
		{"/v1.2", notFound},             //
		{"/guide/nope/", notFound},      //
		{"/docs/", "<docs.html>"},       // both rules apply: extensions are tried first
		{"/docs", "<docs.html>"},        //
		{"/docs/index", "<docs/index.html>"},

		// a mount that declares nothing: exact matches only, as ever
		{"/plain/about.html", "<about.html>"},
		{"/plain/", notFound},
		{"/plain/about", notFound},
		{"/plain/guide/", notFound},

		// each option on its own
		{"/onlydoc/", "<index.html>"},
		{"/onlydoc/guide/", "<guide/index.html>"},
		{"/onlydoc/about", notFound},
		{"/onlyext/about", "<about.html>"},
		{"/onlyext/", notFound},
		{"/onlyext/guide/", notFound},

		// a nested blob's mount has its own
		{"/n/", "<index.html>"},
		{"/n/deep", "<deep.html>"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if c.want == notFound {
			if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "private") {
				t.Errorf("GET %s = %d %q, want 404", c.path, rec.Code, rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusOK || rec.Body.String() != c.want {
			t.Errorf("GET %s = %d %q, want 200 %q", c.path, rec.Code, rec.Body.String(), c.want)
		}
	}

	// The entry found is served as itself: its own content type, its own ETag.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /about Content-Type = %q, want text/html", ct)
	}
	etag := rec.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/about", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if etag == "" || rec.Code != http.StatusNotModified {
		t.Errorf("GET /about with its ETag %q = %d, want 304", etag, rec.Code)
	}
}

// --- TLS ---------------------------------------------------------------------

// testCert makes a throwaway self-signed certificate and its PKCS#8 key.
func testCert(t *testing.T) (certPEM, keyDER []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mskblob.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"mskblob.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if keyDER, err = x509.MarshalPKCS8PrivateKey(key); err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyDER
}

func plainPEM(keyDER []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// encryptPKCS8 is the writing side of decryptPKCS8, built here from RFC 8018 so
// the tests need no key files: PBES2 with PBKDF2 and AES-CBC or triple-DES-CBC. A nil prf leaves the
// PRF out, which means HMAC-SHA1.
func encryptPKCS8(t *testing.T, keyDER []byte, password string, prf, cipherOID asn1.ObjectIdentifier, kdfOID asn1.ObjectIdentifier) []byte {
	t.Helper()
	marshal := func(v any) asn1.RawValue {
		b, err := asn1.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return asn1.RawValue{FullBytes: b}
	}
	newHash := map[string]func() hash.Hash{
		"": sha1.New, oidHMACSHA1.String(): sha1.New, oidHMACSHA256.String(): sha256.New,
		oidHMACSHA384.String(): sha512.New384, oidHMACSHA512.String(): sha512.New,
	}[prf.String()]
	keyLen := map[string]int{oidAES128CBC.String(): 16, oidAES192CBC.String(): 24, oidAES256CBC.String(): 32, oid3DESCBC.String(): 24}[cipherOID.String()]
	newBlock, size := aes.NewCipher, aes.BlockSize
	if cipherOID.Equal(oid3DESCBC) {
		newBlock, size = des.NewTripleDESCipher, des.BlockSize
	}
	salt, iv := make([]byte, 16), make([]byte, size)
	rand.Read(salt)
	rand.Read(iv)
	const iterations = 2048
	dk, err := pbkdf2.Key(newHash, password, salt, iterations, keyLen)
	if err != nil {
		t.Fatal(err)
	}
	pad := size - len(keyDER)%size
	padded := append(append([]byte{}, keyDER...), make([]byte, pad)...)
	for i := len(keyDER); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	block, err := newBlock(dk)
	if err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, padded)

	kdf := pbkdf2Params{Salt: salt, Iterations: iterations}
	if prf != nil {
		kdf.PRF = algorithmID{Algorithm: prf, Parameters: asn1.NullRawValue}
	}
	out, err := asn1.Marshal(encryptedPrivateKeyInfo{
		Algorithm: algorithmID{Algorithm: oidPBES2, Parameters: marshal(pbes2Params{
			KDF:    algorithmID{Algorithm: kdfOID, Parameters: marshal(kdf)},
			Cipher: algorithmID{Algorithm: cipherOID, Parameters: marshal(iv)},
		})},
		Data: enc,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func encryptedPEM(t *testing.T, keyDER []byte, password string) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY",
		Bytes: encryptPKCS8(t, keyDER, password, oidHMACSHA256, oidAES256CBC, oidPBKDF2)})
}

func TestDecryptPKCS8(t *testing.T) {
	_, keyDER := testCert(t)
	prfs := map[string]asn1.ObjectIdentifier{
		"default sha1": nil, "sha1": oidHMACSHA1, "sha256": oidHMACSHA256, "sha384": oidHMACSHA384, "sha512": oidHMACSHA512,
	}
	ciphers := map[string]asn1.ObjectIdentifier{"aes128": oidAES128CBC, "aes192": oidAES192CBC, "aes256": oidAES256CBC, "3des": oid3DESCBC}
	for pn, prf := range prfs {
		for cn, c := range ciphers {
			enc := encryptPKCS8(t, keyDER, "s3cret ñ", prf, c, oidPBKDF2)
			got, err := decryptPKCS8(enc, "s3cret ñ")
			if err != nil {
				t.Errorf("%s/%s: %v", pn, cn, err)
				continue
			}
			if string(got) != string(keyDER) {
				t.Errorf("%s/%s: decrypted key differs from the original", pn, cn)
			}
			for _, wrong := range []string{"", "s3cret", "S3cret ñ", "s3cret ñ "} {
				if _, err := decryptPKCS8(enc, wrong); !errors.Is(err, errWrongPassword) {
					t.Errorf("%s/%s with password %q: got %v, want errWrongPassword", pn, cn, wrong, err)
				}
			}
		}
	}

	// What is not supported is said by name, not mistaken for a wrong password.
	scrypt := encryptPKCS8(t, keyDER, "x", oidHMACSHA256, oidAES256CBC, oidScrypt)
	if _, err := decryptPKCS8(scrypt, "x"); err == nil || !strings.Contains(err.Error(), "scrypt") {
		t.Errorf("scrypt key: %v", err)
	}
	for name, der := range map[string][]byte{"garbage": []byte("not asn1"), "empty": nil, "plain key": keyDER} {
		if _, err := decryptPKCS8(der, "x"); err == nil || errors.Is(err, errWrongPassword) {
			t.Errorf("%s: got %v, want a format error", name, err)
		}
	}
}

// The format has to be the one real tools write. Skipped where there is no openssl.
func TestDecryptPKCS8FromOpenSSL(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not found")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(openssl, args...).CombinedOutput(); err != nil {
			t.Skipf("openssl %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	plain := filepath.Join(dir, "plain.pem")
	run("genpkey", "-algorithm", "EC", "-pkeyopt", "ec_paramgen_curve:P-256", "-out", plain)
	want, _ := pem.Decode(mustRead(t, plain))

	// The password goes in a file, as it would in production: on a command line a
	// non-ASCII one reaches openssl in whatever code page the system uses.
	pw := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pw, []byte("abre sésamo"), 0o600); err != nil {
		t.Fatal(err)
	}

	// openssl's defaults, and an RSA key encrypted at generation.
	passFlag := map[string]string{"genpkey aes256": "-pass"}
	variants := map[string][]string{
		"pkcs8 default":  {"pkcs8", "-topk8", "-in", plain},
		"pkcs8 aes128":   {"pkcs8", "-topk8", "-v2", "aes-128-cbc", "-in", plain},
		"pkcs8 sha512":   {"pkcs8", "-topk8", "-v2", "aes-256-cbc", "-v2prf", "hmacWithSHA512", "-iter", "5000", "-in", plain},
		"genpkey aes256": {"genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-aes-256-cbc"},
		// The usual way to make a certificate: its key comes out in triple-DES.
		"req default": {"req", "-x509", "-newkey", "rsa:2048", "-subj", "/CN=mskblob.test", "-days", "1", "-keyout"},
	}
	for name, args := range variants {
		out := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".pem")
		flag := passFlag[name]
		if flag == "" {
			flag = "-passout"
		}
		if name == "req default" { // its -out is the certificate; the key goes to -keyout
			run(append(args, out, "-out", filepath.Join(dir, "req-cert.pem"), flag, "file:"+pw)...)
		} else {
			run(append(args, "-out", out, flag, "file:"+pw)...)
		}
		block, _ := pem.Decode(mustRead(t, out))
		if block == nil || block.Type != "ENCRYPTED PRIVATE KEY" {
			t.Errorf("%s: openssl did not write an encrypted PKCS#8 key", name)
			continue
		}
		got, err := decryptPKCS8(block.Bytes, "abre sésamo")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if _, err := x509.ParsePKCS8PrivateKey(got); err != nil {
			t.Errorf("%s: decrypted, but not a key: %v", name, err)
		}
		if strings.HasPrefix(name, "pkcs8") && string(got) != string(want.Bytes) {
			t.Errorf("%s: decrypted key differs from the one encrypted", name)
		}
		if _, err := decryptPKCS8(block.Bytes, "abre sesamo"); !errors.Is(err, errWrongPassword) {
			t.Errorf("%s with a wrong password: %v", name, err)
		}
	}

	// The two formats left out are recognised and named.
	scrypt := filepath.Join(dir, "scrypt.pem")
	run("pkcs8", "-topk8", "-scrypt", "-in", plain, "-out", scrypt, "-passout", "pass:x")
	if _, err := plainKeyPEM(mustRead(t, scrypt), &tlsConfig{PasswordEnv: "PATH"}); err == nil || !strings.Contains(err.Error(), "scrypt") {
		t.Errorf("scrypt key: %v", err)
	}
	legacy := filepath.Join(dir, "legacy.pem")
	run("ec", "-in", plain, "-aes256", "-out", legacy, "-passout", "pass:x")
	if _, err := plainKeyPEM(mustRead(t, legacy), &tlsConfig{PasswordEnv: "PATH"}); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Errorf("legacy encrypted key: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// With -config, cert and key are files on disk; the key may be encrypted, its
// password found in a file or in the environment — never in the configuration.
func TestLoadTLSFromDisk(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyDER := testCert(t)
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cert := write("cert.pem", certPEM)
	plain := write("key.pem", plainPEM(keyDER))
	enc := write("key.enc.pem", encryptedPEM(t, keyDER, "la buena"))
	good := write("pw.txt", []byte("la buena\r\n")) // an editor's trailing newline is not part of it
	bad := write("bad.txt", []byte("la mala\n"))
	gone := filepath.Join(dir, "gone.txt")
	t.Setenv("MSKBLOB_TEST_PW", "la buena")
	t.Setenv("MSKBLOB_TEST_BAD", "la mala")

	if c, err := loadTLS(nil, nil); c != nil || err != nil {
		t.Errorf("no tls section: %v, %v; want plain HTTP", c, err)
	}
	if c, err := loadTLS(&tlsConfig{PasswordEnv: "MSKBLOB_TEST_PW"}, nil); c != nil || err != nil {
		t.Errorf("tls without cert: %v, %v; want plain HTTP", c, err)
	}

	ok := map[string]tlsConfig{
		"plain key":                 {Cert: cert, Key: plain},
		"plain key ignores sources": {Cert: cert, Key: plain, PasswordFile: gone, PasswordEnv: "MSKBLOB_TEST_UNSET"},
		"password file":             {Cert: cert, Key: enc, PasswordFile: good},
		"password env":              {Cert: cert, Key: enc, PasswordEnv: "MSKBLOB_TEST_PW"},
		"the file wins":             {Cert: cert, Key: enc, PasswordFile: good, PasswordEnv: "MSKBLOB_TEST_BAD"},
		"file absent, env instead":  {Cert: cert, Key: enc, PasswordFile: gone, PasswordEnv: "MSKBLOB_TEST_PW"},
	}
	for name, cfg := range ok {
		c, err := loadTLS(&cfg, nil)
		if err != nil || c == nil || len(c.Certificate) != 1 {
			t.Errorf("%s: %v, %v", name, c, err)
		}
	}
	fail := map[string]struct {
		cfg  tlsConfig
		want string
	}{
		"no key":             {tlsConfig{Cert: cert}, `"key" is empty`},
		"no password source": {tlsConfig{Cert: cert, Key: enc}, "names no password source"},
		"wrong file":         {tlsConfig{Cert: cert, Key: enc, PasswordFile: bad, PasswordEnv: "MSKBLOB_TEST_PW"}, "wrong password"},
		"wrong env":          {tlsConfig{Cert: cert, Key: enc, PasswordEnv: "MSKBLOB_TEST_BAD"}, "wrong password"},
		"env not set":        {tlsConfig{Cert: cert, Key: enc, PasswordEnv: "MSKBLOB_TEST_UNSET"}, "is not set"},
		"file gone, no env":  {tlsConfig{Cert: cert, Key: enc, PasswordFile: gone}, "cannot be read"},
		"missing cert":       {tlsConfig{Cert: gone, Key: plain}, "certificate"},
		"missing key":        {tlsConfig{Cert: cert, Key: gone}, "private key"},
		"key is not a key":   {tlsConfig{Cert: cert, Key: cert}, "tls:"},
	}
	for name, c := range fail {
		got, err := loadTLS(&c.cfg, nil)
		if err == nil || got != nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, c.want)
		}
		// Whatever goes wrong, the password is not what gets printed.
		if err != nil && (strings.Contains(err.Error(), "la buena") || strings.Contains(err.Error(), "la mala")) {
			t.Errorf("%s: the error leaks the password: %v", name, err)
		}
	}
}

// With -auto the blob is the single file to deploy, certificate included: cert and
// key are entries of the blob itself, under /mskblob/ and flagged "auto,nomux".
// Without those attributes they cannot be used.
func TestLoadTLSFromBlob(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyDER := testCert(t)
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	const auto = mskblob.MskBlobAuto | mskblob.Nomux
	cert, plain := write("cert.pem", certPEM), write("key.pem", plainPEM(keyDER))
	enc := write("key.enc.pem", encryptedPEM(t, keyDER, "la buena"))
	pw := write("pw.txt", []byte("la buena"))
	cfgJSON := write("site.json", []byte(`{"blobs":[{"base":"/"}],
		"tls":{"cert":"/mskblob/auto/cert.pem","key":"/mskblob/auto/key.pem","password_file":"`+filepath.ToSlash(pw)+`"}}`))
	site := filepath.Join(dir, "site.blob")
	if _, err := mskblob.Write(site, []mskblob.Item{
		{URL: "index.html", Src: write("index.html", []byte("home")), RestType: mskblob.Static},
		{Key: autoConfigKey, Src: cfgJSON, RestType: auto},
		{Key: "/mskblob/auto/cert.pem", Src: cert, RestType: auto},
		{Key: "/mskblob/auto/key.pem", Src: enc, RestType: auto},
		{Key: "/mskblob/auto/plain.pem", Src: plain, RestType: auto},
		// The same bytes where they do not belong, or without the flags.
		{Key: "/certs/key.pem", Src: plain, RestType: auto},
		{Key: "/mskblob/auto/unflagged.pem", Src: plain, RestType: mskblob.Static | mskblob.Nomux},
		{Key: "/mskblob/auto/served.pem", URL: "served.pem", Src: plain, RestType: mskblob.Static},
	}, mskblob.Options{}); err != nil {
		t.Fatal(err)
	}
	b, err := mskblob.Open(site)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// The configuration the blob carries, exactly as serve -auto reads it.
	cfg, err := loadAutoConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadTLS(cfg.TLS, b)
	if err != nil || c == nil {
		t.Fatalf("certificate and encrypted key from the blob: %v, %v", c, err)
	}
	if c, err := loadTLS(&tlsConfig{Cert: "/mskblob/auto/cert.pem", Key: "/mskblob/auto/plain.pem"}, b); err != nil || c == nil {
		t.Errorf("an unencrypted key from the blob: %v, %v", c, err)
	}

	fail := map[string]struct{ cert, key, want string }{
		"outside /mskblob/":   {"/mskblob/auto/cert.pem", "/certs/key.pem", "is outside"},
		"not flagged":         {"/mskblob/auto/cert.pem", "/mskblob/auto/unflagged.pem", `must be flagged "nomux,auto"`},
		"served entry":        {"/mskblob/auto/cert.pem", "/mskblob/auto/served.pem", `must be flagged "nomux,auto"`},
		"absent":              {"/mskblob/auto/cert.pem", "/mskblob/auto/nope.pem", "no entry with key"},
		"a path on disk":      {filepath.ToSlash(cert), filepath.ToSlash(plain), "is outside"},
		"cert outside":        {"/certs/key.pem", "/mskblob/auto/plain.pem", "is outside"},
		"cert and key differ": {"/mskblob/auto/cert.pem", "/mskblob/auto/cert.pem", "tls:"},
	}
	for name, f := range fail {
		got, err := loadTLS(&tlsConfig{Cert: f.cert, Key: f.key}, b)
		if err == nil || got != nil || !strings.Contains(err.Error(), f.want) {
			t.Errorf("%s: got %v, want an error containing %q", name, err, f.want)
		}
	}

	// And the key never leaves over HTTP, whatever is asked for.
	mux, closeBlobs, err := mountBlobs(cfg, b, site)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBlobs()
	for _, path := range []string{"/mskblob/auto/key.pem", "/mskblob/auto/cert.pem", "/mskblob/auto/plain.pem", "/mskblob/auto/", "/key.pem"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "PRIVATE KEY") {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}
