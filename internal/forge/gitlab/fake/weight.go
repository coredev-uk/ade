package fake

import (
	"fmt"
	"net/http"
	"strconv"
)

// setWeightMutation answers issueSetWeight. Premium sets or (with a null
// weight) clears the field. Free answers like a gated field: a null payload
// next to an error, so callers gate on Caps.Weight.
func (s *Server) setWeightMutation(w http.ResponseWriter, vars map[string]any) {
	if s.tier != Premium {
		writeJSON(w, http.StatusOK, object{
			"data": object{"issueSetWeight": nil},
			"errors": []object{{
				"message": "The resource that you are attempting to access does not exist or you don't have permission to perform this action",
				"path":    []string{"issueSetWeight"},
			}},
		})
		return
	}
	fail := func(msg string) {
		writeJSON(w, http.StatusOK, object{"data": object{"issueSetWeight": object{"errors": []string{msg}, "issue": nil}}})
	}
	p := s.st.findProject(fmt.Sprint(vars["projectPath"]))
	iid, _ := strconv.Atoi(fmt.Sprint(vars["iid"]))
	var is *issue
	if p != nil {
		is = s.st.findIssue(p, iid)
	}
	if is == nil {
		fail("The resource that you are attempting to access does not exist or you don't have permission to perform this action")
		return
	}
	switch v := vars["weight"].(type) {
	case nil:
		is.Weight = nil
	case float64:
		if v < 0 || v != float64(int(v)) {
			fail("Weight must be a whole number of 0 or more")
			return
		}
		n := int(v)
		is.Weight = &n
	default:
		fail("Weight is invalid")
		return
	}
	is.Updated = s.st.clock
	writeJSON(w, http.StatusOK, object{"data": object{"issueSetWeight": object{"errors": []string{}, "issue": object{"iid": strconv.Itoa(iid)}}}})
}
