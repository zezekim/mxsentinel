package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	dnsx "github.com/zezekim/mxsentinel/internal/dns"
	pgstore "github.com/zezekim/mxsentinel/internal/store/postgres"
)

// ---- response shape --------------------------------------------------------

type dkimPlanJSON struct {
	ID           string  `json:"id"`
	DomainID     string  `json:"domain_id"`
	SelectorOld  string  `json:"selector_old"`
	SelectorNew  string  `json:"selector_new"`
	PublicKeyNew string  `json:"public_key_new"`
	KeyBits      int     `json:"key_bits"`
	Stage        string  `json:"stage"`
	DNSVerified  bool    `json:"dns_verified"`
	TestPassed   bool    `json:"test_passed"`
	DNSRecord    string  `json:"dns_record"`
	CreatedAt    string  `json:"created_at"`
	ActivatedAt  *string `json:"activated_at"`
	RetiredAt    *string `json:"retired_at"`
}

func dkimPlanToJSON(p pgstore.DKIMRotationPlan) dkimPlanJSON {
	var activatedAt, retiredAt *string
	if p.ActivatedAt != nil {
		s := p.ActivatedAt.UTC().Format(time.RFC3339)
		activatedAt = &s
	}
	if p.RetiredAt != nil {
		s := p.RetiredAt.UTC().Format(time.RFC3339)
		retiredAt = &s
	}
	return dkimPlanJSON{
		ID:           p.ID,
		DomainID:     p.DomainID,
		SelectorOld:  p.SelectorOld,
		SelectorNew:  p.SelectorNew,
		PublicKeyNew: p.PublicKeyNew,
		KeyBits:      p.KeyBits,
		Stage:        p.Stage,
		DNSVerified:  p.DNSVerified,
		TestPassed:   p.TestPassed,
		DNSRecord:    fmt.Sprintf("v=DKIM1; k=rsa; p=%s", p.PublicKeyNew),
		CreatedAt:    p.CreatedAt.UTC().Format(time.RFC3339),
		ActivatedAt:  activatedAt,
		RetiredAt:    retiredAt,
	}
}

// ---- handlers --------------------------------------------------------------

// GET /v1/domains/{id}/dkim/plans
func (s *Server) handleListDKIMPlans(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	domainID := r.PathValue("id")

	plans, err := s.pg.ListDKIMPlans(r.Context(), tenantID, domainID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	out := make([]dkimPlanJSON, 0, len(plans))
	for _, p := range plans {
		out = append(out, dkimPlanToJSON(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /v1/domains/{id}/dkim/plans
func (s *Server) handleCreateDKIMPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	domainID := r.PathValue("id")

	var body struct {
		Selector string `json:"selector"`
		KeyBits  int    `json:"key_bits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	keyBits := body.KeyBits
	if keyBits == 0 {
		keyBits = 2048
	}
	if keyBits < 1024 {
		keyBits = 1024
	}
	if keyBits > 4096 {
		keyBits = 4096
	}

	kp, err := dnsx.GenerateDKIMKeyPair(keyBits, body.Selector)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "keygen_failed", err.Error())
		return
	}

	id, err := s.pg.CreateDKIMPlan(r.Context(), tenantID, domainID, kp.Selector, kp.PublicKey, kp.PrivateKey, kp.KeyBits)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	plan, err := s.pg.GetDKIMPlan(r.Context(), tenantID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, dkimPlanToJSON(plan))
}

// PATCH /v1/domains/{id}/dkim/plans/{planID}
func (s *Server) handleUpdateDKIMPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	planID := r.PathValue("planID")

	var body struct {
		Stage       *string `json:"stage"`
		DNSVerified *bool   `json:"dns_verified"`
		TestPassed  *bool   `json:"test_passed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
		return
	}

	if body.Stage != nil {
		found, err := s.pg.UpdateDKIMStage(r.Context(), tenantID, planID, *body.Stage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found", "plan not found")
			return
		}
	}

	if body.DNSVerified != nil || body.TestPassed != nil {
		plan, err := s.pg.GetDKIMPlan(r.Context(), tenantID, planID)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found", "plan not found")
			return
		}
		dnsVerified := plan.DNSVerified
		testPassed := plan.TestPassed
		if body.DNSVerified != nil {
			dnsVerified = *body.DNSVerified
		}
		if body.TestPassed != nil {
			testPassed = *body.TestPassed
		}
		found, err := s.pg.SetDKIMVerified(r.Context(), tenantID, planID, dnsVerified, testPassed)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "not_found", "plan not found")
			return
		}
	}

	plan, err := s.pg.GetDKIMPlan(r.Context(), tenantID, planID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}

	writeJSON(w, http.StatusOK, dkimPlanToJSON(plan))
}

// DELETE /v1/domains/{id}/dkim/plans/{planID}
func (s *Server) handleDeleteDKIMPlan(w http.ResponseWriter, r *http.Request) {
	tenantID := s.tenant(r)
	planID := r.PathValue("planID")

	found, err := s.pg.DeleteDKIMPlan(r.Context(), tenantID, planID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "plan not found")
		return
	}

	writeJSON(w, http.StatusNoContent, nil)
}
