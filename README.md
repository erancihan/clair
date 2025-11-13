# Clair - Yet Another Discord Bot Project
Discord Bot written in GoLang. Reads from SQS and sends data to given Discord Channel.

## Installation
### Prerequisites
- GoLang
- AWS Account
- Discord Bot Token

### Steps
1. Clone the repository
2. Run `go mod tidy` to install dependencies
3. Run `go mod vendor` to create a vendor directory
4. Create a `.env` file in the root directory of the project
5. Fill in the values in the `.env` file

## Usage
### Running the Bot
- Run bot Server
  ```bash
  go run cmd/clair/main.go server
  ```

- Run bot Scheduler (for now, it just sends a message to the SQS queue)<br>
  This is also the worker
  ```bash
  go run cmd/clair/main.go scheduler
  ```

### Building the Bot
- Build the bot<br>
  This will create a binary in the `make-build-release` directory called `clair.bin`. This binary can be run on any system that has the same architecture as the one it was built on.
  Similar to usage, the binary can be run with the same commands.
  ```bash
  make build
  ```

### With Docker Compose
You can build the bot locally with Docker.
```bash
make docker
```

System has both a server and a scheduler. To run both, you can populate your `docker-compose.yaml` file with the following:
```yaml
services:
  clair-scheduler:
    image: ghcr.io/erancihan/clair:latest
    environment:
      DISCORD_BOT_AUTH_KEY: <discord_bot_token>
      DISCORD_BOT_IDENTIFIER: <discord_bot_identifier>
      AWS_SQS_QUEUE_NAME: <aws_sqs_queue_name>
      AWS_SQS_REGION: <aws_sqs_region>
      AWS_ACCESS_KEY_ID: <aws_access_key_id>
      AWS_SECRET_ACCESS_KEY: <aws_secret_access_key>
    command: ./clair.bin scheduler
  
  clair-server:
    image: ghcr.io/erancihan/clair:latest
    depends_on:
      - valkey
    ports:
      - "4000:4000"
    volumes:
      - ./database/folder/path:/opt/clair/database
    env_file:
      - .env
    environment:
      - NODE_ENV=production
      - DB_FOLDER=/opt/clair/database
    command: ./clair.bin server
```

Scheduler handles the SQS queue and sends messages to the Discord channel.
Server... WIP

Or use the docker compose file provided in the repository.
```bash
docker compose up
```

## Bot Configuration
### `.env` Values
|Key                    |Optional|Value|Description|
|:-                     |:-:     |:-   |:-         |
|DISCORD_BOT_AUTH_KEY   |        |     |           |
|DISCORD_BOT_IDENTIFIER |yes     |     |           |
|AWS_SQS_QUEUE_NAME     |        |     |           |
|AWS_SQS_REGION         |        |     |           |
|AWS_ACCESS_KEY_ID      |        |     |           |
|AWS_SECRET_ACCESS_KEY  |        |     |           |

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
  The version that the message body is layed out with. Default is `0`

## Road Map
So that I won't get lost.
- [ ] Register Flow
- [ ] Login Flow
- [ ] Passwords????
- [ ] Basic user home page
- [ ] Discord OAuth
- [ ] Clair Admin `/slash` commands
- [ ] Clair Webhooks
  - [ ] GET Webhook for a Discord Channel
    - [ ] YouTube-like ID system to map webhook to discord channel???
  - [ ] Webhook to Discord
  - [ ] Webhook preview for Discord
