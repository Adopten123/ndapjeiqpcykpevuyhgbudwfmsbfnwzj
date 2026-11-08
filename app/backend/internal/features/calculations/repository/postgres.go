package repository

import (
	"context"
	_ "embed"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"rzdarctica.local/backend/internal/features/calculations"
)

//go:embed schema.sql
var schema string

const columns = `id::text, scenario_id, status, created_at, updated_at, engine_profile, snapshot_id, input_sha256, result, error, input_payload, source_revision, base_accepted_version, base_accepted_packet_id::text, source_sha256, baseline_sha256, context_sha256`

type Postgres struct {
	pool  *pgxpool.Pool
	queue *river.Client[pgx.Tx]
}

func New(pool *pgxpool.Pool, queue *river.Client[pgx.Tx]) *Postgres {
	return &Postgres{pool: pool, queue: queue}
}

func (r *Postgres) Migrate(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, schema)
	return err
}

func read(row pgx.Row) (calculations.Calculation, error) {
	var c calculations.Calculation
	err := row.Scan(&c.ID, &c.ScenarioID, &c.Status, &c.CreatedAt, &c.UpdatedAt, &c.EngineProfile, &c.SnapshotID, &c.InputSHA256, &c.Result, &c.Error, &c.Payload, &c.SourceRevision, &c.BaseAcceptedVersion, &c.BaseAcceptedPacketID, &c.SourceSHA256, &c.BaselineSHA256, &c.ContextSHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, calculations.ErrNotFound
	}
	return c, err
}

func (r *Postgres) ByKey(ctx context.Context, key string) (calculations.Calculation, error) {
	return read(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM rzd_calculations WHERE idempotency_key=$1`, key))
}

func (r *Postgres) Get(ctx context.Context, id string) (calculations.Calculation, error) {
	return read(r.pool.QueryRow(ctx, `SELECT `+columns+` FROM rzd_calculations WHERE id=$1::uuid`, id))
}

func (r *Postgres) List(ctx context.Context) ([]calculations.Calculation, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+columns+` FROM rzd_calculations ORDER BY created_at DESC,id DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []calculations.Calculation{}
	for rows.Next() {
		c, err := read(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// CreateQueued сохраняет снимок и задание River одной транзакцией PostgreSQL.
func (r *Postgres) CreateQueued(ctx context.Context, id, key string, snapshot calculations.Snapshot) (calculations.Calculation, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return calculations.Calculation{}, err
	}
	defer tx.Rollback(context.Background())
	c, err := read(tx.QueryRow(ctx, `INSERT INTO rzd_calculations
        (id,idempotency_key,scenario_id,status,engine_profile,snapshot_id,input_sha256,input_payload)
        VALUES ($1::uuid,$2,$3,'queued',$4,$5,$6,$7::jsonb)
        ON CONFLICT (idempotency_key) DO NOTHING RETURNING `+columns,
		id, key, snapshot.ScenarioID, snapshot.EngineProfile, snapshot.SnapshotID, snapshot.InputSHA256, string(snapshot.Payload)))
	if errors.Is(err, calculations.ErrNotFound) {
		c, err = read(tx.QueryRow(ctx, `SELECT `+columns+` FROM rzd_calculations WHERE idempotency_key=$1`, key))
		if err == nil && c.ScenarioID != snapshot.ScenarioID {
			return calculations.Calculation{}, calculations.ErrConflict
		}
	} else if err == nil {
		_, err = r.queue.InsertTx(ctx, tx, calculations.JobArgs{CalculationID: c.ID}, &river.InsertOpts{Queue: calculations.Queue, MaxAttempts: 3})
	}
	if err != nil {
		return calculations.Calculation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return calculations.Calculation{}, err
	}
	return c, nil
}

func (r *Postgres) BeginAttempt(ctx context.Context, id string) (calculations.Calculation, bool, error) {
	c, err := read(r.pool.QueryRow(ctx, `UPDATE rzd_calculations SET status='running',error=NULL,updated_at=now()
        WHERE id=$1::uuid AND status IN ('queued','running') AND result IS NULL RETURNING `+columns, id))
	if errors.Is(err, calculations.ErrNotFound) {
		c, err = r.Get(ctx, id)
		return c, false, err
	}
	return c, err == nil, err
}

func (r *Postgres) Succeed(ctx context.Context, id string, result []byte) error {
	_, err := r.pool.Exec(ctx, `UPDATE rzd_calculations SET status='succeeded',result=$2::jsonb,error=NULL,updated_at=now()
        WHERE id=$1::uuid AND status IN ('queued','running') AND result IS NULL`, id, string(result))
	return err
}

func (r *Postgres) RetryOrFail(ctx context.Context, id string, terminal bool, message string) error {
	status := "queued"
	if terminal {
		status = "failed"
	}
	_, err := r.pool.Exec(ctx, `UPDATE rzd_calculations SET status=$2,error=$3,updated_at=now()
        WHERE id=$1::uuid AND status IN ('queued','running') AND result IS NULL`, id, status, message)
	return err
}
