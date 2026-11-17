package server

import "slices"

func (s *Server) SetProblems(list []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.problems = slices.Clone(list)
}
