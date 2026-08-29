package webserver

import (
	"Tile-Server-Sync-GO/internal/configdb"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// userIDFromPath parses the {id} path value native ServeMux routing extracts
// for every /api/users/{id} registration below.
func userIDFromPath(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

const maxUserBodyBytes = 1 << 16

// userDTO is a User as sent to/from the JSON API — never carries a password
// hash, matching configdb.User itself.
type userDTO struct {
	ID          int64                `json:"id"`
	Username    string               `json:"username"`
	IsSuperuser bool                 `json:"isSuperuser"`
	Permissions configdb.Permissions `json:"permissions"`
}

func toUserDTO(u configdb.User) userDTO {
	return userDTO{ID: u.ID, Username: u.Username, IsSuperuser: u.IsSuperuser, Permissions: u.Permissions}
}

// listUsersAPIHandler serves GET /api/users: listing every account.
// Requires superuser (enforced by the route-level requireSuperuser wrapper
// in webserver.go), not any of the seven feature permissions.
func listUsersAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { listUsers(w, r, cfgDB) }
}

// createUserAPIHandler serves POST /api/users: creating a new account.
// Requires superuser.
func createUserAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { createUser(w, r, cfgDB) }
}

func listUsers(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store) {
	users, err := cfgDB.ListUsers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
		return
	}

	dtos := make([]userDTO, len(users))
	for i, u := range users {
		dtos[i] = toUserDTO(u)
	}

	writeJSON(w, http.StatusOK, dtos)
}

type createUserRequest struct {
	Username    string               `json:"username"`
	Password    string               `json:"password"`
	IsSuperuser bool                 `json:"isSuperuser"`
	Permissions configdb.Permissions `json:"permissions"`
}

func createUser(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store) {
	var req createUserRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorJSON("decode request: "+err.Error()))
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorJSON("username and password are required"))
		return
	}

	user, err := cfgDB.CreateUser(r.Context(), req.Username, req.Password, req.Permissions, req.IsSuperuser)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configdb.ErrUsernameTaken) {
			status = http.StatusConflict
		}

		writeJSON(w, status, errorJSON(err.Error()))

		return
	}

	if actor, ok := currentUser(r.Context()); ok {
		detail := fmt.Sprintf("target=%s; isSuperuser=%v; permissions=%s",
			user.Username, user.IsSuperuser, strings.Join(grantedPermissions(user.Permissions), ","))
		logSecurityEvent(r, cfgDB, "user_created", actor.Username, detail)
	}

	writeJSON(w, http.StatusOK, toUserDTO(*user))
}

// getUserAPIHandler serves GET /api/users/{id}: one account. Requires
// superuser.
func getUserAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := userIDFromPath(r)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}

		getUser(w, r, cfgDB, id)
	}
}

func getUser(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, id int64) {
	user, err := cfgDB.GetUser(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configdb.ErrUserNotFound) {
			status = http.StatusNotFound
		}

		writeJSON(w, status, errorJSON(err.Error()))

		return
	}

	writeJSON(w, http.StatusOK, toUserDTO(*user))
}

// updateUserAPIHandler serves PUT/PATCH /api/users/{id}, updating one
// account. It refuses to let the acting superuser demote themselves if
// they're the last remaining superuser, so the app can never lock itself out
// of /users entirely.
func updateUserAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := userIDFromPath(r)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}

		updateUser(w, r, cfgDB, id)
	}
}

// deleteUserAPIHandler serves DELETE /api/users/{id}, removing one account —
// same last-remaining-superuser guard as updateUserAPIHandler.
func deleteUserAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := userIDFromPath(r)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}

		deleteUser(w, r, cfgDB, id)
	}
}

type updateUserRequest struct {
	Password    string               `json:"password"`
	IsSuperuser bool                 `json:"isSuperuser"`
	Permissions configdb.Permissions `json:"permissions"`
}

func updateUser(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, id int64) {
	var req updateUserRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUserBodyBytes)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorJSON("decode request: "+err.Error()))
		return
	}

	if !req.IsSuperuser {
		if err := ensureNotLastSuperuser(r, cfgDB, id); err != nil {
			writeJSON(w, http.StatusConflict, errorJSON(err.Error()))
			return
		}
	}

	before, err := cfgDB.GetUser(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configdb.ErrUserNotFound) {
			status = http.StatusNotFound
		}

		writeJSON(w, status, errorJSON(err.Error()))

		return
	}

	if err := cfgDB.UpdateUser(r.Context(), id, req.Permissions, req.IsSuperuser, req.Password); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configdb.ErrUserNotFound) {
			status = http.StatusNotFound
		}

		writeJSON(w, status, errorJSON(err.Error()))

		return
	}

	user, err := cfgDB.GetUser(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorJSON(err.Error()))
		return
	}

	if actor, ok := currentUser(r.Context()); ok {
		changes := diffPermissions(before.Permissions, user.Permissions)
		if before.IsSuperuser != user.IsSuperuser {
			changes = append(changes, fmt.Sprintf("isSuperuser %v->%v", before.IsSuperuser, user.IsSuperuser))
		}

		if req.Password != "" {
			changes = append(changes, "password changed")
		}

		detail := fmt.Sprintf("target=%s; %s", user.Username, changesDetail(changes))
		logSecurityEvent(r, cfgDB, "user_updated", actor.Username, detail)
	}

	writeJSON(w, http.StatusOK, toUserDTO(*user))
}

func deleteUser(w http.ResponseWriter, r *http.Request, cfgDB *configdb.Store, id int64) {
	if err := ensureNotLastSuperuser(r, cfgDB, id); err != nil {
		writeJSON(w, http.StatusConflict, errorJSON(err.Error()))
		return
	}

	target, err := cfgDB.GetUser(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configdb.ErrUserNotFound) {
			status = http.StatusNotFound
		}

		writeJSON(w, status, errorJSON(err.Error()))

		return
	}

	if err := cfgDB.DeleteUser(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configdb.ErrUserNotFound) {
			status = http.StatusNotFound
		}

		writeJSON(w, status, errorJSON(err.Error()))

		return
	}

	if actor, ok := currentUser(r.Context()); ok {
		detail := fmt.Sprintf("target=%s; isSuperuser=%v; permissions=%s",
			target.Username, target.IsSuperuser, strings.Join(grantedPermissions(target.Permissions), ","))
		logSecurityEvent(r, cfgDB, "user_deleted", actor.Username, detail)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ensureNotLastSuperuser returns an error if id is currently a superuser and
// removing/demoting it would leave zero superusers, since that would lock
// the app out of /users (and out of ever creating another account) for
// good.
func ensureNotLastSuperuser(r *http.Request, cfgDB *configdb.Store, id int64) error {
	target, err := cfgDB.GetUser(r.Context(), id)
	if err != nil {
		return err
	}

	if !target.IsSuperuser {
		return nil
	}

	users, err := cfgDB.ListUsers(r.Context())
	if err != nil {
		return err
	}

	superusers := 0

	for _, u := range users {
		if u.IsSuperuser {
			superusers++
		}
	}

	if superusers <= 1 {
		return errors.New("cannot remove the last remaining superuser")
	}

	return nil
}
