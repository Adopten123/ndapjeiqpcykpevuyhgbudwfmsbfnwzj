package simulation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"rzdarctica.local/backend/internal/core/jsonutil"
)

const (
	railClosure  = "rail_edge_closed"
	seaClosure   = "sea_edge_closed"
	vesselChange = "vessel_schedule_changed"
)

var eventID = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,96}$`)

type parameters map[string]int

func (p *parameters) UnmarshalJSON(data []byte) error {
	var fields map[string]*int
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	values := make(parameters, len(fields))
	for key, value := range fields {
		if value == nil {
			return errors.New("integer parameter required")
		}
		values[key] = *value
	}
	*p = values
	return nil
}

type eventInput struct {
	Type       string     `json:"type"`
	Target     string     `json:"target_id"`
	Available  *int       `json:"available_tick"`
	Reason     string     `json:"reason"`
	Parameters parameters `json:"parameters"`
}

type event struct {
	eventInput
	RunID    string `json:"run_id"`
	Source   string `json:"source"`
	ID       string `json:"event_id"`
	Received *int   `json:"received_tick,omitempty"`
}

type eventRecord struct {
	event
	Status   string   `json:"status"`
	Message  string   `json:"message,omitempty"`
	Affected []string `json:"affected_parties,omitempty"`
}

type fieldError struct{ field, message string }

func (e *fieldError) Error() string { return e.message }

func (s *storedRun) validate(e eventInput) error {
	if e.Type != railClosure && e.Type != seaClosure && e.Type != vesselChange {
		return &fieldError{"type", "Выберите поддерживаемый тип события."}
	}
	found := false
	for _, target := range s.Targets {
		if target.ID == e.Target && target.Type == e.Type {
			found = true
			break
		}
	}
	if !found {
		return &fieldError{"target_id", "Объект отсутствует в учебном графе для этого типа события."}
	}
	if e.Available == nil || *e.Available < 0 || *e.Available > horizon {
		return &fieldError{"available_tick", "Укажите целый шаг поступления от 0 до 30."}
	}
	if strings.TrimSpace(e.Reason) == "" || len([]rune(e.Reason)) > 240 || strings.ContainsAny(e.Reason, "\u2028\u2029") || strings.IndexFunc(e.Reason, unicode.IsControl) >= 0 {
		return &fieldError{"reason", "Укажите причину длиной до 240 символов в одной строке."}
	}
	start, end, count := "from_tick", "until_tick", 2
	if e.Type == vesselChange {
		start, end, count = "load_from_tick", "load_until_tick", 3
	}
	a, okA := e.Parameters[start]
	b, okB := e.Parameters[end]
	if !okA || a < 0 || a >= horizon {
		return &fieldError{start, "Укажите шаг начала интервала от 0 до 29."}
	}
	if !okB || a >= b || b > horizon || len(e.Parameters) != count {
		return &fieldError{end, "Конец интервала должен быть позже начала и не превышать шаг 30."}
	}
	if e.Type == vesselChange {
		departure, ok := e.Parameters["departure_tick"]
		if !ok || departure < b || departure > horizon {
			return &fieldError{"departure_tick", "Выход судна должен быть в конце окна погрузки или позже, до шага 30 включительно."}
		}
	}
	return nil
}

func strictJSON(data []byte, dst any) error {
	if len(data) == 0 || data[0] != '{' || !jsonutil.UniqueKeys(data) {
		return errors.New("JSON object with unique fields required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

type feedClient struct {
	base   string
	client *http.Client
}

func newFeed(base string) *feedClient {
	return &feedClient{strings.TrimRight(base, "/"), &http.Client{
		Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type feedPage struct {
	Events   []json.RawMessage `json:"events"`
	Cursor   string            `json:"next_cursor"`
	Complete *int              `json:"complete_through_tick"`
	Next     *int              `json:"next_tick"`
	More     *bool             `json:"has_more"`
}

func (c *feedClient) read(ctx context.Context, s *storedRun, tick int) ([]json.RawMessage, sourceState, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cursor := s.Source.Cursor
	var events []json.RawMessage
	for range 20 {
		query := url.Values{"scenario": {s.Scenario}, "through_tick": {strconv.Itoa(tick)}, "after": {cursor}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/runs/"+s.ID+"/events?"+query.Encode(), nil)
		if err != nil {
			return nil, s.Source, err
		}
		res, err := c.client.Do(req)
		if err != nil {
			return nil, s.Source, err
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, 512*1024+1))
		res.Body.Close()
		var page feedPage
		if err != nil || res.StatusCode != http.StatusOK || len(body) > 512*1024 ||
			json.Unmarshal(body, &page) != nil || page.Events == nil || page.Complete == nil ||
			page.More == nil || page.Cursor == "" || len(page.Cursor) > 256 || len(events)+len(page.Events) > 100 {
			return nil, s.Source, errors.New("invalid feed page")
		}
		events = append(events, page.Events...)
		if !*page.More {
			if *page.Complete != tick || page.Next != nil && (*page.Next <= tick || *page.Next > horizon) {
				return nil, s.Source, errors.New("incomplete feed")
			}
			return events, sourceState{page.Cursor, *page.Complete, page.Next}, nil
		}
		if page.Cursor == cursor {
			return nil, s.Source, errors.New("feed cursor stalled")
		}
		cursor = page.Cursor
	}
	return nil, s.Source, errors.New("feed pagination limit reached")
}

type delivered struct {
	event
	digest    string
	rejection string
}

func (s *storedRun) deliveries(raw []json.RawMessage) []delivered {
	batch := make([]delivered, 0, len(raw)+len(s.ManualEvents))
	sources := map[string]string{"rail-mock": railClosure, "carrier-mock": vesselChange, "ice-mock": seaClosure}
	for _, data := range raw {
		var e event
		parseErr := strictJSON(bytes.TrimSpace(data), &e)
		hash, hashErr := jsonutil.SHA256(data)
		if parseErr != nil || hashErr != nil {
			hash = fmt.Sprintf("%x", sha256.Sum256(data))
		}
		rejection := ""
		switch {
		case parseErr != nil || e.Received != nil:
			rejection = "Неверный формат сообщения."
		case e.RunID != s.ID || !eventID.MatchString(e.ID) || sources[e.Source] == "" || sources[e.Source] != e.Type:
			rejection = "Неверный прогон, источник, тип или идентификатор сообщения."
		default:
			if err := s.validate(e.eventInput); err != nil {
				rejection = err.Error()
			} else if *e.Available > s.Tick {
				rejection = "Сообщение выдано раньше шага поступления."
			}
		}
		if !eventID.MatchString(e.ID) {
			e.ID = "invalid-" + hash
		}
		if !eventID.MatchString(e.Source) {
			e.Source = "unknown"
		}
		batch = append(batch, delivered{e, hash, rejection})
	}
	pending := s.ManualEvents[:0]
	for _, e := range s.ManualEvents {
		if *e.Available <= s.Tick {
			hash, _ := jsonutil.SHA256(e)
			batch = append(batch, delivered{e, hash, ""})
		} else {
			pending = append(pending, e)
		}
	}
	s.ManualEvents = pending
	sort.Slice(batch, func(i, j int) bool {
		a, b := batch[i], batch[j]
		tick := func(e delivered) int {
			if e.Available == nil {
				return -1
			}
			return *e.Available
		}
		if tick(a) != tick(b) {
			return tick(a) < tick(b)
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.digest < b.digest
	})
	return batch
}

func (s *storedRun) affected(e event) []string {
	targets := make(map[string]operation, len(s.Operations))
	for _, op := range s.Operations {
		targets[op.ID] = op
	}
	seen := map[string]bool{}
	for _, scheduled := range s.ActivePlan.Operations {
		op := targets[scheduled.OperationID]
		if e.Type == vesselChange && op.Service == e.Target ||
			op.Edge == e.Target && scheduled.Start < e.Parameters["until_tick"] && scheduled.End > max(s.Tick, e.Parameters["from_tick"]) {
			seen[op.PartyID] = true
		}
	}
	parties := []string{}
	for _, p := range s.Parties {
		if seen[p.ID] {
			parties = append(parties, p.ID)
		}
	}
	return parties
}

func (s *storedRun) overlapsStarted(e event) bool {
	edges := make(map[string]string, len(s.Operations))
	for _, op := range s.Operations {
		edges[op.ID] = op.Edge
	}
	for _, f := range s.Facts {
		if f.State == "in_progress" && edges[f.OperationID] == e.Target &&
			f.Start < e.Parameters["until_tick"] && e.Parameters["from_tick"] < f.End {
			return true
		}
	}
	return false
}

// reveal is called under the same lock and transaction that persists the cursor
// and enqueues the single immutable snapshot for this batch.
func (s *storedRun) reveal(raw []json.RawMessage) (changed, conflict bool) {
	batch := s.deliveries(raw)
	schedules := map[string]map[string]int{}
	conflictingSchedules := map[string]bool{}
	first := len(s.Events)
	for _, d := range batch {
		key := d.Source + "/" + d.ID
		if prior, exists := s.Seen[key]; exists {
			if prior == d.digest || s.Seen[key+"/"+d.digest] != "" {
				continue
			}
			s.Seen[key+"/"+d.digest] = d.digest
			d.rejection = "Повторный идентификатор с изменённым содержимым."
			conflict = true
		} else {
			s.Seen[key] = d.digest
		}
		tick := s.Tick
		d.Received = &tick
		r := eventRecord{event: d.event, Status: "applied"}
		if d.rejection != "" {
			r.Status, r.Message = "rejected", d.rejection
			if s.Seen[key] != d.digest {
				r.Status = "conflict"
			}
		} else if d.Type != vesselChange && d.Parameters["until_tick"] <= tick && !s.overlapsStarted(d.event) {
			r.Status, r.Message = "expired", "Ограничение закончилось до получения сообщения."
		} else if d.Type == vesselChange {
			prior, exists := schedules[d.Target]
			if exists && (prior["load_from_tick"] != d.Parameters["load_from_tick"] || prior["load_until_tick"] != d.Parameters["load_until_tick"] ||
				prior["departure_tick"] != d.Parameters["departure_tick"]) {
				r.Status, r.Message = "conflict", "В пакете несколько разных расписаний одного рейса."
				conflictingSchedules[d.Target] = true
				conflict = true
			}
			schedules[d.Target] = d.Parameters
		}
		if r.Status == "applied" {
			r.Affected = s.affected(d.event)
		}
		s.Events = append(s.Events, r)
		s.record("event", fmt.Sprintf("%s · %s · %s", d.Source, d.Target, d.Reason))
	}
	for i := first; i < len(s.Events); i++ {
		r := &s.Events[i]
		if r.Status == "applied" && r.Type == vesselChange && conflictingSchedules[r.Target] {
			r.Status, r.Message = "conflict", "В пакете несколько разных расписаний одного рейса."
		}
		changed = changed || r.Status == "applied"
	}
	return changed, conflict
}
