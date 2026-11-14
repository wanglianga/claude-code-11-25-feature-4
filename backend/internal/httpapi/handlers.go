package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"recycletrace/backend/internal/app"
	"recycletrace/backend/internal/num"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now().UTC()})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	token, u, err := s.App.Login(r.Context(), in.Username, in.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errBody{Error: "用户名或密码错误", Kind: "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": u})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, u app.User) {
	_ = s.App.Logout(r.Context(), bearer(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u app.User) {
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleSuppliers(w http.ResponseWriter, r *http.Request, u app.User) {
	list, err := s.App.ListSuppliers(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePiles(w http.ResponseWriter, r *http.Request, u app.User) {
	list, err := s.App.ListPiles(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleContracts(w http.ResponseWriter, r *http.Request, u app.User) {
	list, err := s.App.ListContracts(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleFormulas(w http.ResponseWriter, r *http.Request, u app.User) {
	out := []num.Formula{}
	for _, f := range num.Formulas {
		out = append(out, f)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTrips(w http.ResponseWriter, r *http.Request, u app.User) {
	list, err := s.App.ListTrips(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateTrip(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.CreateTripInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	t, err := s.App.CreateTrip(r.Context(), u, in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func pathID(r *http.Request, name string) int64 {
	v := r.PathValue(name)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Server) handleGetTrip(w http.ResponseWriter, r *http.Request, u app.User) {
	id := pathID(r, "id")
	d, err := s.App.TripDetail(r.Context(), id)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleAddWeighing(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.Weighing
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.AddWeighing(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		GrossWeighingID int64  `json:"gross_weighing_id"`
		TareWeighingID  int64  `json:"tare_weighing_id"`
		BasisNote       string `json:"basis_note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.AdoptWeights(r.Context(), u, pathID(r, "id"), in.GrossWeighingID, in.TareWeighingID, in.BasisNote)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleCreateSample(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.CreateSampleInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.CreateSample(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleApproveGrade(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		GradeCode string `json:"grade_code"`
		Isolated  bool   `json:"isolated"`
		Note      string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.ApproveGrade(r.Context(), u, pathID(r, "id"), in.GradeCode, in.Isolated, in.Note)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleIntake(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		PileID     int64  `json:"pile_id"`
		OccurredAt string `json:"occurred_at"`
		Note       string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.Intake(r.Context(), u, app.IntakeInput{TripID: pathID(r, "id"), PileID: in.PileID,
		OccurredAt: in.OccurredAt, Note: in.Note})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleAddAssay(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.Assay
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.AddAssay(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleAdoptAssay(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		Note string `json:"note"`
	}
	_ = decode(r, &in)
	out, err := s.App.AdoptAssay(r.Context(), u, pathID(r, "id"), in.Note)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRejectAssay(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		Note string `json:"note"`
	}
	_ = decode(r, &in)
	out, err := s.App.RejectAssay(r.Context(), u, pathID(r, "id"), in.Note)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStock(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.StockByPile(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetLot(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.GetLot(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.Trace(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.LotTimeline(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.TransferInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.Transfer(r.Context(), u, in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleTransferPreview(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.TransferInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.PreviewTransfer(r.Context(), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMovements(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.ListMovements(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMovementByRef(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.GetMovementByRef(r.Context(), r.PathValue("ref"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePayables(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.ListPayables(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetPayable(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.GetPayable(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePayablePreview(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.PreviewPayable(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleComputePayable(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		Note string `json:"note"`
	}
	_ = decode(r, &in)
	out, err := s.App.ComputePayable(r.Context(), u, pathID(r, "id"), in.Note)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handlePayableVersions(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.PayableVersionsForTrip(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleApprovePayable(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		Note string `json:"note"`
	}
	_ = decode(r, &in)
	out, err := s.App.ApprovePayable(r.Context(), u, pathID(r, "id"), in.Note)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMarkPaid(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.MarkPaid(r.Context(), u, pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.Reconcile(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAffected(w http.ResponseWriter, r *http.Request, u app.User) {
	direct, down, err := s.App.TripsAffectedByTrip(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"direct": direct, "downstream": down})
}

func (s *Server) handlePendingIntake(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.PendingIntake(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReassay(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.ApplyReassay(r.Context(), u, pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleIngestScale(w http.ResponseWriter, r *http.Request, u app.User) {
	var in []app.IngestScaleRecord
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.IngestScale(r.Context(), u, in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleIngestAssays(w http.ResponseWriter, r *http.Request, u app.User) {
	var in []app.IngestAssayRecord
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.IngestAssays(r.Context(), u, in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleIngestTransfers(w http.ResponseWriter, r *http.Request, u app.User) {
	// Accept either a single transfer object or an array of them.
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取失败: "+err.Error())
		return
	}
	var in []app.TransferInput
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var one app.TransferInput
		if err := json.Unmarshal(raw, &one); err != nil {
			writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
			return
		}
		in = []app.TransferInput{one}
	} else if err := json.Unmarshal(raw, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	type item struct {
		Index   int    `json:"index"`
		Ref     string `json:"ref"`
		Outcome string `json:"outcome"`
		ID      int64  `json:"id,omitempty"`
		Reason  string `json:"reason,omitempty"`
	}
	out := []item{}
	for i, tr := range in {
		mv, err := s.App.Transfer(r.Context(), u, tr)
		if err != nil {
			out = append(out, item{Index: i, Ref: tr.ExternalRef, Outcome: "rejected", Reason: err.Error()})
			continue
		}
		out = append(out, item{Index: i, Ref: mv.ExternalRef, Outcome: "accepted", ID: mv.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ------------------------------------------------------------------ 留样与复核

func (s *Server) handleRegisterUnit(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.RegisterUnitInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.RegisterSealedUnit(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleListSampleUnits(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.ListSampleUnits(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetSampleUnit(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.GetSampleUnit(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSplitUnit(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.SplitUnitInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.SplitSampleUnit(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleBreakSeal(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.BreakSealInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.BreakSeal(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReviewCases(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.ListReviewCases(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOpenReviewCase(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.OpenCaseInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.OpenReviewCase(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleGetReviewCase(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.GetReviewCase(r.Context(), u, pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddPartyReport(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.PartyReportInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.AddPartyReport(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleDrawSample(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		UnitID    int64  `json:"unit_id"`
		WeightG   int64  `json:"weight_g"`
		BlindCode string `json:"blind_code"`
		LabName   string `json:"lab_name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.DrawSampleUnit(r.Context(), u, pathID(r, "id"), in.UnitID,
		app.DrawInput{WeightG: in.WeightG, BlindCode: in.BlindCode, LabName: in.LabName})
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleExternalReturn(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.ExternalReturnInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.ReturnExternalReport(r.Context(), u, in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRevealExternal(w http.ResponseWriter, r *http.Request, u app.User) {
	var in struct {
		ExternalReportID int64  `json:"external_report_id"`
		Note             string `json:"note"`
	}
	_ = decode(r, &in)
	out, err := s.App.RevealExternalReport(r.Context(), u, in.ExternalReportID, in.Note)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTrialReview(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.TrialReview(r.Context(), pathID(r, "id"))
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdjudicate(w http.ResponseWriter, r *http.Request, u app.User) {
	var in app.AdjudicateInput
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return
	}
	out, err := s.App.Adjudicate(r.Context(), u, pathID(r, "id"), in)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRecoveries(w http.ResponseWriter, r *http.Request, u app.User) {
	out, err := s.App.ListRecoveries(r.Context())
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
