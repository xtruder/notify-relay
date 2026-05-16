package p2p

import (
	"bytes"
	"testing"
)

func TestDeriveKey(t *testing.T) {
	key1, err := DeriveKey("test-channel")
	if err != nil {
		t.Fatalf("DeriveKey failed: %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key1))
	}

	// Same channel ID should produce same key
	key2, err := DeriveKey("test-channel")
	if err != nil {
		t.Fatalf("DeriveKey failed: %v", err)
	}
	if !bytes.Equal(key1, key2) {
		t.Fatal("same channel ID should derive same key")
	}

	// Different channel ID should produce different key
	key3, err := DeriveKey("other-channel")
	if err != nil {
		t.Fatalf("DeriveKey failed: %v", err)
	}
	if bytes.Equal(key1, key3) {
		t.Fatal("different channel IDs should derive different keys")
	}
}

func TestDeriveKeyEmpty(t *testing.T) {
	_, err := DeriveKey("")
	if err == nil {
		t.Fatal("expected error for empty channel ID")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key, _ := DeriveKey("test-channel")
	plaintext := []byte("hello, world!")

	ciphertext, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext should differ from plaintext")
	}

	decrypted, err := Decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted %q != plaintext %q", decrypted, plaintext)
	}
}

func TestDecryptWrongKey(t *testing.T) {
	key1, _ := DeriveKey("channel-1")
	key2, _ := DeriveKey("channel-2")

	ciphertext, _ := Encrypt(key1, []byte("secret"))
	_, err := Decrypt(key2, ciphertext)
	if err == nil {
		t.Fatal("expected decryption to fail with wrong key")
	}
}

func TestDecryptTooShort(t *testing.T) {
	key, _ := DeriveKey("test")
	_, err := Decrypt(key, []byte("short"))
	if err == nil {
		t.Fatal("expected error for short ciphertext")
	}
}

func TestChannelHash(t *testing.T) {
	h1 := ChannelHash("test-channel")
	h2 := ChannelHash("test-channel")
	h3 := ChannelHash("other-channel")

	if h1 != h2 {
		t.Fatal("same input should produce same hash")
	}
	if h1 == h3 {
		t.Fatal("different input should produce different hash")
	}
	if len(h1) != 64 { // sha256 hex = 64 chars
		t.Fatalf("expected 64-char hex hash, got %d", len(h1))
	}
}
