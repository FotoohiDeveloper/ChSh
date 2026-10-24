package platform

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Crypto protects personal data at rest. Values are sealed with AES-256-GCM
// (random nonce, ciphertext bound to a context string so a value copied into
// another column fails to decrypt). Blind indexes are keyed HMAC-SHA256 values
// that allow equality lookup without decrypting anything.
type Crypto struct {
	aead   cipher.AEAD
	idxKey []byte
}

func NewCrypto(encKey, idxKey []byte) (*Crypto, error) {
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Crypto{aead: aead, idxKey: idxKey}, nil
}

func (c *Crypto) Seal(context, plain string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plain), []byte(context)), nil
}

func (c *Crypto) Open(context string, sealed []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n+c.aead.Overhead() {
		return "", errors.New("sealed value too short")
	}
	plain, err := c.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return "", errors.New("cannot decrypt value")
	}
	return string(plain), nil
}

// Index returns the blind index of value within a namespace ("nid", "mobile").
func (c *Crypto) Index(namespace, value string) []byte {
	m := hmac.New(sha256.New, c.idxKey)
	m.Write([]byte(namespace))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return m.Sum(nil)
}

// ---- passwords (argon2id) ----

const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 2
	argonKeyLen  = 32
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func CheckPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyHash is verified against when a username does not exist, so login time
// does not reveal which usernames are real.
var DummyHash, _ = HashPassword("not-a-real-password")

// ---- tokens ----

func RandomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing means the OS is broken; do not continue
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// TokenID is what we store server-side; the raw token is never persisted.
func TokenID(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

const passwordAlphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// TempPassword generates a readable one-time password (no look-alike characters).
func TempPassword(n int) string {
	out := make([]byte, n)
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i := range out {
		out[i] = passwordAlphabet[int(buf[i])%len(passwordAlphabet)]
	}
	return string(out)
}
