package operationhttp

import (
	"context"
	"gochen/app/operation"
	"io"
	"net/http"
	"reflect"
	"time"

	"gochen/codec/jsoncodec"
	"gochen/errors"
)

// StreamOptions 控制标准 SSE 输出行为。
type StreamOptions struct {
	EventName         string
	DefaultRetryAfter time.Duration
	KeepAliveInterval time.Duration
}

// PrepareStreamHeaders 写入推荐的 SSE 响应头。
func PrepareStreamHeaders(header http.Header) {
	if header == nil {
		return
	}
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
}

// WriteSSEEvent 把 payload 以单个 SSE event 写出。
func WriteSSEEvent(writer io.Writer, event string, payload any) error {
	data, err := jsoncodec.Encode(payload)
	if err != nil {
		return errors.Wrap(err, errors.Internal, "failed to encode operation stream payload")
	}
	if event != "" {
		if _, err := writer.Write([]byte("event: " + event + "\n")); err != nil {
			return err
		}
	}
	if _, err := writer.Write([]byte("data: " + string(data) + "\n\n")); err != nil {
		return err
	}
	return nil
}

// Stream 复用统一的 tracked operation SSE 推送循环。
func Stream(
	ctx context.Context,
	writer http.ResponseWriter,
	flusher http.Flusher,
	loadCurrent func(context.Context) (*operation.Result, error),
	opts *StreamOptions,
) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "operation stream context is required")
	}
	if writer == nil {
		return errors.NewCode(errors.InvalidInput, "operation stream writer is required")
	}
	if flusher == nil {
		return errors.NewCode(errors.InvalidInput, "operation stream flusher is required")
	}
	if loadCurrent == nil {
		return errors.NewCode(errors.InvalidInput, "operation stream loader is required")
	}

	eventName := "operation"
	defaultRetry := 300 * time.Millisecond
	keepAlive := 15 * time.Second
	if opts != nil {
		if opts.EventName != "" {
			eventName = opts.EventName
		}
		if opts.DefaultRetryAfter > 0 {
			defaultRetry = opts.DefaultRetryAfter
		}
		if opts.KeepAliveInterval > 0 {
			keepAlive = opts.KeepAliveInterval
		}
	}

	current, err := loadCurrent(ctx)
	if err != nil {
		return err
	}
	if current == nil {
		return errors.NewCode(errors.InvalidInput, "operation stream loader returned nil result")
	}

	lastSentAt := time.Time{}
	var previous *operation.Result

	for {
		if previous == nil || !reflect.DeepEqual(previous, current) {
			if err := WriteSSEEvent(writer, eventName, current); err != nil {
				return err
			}
			flusher.Flush()
			lastSentAt = time.Now()
			previous = operation.CloneResult(current)
		} else if time.Since(lastSentAt) >= keepAlive {
			if _, err := writer.Write([]byte(": keepalive\n\n")); err != nil {
				return err
			}
			flusher.Flush()
			lastSentAt = time.Now()
		}

		if operation.IsTerminalStatus(current.Operation.Status) {
			return nil
		}

		delay := time.Duration(current.RetryAfterMs) * time.Millisecond
		if delay <= 0 {
			delay = defaultRetry
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		next, err := loadCurrent(ctx)
		if err != nil {
			return err
		}
		if next == nil {
			return errors.NewCode(errors.InvalidInput, "operation stream loader returned nil result")
		}
		current = next
	}
}
