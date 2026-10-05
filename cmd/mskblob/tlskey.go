package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"os"
	"strings"

	"github.com/pablo-botella/mskblob"
)

// autoPrefix is the folder of a blob reserved for what mskblob itself reads out of
// it — the self-contained configuration and, for HTTPS, the certificate and its
// key. Everything in it is flagged "auto,nomux": read by the server, never served.
const autoPrefix = "/mskblob/"

// readAutoEntry returns the bytes of one of the blob's own entries — an entry the
// server reads for itself. It must live under [autoPrefix] and carry the
// attributes that keep it from ever being served: flagged "auto", flagged "nomux"
// and with no url. Anything less is refused rather than read.
func readAutoEntry(b *mskblob.Blob, key string) ([]byte, error) {
	if !strings.HasPrefix(key, autoPrefix) {
		return nil, fmt.Errorf("entry %q is outside %q, the folder reserved for what the server reads from the blob", key, autoPrefix)
	}
	it := b.GetByKey(key)
	if it == nil {
		return nil, fmt.Errorf("no entry with key %q", key)
	}
	const need = mskblob.MskBlobAuto | mskblob.Nomux
	if it.RestType&need != need {
		return nil, fmt.Errorf("entry %q must be flagged %q (it is flagged %q)", key, need.Names(), it.RestType.Names())
	}
	if it.URL != "" {
		return nil, fmt.Errorf("entry %q must have no url (it has %q)", key, it.URL)
	}
	data, err := b.Bytes(it)
	if err != nil {
		return nil, fmt.Errorf("reading entry %q: %w", key, err)
	}
	return data, nil
}

// loadTLS builds the certificate the server presents, or returns nil when the
// configuration asks for plain HTTP (no tls.cert).
//
// Where cert and key come from depends on who loaded the configuration. Read from
// a file (-config), they are paths on disk. Carried inside a blob (-auto, self
// non-nil), they are keys of entries of that same blob — which is then the single
// file to deploy, certificate included.
//
// The private key may be encrypted (PKCS#8, see [decryptPKCS8]); its password is
// never in the configuration, only where to find it: a file, or an environment
// variable. An unencrypted key needs no password and none is looked for.
func loadTLS(cfg *tlsConfig, self *mskblob.Blob) (*tls.Certificate, error) {
	if cfg == nil || cfg.Cert == "" {
		return nil, nil
	}
	if cfg.Key == "" {
		return nil, errors.New(`tls: "cert" is set but "key" is empty`)
	}
	read := os.ReadFile
	if self != nil {
		read = func(key string) ([]byte, error) { return readAutoEntry(self, key) }
	}
	certPEM, err := read(cfg.Cert)
	if err != nil {
		return nil, fmt.Errorf("tls: certificate: %w", err)
	}
	keyPEM, err := read(cfg.Key)
	if err != nil {
		return nil, fmt.Errorf("tls: private key: %w", err)
	}
	if keyPEM, err = plainKeyPEM(keyPEM, cfg); err != nil {
		return nil, fmt.Errorf("tls: private key %q: %w", cfg.Key, err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return &cert, nil
}

// plainKeyPEM returns the private key ready for [tls.X509KeyPair]: as given when
// it is not encrypted, decrypted in memory when it is.
func plainKeyPEM(keyPEM []byte, cfg *tlsConfig) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("not PEM")
	}
	if block.Headers["DEK-Info"] != "" {
		return nil, errors.New(`it is encrypted in the legacy PEM format ("DEK-Info"), which is not supported: ` +
			`convert it with "openssl pkcs8 -topk8"`)
	}
	if block.Type != "ENCRYPTED PRIVATE KEY" {
		return keyPEM, nil
	}
	password, err := tlsPassword(cfg)
	if err != nil {
		return nil, err
	}
	der, err := decryptPKCS8(block.Bytes, password)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// tlsPassword fetches the password of an encrypted private key from where the
// configuration says it is: tls.password_file — one line, its trailing newline
// dropped — or the environment variable named by tls.password_env. The file wins;
// when it is configured but not there, the variable is the fallback.
func tlsPassword(cfg *tlsConfig) (string, error) {
	if cfg.PasswordFile == "" && cfg.PasswordEnv == "" {
		return "", errors.New(`it is encrypted and the configuration names no password source ` +
			`(set "password_file" or "password_env" under "tls")`)
	}
	if cfg.PasswordFile != "" {
		raw, err := os.ReadFile(cfg.PasswordFile)
		if err == nil {
			return strings.TrimRight(string(raw), "\r\n"), nil
		}
		if !os.IsNotExist(err) || cfg.PasswordEnv == "" {
			return "", fmt.Errorf("it is encrypted and its password file cannot be read: %w", err)
		}
	}
	password, ok := os.LookupEnv(cfg.PasswordEnv)
	if !ok {
		return "", fmt.Errorf("it is encrypted and the environment variable %s, which holds its password, is not set", cfg.PasswordEnv)
	}
	return password, nil
}

// The object identifiers of the one encryption scheme supported, PBES2 (RFC 8018):
// a key derived from the password with PBKDF2, the data encrypted with AES-CBC
// or triple-DES-CBC.
var (
	oidPBES2  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidScrypt = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11591, 4, 11}

	oidHMACSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA384 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 10}
	oidHMACSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}

	oidAES128CBC = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oid3DESCBC   = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7} // what "openssl req" still uses
)

type algorithmID struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type encryptedPrivateKeyInfo struct {
	Algorithm algorithmID
	Data      []byte
}

type pbes2Params struct {
	KDF    algorithmID
	Cipher algorithmID
}

type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int         `asn1:"optional"`
	PRF        algorithmID `asn1:"optional"`
}

// maxPBKDF2Iterations bounds the work a key file can demand before the password
// is even checked. Real keys use thousands to a few hundred thousand.
const maxPBKDF2Iterations = 10_000_000

// errWrongPassword is what a failed decryption comes down to: with CBC there is
// no telling a wrong password from damaged data.
var errWrongPassword = errors.New("wrong password (or the key is damaged)")

// decryptPKCS8 decrypts a PKCS#8 EncryptedPrivateKeyInfo — the body of a
// "BEGIN ENCRYPTED PRIVATE KEY" block — and returns the plain PKCS#8 key.
//
// It handles what OpenSSL writes by default: PBES2 with PBKDF2, and AES-CBC
// ("openssl pkcs8", "openssl genpkey") or triple-DES-CBC ("openssl req"). That is
// everything the standard library can do on its own; a key derived with scrypt,
// or encrypted with anything else, is reported as unsupported by name.
func decryptPKCS8(der []byte, password string) ([]byte, error) {
	var info encryptedPrivateKeyInfo
	if rest, err := asn1.Unmarshal(der, &info); err != nil || len(rest) != 0 {
		return nil, errors.New("not a PKCS#8 encrypted private key")
	}
	if !info.Algorithm.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("unsupported encryption scheme %v (only PBES2 is supported)", info.Algorithm.Algorithm)
	}
	var params pbes2Params
	if _, err := asn1.Unmarshal(info.Algorithm.Parameters.FullBytes, &params); err != nil {
		return nil, errors.New("malformed PBES2 parameters")
	}
	if params.KDF.Algorithm.Equal(oidScrypt) {
		return nil, errors.New("its key is derived with scrypt, which is not supported: " +
			`re-encrypt it with PBKDF2 ("openssl pkcs8 -topk8 -v2 aes-256-cbc")`)
	}
	if !params.KDF.Algorithm.Equal(oidPBKDF2) {
		return nil, fmt.Errorf("unsupported key derivation %v (only PBKDF2 is supported)", params.KDF.Algorithm)
	}
	var kdf pbkdf2Params
	if _, err := asn1.Unmarshal(params.KDF.Parameters.FullBytes, &kdf); err != nil {
		return nil, errors.New("malformed PBKDF2 parameters")
	}
	if kdf.Iterations < 1 || kdf.Iterations > maxPBKDF2Iterations {
		return nil, fmt.Errorf("unreasonable PBKDF2 iteration count %d", kdf.Iterations)
	}

	var (
		keyLen   int
		newBlock = aes.NewCipher
	)
	switch c := params.Cipher.Algorithm; {
	case c.Equal(oidAES128CBC):
		keyLen = 16
	case c.Equal(oidAES192CBC):
		keyLen = 24
	case c.Equal(oidAES256CBC):
		keyLen = 32
	case c.Equal(oid3DESCBC):
		keyLen, newBlock = 24, des.NewTripleDESCipher
	default:
		return nil, fmt.Errorf("unsupported cipher %v (only AES-CBC and triple-DES-CBC are supported)", c)
	}
	if kdf.KeyLength != 0 && kdf.KeyLength != keyLen {
		return nil, fmt.Errorf("PBKDF2 key length %d does not match the cipher's %d", kdf.KeyLength, keyLen)
	}
	var iv []byte
	if _, err := asn1.Unmarshal(params.Cipher.Parameters.FullBytes, &iv); err != nil {
		return nil, errors.New("malformed cipher parameters")
	}

	var prf func() hash.Hash
	switch p := kdf.PRF.Algorithm; {
	case len(p) == 0, p.Equal(oidHMACSHA1): // absent: the RFC's default
		prf = sha1.New
	case p.Equal(oidHMACSHA256):
		prf = sha256.New
	case p.Equal(oidHMACSHA384):
		prf = sha512.New384
	case p.Equal(oidHMACSHA512):
		prf = sha512.New
	default:
		return nil, fmt.Errorf("unsupported PBKDF2 hash %v", p)
	}
	key, err := pbkdf2.Key(prf, password, kdf.Salt, kdf.Iterations, keyLen)
	if err != nil {
		return nil, fmt.Errorf("deriving the key: %w", err)
	}

	block, err := newBlock(key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	data := info.Data
	if len(iv) != size || len(data) == 0 || len(data)%size != 0 {
		return nil, errors.New("malformed encrypted data")
	}
	plain := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, data)

	// PKCS#7 padding, then a real parse: between them a wrong password is caught.
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > size {
		return nil, errWrongPassword
	}
	for _, c := range plain[len(plain)-pad:] {
		if int(c) != pad {
			return nil, errWrongPassword
		}
	}
	plain = plain[:len(plain)-pad]
	if _, err := x509.ParsePKCS8PrivateKey(plain); err != nil {
		return nil, errWrongPassword
	}
	return plain, nil
}
