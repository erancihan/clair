package utils

import (
	"encoding/json"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/bwmarrin/discordgo"
)

type SQSJsonMessageEmbed struct {
	Title       string
	Description string
	Image       string
	Color       string
}

type SQSJsonMessage struct {
	Content string
	Embed   *SQSJsonMessageEmbed
}

func v0(pMsg *discordgo.MessageSend, pJsonStr string) {
	payload := SQSJsonMessage{}
	json.Unmarshal([]byte(pJsonStr), &payload)

	if payload.Content != "" {
		pMsg.Content = payload.Content
	}
	if payload.Embed != nil {
		pMsg.Embeds = []*discordgo.MessageEmbed{{
			Color: int(HexToInt("1CADFF")),
		}}

		if payload.Embed.Title != "" {
			pMsg.Embeds[0].Title = payload.Embed.Title
		}
		if payload.Embed.Description != "" {
			pMsg.Embeds[0].Description = payload.Embed.Description
		}
		if payload.Embed.Image != "" {
			pMsg.Embeds[0].Image = &discordgo.MessageEmbedImage{
				URL: payload.Embed.Image,
			}
		}
		if payload.Embed.Color != "" {
			pMsg.Embeds[0].Color = int(HexToInt(payload.Embed.Color))
		}
	}
}

func v1(pMsg *discordgo.MessageSend, pJsonStr string) {
	json.Unmarshal([]byte(pJsonStr), pMsg)
}

func SQSLambdaToDiscordMessage(message events.SQSMessage) *discordgo.MessageSend {
	body := message.Body

	attrValue, ok := message.MessageAttributes["VERSION"]
	if !ok {
		attrValue = events.SQSMessageAttribute{
			StringValue: aws.String("0"),
		}
	}

	pMsg := &discordgo.MessageSend{
		TTS: false,
	}

	switch *attrValue.StringValue {
	case "0":
		v0(pMsg, body)
	case "1":
		v1(pMsg, body)
	default:
		pMsg = nil
	}

	return pMsg
}

func SQSToDiscordMessage(message *sqs.Message) *discordgo.MessageSend {
	pJsonStr := *message.Body // assume body is in json format by default

	attrValue, ok := message.MessageAttributes["VERSION"]
	if !ok {
		attrValue = &sqs.MessageAttributeValue{
			StringValue: aws.String("0"),
		}
	}

	pMsg := &discordgo.MessageSend{
		TTS: false,
	}

	switch *attrValue.StringValue {
	case "0":
		v0(pMsg, pJsonStr)
	case "1":
		v1(pMsg, pJsonStr)
	default:
		pMsg = nil
	}

	return pMsg
}
