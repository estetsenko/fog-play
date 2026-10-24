package auth

import (
	"errors"
	"testing"
	"time"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestPassword(t *testing.T) {
	h, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "hunter22") {
		t.Error("correct password rejected")
	}
	if CheckPassword(h, "hunter23") {
		t.Error("wrong password accepted")
	}
}

func TestTokenRoundTrip(t *testing.T) {
	iss := NewIssuer(secret, time.Hour)
	tok, err := iss.Issue(Claims{UserID: "u1", Role: "host"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := iss.Parse(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "u1" || c.Role != "host" {
		t.Errorf("got %+v", c)
	}
}

func TestTokenRejected(t *testing.T) {
	iss := NewIssuer(secret, time.Hour)
	tok, _ := iss.Issue(Claims{UserID: "u1", Role: "player"})

	other := NewIssuer([]byte("ffffffffffffffffffffffffffffffff"), time.Hour)
	if _, err := other.Parse(tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong secret: got %v", err)
	}

	expired := NewIssuer(secret, -time.Minute)
	old, _ := expired.Issue(Claims{UserID: "u1", Role: "player"})
	if _, err := iss.Parse(old); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired: got %v", err)
	}
}
