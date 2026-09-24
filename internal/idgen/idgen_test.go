package idgen_test

import (
	"crypto/rand"
	"testing"

	"github.com/Viky-Developer/dodo-payments-backend/internal/idgen"
)

func TestSnowflakeGenerator(t *testing.T) {
	gen := idgen.NewSnowflake(1)
	id1 := gen.NextID()
	id2 := gen.NextID()

	if id1 <= 0 || id2 <= 0 {
		t.Fatalf("expected positive IDs, got %d, %d", id1, id2)
	}
	if id1 == id2 {
		t.Fatalf("expected unique IDs, got duplicate %d", id1)
	}
}

func TestCodecEncodeDecode(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)

	codec, err := idgen.NewCodec(key)
	if err != nil {
		t.Fatalf("failed to create codec: %v", err)
	}

	testID := int64(1234567890123456)
	encoded := codec.Encode("cus", testID)

	if len(encoded) == 0 {
		t.Fatalf("expected non-empty encoded string")
	}

	decoded, err := codec.Decode("cus", encoded)
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if decoded != testID {
		t.Fatalf("expected decoded ID %d, got %d", testID, decoded)
	}

	// Mismatched prefix should fail
	_, err = codec.Decode("inv", encoded)
	if err == nil {
		t.Fatalf("expected error when decoding with wrong prefix")
	}
}
