package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/aws/aws-sdk-go/service/sqs"
)

var region = os.Getenv("AWS_REGION")

// SurveyPeriods arrays in JSON message
type SurveyPeriods struct {
	Survey string
	Period string
}

// InputJSON contains snapshot_id and array of surveyperiod combinations
type InputJSON struct {
	SnapshotID    string `json:"snapshot_id"`
	SurveyPeriods []SurveyPeriods
}

// OutputMessage to send to export-output queue
type OutputMessage struct {
	SnapshotID string `json:"snapshot_id"`
	Location   string `json:"location"`
	Successful bool   `json:"successful"`
}

func main() {

	lambda.Start(handle)

}

func handle(ctx context.Context, sqsEvent events.SQSEvent) {
	fmt.Println("Starting the application...")

	//fmt.Println("Event: ", sqsEvent.Records)

	for _, message := range sqsEvent.Records {
		fmt.Printf("The message %s for event source %s = %s \n", message.MessageId, message.EventSource, message.Body)
		queueMessage := message.Body
		cdbExport := make(chan string)
		go callGraphqlEndpoint(cdbExport, queueMessage)
		var wg sync.WaitGroup
		wg.Add(1)

		messageDetails := []byte(queueMessage)
		var messageJSON InputJSON
		parseError := json.Unmarshal(messageDetails, &messageJSON)
		if parseError != nil {
			fmt.Printf("Error with JSON from db-export-input queue: %v", parseError)
		}
		snapshotID := messageJSON.SnapshotID
		survey := messageJSON.SurveyPeriods[0].Survey
		period := messageJSON.SurveyPeriods[0].Period

		go saveToS3(cdbExport, &wg, survey, snapshotID, period)
		var bucketFilenamePrefix = "snapshot-"
		filename := strings.Join([]string{bucketFilenamePrefix, survey, period, snapshotID}, "-")
		sendToSqs(snapshotID, filename)
		wg.Wait()
	}
}

func callGraphqlEndpoint(cdbExport chan string, message string) {
	var gqlEndpoint = os.Getenv("GRAPHQL_ENDPOINT")
	fmt.Println("Going to access  Graphql Endpoint: ", gqlEndpoint)
	//response, err := http.Get(gqlEndpoint)
	response, err := http.Post(gqlEndpoint, "application/json; charset=UTF-8", strings.NewReader(message))
	fmt.Printf("Response from Business Layer: %s", response.Body)
	if err != nil {
		fmt.Printf("The HTTP request failed with error %s\n", err)
	} else {
		data, _ := ioutil.ReadAll(response.Body)
		cdbExport <- string(data)
		fmt.Println("Accessing Graphql Endpoint done")
	}
}

func saveToS3(cdbExport chan string, waitGroup *sync.WaitGroup, survey string, snapshotID string, period string) {

	dbExport := <-cdbExport
	var bucketFilenamePrefix = "snapshot-"

	//currentTime := time.Now().Format("2006-01-02-15:04:05")

	fmt.Printf("Region: %q\n", region)
	config := &aws.Config{
		Region: aws.String(region),
	}

	sess := session.New(config)

	uploader := s3manager.NewUploader(sess)

	bucket := os.Getenv("S3_BUCKET")
	filename := strings.Join([]string{bucketFilenamePrefix, survey, period, snapshotID}, "")
	fmt.Printf("Bucket filename: %q\n", filename)

	reader := strings.NewReader(string(dbExport))
	var err error
	_, err = uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(filename),
		Body:   reader,
	})

	if err != nil {
		fmt.Printf("Unable to upload %q to %q, %v", filename, bucket, err)
	}

	fmt.Printf("Successfully uploaded %q to s3 bucket %q\n", filename, bucket)
	waitGroup.Done()

}

func sendToSqs(snapshotid string, filename string) {

	queue := aws.String(os.Getenv("DB_EXPORT_OUTPUT_QUEUE"))

	fmt.Printf("Region: %q\n", region)
	config := &aws.Config{
		Region: aws.String(region),
	}

	sess := session.New(config)

	svc := sqs.New(sess)

	urlResult, err := svc.GetQueueUrl(&sqs.GetQueueUrlInput{
		QueueName: queue,
	})

	if err != nil {
		fmt.Printf("Unable to find DB Export input queue %q, %q", *queue, err)
	}

	queueURL := urlResult.QueueUrl

	outputMessage := &OutputMessage{
		SnapshotID: snapshotid,
		Location:   filename,
		Successful: true,
	}

	DataToSend, err := json.Marshal(outputMessage)
	if err != nil {
		fmt.Printf("An error occured while marshaling DatToSend: %s", err)
	}
	fmt.Printf("DataToSend %v\n", string(DataToSend))

	_, error := svc.SendMessage(&sqs.SendMessageInput{
		MessageBody: aws.String(string(DataToSend)),
		QueueUrl:    queueURL,
	})

	if error != nil {
		fmt.Printf("Unable to send to DB Export output queue %q, %q", *queue, error)
	}

}
