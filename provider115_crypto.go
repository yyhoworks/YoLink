package main

// 115's download wire format, adapted from SheltonZhu/115driver/pkg/crypto/m115.
// Copyright (c) 2022-2024 SheltonZhu, MIT; see licenses/115driver-LICENSE.txt.
// This is provider protocol compatibility, not application credential storage.
import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"math/big"
)

// defaultCipher115Public is 115's protocol public key (see file header).
// Providers may carry an override in their key115 field; production code
// never sets it, isolated tests use it to run the protocol against a key
// pair they control instead of mutating this package-level value.
var defaultCipher115Public = func() *rsa.PublicKey {
	n, _ := new(big.Int).SetString("8686980c0f5a24c4b9d43020cd2c22703ff3f450756529058b1cf88f09b8602136477198a6e2683149659bd122c33592fdb5ad47944ad1ea4d36c6b172aad6338c3bb6ac6227502d010993ac967d1aef00f0c8e038de2e4d3bc2ec368af2e9f10a6f1eda4f7262f136420c07c331b871bf139f74f3010e3c4fe57df3afb71683", 16)
	return &rsa.PublicKey{N: n, E: 65537}
}()

func (p *Providers) public115() *rsa.PublicKey {
	if p != nil && p.key115 != nil {
		return p.key115
	}
	return defaultCipher115Public
}

var cipher115Seed = [...]byte{
	0xf0, 0xe5, 0x69, 0xae, 0xbf, 0xdc, 0xbf, 0x8a, 0x1a, 0x45, 0xe8, 0xbe, 0x7d, 0xa6, 0x73, 0xb8,
	0xde, 0x8f, 0xe7, 0xc4, 0x45, 0xda, 0x86, 0xc4, 0x9b, 0x64, 0x8b, 0x14, 0x6a, 0xb4, 0xf1, 0xaa,
	0x38, 0x01, 0x35, 0x9e, 0x26, 0x69, 0x2c, 0x86, 0x00, 0x6b, 0x4f, 0xa5, 0x36, 0x34, 0x62, 0xa6,
	0x2a, 0x96, 0x68, 0x18, 0xf2, 0x4a, 0xfd, 0xbd, 0x6b, 0x97, 0x8f, 0x4d, 0x8f, 0x89, 0x13, 0xb7,
	0x6c, 0x8e, 0x93, 0xed, 0x0e, 0x0d, 0x48, 0x3e, 0xd7, 0x2f, 0x88, 0xd8, 0xfe, 0xfe, 0x7e, 0x86,
	0x50, 0x95, 0x4f, 0xd1, 0xeb, 0x83, 0x26, 0x34, 0xdb, 0x66, 0x7b, 0x9c, 0x7e, 0x9d, 0x7a, 0x81,
	0x32, 0xea, 0xb6, 0x33, 0xde, 0x3a, 0xa9, 0x59, 0x34, 0x66, 0x3b, 0xaa, 0xba, 0x81, 0x60, 0x48,
	0xb9, 0xd5, 0x81, 0x9c, 0xf8, 0x6c, 0x84, 0x77, 0xff, 0x54, 0x78, 0x26, 0x5f, 0xbe, 0xe8, 0x1e,
	0x36, 0x9f, 0x34, 0x80, 0x5c, 0x45, 0x2c, 0x9b, 0x76, 0xd5, 0x1b, 0x8f, 0xcc, 0xc3, 0xb8, 0xf5,
}
var cipher115ClientKey = []byte{0x78, 0x06, 0xad, 0x4c, 0x33, 0x86, 0x5d, 0x18, 0x4c, 0x01, 0x3f, 0x46}

func derive115Key(seed []byte, size int) []byte {
	// The indexing below reaches cipher115Seed[size*(size-1)] and seed[size-1];
	// only sizes 4 and 12 (with a 16-byte seed) are ever used. Guard the
	// domain instead of panicking on a future misuse: an out-of-domain size
	// yields a zero key, which simply fails the protocol round-trip.
	if size < 1 || size > 12 || len(seed) < size {
		if size < 0 {
			size = 0
		}
		return make([]byte, size)
	}
	key := make([]byte, size)
	for i := range key {
		key[i] = (seed[i] + cipher115Seed[size*i]) ^ cipher115Seed[size*(size-i-1)]
	}
	return key
}
func xor115(data, key []byte) {
	mod := len(data) % 4
	for i := range data {
		index := i
		if i >= mod {
			index -= mod
		}
		data[i] ^= key[index%len(key)]
	}
}
func reverse115(data []byte) {
	for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
		data[i], data[j] = data[j], data[i]
	}
}
func encode115(input []byte, key [16]byte) (string, error) {
	return encode115WithKey(input, key, defaultCipher115Public)
}

func (p *Providers) encode115(input []byte, key [16]byte) (string, error) {
	return encode115WithKey(input, key, p.public115())
}

func encode115WithKey(input []byte, key [16]byte, pub *rsa.PublicKey) (string, error) {
	buf := append(append([]byte{}, key[:]...), input...)
	xor115(buf[16:], derive115Key(key[:], 4))
	reverse115(buf[16:])
	xor115(buf[16:], cipher115ClientKey)
	var encrypted []byte
	for len(buf) > 0 {
		n := min(len(buf), pub.Size()-11)
		block, err := rsa.EncryptPKCS1v15(rand.Reader, pub, buf[:n])
		if err != nil {
			return "", errors.New("115 下载请求编码失败")
		}
		encrypted = append(encrypted, block...)
		buf = buf[n:]
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func decode115(input string, key [16]byte) ([]byte, error) {
	return decode115WithKey(input, key, defaultCipher115Public)
}

func (p *Providers) decode115(input string, key [16]byte) ([]byte, error) {
	return decode115WithKey(input, key, p.public115())
}

func decode115WithKey(input string, key [16]byte, pub *rsa.PublicKey) ([]byte, error) {
	invalid := errors.New("115 下载响应编码异常，请稍后重试")
	data, err := base64.StdEncoding.DecodeString(input)
	size := pub.Size()
	if err != nil || len(data) == 0 || len(data) > 1<<20 || len(data)%size != 0 {
		return nil, invalid
	}
	var decoded []byte
	for len(data) > 0 {
		x := new(big.Int).SetBytes(data[:size])
		if x.Cmp(pub.N) >= 0 {
			return nil, invalid
		}
		block := new(big.Int).Exp(x, big.NewInt(int64(pub.E)), pub.N).FillBytes(make([]byte, size))
		separator := bytes.IndexByte(block[2:], 0) + 2
		if block[0] != 0 || (block[1] != 1 && block[1] != 2) || separator < 10 {
			return nil, invalid
		}
		if block[1] == 1 {
			// Type 1 padding must be all 0xFF up to the separator; type 2
			// padding is random non-zero bytes and needs no such check.
			for _, b := range block[2:separator] {
				if b != 0xff {
					return nil, invalid
				}
			}
		}
		decoded = append(decoded, block[separator+1:]...)
		data = data[size:]
	}
	if len(decoded) <= 16 {
		return nil, invalid
	}
	output := decoded[16:]
	xor115(output, derive115Key(decoded[:16], 12))
	reverse115(output)
	xor115(output, derive115Key(key[:], 4))
	return output, nil
}
