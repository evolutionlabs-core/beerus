package server

import (
	"context"
	"github.com/evolutionlabs-core/beerus/internal/control"
	"net/http"

	"github.com/evolutionlabs-core/beerus/internal/web"
)

func (s *Banking) Login(ctx context.Context, bankID string, req control.LoginRequest) (control.LoginResult, error) {
	bk, err := s.bankBook(bankID)
	if err != nil {
		return control.LoginResult{}, err
	}
	c, err := bk.Bank.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		return control.LoginResult{}, err
	}
	return control.LoginResult{Customer: c.ID}, nil
}

func (s *Banking) HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req control.LoginRequest
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
