package fake

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) graphqlRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/graphql", s.graphql)
}

// graphql answers the capability query the driver sends. It builds `group`
// from the fields the query names.
//
// GitLab reports a gated field as null next to a field-level error, and the
// Epic work-item type is absent from Group.workItemTypes where epics are not
// licensed (both seen on a Free group).
func (s *Server) graphql(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, object{"errors": []object{{"message": "bad request"}}})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.Contains(req.Query, "query MRDetails") {
		s.mrDetails(w, req.Variables)
		return
	}
	if strings.Contains(req.Query, "issueSetIteration") {
		s.setIterationMutation(w, req.Variables)
		return
	}
	if strings.Contains(req.Query, "issueSetWeight") {
		s.setWeightMutation(w, req.Variables)
		return
	}
	path, _ := req.Variables["path"].(string)
	if !s.st.hasGroup(path) {
		writeJSON(w, http.StatusOK, object{"data": object{"group": nil}})
		return
	}
	group := object{}
	var errs []object
	if strings.Contains(req.Query, "iterationCadences") {
		if s.tier == Premium {
			nodes := []object{{"id": "gid://gitlab/Iterations::Cadence/1"}}
			if strings.Contains(req.Query, "durationInWeeks") {
				nodes = s.cadenceNodes(true)
			}
			group["iterationCadences"] = object{"nodes": nodes}
		} else {
			group["iterationCadences"] = nil
			errs = append(errs, object{
				"message": "The resource that you are attempting to access does not exist or you don't have permission to perform this action",
				"path":    []string{"group", "iterationCadences"},
			})
		}
	}
	if strings.Contains(req.Query, "workItemTypes") {
		nodes := []object{}
		if s.tier == Premium {
			nodes = append(nodes, object{"name": "Epic"})
		}
		group["workItemTypes"] = object{"nodes": nodes}
	}
	res := object{"data": object{"group": group}}
	if len(errs) > 0 {
		res["errors"] = errs
	}
	writeJSON(w, http.StatusOK, res)
}
