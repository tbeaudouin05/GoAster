package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"goaster/internal/scheduler"
)

type cronToolTestRegistrar struct {
	tool Tool
}

func (r *cronToolTestRegistrar) RegisterTool(tool Tool) error {
	r.tool = tool
	return nil
}

func TestCronToolManagesJobsAndPreservesOmittedFields(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{
		Path: filepath.Join(t.TempDir(), "jobs.json"),
		Now:  func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	if registrar.tool.Name != cronToolName || registrar.tool.Handler == nil {
		t.Fatalf("registered cron tool = %+v", registrar.tool)
	}

	result, err := registrar.tool.Handler(context.Background(), `{"action":"add","every_seconds":60,"message":"first message","max_runs":3}`)
	if err != nil {
		t.Fatal(err)
	}
	var added scheduler.Job
	if err := json.Unmarshal([]byte(result), &added); err != nil {
		t.Fatalf("decode add result %q: %v", result, err)
	}
	if added.Kind != scheduler.ScheduleEvery || added.EverySeconds != 60 || added.MaxRuns != 3 || added.Message != "first message" {
		t.Fatalf("added job = %+v", added)
	}

	result, err = registrar.tool.Handler(context.Background(), `{"action":"update","id":"`+added.ID+`","message":"updated message"}`)
	if err != nil {
		t.Fatal(err)
	}
	var updated scheduler.Job
	if err := json.Unmarshal([]byte(result), &updated); err != nil {
		t.Fatalf("decode update result %q: %v", result, err)
	}
	if updated.Message != "updated message" || updated.Kind != scheduler.ScheduleEvery || updated.EverySeconds != 60 || updated.MaxRuns != 3 || updated.NextRun == nil {
		t.Fatalf("partial tool update did not preserve fields: %+v", updated)
	}

	if _, err := registrar.tool.Handler(context.Background(), `{"action":"disable","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"enable","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	listResult, err := registrar.tool.Handler(context.Background(), `{"action":"list"}`)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Jobs []scheduler.Job `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(listResult), &listed); err != nil {
		t.Fatalf("decode list result %q: %v", listResult, err)
	}
	if len(listed.Jobs) != 1 || listed.Jobs[0].ID != added.ID {
		t.Fatalf("listed jobs = %+v", listed.Jobs)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"remove","id":"`+added.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := jobs.Get(added.ID); ok {
		t.Fatal("cron tool remove left job behind")
	}
}

func TestCronToolSupportsAtAndCronSchedulesAndRejectsAmbiguousAdd(t *testing.T) {
	jobs, err := scheduler.New(scheduler.Options{Path: filepath.Join(t.TempDir(), "jobs.json")})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &cronToolTestRegistrar{}
	if err := RegisterCronTool(registrar, jobs); err != nil {
		t.Fatal(err)
	}
	atSeconds := time.Now().Add(time.Hour).Unix()
	result, err := registrar.tool.Handler(context.Background(), `{"action":"add","at_seconds":`+formatInt64(atSeconds)+`,"message":"later"}`)
	if err != nil {
		t.Fatal(err)
	}
	var atJob scheduler.Job
	if err := json.Unmarshal([]byte(result), &atJob); err != nil {
		t.Fatal(err)
	}
	if atJob.Kind != scheduler.ScheduleAt || !atJob.DeleteAfterRun {
		t.Fatalf("at job = %+v, want delete-after-run", atJob)
	}
	result, err = registrar.tool.Handler(context.Background(), `{"action":"add","cron_expr":"0 9 * * 1","message":"weekly"}`)
	if err != nil {
		t.Fatal(err)
	}
	var cronJob scheduler.Job
	if err := json.Unmarshal([]byte(result), &cronJob); err != nil {
		t.Fatal(err)
	}
	if cronJob.Kind != scheduler.ScheduleCron || cronJob.CronExpr != "0 9 * * 1" {
		t.Fatalf("cron job = %+v", cronJob)
	}
	if _, err := registrar.tool.Handler(context.Background(), `{"action":"add","every_seconds":10,"cron_expr":"* * * * *","message":"ambiguous"}`); err == nil {
		t.Fatal("ambiguous cron add unexpectedly succeeded")
	}
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
