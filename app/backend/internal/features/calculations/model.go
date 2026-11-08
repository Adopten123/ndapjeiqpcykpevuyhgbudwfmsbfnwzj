package calculations

import (
	"encoding/json"
	"errors"
	"time"
)

const Profile = "research-demo.v1"
const WorkflowProfile = "workflow-demo.v1"
const Queue = "calculations"

var ErrNotFound = errors.New("calculation not found")
var ErrConflict = errors.New("idempotency key conflicts with request")
var ErrCompute = errors.New("compute service unavailable or invalid response")
var ErrInvalid = errors.New("invalid calculation request")

type Calculation struct {
	ID                   string          `json:"id"`
	ScenarioID           string          `json:"scenario_id"`
	Status               string          `json:"status"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	EngineProfile        string          `json:"engine_profile"`
	SnapshotID           string          `json:"snapshot_id"`
	InputSHA256          string          `json:"input_sha256"`
	Result               json.RawMessage `json:"result"`
	Error                *string         `json:"error"`
	Payload              json.RawMessage `json:"-"`
	SourceRevision       int64           `json:"source_revision"`
	BaseAcceptedVersion  int64           `json:"base_accepted_version"`
	BaseAcceptedPacketID *string         `json:"base_accepted_packet_id"`
	SourceSHA256         string          `json:"source_sha256"`
	BaselineSHA256       string          `json:"baseline_sha256"`
	ContextSHA256        string          `json:"context_sha256"`
}

type Scenario struct {
	ScenarioID    string `json:"scenario_id"`
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	EngineProfile string `json:"engine_profile"`
	SnapshotID    string `json:"snapshot_id"`
	InputSHA256   string `json:"input_sha256"`
}

type Snapshot struct {
	ScenarioID    string          `json:"scenario_id"`
	EngineProfile string          `json:"engine_profile"`
	InputSHA256   string          `json:"input_sha256"`
	SnapshotID    string          `json:"snapshot_id"`
	Payload       json.RawMessage `json:"payload"`
}

type ComputeRequest struct {
	RequestID     string          `json:"request_id"`
	EngineProfile string          `json:"engine_profile"`
	Payload       json.RawMessage `json:"payload"`
}

type ComputeResponse struct {
	RequestID        string          `json:"request_id"`
	EngineProfile    string          `json:"engine_profile"`
	InputSHA256      string          `json:"input_sha256"`
	Result           json.RawMessage `json:"result"`
	ValidationErrors []string        `json:"validation_errors"`
}

type JobArgs struct {
	CalculationID string `json:"calculation_id"`
}

func (JobArgs) Kind() string { return "rzd_demo_calculation" }

func ValidScenario(id string) bool {
	return id == "S0" || id == "S1" || id == "S2" || id == "S3"
}
