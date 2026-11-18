package server

import (
	"context"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResult struct {
	Customer string `json:"customer_id"`
}

func (s *Banking) Login(ctx context.Context, bankID string, req LoginRequest) (LoginResult, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return LoginResult{}, err
	}
	c, err := bk.Bank.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Customer: c.ID}, nil
}

func (s *Banking) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := web.DecodeBody(r, &req); err != nil {
		web.WriteError(w, r, http.StatusBadRequest, "invalid_request", `Send {"username":"ada","password":"..."}.`)
		return
	}
	res, err := s.Login(r.Context(), s.bankIDOf(r), req)
	if err != nil {
		s.Error(w, r, err)
		return
	}
	web.WriteJSON(w, http.StatusOK, res)
}
