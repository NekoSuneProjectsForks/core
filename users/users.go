// Package users implements a lightweight multi-user layer on top of
// CORE's existing single-admin auth. The admin identity from
// config.API.Auth.Username/Password is untouched and always works
// (see http/jwt's localValidator) - this package only adds additional,
// separately-authenticated named users with their own role and a quota
// on how many processes they may own.
package users

import (
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Role of a user. Admins bypass ownership filtering and quota checks
// entirely, the same as the bootstrap super-admin.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// DefaultMaxProcesses is how many processes a newly created "user"-role
// user may own unless the admin sets something else.
const DefaultMaxProcesses = 2

// User is a single named account.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Role         Role   `json:"role"`

	// MaxProcesses is the maximum number of processes this user may own.
	// Ignored for admins (unlimited).
	MaxProcesses int `json:"max_processes"`

	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}

// IsAdmin reports whether this user's role bypasses ownership/quota checks.
func (u User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}

	return string(hash), nil
}

func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func now() int64 {
	return time.Now().Unix()
}
