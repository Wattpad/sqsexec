# SQS Background Job Executor

`sqsexec` is an application that uses an SQS queue and S3 bucket as a message queue for background job processing. It `exec`s a specified external program for each message received, piping the message body to STDIN after gunzipping the body if the S3 object has `Content-Encoding: gzip`.

Runtime metrics are reported to a DataDog agent:
- job.time with labels success:0/1 is execution time in ms

Error logs are written to STDOUT.
