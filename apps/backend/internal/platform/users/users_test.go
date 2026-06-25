package users_test

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/users"
)

// ---------------------------------------------------------------------------
// NormalizeEmail
// ---------------------------------------------------------------------------

func TestNormalizeEmail_TrimsAndLowercases(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"user@example.com", "user@example.com"},
		{"  USER@EXAMPLE.COM  ", "user@example.com"},
		{"\tUser@Example.COM\n", "user@example.com"},
		{"ADMIN@ARENA.RU", "admin@arena.ru"},
	}
	for _, tc := range cases {
		got, err := users.NormalizeEmail(tc.raw)
		if err != nil {
			t.Errorf("NormalizeEmail(%q) unexpected error: %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeEmail(%q) = %q; want %q", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizeEmail_EmptyReturnsError(t *testing.T) {
	cases := []string{"", "   ", "\t\n"}
	for _, raw := range cases {
		_, err := users.NormalizeEmail(raw)
		if err == nil {
			t.Errorf("NormalizeEmail(%q) expected error, got nil", raw)
			continue
		}
		if !errors.Is(err, users.ErrEmailRequired) {
			t.Errorf("NormalizeEmail(%q) err = %v; want ErrEmailRequired", raw, err)
		}
	}
}

// ---------------------------------------------------------------------------
// HashPassword + CheckPassword
// ---------------------------------------------------------------------------

func TestHashPassword_ReturnsValidBcryptHash(t *testing.T) {
	hash, err := users.HashPassword("correctpassword123")
	if err != nil {
		t.Fatalf("HashPassword: unexpected error: %v", err)
	}
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("hash %q does not look like a bcrypt hash (expected $2... prefix)", hash)
	}
	// Verify cost is ≥ 12.
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("bcrypt.Cost: %v", err)
	}
	if cost < users.BcryptCost {
		t.Errorf("bcrypt cost = %d; want ≥ %d", cost, users.BcryptCost)
	}
}

func TestHashPassword_TooShortReturnsError(t *testing.T) {
	_, err := users.HashPassword("short")
	if !errors.Is(err, users.ErrPasswordTooShort) {
		t.Errorf("expected ErrPasswordTooShort, got %v", err)
	}
}

func TestHashPassword_TooLongReturnsError(t *testing.T) {
	// 73 characters — 1 over the 72-byte bcrypt limit.
	long := strings.Repeat("a", 73)
	_, err := users.HashPassword(long)
	if !errors.Is(err, users.ErrPasswordTooLong) {
		t.Errorf("expected ErrPasswordTooLong, got %v", err)
	}
}

func TestHashPassword_ExactMinLengthSucceeds(t *testing.T) {
	exact := strings.Repeat("x", users.MinPasswordLength) // 8 chars
	_, err := users.HashPassword(exact)
	if err != nil {
		t.Errorf("HashPassword(%d chars) unexpected error: %v", len(exact), err)
	}
}

func TestHashPassword_Exactly72BytesSucceeds(t *testing.T) {
	max := strings.Repeat("y", 72)
	_, err := users.HashPassword(max)
	if err != nil {
		t.Errorf("HashPassword(72 bytes) unexpected error: %v", err)
	}
}

func TestCheckPassword_CorrectPasswordNilError(t *testing.T) {
	pw := "superSecret99"
	hash, err := users.HashPassword(pw)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := users.CheckPassword(hash, pw); err != nil {
		t.Errorf("CheckPassword(correct): expected nil, got %v", err)
	}
}

func TestCheckPassword_WrongPasswordReturnsError(t *testing.T) {
	hash, _ := users.HashPassword("correctPassword1")
	err := users.CheckPassword(hash, "wrongPassword99")
	if err == nil {
		t.Error("CheckPassword(wrong): expected error, got nil")
	}
}

func TestHashPassword_DifferentHashEachCall(t *testing.T) {
	// bcrypt salts every hash — two calls with the same password must differ.
	pw := "samePassword123"
	h1, _ := users.HashPassword(pw)
	h2, _ := users.HashPassword(pw)
	if h1 == h2 {
		t.Error("expected distinct hashes on repeated calls (bcrypt salt must differ)")
	}
}

// ---------------------------------------------------------------------------
// GenerateVerificationToken
// ---------------------------------------------------------------------------

func TestGenerateVerificationToken_LengthIs64(t *testing.T) {
	tok, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	// 32 random bytes → 64 hex characters.
	want := users.VerificationTokenBytes * 2
	if len(tok) != want {
		t.Errorf("token length = %d; want %d", len(tok), want)
	}
}

func TestGenerateVerificationToken_IsHex(t *testing.T) {
	tok, err := users.GenerateVerificationToken()
	if err != nil {
		t.Fatalf("GenerateVerificationToken: %v", err)
	}
	const hexChars = "0123456789abcdef"
	for i, ch := range tok {
		if !strings.ContainsRune(hexChars, ch) {
			t.Errorf("token[%d] = %q is not a hex digit", i, ch)
		}
	}
}

func TestGenerateVerificationToken_UniqueEachCall(t *testing.T) {
	t1, _ := users.GenerateVerificationToken()
	t2, _ := users.GenerateVerificationToken()
	if t1 == t2 {
		t.Error("expected distinct tokens on repeated calls")
	}
}

// ---------------------------------------------------------------------------
// Constants sanity
// ---------------------------------------------------------------------------

func TestBcryptCostIsAtLeast12(t *testing.T) {
	if users.BcryptCost < 12 {
		t.Errorf("BcryptCost = %d; compliance spec requires ≥ 12", users.BcryptCost)
	}
}

func TestMinPasswordLengthIsAtLeast8(t *testing.T) {
	if users.MinPasswordLength < 8 {
		t.Errorf("MinPasswordLength = %d; spec requires ≥ 8", users.MinPasswordLength)
	}
}
