// Package auth hashes passwords and issues and verifies JWT access tokens.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidToken = errors.New("invalid token")

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// Claims identify the caller of an authenticated request.
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
}

type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret []byte, ttl time.Duration) *Issuer {
	return &Issuer{secret: secret, ttl: ttl}
}

func (i *Issuer) Issue(c Claims) (string, error) {
	now := time.Now()
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  c.UserID,
		"role": c.Role,
		"iat":  now.Unix(),
		"exp":  now.Add(i.ttl).Unix(),
	})
	return t.SignedString(i.secret)
}

func (i *Issuer) Parse(token string) (Claims, error) {
	t, err := jwt.Parse(token, func(t *jwt.Token) (any, error) {
		return i.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	m, _ := t.Claims.(jwt.MapClaims)
	sub, _ := m["sub"].(string)
	role, _ := m["role"].(string)
	if sub == "" || role == "" {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: sub, Role: role}, nil
}
