package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	"rzdarctica.local/backend/internal/features/calculations"
)

type Store interface {
	ByKey(context.Context, string) (calculations.Calculation, error)
	CreateQueued(context.Context, string, string, calculations.Snapshot) (calculations.Calculation, error)
	Get(context.Context, string) (calculations.Calculation, error)
	List(context.Context) ([]calculations.Calculation, error)
}

type Computer interface {
	Scenarios(context.Context) ([]calculations.Scenario, error)
	Snapshot(context.Context, string) (calculations.Snapshot, error)
}

type Service struct {
	store    Store
	computer Computer
}

func New(store Store, computer Computer) *Service { return &Service{store: store, computer: computer} }

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func (s *Service) Create(ctx context.Context, key, scenario string) (calculations.Calculation, error) {
	if !calculations.ValidScenario(scenario) {
		return calculations.Calculation{}, calculations.ErrInvalid
	}
	if existing, err := s.store.ByKey(ctx, key); err == nil {
		if existing.ScenarioID != scenario {
			return calculations.Calculation{}, calculations.ErrConflict
		}
		return existing, nil
	} else if !errors.Is(err, calculations.ErrNotFound) {
		return calculations.Calculation{}, err
	}
	snapshot, err := s.computer.Snapshot(ctx, scenario)
	if err != nil {
		return calculations.Calculation{}, calculations.ErrCompute
	}
	id, err := newID()
	if err != nil {
		return calculations.Calculation{}, err
	}
	return s.store.CreateQueued(ctx, id, key, snapshot)
}

func (s *Service) Get(ctx context.Context, id string) (calculations.Calculation, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]calculations.Calculation, error) {
	return s.store.List(ctx)
}
func (s *Service) Scenarios(ctx context.Context) ([]calculations.Scenario, error) {
	return s.computer.Scenarios(ctx)
}

// ValidateResponse связывает результат со снимком и запросом приложения.
// Проверку физических ограничений выполняет независимый Python-валидатор.
func ValidateResponse(c calculations.Calculation, response calculations.ComputeResponse) error {
	if response.RequestID != c.ID || response.EngineProfile != c.EngineProfile || response.InputSHA256 != c.InputSHA256 || response.ValidationErrors == nil || len(response.ValidationErrors) != 0 {
		return calculations.ErrCompute
	}
	var input struct {
		SnapshotID    string `json:"snapshot_id"`
		CalculationID string `json:"calculation_id"`
		AttemptID     string `json:"attempt_id"`
		Mode          string `json:"mode"`
		DataMode      string `json:"data_mode"`
		TimeBasis     struct {
			Now     int `json:"now_tick"`
			Horizon int `json:"horizon_end_tick"`
		} `json:"time_basis"`
		Parties []struct {
			ID       string   `json:"id"`
			Services []string `json:"allowed_service_ids"`
		} `json:"parties"`
		Routes []struct {
			ID      string `json:"id"`
			PartyID string `json:"party_id"`
		} `json:"route_candidates"`
	}
	var result struct {
		EngineProfile string          `json:"engine_profile"`
		Execution     string          `json:"execution"`
		Synthetic     bool            `json:"synthetic"`
		InputSHA256   string          `json:"input_sha256"`
		SnapshotID    string          `json:"snapshot_id"`
		CalculationID string          `json:"calculation_id"`
		AttemptID     string          `json:"attempt_id"`
		SolverStatus  string          `json:"solver_status"`
		Candidate     json.RawMessage `json:"candidate"`
		Mode          string          `json:"mode"`
		ProvenPrefix  *int            `json:"proven_prefix"`
		Objective     json.RawMessage `json:"objective"`
		Stages        []struct {
			Name   string `json:"objective_name"`
			Status string `json:"status"`
		} `json:"optimization_stages"`
	}
	if json.Unmarshal(c.Payload, &input) != nil || json.Unmarshal(response.Result, &result) != nil || result.EngineProfile != c.EngineProfile || result.Execution != "executed" || !result.Synthetic || result.InputSHA256 != c.InputSHA256 || result.SnapshotID != input.SnapshotID || result.CalculationID != input.CalculationID || result.AttemptID != input.AttemptID || input.SnapshotID != c.SnapshotID {
		return calculations.ErrCompute
	}
	if input.DataMode != "demo" || result.Mode != input.Mode || input.Mode != "initial" && input.Mode != "replan" || input.TimeBasis.Horizon <= input.TimeBasis.Now || len(input.Parties) != 4 {
		return calculations.ErrCompute
	}
	names := []string{"sum_remaining_tick", "last_remaining_tick"}
	if input.Mode == "replan" {
		names = []string{"sum_positive_shift", "max_positive_shift", "changed_groups"}
	}
	if result.ProvenPrefix == nil || *result.ProvenPrefix < 0 || *result.ProvenPrefix > len(names) || len(result.Stages) < 1 || len(result.Stages) > len(names) {
		return calculations.ErrCompute
	}
	leading := 0
	for i, stage := range result.Stages {
		if stage.Name != names[i] {
			return calculations.ErrCompute
		}
		switch stage.Status {
		case "OPTIMAL":
			leading++
		case "FEASIBLE", "UNKNOWN", "INFEASIBLE", "MODEL_INVALID":
			if i != len(result.Stages)-1 {
				return calculations.ErrCompute
			}
		default:
			return calculations.ErrCompute
		}
	}
	if *result.ProvenPrefix != leading || result.SolverStatus == "OPTIMAL" && leading != len(names) {
		return calculations.ErrCompute
	}
	if result.SolverStatus != "OPTIMAL" && result.SolverStatus != "FEASIBLE" && result.SolverStatus != "INFEASIBLE" && result.SolverStatus != "UNKNOWN" && result.SolverStatus != "MODEL_INVALID" {
		return calculations.ErrCompute
	}
	if len(result.Candidate) == 0 || len(result.Objective) == 0 {
		return calculations.ErrCompute
	}
	hasCandidate := !bytes.Equal(bytes.TrimSpace(result.Candidate), []byte("null"))
	if (result.SolverStatus == "OPTIMAL" || result.SolverStatus == "FEASIBLE") && !hasCandidate || (result.SolverStatus == "INFEASIBLE" || result.SolverStatus == "MODEL_INVALID") && hasCandidate {
		return calculations.ErrCompute
	}
	if !hasCandidate {
		if !bytes.Equal(bytes.TrimSpace(result.Objective), []byte("null")) || *result.ProvenPrefix != 0 {
			return calculations.ErrCompute
		}
		return nil
	}
	var objective map[string]*int
	if json.Unmarshal(result.Objective, &objective) != nil || len(objective) != len(names) {
		return calculations.ErrCompute
	}
	for _, name := range names {
		if objective[name] == nil || *objective[name] < 0 {
			return calculations.ErrCompute
		}
	}
	var candidate struct {
		Plans []struct {
			PartyID   string `json:"party_id"`
			RouteID   string `json:"route_id"`
			ServiceID string `json:"service_id"`
			Port      *int   `json:"port_receipt_tick"`
			Recipient *int   `json:"recipient_receipt_tick"`
		} `json:"party_plans"`
	}
	if json.Unmarshal(result.Candidate, &candidate) != nil || len(candidate.Plans) != len(input.Parties) {
		return calculations.ErrCompute
	}
	parties := map[string]string{}
	for _, party := range input.Parties {
		if party.ID == "" || len(party.Services) != 1 {
			return calculations.ErrCompute
		}
		if _, exists := parties[party.ID]; exists {
			return calculations.ErrCompute
		}
		parties[party.ID] = party.Services[0]
	}
	routes := map[string]string{}
	for _, route := range input.Routes {
		if _, exists := routes[route.ID]; exists {
			return calculations.ErrCompute
		}
		routes[route.ID] = route.PartyID
	}
	seen := map[string]bool{}
	for _, plan := range candidate.Plans {
		service, exists := parties[plan.PartyID]
		if !exists || seen[plan.PartyID] || service != plan.ServiceID || routes[plan.RouteID] != plan.PartyID || plan.Port == nil || plan.Recipient == nil || *plan.Port < input.TimeBasis.Now || *plan.Recipient < *plan.Port || *plan.Recipient > input.TimeBasis.Horizon {
			return calculations.ErrCompute
		}
		seen[plan.PartyID] = true
	}
	return nil
}
