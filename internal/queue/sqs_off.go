//go:build arl_nosqs

package queue

import (
	"context"
	"errors"
)

// SQSCompiled reports whether the split AWS deployment is available.
const SQSCompiled = false

func newSQS(context.Context, string, string) (Sender, error) {
	return nil, errors.New("ARL_SQS_QUEUE_URL is set but this binary was built with -tags arl_nosqs; rebuild without it or unset the queue URL")
}
