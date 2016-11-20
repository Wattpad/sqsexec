package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/Wattpad/sqsconsumer"
	"github.com/Wattpad/sqsconsumer/middleware"
	"github.com/Wattpad/sqsconsumer/sqsmessage"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/metrics"
	"github.com/go-kit/kit/metrics/dogstatsd"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

var Version string = "UNKNOWN"

func main() {
	var (
		command               string
		queueName             string
		region                string
		datadogHostPort       string
		datadogMetricPrefix   string
		deleteAfterProcessing bool
		printVersion          bool
	)
	flag.StringVar(&command, "command", "", "Command to exec per message. Message body will be piped to this command's STDIN. Must be a single executable with no arguments.")
	flag.StringVar(&queueName, "queue", "", "SQS queue name to consume")
	flag.StringVar(&region, "region", "us-east-1", "SQS queue region")
	flag.StringVar(&datadogHostPort, "ddhost", "localhost:8125", "Host:Port for sending metrics to DataDog")
	flag.StringVar(&datadogMetricPrefix, "ddprefix", "", "Prefix for DataDog metric names")
	flag.BoolVar(&deleteAfterProcessing, "delete", true, "Delete S3 objects after processing")
	flag.BoolVar(&printVersion, "version", false, "Print the version and exit")
	flag.Parse()

	if printVersion {
		io.WriteString(os.Stdout, "Version: "+Version+"\n")
		os.Exit(0)
	}

	if command == "" || queueName == "" {
		io.WriteString(os.Stderr, "You must specify both command and queue\n")
		flag.Usage()
		os.Exit(1)
	}

	var logger log.Logger
	{
		logger = log.NewJSONLogger(log.NewSyncWriter(os.Stdout))
		logger = log.NewContext(logger).With("service", "sqsexec", "version", Version)
		logger.Log("msg", "service starting", "queue", queueName)
		defer logger.Log("msg", "service stopped")
	}

	var sqsSvc *sqsconsumer.SQSService
	{
		s, err := sqsconsumer.SQSServiceForQueue(queueName, sqsconsumer.OptAWSRegion(region))
		if err != nil {
			logger.Log("msg", "error creating SQS service", "err", err)
			os.Exit(1)
		}
		sqsSvc = s
	}

	var dd *dogstatsd.Dogstatsd
	{
		dd = dogstatsd.New(datadogMetricPrefix, log.NewContext(logger).With("subsystem", "datadog"))
		go dd.SendLoop(time.NewTicker(time.Second).C, "udp", datadogHostPort)

		routines := dd.NewGauge("runtime.goroutines")
		go func() {
			for range time.NewTicker(time.Second).C {
				routines.Set(float64(runtime.NumGoroutine()))
			}
		}()
	}

	w := &worker{
		Log:               logger,
		S3Client:          s3.New(session.New()),
		Cmd:               command,
		Delete:            deleteAfterProcessing,
		S3NotFoundCounter: dd.NewCounter("s3.message.not.found", 1),
	}

	// set up a context which will gracefully cancel the worker on interrupt
	var fetchCtx context.Context
	{
		ctx, cancel := context.WithCancel(context.Background())
		term := make(chan os.Signal, 1)
		signal.Notify(term, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-term
			logger.Log("msg", "Starting graceful shutdown")
			cancel()
		}()
		fetchCtx = ctx
	}

	// set up default middleware stack + timer
	var (
		stack        []middleware.MessageHandlerDecorator
		cancelDelete context.CancelFunc
	)
	{
		ctx, cancel := context.WithCancel(context.Background())
		stack = middleware.DefaultStack(ctx, sqsSvc)
		cancelDelete = cancel

		// track job execution time
		jobTime := dd.NewTiming("job.time", 1).With("version", Version)
		stack = append(stack, TrackJobTime(jobTime))
	}

	h := middleware.ApplyDecoratorsToHandler(w.HandleMessage, stack...)
	c := sqsconsumer.NewConsumer(sqsSvc, h)
	c.Run(fetchCtx)
	cancelDelete()
}

type worker struct {
	S3Client          *s3.S3
	Log               log.Logger
	Cmd               string
	Delete            bool
	S3NotFoundCounter metrics.Counter
}

type Notification struct {
	Records []NotificationRecord `json:"Records"`
}

type NotificationRecord struct {
	EventTime time.Time `json:"eventTime"`
	EventName string    `json:"eventName"`
	S3        struct {
		Bucket struct {
			Name string `json:"name"`
		} `json:"bucket"`
		Object struct {
			Key string `json:"key"`
		} `json:"object"`
	} `json:"s3"`
	ResponseElements struct {
		RequestID string `json:"x-amz-request-id"`
		ID2       string `json:"x-amz-id-2"`
	} `json:"responseElements"`
}

func (w *worker) HandleMessage(ctx context.Context, msg string) error {
	var n Notification
	err := json.Unmarshal([]byte(msg), &n)
	if err != nil {
		w.Log.Log("msg", "error unmarshalling notification", "err", err)
		return nil
	}

	for _, nr := range n.Records {
		err := w.HandleNotificationRecord(ctx, nr)
		if err != nil {
			return errors.Wrap(err, "error processing notification record")
		}
	}
	return nil
}

func (w *worker) HandleNotificationRecord(ctx context.Context, nr NotificationRecord) error {
	var mID string
	if msg, ok := sqsmessage.FromContext(ctx); ok {
		mID = aws.StringValue(msg.MessageId)
	}

	logger := log.NewContext(w.Log).With("bucket", nr.S3.Bucket.Name, "key", nr.S3.Object.Key, "s3_request_id", nr.ResponseElements.RequestID, "s3_id_2", nr.ResponseElements.ID2, "message_id", mID)

	req := &s3.GetObjectInput{
		Bucket: &nr.S3.Bucket.Name,
		Key:    &nr.S3.Object.Key,
	}
	resp, err := w.S3Client.GetObject(req)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok && aerr.Code() == "NoSuchKey" {
			w.S3NotFoundCounter.Add(1)
			logger.Log("msg", "error getting S3 object: does not exist", "err", err)

			// retrying this message is useless because the message is gone
			return nil
		}
		logger.Log("msg", "error getting S3 object", "err", err)
		return errors.Wrap(err, "error getting S3 object")
	}

	// read the s3 object so the s3 client can reuse the connection sooner
	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		logger.Log("msg", "error reading S3 object", "err", err)
		return errors.Wrap(err, "error reading S3 object")
	}

	br := bytes.NewReader(body)
	output, err := w.RunCommand(ctx, br)
	logger.Log("msg", "completed job", "output", output, "err", err)

	if err == nil && w.Delete {
		_, dErr := w.S3Client.DeleteObject(&s3.DeleteObjectInput{
			Bucket: &nr.S3.Bucket.Name,
			Key:    &nr.S3.Object.Key,
		})
		if dErr != nil {
			logger.Log("msg", "error deleting s3 object", "err", err)
		}
	}

	if err != nil {
		return errors.Wrap(err, "error running job")
	}
	return nil
}

func (w *worker) RunCommand(ctx context.Context, msg io.Reader) (string, error) {
	cmd := exec.CommandContext(ctx, w.Cmd)
	cmd.Stdin = msg
	res, err := cmd.CombinedOutput()
	return string(res), errors.Wrap(err, "error running command")
}

func TrackJobTime(hist metrics.Histogram) middleware.MessageHandlerDecorator {
	return func(fn sqsconsumer.MessageHandlerFunc) sqsconsumer.MessageHandlerFunc {
		return func(ctx context.Context, msg string) error {
			start := time.Now()

			err := fn(ctx, msg)

			success := "1"
			if err != nil {
				success = "0"
			}
			hist.With("success", success).Observe(time.Since(start).Seconds() * 1000)

			return err
		}
	}
}
