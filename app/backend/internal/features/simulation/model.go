package simulation

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

const (
	profile        = "simulation-demo.v1"
	Queue          = "simulation"
	horizon        = 30
	pilotRouteID   = "khovrino-murmansk-kamchatka"
	defaultCargoID = "dry-supplies"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Start with the party already in motion, then add a party on the other sailing.
var templatePartyOrder = [...]string{"P2", "P3", "P1", "P4"}

var cargoNames = map[string]string{
	"steel-fasteners": "Стальной крепёж",
	"metal-spares":    "Металлические запчасти",
	defaultCargoID:    "Сухой контейнерный груз",
}

type cargoSelection struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type routeSelection struct {
	ID string `json:"id"`
}

type runMode string

const (
	modeTraining   runMode = "training"
	modeOperations runMode = "operations"
	modeLegacy     runMode = "legacy"
)

func newID() string {
	var v [16]byte
	rand.Read(v[:])
	v[6] = v[6]&15 | 64
	v[8] = v[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", v[:4], v[4:6], v[6:8], v[8:10], v[10:])
}

type summary struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Mode        runMode `json:"mode"`
	Scenario    string  `json:"scenario"`
	Tick        int     `json:"tick"`
	Status      string  `json:"status"`
	PlanVersion int     `json:"plan_version"`
}

func (s *summary) restoreMode() {
	// Saved rows from the original mixed scenarios have no mode field.
	if s.Mode == "" {
		s.Mode = modeLegacy
	}
}

type run struct {
	summary
	Cargo             cargoSelection `json:"cargo"`
	Route             routeSelection `json:"route"`
	StateVersion      int            `json:"state_version"`
	ConditionRevision int            `json:"condition_revision"`
	BasePlan          plan           `json:"base_plan"`
	ActivePlan        plan           `json:"active_plan"`
	Candidate         *candidate     `json:"candidate"`
	Events            []eventRecord  `json:"events"`
	ManualEvents      []event        `json:"manual_events"`
	Operations        []operation    `json:"operations"`
	Parties           []party        `json:"parties"`
	Targets           []target       `json:"targets"`
	Facts             []fact         `json:"facts"`
	Timeline          []entry        `json:"timeline"`
	Message           string         `json:"message"`
	Source            sourceState    `json:"source"`
}

func (r *run) restoreConfiguration() {
	if r.Cargo.ID == "" {
		r.Cargo = cargoSelection{ID: defaultCargoID, Name: cargoNames[defaultCargoID]}
	}
	if r.Route.ID == "" {
		r.Route = routeSelection{ID: pilotRouteID}
	}
}

type storedRun struct {
	run
	Assignments json.RawMessage   `json:"assignments"`
	AttemptID   string            `json:"attempt_id"`
	AppliedID   string            `json:"applied_id"`
	Seen        map[string]string `json:"seen"`
}

func (s *storedRun) partyIDs() []string {
	ids := make([]string, 0, len(s.Parties))
	for _, p := range s.Parties {
		ids = append(ids, p.ID)
	}
	return ids
}

type operation struct {
	ID      string `json:"id"`
	PartyID string `json:"party_id"`
	Kind    string `json:"kind"`
	From    string `json:"from_location_id"`
	To      string `json:"to_location_id"`
	Edge    string `json:"edge_id,omitempty"`
	Service string `json:"service_id,omitempty"`
}

type party struct {
	ID       string `json:"id"`
	DueTick  int    `json:"due_tick"`
	Location string `json:"location_id,omitempty"`
}

type target struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Label string `json:"label"`
}

type fact struct {
	Scheduled
	State string `json:"state"`
}

type Scheduled struct {
	OperationID string `json:"operation_id"`
	Start       int    `json:"start_tick"`
	End         int    `json:"end_tick"`
}

type partyPlan struct {
	ID      string `json:"party_id"`
	Route   string `json:"route_id"`
	Service string `json:"service_id"`
	Port    int    `json:"port_receipt_tick"`
	Receipt int    `json:"recipient_receipt_tick"`
}

type plan struct {
	Parties      []partyPlan `json:"party_plans"`
	Operations   []Scheduled `json:"scheduled_operations"`
	FixedFactIDs []string    `json:"fixed_fact_ids"`
}

func (p *plan) covers(ids []string) bool {
	if p == nil || len(p.Parties) != len(ids) || len(p.Operations) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range p.Parties {
		if seen[item.ID] || !slices.Contains(ids, item.ID) {
			return false
		}
		seen[item.ID] = true
	}
	return true
}

type candidate struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Plan    *plan  `json:"plan"`
	Message string `json:"message"`
}

type result struct {
	EngineProfile string          `json:"engine_profile"`
	InputSHA256   string          `json:"input_sha256"`
	SnapshotID    string          `json:"snapshot_id"`
	CalculationID string          `json:"calculation_id"`
	AttemptID     string          `json:"attempt_id"`
	Status        string          `json:"solver_status"`
	Plan          *plan           `json:"candidate"`
	Assignments   json.RawMessage `json:"group_decisions"`
	Conflicts     []struct {
		EventID     string `json:"event_id"`
		Source      string `json:"source"`
		OperationID string `json:"operation_id"`
		Reason      string `json:"reason"`
	} `json:"conflicts"`
}

// Restrict the verified starting plan to the selected template parties.
func (r *result) restrictTo(ids []string, operations map[string]string, facts map[string]bool) error {
	r.Plan.Parties = slices.DeleteFunc(r.Plan.Parties, func(item partyPlan) bool { return !slices.Contains(ids, item.ID) })
	r.Plan.Operations = slices.DeleteFunc(r.Plan.Operations, func(item Scheduled) bool { return operations[item.OperationID] == "" })
	r.Plan.FixedFactIDs = slices.DeleteFunc(r.Plan.FixedFactIDs, func(id string) bool { return !facts[id] })
	if !r.Plan.covers(ids) {
		return errors.New("starting plan does not cover selected parties")
	}
	var all []json.RawMessage
	if err := json.Unmarshal(r.Assignments, &all); err != nil {
		return err
	}
	selected := []json.RawMessage{}
	for _, assignment := range all {
		var group struct {
			PartyID string `json:"party_id"`
		}
		if err := json.Unmarshal(assignment, &group); err != nil {
			return err
		}
		if slices.Contains(ids, group.PartyID) {
			selected = append(selected, assignment)
		}
	}
	var err error
	r.Assignments, err = json.Marshal(selected)
	return err
}

type entry struct {
	Tick  int    `json:"tick"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type sourceState struct {
	Cursor   string `json:"cursor"`
	Complete int    `json:"complete_through_tick"`
	Next     *int   `json:"next_tick"`
}

func (s *storedRun) record(kind, label string) {
	s.Timeline = append(s.Timeline, entry{s.Tick, kind, label})
}

func (s *storedRun) complete() {
	for i := range s.Facts {
		f := &s.Facts[i]
		if f.State == "in_progress" && f.End <= s.Tick {
			f.State = "completed"
			s.record("operation_completed", "Завершено · "+f.OperationID)
		}
	}
}

func (s *storedRun) start() {
	fixed := make(map[string]bool, len(s.Facts))
	for _, f := range s.Facts {
		fixed[f.OperationID] = true
	}
	for _, op := range s.ActivePlan.Operations {
		if !fixed[op.OperationID] && op.Start == s.Tick {
			s.Facts = append(s.Facts, fact{op, "in_progress"})
			s.record("operation_started", "Начато · "+op.OperationID)
		}
	}
	s.complete()
	s.Status = "ready"
	s.Message = ""
	if len(s.Facts) == len(s.ActivePlan.Operations) && len(s.ManualEvents) == 0 && s.Source.Next == nil {
		for _, f := range s.Facts {
			if f.State != "completed" {
				return
			}
		}
		s.Status = "completed"
		s.record("completed", fmt.Sprintf("Все партии получены (%d)", len(s.Parties)))
	}
}

func (s *storedRun) nextTick() int {
	next := horizon
	consider := func(t int) {
		if t > s.Tick && t < next {
			next = t
		}
	}
	if s.Source.Next != nil {
		consider(*s.Source.Next)
	}
	for _, e := range s.ManualEvents {
		consider(*e.Available)
	}
	for _, e := range s.Events {
		if e.Status != "applied" {
			continue
		}
		for _, key := range []string{"from_tick", "until_tick", "load_from_tick", "load_until_tick", "departure_tick"} {
			if t, ok := e.Parameters[key]; ok {
				consider(t)
			}
		}
	}
	for _, op := range s.ActivePlan.Operations {
		consider(op.Start)
		consider(op.End)
	}
	return next
}
