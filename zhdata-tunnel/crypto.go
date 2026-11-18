package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/cipher"
)

// ---- HKDF-SHA256 (RFC 5869), zero external deps ----

func hkdfExtract(salt, ikm []byte) []byte {
	m := hmac.New(sha256.New, salt)
	m.Write(ikm)
	return m.Sum(nil)
}

func hkdfExpand(prk, info []byte, n int) []byte {
	var out, t []byte
	for i := 1; len(out) < n; i++ {
		m := hmac.New(sha256.New, prk)
		m.Write(t)
		m.Write(info)
		m.Write([]byte{byte(i)})
		t = m.Sum(nil)
		out = append(out, t...)
	}
	return out[:n]
}

// deriveKeys derives the per-connection AES-CTR key/iv from (uuid, salt).
func deriveKeys(uuid string, salt []byte) (key, iv []byte) {
	prk := hkdfExtract(salt, []byte(uuid))
	key = hkdfExpand(prk, []byte("zhdata-v1-key"), 32)
	iv = hkdfExpand(prk, []byte("zhdata-v1-iv"), 16)
	return
}

func newCTR(uuid string, salt []byte) cipher.Stream {
	key, iv := deriveKeys(uuid, salt)
	blk, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // key is always 32 bytes
	}
	return cipher.NewCTR(blk, iv)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
