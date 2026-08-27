package webserver

import (
	"encoding/json"
	"errors"
	"go-sync-objects/internal/configdb"
	"net/http"
	"strconv"
	"strings"
)

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

// usersAPIHandler serves GET/POST /api/users: listing every account, and
// creating a new one. Both require superuser (enforced by the route-level
// requireSuperuser wrapper in webserver.go), not any of the six feature
// permissions.
func usersAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listUsers(w, r, cfgDB)
		case http.MethodPost:
			createUser(w, r, cfgDB)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
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
		logSecurityEvent(r, cfgDB, "user_created", actor.Username, "target="+user.Username)
	}

	writeJSON(w, http.StatusOK, toUserDTO(*user))
}

// userAPIHandler serves PUT/PATCH/DELETE /api/users/{id}, updating or
// removing one account. It refuses to let the acting superuser demote or
// delete themselves if they're the last remaining superuser, so the app can
// never lock itself out of /users entirely.
func userAPIHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/users/"), 10, 64)
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodPut, http.MethodPatch:
			updateUser(w, r, cfgDB, id)
		case http.MethodDelete:
			deleteUser(w, r, cfgDB, id)
		default:
			w.Header().Set("Allow", http.MethodPut+", "+http.MethodPatch+", "+http.MethodDelete)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
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
		logSecurityEvent(r, cfgDB, "user_updated", actor.Username, "target="+user.Username)
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
		logSecurityEvent(r, cfgDB, "user_deleted", actor.Username, "target="+target.Username)
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
