package api

import (
	"net/http"

	"github.com/datarhei/core/v16/http/api"
	"github.com/datarhei/core/v16/http/handler/util"
	"github.com/datarhei/core/v16/users"

	"github.com/labstack/echo/v4"
)

// The UsersHandler type provides functions to manage named users. All
// routes it's mounted on are admin-only (see RequireAdmin).
type UsersHandler struct {
	registry users.Registry
}

// NewUsers returns a new UsersHandler. You have to provide a valid users.Registry.
func NewUsers(registry users.Registry) *UsersHandler {
	return &UsersHandler{
		registry: registry,
	}
}

// GetAll lists all named users
// @Summary List all named users
// @Description List all named users. Admin only.
// @Tags v16.7.2
// @ID users-3-get-all
// @Produce json
// @Success 200 {array} api.User
// @Security ApiKeyAuth
// @Router /api/v3/users [get]
func (h *UsersHandler) GetAll(c echo.Context) error {
	list := h.registry.List()

	out := make([]api.User, len(list))
	for i, u := range list {
		out[i].Unmarshal(&u)
	}

	return c.JSON(http.StatusOK, out)
}

// Get returns the user with the given ID
// @Summary Get a named user by their ID
// @Tags v16.7.2
// @ID users-3-get
// @Produce json
// @Param id path string true "User ID"
// @Success 200 {object} api.User
// @Failure 404 {object} api.Error
// @Security ApiKeyAuth
// @Router /api/v3/users/{id} [get]
func (h *UsersHandler) Get(c echo.Context) error {
	id := util.PathParam(c, "id")

	u, ok := h.registry.Get(id)
	if !ok {
		return api.Err(http.StatusNotFound, "Unknown user ID", "%s", id)
	}

	user := api.User{}
	user.Unmarshal(&u)

	return c.JSON(http.StatusOK, user)
}

// Create adds a new named user
// @Summary Add a new named user
// @Tags v16.7.2
// @ID users-3-create
// @Accept json
// @Produce json
// @Param user body api.UserCreate true "User"
// @Success 200 {object} api.User
// @Failure 400 {object} api.Error
// @Security ApiKeyAuth
// @Router /api/v3/users [post]
func (h *UsersHandler) Create(c echo.Context) error {
	var body api.UserCreate

	if err := util.ShouldBindJSON(c, &body); err != nil {
		return api.Err(http.StatusBadRequest, "Invalid JSON", "%s", err)
	}

	if len(body.Username) == 0 || len(body.Password) == 0 {
		return api.Err(http.StatusBadRequest, "Invalid user", "username and password must not be empty")
	}

	role := users.RoleUser
	if body.Role == string(users.RoleAdmin) {
		role = users.RoleAdmin
	}

	maxProcesses := body.MaxProcesses
	if maxProcesses <= 0 {
		maxProcesses = -1 // let the registry apply its default
	}

	u, err := h.registry.Create(body.Username, body.Password, role, maxProcesses)
	if err != nil {
		return api.Err(http.StatusBadRequest, "Can't create user", "%s", err.Error())
	}

	user := api.User{}
	user.Unmarshal(&u)

	return c.JSON(http.StatusOK, user)
}

// Update changes an existing named user's role, quota, and optionally password
// @Summary Update a named user
// @Tags v16.7.2
// @ID users-3-update
// @Accept json
// @Produce json
// @Param id path string true "User ID"
// @Param user body api.UserUpdate true "User"
// @Success 200 {object} api.User
// @Failure 400 {object} api.Error
// @Failure 404 {object} api.Error
// @Security ApiKeyAuth
// @Router /api/v3/users/{id} [put]
func (h *UsersHandler) Update(c echo.Context) error {
	id := util.PathParam(c, "id")

	current, ok := h.registry.Get(id)
	if !ok {
		return api.Err(http.StatusNotFound, "Unknown user ID", "%s", id)
	}

	body := api.UserUpdate{
		Role:         string(current.Role),
		MaxProcesses: current.MaxProcesses,
	}

	if err := util.ShouldBindJSON(c, &body); err != nil {
		return api.Err(http.StatusBadRequest, "Invalid JSON", "%s", err)
	}

	role := users.RoleUser
	if body.Role == string(users.RoleAdmin) {
		role = users.RoleAdmin
	}

	u, err := h.registry.Update(id, role, body.MaxProcesses, body.Password)
	if err != nil {
		return api.Err(http.StatusBadRequest, "Can't update user", "%s", err.Error())
	}

	user := api.User{}
	user.Unmarshal(&u)

	return c.JSON(http.StatusOK, user)
}

// Delete removes a named user
// @Summary Delete a named user by their ID
// @Tags v16.7.2
// @ID users-3-delete
// @Produce json
// @Param id path string true "User ID"
// @Success 200 {string} string
// @Failure 404 {object} api.Error
// @Security ApiKeyAuth
// @Router /api/v3/users/{id} [delete]
func (h *UsersHandler) Delete(c echo.Context) error {
	id := util.PathParam(c, "id")

	if err := h.registry.Delete(id); err != nil {
		return api.Err(http.StatusNotFound, "Unknown user ID", "%s", err)
	}

	return c.JSON(http.StatusOK, "OK")
}
