package users

import (
	"fmt"
	"sync"

	"github.com/datarhei/core/v16/io/fs"

	"github.com/google/uuid"
)

// Registry manages the set of named users, backed by a JSON file.
type Registry interface {
	// Authenticate checks a username/password against the registry. Returns
	// the matching user and true on success.
	Authenticate(username, password string) (User, bool)

	// Get returns a user by ID.
	Get(id string) (User, bool)

	// GetByUsername returns a user by username.
	GetByUsername(username string) (User, bool)

	// List returns all users.
	List() []User

	// Create adds a new user. Returns an error if the username is already
	// taken.
	Create(username, password string, role Role, maxProcesses int) (User, error)

	// Update changes an existing user's role/quota, and optionally their
	// password (if newPassword is non-empty).
	Update(id string, role Role, maxProcesses int, newPassword string) (User, error)

	// Delete removes a user.
	Delete(id string) error
}

type registry struct {
	store *store

	lock  sync.RWMutex
	users map[string]User // by ID
}

// New returns a new user Registry, loading any existing users from the
// given filesystem/path (defaults to "/users.json" if path is empty).
func New(filesystem fs.Filesystem, path string) (Registry, error) {
	s, err := newStore(filesystem, path)
	if err != nil {
		return nil, err
	}

	r := &registry{
		store: s,
		users: map[string]User{},
	}

	list, err := s.Load()
	if err != nil {
		return nil, fmt.Errorf("loading users: %w", err)
	}

	for _, u := range list {
		r.users[u.ID] = u
	}

	return r, nil
}

func (r *registry) persist() error {
	list := make([]User, 0, len(r.users))
	for _, u := range r.users {
		list = append(list, u)
	}

	return r.store.Store(list)
}

func (r *registry) Authenticate(username, password string) (User, bool) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	for _, u := range r.users {
		if u.Username != username {
			continue
		}

		if !checkPassword(u.PasswordHash, password) {
			return User{}, false
		}

		return u, true
	}

	return User{}, false
}

func (r *registry) Get(id string) (User, bool) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	u, ok := r.users[id]

	return u, ok
}

func (r *registry) GetByUsername(username string) (User, bool) {
	r.lock.RLock()
	defer r.lock.RUnlock()

	for _, u := range r.users {
		if u.Username == username {
			return u, true
		}
	}

	return User{}, false
}

func (r *registry) List() []User {
	r.lock.RLock()
	defer r.lock.RUnlock()

	list := make([]User, 0, len(r.users))
	for _, u := range r.users {
		list = append(list, u)
	}

	return list
}

func (r *registry) Create(username, password string, role Role, maxProcesses int) (User, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	if len(username) == 0 {
		return User{}, fmt.Errorf("username must not be empty")
	}

	for _, u := range r.users {
		if u.Username == username {
			return User{}, fmt.Errorf("a user named %s already exists", username)
		}
	}

	hash, err := hashPassword(password)
	if err != nil {
		return User{}, fmt.Errorf("hashing password: %w", err)
	}

	if role != RoleAdmin {
		role = RoleUser
	}

	if maxProcesses < 0 {
		maxProcesses = DefaultMaxProcesses
	}

	t := now()

	u := User{
		ID:           uuid.New().String(),
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		MaxProcesses: maxProcesses,
		CreatedAt:    t,
		UpdatedAt:    t,
	}

	r.users[u.ID] = u

	if err := r.persist(); err != nil {
		delete(r.users, u.ID)
		return User{}, err
	}

	return u, nil
}

func (r *registry) Update(id string, role Role, maxProcesses int, newPassword string) (User, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	u, ok := r.users[id]
	if !ok {
		return User{}, fmt.Errorf("no such user")
	}

	previous := u

	if role != RoleAdmin {
		role = RoleUser
	}
	u.Role = role

	if maxProcesses >= 0 {
		u.MaxProcesses = maxProcesses
	}

	if len(newPassword) != 0 {
		hash, err := hashPassword(newPassword)
		if err != nil {
			return User{}, fmt.Errorf("hashing password: %w", err)
		}

		u.PasswordHash = hash
	}

	u.UpdatedAt = now()

	r.users[id] = u

	if err := r.persist(); err != nil {
		r.users[id] = previous
		return User{}, err
	}

	return u, nil
}

func (r *registry) Delete(id string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	previous, ok := r.users[id]
	if !ok {
		return fmt.Errorf("no such user")
	}

	delete(r.users, id)

	if err := r.persist(); err != nil {
		r.users[id] = previous
		return err
	}

	return nil
}
