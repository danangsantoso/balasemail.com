// Package security provides password hashing, tokens, signing and
// at-rest encryption primitives used across modules.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a password with bcrypt.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CheckPassword reports whether pw matches hash.
func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// RandomHex returns n random bytes hex-encoded.
func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// NumericCode returns a uniformly random numeric code of the given length.
func NumericCode(length int) string {
	var sb strings.Builder
	for range length {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(byte('0' + n.Int64()))
	}
	return sb.String()
}

// AlphanumericCode returns a random code from an unambiguous uppercase alphabet.
func AlphanumericCode(length int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var sb strings.Builder
	for range length {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(alphabet[n.Int64()])
	}
	return sb.String()
}

// SHA256 returns the hex SHA-256 of s.
func SHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ConstantTimeEqual compares two strings in constant time.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ---- JWT ----

// Claims are the JWT claims for dashboard sessions.
type Claims struct {
	WorkspaceID string `json:"wid"`
	Role        string `json:"role"`
	jwt.RegisteredClaims
}

// JWTIssuer issues and parses HS256 session tokens.
type JWTIssuer struct {
	secret []byte
	ttl    time.Duration
}

func NewJWTIssuer(secret string, ttl time.Duration) *JWTIssuer {
	return &JWTIssuer{secret: []byte(secret), ttl: ttl}
}

func (j *JWTIssuer) Issue(userID, workspaceID, role string) (string, time.Time, error) {
	exp := time.Now().Add(j.ttl)
	c := Claims{
		WorkspaceID: workspaceID,
		Role:        role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(exp),
			Issuer:    "balasemail",
		},
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(j.secret)
	return s, exp, err
}

func (j *JWTIssuer) Parse(token string) (*Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		return j.secret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("balasemail"))
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ---- HMAC signer for opaque URL tokens (unsubscribe, tracking) ----

// Signer produces compact, URL-safe, tamper-proof tokens.
type Signer struct{ key []byte }

func NewSigner(key string) *Signer { return &Signer{key: []byte(key)} }

func (s *Signer) mac(payload string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// Sign returns base64url(payload).mac.
func (s *Signer) Sign(payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.mac(payload)
}

// Verify returns the payload if the token is authentic.
func (s *Signer) Verify(token string) (string, error) {
	enc, mac, ok := strings.Cut(token, ".")
	if !ok {
		return "", errors.New("malformed token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "", errors.New("malformed token")
	}
	if !hmac.Equal([]byte(mac), []byte(s.mac(string(raw)))) {
		return "", errors.New("invalid signature")
	}
	return string(raw), nil
}

// MAC returns a keyed hash of value (for storing OTP codes).
func (s *Signer) MAC(value string) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(value))
	return m.Sum(nil)
}

// ---- AES-GCM for secrets at rest (DKIM keys, SMTP passwords) ----

// Cipher encrypts small secrets with AES-256-GCM.
type Cipher struct{ aead cipher.AEAD }

// NewCipher derives a 32-byte key from secret via SHA-256.
func NewCipher(secret string) (*Cipher, error) {
	if len(secret) < 16 {
		return nil, errors.New("encryption key must be at least 16 characters")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, plain, nil), nil
}

func (c *Cipher) Decrypt(sealed []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return c.aead.Open(nil, sealed[:n], sealed[n:], nil)
}

// UUID returns a random RFC 4122 version 4 UUID.
func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
