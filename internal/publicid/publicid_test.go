package publicid_test

import (
	"testing"

	"github.com/Viky-Developer/dodo-payments-backend/internal/publicid"
)

func TestCodec(t *testing.T) {
	codec := publicid.New("test_secret_key_1234567890")

	tests := []struct {
		prefix string
		id     int64
	}{
		{publicid.PrefixCustomer, 1},
		{publicid.PrefixCustomer, 842735982145671168},
		{publicid.PrefixInvoice, 842735982145671168},
		{publicid.PrefixInvoiceItem, 100},
		{publicid.PrefixBusiness, 9999999999},
		{publicid.PrefixAPIKey, 42},
		{publicid.PrefixPaymentAttempt, 777},
	}

	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			encoded, err := codec.Encode(tt.prefix, tt.id)
			if err != nil {
				t.Fatalf("unexpected encode error: %v", err)
			}

			if len(encoded) < len(tt.prefix)+2 {
				t.Fatalf("encoded ID too short: %s", encoded)
			}

			decoded, err := codec.Decode(tt.prefix, encoded)
			if err != nil {
				t.Fatalf("unexpected decode error: %v", err)
			}

			if decoded != tt.id {
				t.Fatalf("expected ID %d, got %d", tt.id, decoded)
			}
		})
	}
}

func TestCodecPrefixMismatch(t *testing.T) {
	codec := publicid.New("test_secret")

	// Encode as customer
	encoded, err := codec.Encode(publicid.PrefixCustomer, 123456)
	if err != nil {
		t.Fatalf("encode error: %v", err)
	}

	// Try to decode as invoice
	_, err = codec.Decode(publicid.PrefixInvoice, encoded)
	if err == nil {
		t.Fatal("expected error when decoding customer ID as invoice, got nil")
	}
}

func TestCodecInvalidFormat(t *testing.T) {
	codec := publicid.New("test_secret")

	invalidIDs := []string{
		"",
		"inv",
		"inv_",
		"_abc",
		"inv_!@#$%",
	}

	for _, id := range invalidIDs {
		_, err := codec.Decode(publicid.PrefixInvoice, id)
		if err == nil {
			t.Fatalf("expected error for invalid ID %q, got nil", id)
		}
	}
}
