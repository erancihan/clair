## Mercury
Discord Bot written in GoLang. Reads from SQS and sends data to given Discord Channel.

## Bot Configuration
### `.env` Values
|Key|Optional|Description|
|:- |:-:|:- |
|DISCORD_BOT_AUTH_KEY   |   ||
|IDENTIFIER             |yes||
|AWS_SQS_QUEUE_NAME     ||
|AWS_SQS_REGION         ||
|AWS_ACCESS_KEY_ID      ||
|AWS_SECRET_ACCESS_KEY  ||

## SQS Message Structure
### Message Body
#### **v0**
```json
{
    "Content": <string>,    // message content
    "Embed": {
        "Title": <string>,       // embed title
        "Description": <string>, // embed description
        "Image": <string>,       // image url
        "Color": <string>,       // Hex color of the embed without `#`
    }
}
```
#### **v1**
v1 accepts input with the same structure as [discordgo.MessageSend](https://github.com/bwmarrin/discordgo/blob/aa9af1488f6e4d39393bd4a5c85667f65f6bfad8/message.go#L228)
```json
{
  "Content": "some stuff with embed stuff",
  "Embeds": [
    {
      "Title": "embed title",
      "Description": "Hey go here [link](url)",
      "Image": {
        "URL": "some link to image"
      },
      "Color": "#002200"
    }
  ]
}

```

### Message Attributes
- `VERSION` : string<br/>
  The version that the message body is layed out with.
