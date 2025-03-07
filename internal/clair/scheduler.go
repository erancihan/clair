package clair

import (
	awssqshandler "clair/internal/aws-sqs-handler"
	discordbot "clair/internal/discord-bot"
	timedexecutor "clair/internal/timed-executor"
	utils "clair/internal/utils"
	"log"
	"strconv"
	"time"

	"github.com/getsentry/sentry-go"
)

type Scheduler struct {
	discordbot *discordbot.DiscordBot
	sqs        *awssqshandler.AWSSQSHandler

	dropOlderThanMilli int64

	executor timedexecutor.ScheduledExecutor
}

func NewScheduler(
	discordbot *discordbot.DiscordBot,
	sqs *awssqshandler.AWSSQSHandler,
	dropOlderThanMilli int64,
) *Scheduler {
	return &Scheduler{
		discordbot:         discordbot,
		sqs:                sqs,
		dropOlderThanMilli: dropOlderThanMilli,
	}
}

func (s *Scheduler) ScheduleSQS(channelId string, delay time.Duration) {
	s.executor = timedexecutor.NewScheduledExecutor(1*time.Second, delay)

	s.executor.StartLoop(func() bool {
		message, err := s.sqs.GetMessage()
		if err != nil {
			sentry.CaptureException(err)
			return true
		}
		if message == nil {
			return true
		}

		at, err := strconv.ParseInt(*message.Attributes["SentTimestamp"], 10, 64)
		if err != nil {
			sentry.CaptureException(err)
			return true
		}
		if at < int64(s.dropOlderThanMilli) {
			// message is older than 2 hours, delete and skip
			s.sqs.DeleteMessage(message.ReceiptHandle)
			return true
		}

		// Process message payload
		response := utils.SQSToDiscordMessage(message)
		if response == nil {
			// no need to log erroneous message payload
			return true
		}

		// send response message
		_, err = s.discordbot.MessageEmbed(channelId, response)
		if err != nil {
			// this _should be_ a recoverable error
			log.Println(err)
			sentry.CaptureException(err)
			return true
		}

		// Retrieve the message handle of the first message in the queue
		//  (you need the handle to delete the message).
		s.sqs.DeleteMessage(message.ReceiptHandle)

		return true
	})
}

func (s *Scheduler) Close() {
	log.Println("Closing scheduler")
	s.executor.Close()
}
