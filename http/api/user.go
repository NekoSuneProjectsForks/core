package api

import "github.com/datarhei/core/v16/users"

// User represents a named user account. The password hash is never exposed.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Role         string `json:"role"`
	MaxProcesses int    `json:"max_processes"`
	CreatedAt    int64  `json:"created_at" format:"int64"`
	UpdatedAt    int64  `json:"updated_at" format:"int64"`
}

func (u *User) Unmarshal(from *users.User) {
	if from == nil {
		return
	}

	u.ID = from.ID
	u.Username = from.Username
	u.Role = string(from.Role)
	u.MaxProcesses = from.MaxProcesses
	u.CreatedAt = from.CreatedAt
	u.UpdatedAt = from.UpdatedAt
}

// UserCreate is the payload to create a new named user.
type UserCreate struct {
	Username     string `json:"username" validate:"required"`
	Password     string `json:"password" validate:"required"`
	Role         string `json:"role"`
	MaxProcesses int    `json:"max_processes"`
}

// UserUpdate is the payload to update an existing named user. Password is
// optional; if empty the existing password is kept.
type UserUpdate struct {
	Role         string `json:"role"`
	MaxProcesses int    `json:"max_processes"`
	Password     string `json:"password"`
}
