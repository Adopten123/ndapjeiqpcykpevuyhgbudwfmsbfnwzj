package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"rzdarctica.local/backend/internal/features/calculations"
)

// This opt-in test uses a fresh schema inside an explicitly isolated test DB.
// It does not alter the working demo-chain schema or use SUPERUSER privileges.
func isolatedPlanningStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("RZD_TEST_DB_URL")
	if url == "" {
		t.Skip("set RZD_TEST_DB_URL to a separate rzd_arctica_replan_checks_* database")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid isolated test database configuration")
	}
	if !strings.HasPrefix(cfg.ConnConfig.Database, "rzd_arctica_replan_checks_") || cfg.ConnConfig.Host != "127.0.0.1" {
		t.Fatal("isolated loopback database required")
	}
	ctx := context.Background()
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot open test database")
	}
	t.Cleanup(base.Close)
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	name := "planning_test_" + strings.ReplaceAll(id, "-", "")
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal("cannot create isolated test schema")
	}
	t.Cleanup(func() { _, _ = base.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE") })
	cfg = cfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("cannot open isolated test schema")
	}
	t.Cleanup(pool.Close)
	raw, err := os.ReadFile(filepath.Join("..", "calculations", "repository", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(raw)); err != nil {
		t.Fatal("calculation migration failed")
	}
	store := New(pool)
	if err = store.Migrate(ctx); err != nil {
		t.Fatal("workflow migration failed")
	}
	return store, ctx
}

func TestPostgresBootstrapCannotInventAnUnknownLegacyRevision(t *testing.T) {
	store, ctx := isolatedPlanningStore(t)
	example := acceptedCalculationExample(t)
	id := "11111111-1111-4111-8111-111111111111"
	_, err := store.pool.Exec(ctx, `INSERT INTO rzd_calculations(id,idempotency_key,scenario_id,status,engine_profile,snapshot_id,input_sha256,input_payload,result) VALUES($1::uuid,'migration-fixture','S1','succeeded',$2,$3,$4,$5::jsonb,$6::jsonb)`, id, example.EngineProfile, example.SnapshotID, example.InputSHA256, string(example.Payload), string(example.Result))
	if err != nil {
		t.Fatal("fixture calculation failed")
	}

	// Mapping regression: the planning endpoint must return complete persisted
	// calculation DTOs, including an existing failed attempt on idempotent replay.
	if _, err = store.pool.Exec(ctx, `UPDATE rzd_calculations SET status='failed',error='Учебная ошибка' WHERE id=$1::uuid`, id); err != nil {
		t.Fatal("fixture status failed")
	}
	readTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal("read transaction failed")
	}
	saved, err := calc(ctx, readTx, id)
	readTx.Rollback(ctx)
	if err != nil || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() || saved.Error == nil || *saved.Error != "Учебная ошибка" || saved.Status != "failed" {
		t.Fatal("incomplete persisted calculation DTO")
	}
	_, err = store.pool.Exec(ctx, `INSERT INTO rzd_approval_packets(id,version,calculation_id,source_revision,base_accepted_version,status,created_at,content) VALUES($1::uuid,3,$1::uuid,2,0,'stale',clock_timestamp(),'{}'::jsonb)`, id)
	if err == nil {
		_, err = store.pool.Exec(ctx, `UPDATE rzd_approval_context SET source_revision=2,packet_version=3,active_packet_id=$1::uuid`, id)
	}
	if err != nil {
		t.Fatal("fixture workflow failed")
	}
	snapshot := calculations.Snapshot{ScenarioID: "S1", EngineProfile: calculations.Profile, InputSHA256: example.InputSHA256, SnapshotID: example.SnapshotID, Payload: example.Payload}
	if err = store.EnsureSource(ctx, snapshot); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("unknown legacy revision became a source snapshot")
	}
	var revision, version, count int
	var status string
	if store.pool.QueryRow(ctx, `SELECT source_revision,packet_version FROM rzd_approval_context`).Scan(&revision, &version) != nil || revision != 2 || version != 3 {
		t.Fatal("legacy context rewritten")
	}
	if store.pool.QueryRow(ctx, `SELECT status FROM rzd_approval_packets WHERE id=$1::uuid`, id).Scan(&status) != nil || status != "stale" {
		t.Fatal("legacy plan status rewritten")
	}
	if store.pool.QueryRow(ctx, `SELECT count(*) FROM rzd_planning_sources`).Scan(&count) != nil || count != 0 {
		t.Fatal("fabricated source inserted")
	}
}

func TestPostgresBootstrapIsImmutableAndIdempotent(t *testing.T) {
	store, ctx := isolatedPlanningStore(t)
	example := acceptedCalculationExample(t)
	snapshot := calculations.Snapshot{ScenarioID: "S1", EngineProfile: calculations.Profile, InputSHA256: example.InputSHA256, SnapshotID: example.SnapshotID, Payload: example.Payload}
	if err := store.EnsureSource(ctx, snapshot); err != nil {
		t.Fatal("initial source bootstrap failed")
	}
	altered, err := changeSource(example.Payload, 11, TickWindow{Start: 9, End: 11})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Payload = altered
	snapshot.InputSHA256, _ = hashJSON(altered)
	if err = store.EnsureSource(ctx, snapshot); err != nil {
		t.Fatal("idempotent bootstrap failed")
	}
	var count int
	var hash string
	var payload json.RawMessage
	if store.pool.QueryRow(ctx, `SELECT count(*) FROM rzd_planning_sources`).Scan(&count) != nil || count != 1 {
		t.Fatal("bootstrap added another revision")
	}
	if store.pool.QueryRow(ctx, `SELECT input_sha256,payload FROM rzd_planning_sources WHERE revision=1`).Scan(&hash, &payload) != nil || hash != example.InputSHA256 {
		t.Fatal("existing source replaced by fetched template")
	}
	actual, _ := hashJSON(payload)
	if actual != hash {
		t.Fatal("source hash differs from immutable content")
	}
}
