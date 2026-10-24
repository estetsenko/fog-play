package api

import "net/http"

type gameResponse struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	IdleHosts   int    `json:"idle_hosts"`
	OnlineHosts int    `json:"online_hosts"`
}

// listGames returns the catalog with live host counts, so players only pick
// games they can actually start now.
func (s *Server) listGames(w http.ResponseWriter, r *http.Request) {
	catalog, err := s.store.Catalog(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range catalog {
		for _, id := range e.HostIDs {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	statuses, err := s.presence.Statuses(r.Context(), ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]gameResponse, 0, len(catalog))
	for _, e := range catalog {
		g := gameResponse{Slug: e.Slug, Title: e.Title}
		for _, id := range e.HostIDs {
			switch statuses[id] {
			case "idle":
				g.IdleHosts++
				g.OnlineHosts++
			case "busy":
				g.OnlineHosts++
			}
		}
		out = append(out, g)
	}
	writeJSON(w, http.StatusOK, out)
}
