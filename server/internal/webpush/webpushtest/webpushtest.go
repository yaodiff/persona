// Package webpushtest is a browser for tests of Web Push senders: a subscription with keys of its own, and the
// receiving side of RFC 8291, written apart from the package's encryption so each checks the other.
package webpushtest

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/webpush"
)

// Browser is one subscriber.
type Browser struct {
	Key  *ecdh.PrivateKey
	Auth []byte
	Sub  webpush.Subscription
}

// NewBrowser subscribes a browser at endpoint.
func NewBrowser(endpoint string) (*Browser, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		return nil, err
	}
	enc := base64.RawURLEncoding.EncodeToString
	return &Browser{Key: k, Auth: auth, Sub: webpush.Subscription{Endpoint: endpoint, P256DH: enc(k.PublicKey().Bytes()), Auth: enc(auth)}}, nil
}

// Open decrypts a push's body as the browser does: one aes128gcm record (RFC 8188) keyed by RFC 8291.
func (b *Browser) Open(body []byte) ([]byte, error) {
	const head = 16 + 4 + 1
	if len(body) < head {
		return nil, errors.New("short body")
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs < 18 || len(body) < head+idlen || int(rs) < len(body)-head-idlen {
		return nil, fmt.Errorf("record size %d, key id %d bytes, body %d bytes", rs, idlen, len(body))
	}
	sender, err := ecdh.P256().NewPublicKey(body[head : head+idlen])
	if err != nil {
		return nil, fmt.Errorf("sender key: %w", err)
	}
	shared, err := b.Key.ECDH(sender)
	if err != nil {
		return nil, err
	}
	info := []byte("WebPush: info\x00")
	info = append(info, b.Key.PublicKey().Bytes()...)
	info = append(info, sender.Bytes()...)
	prkKey, err := hkdf.Extract(sha256.New, shared, b.Auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[head+idlen:], nil)
	if err != nil {
		return nil, err
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, errors.New("not the last record")
	}
	return plain[:len(plain)-1], nil
}
