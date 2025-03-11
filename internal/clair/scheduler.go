package clair

import (
	timedexecutor "clair/internal/timed-executor"
	"log"
	"time"
)

type Scheduler struct {
	executor timedexecutor.ScheduledExecutor
}

func NewScheduler() *Scheduler {
	return &Scheduler{}
}

func (s *Scheduler) ScheduleSQS(callback func() bool, delay time.Duration) {
	s.executor = timedexecutor.NewScheduledExecutor(1*time.Second, delay)
	s.executor.StartLoop(callback)
}

func (s *Scheduler) Close() {
	log.Println("Closing scheduler")
	s.executor.Close()
}
