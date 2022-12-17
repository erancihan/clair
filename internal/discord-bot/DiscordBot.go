package discordbot

import (
	"log"

	"clair/internal/utils"

	"github.com/bwmarrin/discordgo"
)

var (
	DISCORD_BOT_AUTH_KEY        string = ""
	DISCORD_BOT_IDENTIFIER      string = ""
	DISCORD_BOT_SELF_IDENTIFIER string = "<@!" + DISCORD_BOT_IDENTIFIER + ">"
)

type DiscordBot struct {
	session *discordgo.Session
}

func New() DiscordBot {
	authKey := utils.GetEnv("DISCORD_BOT_AUTH_KEY", DISCORD_BOT_AUTH_KEY)
	if authKey == "" {
		log.Panicln("BOT Auth Key cannot be empty")
	}

	sess, err := discordgo.New("Bot " + authKey)
	if err != nil {
		log.Panicf("Invalid bot parameters: %v", err)
	}
	sess.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		log.Println("Bot is up!")
	})

	bot := DiscordBot{
		session: sess,
	}

	return bot
}

func (bot *DiscordBot) Connect() error {
	log.Println("Discord Bot Connecting...")
	return bot.session.Open()
}

func (bot *DiscordBot) Disconnect() error {
	log.Println("Discord Bot Disconnecting...")
	return bot.session.Close()
}

func (bot *DiscordBot) MessageEmbed(channelId string, data *discordgo.MessageSend) (st *discordgo.Message, err error) {
	return bot.session.ChannelMessageSendComplex(channelId, data)
}

func (bot *DiscordBot) MessageText(channelId, message string) (st *discordgo.Message, err error) {
	return bot.session.ChannelMessageSend(channelId, message)
}
