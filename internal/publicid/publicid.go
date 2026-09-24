package publicid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Supported resource prefixes as defined in DATABASE.md
const (
	PrefixBusiness        = "biz"
	PrefixAPIKey          = "key"
	PrefixCustomer        = "cus"
	PrefixInvoice         = "inv"
	PrefixInvoiceItem     = "itm"
	PrefixIdempotencyKey  = "idem"
	PrefixPaymentAttempt  = "att"
	PrefixWebhookEndpoint = "whe"
	PrefixWebhookDelivery = "whd"
)

var (
	ErrInvalidIDFormat = errors.New("invalid public ID format")
	ErrInvalidPrefix   = errors.New("invalid resource prefix")
	ErrDecodingFailed  = errors.New("failed to decode public ID")
)

// Codec handles keyed, reversible encoding of 64-bit integer IDs with resource type context.
type Codec struct {
	secret []byte
}

// New creates a new Codec using the provided application-level secret.
func New(secret string) *Codec {
	if secret == "" {
		secret = "default_takehome_secret_key_change_in_production"
	}
	return &Codec{secret: []byte(secret)}
}

// Encode encodes a signed 64-bit integer ID with the specified resource prefix.
func (c *Codec) Encode(prefix string, id int64) (string, error) {
	if prefix == "" {
		return "", ErrInvalidPrefix
	}

	// 1. Derive 4 round keys for the 64-bit Feistel cipher using HMAC-SHA256(secret, prefix)
	roundKeys := c.deriveRoundKeys(prefix)

	// 2. Encrypt 64-bit integer through Feistel network (1:1 bijection)
	encrypted := feistelEncrypt(uint64(id), roundKeys)

	// 3. Encode to base62
	encoded := toBase62(encrypted)

	return fmt.Sprintf("%s_%s", prefix, encoded), nil
}

// Decode decodes a public ID string, validates the expected resource prefix,
// and recovers the original 64-bit integer ID.
func (c *Codec) Decode(expectedPrefix string, publicID string) (int64, error) {
	parts := strings.SplitN(publicID, "_", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, ErrInvalidIDFormat
	}

	if parts[0] != expectedPrefix {
		return 0, fmt.Errorf("%w: expected '%s_', got '%s_'", ErrInvalidPrefix, expectedPrefix, parts[0])
	}

	// 1. Decode base62 to uint64
	val, err := fromBase62(parts[1])
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrDecodingFailed, err)
	}

	// 2. Derive round keys for the expected resource context
	roundKeys := c.deriveRoundKeys(expectedPrefix)

	// 3. Decrypt through reverse Feistel network
	decrypted := feistelDecrypt(val, roundKeys)

	return int64(decrypted), nil
}

// deriveRoundKeys generates round keys for a 64-bit Feistel cipher.
func (c *Codec) deriveRoundKeys(context string) [4]uint32 {
	h := hmac.New(sha256.New, c.secret)
	h.Write([]byte(context))
	sum := h.Sum(nil)

	return [4]uint32{
		binary.BigEndian.Uint32(sum[0:4]),
		binary.BigEndian.Uint32(sum[4:8]),
		binary.BigEndian.Uint32(sum[8:12]),
		binary.BigEndian.Uint32(sum[12:16]),
	}
}

// feistelRound is the round function F(R, K) = (R ^ K) * 0x9e3779b9
func feistelRound(r uint32, k uint32) uint32 {
	return (r ^ k) * 0x9e3779b9
}

// feistelEncrypt runs a 4-round balanced Feistel cipher on a 64-bit block.
func feistelEncrypt(block uint64, keys [4]uint32) uint64 {
	l := uint32(block >> 32)
	r := uint32(block)

	for i := 0; i < 4; i++ {
		nextL := r
		nextR := l ^ feistelRound(r, keys[i])
		l = nextL
		r = nextR
	}

	return (uint64(l) << 32) | uint64(r)
}

// feistelDecrypt reverses the 4-round balanced Feistel cipher.
func feistelDecrypt(block uint64, keys [4]uint32) uint64 {
	l := uint32(block >> 32)
	r := uint32(block)

	for i := 3; i >= 0; i-- {
		prevR := l
		prevL := r ^ feistelRound(l, keys[i])
		l = prevL
		r = prevR
	}

	return (uint64(l) << 32) | uint64(r)
}

const base62Chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func toBase62(num uint64) string {
	if num == 0 {
		return "0"
	}
	n := new(big.Int).SetUint64(num)
	base := big.NewInt(62)
	zero := big.NewInt(0)
	mod := new(big.Int)

	var sb strings.Builder
	for n.Cmp(zero) > 0 {
		n.DivMod(n, base, mod)
		sb.WriteByte(base62Chars[mod.Int64()])
	}

	// Reverse string
	bytes := []byte(sb.String())
	for i, j := 0, len(bytes)-1; i < j; i, j = i+1, j-1 {
		bytes[i], bytes[j] = bytes[j], bytes[i]
	}
	return string(bytes)
}

func fromBase62(s string) (uint64, error) {
	n := big.NewInt(0)
	base := big.NewInt(62)

	for i := 0; i < len(s); i++ {
		idx := strings.IndexByte(base62Chars, s[i])
		if idx == -1 {
			return 0, fmt.Errorf("invalid base62 character: %c", s[i])
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}

	if !n.IsUint64() {
		return 0, errors.New("value exceeds 64-bit integer")
	}

	return n.Uint64(), nil
}
