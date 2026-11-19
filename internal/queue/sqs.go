//go:build !arl_nosqs

package queue

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// SQSCompiled reports whether the split AWS deployment is available.
const SQSCompiled = true

// newSQS returns a closure rather than a struct holding *sqs.Client: keeping
// the client type out of interface values lets the linker drop the SQS API
// methods the lab never calls.
func newSQS(ctx context.Context, url, region string) (Sender, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, errors.New("AWS configuration unavailable")
	}
	client := sqs.NewFromConfig(cfg)
	return SendFunc(func(ctx context.Context, bodies []string) ([]int, error) { return sendBatch(ctx, client, url, bodies) }), nil
}

func sendBatch(ctx context.Context, client *sqs.Client, url string, bodies []string) ([]int, error) {
	entries := []sqstypes.SendMessageBatchRequestEntry{}
	for i, body := range bodies {
		entries = append(entries, sqstypes.SendMessageBatchRequestEntry{Id: aws.String(fmt.Sprint(i)), MessageBody: aws.String(body)})
	}
	out, err := client.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{QueueUrl: aws.String(url), Entries: entries})
	if err != nil {
		return nil, err
	}
	sent := []int{}
	for _, ok := range out.Successful {
		var i int
		if _, e := fmt.Sscan(aws.ToString(ok.Id), &i); e == nil {
			sent = append(sent, i)
		}
	}
	return sent, nil
}
