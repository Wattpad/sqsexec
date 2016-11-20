# SQS Background Job Executor

`sqsexec` is an application that uses an SQS queue and S3 bucket as a message queue for background job processing. It `exec`s a specified external program for each message received, piping the message body to STDIN.

Runtime metrics are reported to a DataDog agent:
- job.time with labels success:0/1 is execution time in ms

Error logs are written to STDOUT.

Job messages are expected to be stored in an S3 bucket, with S3 notifications enabled. The SQS queue should be a destination for the S3 notifications.

## Install

Fetch the latest release for linux or darwin (OS X) from https://github.com/Wattpad/sqsexec/releases/latest

## Setup

Create an S3 bucket and SQS queue. This program does not support S3 object versioning, so do not turn that on. Optionally set a bucket lifecycle policy to delete objects that are older than X days/weeks/months - pick a time window that is large enough that you are confident the message will no longer be in the SQS queue or that it is invalid if it is still in the queue.

Configure the S3 bucket to enable notifications, and set the SQS queue as the destination for those notifications.

Run `sqsexec` as a service on a node such that AWS credentials are available to the Go AWS library - this can be accomplished either by setting environment variables or (even better) using an IAM role for the host (assuming the node is an EC2 instance).

Ensure `sqsexec` is automatically restarted so that an unexpected crash does not kill the system: supervisord.org or equivalent, as you like.

Scale out the number of running `sqsexec` processes to control the level of job processing concurrency: every instance will run approximately 10 concurrent jobs, so depending on the resource requirements of the exec'd command, you can scale out replicas of `sqsexec` on the same host (eg, `numprocs` in the supervisor conf) or just run more hosts (an ASG driven by the SQS ApproximateNumberOfMessagesVisible may be a good choice if you want to keep queue depth under control).

The executed command will inherit the environment variables from `sqsexec`'s runtime environment, so you can pass config values to the external program by setting environment variables.

## Usage

Given the setup above yielded an SQS queue named `commands` and you have a script `save_command.sh` as follows:

```
#!/bin/bash

pre=file_${FILENAME_PREFIX}
cat > output/${pre}.$(date +%s).$RANDOM <&0
echo done
```

You can execute `sqsexec` as follows:

```
FILENAME_PREFIX=cmd ./sqsexec -command "${PWD}/save_command.sh" -queue commands -region us-east-1
```

Then every file PUT into the S3 bucket that notifies `commands` will be copied into a new file within a local `output` directory with a filename that starts with `test_cmd`.

## Releases

Travis should automatically build and release on new tags, so simply pushing a new tag to github is sufficient. Tags should be [ComVer](https://github.com/staltz/comver) versions formatted MAJOR.MINOR. Increment:

- MAJOR version when any backwards-incompatible change is made.
- MINOR version when any 100% backwards-compatible change is made.

`git tag 1.0 && git push --tags`
