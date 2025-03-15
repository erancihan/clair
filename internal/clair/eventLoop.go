package clair

import (
	"log"
	"strconv"
	"time"

	awssqshandler "github.com/erancihan/clair/internal/aws-sqs-handler"
	discordbot "github.com/erancihan/clair/internal/discord-bot"
	"github.com/erancihan/clair/internal/utils"
	"go.uber.org/zap"

	sentry "github.com/getsentry/sentry-go"
)

type EventLoop struct {
	DiscordBot *discordbot.DiscordBot
	SQS        *awssqshandler.AWSSQSHandler
	TTL        int64

	channelId string
	logger    *zap.Logger
}

var (
	DISCORD_CHANNEL_ID string = ""
)

func NewEventLoop() *EventLoop {
	logger := utils.NewLogger("eventloop")
	defer func() { _ = logger.Sync() }()

	channelId := utils.GetEnv("DISCORD_CHANNEL_ID", DISCORD_CHANNEL_ID)
	logger.Info("DISCORD_CHANNEL_ID", zap.String("DISCORD_CHANNEL_ID", channelId))
	if channelId == "" {
		// this is a non-recoverable error, FATAL
		sentry.CaptureMessage("DISCORD_CHANNEL_ID is EMPTY")
		logger.Fatal("DISCORD_CHANNEL_ID is EMPTY")
	}

	// setup Discord
	discord := discordbot.New()
	// discord.Connect()

	// setup SQS Handler
	sqs := awssqshandler.New()

	return &EventLoop{
		DiscordBot: &discord,
		SQS:        &sqs,
		TTL:        7200, // 2 hours in seconds

		channelId: channelId,
		logger:    logger,
	}
}

func (el *EventLoop) Close() {
	el.DiscordBot.Disconnect()
}

func (el *EventLoop) Loop() bool {
	// TODO: get all messages from the queue

	message, err := el.SQS.GetMessage()
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

	// Check if the message is older than the TTL
	if at < (time.Now().Unix() - el.TTL) {
		el.SQS.DeleteMessage(message.ReceiptHandle)
		return true
	}

	// Process message payload
	response := utils.SQSToDiscordMessage(message)
	if response == nil {
		// no need to log erroneous message payload
		return true
	}

	// send response message
	_, err = el.DiscordBot.MessageEmbed(el.channelId, response)
	if err != nil {
		// this _should be_ a recoverable error
		log.Println(err)
		sentry.CaptureException(err)
		return true
	}

	// Retrieve the message handle of the first message in the queue
	//  (you need the handle to delete the message).
	el.SQS.DeleteMessage(message.ReceiptHandle)

	return true
}
