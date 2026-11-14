package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"recycletrace/backend/internal/app"
)

type Server struct {
	App    *app.App
	Static http.Handler
	Mux    *http.ServeMux
}

func New(a *app.App, static http.Handler) *Server {
	s := &Server{App: a, Static: static, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.withCORS(s.Mux)
}

func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------ routing

func (s *Server) routes() {
	m := s.Mux
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/logout", s.authed(s.handleLogout, ""))
	m.HandleFunc("GET /api/me", s.authed(s.handleMe, ""))

	m.HandleFunc("GET /api/suppliers", s.authed(s.handleSuppliers, ""))
	m.HandleFunc("GET /api/piles", s.authed(s.handlePiles, ""))
	m.HandleFunc("GET /api/contracts", s.authed(s.handleContracts, ""))
	m.HandleFunc("GET /api/formulas", s.authed(s.handleFormulas, ""))

	m.HandleFunc("GET /api/trips", s.authed(s.handleTrips, ""))
	m.HandleFunc("POST /api/trips", s.authed(s.handleCreateTrip, "purchasing,admin"))
	m.HandleFunc("GET /api/trips/{id}", s.authed(s.handleGetTrip, ""))
	m.HandleFunc("POST /api/trips/{id}/weighings", s.authed(s.handleAddWeighing, "yard,purchasing,admin"))
	m.HandleFunc("POST /api/trips/{id}/adopt", s.authed(s.handleAdopt, "yard,purchasing,admin"))
	m.HandleFunc("POST /api/trips/{id}/samples", s.authed(s.handleCreateSample, "lab,yard,admin"))
	m.HandleFunc("POST /api/trips/{id}/grade", s.authed(s.handleApproveGrade, "quality,admin"))
	m.HandleFunc("POST /api/trips/{id}/intake", s.authed(s.handleIntake, "yard,purchasing,admin"))
	m.HandleFunc("GET /api/trips/{id}/payable/preview", s.authed(s.handlePayablePreview, ""))
	m.HandleFunc("POST /api/trips/{id}/payable", s.authed(s.handleComputePayable, "finance,purchasing,admin"))
	m.HandleFunc("GET /api/trips/{id}/payable/versions", s.authed(s.handlePayableVersions, ""))
	m.HandleFunc("GET /api/trips/{id}/affected", s.authed(s.handleAffected, ""))
	m.HandleFunc("POST /api/trips/{id}/reassay", s.authed(s.handleReassay, "quality,finance,admin"))

	m.HandleFunc("POST /api/assays/{id}/adopt", s.authed(s.handleAdoptAssay, "quality,admin"))
	m.HandleFunc("POST /api/assays/{id}/reject", s.authed(s.handleRejectAssay, "quality,admin"))
	m.HandleFunc("POST /api/samples/{id}/assays", s.authed(s.handleAddAssay, "lab,admin"))

	// 留样、封签流转与复核裁定
	m.HandleFunc("POST /api/samples/{id}/units", s.authed(s.handleRegisterUnit, "lab,yard,admin"))
	m.HandleFunc("GET /api/trips/{id}/sample-units", s.authed(s.handleListSampleUnits, ""))
	m.HandleFunc("GET /api/sample-units/{id}", s.authed(s.handleGetSampleUnit, ""))
	m.HandleFunc("POST /api/sample-units/{id}/split", s.authed(s.handleSplitUnit, "lab,yard,admin"))
	m.HandleFunc("POST /api/sample-units/{id}/break-seal", s.authed(s.handleBreakSeal, "lab,yard,quality,admin"))

	m.HandleFunc("GET /api/review-cases", s.authed(s.handleReviewCases, ""))
	m.HandleFunc("POST /api/trips/{id}/review-cases", s.authed(s.handleOpenReviewCase, "quality,lab,admin"))
	m.HandleFunc("GET /api/review-cases/{id}", s.authed(s.handleGetReviewCase, ""))
	m.HandleFunc("POST /api/review-cases/{id}/party-reports", s.authed(s.handleAddPartyReport, "quality,lab,purchasing,admin"))
	m.HandleFunc("POST /api/review-cases/{id}/draws", s.authed(s.handleDrawSample, "lab,yard,quality,admin"))
	m.HandleFunc("POST /api/review-cases/{id}/external-return", s.authed(s.handleExternalReturn, "lab,quality,admin"))
	m.HandleFunc("POST /api/review-cases/{id}/reveal", s.authed(s.handleRevealExternal, "quality,admin"))
	m.HandleFunc("GET /api/review-cases/{id}/trial", s.authed(s.handleTrialReview, ""))
	m.HandleFunc("POST /api/review-cases/{id}/adjudicate", s.authed(s.handleAdjudicate, "quality,admin"))
	m.HandleFunc("GET /api/payment-recoveries", s.authed(s.handleRecoveries, ""))

	m.HandleFunc("GET /api/stock", s.authed(s.handleStock, ""))
	m.HandleFunc("GET /api/lots/{id}", s.authed(s.handleGetLot, ""))
	m.HandleFunc("GET /api/lots/{id}/trace", s.authed(s.handleTrace, ""))
	m.HandleFunc("GET /api/lots/{id}/timeline", s.authed(s.handleTimeline, ""))
	m.HandleFunc("POST /api/transfers", s.authed(s.handleTransfer, "yard,admin"))
	m.HandleFunc("GET /api/movements", s.authed(s.handleMovements, ""))
	m.HandleFunc("GET /api/movements/{ref}", s.authed(s.handleMovementByRef, ""))
	m.HandleFunc("POST /api/transfers/preview", s.authed(s.handleTransferPreview, "yard,admin"))

	m.HandleFunc("GET /api/payables", s.authed(s.handlePayables, ""))
	m.HandleFunc("GET /api/payables/{id}", s.authed(s.handleGetPayable, ""))
	m.HandleFunc("POST /api/payables/{id}/approve", s.authed(s.handleApprovePayable, "finance,admin"))
	m.HandleFunc("POST /api/payables/{id}/pay", s.authed(s.handleMarkPaid, "finance,admin"))

	m.HandleFunc("GET /api/reconciliation", s.authed(s.handleReconcile, ""))

	m.HandleFunc("GET /api/intake", s.authed(s.handlePendingIntake, ""))
	m.HandleFunc("POST /api/ingest/scale", s.authed(s.handleIngestScale, "yard,purchasing,admin"))
	m.HandleFunc("POST /api/ingest/assays", s.authed(s.handleIngestAssays, "lab,admin"))
	m.HandleFunc("POST /api/ingest/transfers", s.authed(s.handleIngestTransfers, "yard,admin"))

	// Static SPA
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusNotFound, "unknown endpoint")
			return
		}
		if s.Static == nil {
			writeErr(w, http.StatusNotFound, "frontend not built")
			return
		}
		s.Static.ServeHTTP(w, r)
	})
}

// ------------------------------------------------------------------ plumbing

type ctxKey string

const userKey ctxKey = "user"

type handlerFunc func(http.ResponseWriter, *http.Request, app.User)

func (s *Server) authed(fn handlerFunc, roles string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		u, err := s.App.UserByToken(r.Context(), token)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if roles != "" {
			allowed := false
			for _, role := range strings.Split(roles, ",") {
				if u.Role == role {
					allowed = true
					break
				}
			}
			if !allowed {
				writeErr(w, http.StatusForbidden, "当前角色无权执行此操作")
				return
			}
		}
		fn(w, r, u)
	}
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type errBody struct {
	Error string `json:"error"`
	Kind  string `json:"kind,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errBody{Error: msg})
}

func writeAppErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	kind := "internal"
	switch {
	case errors.Is(err, app.ErrValidation):
		status, kind = http.StatusBadRequest, "validation"
	case errors.Is(err, app.ErrBlocked):
		status, kind = http.StatusConflict, "blocked"
	case errors.Is(err, app.ErrConflict):
		status, kind = http.StatusConflict, "conflict"
	case errors.Is(err, app.ErrNotFound):
		status, kind = http.StatusNotFound, "not_found"
	case errors.Is(err, app.ErrForbidden):
		status, kind = http.StatusForbidden, "forbidden"
	case errors.Is(err, app.ErrUnauthorized):
		status, kind = http.StatusUnauthorized, "unauthorized"
	}
	writeJSON(w, status, errBody{Error: err.Error(), Kind: kind})
}

func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
