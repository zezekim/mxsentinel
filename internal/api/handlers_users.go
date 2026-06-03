package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zezekim/mxsentinel/internal/auth"
)

type userJSON struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// handleListUsers lists the tenant's users (admin scope).
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.pg.ListUsers(r.Context(), s.tenant(r))
	if err != nil {
		s.log.Error("list users", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "failed to list users")
		return
	}
	items := make([]userJSON, 0, len(users))
	for _, u := range users {
		items = append(items, userJSON{ID: u.ID, Email: u.Email, Role: u.Role, Status: u.Status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": items})
}

type createUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleCreateUser creates a user in the caller's tenant (admin scope).
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
		return
	}
	role := req.Role
	if role == "" {
		role = "viewer"
	}
	if !validRole(role) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid role (owner|admin|operator|viewer)")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not hash password")
		return
	}
	id, err := s.pg.CreateUser(r.Context(), s.tenant(r), req.Email, hash, role)
	if err != nil {
		s.log.Error("create user", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create user (email may already exist)")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "email": req.Email, "role": role})
}

func validRole(role string) bool {
	switch role {
	case "owner", "admin", "operator", "viewer":
		return true
	default:
		return false
	}
}
