package approvals

import (
	"encoding/json"
	"errors"
	"time"
)

const WorkflowID = "demo-chain"
const PolicyID = "approval-s1-demo.v1"

var ErrForbidden = errors.New("forbidden")
var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("stale version or conflicting repeat")
var ErrInvalid = errors.New("invalid request")
var ErrUnsupported = errors.New("unsupported calculation")
var ErrSourceUnavailable = errors.New("no persisted source for current revision")
var ErrSourceMutationRequired = errors.New("real source payload update required")
var ErrCycleClosed = errors.New("fixed S1 demonstration cycle is closed")

type Context struct {
	SourceRevision   int64      `json:"source_revision"`
	ApprovalRevision int64      `json:"approval_revision"`
	PacketVersion    int64      `json:"packet_version"`
	ActivePacketID   *string    `json:"active_packet_id"`
	AcceptedPacketID *string    `json:"accepted_packet_id"`
	AcceptedVersion  int64      `json:"accepted_version"`
	SourceChangedAt  *time.Time `json:"-"`
}

type Response struct {
	ID           string    `json:"id"`
	SubjectID    string    `json:"subject_id"`
	Decision     string    `json:"decision"`
	ExpiresAt    time.Time `json:"expires_at"`
	ActorID      string    `json:"actor_id"`
	Revision     int64     `json:"revision"`
	CreatedAt    time.Time `json:"created_at"`
	Comment      string    `json:"comment,omitempty"`
	GrantVersion int64     `json:"-"`
}

type Subject struct {
	ID             string         `json:"id"`
	Title          string         `json:"title"`
	OrganizationID string         `json:"organization_id"`
	SHA256         string         `json:"sha256"`
	Details        map[string]any `json:"details"`
	Revision       int64          `json:"revision"`
	LatestResponse *Response      `json:"latest_response"`
	CanRespond     bool           `json:"can_respond"`
}

type Acceptance struct {
	ID              string    `json:"id"`
	AcceptedAt      time.Time `json:"accepted_at"`
	ActorID         string    `json:"actor_id"`
	AcceptedVersion int64     `json:"accepted_version"`
}

type Packet struct {
	ID                  string          `json:"id"`
	Version             int64           `json:"version"`
	CalculationID       string          `json:"calculation_id"`
	Status              string          `json:"status"`
	CreatedAt           time.Time       `json:"created_at"`
	InputSHA256         string          `json:"input_sha256"`
	CandidateSHA256     string          `json:"candidate_sha256"`
	SnapshotID          string          `json:"snapshot_id"`
	PolicyID            string          `json:"policy_id"`
	SourceRevision      int64           `json:"source_revision"`
	BaseAcceptedVersion int64           `json:"base_accepted_version"`
	ApprovalRevision    int64           `json:"approval_revision"`
	Subjects            []Subject       `json:"subjects"`
	Acceptance          *Acceptance     `json:"acceptance"`
	Issues              []string        `json:"issues"`
	CanAccept           bool            `json:"can_accept"`
	Binding             map[string]any  `json:"binding,omitempty"`
	FixedContext        json.RawMessage `json:"fixed_context,omitempty"`
}

type Document struct {
	Context Context `json:"context"`
	Packet  *Packet `json:"packet"`
}
type Listing struct {
	Context Context  `json:"context"`
	Packets []Packet `json:"packets"`
}

type CreateRequest struct {
	CalculationID           string `json:"calculation_id"`
	ExpectedSourceRevision  int64  `json:"expected_source_revision"`
	ExpectedAcceptedVersion int64  `json:"expected_accepted_version"`
}
type RespondRequest struct {
	PacketVersion    int64     `json:"packet_version"`
	SubjectID        string    `json:"subject_id"`
	SubjectSHA256    string    `json:"subject_sha256"`
	ExpectedRevision int64     `json:"expected_revision"`
	Decision         string    `json:"decision"`
	ExpiresAt        time.Time `json:"expires_at"`
	Comment          string    `json:"comment,omitempty"`
}
type AcceptRequest struct {
	PacketVersion            int64 `json:"packet_version"`
	ExpectedApprovalRevision int64 `json:"expected_approval_revision"`
	ExpectedSourceRevision   int64 `json:"expected_source_revision"`
	ExpectedAcceptedVersion  int64 `json:"expected_accepted_version"`
}
type InvalidateRequest struct {
	ExpectedSourceRevision int64 `json:"expected_source_revision"`
}
