package builtin

import (
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/zalando/skipper/filters"
)

type timeoutType int

const (
	requestTimeout timeoutType = iota + 1
	readTimeout
	writeTimeout
)

type timeout struct {
	typ     timeoutType
	timeout time.Duration
}

func NewBackendTimeout() filters.Spec {
	return &timeout{
		typ: requestTimeout,
	}
}

func NewReadTimeout() filters.Spec {
	return &timeout{
		typ: readTimeout,
	}
}

func NewWriteTimeout() filters.Spec {
	return &timeout{
		typ: writeTimeout,
	}
}

func (t *timeout) Name() string {
	switch t.typ {
	case requestTimeout:
		return filters.BackendTimeoutName
	case readTimeout:
		return filters.ReadTimeoutName
	case writeTimeout:
		return filters.WriteTimeoutName
	}
	return "unknownFilter"
}

func (t *timeout) CreateFilter(args []interface{}) (filters.Filter, error) {
	if len(args) != 1 {
		return nil, filters.ErrInvalidFilterParameters
	}

	var tf timeout
	tf.typ = t.typ
	switch v := args[0].(type) {
	case string:
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, err
		}
		tf.timeout = d
	case time.Duration:
		tf.timeout = v
	default:
		return nil, filters.ErrInvalidFilterParameters
	}
	return &tf, nil
}

// Request timeout allows overwrite.
//
// Type request timeout set the timeout for the backend roundtrip.
//
// Type read timeout sets the timeout to read the request including the body.
// It uses http.ResponseController to SetReadDeadline().
//
// Type write timeout allows to set a timeout for writing the response.
// It uses http.ResponseController to SetWriteDeadline().
func (t *timeout) Request(ctx filters.FilterContext) {
	switch t.typ {
	case requestTimeout:
		ctx.StateBag()[filters.BackendTimeout] = t.timeout
	case readTimeout:
		err := ctx.ResponseController().SetReadDeadline(time.Now().Add(t.timeout))
		if err != nil {
			log.Errorf("Failed to set read deadline: %v", err)
		}
	case writeTimeout:
		err := ctx.ResponseController().SetWriteDeadline(time.Now().Add(t.timeout))
		if err != nil {
			log.Errorf("Failed to set write deadline: %v", err)
		}
	}
}

func (*timeout) Response(filters.FilterContext) {}
