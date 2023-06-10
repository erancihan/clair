package timedexecutor

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type ScheduledExecutor struct {
	delay  time.Duration
	ticker time.Ticker
	quit   chan int
}

func NewScheduledExecutor(initDelay, delay time.Duration) ScheduledExecutor {
	return ScheduledExecutor{
		delay:  delay,
		ticker: *time.NewTicker(initDelay),
		quit:   make(chan int),
	}
}

func (executor *ScheduledExecutor) Close() error {
	go func() {
		executor.quit <- 1
	}()

	return nil
}

func (executor *ScheduledExecutor) close() {
	executor.ticker.Stop()
}

func (executor ScheduledExecutor) StartLoop(task func() bool) {
	executor.Start(func() {
		for {
			if isDone := task(); isDone {
				return
			}
		}
	})
}

func (executor ScheduledExecutor) Start(task func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		defer func() {
			executor.close()
			log.Println("Scheduler stopped")
		}()

		initial := true

		for {
			select {
			case <-executor.ticker.C:
				if initial {
					executor.ticker.Stop()
					executor.ticker = *time.NewTicker(executor.delay)
					initial = false
				}

				go task()

			case <-executor.quit:
				return

			case <-sig:
				_ = executor.Close()
			}
		}
	}()
}
