# SQS Background Job Executor

`sqsexec` is an application that uses an SQS queue and S3 bucket as a message queue for background job processing. It `exec`s a specified external program for each message received, piping the message body to STDIN.

Runtime metrics are reported to a DataDog agent:
- job.time with labels success:0/1 is execution time in ms

Error logs are written to STDOUT.

Job messages are expected to be stored in an S3 bucket, with S3 notifications enabled. The SQS queue should be a destination for the S3 notifications.
