package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/odyfey/riverdelta/app/config"
	"github.com/odyfey/riverdelta/app/pgtest"
	"github.com/odyfey/riverdelta/app/sqlitetest"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/riverdriver/riversqlite"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
)

type tenantAArgs struct{}

func (tenantAArgs) Kind() string { return "tenant_a_job" }

type tenantBArgs struct{}

func (tenantBArgs) Kind() string { return "tenant_b_job" }

type apiJob struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

type apiQueue struct {
	Name        string          `json:"name"`
	PausedAt    *string         `json:"paused_at"`
	Concurrency json.RawMessage `json:"concurrency"`
}

// insertTestJobs inserts jobs through a real insert-only River client.
func insertTestJobs[TTx any](t *testing.T, driver riverdriver.Driver[TTx], schema string, args river.JobArgs, count int) []int64 {
	t.Helper()
	client, err := river.NewClient(driver, &river.Config{Schema: schema, SkipUnknownJobCheck: true})
	require.NoError(t, err)

	ids := make([]int64, count)
	for i := range count {
		res, err := client.Insert(t.Context(), args, nil)
		require.NoError(t, err)
		ids[i] = res.Job.ID
	}
	return ids
}

// insertTestQueue registers a queue the way a running River producer would.
func insertTestQueue(t *testing.T, pool *pgxpool.Pool, schema, name string) {
	t.Helper()
	query := fmt.Sprintf("insert into %s.river_queue (name, created_at, updated_at) values ($1, now(), now())",
		pgx.Identifier{schema}.Sanitize())
	_, err := pool.Exec(t.Context(), query, name)
	require.NoError(t, err)
}

func doJSON(t *testing.T, srv *httptest.Server, method, path string, body, out any) int {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, reqBody)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	res, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, res.Body.Close()) })
	raw, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	if out != nil && res.StatusCode == http.StatusOK {
		require.NoError(t, json.Unmarshal(raw, out), "body: %s", raw)
	}
	return res.StatusCode
}

func listJobs(t *testing.T, srv *httptest.Server, instance, state string) []apiJob {
	t.Helper()
	var list struct {
		Data []apiJob `json:"data"`
	}
	status := doJSON(t, srv, http.MethodGet, "/"+instance+"/api/jobs?state="+state, nil, &list)
	require.Equal(t, http.StatusOK, status)
	return list.Data
}

func getDefaultQueue(t *testing.T, srv *httptest.Server, instance string) apiQueue {
	t.Helper()
	var queue apiQueue
	status := doJSON(t, srv, http.MethodGet, "/"+instance+"/api/queues/"+river.QueueDefault, nil, &queue)
	require.Equal(t, http.StatusOK, status)
	return queue
}

func jobIDs(jobs []apiJob) []int64 {
	ids := make([]int64, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}
	return ids
}

func idsBody(id int64) map[string][]string {
	return map[string][]string{"ids": {strconv.FormatInt(id, 10)}}
}

func TestIntegrationInstancesAreIsolated(t *testing.T) {
	pool := pgtest.NewPool(t)
	schemaA := pgtest.NewSchema(t, pool, 0)
	schemaB := pgtest.NewSchema(t, pool, 0)
	idsA := insertTestJobs(t, riverpgxv5.New(pool), schemaA, tenantAArgs{}, 2)
	idsB := insertTestJobs(t, riverpgxv5.New(pool), schemaB, tenantBArgs{}, 1)
	insertTestQueue(t, pool, schemaA, river.QueueDefault)
	insertTestQueue(t, pool, schemaB, river.QueueDefault)

	// both schemas number jobs from 1, so acting on an id in one instance
	// must leave the same id in the other untouched
	require.Equal(t, idsA[0], idsB[0])

	instances := openTestInstances(t, pgtest.Database(t,
		config.Instance{Name: "tenant-a", Schema: schemaA},
		config.Instance{Name: "tenant-b", Schema: schemaB},
	))
	srv := httptest.NewServer(newServerHandler(instances, testStylesheet(t), nil, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	t.Run("JobListsDiffer", func(t *testing.T) {
		jobsA := listJobs(t, srv, "tenant-a", "available")
		jobsB := listJobs(t, srv, "tenant-b", "available")
		require.ElementsMatch(t, idsA, jobIDs(jobsA))
		require.ElementsMatch(t, idsB, jobIDs(jobsB))
		for _, job := range jobsA {
			require.Equal(t, "tenant_a_job", job.Kind)
		}
		require.Equal(t, "tenant_b_job", jobsB[0].Kind)
	})

	t.Run("JobDetailsPerInstance", func(t *testing.T) {
		var jobA, jobB apiJob
		path := fmt.Sprintf("/api/jobs/%d", idsA[0])
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodGet, "/tenant-a"+path, nil, &jobA))
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodGet, "/tenant-b"+path, nil, &jobB))
		require.Equal(t, "tenant_a_job", jobA.Kind)
		require.Equal(t, "tenant_b_job", jobB.Kind)
	})

	t.Run("CancelRetryDeleteStayInInstance", func(t *testing.T) {
		id := idsA[0]
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPost, "/tenant-a/api/jobs/cancel", idsBody(id), nil))
		require.Equal(t, []int64{id}, jobIDs(listJobs(t, srv, "tenant-a", string(rivertype.JobStateCancelled))))
		require.Equal(t, idsB, jobIDs(listJobs(t, srv, "tenant-b", "available")))

		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPost, "/tenant-a/api/jobs/retry", idsBody(id), nil))
		require.ElementsMatch(t, idsA, jobIDs(listJobs(t, srv, "tenant-a", "available")))

		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPost, "/tenant-a/api/jobs/delete", idsBody(id), nil))
		require.Equal(t, idsA[1:], jobIDs(listJobs(t, srv, "tenant-a", "available")))
		require.Equal(t, idsB, jobIDs(listJobs(t, srv, "tenant-b", "available")))
	})

	t.Run("QueuePauseResumeUpdateStayInInstance", func(t *testing.T) {
		queuePath := "/api/queues/" + river.QueueDefault
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPut, "/tenant-a"+queuePath+"/pause", nil, nil))
		require.NotNil(t, getDefaultQueue(t, srv, "tenant-a").PausedAt)
		require.Nil(t, getDefaultQueue(t, srv, "tenant-b").PausedAt)

		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPut, "/tenant-a"+queuePath+"/resume", nil, nil))
		require.Nil(t, getDefaultQueue(t, srv, "tenant-a").PausedAt)

		update := map[string]any{"concurrency": map[string]any{
			"global_limit": 5,
			"local_limit":  1,
			"partition":    map[string]any{"by_args": []string{}, "by_kind": false},
		}}
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPatch, "/tenant-a"+queuePath, update, nil))
		require.JSONEq(t,
			`{"global_limit":5,"local_limit":1,"partition":{"by_args":[],"by_kind":false}}`,
			string(getDefaultQueue(t, srv, "tenant-a").Concurrency))
		require.JSONEq(t, "null", string(getDefaultQueue(t, srv, "tenant-b").Concurrency))
	})

	t.Run("PagesCarryInstanceBase", func(t *testing.T) {
		for _, name := range []string{"tenant-a", "tenant-b"} {
			rec := serveTest(t, srv.Config.Handler, "/"+name+"/jobs", nil)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), `"base":"/`+name+`"`)
		}
	})
}

// TestIntegrationSQLiteInstance runs riverui's SQLite queries through the
// wrapper, next to a Postgres instance whose job ids collide with it.
func TestIntegrationSQLiteInstance(t *testing.T) {
	pool := pgtest.NewPool(t)
	schema := pgtest.NewSchema(t, pool, 0)
	idsPG := insertTestJobs(t, riverpgxv5.New(pool), schema, tenantAArgs{}, 1)

	path := sqlitetest.NewDatabase(t, 0)
	idsLite := insertTestJobs(t, riversqlite.New(sqlitetest.Open(t, path)), "", tenantBArgs{}, 2)
	require.Equal(t, idsPG[0], idsLite[0])

	instances := openTestInstances(t,
		pgtest.Database(t, config.Instance{Name: "pg", Schema: schema}),
		config.Database{Name: "lite", URL: sqlitetest.URL(path), Instances: []config.Instance{{Name: "lite"}}},
	)
	srv := httptest.NewServer(newServerHandler(instances, testStylesheet(t), nil, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	t.Run("JobList", func(t *testing.T) {
		jobs := listJobs(t, srv, "lite", "available")
		require.ElementsMatch(t, idsLite, jobIDs(jobs))
		require.Equal(t, "tenant_b_job", jobs[0].Kind)
	})

	t.Run("JobDetails", func(t *testing.T) {
		var job apiJob
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodGet, fmt.Sprintf("/lite/api/jobs/%d", idsLite[0]), nil, &job))
		require.Equal(t, "tenant_b_job", job.Kind)
	})

	t.Run("CancelRetryDeleteStayInInstance", func(t *testing.T) {
		id := idsLite[0]
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPost, "/lite/api/jobs/cancel", idsBody(id), nil))
		require.Equal(t, []int64{id}, jobIDs(listJobs(t, srv, "lite", string(rivertype.JobStateCancelled))))
		require.Equal(t, idsPG, jobIDs(listJobs(t, srv, "pg", "available")))

		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPost, "/lite/api/jobs/retry", idsBody(id), nil))
		require.ElementsMatch(t, idsLite, jobIDs(listJobs(t, srv, "lite", "available")))

		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodPost, "/lite/api/jobs/delete", idsBody(id), nil))
		require.Equal(t, idsLite[1:], jobIDs(listJobs(t, srv, "lite", "available")))
		require.Equal(t, idsPG, jobIDs(listJobs(t, srv, "pg", "available")))
	})

	t.Run("StatusHealthy", func(t *testing.T) {
		var views []map[string]any
		require.Equal(t, http.StatusOK, doJSON(t, srv, http.MethodGet, "/instances.json", nil, &views))
		require.Len(t, views, 2)
		for _, view := range views {
			require.Equal(t, true, view["healthy"], "instance %v", view["name"])
		}
	})
}
