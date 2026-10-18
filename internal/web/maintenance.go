package web

import (
	"context"
	"net/http"

	"pandadays/internal/i18n"
)

type requestLease struct {
	released bool // protected by Server.gate
}

type leaseKey struct{}

func (s *Server) release(lease *requestLease) {
	if !lease.released {
		lease.released = true
		s.active--
		if s.maintenance && s.active == 0 {
			close(s.drained)
		}
	}
}

func (s *Server) maintenanceResponse(w http.ResponseWriter, r *http.Request, lang string) {
	if preview := i18n.Normalize(r.URL.Query().Get("lang")); preview != "" {
		lang = preview
	} else {
		lang = i18n.Resolve("", lang, r.Header.Get("Accept-Language"))
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "10")
	w.Header().Add("Vary", "Accept-Language")
	http.Error(w, s.i18n.T(lang, "backup.maintenance"), http.StatusServiceUnavailable)
}

func (s *Server) maintenanceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.gate.Lock()
		if s.maintenance {
			lang := s.maintenanceLang
			s.gate.Unlock()
			s.maintenanceResponse(w, r, lang)
			return
		}
		s.active++
		lease := &requestLease{}
		s.gate.Unlock()
		defer func() {
			s.gate.Lock()
			s.release(lease)
			s.gate.Unlock()
		}()
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), leaseKey{}, lease)))
	})
}

// beginMaintenance relinquishes this request's lease before waiting, so two
// concurrent admin operations cannot deadlock while draining one another.
func (s *Server) beginMaintenance(w http.ResponseWriter, r *http.Request) bool {
	lang := s.siteLang(r.Context())
	s.gate.Lock()
	if s.maintenance {
		lang = s.maintenanceLang
		s.gate.Unlock()
		s.maintenanceResponse(w, r, lang)
		return false
	}
	s.maintenance, s.maintenanceLang = true, lang
	s.drained = make(chan struct{})
	s.release(r.Context().Value(leaseKey{}).(*requestLease))
	drained := s.drained
	s.gate.Unlock()
	select {
	case <-drained:
		return true
	case <-r.Context().Done():
		defer s.endMaintenance()
		s.authError(w, r, "backup.error.failed", http.StatusRequestTimeout)
		return false
	}
}

func (s *Server) endMaintenance() {
	s.gate.Lock()
	s.maintenance = false
	s.gate.Unlock()
}
