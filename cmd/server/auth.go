package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"alphathesis/store"
)

// ------------------------------------------------------------
// Context key
// ------------------------------------------------------------

type contextKey string

const ctxUserID contextKey = "userID"

func userIDFromContext(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(ctxUserID).(int64)
	return v, ok
}

// ------------------------------------------------------------
// Session store (in-memory)
// ------------------------------------------------------------

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]int64 // token → userID
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]int64)}
}

func (s *sessionStore) create(userID int64) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = userID
	s.mu.Unlock()
	return token, nil
}

func (s *sessionStore) lookup(token string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.sessions[token]
	return id, ok
}

func (s *sessionStore) delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// ------------------------------------------------------------
// Middleware
// ------------------------------------------------------------

func (s *srv) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			jsonErr(w, errors.New("unauthorized"), http.StatusUnauthorized)
			return
		}
		userID, ok := s.sessions.lookup(token)
		if !ok {
			jsonErr(w, errors.New("unauthorized"), http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

// ------------------------------------------------------------
// Auth handlers
// ------------------------------------------------------------

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type authResponse struct {
	Token string      `json:"token"`
	User  *store.User `json:"user"`
}

func (s *srv) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, errors.New("invalid request body"), http.StatusBadRequest)
		return
	}
	if req.Email == "" || req.Password == "" {
		jsonErr(w, errors.New("email and password are required"), http.StatusBadRequest)
		return
	}

	existing, err := s.thesisStore.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		jsonErr(w, errors.New("database error"), http.StatusInternalServerError)
		return
	}
	if existing != nil {
		jsonErr(w, errors.New("email already registered"), http.StatusConflict)
		return
	}

	name := req.Name
	if name == "" {
		name = req.Email
	}
	user, err := s.thesisStore.CreateUser(r.Context(), req.Email, name, req.Password)
	if err != nil {
		jsonErr(w, errors.New("failed to create user"), http.StatusInternalServerError)
		return
	}

	token, err := s.sessions.create(user.ID)
	if err != nil {
		jsonErr(w, errors.New("failed to create session"), http.StatusInternalServerError)
		return
	}

	jsonOK(w, authResponse{Token: token, User: user})
}

func (s *srv) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, errors.New("invalid request body"), http.StatusBadRequest)
		return
	}

	user, err := s.thesisStore.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		jsonErr(w, errors.New("database error"), http.StatusInternalServerError)
		return
	}
	if user == nil || user.Password != req.Password {
		jsonErr(w, errors.New("invalid email or password"), http.StatusUnauthorized)
		return
	}

	token, err := s.sessions.create(user.ID)
	if err != nil {
		jsonErr(w, errors.New("failed to create session"), http.StatusInternalServerError)
		return
	}

	jsonOK(w, authResponse{Token: token, User: user})
}

func (s *srv) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token != "" {
		s.sessions.delete(token)
	}
	jsonOK(w, map[string]string{"status": "ok"})
}

func (s *srv) handleMe(w http.ResponseWriter, r *http.Request) {
	userID, _ := userIDFromContext(r.Context())
	user, err := s.thesisStore.GetUser(r.Context(), userID)
	if err != nil || user == nil {
		jsonErr(w, errors.New("user not found"), http.StatusNotFound)
		return
	}
	jsonOK(w, user)
}
