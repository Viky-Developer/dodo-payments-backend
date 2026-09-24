package idgen

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidID    = errors.New("invalid public id format")
	ErrTypeMismatch = errors.New("id type mismatch")
)

// SnowflakeGenerator produces unique 64-bit integers.
// 41 bits timestamp (ms), 10 bits node ID (0-1023), 12 bits sequence (0-4095).
type SnowflakeGenerator struct {
	mu       sync.Mutex
	epoch    int64
	nodeID   int64
	sequence int64
	lastTime int64
}

// Custom epoch (e.g., 2026-01-01 00:00:00 UTC = 1767225600000 ms)
const defaultEpoch = int64(1767225600000)

func NewSnowflake(nodeID int64) *SnowflakeGenerator {
	return &SnowflakeGenerator{
		epoch:  defaultEpoch,
		nodeID: nodeID & 0x3FF, // 10 bits
	}
}

func (s *SnowflakeGenerator) NextID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	if now < s.lastTime {
		// Clock drift fallback
		now = s.lastTime
	}

	if now == s.lastTime {
		s.sequence = (s.sequence + 1) & 0xFFF // 12 bits
		if s.sequence == 0 {
			for now <= s.lastTime {
				time.Sleep(100 * time.Microsecond)
				now = time.Now().UnixMilli()
			}
		}
	} else {
		s.sequence = 0
	}

	s.lastTime = now

	id := ((now - s.epoch) << 22) | (s.nodeID << 12) | s.sequence
	return id
}

// Codec handles encoding numeric IDs to prefixed public strings and decoding back.
type Codec struct {
	block cipher.Block
}

func NewCodec(secretKey []byte) (*Codec, error) {
	if len(secretKey) != 16 && len(secretKey) != 24 && len(secretKey) != 32 {
		return nil, fmt.Errorf("secret key must be 16, 24, or 32 bytes, got %d", len(secretKey))
	}
	block, err := aes.NewCipher(secretKey)
	if err != nil {
		return nil, err
	}
	return &Codec{block: block}, nil
}

// Encode converts a numeric int64 ID and prefix (e.g. "cus", "inv") into a public ID (e.g. "cus_01a2b3...").
func (c *Codec) Encode(prefix string, id int64) string {
	src := make([]byte, 16)
	// Put type prefix hash into high 8 bytes for domain context binding
	copy(src[0:8], []byte(prefix + "________")[:8])
	binary.BigEndian.PutUint64(src[8:16], uint64(id))

	dst := make([]byte, 16)
	c.block.Encrypt(dst, src)

	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(dst))
}

// Decode parses a public ID string, validates the expected prefix, and extracts the numeric int64 ID.
func (c *Codec) Decode(expectedPrefix string, publicID string) (int64, error) {
	parts := strings.Split(publicID, "_")
	if len(parts) != 2 || parts[0] != expectedPrefix {
		return 0, fmt.Errorf("%w: expected prefix %s, got %s", ErrTypeMismatch, expectedPrefix, publicID)
	}

	dst, err := hex.DecodeString(parts[1])
	if err != nil || len(dst) != 16 {
		return 0, fmt.Errorf("%w: invalid hex payload", ErrInvalidID)
	}

	src := make([]byte, 16)
	c.block.Decrypt(src, dst)

	expectedTypePrefix := []byte(expectedPrefix + "________")[:8]
	if string(src[0:8]) != string(expectedTypePrefix) {
		return 0, fmt.Errorf("%w: type mismatch in decrypted payload", ErrTypeMismatch)
	}

	return int64(binary.BigEndian.Uint64(src[8:16])), nil
}

// Default global generator for convenience
var DefaultGenerator = NewSnowflake(1)

func GenerateID() int64 {
	return DefaultGenerator.NextID()
}
