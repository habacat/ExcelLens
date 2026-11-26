package server

import (
	"context"
	"testing"
)

func TestSubControllersCancelAllTasks(t *testing.T) {
	ac := newAnalysisController()
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	ac.analysisJobs["a1"] = &analysisTask{cancel: cancel1}
	ac.analysisTreeJobs["t1"] = &analysisTreeTask{cancel: cancel2}
	ac.cancelAll()
	if ctx1.Err() == nil || ctx2.Err() == nil {
		t.Fatalf("expected all analysis tasks to be cancelled: job=%v tree=%v", ctx1.Err(), ctx2.Err())
	}

	mj := newMetadataJobs()
	ctxs := make([]context.Context, 6)
	cancels := make([]context.CancelFunc, 6)
	for i := range ctxs {
		ctxs[i], cancels[i] = context.WithCancel(context.Background())
	}
	mj.catalogueJobs["c1"] = &catalogueTask{cancel: cancels[0]}
	mj.dataRuleJobs["d1"] = &dataRuleTask{cancel: cancels[1]}
	mj.printJobs["p1"] = &printTask{cancel: cancels[2]}
	mj.clusterJobs["k1"] = &clusterTask{cancel: cancels[3]}
	mj.compositionJobs["m1"] = &compositionTask{cancel: cancels[4]}
	mj.relationJobs["r1"] = &relationTask{cancel: cancels[5]}

	mj.cancelAllLocked()
	for i, ctx := range ctxs {
		if ctx.Err() == nil {
			t.Fatalf("expected metadata job %d to be cancelled", i)
		}
	}
}
