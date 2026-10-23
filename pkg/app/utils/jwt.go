package utils

import (
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"time"
)

// SecretKey signs and verifies JWTs. It must be set via SetSecretKey before
// any token is issued or parsed; InitSecretKey (called from server startup)
// loads it from config/env instead of a hardcoded value.
var SecretKey []byte

// SetSecretKey overrides the active JWT signing key. Exposed for tests and
// for server startup to install the configured/generated key.
func SetSecretKey(key []byte) { SecretKey = key }

type Claims struct {
	UserID      uint     `json:"user_id"`
	Username    string   `json:"username"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
	SessionID   string   `json:"session_id"`
	jwt.RegisteredClaims
}

// GenerateToken generates JWT Token (supports dynamic duration)
func GenerateToken(uid uint, username string, roles []string, permissions []string, expireHours int) (string, error) {
	if expireHours <= 0 {
		expireHours = 24 // Defaults to 24 hours
	}

	claims := Claims{
		UserID:      uid,
		Username:    username,
		Roles:       roles,
		Permissions: permissions,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expireHours) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(SecretKey)
}

func GenerateAccessToken(uid uint, username string, roles []string, permissions []string, sessionID string, expireMinutes int) (string, error) {
	if expireMinutes <= 0 {
		expireMinutes = 30
	}
	claims := Claims{
		UserID: uid, Username: username, Roles: roles, Permissions: permissions, SessionID: sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(expireMinutes) * time.Minute)),
			IssuedAt:  jwt.NewNumericDate(time.Now()), NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(SecretKey)
}

// ParseToken parses JWT Token
func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return SecretKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}

// GenerateLoginTicket generates a short-lived (5m) JWT ticket for two-step OTP
func GenerateLoginTicket(uid uint) (string, error) {
	claims := jwt.MapClaims{
		"user_id": uid,
		"is_temp": true,
		"exp":     time.Now().Add(5 * time.Minute).Unix(),
		"iat":     time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(SecretKey)
}

// VerifyLoginTicket validates the temporary ticket and returns user ID
func VerifyLoginTicket(ticket string) (uint, error) {
	token, err := jwt.Parse(ticket, func(token *jwt.Token) (interface{}, error) {
		return SecretKey, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if isTemp, ok := claims["is_temp"].(bool); ok && isTemp {
			if uid, ok := claims["user_id"].(float64); ok {
				return uint(uid), nil
			}
		}
	}
	return 0, errors.New("invalid login ticket")
}
