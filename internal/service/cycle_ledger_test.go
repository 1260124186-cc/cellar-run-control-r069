package service

import (
	"testing"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/clock"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/storage"
)

type cycleHarness struct {
	t       *testing.T
	dir     string
	store   *storage.Store
	clock   *clock.Fixed
	service *Service
}

func newCycleHarness(t *testing.T) *cycleHarness {
	t.Helper()
	fixed := clock.NewFixed(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	store, err := storage.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return &cycleHarness{
		t:       t,
		dir:     dir,
		store:   store,
		clock:   fixed,
		service: New(store, fixed),
	}
}

// reopen simulates a controller restart against the same data directory.
func (h *cycleHarness) reopen() {
	h.t.Helper()
	store, err := storage.Open(h.dir)
	if err != nil {
		h.t.Fatalf("reopen store: %v", err)
	}
	h.store = store
	h.service = New(store, h.clock)
}

func (h *cycleHarness) approvedFormula(name string) string {
	h.t.Helper()
	formula, err := h.service.CreateFormula(CreateFormulaInput{
		Name:                  name,
		Style:                 "Cellar pilot",
		TargetOriginalGravity: 1.052,
		TargetFinalGravity:    1.012,
		MaxFermentationTempC:  22,
		MinimumDays:           7,
		MaximumDays:           21,
	})
	if err != nil {
		h.t.Fatalf("create formula: %v", err)
	}
	steps := []domain.FormulaStep{
		{Sequence: 1, Label: "Heat and combine", TemperatureC: 66, HoldMinutes: 60},
		{Sequence: 2, Label: "Transfer and cool", TemperatureC: 20, HoldMinutes: 30},
	}
	if _, err := h.service.SetFormulaSteps(formula.ID,
		SetFormulaStepsInput{ExpectedVersion: ptrCycle(1), Steps: steps}); err != nil {
		h.t.Fatalf("set steps: %v", err)
	}
	if _, err := h.service.ApproveFormula(formula.ID,
		VersionInput{ExpectedVersion: ptrCycle(2)}); err != nil {
		h.t.Fatalf("approve formula: %v", err)
	}
	return formula.ID
}

func (h *cycleHarness) vessel(code string, capacity float64) domain.Vessel {
	h.t.Helper()
	vessel, err := h.service.CreateVessel(CreateVesselInput{
		Code: code, CapacityL: capacity,
	})
	if err != nil {
		h.t.Fatalf("create vessel: %v", err)
	}
	return vessel
}

func (h *cycleHarness) run(code, formulaID string) domain.FermentationRun {
	h.t.Helper()
	created, err := h.service.CreateRun(CreateRunInput{
		Code: code, FormulaID: formulaID, PlannedVolumeL: 180,
	})
	if err != nil {
		h.t.Fatalf("create run: %v", err)
	}
	return created
}

func (h *cycleHarness) reserve(runID, vesselID string, expected int64) (domain.FermentationRun, domain.Vessel) {
	h.t.Helper()
	run, vessel, err := h.service.ReserveRun(runID, ReserveRunInput{
		ExpectedVersion: ptrCycle(expected), VesselID: vesselID,
	})
	if err != nil {
		h.t.Fatalf("reserve run: %v", err)
	}
	return run, vessel
}

func (h *cycleHarness) start(runID string, expected int64) {
	h.t.Helper()
	if _, _, err := h.service.StartRun(runID,
		VersionInput{ExpectedVersion: ptrCycle(expected)}); err != nil {
		h.t.Fatalf("start run: %v", err)
	}
}

func (h *cycleHarness) completeRun(runID string, expected int64) (domain.FermentationRun, domain.Vessel) {
	h.t.Helper()
	for sequence := 1; sequence <= 3; sequence++ {
		h.clock.Advance(time.Hour)
		if _, err := h.service.AppendObservation(runID, ObservationInput{
			Sequence:     sequence,
			Gravity:      1.050 - float64(sequence)*0.012,
			TemperatureC: 20,
			ObservedAt:   h.clock.Now(),
		}); err != nil {
			h.t.Fatalf("append observation %d: %v", sequence, err)
		}
	}
	if _, err := h.service.BeginConditioning(runID,
		VersionInput{ExpectedVersion: ptrCycle(expected + 4)}); err != nil {
		h.t.Fatalf("begin conditioning: %v", err)
	}
	h.clock.Advance(72 * time.Hour)
	run, vessel, err := h.service.CompleteRun(runID,
		VersionInput{ExpectedVersion: ptrCycle(expected + 5)})
	if err != nil {
		h.t.Fatalf("complete run: %v", err)
	}
	return run, vessel
}

func ptrCycle(value int64) *int64 {
	return &value
}

// TestCompletedRunAdvancesCycle drives one run through completion and verifies
// the vessel ledger moves exactly once, while the run keeps its pinned cycle.
func TestCompletedRunAdvancesCycle(t *testing.T) {
	h := newCycleHarness(t)
	formulaID := h.approvedFormula("Completion Only Formula")
	vessel := h.vessel("VSL-COMPLETE", 300)
	run := h.run("RUN-COMPLETE", formulaID)

	reservedRun, reservedVessel := h.reserve(run.ID, vessel.ID, run.Version)
	if reservedRun.CycleIndex != 1 {
		t.Fatalf("reserved run cycle index = %d, want 1", reservedRun.CycleIndex)
	}
	if reservedVessel.CompletedCycles != 0 {
		t.Fatalf("reservation advanced completed cycles to %d, want 0",
			reservedVessel.CompletedCycles)
	}

	h.start(run.ID, reservedRun.Version)
	completedRun, completedVessel := h.completeRun(run.ID, 2)
	if completedVessel.CompletedCycles != 1 {
		t.Fatalf("completed cycles after completion = %d, want 1",
			completedVessel.CompletedCycles)
	}
	if completedRun.CycleIndex != 1 {
		t.Fatalf("completed run cycle index = %d, want 1", completedRun.CycleIndex)
	}
	if completedVessel.State != domain.VesselCleaning {
		t.Fatalf("vessel state = %q, want cleaning", completedVessel.State)
	}
	if completedVessel.LastCycleRunID == nil || *completedVessel.LastCycleRunID != run.ID {
		t.Fatalf("last cycle run = %v, want %q", completedVessel.LastCycleRunID, run.ID)
	}
}

// TestAbortNeverConsumesCycle aborts runs both before and after fermentation
// and verifies the used-round count never moves.
func TestAbortNeverConsumesCycle(t *testing.T) {
	h := newCycleHarness(t)
	formulaID := h.approvedFormula("Abort Formula")
	vessel := h.vessel("VSL-ABORT", 300)

	// Abort before fermentation: the reservation is released, no cycle used.
	before := h.run("RUN-ABORT-BEFORE", formulaID)
	reserved, reservedVessel := h.reserve(before.ID, vessel.ID, before.Version)
	vesselVersion := reservedVessel.Version
	aborted, abortedVessel, err := h.service.AbortRun(before.ID,
		VersionInput{ExpectedVersion: ptrCycle(reserved.Version)})
	if err != nil {
		t.Fatalf("abort before start: %v", err)
	}
	if abortedVessel.CompletedCycles != 0 {
		t.Fatalf("abort before start advanced completed cycles to %d, want 0",
			abortedVessel.CompletedCycles)
	}
	if aborted.CycleIndex != 1 {
		t.Fatalf("aborted run lost its pinned cycle: index = %d, want 1",
			aborted.CycleIndex)
	}
	if abortedVessel.State != domain.VesselAvailable {
		t.Fatalf("vessel state = %q, want available", abortedVessel.State)
	}
	if abortedVessel.Version != vesselVersion+1 {
		t.Fatalf("release must still bump vessel version: got %d, want %d",
			abortedVessel.Version, vesselVersion+1)
	}

	// The released vessel hands the same slot to the next reservation.
	next := h.run("RUN-ABORT-NEXT", formulaID)
	nextRun, _ := h.reserve(next.ID, vessel.ID, next.Version)
	if nextRun.CycleIndex != 1 {
		t.Fatalf("next reservation cycle index = %d, want 1 (abort freed the slot)",
			nextRun.CycleIndex)
	}

	// Abort after fermentation: vessel goes to cleaning, still no cycle used.
	started := h.run("RUN-ABORT-AFTER", formulaID)
	secondVessel := h.vessel("VSL-ABORT-2", 300)
	startedRun, _ := h.reserve(started.ID, secondVessel.ID, started.Version)
	h.start(started.ID, startedRun.Version)
	abortedStarted, cleaningVessel, err := h.service.AbortRun(started.ID,
		VersionInput{ExpectedVersion: ptrCycle(startedRun.Version + 1)})
	if err != nil {
		t.Fatalf("abort after start: %v", err)
	}
	if cleaningVessel.CompletedCycles != 0 {
		t.Fatalf("abort after start advanced completed cycles to %d, want 0",
			cleaningVessel.CompletedCycles)
	}
	if cleaningVessel.State != domain.VesselCleaning {
		t.Fatalf("vessel state = %q, want cleaning", cleaningVessel.State)
	}
	if abortedStarted.CycleIndex != 1 {
		t.Fatalf("aborted started run cycle index = %d, want 1",
			abortedStarted.CycleIndex)
	}
	if _, err := h.service.FinishCleaning(secondVessel.ID,
		FinishCleaningInput{ExpectedVersion: ptrCycle(cleaningVessel.Version)}); err != nil {
		t.Fatalf("finish cleaning after abort: %v", err)
	}
}

// TestCycleLedgerSurvivesRestart verifies the vessel counter and every run's
// pinned cycle remain the same fact after the controller reloads state.
func TestCycleLedgerSurvivesRestart(t *testing.T) {
	h := newCycleHarness(t)
	formulaID := h.approvedFormula("Restart Formula")
	vessel := h.vessel("VSL-RESTART", 300)

	// First reservation aborts before fermentation: cycle pinned but unused.
	aborted := h.run("RUN-RESTART-ABORT", formulaID)
	abortedRun, _ := h.reserve(aborted.ID, vessel.ID, aborted.Version)
	if _, _, err := h.service.AbortRun(aborted.ID,
		VersionInput{ExpectedVersion: ptrCycle(abortedRun.Version)}); err != nil {
		t.Fatalf("abort: %v", err)
	}

	// Second reservation goes the full distance and closes cycle 1.
	first := h.run("RUN-RESTART-FIRST", formulaID)
	firstReserved, _ := h.reserve(first.ID, vessel.ID, first.Version)
	h.start(first.ID, firstReserved.Version)
	_, firstVessel := h.completeRun(first.ID, 2)
	if firstVessel.CompletedCycles != 1 {
		t.Fatalf("completed cycles = %d, want 1", firstVessel.CompletedCycles)
	}
	if _, err := h.service.FinishCleaning(vessel.ID,
		FinishCleaningInput{ExpectedVersion: ptrCycle(firstVessel.Version)}); err != nil {
		t.Fatalf("finish cleaning: %v", err)
	}

	// Third reservation is mid-flight (cycle 2) when the controller restarts.
	second := h.run("RUN-RESTART-SECOND", formulaID)
	secondReserved, secondVessel := h.reserve(second.ID, vessel.ID, second.Version)
	if secondReserved.CycleIndex != 2 {
		t.Fatalf("reserved cycle index = %d, want 2", secondReserved.CycleIndex)
	}
	if secondVessel.CompletedCycles != 1 {
		t.Fatalf("completed cycles after second reservation = %d, want 1",
			secondVessel.CompletedCycles)
	}

	h.reopen()

	reloadedVessel, err := h.service.GetVessel(vessel.ID)
	if err != nil {
		t.Fatalf("get vessel after restart: %v", err)
	}
	if reloadedVessel.CompletedCycles != 1 {
		t.Fatalf("after restart completed cycles = %d, want 1",
			reloadedVessel.CompletedCycles)
	}
	if reloadedVessel.LastCycleRunID == nil || *reloadedVessel.LastCycleRunID != first.ID {
		t.Fatalf("after restart last cycle run = %v, want %q",
			reloadedVessel.LastCycleRunID, first.ID)
	}
	if reloadedVessel.ActiveRunID == nil || *reloadedVessel.ActiveRunID != second.ID {
		t.Fatalf("after restart active run = %v, want %q",
			reloadedVessel.ActiveRunID, second.ID)
	}

	expectations := []struct {
		runID string
		state domain.RunState
		cycle int
	}{
		{aborted.ID, domain.RunAborted, 1},
		{first.ID, domain.RunCompleted, 1},
		{second.ID, domain.RunReserved, 2},
	}
	for _, expectation := range expectations {
		reloadedRun, err := h.service.GetRun(expectation.runID)
		if err != nil {
			t.Fatalf("get run %s after restart: %v", expectation.runID, err)
		}
		if reloadedRun.State != expectation.state {
			t.Fatalf("run %s state after restart = %q, want %q",
				expectation.runID, reloadedRun.State, expectation.state)
		}
		if reloadedRun.CycleIndex != expectation.cycle {
			t.Fatalf("run %s cycle after restart = %d, want %d",
				expectation.runID, reloadedRun.CycleIndex, expectation.cycle)
		}
	}

	// The in-flight reservation can still finish and become cycle 2.
	h.start(second.ID, secondReserved.Version)
	secondCompleted, _ := h.completeRun(second.ID, secondReserved.Version)
	if secondCompleted.CycleIndex != 2 {
		t.Fatalf("second completed run cycle index = %d, want 2",
			secondCompleted.CycleIndex)
	}
	reloadedVessel, _ = h.service.GetVessel(vessel.ID)
	if reloadedVessel.CompletedCycles != 2 {
		t.Fatalf("completed cycles after second completion = %d, want 2",
			reloadedVessel.CompletedCycles)
	}
	if reloadedVessel.LastCycleRunID == nil || *reloadedVessel.LastCycleRunID != second.ID {
		t.Fatalf("last cycle run = %v, want %q", reloadedVessel.LastCycleRunID, second.ID)
	}
}

// TestRejectedWritesDoNotTouchCycleLedger ensures failures leave the counter,
// versions, and state exactly as they were.
func TestRejectedWritesDoNotTouchCycleLedger(t *testing.T) {
	h := newCycleHarness(t)
	formulaID := h.approvedFormula("Rejection Formula")
	vessel := h.vessel("VSL-REJECT", 300)
	run := h.run("RUN-REJECT", formulaID)
	reserved, reservedVessel := h.reserve(run.ID, vessel.ID, run.Version)

	// A second run cannot reserve the occupied vessel.
	other := h.run("RUN-REJECT-OTHER", formulaID)
	if _, _, err := h.service.ReserveRun(other.ID, ReserveRunInput{
		VesselID: vessel.ID,
	}); err == nil || !domain.IsCode(err, domain.CodeVesselUnavailable) {
		t.Fatalf("expected vessel_unavailable, got %v", err)
	}
	// A stale version on the same request must be rejected too.
	if _, _, err := h.service.ReserveRun(other.ID, ReserveRunInput{
		ExpectedVersion: ptrCycle(reservedVessel.Version), VesselID: vessel.ID,
	}); err == nil || !domain.IsCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("expected revision_conflict for stale reservation, got %v", err)
	}

	after, err := h.service.GetVessel(vessel.ID)
	if err != nil {
		t.Fatalf("get vessel: %v", err)
	}
	if after.Version != reservedVessel.Version || after.CompletedCycles != 0 ||
		after.State != domain.VesselReserved {
		t.Fatalf("rejected reservation mutated vessel: %+v", after)
	}

	// Completing a merely reserved run (no observations/conditioning) fails
	// without advancing anything.
	if _, _, err := h.service.CompleteRun(run.ID,
		VersionInput{ExpectedVersion: ptrCycle(reserved.Version)}); err == nil ||
		!domain.IsCode(err, domain.CodeRunState) {
		t.Fatalf("expected run_state for early completion, got %v", err)
	}
	after, _ = h.service.GetVessel(vessel.ID)
	if after.CompletedCycles != 0 || after.State != domain.VesselReserved ||
		after.Version != reservedVessel.Version {
		t.Fatalf("rejected completion mutated vessel: %+v", after)
	}
	persistedRun, err := h.service.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if persistedRun.Version != reserved.Version || persistedRun.CycleIndex != 1 ||
		persistedRun.State != domain.RunReserved {
		t.Fatalf("rejected completion mutated run: %+v", persistedRun)
	}

	// Aborting with a stale version is rejected verbatim.
	if _, _, err := h.service.AbortRun(run.ID,
		VersionInput{ExpectedVersion: ptrCycle(reserved.Version - 1)}); err == nil ||
		!domain.IsCode(err, domain.CodeRevisionConflict) {
		t.Fatalf("expected revision_conflict for stale abort, got %v", err)
	}
	after, _ = h.service.GetVessel(vessel.ID)
	if after.CompletedCycles != 0 || after.State != domain.VesselReserved {
		t.Fatalf("rejected abort mutated vessel: %+v", after)
	}
}
