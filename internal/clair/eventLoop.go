package clair

import (
	awssqshandler "clair/internal/aws-sqs-handler"
	discordbot "clair/internal/discord-bot"
	"clair/internal/utils"
	"log"
	"strconv"

	sentry "github.com/getsentry/sentry-go"
)

type EventLoop struct {
	DiscordBot *discordbot.DiscordBot
	SQS        *awssqshandler.AWSSQSHandler
	TTL        int64
}

func (eventLoop *EventLoop) Loop(channelId string) bool {
	message, err := eventLoop.SQS.GetMessage()
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
	if at < int64(eventLoop.TTL) {
		// message is older than 2 hours, delete and skip
		eventLoop.SQS.DeleteMessage(message.ReceiptHandle)
		return true
	}

	// Process message payload
	response := utils.SQSToDiscordMessage(message)
	if response == nil {
		// no need to log erroneous message payload
		return true
	}

	// send response message
	_, err = eventLoop.DiscordBot.MessageEmbed(channelId, response)
	if err != nil {
		// this _should be_ a recoverable error
		log.Println(err)
		sentry.CaptureException(err)
		return true
	}

	// Retrieve the message handle of the first message in the queue
	//  (you need the handle to delete the message).
	eventLoop.SQS.DeleteMessage(message.ReceiptHandle)

	return true
}
