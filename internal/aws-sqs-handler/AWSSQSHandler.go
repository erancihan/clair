package awssqshandler

import (
	"log"

	utils "github.com/erancihan/clair/internal/utils"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
)

type AWSSQSHandler struct {
	aws_sqs_queue_url *string
	svc               *sqs.SQS
}

var (
	AWS_ACCESS_KEY_ID     = ""
	AWS_SECRET_ACCESS_KEY = ""

	AWS_SQS_REGION           = ""
	AWS_SQS_QUEUE_NAME       = "" // the name of the queue
	AWS_SQS_TIMEOUT    int64 = 30 // how long the message is hidden from others in Seconds
)

func New() AWSSQSHandler {
	keyId := utils.GetEnv("AWS_ACCESS_KEY_ID", AWS_ACCESS_KEY_ID)
	secret := utils.GetEnv("AWS_SECRET_ACCESS_KEY", AWS_SECRET_ACCESS_KEY)
	region := utils.GetEnv("AWS_SQS_REGION", AWS_SQS_REGION)
	qName := utils.GetEnv("AWS_SQS_QUEUE_NAME", AWS_SQS_QUEUE_NAME)

	// Initialize a session that the SDK uses to load credentials
	sess := session.Must(session.NewSession(&aws.Config{
		Region:      aws.String(region),
		Credentials: credentials.NewStaticCredentials(keyId, secret, ""),
	}))

	// Create a service client and call the GetQueueUrl function to get the URL of the queue.
	svc := sqs.New(sess)
	urlResult, err := svc.GetQueueUrl(&sqs.GetQueueUrlInput{
		QueueName: aws.String(qName),
	})
	if err != nil {
		log.Panicf("Could not get URL Result from SVC\n %v\n", err)
	}

	handler := AWSSQSHandler{
		// The URL of the queue is in the QueueUrl property of the returned object.
		aws_sqs_queue_url: urlResult.QueueUrl,
		svc:               svc,
	}

	return handler
}

func (handler AWSSQSHandler) GetMessage() (*sqs.Message, error) {
	msgResult, err := handler.svc.ReceiveMessage(&sqs.ReceiveMessageInput{
		AttributeNames: []*string{
			aws.String(sqs.MessageSystemAttributeNameSentTimestamp),
		},
		MessageAttributeNames: []*string{
			aws.String(sqs.QueueAttributeNameAll),
		},
		QueueUrl:            handler.aws_sqs_queue_url,
		MaxNumberOfMessages: aws.Int64(1),
		VisibilityTimeout:   aws.Int64(AWS_SQS_TIMEOUT),
	})
	if err != nil {
		return nil, err
	}
	if len(msgResult.Messages) == 0 {
		return nil, nil
	}

	return msgResult.Messages[0], nil
}

func (handler AWSSQSHandler) DeleteMessage(receiptHandle *string) error {
	_, err := handler.svc.DeleteMessage(&sqs.DeleteMessageInput{
		QueueUrl:      handler.aws_sqs_queue_url,
		ReceiptHandle: receiptHandle,
	})

	return err
}
