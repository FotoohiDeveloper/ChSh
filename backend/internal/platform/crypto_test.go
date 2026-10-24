package platform

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func newTestCrypto(t *testing.T) *Crypto {
	enc, idx := make([]byte, 32), make([]byte, 32)
	_, _ = rand.Read(enc)
	_, _ = rand.Read(idx)
	c, err := NewCrypto(enc, idx)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSealOpen(t *testing.T) {
	c := newTestCrypto(t)
	sealed, err := c.Seal("pilgrim.nid", "0084575948")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("0084575948")) {
		t.Fatal("plaintext visible in sealed value")
	}
	got, err := c.Open("pilgrim.nid", sealed)
	if err != nil || got != "0084575948" {
		t.Fatalf("got %q, %v", got, err)
	}
	// moving a ciphertext into another column must fail
	if _, err := c.Open("pilgrim.mobile", sealed); err == nil {
		t.Fatal("context binding missing")
	}
	// tampering must fail
	sealed[len(sealed)-1] ^= 1
	if _, err := c.Open("pilgrim.nid", sealed); err == nil {
		t.Fatal("tampered value accepted")
	}
	// same plaintext twice gives different ciphertexts (random nonce)
	a, _ := c.Seal("x", "same")
	b, _ := c.Seal("x", "same")
	if bytes.Equal(a, b) {
		t.Fatal("nonce reuse")
	}
}

func TestBlindIndex(t *testing.T) {
	c := newTestCrypto(t)
	if !bytes.Equal(c.Index("nid", "123"), c.Index("nid", "123")) {
		t.Fatal("index not deterministic")
	}
	if bytes.Equal(c.Index("nid", "123"), c.Index("mobile", "123")) {
		t.Fatal("namespaces collide")
	}
	other := newTestCrypto(t)
	if bytes.Equal(c.Index("nid", "123"), other.Index("nid", "123")) {
		t.Fatal("index must depend on the key")
	}
}

func TestPasswords(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatal("not argon2id")
	}
	if !CheckPassword("correct horse battery", h) || CheckPassword("wrong", h) || CheckPassword("x", "garbage") {
		t.Fatal("password check wrong")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salt not random")
	}
}

func TestTokens(t *testing.T) {
	a, b := RandomToken(32), RandomToken(32)
	if a == b || len(a) < 40 {
		t.Fatal("weak tokens")
	}
	if TokenID(a) == a || len(TokenID(a)) != 64 {
		t.Fatal("token id must be a hash")
	}
	pw := TempPassword(12)
	if len(pw) != 12 || strings.ContainsAny(pw, "0O1lIi") {
		t.Fatalf("bad temp password %q", pw)
	}
}
